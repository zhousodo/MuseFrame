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
	"time"
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
	// 🔴 2026-09-12 新增。这个值此前是**四处独立的字面量**：
	//   public_email.go 的 emailWindow 常量（真正的有效期与重发窗口）、
	//   同文件 ExpiresInSeconds: 600（告诉 App 的数字）、
	//   mailer.go 纯文本正文里的「10 分钟内有效」、
	//   mailer.go HTML 正文里的「10 分钟内有效」。
	// 四份里改一份，用户就会按信里写的时间慢慢输码，拿到「验证码已过期」——
	// 而所有接口回归全绿（掌镜 2026-09-11 踩的正是同一个坑）。
	// 现在四处全部改读 Store.EmailCodeTTL()，注册表这一项是唯一真相源。
	{"email_code_ttl_seconds", KindNumber, "EMAIL_CODE_TTL_SECONDS", float64(600),
		"邮箱验证码有效期（秒，同时也是重发窗口）", false},
	// 🔴 2026-09-12 第二批。这三项此前是**纯字面量 / 纯 env**，后台看不见也改不了：
	//   public_email.go 的 `issued >= 5` 与 `rec.Attempts >= 5` 是两个裸 5；
	//   每账号存储上限只活在 config.MAX_USER_STORAGE_BYTES 里，改一次要重启整个容器。
	// 它们都是**运营旋钮**而不是部署常量：刷码攻击来的时候要能立刻把签发次数压到 1，
	// 磁盘快满的时候要能立刻把存储上限压下去 —— 这两件事都等不起一次发版。
	{"email_code_max_attempts", KindNumber, "EMAIL_CODE_MAX_ATTEMPTS", float64(5),
		"同一个验证码最多可猜几次（猜满即锁，需重新获取）", false},
	{"email_code_max_issues_per_window", KindNumber, "EMAIL_CODE_MAX_ISSUES_PER_WINDOW", float64(5),
		"同一个邮箱在一个有效期窗口内最多可签发几个验证码（越小越抗刷）", false},
	{"max_user_storage_bytes", KindNumber, "MAX_USER_STORAGE_BYTES", float64(256 * 1024 * 1024),
		"每个账号的原图存储上限（字节，超过即拒绝上传并回 413）", false},
}

// numRanges 是数值项的合法**闭区间**，按键名索引。没有条目 = 不限。
//
// 🔴 为什么必须有这张表，而且必须同时管「写」和「读」两头：
//
//	写（Set）：后台手滑把 worker_concurrency 存成 0，队列会静默冻结，而每个
//	排队任务的额度都已经预留掉了，除了重启没有出路 —— 页面还显示「已保存」。
//	越界的写现在直接 422，并在消息里写出合法区间。
//
//	读（ClampedNumber）：光靠写校验不够。库里可能已经躺着 Node 版写下的越界行，
//	project.env 里也可能有人手写了 EMAIL_CODE_TTL_SECONDS=0。读的时候再夹一次，
//	保证**任何**来源的脏值都不能把服务打死；同时 List() 会给这一项挂
//	warning，让后台看得见「这个值被夹过」，而不是默默生效一个不同的数。
//
// 🔴 区间本身写死在代码里、不做成配置项：它们是安全/可用性边界，不是口味问题。
var numRanges = map[string][2]float64{
	// 下界 60 秒：配成 0（手滑清空）= 每个码一签发就过期，邮箱登录整条路死掉。
	// 上界 1 小时：一个泄漏的验证码不该能用一整天，而它同时是签发次数窗口的长度。
	"email_code_ttl_seconds": {60, 3600},
	// 1 次太严但合法（攻击时的应急档）；10 次以上就等于把 6 位码的猜中率抬进可行区。
	"email_code_max_attempts": {1, 10},
	// 同上：1 是应急档；上界 20 防止有人填个 9999 把防刷彻底关掉。
	"email_code_max_issues_per_window": {1, 20},
	// 1 MiB..8 GiB。下界保证至少能放下一张图（单图上限 20 MB 另有 MaxUpload 管），
	// 上界防止手滑多打几个 0 之后一个账号就能把数据盘写满。
	"max_user_storage_bytes": {1 << 20, 8 << 30},
	// 0 = 停发免费额度（合法的运营动作）；上界纯防手滑。
	"free_units":             {0, 1000},
	"free_grants_per_ip_day": {0, 10000},
	"free_grants_per_day":    {0, 1000000},
	// 与 worker.Concurrency 的夹区间一致：0 会冻结队列，过大会同时打爆上游与内存。
	"worker_concurrency": {1, 8},
	"smtp_port":          {1, 65535},
	// 1 秒..15 分钟。上游单次 edits 调用实测可达 6 分钟，所以上界给得宽；
	// 下界防止配成 0 后每个任务都在连接建立前就超时（现象与「上游挂了」一模一样）。
	"image_provider_timeout_ms": {1000, 900000},
}

// NumRange 返回某个数值项的合法闭区间。ok 为假表示不限。
func NumRange(key string) (min, max float64, ok bool) {
	r, ok := numRanges[key]
	if !ok {
		return 0, 0, false
	}
	return r[0], r[1], true
}

// ClampedNumber 是带区间夹取的 Number。所有消费方都该用它，而不是裸 Number ——
// 脏值的来源可能是 DB 行、env、甚至是将来改错的默认值。
func (s *Store) ClampedNumber(key string) float64 {
	v := s.Number(key)
	if min, max, ok := NumRange(key); ok {
		if v < min {
			return min
		}
		if v > max {
			return max
		}
	}
	return v
}

// ClampedInt 是 ClampedNumber 的整数封装。
func (s *Store) ClampedInt(key string) int { return int(s.ClampedNumber(key)) }

// EmailCodeMaxAttempts 是「同一个码最多猜几次」。
func (s *Store) EmailCodeMaxAttempts() int { return s.ClampedInt("email_code_max_attempts") }

// EmailCodeMaxIssuesPerWindow 是「一个窗口内最多签几个码」。
func (s *Store) EmailCodeMaxIssuesPerWindow() int {
	return s.ClampedInt("email_code_max_issues_per_window")
}

// MaxUserStorageBytes 是每账号存储上限（字节）。
//
// 🔴 返回 int64 而不是 int：32 位平台上 8 GiB 的上界会在 int 里溢出成负数，
// 于是「存储上限」变成「任何上传都超限」。
func (s *Store) MaxUserStorageBytes() int64 { return int64(s.ClampedNumber("max_user_storage_bytes")) }

// EmailCodeTTL 是邮箱验证码的有效期。
//
// 🔴 必须夹在区间内，不能直接信注册表里的数字：
//   - 配成 0 或负数（手滑清空）= 每个码一签发就过期，邮箱登录整条路死掉，
//     而后台看起来一切正常；
//   - 配成一天 = 一个泄漏的验证码在一天内都能登进账号，而它同时是
//     「每窗口最多签 5 次」的窗口长度，等于一天只能要 5 次码。
//
// 上下界是**安全边界**而不是口味问题，所以写在代码里、不做成配置项。
// 区间本身现在统一登记在 numRanges 里（Set 的写校验与这里的读夹取同一个真相源：
// 原先这里有一对独立的 minTTL/maxTTL 常量，改区间时会漏掉另一处）。
func (s *Store) EmailCodeTTL() time.Duration {
	return time.Duration(s.ClampedInt("email_code_ttl_seconds")) * time.Second
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
	// 🔴 区间校验必须在**写库之前**。夹一下再存是更糟的选择：运营输 0、库里变成 60，
	// 页面刷新后显示 60，没人知道刚才那次保存其实没按要求生效。直接拒绝 + 说出区间。
	if err := checkRange(it, stored); err != nil {
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

// checkRange 校验一个已编码的数值是否落在注册区间内。
func checkRange(it Item, stored string) error {
	if it.Type != KindNumber {
		return nil
	}
	min, max, ok := NumRange(it.Key)
	if !ok {
		return nil
	}
	n, err := strconv.ParseFloat(stored, 64)
	if err != nil {
		return fmt.Errorf("%s 必须是数字", it.Key)
	}
	if n < min || n > max {
		return fmt.Errorf("%s 必须在 %s..%s 之间（收到 %s）",
			it.Key, fmtNum(min), fmtNum(max), fmtNum(n))
	}
	return nil
}

func fmtNum(f float64) string { return strconv.FormatFloat(f, 'f', -1, 64) }

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
	// Min / Max 是数值项的合法闭区间；前端据此给 <input type=number> 加 min/max
	// 并把区间写在说明里，省掉「保存 → 422 → 猜区间」这一轮。
	Min *float64 `json:"min,omitempty"`
	Max *float64 `json:"max,omitempty"`
	// Warning 非空表示「当前生效值和配置源里写的那个值不一样」——
	// 源里的数字越界被夹了，或者根本不是数字。不标出来的话后台会展示一个
	// 看似正常的数，而真正生效的是另一个（本轮要消灭的正是这种静默偏差）。
	Warning string `json:"warning,omitempty"`
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

// numWarning 在「配置源里写的数字 != 真正生效的数字」时给出一句中文说明。
// 两种情况：源里根本不是数字（回落默认值），或者数字越界（被夹到边界）。
func (s *Store) numWarning(it Item, effective float64) string {
	raw, has := s.rawOverride(it.Key)
	from := "数据库覆盖项"
	if !has && it.EnvVar != "" {
		if v, set := s.lookupEnv(it.EnvVar); set {
			raw, has, from = v, true, "环境变量 "+it.EnvVar
		}
	}
	if !has {
		return ""
	}
	n, err := strconv.ParseFloat(strings.TrimSpace(raw), 64)
	if err != nil {
		return "🔴 " + from + " 里的值不是数字，已回落默认值 " + fmtNum(effective) + "。请修正或恢复默认。"
	}
	if n != effective {
		return "🔴 " + from + " 里写的是 " + fmtNum(n) + "，越界已夹到 " + fmtNum(effective) +
			"。请把它改成区间内的值，否则后台显示的和实际生效的不是一回事。"
	}
	return ""
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
			st.Value = s.ClampedNumber(it.Key)
			if min, max, ok := NumRange(it.Key); ok {
				lo, hi := min, max
				st.Min, st.Max = &lo, &hi
			}
			st.Warning = s.numWarning(it, st.Value.(float64))
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
