package waffo

import "strings"

// ProdWebhookPublicKeyPEM 是 Waffo **生产环境**的平台级 Webhook 验签公钥
// （Dashboard → Settings → Webhooks → Webhook Public Key，LIVE；所有商户、所有店铺共用，
// @waffo/pancake-ts 也把它内置在 SDK 里）。它是**公钥**，不是密钥，可以进仓库。
//
// 2026-09-23 从 Dashboard 抄下并用 openssl 核对可解析（2048 位 RSA）。
//
// WAFFO_WEBHOOK_PUBLIC_KEY 环境变量仍可覆盖它：测试环境（mode=test）用的是另一把钥，
// 或者 Waffo 将来换钥时不必等发版。生产模式下留空即用这一把。
const ProdWebhookPublicKeyPEM = `-----BEGIN PUBLIC KEY-----
MIIBIjANBgkqhkiG9w0BAQEFAAOCAQ8AMIIBCgKCAQEAz+xApdTIb4ua+DgZKQ54iBsD82ybyhGCLRETONW4Jgbb3A8DUM1LqBk6r/CmTOCHqLalTQHNigvP3R5zkDNXiRJz6gA4MJ/+8K0+mnEE2RISQzN+Qu65TNd6svb+INm/kMaftY4uIXr6y6kchtTJdwnQhcKdAL2v7h7IFnkVelQsKxDdb2PqX8xX/qwd01iXvMcpCCaXovUwZsxH2QN5ZKBTseJivbhUeyJCco4fdUyxOMHe2ybCVhyvim2uxAl1nkvL5L8RCWMCAV55LLo09OhmLahz/DYNu13YLVP6dvIT09ZFBYU6Owj1NxdinTynlJCFS9VYwBgmftosSE1UdwIDAQAB
-----END PUBLIC KEY-----
`

// DefaultWebhookPublicKeyPEM 返回某个环境的内置验签公钥；只有生产有内置值，
// 测试环境的钥必须由 WAFFO_WEBHOOK_PUBLIC_KEY 提供。
func DefaultWebhookPublicKeyPEM(mode string) string {
	if strings.ToLower(strings.TrimSpace(mode)) == "prod" {
		return ProdWebhookPublicKeyPEM
	}
	return ""
}
