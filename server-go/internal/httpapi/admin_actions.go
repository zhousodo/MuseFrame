// 后台的三个**写**入口：反馈标已处理、失败任务重试、购买重验。
// 每一个都落一条审计（admin.*），每一个都先校验再动。
package httpapi

import (
	"context"
	"encoding/json"
	"time"
	"unicode/utf8"

	"museframe-api/internal/apierr"
	"museframe-api/internal/ledger"
	"museframe-api/internal/store"
)

// MaxHandledNote 是「已处理」备注的长度上限（字符）。
const MaxHandledNote = 500

// ---- 反馈：标记已处理 ------------------------------------------------------

// hAdminFeedbackHandled 是 POST /v1/admin/feedback/{id}/handled。
// body: {"handled": true|false, "note": "可选备注"}
func (a *App) hAdminFeedbackHandled(c *Ctx) (any, error) {
	if err := a.requireAdmin(c); err != nil {
		return nil, err
	}
	ctx := c.R.Context()
	id := c.Params[0]

	raw, ok := c.Body["handled"]
	if !ok {
		return nil, apierr.New(422, apierr.CodeValidation, "handled 必填（true 标记已处理，false 取消）。")
	}
	handled, ok := raw.(bool)
	if !ok {
		return nil, apierr.New(422, apierr.CodeValidation, "handled 必须是布尔值。")
	}

	var note *string
	if handled {
		// 取消标记时带 note 一律忽略：SetFeedbackHandled 在 handled=false 时
		// 会把 note 清空，收一个注定被丢掉的值只会让调用方以为它存下来了。
		n, err := adminText(c.Body, "note", MaxHandledNote)
		if err != nil {
			return nil, err
		}
		if n != nil && *n != "" {
			note = n
		}
	}

	n, err := store.SetFeedbackHandled(ctx, a.st.Q(), id, handled, note, a.now())
	if err != nil {
		return nil, err
	}
	// 0 行必须报 404 而不是静默成功：运营点了「已处理」，页面说成了，
	// 刷新之后那条还在未处理里 —— 然后他会以为后台坏了，而真正的原因是 id 不存在。
	if n == 0 {
		return nil, notFound("Unknown feedback row.")
	}
	props := map[string]any{"feedbackId": id, "handled": handled}
	if note != nil {
		props["note"] = *note
	}
	logged := a.audit(c, "feedback_handled", props)
	unhandled, err := store.CountFeedbackUnhandled(ctx, a.st.Q())
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"ok": true, "handled": handled, "unhandled": unhandled, "auditLogged": logged,
	}, nil
}

// ---- 任务：重试 ------------------------------------------------------------

// hAdminJobRetry 是 POST /v1/admin/jobs/{id}/retry。
//
// 🔴 重试**新建一个任务**，绝不重排原来那一条。原因是额度台账的语义：
// 任务失败时 ledger.Release 写的是**补偿分录**，原来的 reserve 行还留在表里。
// 于是把原任务改回 queued 再入队时：
//
//	ledger.Reserve  看到 `job:<id>:reserve` 前缀已存在 -> 直接 return nil（不扣）
//	LedgerHasReserve 看到 reserve 行还在           -> 放行执行
//
// 两个守卫都通过，而钱在失败那一刻已经退给用户了 —— 净结果是一张**免费的图**，
// 而且额度对账表上看不出任何异常。新建任务走的是和 POST /v1/generation-jobs
// 完全相同的「建行 + 预留」事务，每一次重试都有自己的一笔扣减。
//
// 用户不会被重复扣钱：失败时退过一次，重试时扣回来，净额仍是「成功一张扣一份」。
// 余额不足就拒绝并告诉运营先发额度 —— 悄悄跳过扣减等于回到上面那个免费图。
func (a *App) hAdminJobRetry(c *Ctx) (any, error) {
	if err := a.requireAdmin(c); err != nil {
		return nil, err
	}
	ctx := c.R.Context()
	old, err := store.GetJob(ctx, a.st.Q(), c.Params[0])
	if err != nil {
		if store.IsNoRows(err) {
			return nil, notFound("Unknown job.")
		}
		return nil, err
	}
	// 只重试终态的失败 / 取消。重试一个还在跑的任务会变成两个任务抢同一个
	// 项目的 selected_candidate，而运营在列表上看不出哪一个赢了。
	if old.Status != "failed" && old.Status != "cancelled" {
		return nil, apierr.WithDetails(409, apierr.CodeValidation,
			"只有 failed / cancelled 的任务能重试（当前："+old.Status+"）。",
			map[string]any{"status": old.Status})
	}

	units := old.ReservedUnits
	if units < 0 {
		units = 0
	}
	t := a.now()
	newJobID := a.newID()
	var after int
	err = a.st.InTx(ctx, func(q store.Queryer) error {
		if err := store.InsertJob(ctx, q, &store.Job{
			ID: newJobID, UserID: old.UserID, ProjectID: old.ProjectID,
			SourceAssetID: old.SourceAssetID, StyleVersionID: old.StyleVersionID,
			// parent_job_id 指向被重试的那一条：列表上的「重试次数」就是数它。
			ParentJobID: &old.ID,
			Status:      "queued", Stage: "preparing",
			Controls: old.Controls, Output: old.Output,
			ReservedUnits: units, CreatedAt: t, UpdatedAt: t,
		}); err != nil {
			return err
		}
		if units > 0 {
			if err := ledger.Reserve(ctx, q, a.newID, old.UserID, newJobID, units, t); err != nil {
				return err
			}
		}
		if err := store.SetProjectStatus(ctx, q, old.ProjectID, "generating", t); err != nil {
			return err
		}
		bal, err := store.AvailableUnits(ctx, q, old.UserID, t)
		if err != nil {
			return err
		}
		after = bal
		return nil
	})
	if err != nil {
		var ins *ledger.ErrInsufficient
		if asErr(err, &ins) {
			return nil, apierr.WithDetails(409, apierr.CodeInsufficientEntitle,
				"这个用户的额度不够重试（需要 "+itoa(ins.RequiredUnits)+"，可用 "+itoa(ins.AvailableUnits)+
					"）。先在用户页发放额度，再重试。",
				map[string]any{"requiredUnits": ins.RequiredUnits, "availableUnits": ins.AvailableUnits})
		}
		return nil, err
	}
	// 入队必须在事务提交**之后**：提交前入队时，worker 可能先跑起来、
	// 读不到还没可见的任务行，然后把一个其实存在的任务当成「不存在」。
	a.worker.Enqueue(newJobID)

	logged := a.audit(c, "job_retry", map[string]any{
		"jobId": old.ID, "newJobId": newJobID, "userId": old.UserID,
		"units": units, "previousStatus": old.Status,
		"previousErrorCode": derefStr(old.ErrorCode),
	})
	return map[string]any{
		"ok": true, "jobId": newJobID, "parentJobId": old.ID,
		"reservedUnits": units, "availableUnits": after, "auditLogged": logged,
	}, nil
}

// ---- 购买：重验 ------------------------------------------------------------

// hAdminPurchaseReverify 是 POST /v1/admin/purchases/{id}/reverify。
//
// 🔴 它**只**补发缺失的额度，绝不重新向商店要一次收据 ——
// 后台手里没有购买令牌（purchaseToken 只在 App 那一次请求里出现过，从不落库），
// 所以「重新找 Google/Apple 验一次」在服务端做不到。
// 能做、也是运营真正需要的那件事是：这笔已核验（verified）的购买，额度入账了吗？
//
// 🔴 刻意**不**复用 finalizePurchase。那条路径对一个已经 verified 的购买只在
// 「到期时间变新了」时才发额度（自动续订的每个计费周期）；对一个没有到期时间的
// 点数包（expires 恒为 nil，fresher 恒为假）它会直接 return 早退 —— 也就是说
// 走 finalizePurchase 的「重验」对消耗型商品**永远补不上**，是一个点了必然
// 回 200 且什么都不做的按钮。这里直接对着幂等键判断并发放。
func (a *App) hAdminPurchaseReverify(c *Ctx) (any, error) {
	if err := a.requireAdmin(c); err != nil {
		return nil, err
	}
	ctx := c.R.Context()
	id := c.Params[0]
	userID, productID, platform, _, status, err := store.GetPurchaseForReverify(ctx, a.st.Q(), id)
	if err != nil {
		if store.IsNoRows(err) {
			return nil, notFound("Unknown purchase.")
		}
		return nil, err
	}
	// 🔴 这里的终态是 **verified**，不是 active。purchases.status 的词汇表只有
	// 两个值：recordPendingPurchase 写 'pending'、MarkPurchaseVerified /
	// InsertPurchase 写 'verified'（生产库里那一笔就是 verified/web）。
	// 判据最初写的是 status != "active"，那会让**每一笔真实购买**都被拒成 409 ——
	// 一个点了没反应的按钮，而漏入账的单子永远补不上。
	// pending 的还没核验过，给它发额度等于白送。
	if status != "verified" {
		return nil, apierr.WithDetails(409, apierr.CodeValidation,
			"只有已核验（verified）的购买能重验（当前："+status+"）。",
			map[string]any{"status": status})
	}
	product, err := store.GetProductByID(ctx, a.st.Q(), productID)
	if err != nil {
		if store.IsNoRows(err) {
			return nil, notFound("Unknown product.")
		}
		return nil, err
	}

	before, err := store.AvailableUnits(ctx, a.st.Q(), userID, a.now())
	if err != nil {
		return nil, err
	}
	// 幂等键和 ledger.Grant 为这笔购买生成的那一个**完全一致**
	// （grant:<sourceType>:<sourceID>，sourceID 就是 purchaseID）。
	// 先查再发，而不是「发了撞唯一约束再回滚」：后者虽然也安全（整笔事务回滚），
	// 但它会把一次正常的幂等重验变成一个 500，运营看到的是「重验失败」。
	granted, err := store.LedgerRefExistsForUser(ctx, a.st.Q(), userID, "grant:purchase:"+id)
	if err != nil {
		return nil, err
	}
	if !granted {
		if err := a.st.InTx(ctx, func(q store.Queryer) error {
			// expires 传 nil：消耗型点数包的额度有效期由注册表热键
			// pack_credit_expiry_days 决定（grantPurchaseUnits 里读），
			// 订阅型则跟随 purchases.expires_at —— 这里不自己编一个。
			return a.grantPurchaseUnits(ctx, q, userID, product, id, nil, nil)
		}); err != nil {
			return nil, err
		}
	}
	after, err := store.AvailableUnits(ctx, a.st.Q(), userID, a.now())
	if err != nil {
		return nil, err
	}
	logged := a.audit(c, "purchase_reverify", map[string]any{
		"purchaseId": id, "userId": userID, "platform": platform,
		"alreadyGranted": granted,
		"unitsBefore":    before, "unitsAfter": after, "granted": after - before,
	})
	return map[string]any{
		"ok": true, "purchaseId": id, "unitsBefore": before, "unitsAfter": after,
		"granted": after - before, "alreadyGranted": granted, "auditLogged": logged,
		// 额度没变是**正常**结果（幂等命中：这笔钱早就入过账）。
		// 不说这句，运营会把「granted: 0」读成「重验失败」然后一直点。
		"message": reverifyMessage(after - before),
	}, nil
}

func reverifyMessage(delta int) string {
	if delta > 0 {
		return "补发了 " + itoa(delta) + " 份额度（此前漏入账）。"
	}
	return "额度未变化 —— 这笔购买此前已经正确入账（幂等命中），无需处理。"
}

// asErr 是 errors.As 的本地包装，避免在本文件再 import errors。
func asErr[T error](err error, target *T) bool {
	for err != nil {
		if v, ok := err.(T); ok {
			*target = v
			return true
		}
		u, ok := err.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		err = u.Unwrap()
	}
	return false
}

func derefStr(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// ---- 发信记录落库 ----------------------------------------------------------

// recordEmailSend 把一次发信尝试落到 events 表。
//
// 🔴 落库失败不能让发信这件事失败：验证码已经发出去了（或者已经失败了），
// 把一条记录写不进去变成 500 会让用户收到了码却看到一个错误页。
// 只记一行 warn。
func (a *App) recordEmailSend(ctx context.Context, kind, to string, sendErr error) {
	errText := ""
	if sendErr != nil {
		errText = truncateRunes(sendErr.Error(), 300)
	}
	if err := store.InsertEmailSend(ctx, a.st.Q(), a.newID(), kind,
		store.MaskEmail(to), sendErr == nil, errText, a.now()); err != nil {
		a.lg.Warn("email: 发送记录落库失败（邮件本身已处理）", map[string]any{
			"kind": kind, "error": err.Error(),
		})
	}
}

var _ = json.Marshal
var _ = utf8.RuneCountInString
var _ = time.Now
