package httpapi

import (
	"net/http/httptest"
	"testing"
)

// 校验点 9：伪造的 X-Forwarded-For / CF-Connecting-IP 不得欺骗限流桶。
//
// 用 /v1/auth/exchange（20 次 / 10 分钟）而不是 email/request：后者除了 per-IP
// 限流还有一层 per-地址签发上限，两条闸混在一起会让断言含糊。exchange 只有 per-IP
// 这一条，命中 429 就只能是限流桶生效了。
func TestCheck09_SpoofedForwardedForCannotResetRateLimit(t *testing.T) {
	e := newTestEnv(t)
	// 对端是 127.0.0.1（可信私网），所以 XFF 会被读 —— 但只读**最右一项**。
	// 攻击者能控制的是左边那些，右边那项由可信代理追加。
	post := func(app *App, xffLeft, xffRight string) int {
		req := httptest.NewRequest("POST", "/v1/auth/exchange", newBytesReader([]byte(`{"provider":"guest"}`)))
		req.RemoteAddr = "127.0.0.1:5000"
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Forwarded-For", xffLeft+", "+xffRight)
		req.Header.Set("CF-Connecting-IP", xffLeft) // 默认必须被忽略
		w := httptest.NewRecorder()
		app.Handler().ServeHTTP(w, req)
		return w.Code
	}

	limited := false
	for i := 0; i < 25; i++ {
		// 每次换一个伪造的左侧地址；真实计数键应当始终是 203.0.113.9。
		code := post(e.app, "10.0.0."+itoaSmall(i), "203.0.113.9")
		if code == 429 {
			limited = true
			if i < 20 {
				t.Fatalf("第 %d 次就被限流了，早于上限 20", i+1)
			}
			break
		}
		if code != 403 {
			t.Fatalf("第 %d 次期望 403（allow_guest=false），实际 %d", i+1, code)
		}
	}
	if !limited {
		t.Fatal("轮换伪造 X-Forwarded-For / CF-Connecting-IP 不得绕过 per-IP 限流")
	}

	// 负向：换一个**真实的**最右项（另一个来源地址）应当有自己的桶 ——
	// 否则限流就是「全局一个桶」，上面那条断言毫无意义。
	if code := post(e.app, "10.0.0.1", "198.51.100.4"); code != 403 {
		t.Fatalf("另一个真实来源地址应当有自己的限流桶（期望 403），实际 %d", code)
	}
}

func itoaSmall(n int) string {
	if n < 10 {
		return string(rune('0' + n))
	}
	return string(rune('0'+n/10)) + string(rune('0'+n%10))
}
