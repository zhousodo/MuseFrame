package imaging

import (
	"math"
	"sort"
)

// reasonByTag 与 Node 版 REASON_BY_TAG 逐条一致。
var reasonByTag = map[string]string{
	"portrait":     "PRESERVES_SOFT_FACE_LIGHT",
	"pet":          "GENTLE_ON_FUR_TEXTURE",
	"landscape":    "SUITS_OPEN_DISTANCE",
	"object":       "STRONG_SILHOUETTE_MATCH",
	"architecture": "SUITS_HARD_EDGES",
	"street":       "HOLDS_SCENE_MOOD",
}

// StyleCandidate 是参与排序的一个风格版本。
type StyleCandidate struct {
	StyleID       string
	VersionID     string
	EditorialRank int
	// Subjects 是 spec.compatibility.subjects；Tags 是 spec.identity.tags。
	Subjects map[string]float64
	Tags     []string
}

// Recommendation 是推荐结果的一项，字段顺序即 JSON 键序。
type Recommendation struct {
	StyleID        string  `json:"styleId"`
	StyleVersionID string  `json:"styleVersionId"`
	Score          float64 `json:"score"`
	ReasonCode     string  `json:"reasonCode"`
}

// Recommend 复刻 recommendStyles()：
// 0.40 兼容度 + 0.25 保存率先验(0.5) + 0.20 伪随机新鲜度 + 0.15 编辑优先级。
// 新鲜度用的是与 Node 一致的 (i * 2654435761) % 100 / 100，稳定可复现。
func Recommend(a Analysis, rows []StyleCandidate) []Recommendation {
	subj := a.SubjectType
	switch subj {
	case "person", "landscape", "object", "pet":
	default:
		subj = "object"
	}
	out := make([]Recommendation, 0, len(rows))
	total := len(rows)
	if total == 0 {
		return out
	}
	for i, s := range rows {
		compat, ok := s.Subjects[subj]
		if !ok {
			compat = 0.5
		}
		// Node 的 (i * 2654435761) % 100 在 IEEE754 双精度下是精确整数运算
		// （i 很小，乘积远小于 2^53），用 int64 复现同一结果。
		novelty := float64((int64(i)*2654435761)%100) / 100
		editorial := 1 - float64(s.EditorialRank)/float64(total)
		score := 0.40*compat + 0.25*0.5 + 0.20*novelty + 0.15*editorial
		tag := "portrait"
		for _, t := range s.Tags {
			if _, ok := reasonByTag[t]; ok {
				tag = t
				break
			}
		}
		out = append(out, Recommendation{
			StyleID: s.StyleID, StyleVersionID: s.VersionID,
			Score: math.Round(score*10000) / 10000, ReasonCode: reasonByTag[tag],
		})
	}
	// 稳定排序：分数降序，同分保持输入顺序（与 JS 的 Array.sort 稳定语义一致）。
	sort.SliceStable(out, func(i, j int) bool { return out[i].Score > out[j].Score })
	return out
}
