package httpapi

import (
	"regexp"
	"strconv"
	"strings"
	"time"

	"museframe-api/internal/apierr"
	"museframe-api/internal/ledger"
	"museframe-api/internal/store"
)

var idemGrantRe = regexp.MustCompile(`^[\w.-]{8,80}$`)
var ctrlRe = regexp.MustCompile(`[\r\n\x00-\x1f\x7f]+`)

// AdminGrantResult 是 POST /v1/admin/users/grant 的出参。
type AdminGrantResult struct {
	OK             bool    `json:"ok"`
	Replayed       bool    `json:"replayed,omitempty"`
	UserID         string  `json:"userId"`
	IsGuest        bool    `json:"isGuest"`
	Granted        int     `json:"granted"`
	BucketID       *string `json:"bucketId"`
	AvailableUnits int     `json:"availableUnits"`
	ExpiresAt      *string `json:"expiresAt"`
}

// hAdminGrant 是手动加额度（「额度用完 -> 邮件联系 -> 后台充值」流程的落点）。
// 只走台账的正规 grant 路径：append-only、有 reference_key，和购买发放同一套账。
// 目标用户用完整 ID 或邮箱指定；邮箱命中多个账号时拒绝，避免充错人。
func (a *App) hAdminGrant(c *Ctx) (any, error) {
	if err := a.requireAdmin(c); err != nil {
		return nil, err
	}
	ctx := c.R.Context()

	var idemKey *string
	if raw, ok := c.Body["idempotencyKey"]; ok && raw != nil {
		s, _ := raw.(string)
		if !idemGrantRe.MatchString(s) {
			return nil, apierr.New(422, apierr.CodeValidation, "idempotencyKey must be 8-80 chars of [A-Za-z0-9_.-].")
		}
		idemKey = &s
	}
	unitsRaw, _, err := optionalInt(c.Body, "units")
	if err != nil || unitsRaw == nil || *unitsRaw <= 0 || *unitsRaw > 10000 {
		return nil, apierr.New(422, apierr.CodeValidation, "units must be an integer between 1 and 10000.")
	}
	units := int(*unitsRaw)
	var note *string
	if raw, ok := c.Body["note"]; ok && raw != nil {
		s, ok := raw.(string)
		if !ok || len(s) > 300 {
			return nil, apierr.New(422, apierr.CodeValidation, "note must be a string of at most 300 chars.")
		}
		cleaned := ctrlRe.ReplaceAllString(trimSpace(s), " ")
		if cleaned != "" {
			note = &cleaned
		}
	}
	var expiresAt *time.Time
	if raw, ok := c.Body["expiresInDays"]; ok && raw != nil {
		days, _, err := optionalInt(c.Body, "expiresInDays")
		if err != nil || days == nil || *days <= 0 || *days > 3650 {
			return nil, apierr.New(422, apierr.CodeValidation, "expiresInDays must be an integer between 1 and 3650.")
		}
		t := a.now().Add(time.Duration(*days) * 24 * time.Hour)
		expiresAt = &t
	}

	var targetID string
	var targetGuest bool
	userIDRaw, _ := c.Body["userId"].(string)
	emailRaw, _ := c.Body["email"].(string)
	switch {
	case trimSpace(userIDRaw) != "":
		id, guest, err := store.FindUserByID(ctx, a.st.Q(), trimSpace(userIDRaw))
		if err != nil {
			if store.IsNoRows(err) {
				return nil, notFound("No user with that id.")
			}
			return nil, err
		}
		targetID, targetGuest = id, guest
	case trimSpace(emailRaw) != "":
		ids, guests, err := store.FindUsersByEmail(ctx, a.st.Q(), strings.ToLower(trimSpace(emailRaw)))
		if err != nil {
			return nil, err
		}
		if len(ids) == 0 {
			return nil, notFound("No user with that email.")
		}
		if len(ids) > 1 {
			return nil, apierr.New(409, apierr.CodeAmbiguous,
				"That email matches "+strconv.Itoa(len(ids))+" accounts — grant by userId instead.")
		}
		targetID, targetGuest = ids[0], guests[0]
	default:
		return nil, apierr.New(422, apierr.CodeValidation, "Provide userId or email.")
	}

	// 防重复提交：面板每次点击带一个新键；同一次点击的重试返回原来的发放，而不是再充一次。
	if idemKey != nil {
		_, priorUser, priorUnits, err := store.GetManualGrantByKey(ctx, a.st.Q(), *idemKey)
		if err == nil {
			if priorUser != targetID || priorUnits != units {
				return nil, apierr.New(409, apierr.CodeIdempotencyMismatch,
					"That idempotency key was used for a different grant.")
			}
			avail, err := store.AvailableUnits(ctx, a.st.Q(), targetID, a.now())
			if err != nil {
				return nil, err
			}
			return AdminGrantResult{OK: true, Replayed: true, UserID: targetID, IsGuest: targetGuest,
				Granted: units, BucketID: nil, AvailableUnits: avail, ExpiresAt: store.ISOPtr(expiresAt)}, nil
		} else if !store.IsNoRows(err) {
			return nil, err
		}
	}

	grantID := a.newID()
	var bucketID string
	// 🔴 额度与它的审计行同生共死：同一个事务，否则充值没有审计。
	err = a.st.InTx(ctx, func(q store.Queryer) error {
		b, err := ledger.Grant(ctx, q, a.newID, targetID, units, "manual", &grantID, expiresAt, nil, a.now())
		if err != nil {
			return err
		}
		bucketID = b
		return store.InsertManualGrant(ctx, q, grantID, targetID, units, note, expiresAt, idemKey, a.now())
	})
	if err != nil {
		return nil, err
	}
	avail, err := store.AvailableUnits(ctx, a.st.Q(), targetID, a.now())
	if err != nil {
		return nil, err
	}
	return AdminGrantResult{OK: true, UserID: targetID, IsGuest: targetGuest, Granted: units,
		BucketID: &bucketID, AvailableUnits: avail, ExpiresAt: store.ISOPtr(expiresAt)}, nil
}

// hAdminEmailTest 发一封测试邮件，验证 SMTP 配置是否可用。
func (a *App) hAdminEmailTest(c *Ctx) (any, error) {
	if err := a.requireAdmin(c); err != nil {
		return nil, err
	}
	to := trimSpace(str(c.Body["to"]))
	if !emailRe.MatchString(to) {
		return nil, apierr.New(422, apierr.CodeValidation, "请输入有效的收件邮箱。")
	}
	if a.mailer == nil || !a.mailer.Configured() {
		return nil, apierr.New(400, apierr.CodeSMTPNotConfigured, "SMTP 未配置。")
	}
	accepted, err := a.mailer.Send(to, "MuseFrame 邮件配置测试",
		"这是一封来自 MuseFrame 管理后台的测试邮件，收到即说明 SMTP 配置正常。", "")
	a.recordEmailSend(c.R.Context(), "admin_test", to, err)
	if err != nil {
		return nil, apierr.New(502, apierr.CodeEmailSendFailed, "发送失败。")
	}
	return map[string]any{"ok": true, "accepted": accepted}, nil
}

func str(v any) string {
	s, _ := v.(string)
	return s
}
