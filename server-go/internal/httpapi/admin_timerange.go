package httpapi

import (
	"strings"
	"time"

	"museframe-api/internal/apierr"
	"museframe-api/internal/store"
)

// 后台列表 / CSV 导出统一的 ?from= / ?to= 时间筛选。
//
// 🔴 一半的 CSV 此前根本没有时间筛选（只有任务表有「最近 N 小时」）。运营要
// 「对上个月的账」只能整段导出再在表格里删行 —— 而导出有 5000 行上限，
// 一旦被截断，删到最后得到的是一份静悄悄少了几天的账。
//
// 🔴 输入按**北京时间**解释，不是 UTC：后台每一处展示都写着 (UTC+8)，
// 运营看着 09-01 的行去填 from=2026-09-01，指的当然是北京时间的 9 月 1 日。
// 按 UTC 解释会让筛出来的一天比看到的那一天早 8 小时 —— 差的那 8 小时里
// 通常正好是一天里最忙的晚上。
//
// 支持两种写法：
//
//	2026-09-01             北京时间当天 00:00（to 则是当天结束，即次日 00:00，闭区间体验）
//	2026-09-01T12:30:00Z   RFC3339，带时区就按它自己的时区算，不再套 +08
const adminTZOffsetHours = 8

var adminTZ = time.FixedZone("UTC+8", adminTZOffsetHours*3600)

// parseAdminTime 解析一个 from/to 值。isTo 为真时，纯日期表示「那一天结束」。
func parseAdminTime(raw string, isTo bool) (*time.Time, error) {
	raw = trimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	if t, err := time.Parse(time.RFC3339, raw); err == nil {
		u := t.UTC()
		return &u, nil
	}
	if d, err := time.ParseInLocation("2006-01-02", raw, adminTZ); err == nil {
		if isTo {
			// 半开区间：to=2026-09-01 意味着「含 9 月 1 日一整天」，
			// 也就是 < 9 月 2 日 00:00（北京时间）。写成闭区间会漏掉
			// 当天 00:00:00.xxx 之后的所有行，而运营以为自己筛的是整天。
			d = d.AddDate(0, 0, 1)
		}
		u := d.UTC()
		return &u, nil
	}
	return nil, apierr.New(422, apierr.CodeValidation,
		"时间格式只支持 2026-09-01（北京时间当天）或 RFC3339（如 2026-09-01T12:30:00Z）。")
}

// parseTimeRange 读 ?from= / ?to=，并拦住颠倒的区间。
func parseTimeRange(q urlValues) (store.TimeRange, error) {
	from, err := parseAdminTime(q.Get("from"), false)
	if err != nil {
		return store.TimeRange{}, err
	}
	to, err := parseAdminTime(q.Get("to"), true)
	if err != nil {
		return store.TimeRange{}, err
	}
	if from != nil && to != nil && !to.After(*from) {
		// 悄悄换个空结果回去等于让运营以为「那几天真的什么都没发生」。
		return store.TimeRange{}, apierr.New(422, apierr.CodeValidation, "结束时间必须晚于开始时间。")
	}
	return store.TimeRange{From: from, To: to}, nil
}

// timeRangeNote 把生效的区间摊成一句人话，附在视图说明后面 ——
// 「我到底筛的是哪一段」必须能在页面上自证，而不是靠运营记得自己填了什么。
func timeRangeNote(tr store.TimeRange) string {
	if tr.Empty() {
		return ""
	}
	fmtBJ := func(t *time.Time) string {
		if t == nil {
			return "不限"
		}
		return t.In(adminTZ).Format("2006-01-02 15:04")
	}
	return "　当前时间筛选：" + fmtBJ(tr.From) + " ≤ 时间 < " + fmtBJ(tr.To) + "（北京时间 UTC+8）。"
}

var _ = strings.TrimSpace
