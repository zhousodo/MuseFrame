package store

import "time"

// TimeRange 是后台列表 / CSV 导出统一的「从 … 到 …」时间筛选。
//
// 🔴 为什么要有它：此前只有任务表有「时间窗」（而且是「最近 N 小时」这种相对窗），
// 购买 / 反馈 / 资产 / 用户 / 埋点的 CSV 都只能整段导出。运营要「对一下上个月的账」
// 就只能把全量拉下来再在表格里删行 —— 而那份 CSV 的行数上限是 5000，
// 一旦被截断，删到最后得到的是一份**静悄悄少了几天**的账。
//
// From / To 都是**UTC 时刻**（半开区间 [From, To)）。前端传进来的可能是
// 「2026-09-01」这样的北京日期，换算在 httpapi 那一层做，store 只认时刻。
type TimeRange struct {
	From *time.Time
	To   *time.Time
}

// Empty 表示没有任何时间筛选。
func (t TimeRange) Empty() bool { return t.From == nil && t.To == nil }

// apply 把半开区间拼进 WHERE。col 必须是调用方写死的列名（绝不接受外部输入）。
func (t TimeRange) apply(col string, sql string, args *[]any) string {
	if t.From != nil {
		*args = append(*args, *t.From)
		sql += ` AND ` + col + ` >= $` + itoa(len(*args))
	}
	if t.To != nil {
		*args = append(*args, *t.To)
		sql += ` AND ` + col + ` < $` + itoa(len(*args))
	}
	return sql
}

// Apply 是 apply 的导出版本，给 store 包外的调用方（目前没有）与测试用。
func (t TimeRange) Apply(col string, sql string, args *[]any) string {
	return t.apply(col, sql, args)
}
