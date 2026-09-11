package httpapi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"strings"

	"museframe-api/internal/apierr"
	"museframe-api/internal/oidc"
	"museframe-api/internal/store"
)

// ExchangeResult 是 POST /v1/auth/exchange 的出参。
type ExchangeResult struct {
	AccessToken string       `json:"accessToken"`
	User        ExchangeUser `json:"user"`
}

// ExchangeUser 是出参里的 user 块。
type ExchangeUser struct {
	ID          string  `json:"id"`
	IsGuest     bool    `json:"isGuest"`
	DisplayName *string `json:"displayName"`
	Email       string  `json:"email,omitempty"`
}

// hAuthExchange 是 POST /v1/auth/exchange。
// 🔴 这条是 2026-08-31「游客无限白嫖」漏洞的入口：空 body 曾经直接发令牌 + 免费额度。
func (a *App) hAuthExchange(c *Ctx) (any, error) {
	ctx := c.R.Context()
	// JSON 没有类型。`deviceId: {}` 曾经一路绑进 SQL 抛 TypeError -> 500，
	// 而此时 createUser() 已经提交了一行；`locale: []` 同理。先拒。
	deviceID, err := optionalString(c.Body, "deviceId", 200)
	if err != nil {
		return nil, err
	}
	locale, err := optionalString(c.Body, "locale", 40)
	if err != nil {
		return nil, err
	}
	displayName, err := optionalString(c.Body, "displayName", 120)
	if err != nil {
		return nil, err
	}
	identityToken, err := optionalString(c.Body, "identityToken", 8192)
	if err != nil {
		return nil, err
	}
	providerPtr, err := optionalString(c.Body, "provider", 20)
	if err != nil {
		return nil, err
	}
	providerName := ""
	if providerPtr != nil {
		providerName = *providerPtr
	}

	var userID string
	localeVal := ""
	if locale != nil {
		localeVal = *locale
	}

	switch providerName {
	case "", "guest":
		if !a.rt.Bool("allow_guest") {
			return nil, apierr.New(http.StatusForbidden, apierr.CodeAuthRequired, "Sign in to continue.")
		}
		id := a.newID()
		err := a.st.InTx(ctx, func(q store.Queryer) error {
			if err := store.CreateUser(ctx, q, id, displayName, true, localeVal, a.now()); err != nil {
				return err
			}
			outcome, err := a.MaybeGrantFree(ctx, q, id, true, deviceID, c.ClientIP)
			if err != nil {
				return err
			}
			if outcome != OutcomeGranted {
				a.lg.Info("free-grant: 未发放", map[string]any{"reason": string(outcome)})
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
		userID = id

	case "google", "apple", "dev":
		claims, storedProvider, err := a.resolveClaims(ctx, c, providerName, identityToken)
		if err != nil {
			return nil, err
		}
		name := claims.Name
		if displayName != nil && *displayName != "" {
			name = *displayName
		}
		if name == "" {
			name = claims.Email
		}
		userID, err = a.resolveIdentity(ctx, c, storedProvider, claims.Subject, claims.Email, name, localeVal, deviceID)
		if err != nil {
			return nil, err
		}

	default:
		return nil, apierr.New(422, apierr.CodeValidation, "Unknown provider.")
	}

	token := randomToken()
	if err := store.CreateSession(ctx, a.st.Q(), token, userID, deviceID, a.now(), a.cfg.SessionTTLDays); err != nil {
		return nil, err
	}
	u, err := store.GetUserAny(ctx, a.st.Q(), userID)
	if err != nil {
		return nil, err
	}
	return ExchangeResult{AccessToken: token, User: ExchangeUser{ID: u.ID, IsGuest: u.IsGuest, DisplayName: u.DisplayName}}, nil
}

// resolveClaims 验身份令牌。dev 分支是**双闸**：旗标 + 管理员令牌。
// 生产里忘关的旗标，单独就是「任何人都能用任意邮箱铸一个已登录账号」的水龙头 ——
// 那同时也绕过了 free_requires_auth。
func (a *App) resolveClaims(ctx context.Context, c *Ctx, providerName string, identityToken *string) (*oidc.Claims, string, error) {
	if providerName == "dev" {
		if !a.cfg.AllowTestLogin || !a.isAdminRequest(c.R) {
			return nil, "", apierr.New(http.StatusForbidden, apierr.CodeAuthInvalid, "Test login disabled.")
		}
		emailPtr, err := optionalString(c.Body, "email", 254)
		if err != nil {
			return nil, "", err
		}
		email := ""
		if emailPtr != nil {
			email = strings.ToLower(trimSpace(*emailPtr))
		}
		if email == "" {
			return nil, "", apierr.New(422, apierr.CodeValidation, "email required for test login.")
		}
		sum := sha256.Sum256([]byte(email))
		name := email
		if i := strings.Index(email, "@"); i > 0 {
			name = email[:i]
		}
		// 记在一个真实 provider 行下，后台列表才正常显示。
		return &oidc.Claims{Subject: "dev:" + hex.EncodeToString(sum[:])[:16], Email: email, Name: name}, "google", nil
	}

	tok := ""
	if identityToken != nil {
		tok = *identityToken
	}
	var claims *oidc.Claims
	var err error
	if providerName == "google" {
		claims, err = a.verifier.VerifyGoogle(ctx, tok, splitCSV(a.rt.String("google_client_ids")))
	} else {
		claims, err = a.verifier.VerifyApple(ctx, tok, splitCSV(a.rt.String("apple_bundle_ids")))
	}
	if err != nil {
		switch {
		case errors.Is(err, oidc.ErrNotConfigured):
			return nil, "", apierr.New(501, apierr.CodeProviderNotConfigured, providerName+" sign-in is not configured on the server.")
		case errors.Is(err, oidc.ErrJWKSUnavailable):
			a.lg.Warn("auth: 签发方公钥服务不可达", map[string]any{"provider": providerName})
			return nil, "", apierr.New(503, apierr.CodeVerificationUnavail, "Sign-in is temporarily unavailable. Please try again.")
		default:
			return nil, "", apierr.New(http.StatusUnauthorized, apierr.CodeAuthInvalid, "Sign-in could not be verified.")
		}
	}
	return claims, providerName, nil
}

func splitCSV(s string) []string {
	var out []string
	for _, v := range strings.Split(s, ",") {
		if v = trimSpace(v); v != "" {
			out = append(out, v)
		}
	}
	return out
}
