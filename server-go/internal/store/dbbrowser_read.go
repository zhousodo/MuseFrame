package store

import (
	"context"
	"fmt"
	"strings"
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
	//
	// 🔴 排序键必须是**全序**。早先这里是「有 id 就按 id，否则按 cols[0]」，
	// 而 cols[0] 只有在第一列恰好唯一时才是全序。白名单 23 张表里有两张不满足：
	// exhibition_styles 第一列是 exhibition_id，idempotency_records 第一列是
	// user_id —— 两者都能重复。相同键的那一组行在 PG 里顺序是未定义的，于是翻页
	// （纯 OFFSET 算术）会让同一行在第 N 页和第 N+1 页重复出现，或者整行漏掉。
	// 没有 id 列时退化成「按全部列排序」：全部列相同的行彼此无法区分，
	// 谁前谁后不影响分页正确性，所以这就够了。
	orderCols := pageOrderColumns(cols)
	quoted := make([]string, len(orderCols))
	for i, c := range orderCols {
		quoted[i] = fmt.Sprintf("%q", c)
	}
	rows, err := q.Query(ctx,
		fmt.Sprintf(`SELECT * FROM %q ORDER BY %s LIMIT $1 OFFSET $2`,
			table, strings.Join(quoted, ", ")), limit, offset)
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

// pageOrderColumns 决定分页的 ORDER BY 列表，要求结果是**全序**。
//
// 有 id 列就按 id（唯一，天然全序）。没有 id 列时按**全部列**排序：
// 全部列都相同的两行彼此无法区分，谁前谁后不影响分页正确性。
//
// 🔴 这里以前是「没有 id 就按 cols[0]」，而 cols[0] 只在第一列恰好唯一时才是全序。
// 白名单 23 张表里有两张不满足：exhibition_styles 第一列是 exhibition_id，
// idempotency_records 第一列是 user_id，两者都能重复。并列键那一组行的相对顺序
// 在 PG 里是未定义的 —— 同样的数据换一个执行计划（小 LIMIT 走 top-N heapsort、
// 大 offset 走 quicksort）或者翻页期间有并发写，就可能让某一行在第 N 页和
// 第 N+1 页各出现一次、另一行整个漏掉。后台是纯 OFFSET 算术翻页，没有任何补偿。
func pageOrderColumns(cols []string) []string {
	for _, c := range cols {
		if c == "id" {
			return []string{"id"}
		}
	}
	return cols
}
