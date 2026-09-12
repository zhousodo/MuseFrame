package aigc

import (
	"bytes"
	"encoding/json"
	"image"
	"image/jpeg"
	"strings"
	"testing"
	"time"
)

// testEncode 是注入给 Apply 的编码器（生产里注入的是 imaging.EncodeJPEG）。
func testEncode(img *Image, q int) ([]byte, error) {
	m := image.NewRGBA(image.Rect(0, 0, img.Width, img.Height))
	copy(m.Pix, img.Data)
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, m, &jpeg.Options{Quality: q}); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// grayImage 造一张中灰图。中灰而不是纯黑/纯白：水印是「白字 + 深色影子」，
// 在纯黑或纯白底上只有一层看得见，像素差分的断言会变得似是而非。
func grayImage(w, h int) *Image {
	d := make([]byte, w*h*4)
	for i := 0; i < len(d); i += 4 {
		d[i], d[i+1], d[i+2], d[i+3] = 128, 128, 128, 255
	}
	return &Image{Data: d, Width: w, Height: h}
}

func defaultOpts() Options {
	return Options{
		LabelEnabled:  true,
		LabelText:     "AI 生成 · 留影",
		LabelPosition: PosBottomRight,
		LabelOpacity:  0.85,
		LabelSizePct:  3.2,
		Producer:      "留影 MuseFrame（lenscript.cn）",
		ProductName:   "MuseFrame",
		ProduceID:     "job_test_0001",
		Now:           time.Date(2026, 9, 12, 12, 34, 56, 0, time.UTC),
	}
}

// inkCount 数「被改过的像素」。底图是均匀中灰，所以任何非 128 的像素都是水印。
func inkCount(img *Image) int {
	n := 0
	for i := 0; i < len(img.Data); i += 4 {
		if img.Data[i] != 128 || img.Data[i+1] != 128 || img.Data[i+2] != 128 {
			n++
		}
	}
	return n
}

// quadrantInk 数四个象限各自的水印像素数。
func quadrantInk(img *Image) (tl, tr, bl, br int) {
	for y := 0; y < img.Height; y++ {
		for x := 0; x < img.Width; x++ {
			i := (y*img.Width + x) * 4
			if img.Data[i] == 128 && img.Data[i+1] == 128 && img.Data[i+2] == 128 {
				continue
			}
			switch {
			case y < img.Height/2 && x < img.Width/2:
				tl++
			case y < img.Height/2:
				tr++
			case x < img.Width/2:
				bl++
			default:
				br++
			}
		}
	}
	return
}

// 🔴 变异测试 1：把 Apply 里的 `if o.LabelEnabled` 改成 `if true`
// （或者把它整条删掉），这个用例立刻红。
//
// 它守的不是代码风格，是一个**有法律与产品双重后果**的开关：运营把
// aigc_label_enabled 关掉之后，成品上必须一个水印像素都没有。
func TestLabelDisabledDoesNotTouchAnyPixel(t *testing.T) {
	o := defaultOpts()
	o.LabelEnabled = false
	img := grayImage(600, 400)
	res, err := Apply(img, 90, o, testEncode)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if n := inkCount(img); n != 0 {
		t.Fatalf("开关关闭却改了 %d 个像素；水印必须一个点都不画", n)
	}
	if res.Mark != MarkMeta {
		t.Fatalf("mark = %q，想要 %q（关掉显式标识后只剩隐式）", res.Mark, MarkMeta)
	}
	// 但隐式标识**照写不误** —— 它是办法第五条的「应当」，没有开关。
	got, err := ExtractJPEG(res.JPEG)
	if err != nil {
		t.Fatalf("ExtractJPEG: %v", err)
	}
	if got.Label.Label != "1" {
		t.Fatalf("关掉显式水印后隐式标识也没了：%+v", got.Label)
	}
}

func TestLabelEnabledDrawsInk(t *testing.T) {
	img := grayImage(600, 400)
	res, err := Apply(img, 90, defaultOpts(), testEncode)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if n := inkCount(img); n < 200 {
		t.Fatalf("水印只画了 %d 个像素，看起来根本没画上", n)
	}
	if res.Mark != MarkVisibleMeta {
		t.Fatalf("mark = %q，想要 %q", res.Mark, MarkVisibleMeta)
	}
}

// 🔴 变异测试 2：从 AIGCLabel 里删掉任意一个字段、或把 json tag 从
// "ContentProducer" 改成 "contentProducer"，这个用例立刻红。
//
// GB 45438-2025 附录 A 的字段名是**核验方按字面读的**。改一个字母
// 不会有任何编译错误、不会有任何运行时报错，只会让标识在平台侧变成不存在。
func TestMetadataCarriesEveryRequiredField(t *testing.T) {
	o := defaultOpts()
	img := grayImage(600, 400)
	res, err := Apply(img, 90, o, testEncode)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	got, err := ExtractJPEG(res.JPEG)
	if err != nil {
		t.Fatalf("ExtractJPEG: %v", err)
	}

	// (a) EXIF UserComment 里的 JSON 必须带齐 6 个标准键，一个都不能少。
	var envelope map[string]any
	if err := json.Unmarshal([]byte(got.UserCommentJSON), &envelope); err != nil {
		t.Fatalf("UserComment 不是合法 JSON：%v（%q）", err, got.UserCommentJSON)
	}
	inner, ok := envelope["AIGC"].(map[string]any)
	if !ok {
		t.Fatalf("UserComment 里没有 AIGC 外壳：%q", got.UserCommentJSON)
	}
	for _, k := range []string{"Label", "ContentProducer", "ProduceID",
		"ReservedCode1", "ContentPropagator", "PropagateID"} {
		if _, ok := inner[k]; !ok {
			t.Errorf("隐式标识缺字段 %s（GB 45438-2025 附录A）", k)
		}
	}
	if inner["Label"] != "1" {
		t.Errorf("Label = %v，想要 \"1\"（人工智能生成合成内容）", inner["Label"])
	}
	if inner["ContentProducer"] != o.Producer {
		t.Errorf("ContentProducer = %v，想要 %q", inner["ContentProducer"], o.Producer)
	}
	if inner["ProduceID"] != o.ProduceID {
		t.Errorf("ProduceID = %v，想要 %q", inner["ProduceID"], o.ProduceID)
	}
	// UserComment 必须是纯 ASCII（中文走 \uXXXX 转义），否则一半看图软件显示乱码。
	for i := 0; i < len(got.UserCommentJSON); i++ {
		if got.UserCommentJSON[i] > 0x7E {
			t.Fatalf("UserComment 里出现了非 ASCII 字节（位置 %d）", i)
		}
	}

	// (b) XMP 必须带齐同一组字段 + IPTC 的 DigitalSourceType。
	for _, want := range []string{
		`aigc:Label="1"`,
		`aigc:ContentProducer="` + o.Producer + `"`,
		`aigc:ProduceID="` + o.ProduceID + `"`,
		`aigc:ReservedCode1=`,
		`aigc:ContentPropagator=`,
		`aigc:PropagateID=`,
		`aigc:ContentHash="sha256:` + res.SHA256 + `"`,
		`aigc:VisibleWatermark="true"`,
		"trainedAlgorithmicMedia",
		XMPNamespace,
		"本图片由人工智能生成合成",
	} {
		if !strings.Contains(got.XMP, want) {
			t.Errorf("XMP 里缺 %q", want)
		}
	}
	// XMP 是 UTF-8 的，中文必须原样保留（这正是它和 EXIF 分工的理由）。
	if !strings.Contains(got.XMP, o.Producer) {
		t.Errorf("XMP 没有原样保留中文制作方名称")
	}
}

// 隐式标识不能把 JPEG 写坏：加完元数据还得解得开，且像素一个不差。
func TestEmbedKeepsJPEGDecodable(t *testing.T) {
	img := grayImage(320, 240)
	res, err := Apply(img, 90, defaultOpts(), testEncode)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	before, err := jpeg.Decode(bytes.NewReader(res.JPEG))
	if err != nil {
		t.Fatalf("带标识的 JPEG 解不开：%v", err)
	}
	if b := before.Bounds(); b.Dx() != 320 || b.Dy() != 240 {
		t.Fatalf("尺寸被改了：%v", b)
	}
	// image/jpeg 不写 JFIF APP0（第一段就是 DQT），所以这里 APP1 紧跟 SOI。
	if !bytes.HasPrefix(res.JPEG[2:], []byte{0xFF, 0xE1}) {
		t.Fatalf("APP1 没有紧跟 SOI：% x", res.JPEG[:8])
	}
	if !bytes.Contains(res.JPEG, []byte(exifHeader)) {
		t.Fatalf("没有 EXIF APP1 段")
	}
	if !bytes.Contains(res.JPEG, []byte("http://ns.adobe.com/xap/1.0/")) {
		t.Fatalf("没有 XMP APP1 段")
	}
}

// 🔴 别人的 JPEG（上游直出、或将来换了编码器）可能带 JFIF APP0，
// 而 JFIF 规范要求 APP0 紧跟 SOI。插到它前面，部分老解析器会把整个文件
// 判为损坏 —— 表现是「后端加了标识之后，某些相册打不开图了」。
func TestInsertPointSkipsJFIFAPP0(t *testing.T) {
	// SOI + APP0(len=16, "JFIF\0"+11 字节) + DQT 开头
	app0 := []byte{0xFF, 0xE0, 0x00, 0x10, 'J', 'F', 'I', 'F', 0,
		1, 1, 0, 0, 1, 0, 1, 0, 0}
	jpg := append([]byte{0xFF, 0xD8}, app0...)
	jpg = append(jpg, 0xFF, 0xDB, 0x00, 0x04, 0x00, 0x00)
	if got, want := insertPoint(jpg), 2+len(app0); got != want {
		t.Fatalf("insertPoint = %d，想要 %d（APP0 之后）", got, want)
	}
	out, err := EmbedJPEG(jpg, Meta{ProductName: "MuseFrame", At: time.Now().UTC()})
	if err != nil {
		t.Fatalf("EmbedJPEG: %v", err)
	}
	if !bytes.HasPrefix(out[2:], app0) {
		t.Fatalf("APP0 不再紧跟 SOI：% x", out[:12])
	}
}

func TestPositionPutsInkInTheRightCorner(t *testing.T) {
	cases := []struct {
		pos  string
		want string // 期望落墨最多的象限
	}{
		{PosBottomRight, "br"}, {PosBottomLeft, "bl"},
		{PosTopRight, "tr"}, {PosTopLeft, "tl"},
	}
	for _, c := range cases {
		o := defaultOpts()
		o.LabelPosition = c.pos
		img := grayImage(800, 600)
		if err := DrawLabel(img, o); err != nil {
			t.Fatalf("%s: DrawLabel: %v", c.pos, err)
		}
		tl, tr, bl, br := quadrantInk(img)
		got := map[string]int{"tl": tl, "tr": tr, "bl": bl, "br": br}
		best, bestN := "", -1
		for k, v := range got {
			if v > bestN {
				best, bestN = k, v
			}
		}
		if best != c.want {
			t.Errorf("position=%s 落墨最多的象限是 %s（%v），想要 %s", c.pos, best, got, c.want)
		}
	}
}

func TestUnknownPositionFallsBackToBottomRight(t *testing.T) {
	o := defaultOpts()
	o.LabelPosition = "somewhere-else"
	img := grayImage(800, 600)
	if err := DrawLabel(img, o); err != nil {
		t.Fatalf("DrawLabel: %v", err)
	}
	_, _, _, br := quadrantInk(img)
	if br < 100 {
		t.Fatalf("未知位置没有回落到右下角（br=%d）", br)
	}
}

func TestOpacityChangesInkStrength(t *testing.T) {
	strength := func(a float64) float64 {
		o := defaultOpts()
		o.LabelOpacity = a
		img := grayImage(600, 400)
		if err := DrawLabel(img, o); err != nil {
			t.Fatalf("DrawLabel: %v", err)
		}
		var sum float64
		for i := 0; i < len(img.Data); i += 4 {
			d := float64(img.Data[i]) - 128
			if d < 0 {
				d = -d
			}
			sum += d
		}
		return sum
	}
	low, high := strength(0.2), strength(1.0)
	if !(high > low*1.5) {
		t.Fatalf("不透明度没生效：0.2 -> %.0f，1.0 -> %.0f", low, high)
	}
}

// 字号跟着图像短边走：小图上的水印不能大到糊住画面，大图上的不能小到看不见。
func TestFontSizeScalesWithShortSide(t *testing.T) {
	small, big := fontPx(400, 300, 3.2), fontPx(4000, 3000, 3.2)
	if !(big > small) {
		t.Fatalf("字号没随尺寸变化：small=%d big=%d", small, big)
	}
	if got := fontPx(80, 60, 3.2); got < 12 {
		t.Fatalf("小图字号 %d 低于 12px 下限（那就等于没有标识）", got)
	}
	if got := fontPx(100, 100, 99); got > 25 {
		t.Fatalf("百分比填得离谱时字号 %d 没被夹住", got)
	}
}

// 🔴 字体是子集。没有这道校验，一句含生僻字的文案会被存进注册表，
// 而线上每张图的水印里那个字是空白 —— 没人会发现。
func TestUnsupportedRunesDetectsGlyphsOutsideTheSubset(t *testing.T) {
	if bad := UnsupportedRunes("AI 生成 · 留影 MuseFrame 2026"); len(bad) > 0 {
		t.Fatalf("默认文案里的字反而不支持：%q", string(bad))
	}
	// 韩文/emoji 不在 GB2312 子集里。
	bad := UnsupportedRunes("AI 생성 🎨")
	if len(bad) == 0 {
		t.Fatalf("子集外的字符没被检出")
	}
	if err := DrawLabel(grayImage(600, 400), Options{
		LabelEnabled: true, LabelText: "AI 생성", LabelOpacity: 0.8, LabelSizePct: 3.2,
	}); err == nil {
		t.Fatalf("画不出来的文案应该报错，而不是画出一排空白")
	}
}

func TestApplyRejectsEmptyImage(t *testing.T) {
	if _, err := Apply(nil, 90, defaultOpts(), testEncode); err != ErrNoImage {
		t.Fatalf("err = %v，想要 ErrNoImage", err)
	}
	if _, err := Apply(&Image{Width: 10, Height: 10, Data: []byte{1, 2, 3}}, 90, defaultOpts(), testEncode); err != ErrNoImage {
		t.Fatalf("像素不够时没有报 ErrNoImage")
	}
}

// 空文案不等于「没开关」：它应该被当成没有显式标识，而不是画一个空水印。
func TestEmptyTextCountsAsNoVisibleLabel(t *testing.T) {
	o := defaultOpts()
	o.LabelText = "   "
	img := grayImage(400, 300)
	res, err := Apply(img, 90, o, testEncode)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if res.Mark != MarkMeta || inkCount(img) != 0 {
		t.Fatalf("空文案却画了水印：mark=%s ink=%d", res.Mark, inkCount(img))
	}
}

func TestExtractRejectsNonJPEG(t *testing.T) {
	if _, err := ExtractJPEG([]byte("not a jpeg")); err != ErrNotJPEG {
		t.Fatalf("err = %v，想要 ErrNotJPEG", err)
	}
	if _, err := EmbedJPEG([]byte{0x89, 0x50, 0, 0}, Meta{}); err != ErrNotJPEG {
		t.Fatalf("PNG 字节应该被拒")
	}
}
