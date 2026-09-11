package httpapi

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"

	"museframe-api/internal/store"
)

// StyleSpec 只解出服务端真正要用的字段，其余原样透传。
type StyleSpec struct {
	Identity struct {
		Tags []string `json:"tags"`
	} `json:"identity"`
	Intent struct {
		Summary string `json:"summary"`
	} `json:"intent"`
	Compatibility struct {
		Subjects map[string]float64 `json:"subjects"`
	} `json:"compatibility"`
	Controls       map[string]SpecControl `json:"controls"`
	CoverArt       json.RawMessage        `json:"coverArt"`
	PromptAssembly *PromptAssembly        `json:"promptAssembly"`
}

// SpecControl 是 spec.controls.<name>。
type SpecControl struct {
	Default string   `json:"default"`
	Allowed []string `json:"allowed"`
}

// PromptAssembly 是提示词拼装块。
type PromptAssembly struct {
	BaseDirection    string            `json:"baseDirection"`
	SubjectRules     map[string]string `json:"subjectRules"`
	ControlFragments struct {
		Strength    map[string]string `json:"strength"`
		Fidelity    map[string]string `json:"fidelity"`
		Composition map[string]string `json:"composition"`
	} `json:"controlFragments"`
	NegativeConstraints []string `json:"negativeConstraints"`
	Compiler            any      `json:"compiler"`
}

func parseSpec(raw []byte) (*StyleSpec, error) {
	var s StyleSpec
	if err := json.Unmarshal(raw, &s); err != nil {
		return nil, err
	}
	return &s, nil
}

// StyleCard 是目录卡片，字段顺序即 JSON 键序（api.js:199-216）。
type StyleCard struct {
	StyleID         string          `json:"styleId"`
	StyleVersionID  string          `json:"styleVersionId"`
	Name            string          `json:"name"`
	ShortCaption    string          `json:"shortCaption"`
	SuitabilityTags []string        `json:"suitabilityTags"`
	Premium         bool            `json:"premium"`
	LockedForUser   bool            `json:"lockedForUser"`
	CoverArt        json.RawMessage `json:"coverArt"`
	CoverURL        *string         `json:"coverUrl"`
	// 🔴 硬编码字面量，中间是 EN DASH（U+2013）不是连字符。
	EstimatedTimeLabel string                 `json:"estimatedTimeLabel"`
	Controls           map[string]SpecControl `json:"controls"`
	Compatibility      map[string]float64     `json:"compatibility"`
}

const estimatedTimeLabel = "20–45 s"

func (a *App) styleCard(r store.StyleRow, plan string) (StyleCard, error) {
	spec, err := parseSpec(r.Spec)
	if err != nil {
		return StyleCard{}, err
	}
	var tags []string
	if err := json.Unmarshal(r.SuitabilityTags, &tags); err != nil {
		tags = []string{}
	}
	if tags == nil {
		tags = []string{}
	}
	var coverURL *string
	if a.cfg.WebDir != "" {
		f := filepath.Join(a.cfg.WebDir, "covers", r.InternalKey+".jpg")
		if fi, err := os.Stat(f); err == nil && fi.Mode().IsRegular() {
			u := "/covers/" + r.InternalKey + ".jpg"
			coverURL = &u
		}
	}
	return StyleCard{
		StyleID: r.StyleID, StyleVersionID: r.VersionID, Name: r.PublicName,
		ShortCaption: r.ShortCaption, SuitabilityTags: tags,
		Premium: r.Premium, LockedForUser: r.Premium && plan == "free",
		CoverArt: spec.CoverArt, CoverURL: coverURL,
		EstimatedTimeLabel: estimatedTimeLabel,
		Controls:           spec.Controls, Compatibility: spec.Compatibility.Subjects,
	}, nil
}

// GenerationInfo 是 /v1/auth/config 与 /v1/discover 里的 generation 块。
type GenerationInfo struct {
	Available             bool    `json:"available"`
	UnavailableReason     *string `json:"unavailableReason"`
	EstimatedRangeSeconds []int   `json:"estimatedRangeSeconds"`
}

func (a *App) generationInfo() GenerationInfo {
	gen := a.prov.Status()
	info := GenerationInfo{Available: gen.Available, EstimatedRangeSeconds: []int{5, 30}}
	if !gen.Available {
		info.UnavailableReason = gen.Reason
	}
	if gen.Mode == "remote" {
		info.EstimatedRangeSeconds = []int{60, 300}
	}
	return info
}

// Entitlements 是 GET /v1/entitlements/me 的出参。
// 🔴 availableUnits 必须是数字；features.* 必须是布尔。
type Entitlements struct {
	Plan                         string   `json:"plan"`
	AvailableUnits               int      `json:"availableUnits"`
	FreeCompletedImagesRemaining int      `json:"freeCompletedImagesRemaining"`
	Features                     Features `json:"features"`
}

// Features 是权益开关。
type Features struct {
	PremiumStyles  bool `json:"premiumStyles"`
	PriorityQueue  bool `json:"priorityQueue"`
	HighResolution bool `json:"highResolution"`
}

func (a *App) entitlements(ctx context.Context, userID string) (Entitlements, error) {
	now := a.now()
	plan, err := store.UserPlan(ctx, a.st.Q(), userID, now)
	if err != nil {
		return Entitlements{}, err
	}
	isCreator := len(plan) >= 7 && plan[:7] == "creator"
	units, err := store.AvailableUnits(ctx, a.st.Q(), userID, now)
	if err != nil {
		return Entitlements{}, err
	}
	savedFree, err := store.CommittedFreeCount(ctx, a.st.Q(), userID)
	if err != nil {
		return Entitlements{}, err
	}
	remaining := 0
	if plan == "free" {
		remaining = a.rt.Int("free_units") - savedFree
		if remaining < 0 {
			remaining = 0
		}
	}
	return Entitlements{
		Plan: plan, AvailableUnits: units, FreeCompletedImagesRemaining: remaining,
		Features: Features{PremiumStyles: isCreator, PriorityQueue: isCreator, HighResolution: isCreator},
	}, nil
}

// 控制项白名单（api.js:242-249）。
var controlDefaults = map[string]string{"strength": "balanced", "fidelity": "high", "composition": "keep"}
var controlAllowed = map[string][]string{
	"strength":    {"soft", "balanced", "bold"},
	"fidelity":    {"high", "natural"},
	"composition": {"keep", "reframe"},
}

// AspectRatios 与 QualityTiers 是出参与入参都会用到的枚举。
var AspectRatios = []string{"original", "1:1", "4:5", "16:9"}

// CoerceControls 把三个用户控制项夹到 StyleSpec 自己声明的白名单里。
// 其他任何值 —— 包括试图往编译提示词里夹带指令的字符串 —— 一律塌回默认值。
func CoerceControls(spec *StyleSpec, controls map[string]any) map[string]string {
	out := map[string]string{}
	for _, name := range []string{"strength", "fidelity", "composition"} {
		fallback := controlDefaults[name]
		allowed := controlAllowed[name]
		if spec != nil && spec.Controls != nil {
			if decl, ok := spec.Controls[name]; ok && len(decl.Allowed) > 0 {
				// spec 忘了声明 allowed 时不能静默丢掉用户的选择 ——
				// 回落到产品级词表，绝不回落到空串。
				allowed = decl.Allowed
			}
		}
		def := fallback
		if spec != nil && spec.Controls != nil {
			if decl, ok := spec.Controls[name]; ok && decl.Default != "" && contains(allowed, decl.Default) {
				def = decl.Default
			}
		}
		out[name] = def
		if given, ok := controls[name].(string); ok && contains(allowed, given) {
			out[name] = given
		}
	}
	return out
}

func contains(list []string, v string) bool {
	for _, s := range list {
		if s == v {
			return true
		}
	}
	return false
}
