// PostgreSQL 持久层。pgx/v5 连接池 + 事务助手 + app_config 后端。
//
// 时区红线：库里全是 timestamptz，出参一律 UTC ISO-8601 带毫秒与 Z
// （2026-09-11T04:26:12.396Z）。MuseFrame 是 UTC，**不是** paida 的 UTC+8，
// 两边迁移与查询模板绝不能共用。
package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// TimeFormat 是全部出参的时间格式。
const TimeFormat = "2006-01-02T15:04:05.000Z"

// ISO 把时刻格式化成出参用的 UTC ISO-8601 字符串。
func ISO(t time.Time) string { return t.UTC().Format(TimeFormat) }

// ISOPtr 把可空时刻格式化成 *string（nil 在 JSON 里是 null）。
func ISOPtr(t *time.Time) *string {
	if t == nil {
		return nil
	}
	s := ISO(*t)
	return &s
}

// Queryer 是 pgxpool.Pool 与 pgx.Tx 的公共子集，让同一份查询既能裸跑也能进事务。
type Queryer interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Exec(ctx context.Context, sql string, args ...any) (pgconnCommandTag, error)
}

type pgconnCommandTag = interface {
	RowsAffected() int64
	String() string
}

// Store 持有连接池。
type Store struct {
	pool *pgxpool.Pool
}

// Open 建池。连接串永远不进日志、不进错误文本。
func Open(ctx context.Context, dsn string, maxConns int32) (*Store, error) {
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, errors.New("MUSEFRAME_DATABASE_URL 不是合法的连接串")
	}
	if maxConns > 0 {
		cfg.MaxConns = maxConns
	}
	cfg.MaxConnLifetime = 30 * time.Minute
	cfg.MaxConnIdleTime = 5 * time.Minute
	cfg.HealthCheckPeriod = time.Minute
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, errors.New("无法创建 PostgreSQL 连接池")
	}
	return &Store{pool: pool}, nil
}

// Close 关池。
func (s *Store) Close() {
	if s.pool != nil {
		s.pool.Close()
	}
}

// Pool 暴露底层池（仅测试与迁移工具用）。
func (s *Store) Pool() *pgxpool.Pool { return s.pool }

// Ready 是只读探活：SELECT 1，绝不写库。
// （paida 的老实现每次探活写一次 PRAGMA user_version，是零流量库长出 4MB WAL 的根因；
//
//	这里从一开始就不留这个口子。）
func (s *Store) Ready(ctx context.Context) bool {
	var one int
	if err := s.pool.QueryRow(ctx, "SELECT 1").Scan(&one); err != nil {
		return false
	}
	return one == 1
}

// HealthProbe 复刻 /v1/health 的 DB 探测：一次轻量读 + 一次业务表读。
// SQLite 版是 PRAGMA quick_check(1) + SELECT COUNT(*) FROM app_config；
// PG 侧对应 SELECT 1 + SELECT count(*) FROM app_config。两者都不写库。
func (s *Store) HealthProbe(ctx context.Context) error {
	var one int
	if err := s.pool.QueryRow(ctx, "SELECT 1").Scan(&one); err != nil {
		return err
	}
	var n int64
	return s.pool.QueryRow(ctx, "SELECT count(*) FROM app_config").Scan(&n)
}

// Q 返回可直接用于查询的对象（非事务）。
func (s *Store) Q() Queryer { return poolQ{s.pool} }

type poolQ struct{ p *pgxpool.Pool }

func (q poolQ) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	return q.p.Query(ctx, sql, args...)
}
func (q poolQ) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	return q.p.QueryRow(ctx, sql, args...)
}
func (q poolQ) Exec(ctx context.Context, sql string, args ...any) (pgconnCommandTag, error) {
	return q.p.Exec(ctx, sql, args...)
}

type txQ struct{ tx pgx.Tx }

func (q txQ) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	return q.tx.Query(ctx, sql, args...)
}
func (q txQ) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	return q.tx.QueryRow(ctx, sql, args...)
}
func (q txQ) Exec(ctx context.Context, sql string, args ...any) (pgconnCommandTag, error) {
	return q.tx.Exec(ctx, sql, args...)
}

// InTx 在一个事务里跑 fn。任何错误一律回滚。
//
// 这是「建任务 + 预留额度 + 改项目状态必须同一事务」的落点：Node 版曾经分两次
// 提交，留下「行已建、预留失败」的白嫖任务。Go 版没有再入事务的概念，
// 所有需要原子性的写路径都显式接收同一个 Queryer。
func (s *Store) InTx(ctx context.Context, fn func(q Queryer) error) error {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := fn(txQ{tx}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// ReadOnlyTx 在一个显式只读事务里跑 fn —— 给管理后台的 SQL 控制台用。
// 即使连接角色被误配成可写，SET TRANSACTION READ ONLY 也会拦住写操作（双保险）。
func (s *Store) ReadOnlyTx(ctx context.Context, fn func(q Queryer) error) error {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly, IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	return fn(txQ{tx})
}

// ---- cfgstore.Backend 实现 --------------------------------------------------

// LoadAppConfig 读全部运行时覆盖项。
func (s *Store) LoadAppConfig(ctx context.Context) (map[string]string, error) {
	rows, err := s.pool.Query(ctx, "SELECT key, value FROM app_config")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var k string
		var v *string
		if err := rows.Scan(&k, &v); err != nil {
			return nil, err
		}
		if v != nil {
			out[k] = *v
		}
	}
	return out, rows.Err()
}

// UpsertAppConfig 写一个覆盖项。
func (s *Store) UpsertAppConfig(ctx context.Context, key, value string) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO app_config (key, value, updated_at) VALUES ($1,$2,$3)
		 ON CONFLICT (key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`,
		key, value, time.Now().UTC())
	return err
}

// DeleteAppConfig 清一个覆盖项。
func (s *Store) DeleteAppConfig(ctx context.Context, key string) error {
	_, err := s.pool.Exec(ctx, "DELETE FROM app_config WHERE key = $1", key)
	return err
}

// ErrNoRows 转发 pgx 的 no rows 哨兵，调用方不必直接依赖 pgx。
var ErrNoRows = pgx.ErrNoRows

// IsNoRows 判断错误是否为「查不到」。
func IsNoRows(err error) bool { return errors.Is(err, pgx.ErrNoRows) }

// Wrap 把底层错误包一层但**不带连接串**。
func Wrap(op string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%s 失败: %w", op, err)
}
