package aigc

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Meta 是一次隐式标识的内容。
type Meta struct {
	Producer    string    // 内容制作服务提供者（ContentProducer）
	ProductName string    // 产品名，进 EXIF Software / XMP CreatorTool
	ProduceID   string    // 内容制作编号（ProduceID），用生成任务 id
	ContentHash string    // 成品字节的 sha256（写元数据之前算的）
	At          time.Time // 制作时间（UTC）
	Visible     bool      // 这张图上是否**同时**画了显式水印
	LabelText   string    // 显式水印文案（没画就是空）
}

// AIGCLabel 是 GB 45438-2025《网络安全技术 人工智能生成合成内容标识方法》
// 附录 A 规定的、写进文件元数据的「生成合成标识」结构。
//
// 🔴 字段名与大小写**按标准逐字对齐**，不要改成 Go 风格的驼峰或下划线：
// 平台侧核验是按字段名读的，改一个字母就等于没有标识。
//
//	Label             "1" = 人工智能生成合成内容（"2" = 人工智能辅助）
//	ContentProducer   内容制作服务提供者的名称或编码
//	ProduceID         内容制作编号
//	ReservedCode1     预留字段
//	ContentPropagator 内容传播服务提供者（我们是制作方不是传播方，留空）
//	PropagateID       内容传播编号（同上，留空）
type AIGCLabel struct {
	Label             string `json:"Label"`
	ContentProducer   string `json:"ContentProducer"`
	ProduceID         string `json:"ProduceID"`
	ReservedCode1     string `json:"ReservedCode1"`
	ContentPropagator string `json:"ContentPropagator"`
	PropagateID       string `json:"PropagateID"`
}

// aigcEnvelope 是 UserComment / XMP 里那层 {"AIGC": {...}} 外壳。
type aigcEnvelope struct {
	AIGC AIGCLabel `json:"AIGC"`
	// 以下三项是标准之外我们自己加的追溯信息，放在 AIGC 同级、不污染标准字段。
	ProduceTime string `json:"ProduceTime,omitempty"`
	ContentHash string `json:"ContentHash,omitempty"`
	VisibleMark string `json:"VisibleMark,omitempty"`
}

// XMPNamespace 是隐式标识用的 XMP 命名空间。
const XMPNamespace = "http://www.tc260.org.cn/ns/aigc/1.0/"

// xmpPacketHeader 是 XMP APP1 段的标识串（末尾那个 \x00 属于它，别删）。
const xmpPacketHeader = "http://ns.adobe.com/xap/1.0/\x00"

// exifHeader 是 EXIF APP1 段的标识串。
const exifHeader = "Exif\x00\x00"

// Label 构造标准结构。
func (m Meta) Label() AIGCLabel {
	producer := strings.TrimSpace(m.Producer)
	if producer == "" {
		producer = strings.TrimSpace(m.ProductName)
	}
	return AIGCLabel{
		Label:             "1",
		ContentProducer:   producer,
		ProduceID:         strings.TrimSpace(m.ProduceID),
		ReservedCode1:     "",
		ContentPropagator: "",
		PropagateID:       "",
	}
}

func (m Meta) envelope() aigcEnvelope {
	mark := "none"
	if m.Visible {
		mark = "watermark"
	}
	return aigcEnvelope{
		AIGC:        m.Label(),
		ProduceTime: m.At.UTC().Format(time.RFC3339),
		ContentHash: "sha256:" + m.ContentHash,
		VisibleMark: mark,
	}
}

// JSON 是写进 EXIF UserComment 与 XMP 的那一串。
func (m Meta) JSON() string {
	b, err := json.Marshal(m.envelope())
	if err != nil {
		return "{}"
	}
	return string(b)
}

// ErrNotJPEG 表示字节不是 JPEG。
var ErrNotJPEG = errors.New("aigc: 不是 JPEG")

// EmbedJPEG 在 JPEG 里写入隐式标识：一段 EXIF APP1 + 一段 XMP APP1。
//
// 🔴 为什么两段都写：EXIF 是相册、微信、大部分看图软件会读的地方；
// XMP 是内容溯源工具链（C2PA/IPTC 生态）会读的地方，而且它是 UTF-8 的，
// 中文文案能原样保留。少写任一段，都会有一整类核验方读不到标识。
func EmbedJPEG(jpg []byte, m Meta) ([]byte, error) {
	if len(jpg) < 4 || jpg[0] != 0xFF || jpg[1] != 0xD8 {
		return nil, ErrNotJPEG
	}
	exifSeg, err := app1Segment(exifHeader, buildTIFF(m))
	if err != nil {
		return nil, err
	}
	xmpSeg, err := app1Segment(xmpPacketHeader, []byte(buildXMP(m)))
	if err != nil {
		return nil, err
	}
	at := insertPoint(jpg)
	out := make([]byte, 0, len(jpg)+len(exifSeg)+len(xmpSeg))
	out = append(out, jpg[:at]...)
	out = append(out, exifSeg...)
	out = append(out, xmpSeg...)
	out = append(out, jpg[at:]...)
	return out, nil
}

// insertPoint 返回插入 APP1 的字节位置：SOI 之后，但要跳过紧跟其后的 APP0(JFIF)。
//
// 🔴 image/jpeg 编码出来的第一段就是 APP0 JFIF。把 APP1 插到它**前面**在
// JFIF 规范里是不合法的（JFIF APP0 必须紧跟 SOI），部分老解析器会因此
// 把整个文件判为损坏 —— 表现是「后端加了标识之后，某些相册打不开图了」。
func insertPoint(jpg []byte) int {
	i := 2
	for i+4 <= len(jpg) && jpg[i] == 0xFF && jpg[i+1] == 0xE0 {
		size := int(binary.BigEndian.Uint16(jpg[i+2 : i+4]))
		if size < 2 || i+2+size > len(jpg) {
			break
		}
		i += 2 + size
	}
	return i
}

// app1Segment 组一段 APP1。
func app1Segment(header string, payload []byte) ([]byte, error) {
	size := 2 + len(header) + len(payload)
	if size > 0xFFFF {
		return nil, fmt.Errorf("aigc: APP1 段超长（%d 字节）", size)
	}
	out := make([]byte, 0, 2+size)
	out = append(out, 0xFF, 0xE1)
	out = binary.BigEndian.AppendUint16(out, uint16(size))
	out = append(out, header...)
	out = append(out, payload...)
	return out, nil
}

// ---- EXIF（TIFF）写入 -------------------------------------------------------

const (
	tagImageDescription = 0x010E
	tagSoftware         = 0x0131
	tagDateTime         = 0x0132
	tagExifIFD          = 0x8769
	tagExifVersion      = 0x9000
	tagDateTimeOriginal = 0x9003
	tagUserComment      = 0x9286

	typASCII     = 2
	typLong      = 4
	typUndefined = 7
)

type ifdEntry struct {
	tag, typ uint16
	count    uint32
	val      []byte // 原始值字节（≤4 字节内联，否则进数据区）
}

func asciiEntry(tag uint16, s string) ifdEntry {
	b := append([]byte(asciiOnly(s)), 0)
	return ifdEntry{tag: tag, typ: typASCII, count: uint32(len(b)), val: b}
}

func undefEntry(tag uint16, b []byte) ifdEntry {
	return ifdEntry{tag: tag, typ: typUndefined, count: uint32(len(b)), val: b}
}

// asciiOnly 把非 ASCII 字符换成 '?'。
//
// 🔴 EXIF 的 ASCII 类型按规范就只能放 ASCII，中文塞进去在不同看图软件里
// 会解成不同的乱码。所以中文一律走 XMP（UTF-8）和 UserComment 里的
// \uXXXX 转义 JSON，EXIF 的 ASCII 字段只放英文 —— 宁可信息少一点，
// 也不要一个在半数软件里显示成乱码的「标识」。
func asciiOnly(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r == 0:
			// 跳过：ASCII 字段以 NUL 结尾，值里再有 NUL 会把字符串截断。
		case r < 0x20 || r > 0x7E:
			b.WriteByte('?')
		default:
			b.WriteRune(r)
		}
	}
	return strings.TrimSpace(b.String())
}

// asciiJSON 把 JSON 里的非 ASCII 字符转成 \uXXXX，结果是纯 ASCII 且仍是合法 JSON。
func asciiJSON(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r < 0x80 {
			b.WriteRune(r)
			continue
		}
		if r > 0xFFFF {
			r -= 0x10000
			fmt.Fprintf(&b, "\\u%04x\\u%04x", 0xD800+(r>>10), 0xDC00+(r&0x3FF))
			continue
		}
		fmt.Fprintf(&b, "\\u%04x", r)
	}
	return b.String()
}

// buildTIFF 生成 EXIF 的 TIFF 块（大端）。
func buildTIFF(m Meta) []byte {
	ts := m.At.UTC().Format("2006:01:02 15:04:05")
	// UserComment 的前 8 字节是字符集标识。用 ASCII 而不是 UNICODE：
	// 正文已经是 \uXXXX 转义过的纯 ASCII JSON，UTF-16 只会让更少的工具读得懂。
	uc := append([]byte("ASCII\x00\x00\x00"), []byte(asciiJSON(m.JSON()))...)

	ifd0 := []ifdEntry{
		asciiEntry(tagImageDescription, "AI-generated content (AIGC); see XMP/UserComment for the full label"),
		asciiEntry(tagSoftware, m.ProductName),
		asciiEntry(tagDateTime, ts),
	}
	exifIFD := []ifdEntry{
		undefEntry(tagExifVersion, []byte("0232")),
		asciiEntry(tagDateTimeOriginal, ts),
		undefEntry(tagUserComment, uc),
	}

	const headerLen = 8
	ifd0Len := 2 + 12*(len(ifd0)+1) + 4 // +1 = ExifIFD 指针
	exifLen := 2 + 12*len(exifIFD) + 4
	exifOff := headerLen + ifd0Len
	dataOff := exifOff + exifLen

	out := make([]byte, 0, dataOff+256)
	out = append(out, 'M', 'M', 0x00, 0x2A)
	out = binary.BigEndian.AppendUint32(out, headerLen)

	var data []byte
	writeIFD := func(entries []ifdEntry, extra *ifdEntry) {
		n := len(entries)
		if extra != nil {
			n++
		}
		out = binary.BigEndian.AppendUint16(out, uint16(n))
		emit := func(e ifdEntry) {
			out = binary.BigEndian.AppendUint16(out, e.tag)
			out = binary.BigEndian.AppendUint16(out, e.typ)
			out = binary.BigEndian.AppendUint32(out, e.count)
			if len(e.val) <= 4 {
				v := make([]byte, 4)
				copy(v, e.val) // ≤4 字节的值**左对齐**内联，右边补零
				out = append(out, v...)
				return
			}
			out = binary.BigEndian.AppendUint32(out, uint32(dataOff+len(data)))
			data = append(data, e.val...)
			if len(data)%2 == 1 { // TIFF 要求值偏移为偶数
				data = append(data, 0)
			}
		}
		for _, e := range entries {
			emit(e)
		}
		if extra != nil {
			emit(*extra)
		}
		out = binary.BigEndian.AppendUint32(out, 0) // 没有下一个 IFD
	}

	ptr := ifdEntry{tag: tagExifIFD, typ: typLong, count: 1,
		val: binary.BigEndian.AppendUint32(nil, uint32(exifOff))}
	writeIFD(ifd0, &ptr)
	writeIFD(exifIFD, nil)
	return append(out, data...)
}

// ---- XMP -------------------------------------------------------------------

// buildXMP 生成 XMP 包。除了标准的 AIGC 字段，还写一条
// Iptc4xmpExt:DigitalSourceType = trainedAlgorithmicMedia —— 那是 IPTC
// 给「完全由 AI 生成」定的取值，境外平台（含各家相册与社交产品）读的是它。
func buildXMP(m Meta) string {
	l := m.Label()
	esc := xmlEscape
	visible := "false"
	if m.Visible {
		visible = "true"
	}
	return `<?xpacket begin="` + "\uFEFF" + `" id="W5M0MpCehiHzreSzNTczkc9d"?>` + "\n" +
		`<x:xmpmeta xmlns:x="adobe:ns:meta/">` + "\n" +
		` <rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#">` + "\n" +
		`  <rdf:Description rdf:about=""` + "\n" +
		`    xmlns:xmp="http://ns.adobe.com/xap/1.0/"` + "\n" +
		`    xmlns:dc="http://purl.org/dc/elements/1.1/"` + "\n" +
		`    xmlns:Iptc4xmpExt="http://iptc.org/std/Iptc4xmpExt/2008-02-29/"` + "\n" +
		`    xmlns:aigc="` + XMPNamespace + `"` + "\n" +
		`    xmp:CreatorTool="` + esc(m.ProductName) + `"` + "\n" +
		`    xmp:CreateDate="` + m.At.UTC().Format(time.RFC3339) + `"` + "\n" +
		`    Iptc4xmpExt:DigitalSourceType="http://cv.iptc.org/newscodes/digitalsourcetype/trainedAlgorithmicMedia"` + "\n" +
		`    aigc:Label="` + esc(l.Label) + `"` + "\n" +
		`    aigc:ContentProducer="` + esc(l.ContentProducer) + `"` + "\n" +
		`    aigc:ProduceID="` + esc(l.ProduceID) + `"` + "\n" +
		`    aigc:ReservedCode1="` + esc(l.ReservedCode1) + `"` + "\n" +
		`    aigc:ContentPropagator="` + esc(l.ContentPropagator) + `"` + "\n" +
		`    aigc:PropagateID="` + esc(l.PropagateID) + `"` + "\n" +
		`    aigc:ProduceTime="` + m.At.UTC().Format(time.RFC3339) + `"` + "\n" +
		`    aigc:ContentHash="sha256:` + esc(m.ContentHash) + `"` + "\n" +
		`    aigc:VisibleWatermark="` + visible + `"` + "\n" +
		`    aigc:VisibleWatermarkText="` + esc(m.LabelText) + `">` + "\n" +
		`   <dc:description><rdf:Alt><rdf:li xml:lang="x-default">` +
		esc(aigcStatement(m)) + `</rdf:li></rdf:Alt></dc:description>` + "\n" +
		`  </rdf:Description>` + "\n" +
		` </rdf:RDF>` + "\n" +
		`</x:xmpmeta>` + "\n" +
		`<?xpacket end="w"?>`
}

// aigcStatement 是给人读的那句话（进 dc:description）。
func aigcStatement(m Meta) string {
	who := strings.TrimSpace(m.Producer)
	if who == "" {
		who = strings.TrimSpace(m.ProductName)
	}
	return "本图片由人工智能生成合成，生成服务提供者：" + who +
		"。This image was generated by an AI service (" + who + ")."
}

func xmlEscape(s string) string {
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;", "'", "&apos;")
	return r.Replace(s)
}

// ---- 读回（核验 / 测试用）---------------------------------------------------

// Extracted 是从一张 JPEG 里读回的隐式标识。
type Extracted struct {
	// UserCommentJSON 是 EXIF UserComment 里那串 JSON（已去掉 8 字节字符集前缀）。
	UserCommentJSON string
	// XMP 是 XMP 包正文。
	XMP string
	// Label 是解析出来的标准结构。
	Label AIGCLabel
}

// ExtractJPEG 从 JPEG 里读回隐式标识。给核验脚本与测试用。
func ExtractJPEG(jpg []byte) (Extracted, error) {
	var out Extracted
	if len(jpg) < 4 || jpg[0] != 0xFF || jpg[1] != 0xD8 {
		return out, ErrNotJPEG
	}
	i := 2
	for i+4 <= len(jpg) {
		if jpg[i] != 0xFF {
			break
		}
		marker := jpg[i+1]
		if marker == 0xD8 || marker == 0x01 || (marker >= 0xD0 && marker <= 0xD7) {
			i += 2
			continue
		}
		if marker == 0xDA || marker == 0xD9 { // SOS 之后是压缩数据，不再有 APP 段
			break
		}
		size := int(binary.BigEndian.Uint16(jpg[i+2 : i+4]))
		if size < 2 || i+2+size > len(jpg) {
			break
		}
		body := jpg[i+4 : i+2+size]
		if marker == 0xE1 {
			switch {
			case bytes.HasPrefix(body, []byte(exifHeader)):
				if s, ok := userCommentOf(body[len(exifHeader):]); ok {
					out.UserCommentJSON = s
				}
			case bytes.HasPrefix(body, []byte(xmpPacketHeader)):
				out.XMP = string(body[len(xmpPacketHeader):])
			}
		}
		i += 2 + size
	}
	if out.UserCommentJSON != "" {
		var env aigcEnvelope
		if err := json.Unmarshal([]byte(out.UserCommentJSON), &env); err == nil {
			out.Label = env.AIGC
		}
	}
	return out, nil
}

// userCommentOf 从 TIFF 块里找 ExifIFD 的 UserComment。只支持我们自己写的大端布局。
func userCommentOf(tiff []byte) (string, bool) {
	if len(tiff) < 8 || tiff[0] != 'M' || tiff[1] != 'M' {
		return "", false
	}
	read := func(off int) (entries int, ok bool) {
		if off+2 > len(tiff) {
			return 0, false
		}
		return int(binary.BigEndian.Uint16(tiff[off : off+2])), true
	}
	walk := func(off int) (exifOff int, comment string) {
		n, ok := read(off)
		if !ok {
			return 0, ""
		}
		for k := 0; k < n; k++ {
			e := off + 2 + k*12
			if e+12 > len(tiff) {
				break
			}
			tag := binary.BigEndian.Uint16(tiff[e : e+2])
			count := int(binary.BigEndian.Uint32(tiff[e+4 : e+8]))
			raw := tiff[e+8 : e+12]
			switch tag {
			case tagExifIFD:
				exifOff = int(binary.BigEndian.Uint32(raw))
			case tagUserComment:
				if count <= 4 {
					break
				}
				vo := int(binary.BigEndian.Uint32(raw))
				if vo < 0 || vo+count > len(tiff) || count < 8 {
					break
				}
				comment = strings.TrimRight(string(tiff[vo+8:vo+count]), "\x00")
			}
		}
		return exifOff, comment
	}
	exifOff, comment := walk(int(binary.BigEndian.Uint32(tiff[4:8])))
	if comment != "" {
		return comment, true
	}
	if exifOff > 0 && exifOff < len(tiff) {
		if _, c := walk(exifOff); c != "" {
			return c, true
		}
	}
	return "", false
}
