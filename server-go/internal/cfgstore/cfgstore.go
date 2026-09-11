// 运行时配置（对应 Node 版 server/configStore.js 的 REGISTRY + cfg/setCfg/listCfg）。
//
// 与 Node 版的关键差异 —— 安全问题 1 的修复点：
//
//	Node 版解析优先级是「DB 覆盖 > 环境变量 > 内置默认」，对所有键一视同仁，
//	包括 secret:true 的 image_provider_api_key 与 smtp_pass。运维 2026-09-02 从后台
//	设了一次上游密钥，密钥就以明文躺进 app_config 表，随每日备份 tar 落盘；
//	此后清空 .env 里的同名变量不再生效（DB 优先级更高）。
//
//	Go 版把 secret 键的 DB 路径整条拆掉：
//	  - Load 时 app_config 里 secret 键的行不进缓存（等于不生效）；
//	  - Get(secretKey) 只读环境变量，读不到就用默认值（通常是空串）；
//	  - Set(secretKey, ...) 直接返回 ErrSecretNotWritable，后台 PUT 得到 422。
//	因此「后台热改密钥」这条写入口在 Go 版不存在，漏洞没法被原样搬过来。
package cfgstore

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
)

// ErrSecretNotWritable：secret 键不允许写入数据库。
var ErrSecretNotWritable = errors.New("这是密钥项，只能通过环境变量 / project.env 设置，不允许写入数据库")

type Kind string

const (
	KindString Kind = "string"
	KindNumber Kind = "number"
	KindBool   Kind = "boolean"
)

// Item 是注册表里的一项。Secret 为真的项永远不走 DB。
type Item struct {
	Key         string
	Type        Kind
	EnvVar      string
	Default     any
	Description string
	Secret      bool
}

// Registry 与 Node 版 configStore.js:20-54 的 26 个键逐条对齐
// （键名、类型、环境变量名、默认值、中文描述、secret 标记）。
var Registry = []Item{
	{"free_units", KindNumber, "FREE_UNITS", float64(3), "注册用户免费生成张数", false},
	{"allow_guest", KindBool, "ALLOW_GUEST", false, "兼容旧客户端的游客令牌（默认关闭；不能访问作品、图片、额度或生成）", false},
	{"free_requires_auth", KindBool, "FREE_REQUIRES_AUTH", true, "免费额度需登录（邮箱注册）后发放", false},
	{"support_email", KindString, "SUPPORT_EMAIL", "donaldkuke@gmail.com", "客服 / 加购联系邮箱（额度用完时展示给用户）", false},
	{"support_qq_group", KindString, "SUPPORT_QQ_GROUP", "824558022", "客服 QQ 群号（购买额度的主要入口，留空则不展示）", false},
	{"free_grants_per_ip_day", KindNumber, "FREE_GRANTS_PER_IP_DAY", float64(3), "每个 IP 每 24 小时最多发放几次免费额度（0=停发）", false},
	{"free_grants_per_day", KindNumber, "FREE_GRANTS_PER_DAY", float64(50), "全站每 24 小时免费额度发放次数上限（0=停发）", false},
	{"image_provider_base_url", KindString, "IMAGE_PROVIDER_BASE_URL", "", "图像模型接口地址", false},
	{"image_provider_api_key", KindString, "IMAGE_PROVIDER_API_KEY", "", "图像模型 API 密钥（只读环境变量，不可从后台写入）", true},
	{"image_provider_model", KindString, "IMAGE_PROVIDER_MODEL", "gpt-image-2", "图像模型名称", false},
	{"prompt_compiler_model", KindString, "PROMPT_COMPILER_MODEL", "gpt-5.4-mini", "提示词编译模型", false},
	{"image_quality_standard", KindString, "IMAGE_QUALITY_STANDARD", "medium", "标准档输出质量（low/medium/high/auto）", false},
	{"image_quality_high", KindString, "IMAGE_QUALITY_HIGH", "high", "高清档输出质量（low/medium/high/auto）", false},
	{"image_provider_model_high", KindString, "IMAGE_PROVIDER_MODEL_HIGH", "", "高清档使用的模型（留空 = 与标准档相同）", false},
	{"image_provider_timeout_ms", KindNumber, "IMAGE_PROVIDER_TIMEOUT_MS", float64(420000), "图像模型请求超时时间（毫秒）", false},
	{"local_engine_fallback", KindBool, "LOCAL_ENGINE_FALLBACK", false, "远程生成失败时回落本地像素引擎（默认关闭：回落产出的不是模型结果）", false},
	{"worker_concurrency", KindNumber, "WORKER_CONCURRENCY", float64(3), "生成任务并发数", false},
	{"google_client_ids", KindString, "GOOGLE_CLIENT_IDS", "", "Google 登录 OAuth Client IDs(逗号分隔)", false},
	{"apple_bundle_ids", KindString, "APPLE_BUNDLE_IDS", "", "Sign in with Apple Bundle IDs(逗号分隔)", false},
	{"google_package_name", KindString, "GOOGLE_PACKAGE_NAME", "com.museframe.app", "Google Play 包名", false},
	{"smtp_host", KindString, "SMTP_HOST", "", "SMTP 服务器地址", false},
	{"smtp_port", KindNumber, "SMTP_PORT", float64(587), "SMTP 端口", false},
	{"smtp_user", KindString, "SMTP_USER", "", "SMTP 登录用户名", false},
	{"smtp_pass", KindString, "SMTP_PASS", "", "SMTP 登录密码/密钥（只读环境变量，不可从后台写入）", true},
	{"smtp_from", KindString, "SMTP_FROM", "MuseFrame <no-reply@lenscript.cn>", "发件人地址", false},
	{"email_login_enabled", KindBool, "EMAIL_LOGIN_ENABLED", false, "开启邮箱验证码登录", false},
}

var byKey = func() map[string]Item {
	m := make(map[string]Item, len(Registry))
	for _, it := range Registry {
		m[it.Key] = it
	}
	return m
}()

// SecretKeys 返回注册表里所有 secret 键（当前 2 个）。
func SecretKeys() []string {
	var out []string
	for _, it := range Registry {
		if it.Secret {
			out = append(out, it.Key)
		}
	}
	sort.Strings(out)
	return out
}

// IsSecret 判断某键是否为密钥项。迁移脚本与 /db/table 脱敏都用它。
func IsSecret(key string) bool {
	it, ok := byKey[key]
	return ok && it.Secret
}

// Backend 是 app_config 表的读写口，由 store 实现；cfgstore 不直接依赖 pgx。
type Backend interface {
	LoadAppConfig(ctx context.Context) (map[string]string, error)
	UpsertAppConfig(ctx context.Context, key, value string) error
	DeleteAppConfig(ctx context.Context, key string) error
}

// Store 是运行时配置的唯一出口。所有消费方在调用时读，不缓存返回值。
type Store struct {
	mu      sync.RWMutex
	cache   map[string]string
	backend Backend
	lookup  func(string) (string, bool)
	// SkippedSecretRows 记录 Load 时因为是密钥而被丢弃的 app_config 行的键名。
	// 只记键名，绝不记值 —— 它是「库里还有遗留明文密钥，该去清了」的信号。
	SkippedSecretRows []string
}

// New 构造一个配置存储并从 DB 装载非密钥覆盖项。
func New(ctx context.Context, backend Backend) (*Store, error) {
	s := &Store{cache: map[string]string{}, backend: backend, lookup: os.LookupEnv}
	if err := s.Reload(ctx); err != nil {
		return nil, err
	}
	return s, nil
}

// NewForTest 构造一个不连 DB 的配置存储，环境变量读取可注入。
func NewForTest(env map[string]string) *Store {
	return &Store{
		cache: map[string]string{},
		lookup: func(k string) (string, bool) {
			v, ok := env[k]
			return v, ok
		},
	}
}

// Reload 重新从 DB 装载覆盖项。secret 键的行一律跳过，不进缓存。
func (s *Store) Reload(ctx context.Context) error {
	if s.backend == nil {
		return nil
	}
	rows, err := s.backend.LoadAppConfig(ctx)
	if err != nil {
		return err
	}
	cache := make(map[string]string, len(rows))
	var skipped []string
	for k, v := range rows {
		if IsSecret(k) {
			skipped = append(skipped, k)
			continue
		}
		if _, known := byKey[k]; !known {
			continue
		}
		cache[k] = v
	}
	sort.Strings(skipped)
	s.mu.Lock()
	s.cache, s.SkippedSecretRows = cache, skipped
	s.mu.Unlock()
	return nil
}

func (s *Store) rawOverride(key string) (string, bool) {
	if IsSecret(key) {
		return "", false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	v, ok := s.cache[key]
	return v, ok
}

// Source 返回该键当前生效值的来源：db / env / default。
func (s *Store) Source(key string) string {
	if _, ok := s.rawOverride(key); ok {
		return "db"
	}
	it, ok := byKey[key]
	if ok && it.EnvVar != "" {
		if _, set := s.lookupEnv(it.EnvVar); set {
			return "env"
		}
	}
	return "default"
}

func (s *Store) lookupEnv(k string) (string, bool) {
	if s.lookup != nil {
		return s.lookup(k)
	}
	return os.LookupEnv(k)
}

// String 返回字符串型配置的生效值。
func (s *Store) String(key string) string {
	it, ok := byKey[key]
	if !ok {
		return ""
	}
	if raw, ok := s.rawOverride(key); ok {
		return raw
	}
	if it.EnvVar != "" {
		if v, set := s.lookupEnv(it.EnvVar); set {
			return v
		}
	}
	if d, ok := it.Default.(string); ok {
		return d
	}
	return ""
}

// Number 返回数值型配置的生效值；非法值回落默认值。
func (s *Store) Number(key string) float64 {
	it, ok := byKey[key]
	if !ok {
		return 0
	}
	def, _ := it.Default.(float64)
	raw, has := s.rawOverride(key)
	if !has && it.EnvVar != "" {
		if v, set := s.lookupEnv(it.EnvVar); set {
			raw, has = v, true
		}
	}
	if !has {
		return def
	}
	n, err := strconv.ParseFloat(strings.TrimSpace(raw), 64)
	if err != nil {
		return def
	}
	return n
}

// Int 是 Number 的整数封装。
func (s *Store) Int(key string) int { return int(s.Number(key)) }

// Bool 返回布尔型配置的生效值（"true" 为真，其余为假，与 Node 的 coerce 一致）。
func (s *Store) Bool(key string) bool {
	it, ok := byKey[key]
	if !ok {
		return false
	}
	def, _ := it.Default.(bool)
	raw, has := s.rawOverride(key)
	if !has && it.EnvVar != "" {
		if v, set := s.lookupEnv(it.EnvVar); set {
			raw, has = v, true
		}
	}
	if !has {
		return def
	}
	return raw == "true"
}

// Set 写入（value 为 nil 时清除）一个 DB 覆盖项。
// secret 键返回 ErrSecretNotWritable —— 这条就是漏洞的写入口，必须堵死。
func (s *Store) Set(ctx context.Context, key string, value any) error {
	it, ok := byKey[key]
	if !ok {
		return fmt.Errorf("未知配置项：%s", key)
	}
	if it.Secret {
		return ErrSecretNotWritable
	}
	if value == nil {
		if s.backend != nil {
			if err := s.backend.DeleteAppConfig(ctx, key); err != nil {
				return err
			}
		}
		s.mu.Lock()
		delete(s.cache, key)
		s.mu.Unlock()
		return nil
	}

	stored, err := encode(it, value)
	if err != nil {
		return err
	}
	if s.backend != nil {
		if err := s.backend.UpsertAppConfig(ctx, key, stored); err != nil {
			return err
		}
	}
	s.mu.Lock()
	s.cache[key] = stored
	s.mu.Unlock()
	return nil
}

func encode(it Item, value any) (string, error) {
	switch it.Type {
	case KindNumber:
		var f float64
		switch v := value.(type) {
		case float64:
			f = v
		case int:
			f = float64(v)
		case string:
			n, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
			if err != nil {
				return "", fmt.Errorf("%s 必须是数字", it.Key)
			}
			f = n
		default:
			return "", fmt.Errorf("%s 必须是数字", it.Key)
		}
		return strconv.FormatFloat(f, 'f', -1, 64), nil
	case KindBool:
		switch v := value.(type) {
		case bool:
			return strconv.FormatBool(v), nil
		case string:
			if v != "true" && v != "false" {
				return "", fmt.Errorf("%s 必须是布尔值", it.Key)
			}
			return v, nil
		default:
			return "", fmt.Errorf("%s 必须是布尔值", it.Key)
		}
	default:
		switch v := value.(type) {
		case string:
			return v, nil
		case float64:
			return strconv.FormatFloat(v, 'f', -1, 64), nil
		case bool:
			return strconv.FormatBool(v), nil
		default:
			return "", fmt.Errorf("%s 必须是字符串", it.Key)
		}
	}
}

// Setting 是 GET /v1/admin/config 出参里的一项，字段顺序即 JSON 键序。
type Setting struct {
	Key             string `json:"key"`
	Value           any    `json:"value"`
	Source          string `json:"source"`
	Type            string `json:"type"`
	Description     string `json:"description"`
	Secret          bool   `json:"secret"`
	RequiresRestart bool   `json:"requiresRestart"`
	RawSet          bool   `json:"rawSet"`
	ReadOnly        bool   `json:"readOnly,omitempty"`
}

// MaskSecret 复刻 Node 版 maskSecret：4 个圆点 + 末 4 位；空值为 null。
func MaskSecret(v string) any {
	if v == "" {
		return nil
	}
	r := []rune(v)
	if len(r) <= 4 {
		return "••••" + v
	}
	return "••••" + string(r[len(r)-4:])
}

// List 返回全部注册项的生效值（secret 项掩码）。
// secret 项的 source 只可能是 env / default，永远不会是 db。
func (s *Store) List() []Setting {
	out := make([]Setting, 0, len(Registry))
	for _, it := range Registry {
		_, rawSet := s.rawOverride(it.Key)
		st := Setting{
			Key: it.Key, Source: s.Source(it.Key), Type: string(it.Type),
			Description: it.Description, Secret: it.Secret, RawSet: rawSet,
		}
		switch it.Type {
		case KindNumber:
			st.Value = s.Number(it.Key)
		case KindBool:
			st.Value = s.Bool(it.Key)
		default:
			st.Value = s.String(it.Key)
		}
		if it.Secret {
			st.Value = MaskSecret(s.String(it.Key))
			st.RequiresRestart = true
		}
		out = append(out, st)
	}
	return out
}
