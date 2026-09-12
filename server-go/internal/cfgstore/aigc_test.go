package cfgstore

import (
	"context"
	"strings"
	"testing"
	"time"
)

// 🔴 变异测试：把 Set() 里那行 checkLabelText 删掉，这个用例立刻红。
//
// 它守的是一个**看不见**的故障：字体是 ASCII + GB2312 子集，子集外的字
// 只会画成一片空白。没有写校验的话后台显示「已保存」，而此后每一张成品的
// 法定显式标识都缺了半句话，唯一的发现方式是下载成品逐字看。
func TestSetRejectsLabelTextTheFontCannotDraw(t *testing.T) {
	s := NewForTest(nil)
	ctx := context.Background()
	for _, bad := range []string{"AI 생성", "AI 生成 🎨", "AI 生成 📷 留影"} {
		if err := s.Set(ctx, "aigc_label_text", bad); err == nil {
			t.Errorf("%q 应该被拒（字体画不出来），却存进去了", bad)
		} else if !strings.Contains(err.Error(), "画不出来") {
			t.Errorf("%q 的报错没说清原因：%v", bad, err)
		}
	}
	// 常用简体中文 + ASCII 必须放行，否则这道校验就把正常运营也挡死了。
	for _, ok := range []string{"AI 生成", "AI 生成 · 留影", "本图由人工智能生成", "AI-generated"} {
		if err := s.Set(ctx, "aigc_label_text", ok); err != nil {
			t.Errorf("%q 是常用文案，不该被拒：%v", ok, err)
		}
	}
}

// 空文案 ≠ 关标识。把文案清空会让后台显示「标识开着」而实际什么都不画 ——
// 这比直接关掉更危险，因为它连自己的审计都骗过去了。
func TestSetRejectsEmptyLabelText(t *testing.T) {
	s := NewForTest(nil)
	if err := s.Set(context.Background(), "aigc_label_text", "   "); err == nil {
		t.Fatal("空文案应该被拒")
	}
}

func TestSetRejectsOverlongLabelText(t *testing.T) {
	s := NewForTest(nil)
	long := strings.Repeat("生成", MaxLabelTextRunes/2+1)
	if err := s.Set(context.Background(), "aigc_label_text", long); err == nil {
		t.Fatalf("超过 %d 字的文案应该被拒（会横穿整张画面）", MaxLabelTextRunes)
	}
}

// 🔴 不透明度下界必须挡住 0：一个完全透明的水印会让成品被标成
// 「已加显式标识」而肉眼什么都看不到。
func TestSetRejectsInvisibleOpacity(t *testing.T) {
	s := NewForTest(nil)
	ctx := context.Background()
	for _, bad := range []float64{0, 0.01, 1.5, -1} {
		if err := s.Set(ctx, "aigc_label_opacity", bad); err == nil {
			t.Errorf("不透明度 %v 应该被拒", bad)
		}
	}
	if err := s.Set(ctx, "aigc_label_opacity", 0.6); err != nil {
		t.Fatalf("0.6 是合法值：%v", err)
	}
}

func TestSetRejectsUnknownLabelPosition(t *testing.T) {
	s := NewForTest(nil)
	ctx := context.Background()
	if err := s.Set(ctx, "aigc_label_position", "middle"); err == nil {
		t.Fatal("未知位置应该被拒，而不是存进去再在渲染时悄悄回落右下角")
	}
	if err := s.Set(ctx, "aigc_label_position", "top-left"); err != nil {
		t.Fatalf("top-left 是合法位置：%v", err)
	}
}

// AIGCOptions 是 worker 每次生成现读现取的那一份参数。默认值必须是
// 「开着 + 有文案 + 有制作方」—— 一个默认关闭的合规标识等于没做。
func TestAIGCOptionsDefaultsAreCompliant(t *testing.T) {
	s := NewForTest(nil)
	o := s.AIGCOptions("job_x", time.Date(2026, 9, 12, 0, 0, 0, 0, time.UTC))
	if !o.LabelEnabled {
		t.Error("显式标识默认必须是开的")
	}
	if strings.TrimSpace(o.LabelText) == "" {
		t.Error("默认文案不能为空")
	}
	if strings.TrimSpace(o.Producer) == "" {
		t.Error("默认 ContentProducer 不能为空（核验方靠它判断这张图出自谁家）")
	}
	if o.ProduceID != "job_x" || o.ProductName != AIGCProductName {
		t.Errorf("ProduceID/ProductName 没透传：%+v", o)
	}
	if o.LabelOpacity <= 0 || o.LabelSizePct <= 0 {
		t.Errorf("不透明度/字号默认值不合理：%+v", o)
	}
}

// 热改必须立刻对下一个任务生效（和 max_job_attempts 一样是现读现取）。
func TestAIGCOptionsReflectHotChanges(t *testing.T) {
	s := NewForTest(nil)
	ctx := context.Background()
	if err := s.Set(ctx, "aigc_label_enabled", false); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if err := s.Set(ctx, "aigc_label_position", "top-left"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	o := s.AIGCOptions("job_y", time.Now())
	if o.LabelEnabled {
		t.Error("关掉之后 AIGCOptions 还说是开的")
	}
	if o.LabelPosition != "top-left" {
		t.Errorf("位置没生效：%q", o.LabelPosition)
	}
}
