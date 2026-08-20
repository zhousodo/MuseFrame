# MuseFrame 上架审查报告（Google Play + iOS App Store）

> 更新：2026-08-20 · 状态图例：✅ 已完成 · 🟡 需你的账号/资料 · 🔴 阻断项

## 0. 一句话结论

**技术骨架已就绪，安全已加固到「服务端说了算」**。剩下的都不是代码问题，而是
**你的开发者账号里的配置**：真实内购商品、OAuth 凭据、Play 服务账号、商店表单。

---

## 1. 安全审查（你最关心的「防破解」）——本轮已修复

### 🔴→✅ 支付伪造（最严重，已堵死）

**改前**：客户端 POST `/v1/purchases/verify {productKey:"creator_monthly"}` 就能白嫖
Creator，无限生成烧我们的模型钱。
**改后**：服务端只认平台签名的真实收据 —— Google Play 用服务账号向
`androidpublisher` API 核验 `purchaseToken`，伪造 token 直接 402 拒绝。生产环境
`ALLOW_MOCK_PURCHASES=false`，演示购买通道彻底关闭。**已在生产实测：伪造购买被拒，
额度不变。**

### 🔴→✅ 登录伪造（已堵死）

**改前**：`provider=google` 只对 token 做哈希，任意字符串都能「登录」。
**改后**：真正验证 Google/Apple 的 ID token —— 拉取签发方 JWKS、验 RS256 签名、
校验 `iss`/`aud`(你的 Client ID)/`exp`。伪造 token 返回 401。身份 = 平台的 `sub`，
不可伪造。

### 🟡 免费额度刷取（已加防护开关，需你决策）

游客身份存在手机本地，卸载重装理论上能再拿 1 张免费。已实现两个服务端开关：
- `FREE_REQUIRES_AUTH=true`：免费额度只发给登录用户（按 Google `sub` 去重，
  重装也不再送）——**推荐上架前打开**。
- `ALLOW_GUEST=false`：完全强制登录才能用。

### ✅ 计数本身：一直是服务端权威

生成扣费走服务端 append-only 账本（reserve→commit/release），客户端**无法伪造额度**
——它只能请求生成，扣不扣、够不够，全由服务器的账本裁决。并发争抢最后 1 点也只有
一个成功。这一层从一开始就是安全的。

### ✅ 本轮附加加固

- **限流**：登录/生成/购买/上传按 IP 滑动窗口限流（实测第 21 次登录起 429）。
- **越权防护**：所有资源查询都带 `user_id` 校验，拿不到别人的项目/作品/任务。
- **管理后台**：独立 `ADMIN_TOKEN` 鉴权，与用户体系隔离；用户图片默认不暴露，
  只通过令牌门控接口按需查看。

---

## 2. Google Play 上架清单

| 项 | 状态 | 说明 |
|---|---|---|
| AAB 包（Play 要求）| ✅ | `MuseFrame-release-v1.0.aab` |
| targetSdk 35+ | ✅ | 编 targetSdk 36 |
| 应用签名 | ✅ | 已签名；**建议改用 Play App Signing**（上传后 Google 托管签名密钥）|
| 隐私政策 URL | ✅ | https://museframe.lenscript.cn/privacy.html |
| 全链路 HTTPS | ✅ | 已移除明文流量豁免 |
| 应用图标/截图 | ✅ | `store-assets/`（5 张 1125×2436）|
| 商品与真实内购 | 🔴 | **需你在 Play Console 建订阅/点数包商品**，再接 Play Billing 客户端 SDK |
| Play 收据校验服务账号 | 🔴 | Console 建服务账号 → 授权 → 下载 JSON → 放服务器 `.env` 的 `GOOGLE_SERVICE_ACCOUNT_JSON` |
| Google 登录 OAuth 客户端 | 🟡 | Google Cloud 建 OAuth Client（Android，填 SHA-1 指纹）→ Client ID 填 `GOOGLE_CLIENT_IDS` |
| Data Safety 表单 | 🟡 | 申报：收集照片(应用功能)、匿名标识；不共享给第三方广告；见隐私政策 |
| 内容分级问卷 | 🟡 | 含 AI 生成内容，如实勾选 |
| 目标受众/年龄 | 🟡 | 13+ |
| AI 生成内容声明 | 🟡 | Play 政策要求标注 |

### APK 签名 SHA-1（配 Google 登录用）
```
47:87:84:49:B3:32:A9:E7:55:49:F6:F8:8C:2F:A4:C7:DC:88:DB:DE
```

## 3. iOS App Store 上架清单

| 项 | 状态 | 说明 |
|---|---|---|
| iOS 构建 | 🔴 | 需在 **macOS + Xcode** 上 `npx cap add ios` 打包（当前是 Windows，无法产出 .ipa）|
| Apple 开发者账号 | 🔴 | $99/年 |
| Sign in with Apple | 🟡 | 代码已支持验签，填 `APPLE_BUNDLE_IDS`；**若提供第三方登录，Apple 强制必须同时提供 Apple 登录** |
| StoreKit 2 内购 | 🔴 | App Store Connect 建商品；服务端 App Store Server API 验签（代码已留接口，需 .p8 密钥）|
| App Privacy 标签 | 🟡 | 同 Data Safety |
| 隐私政策 | ✅ | 同上 |

## 4. 两端通用的剩余工作

1. **真实内购接线**（🔴 最大工作量）：客户端接 Play Billing / StoreKit 拿真实
   `purchaseToken`/`transaction`，服务端验签逻辑**已写好**，只差你的商品 ID + 凭据。
2. **配置注入**（改服务器 `.env` 即可，无需重发 App）：
   ```
   GOOGLE_CLIENT_IDS=<android/ios/web 的 OAuth client id>
   GOOGLE_SERVICE_ACCOUNT_JSON=/opt/museframe/play-sa.json
   APPLE_BUNDLE_IDS=com.museframe.app
   FREE_REQUIRES_AUTH=true      # 上架前建议打开
   ```
   然后 `sudo systemctl restart museframe`。
3. **前端登录 UI**：目前 onboarding 是游客直进。接真实登录时加「使用 Google/Apple
   登录」按钮（Capacitor 有官方插件），拿到 idToken 传 `/v1/auth/exchange`。

## 5. 我能立刻帮你做的下一步

- 你在 Play Console / App Store Connect 建好商品并给我商品 ID + 凭据 → 我接完
  客户端 IAP + 前端登录按钮，端到端跑通沙盒购买。
- 需要 iOS 包时，把项目在 Mac 上打开，我可以远程指导或在 Mac 环境继续。
- 想先把 `FREE_REQUIRES_AUTH` 打开防刷 → 一条命令，随时说。
