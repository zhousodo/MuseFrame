package httpapi

import (
	"net/url"
	"strings"
	"testing"
	"time"

	"museframe-api/internal/store"
)

// 🔴 from/to 按**北京时间**解释。后台每一处展示都写着 (UTC+8)，运营看着
// 09-01 的行去填 from=2026-09-01 指的就是北京时间那一天；按 UTC 解释会把
// 窗口整体前移 8 小时 —— 差的那 8 小时里通常正好是一天中最忙的晚上。
func TestParseAdminTimeUsesBeijingDay(t *testing.T) {
	from, err := parseAdminTime("2026-09-01", false)
	if err != nil {
		t.Fatalf("from 解析失败：%v", err)
	}
	if want := "2026-08-31T16:00:00Z"; from.UTC().Format(time.RFC3339) != want {
		t.Errorf("from=2026-09-01 应当是北京时间当天 00:00（%s），实际 %s", want, from.UTC().Format(time.RFC3339))
	}
	// to 是**半开区间的右端**：to=2026-09-01 要含 9 月 1 日一整天。
	to, err := parseAdminTime("2026-09-01", true)
	if err != nil {
		t.Fatalf("to 解析失败：%v", err)
	}
	if want := "2026-09-01T16:00:00Z"; to.UTC().Format(time.RFC3339) != want {
		t.Errorf("to=2026-09-01 应当是次日 00:00（%s），实际 %s", want, to.UTC().Format(time.RFC3339))
	}
	// 带时区的 RFC3339 按它自己的时区算，不再套 +08。
	rf, err := parseAdminTime("2026-09-01T12:30:00Z", false)
	if err != nil {
		t.Fatalf("RFC3339 解析失败：%v", err)
	}
	if rf.UTC().Format(time.RFC3339) != "2026-09-01T12:30:00Z" {
		t.Errorf("RFC3339 被二次换算了：%s", rf.UTC().Format(time.RFC3339))
	}
	// 空值 = 不筛。
	if v, err := parseAdminTime("  ", false); err != nil || v != nil {
		t.Errorf("空值应当表示不筛，得到 %v / %v", v, err)
	}
	// 拼错的格式必须报错。悄悄当成「不筛」会让运营拿到一份全量 CSV
	// 却以为它只含自己要的那几天。
	for _, bad := range []string{"2026/09/01", "09-01", "昨天", "2026-13-45"} {
		if _, err := parseAdminTime(bad, false); err == nil {
			t.Errorf("%q 应当被拒绝", bad)
		}
	}
}

func TestParseTimeRangeRejectsInverted(t *testing.T) {
	q := url.Values{"from": {"2026-09-05"}, "to": {"2026-09-01"}}
	if _, err := parseTimeRange(q); err == nil {
		t.Fatal("颠倒的区间必须回 422，不能悄悄返回空结果")
	}
	// 同一天的 from/to 是合法的（to 会被推到次日 00:00）。
	q = url.Values{"from": {"2026-09-01"}, "to": {"2026-09-01"}}
	tr, err := parseTimeRange(q)
	if err != nil {
		t.Fatalf("同一天应当合法：%v", err)
	}
	if tr.From == nil || tr.To == nil || !tr.To.After(*tr.From) {
		t.Fatal("同一天的区间应当是 [当天 00:00, 次日 00:00)")
	}
	if tr.Empty() {
		t.Fatal("填了 from/to 却判成空区间")
	}
	// 都不填 = 不筛。
	if tr, err := parseTimeRange(url.Values{}); err != nil || !tr.Empty() {
		t.Fatalf("都不填应当是空区间：%v / %v", tr, err)
	}
}

// 生效的区间必须摊在视图说明里 ——「我到底筛的是哪一段」要能在页面上自证。
func TestTimeRangeNoteIsBeijingTime(t *testing.T) {
	tr, err := parseTimeRange(url.Values{"from": {"2026-09-01"}, "to": {"2026-09-02"}})
	if err != nil {
		t.Fatal(err)
	}
	note := timeRangeNote(tr)
	for _, want := range []string{"2026-09-01 00:00", "2026-09-03 00:00", "UTC+8"} {
		if !strings.Contains(note, want) {
			t.Errorf("说明里缺少 %q：%s", want, note)
		}
	}
	if timeRangeNote(store.TimeRange{}) != "" {
		t.Error("没有筛选时不该追加说明")
	}
}
