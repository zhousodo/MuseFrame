package httpapi

import (
	"context"

	"museframe-api/internal/ledger"
	"museframe-api/internal/store"
)

// GrantOutcome 是 maybeGrantFree 的结论。
type GrantOutcome string

// 发放侧四道闸的拒绝原因（按「最便宜的先判」的顺序）。
const (
	OutcomeFreeUnitsZero  GrantOutcome = "FREE_UNITS_ZERO"
	OutcomeRequiresAuth   GrantOutcome = "REQUIRES_AUTH"
	OutcomeNoDeviceID     GrantOutcome = "NO_DEVICE_ID"
	OutcomeAlreadyClaimed GrantOutcome = "ALREADY_CLAIMED"
	OutcomeGrantsDisabled GrantOutcome = "GRANTS_DISABLED"
	OutcomeSiteCap        GrantOutcome = "SITE_CAP"
	OutcomeIPCap          GrantOutcome = "IP_CAP"
	OutcomeGranted        GrantOutcome = "GRANTED"
)

// MaybeGrantFree 发那张免费的图 —— 刻意地、幂等地、带天花板地发。
//
// 一个游客令牌的铸造成本是零（POST /v1/auth/exchange 接受空 body），
// 所以任何只以账号为键的限制根本不是限制：循环调 exchange，每个新 user id
// 都能在付费模型上再领一次生成。旧的去重在没有 deviceId 时**回落到 user id**，
// 而匿名调用者恰恰就是那一类 —— 循环完全敞开。
//
// 现在是四道闸，最便宜的先判。除设备哈希外全部是服务端可观测的；
// 设备哈希只用来**让发放更难**，从不用来**准许发放**：
//  1. 游客没有设备指纹 -> 一张都不发（不回落 user id）；
//  2. 该设备 / 身份此生只能领一次；
//  3. per-IP 滚动 24h 上限（free_grants_per_ip_day）；
//  4. 全站滚动 24h 上限（free_grants_per_day）—— 攻击者轮换 IP 时的总熔断。
//
// 拒发只跳过发放：账号照建、可登录、可购买、可浏览。
func (a *App) MaybeGrantFree(ctx context.Context, q store.Queryer, userID string, isGuest bool, deviceID *string, clientIP string) (GrantOutcome, error) {
	freeUnits := a.rt.Int("free_units")
	if freeUnits <= 0 {
		return OutcomeFreeUnitsZero, nil
	}
	if isGuest && a.rt.Bool("free_requires_auth") {
		return OutcomeRequiresAuth, nil
	}

	var deviceHash *string
	if deviceID != nil && *deviceID != "" {
		h := hash24(*deviceID, "")
		deviceHash = &h
	}
	// 闸 1：游客必须出示设备指纹。没有指纹就没有可去重的键，
	// 而「没有去重键」必须意味着「不发免费额度」，绝不能意味着「免费送」。
	if isGuest && deviceHash == nil {
		return OutcomeNoDeviceID, nil
	}
	dedupeID := userID
	if isGuest {
		dedupeID = *deviceHash
	}
	// 去重键曾经会在游客登录时**切换**（游客期是设备哈希，登录后是 user id），
	// 而合并保留同一条 user 行，于是 user-id 这个键从未被记录过，
	// 同一台设备在登录时又领了第二张。现在两个方向的键都查、也都记。
	keys := uniqueKeys(dedupeID, deviceHash, userID)
	for _, k := range keys {
		exists, err := store.FreeGrantDedupeExists(ctx, q, k)
		if err != nil {
			return "", err
		}
		if exists {
			return OutcomeAlreadyClaimed, nil
		}
		// 老库里的历史发放先于 free_grants 表存在，继续认它们的去重键。
		exists, err = store.LedgerReferenceExists(ctx, q, "grant:free_grant:"+k)
		if err != nil {
			return "", err
		}
		if exists {
			return OutcomeAlreadyClaimed, nil
		}
	}

	perIP := a.rt.Int("free_grants_per_ip_day")
	perDay := a.rt.Int("free_grants_per_day")
	if perDay <= 0 || perIP <= 0 {
		return OutcomeGrantsDisabled, nil
	}
	// 未知地址共用一个桶，而不是豁免于上限。
	src := clientIP
	if src == "" {
		src = "unknown"
	}
	ipHash := hash24(src, a.ipSalt)
	now := a.now()
	w, err := store.GetFreeGrantWindow(ctx, q, &ipHash, now)
	if err != nil {
		return "", err
	}
	if w.Today >= perDay {
		return OutcomeSiteCap, nil
	}
	if w.ForIP >= perIP {
		return OutcomeIPCap, nil
	}

	// 🔴 有效期来自注册表热键 free_credit_expiry_days，默认 0 = nil = 永不过期
	//    （与此前写死的 nil 完全一致，所以这次改动对现状是零行为变更）。
	//    它只影响**此后**新发放的桶；已发出去的 credit_buckets 行不会被追溯改写，
	//    否则一次误操作就能把所有人手里的免费额度一起作废。
	expiry := a.rt.CreditExpiry("free_credit_expiry_days", now)
	if _, err := ledger.Grant(ctx, q, a.newID, userID, freeUnits, "free_grant", &dedupeID, expiry, nil, now); err != nil {
		return "", err
	}
	// 每个键各写一行，让**另一个**键再也领不到
	// （今天是设备，游客合并之后是账号 —— 反正是同一个人）。
	for _, k := range keys {
		units := 0
		if k == dedupeID {
			units = freeUnits
		}
		if err := store.InsertFreeGrant(ctx, q, a.newID(), userID, k, deviceHash, &ipHash, units, now); err != nil {
			return "", err
		}
	}
	return OutcomeGranted, nil
}

func uniqueKeys(dedupeID string, deviceHash *string, userID string) []string {
	var out []string
	seen := map[string]bool{}
	add := func(s string) {
		if s == "" || seen[s] {
			return
		}
		seen[s] = true
		out = append(out, s)
	}
	add(dedupeID)
	if deviceHash != nil {
		add(*deviceHash)
	}
	add(userID)
	return out
}
