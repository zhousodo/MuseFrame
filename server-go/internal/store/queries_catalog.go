package store

import (
	"context"
	"time"
)

// PublishedStyleRows 复刻 api.js:publishedStyleRows()：
// 已发布风格 + 其最新已发布版本 + 所属展览，按 editorial_rank, position 排序。
func PublishedStyleRows(ctx context.Context, q Queryer) ([]StyleRow, error) {
	rows, err := q.Query(ctx, `
		SELECT s.id, s.internal_key, s.premium, s.public_name, s.short_caption, s.suitability_tags, s.theme,
		       v.id, v.version, v.spec, e.editorial_rank, e.id, es.position
		FROM styles s
		JOIN style_versions v ON v.style_id = s.id AND v.status = 'published'
		  AND v.version = (SELECT MAX(version) FROM style_versions WHERE style_id = s.id AND status = 'published')
		JOIN exhibition_styles es ON es.style_id = s.id
		JOIN exhibitions e ON e.id = es.exhibition_id
		WHERE s.status = 'published'
		ORDER BY e.editorial_rank ASC, es.position ASC, s.id ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []StyleRow
	for rows.Next() {
		var r StyleRow
		if err := rows.Scan(&r.StyleID, &r.InternalKey, &r.Premium, &r.PublicName, &r.ShortCaption,
			&r.SuitabilityTags, &r.Theme, &r.VersionID, &r.Version, &r.Spec, &r.EditorialRank,
			&r.ExhibitionID, &r.Position); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ListPublishedExhibitions 已发布展览，按 editorial_rank 排序。
func ListPublishedExhibitions(ctx context.Context, q Queryer) ([]Exhibition, error) {
	rows, err := q.Query(ctx,
		`SELECT id, slug, title, curatorial_note, edition, editorial_rank, status, created_at
		 FROM exhibitions WHERE status = 'published' ORDER BY editorial_rank ASC, id ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Exhibition
	for rows.Next() {
		var e Exhibition
		if err := rows.Scan(&e.ID, &e.Slug, &e.Title, &e.CuratorialNote, &e.Edition, &e.EditorialRank,
			&e.Status, &e.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// GetPublishedStyleVersion 取一个可用的风格版本（含 premium 标记）。
// s.status 与 v.status 都要判：紧急下架把 styles.status 置 disabled，
// 少了这个谓词，任何持有缓存 styleVersionId 的客户端还能继续用已下架的风格生成。
func GetPublishedStyleVersion(ctx context.Context, q Queryer, versionID string) (spec []byte, premium bool, err error) {
	err = q.QueryRow(ctx,
		`SELECT v.spec, s.premium FROM style_versions v JOIN styles s ON s.id = v.style_id
		 WHERE v.id = $1 AND v.status = 'published' AND s.status = 'published'`, versionID).Scan(&spec, &premium)
	return
}

// GetStyleVersionSpec 取任意版本的 spec（worker 用）。
func GetStyleVersionSpec(ctx context.Context, q Queryer, versionID string) ([]byte, error) {
	var spec []byte
	err := q.QueryRow(ctx, `SELECT spec FROM style_versions WHERE id = $1`, versionID).Scan(&spec)
	return spec, err
}

// SetStyleStatus 上下架一个风格。
func SetStyleStatus(ctx context.Context, q Queryer, id, status string) (bool, error) {
	tag, err := q.Exec(ctx, `UPDATE styles SET status = $1 WHERE id = $2`, status, id)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() > 0, nil
}

// StyleExists 判断风格是否存在。
func StyleExists(ctx context.Context, q Queryer, id string) (bool, error) {
	var n int
	err := q.QueryRow(ctx, `SELECT 1 FROM styles WHERE id = $1`, id).Scan(&n)
	if IsNoRows(err) {
		return false, nil
	}
	return err == nil, err
}

// ---- 商品 ------------------------------------------------------------------

const productCols = `id, internal_key, product_type, display_name, granted_units, price_minor, currency,
	period, feature_flags, active, google_product_id, apple_product_id, price_cny_minor`

func scanProduct(row interface{ Scan(...any) error }) (*Product, error) {
	var p Product
	err := row.Scan(&p.ID, &p.InternalKey, &p.ProductType, &p.DisplayName, &p.GrantedUnits, &p.PriceMinor,
		&p.Currency, &p.Period, &p.FeatureFlags, &p.Active, &p.GoogleProductID, &p.AppleProductID, &p.PriceCnyMinor)
	if err != nil {
		return nil, err
	}
	return &p, nil
}

// ListActiveProducts 上架商品目录。
//
// 🔴 行序是契约。Node 版的 SQL 是 `SELECT * FROM products WHERE active = 1`，
// **没有 ORDER BY**，行序恰好等于 SQLite rowid（插入顺序），实测为
// creator_monthly, pack_10, pack_30, pack_100。
// PG 的堆表顺序会随 UPDATE 漂，所以必须显式固定。下面这个排序键
// （订阅在前 → 价格升序 → internal_key）在当前目录上**逐字复现**线上行序，
// 且对新商品仍然确定。见 45 号报告的「排序黄金测试」。
func ListActiveProducts(ctx context.Context, q Queryer) ([]Product, error) {
	return queryProducts(ctx, q, `SELECT `+productCols+` FROM products WHERE active = true
		ORDER BY CASE product_type WHEN 'subscription' THEN 0 ELSE 1 END ASC, price_minor ASC, internal_key ASC`)
}

// ListAllProducts 管理后台商品列表：ORDER BY product_type, price_minor（+ internal_key 兜底确定性）。
func ListAllProducts(ctx context.Context, q Queryer) ([]Product, error) {
	return queryProducts(ctx, q, `SELECT `+productCols+` FROM products ORDER BY product_type ASC, price_minor ASC, internal_key ASC`)
}

func queryProducts(ctx context.Context, q Queryer, sql string) ([]Product, error) {
	rows, err := q.Query(ctx, sql)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Product
	for rows.Next() {
		p, err := scanProduct(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *p)
	}
	return out, rows.Err()
}

// GetActiveProductByKey 按 internal_key 取上架商品。
func GetActiveProductByKey(ctx context.Context, q Queryer, key string) (*Product, error) {
	return scanProduct(q.QueryRow(ctx, `SELECT `+productCols+` FROM products WHERE internal_key = $1 AND active = true`, key))
}

// GetProductByKey 按 internal_key 取商品（含下架）。
func GetProductByKey(ctx context.Context, q Queryer, key string) (*Product, error) {
	return scanProduct(q.QueryRow(ctx, `SELECT `+productCols+` FROM products WHERE internal_key = $1`, key))
}

// ProductUpdate 是商品可编辑字段的补丁。nil = 这次不改这一项。
//
// 🔴 三个「可为 null 的列」用 (SetX bool, X *T) 二元组表达，不能只用指针：
// 指针的 nil 同时要表达「不改」和「改成 SQL NULL」两件事，而这两件事在
// price_cny_minor 上的后果差一个数量级 —— 清空人民币价会让这个商品对
// **所有中文用户彻底消失**（web/app.js 的 offeredProducts() 过滤掉
// priceCnyMinor == null 的行），清空 SKU 则会让商店内购按钮点下去没反应。
type ProductUpdate struct {
	DisplayName   *string
	GrantedUnits  *int
	PriceMinor    *int64
	SetCny        bool
	PriceCnyMinor *int64
	Active        *bool
	SetGoogleSKU  bool
	GoogleSKU     *string
	SetAppleSKU   bool
	AppleSKU      *string
}

// UpdateProductFields 按后台补丁改商品。
//
// 🔴 必须在一个事务里。原来是一串各自独立的 UPDATE：改价成功、改张数失败会留下
// 「新价格 + 旧张数」的上架商品，而后台那一次保存回的是错误。
// 对一个正在卖的价目表来说，这个中间态每多存在一秒就可能卖错一单。
func UpdateProductFields(ctx context.Context, st *Store, key string, u ProductUpdate) error {
	return st.InTx(ctx, func(q Queryer) error {
		set := func(col string, val any) error {
			_, err := q.Exec(ctx, `UPDATE products SET `+col+` = $1 WHERE internal_key = $2`, val, key)
			return err
		}
		if u.DisplayName != nil {
			if err := set("display_name", *u.DisplayName); err != nil {
				return err
			}
		}
		if u.GrantedUnits != nil {
			if err := set("granted_units", *u.GrantedUnits); err != nil {
				return err
			}
		}
		if u.PriceMinor != nil {
			if err := set("price_minor", *u.PriceMinor); err != nil {
				return err
			}
		}
		if u.SetCny {
			if err := set("price_cny_minor", u.PriceCnyMinor); err != nil {
				return err
			}
		}
		if u.SetGoogleSKU {
			if err := set("google_product_id", u.GoogleSKU); err != nil {
				return err
			}
		}
		if u.SetAppleSKU {
			if err := set("apple_product_id", u.AppleSKU); err != nil {
				return err
			}
		}
		if u.Active != nil {
			if err := set("active", *u.Active); err != nil {
				return err
			}
		}
		return nil
	})
}

// UserPlan 返回用户当前计划：有效订阅的 internal_key，否则 'free'。
func UserPlan(ctx context.Context, q Queryer, userID string, now time.Time) (string, error) {
	var key string
	err := q.QueryRow(ctx, `
		SELECT p.internal_key FROM purchases pu
		JOIN products p ON p.id = pu.product_id
		WHERE pu.user_id = $1 AND p.product_type = 'subscription' AND pu.status = 'verified'
		  AND (pu.expires_at IS NULL OR pu.expires_at > $2)
		ORDER BY pu.purchased_at DESC LIMIT 1`, userID, now).Scan(&key)
	if IsNoRows(err) {
		return "free", nil
	}
	if err != nil {
		return "free", err
	}
	return key, nil
}

// StyleNameOfVersion 按版本 id 取风格名（项目列表用）。
func StyleNameOfVersion(ctx context.Context, q Queryer, versionID string) (string, error) {
	var name string
	err := q.QueryRow(ctx,
		`SELECT s.public_name FROM style_versions v JOIN styles s ON s.id = v.style_id WHERE v.id = $1`,
		versionID).Scan(&name)
	return name, err
}

// CandidateAssetID 按候选 id 取资产 id。
func CandidateAssetID(ctx context.Context, q Queryer, candidateID string) (string, error) {
	var id string
	err := q.QueryRow(ctx, `SELECT asset_id FROM generation_candidates WHERE id = $1`, candidateID).Scan(&id)
	return id, err
}
