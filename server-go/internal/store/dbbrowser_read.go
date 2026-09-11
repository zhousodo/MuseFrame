package store

import (
	"context"
	"fmt"
	"time"
)

// TablePage 是 GET /v1/admin/db/table/{name} 的出参。
// 🔴 rows 是**二维数组**不是对象数组（与 Node 版一致），列序 = information_schema 的 ordinal。
type TablePage struct {
	Columns []string `json:"columns"`
	Rows    [][]any  `json:"rows"`
	Total   int64    `json:"total"`
}

// TableColumns 按 ordinal_position 取列名。
func TableColumns(ctx context.Context, q Queryer, table string) ([]string, error) {
	rows, err := q.Query(ctx,
		`SELECT column_name FROM information_schema.columns
		 WHERE table_schema = 'public' AND table_name = $1 ORDER BY ordinal_position`, table)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var c string
		if err := rows.Scan(&c); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// ReadTablePage 读一页并逐单元格脱敏。
// 只接受白名单里的表；表名来自白名单常量，不是用户输入拼接。
func ReadTablePage(ctx context.Context, q Queryer, table string, limit, offset int, isSecretKey func(string) bool) (*TablePage, error) {
	if !IsBrowsableTable(table) {
		return nil, fmt.Errorf("表不可浏览")
	}
	cols, err := TableColumns(ctx, q, table)
	if err != nil {
		return nil, err
	}
	var total int64
	if err := q.QueryRow(ctx, fmt.Sprintf(`SELECT count(*) FROM %q`, table)).Scan(&total); err != nil {
		return nil, err
	}
	// 显式固定行序，否则 PG 的堆表顺序会随 UPDATE 漂，分页会重复/漏行。
	order := cols[0]
	for _, c := range cols {
		if c == "id" {
			order = "id"
			break
		}
	}
	rows, err := q.Query(ctx,
		fmt.Sprintf(`SELECT * FROM %q ORDER BY %q LIMIT $1 OFFSET $2`, table, order), limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	keyIdx := -1
	for i, c := range cols {
		if c == "key" {
			keyIdx = i
			break
		}
	}
	page := &TablePage{Columns: cols, Rows: [][]any{}, Total: total}
	for rows.Next() {
		vals, err := rows.Values()
		if err != nil {
			return nil, err
		}
		secretRow := false
		if table == "app_config" && keyIdx >= 0 && keyIdx < len(vals) {
			if k, ok := vals[keyIdx].(string); ok && isSecretKey != nil {
				secretRow = isSecretKey(k)
			}
		}
		out := make([]any, len(cols))
		for i := range cols {
			if i < len(vals) {
				out[i] = MaskCell(table, cols[i], vals[i], secretRow)
			}
		}
		page.Rows = append(page.Rows, out)
	}
	return page, rows.Err()
}

// QueryResult 是 POST /v1/admin/db/query 的出参。
type QueryResult struct {
	Columns   []string `json:"columns"`
	Rows      [][]any  `json:"rows"`
	RowCount  int      `json:"rowCount"`
	Truncated bool     `json:"truncated"`
}

// RunReadOnlyQuery 在只读事务里跑一条 SELECT/WITH。
// 硬行数上限在 SQL 内部生效（流水线 LIMIT 会提前停，而不是先物化再切片）。
func (s *Store) RunReadOnlyQuery(ctx context.Context, sql string) (*QueryResult, error) {
	out := &QueryResult{Columns: []string{}, Rows: [][]any{}}
	err := s.ReadOnlyTx(ctx, func(q Queryer) error {
		rows, err := q.Query(ctx, `SELECT * FROM (`+sql+`) AS mf_console LIMIT 501`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for _, fd := range rows.FieldDescriptions() {
			out.Columns = append(out.Columns, string(fd.Name))
		}
		for rows.Next() {
			vals, err := rows.Values()
			if err != nil {
				return err
			}
			// 与 /db/table 同一条时区归一：绝不让本地时区偏移进出参。
			for i, v := range vals {
				if t, ok := v.(time.Time); ok {
					vals[i] = ISO(t)
				}
			}
			out.Rows = append(out.Rows, vals)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	if len(out.Rows) > 500 {
		out.Rows = out.Rows[:500]
		out.Truncated = true
	}
	out.RowCount = len(out.Rows)
	return out, nil
}
