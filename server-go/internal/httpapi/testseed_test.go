package httpapi

import (
	"context"
	"net/http/httptest"
	"time"

	"museframe-api/internal/store"
)

// 商品目录按生产实况播种（D-14 的四重确认值）：
// 4 个上架 + 2 个下架；priceMinor 是美分，priceCnyMinor 是人民币分且可为 null。
type seedProduct struct {
	key      string
	kind     string
	name     string
	units    int
	minor    int64
	cny      *int64
	period   *string
	active   bool
	insertAt int
}

func i64p(v int64) *int64   { return &v }
func strp(v string) *string { return &v }

// seedCatalog 播种展览 / 风格 / 版本 / 商品。
// 🔴 商品的插入顺序刻意打乱成生产库的历史顺序（旧目录先、点数包后），
// 这样「行序靠 rowid 巧合」的实现会在排序黄金测试里当场露馅。
func (e *testEnv) seedCatalog() {
	ctx := context.Background()
	t := e.now
	q := e.st.Q()

	exec := func(sql string, args ...any) {
		if _, err := q.Exec(ctx, sql, args...); err != nil {
			e.t.Fatalf("播种失败: %v\nSQL: %s", err, sql)
		}
	}

	exec(`INSERT INTO exhibitions (id, slug, title, curatorial_note, edition, editorial_rank, status, created_at)
	      VALUES ('exh-1','quiet-portraits','Quiet Portraits','note','EDITION 01',0,'published',$1),
	             ('exh-2','printed-matter','Printed Matter','note','EDITION 01',1,'published',$1)`, t)

	spec := `{"schemaVersion":1,
	 "identity":{"internalKey":"%s","publicName":"%s","theme":"quiet-portraits","tags":["portrait"],"premium":%v},
	 "intent":{"summary":"Near-window light, gentle contrast."},
	 "compatibility":{"subjects":{"person":0.95,"pet":0.7,"landscape":0.4,"object":0.5},"minShortEdge":320,"maxPeople":6},
	 "controls":{"strength":{"default":"balanced","allowed":["soft","balanced","bold"]},
	             "fidelity":{"default":"high","allowed":["high","natural"]},
	             "composition":{"default":"keep","allowed":["keep","reframe"]}},
	 "coverArt":{"palette":["#eee","#ccc"]},
	 "promptAssembly":{"baseDirection":"Soft window light.",
	   "subjectRules":{"person":"Keep the face."},
	   "controlFragments":{"strength":{"balanced":"Balanced."},"fidelity":{"high":"High."},"composition":{"keep":"Keep."}},
	   "negativeConstraints":["No text."]}}`

	type st struct {
		id, key, name string
		premium       bool
		exh           string
		pos           int
	}
	for _, s := range []st{
		{"style-free", "quiet_soft_window_01", "Soft Window", false, "exh-1", 0},
		{"style-premium", "quiet_gold_hour_01", "Gold Hour", true, "exh-1", 1},
		{"style-other", "print_riso_01", "Riso Press", false, "exh-2", 0},
	} {
		exec(`INSERT INTO styles (id, internal_key, slug, status, theme, premium, public_name, short_caption, suitability_tags, created_at)
		      VALUES ($1,$2,$3,'published','quiet-portraits',$4,$5,'caption','["portrait"]'::jsonb,$6)`,
			s.id, s.key, s.key, s.premium, s.name, t)
		exec(`INSERT INTO style_versions (id, style_id, version, status, spec, published_at, created_at)
		      VALUES ($1,$2,1,'published',$3::jsonb,$4,$4)`,
			"ver-"+s.id, s.id, sprintfSpec(spec, s.key, s.name, s.premium), t)
		exec(`INSERT INTO exhibition_styles (exhibition_id, style_id, position) VALUES ($1,$2,$3)`, s.exh, s.id, s.pos)
	}

	month := "month"
	products := []seedProduct{
		{"mini_pack", "pack", "Mini Pack", 8, 399, nil, nil, false, 1},
		{"creator_monthly", "subscription", "Creator Monthly", 30, 799, i64p(4900), &month, true, 2},
		{"creator_annual", "subscription", "Creator Annual", 480, 6999, nil, strp("year"), false, 3},
		{"pack_10", "pack", "Pack 10", 10, 499, i64p(2900), nil, true, 4},
		{"pack_30", "pack", "Pack 30", 30, 999, i64p(6900), nil, true, 5},
		{"pack_100", "pack", "Pack 100", 100, 2999, i64p(19900), nil, true, 6},
	}
	for _, p := range products {
		exec(`INSERT INTO products (id, internal_key, product_type, display_name, granted_units, price_minor,
		        currency, period, feature_flags, active, google_product_id, apple_product_id, price_cny_minor)
		      VALUES ($1,$2,$3,$4,$5,$6,'USD',$7,'{}'::jsonb,$8,$2,$2,$9)`,
			"prod-"+p.key, p.key, p.kind, p.name, p.units, p.minor, p.period, p.active, p.cny)
	}
}

func sprintfSpec(tmpl, key, name string, premium bool) string {
	out := ""
	ai := 0
	args := []string{key, name, boolStr(premium)}
	for i := 0; i < len(tmpl); i++ {
		if tmpl[i] == '%' && i+1 < len(tmpl) {
			switch tmpl[i+1] {
			case 's', 'v':
				if ai < len(args) {
					out += args[ai]
					ai++
				}
				i++
				continue
			}
		}
		out += string(tmpl[i])
	}
	return out
}

func boolStr(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

// signUp 走真实的邮箱验证码登录，拿到一个非游客账号的令牌。
func (e *testEnv) signUp(email string) (userID, token string) {
	e.t.Helper()
	r := e.do("POST", "/v1/auth/email/request", map[string]any{"email": email}, nil)
	if r.Code != 200 {
		e.t.Fatalf("请求验证码失败: %d %s", r.Code, r.Body)
	}
	r = e.do("POST", "/v1/auth/email/verify",
		map[string]any{"email": email, "code": e.mail.lastCode, "deviceId": "dev-" + email}, nil)
	if r.Code != 200 {
		e.t.Fatalf("验证码登录失败: %d %s", r.Code, r.Body)
	}
	var out ExchangeResult
	r.JSON(e.t, &out)
	return out.User.ID, out.AccessToken
}

// grantUnits 直接往台账里塞额度（绕过四道闸，用于消费侧的测试）。
func (e *testEnv) grantUnits(userID string, units int) {
	e.t.Helper()
	ctx := context.Background()
	bucketID := e.nextID()
	if err := store.InsertBucket(ctx, e.st.Q(), bucketID, userID, "manual", nil, units, nil, e.now); err != nil {
		e.t.Fatal(err)
	}
	if err := store.InsertLedger(ctx, e.st.Q(), e.nextID(), userID, "grant", units, bucketID, nil, nil,
		"grant:manual:"+bucketID, e.now); err != nil {
		e.t.Fatal(err)
	}
}

var _ = time.Now

// fakeJPEG 造 n 字节、带合法 JPEG 魔数（FF D8）的假图。
//
// 🔴 PUT /upload 只校验魔数与长度（不解码），所以这里不需要一张真图 ——
// 真图会让「上限 2 MiB」这类用例必须随图片体积调参，反而更脆。
func fakeJPEG(n int) []byte {
	if n < 2 {
		n = 2
	}
	b := make([]byte, n)
	b[0], b[1] = 0xFF, 0xD8
	for i := 2; i < n; i++ {
		b[i] = byte(i % 251)
	}
	return b
}

// newIntent 开一个上传意向，返回 assetId。
func (e *testEnv) newIntent(token string) string {
	e.t.Helper()
	r := e.do("POST", "/v1/assets/upload-intents",
		map[string]any{"contentType": "image/jpeg", "byteSize": 4096}, bearer(token))
	if r.Code != 200 {
		e.t.Fatalf("开上传意向失败: %d %s", r.Code, r.Body)
	}
	var out UploadIntent
	r.JSON(e.t, &out)
	return out.AssetID
}

// putRaw 打一个请求体是**裸二进制**的 PUT（e.do 只会发 JSON）。
func (e *testEnv) putRaw(path string, body []byte, token string) (int, []byte) {
	e.t.Helper()
	req := httptest.NewRequest("PUT", path, newBytesReader(body))
	req.RemoteAddr = "127.0.0.1:5000"
	req.Header.Set("Content-Type", "image/jpeg")
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	e.app.Handler().ServeHTTP(w, req)
	return w.Code, w.Body.Bytes()
}
