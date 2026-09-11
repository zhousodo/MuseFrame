package httpapi

import (
	"context"

	"museframe-api/internal/store"
)

// resolveIdentity 把一个已验证的第三方身份解析成 user_id，并把在途游客账号
// （它的作品与**已购买**额度）并进去。OAuth 与邮箱登录共用这一条路径，
// 好让游客合并规则（包括「身份已存在」那个分支）永远不会各走各的。
//
// 在这之前，只要 provider subject 已经映射到某个用户，游客 id 就被直接丢掉，
// 把付过钱的额度搁浅在一个没有登录方式的账号上 —— 通过 API 永远够不到。
func (a *App) resolveIdentity(ctx context.Context, c *Ctx, provider, subject, email, name, locale string, deviceID *string) (string, error) {
	var userID string
	var emailPtr *string
	if email != "" {
		emailPtr = &email
	}
	var namePtr *string
	if name != "" {
		namePtr = &name
	}
	now := a.now()

	err := a.st.InTx(ctx, func(q store.Queryer) error {
		existing, err := store.GetIdentityUserID(ctx, q, provider, subject)
		switch {
		case err == nil:
			userID = existing
			if emailPtr != nil {
				if err := store.UpdateIdentityEmail(ctx, q, provider, subject, email); err != nil {
					return err
				}
			}
			// 身份已存在，游客行不能简单升格 —— 把它的内容与付费额度搬过来，
			// 而不是丢掉。
			if c.User != nil && c.User.IsGuest && c.User.ID != userID {
				n, err := store.MergeGuestInto(ctx, q, userID, c.User.ID, now)
				if err != nil {
					return err
				}
				a.lg.Info("merge: 游客并入账号", map[string]any{"buckets": n})
			}
		case store.IsNoRows(err):
			if c.User != nil && c.User.IsGuest {
				userID = c.User.ID
				if err := store.PromoteGuest(ctx, q, userID, namePtr, now); err != nil {
					return err
				}
			} else {
				userID = a.newID()
				if err := store.CreateUser(ctx, q, userID, namePtr, false, locale, now); err != nil {
					return err
				}
			}
			if err := store.InsertIdentity(ctx, q, a.newID(), userID, provider, subject, emailPtr, now); err != nil {
				return err
			}
		default:
			return err
		}

		outcome, err := a.MaybeGrantFree(ctx, q, userID, false, deviceID, c.ClientIP)
		if err != nil {
			return err
		}
		if outcome != OutcomeGranted {
			a.lg.Info("free-grant: 未发放", map[string]any{"reason": string(outcome)})
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	return userID, nil
}
