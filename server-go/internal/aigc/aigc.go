// Package aigc 实现《人工智能生成合成内容标识办法》（2025-09-01 施行）要求的
// 两类标识，作用在**每一张新产出的成品**上：
//
//	显式标识（办法第四条）—— 画在像素上的角标文字（默认「AI 生成 · 留影」）。
//	  它是给人看的，所以可以被运营调位置/字号/透明度，也可以整体关掉
//	  （关掉是个有法律后果的动作，注册表那一项的描述里写明了）。
//
//	隐式标识（办法第五条 + GB 45438-2025 附录A）—— 写进文件元数据的结构化字段。
//	  它是给机器看的（平台侧核验、传播链路溯源），**不提供开关**：
//	  第五条是「应当」，不是「可以」。一个没有隐式标识的成品出了站就再也
//	  补不回来，而开关的唯一用途是把自己关进违规状态。
//
// 🔴 只对**新**产出生效，不回溯历史成品：回溯要重编码已经交付给用户的图，
// 那是一次不可逆的画质损失 + 一次全量重写，换来的合规收益是零 ——
// 办法约束的是生成服务此后的产出。历史行的 assets.aigc_label 保持 NULL，
// 后台资产视图把它显示成「未标识（历史）」。
package aigc

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"time"
)

// 位置取值。与 cfgstore 的 aigc_label_position 白名单逐字对应。
const (
	PosBottomRight  = "bottom-right"
	PosBottomLeft   = "bottom-left"
	PosTopRight     = "top-right"
	PosTopLeft      = "top-left"
	PosBottomCenter = "bottom-center"
)

// Positions 是全部合法位置（cfgstore 的白名单从这里取，避免两处各写一份）。
var Positions = []string{PosBottomRight, PosBottomLeft, PosTopRight, PosTopLeft, PosBottomCenter}

// Options 是一次标识动作的全部输入，由 worker 从注册表现读现取。
type Options struct {
	// LabelEnabled 控制**显式**水印。为 false 时一个像素都不改。
	LabelEnabled bool
	// LabelText 是水印文字。空串等同于关掉显式水印（画不出东西来）。
	LabelText string
	// LabelPosition 见 Positions。未知值回落 PosBottomRight。
	LabelPosition string
	// LabelOpacity 是水印不透明度，0..1。
	LabelOpacity float64
	// LabelSizePct 是字号占图像**短边**的百分比（1..10）。
	LabelSizePct float64

	// Producer 是隐式标识里的「内容制作服务提供者」（GB 45438-2025 的 ContentProducer）。
	Producer string
	// ProductName 进 EXIF Software / XMP CreatorTool。
	ProductName string
	// ProduceID 是本次内容制作编号（GB 45438-2025 的 ProduceID）。用生成任务 id。
	ProduceID string
	// Now 是标识时间（UTC）。
	Now time.Time
}

// 标识结果，落进 assets.aigc_label 列。
const (
	// MarkMeta = 只有隐式标识（显式水印被运营关掉了）。
	MarkMeta = "meta"
	// MarkVisibleMeta = 显式水印 + 隐式元数据，两样都有。
	MarkVisibleMeta = "visible+meta"
)

// Result 是 Apply 的产物。
type Result struct {
	// JPEG 是带隐式标识（可能还带水印）的成品字节。
	JPEG []byte
	// Mark 是写进 assets.aigc_label 的值：MarkMeta 或 MarkVisibleMeta。
	Mark string
	// SHA256 是**加水印之后、写元数据之前**的图像字节哈希，
	// 同时也写进隐式标识里（核验方拿到文件后可以剥掉元数据段重算）。
	SHA256 string
}

// ErrNoImage 表示传进来的图是空的。
var ErrNoImage = errors.New("aigc: 没有可标识的图像")

// Image 是本包需要的最小图像接口，刻意不 import internal/imaging ——
// 那个包是「解码与启发式分析」，本包是「标识」，让标识去依赖分析会把
// 两个变更速度完全不同的东西焊在一起。
type Image struct {
	Data          []byte // RGBA
	Width, Height int
}

// Apply 是唯一入口：先在像素上画显式标识，再编码 JPEG，最后写隐式标识。
//
// 🔴 顺序不能反。水印必须在编码前画进像素（否则它不是水印，是一层可以被
// 剥掉的元数据）；隐式标识必须在编码后写（EXIF/XMP 是 JPEG 容器里的段，
// 不是像素）。两件事之间算一次哈希，那个哈希才描述「被标识的那张图」。
func Apply(img *Image, jpegQuality int, o Options, encode func(*Image, int) ([]byte, error)) (Result, error) {
	if img == nil || img.Width <= 0 || img.Height <= 0 || len(img.Data) < img.Width*img.Height*4 {
		return Result{}, ErrNoImage
	}
	mark := MarkMeta
	if o.LabelEnabled && strings.TrimSpace(o.LabelText) != "" {
		if err := DrawLabel(img, o); err != nil {
			// 画不出来不能让整个任务失败（用户已经付过钱了），但必须留痕：
			// 调用方看 Mark 就知道这张只有隐式标识。
			return Result{}, err
		}
		mark = MarkVisibleMeta
	}
	raw, err := encode(img, jpegQuality)
	if err != nil {
		return Result{}, err
	}
	sum := sha256.Sum256(raw)
	hash := hex.EncodeToString(sum[:])
	out, err := EmbedJPEG(raw, Meta{
		Producer:    o.Producer,
		ProductName: o.ProductName,
		ProduceID:   o.ProduceID,
		ContentHash: hash,
		At:          o.Now,
		Visible:     mark == MarkVisibleMeta,
		LabelText:   o.LabelText,
	})
	if err != nil {
		return Result{}, err
	}
	return Result{JPEG: out, Mark: mark, SHA256: hash}, nil
}
