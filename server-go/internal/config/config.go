// 进程级配置：只从环境变量读，任何解析失败直接启动失败，不猜、不兜底。
//
// 🔴 与 Node 版的两条硬性差异（都是本轮要修的安全问题，见 README「行为差异清单」）：
//  1. 上游图像 API Key、SMTP 密码这类**密钥只从环境变量读**。Node 版的
//     configStore 是「DB 覆盖 > env > 默认」，运维从后台写一次密钥就明文落进
//     app_config 表并随每日备份落盘，之后清空环境变量也不再生效。
//     Go 版**不保留从 DB 读密钥这条路径**。
//  2. IP_HASH_SALT 变成**独立必填变量**。Node 版它回落到 ADMIN_TOKEN，
//     换令牌会让历史 free_grants.ip_hash 全部对不上，24h IP 上限被静默重置一次。
package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

// MaxPoolConnsHardLimit 硬顶 4：统一 PG 实例 max_connections=50，每项目配额 8
// （owner 2 + app 5 + readonly 1）。app 的 5 个槽必须留 1 个给滚动重启瞬间的
// 残留连接与探针，所以池子最多 4。配大了直接启动失败，不静默降级。
const MaxPoolConnsHardLimit = 4

type Config struct {
	Host string
	Port string

	DatabaseURL  string
	PoolMaxConns int32

	// 资产目录（原 data/assets，单层平铺，文件名 = storage_key = <uuid>.jpg）。
	AssetDir string
	// 静态 SPA 目录；留空则不提供静态文件（容器里由 OpenResty/Caddy 托管时用）。
	WebDir string

	AdminToken   string
	IPHashSalt   string
	TrustedProxy string
	TrustCFIP    bool

	SessionTTLDays      int
	MaxUserStorageBytes int64
	EventRetentionDays  int
	IdempotencyDays     int
	MaxJobAttempts      int
	RateLimitMaxKeys    int
	ShutdownGraceS      int

	// 三个开发逃生口。**旗标为真还要带管理员令牌**才生效（双闸），
	// 这里只记录旗标本身。
	AllowMockPurchases bool
	AllowTestLogin     bool
	PlayAcknowledge    bool

	// 上游 / 邮件密钥：只在这里出现，绝不进 DB、绝不进日志。
	ImageProviderAPIKey string
	SMTPPass            string
	// 非密钥的上游设置仍可被后台热改（见 cfgstore）。
	ImageProvider string

	GoogleServiceAccountJSON string
	GoogleWebClientID        string
}

func getenv(k, def string) string {
	if v, ok := os.LookupEnv(k); ok {
		return v
	}
	return def
}

func atoi(s, name string) (int, error) {
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil {
		return 0, fmt.Errorf("%s 不是合法整数", name)
	}
	return n, nil
}

// Load 读取环境变量并做硬校验。错误信息里不带任何连接串或密钥值。
func Load() (*Config, error) {
	c := &Config{
		Host:                     getenv("HOST", "127.0.0.1"),
		Port:                     getenv("PORT", "8787"),
		AssetDir:                 getenv("MUSEFRAME_ASSET_DIR", "./data/assets"),
		WebDir:                   getenv("MUSEFRAME_WEB_DIR", ""),
		AdminToken:               os.Getenv("ADMIN_TOKEN"),
		TrustedProxy:             getenv("TRUSTED_PROXY", "private"),
		TrustCFIP:                os.Getenv("TRUST_CF_CONNECTING_IP") == "true",
		AllowMockPurchases:       os.Getenv("ALLOW_MOCK_PURCHASES") == "true",
		AllowTestLogin:           os.Getenv("ALLOW_TEST_LOGIN") == "true",
		PlayAcknowledge:          os.Getenv("PLAY_ACKNOWLEDGE") == "true",
		ImageProviderAPIKey:      os.Getenv("IMAGE_PROVIDER_API_KEY"),
		SMTPPass:                 os.Getenv("SMTP_PASS"),
		ImageProvider:            strings.ToLower(strings.TrimSpace(getenv("IMAGE_PROVIDER", "remote"))),
		GoogleServiceAccountJSON: os.Getenv("GOOGLE_SERVICE_ACCOUNT_JSON"),
		GoogleWebClientID:        os.Getenv("GOOGLE_WEB_CLIENT_ID"),
	}
	if c.ImageProvider == "" {
		c.ImageProvider = "remote"
	}

	c.DatabaseURL = strings.TrimSpace(os.Getenv("MUSEFRAME_DATABASE_URL"))
	if c.DatabaseURL == "" {
		return nil, fmt.Errorf("MUSEFRAME_DATABASE_URL 未设置")
	}

	pool, err := atoi(getenv("MUSEFRAME_DATABASE_POOL_MAX_CONNS", "4"), "MUSEFRAME_DATABASE_POOL_MAX_CONNS")
	if err != nil {
		return nil, err
	}
	if pool < 1 {
		return nil, fmt.Errorf("MUSEFRAME_DATABASE_POOL_MAX_CONNS 必须 >= 1")
	}
	if pool > MaxPoolConnsHardLimit {
		return nil, fmt.Errorf("MUSEFRAME_DATABASE_POOL_MAX_CONNS=%d 超过硬顶 %d（统一 PG 每项目配额所限）", pool, MaxPoolConnsHardLimit)
	}
	c.PoolMaxConns = int32(pool)

	// 🔴 IP_HASH_SALT 必填且**不得回落 ADMIN_TOKEN**。
	c.IPHashSalt = strings.TrimSpace(os.Getenv("IP_HASH_SALT"))
	if c.IPHashSalt == "" {
		return nil, fmt.Errorf("IP_HASH_SALT 未设置：它是免费额度 per-IP 上限的盐，" +
			"必须是独立固化的值（Node 版回落到 ADMIN_TOKEN，换令牌会静默重置 24h 上限）")
	}
	if c.IPHashSalt == c.AdminToken {
		return nil, fmt.Errorf("IP_HASH_SALT 不得等于 ADMIN_TOKEN：两者必须独立")
	}

	for _, f := range []struct {
		dst *int
		env string
		def string
	}{
		{&c.SessionTTLDays, "SESSION_TTL_DAYS", "90"},
		{&c.EventRetentionDays, "EVENT_RETENTION_DAYS", "90"},
		{&c.IdempotencyDays, "IDEMPOTENCY_RETENTION_DAYS", "30"},
		{&c.MaxJobAttempts, "MAX_JOB_ATTEMPTS", "3"},
		{&c.RateLimitMaxKeys, "RATE_LIMIT_MAX_KEYS", "50000"},
		{&c.ShutdownGraceS, "SHUTDOWN_GRACE_SECONDS", "25"},
	} {
		v, err := atoi(getenv(f.env, f.def), f.env)
		if err != nil {
			return nil, err
		}
		if v < 1 {
			return nil, fmt.Errorf("%s 必须 >= 1", f.env)
		}
		*f.dst = v
	}

	storage, err := strconv.ParseInt(getenv("MAX_USER_STORAGE_BYTES", strconv.Itoa(256*1024*1024)), 10, 64)
	if err != nil || storage < 1 {
		return nil, fmt.Errorf("MAX_USER_STORAGE_BYTES 不是合法正整数")
	}
	c.MaxUserStorageBytes = storage

	return c, nil
}
