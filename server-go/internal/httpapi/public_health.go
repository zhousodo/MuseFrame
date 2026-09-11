package httpapi

import (
	"encoding/json"
	"net/http"

	"museframe-api/internal/store"
	"museframe-api/internal/worker"
)

// HealthOK 是 200 的响应体，键序固定：ok, service, time, database, generation, queue。
type HealthOK struct {
	OK         bool         `json:"ok"`
	Service    string       `json:"service"`
	Time       string       `json:"time"`
	Database   string       `json:"database"`
	Generation HealthGen    `json:"generation"`
	Queue      worker.Queue `json:"queue"`
}

// HealthGen 是 generation 块。reason 是 null 不是空串。
type HealthGen struct {
	Available bool    `json:"available"`
	Mode      string  `json:"mode"`
	Reason    *string `json:"reason"`
}

// HealthDown 是 503 的响应体，**只有 4 个键**（没有 generation / queue），键序固定。
// 🔴 底层数据库错误文本只进日志，不进响应体。
type HealthDown struct {
	OK       bool   `json:"ok"`
	Service  string `json:"service"`
	Database string `json:"database"`
	Time     string `json:"time"`
}

// hHealth 是 GET /v1/health。
// 「健康」指的是这个进程还能不能干活，不是进程是否还活着：
// 一个写死的字面量曾经在库不可写、磁盘写满、镜像密钥被清空时依然报绿。
func (a *App) hHealth(c *Ctx) (any, error) {
	gen := a.prov.Status()
	if err := a.st.HealthProbe(c.R.Context()); err != nil {
		// 只记一句「探测失败」，不把 SQL 错误文本回给调用方。
		a.lg.Warn("health: 数据库探测失败", map[string]any{"requestId": c.RequestID})
		c.W.Header().Set("Content-Type", "application/json")
		c.W.WriteHeader(http.StatusServiceUnavailable)
		_ = json.NewEncoder(c.W).Encode(HealthDown{
			OK: false, Service: "museframe-api", Database: "error", Time: store.ISO(a.now()),
		})
		c.Handled = true
		return nil, nil
	}
	// 生成不可用**不会**导致 503：缺 provider key 是运维在面板里修的配置态，
	// 不是让 Docker 重启容器的理由。
	return HealthOK{
		OK: true, Service: "museframe-api", Time: store.ISO(a.now()), Database: "ok",
		Generation: HealthGen{Available: gen.Available, Mode: gen.Mode, Reason: gen.Reason},
		Queue:      a.worker.Depth(),
	}, nil
}

// ReadyBody 是 /v1/ready 的出参。
type ReadyBody struct {
	Ready   bool   `json:"ready"`
	Service string `json:"service"`
	Time    string `json:"time"`
}

// hReady 是只读探活：SELECT 1，绝不写库。
func (a *App) hReady(c *Ctx) (any, error) {
	if !a.st.Ready(c.R.Context()) {
		c.W.Header().Set("Content-Type", "application/json")
		c.W.WriteHeader(http.StatusServiceUnavailable)
		_ = json.NewEncoder(c.W).Encode(ReadyBody{Ready: false, Service: "museframe-api", Time: store.ISO(a.now())})
		c.Handled = true
		return nil, nil
	}
	return ReadyBody{Ready: true, Service: "museframe-api", Time: store.ISO(a.now())}, nil
}
