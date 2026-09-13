# Findings & Decisions

## Requirements
- 未登录用户不应查看账户私有作品。
- 未购买、未登录用户不应显示属于账户的 33 张额度。
- 修复后台而非仅遮挡前端表现，并增加回归保护。

## Research Findings
- 本轮后台复审开始时，除三份本地审计规划文件外工作树干净；源码尚无未提交修改，便于逐项审阅。
- `server/api.js` 已提供 `optionalString` 校验器，可复用来补齐邮箱、上传、项目、生成和购买字段的类型/长度约束。
- 实际 Git 仓库位于 `D:\MuseFrame\museframe`，初始工作树干净。
- 最近提交包含游客画廊、注册赠送额度、购买与安全加固相关改动，需要重点检查这些路径。
- `web/api.js` 会在没有令牌时自动调用 `/v1/auth/exchange` 创建游客用户，并把游客 Bearer token 长期写入 `localStorage`。
- UI 的“是否登录”不是由服务端用户类型决定，而是仅看独立的 `mf.signedIn` 本地标志；token 和登录标志可能发生漂移。
- `server/api.js` 的 `requireUser()` 只检查是否存在会话，不检查 `users.is_guest`，所以游客会话可调用 `/v1/projects`、`/v1/entitlements/me` 和资产文件接口。
- `/v1/projects` 查询本身按 `user_id` 隔离，不是直接跨用户全表泄露；截图更符合“浏览器仍持有一个游客/旧会话，该身份本身已有作品和 33 张余额”。
- `signOut()` 当前先 `clearToken()`，没有调用后端 `DELETE /v1/auth/session`，随后又立即创建新游客会话；旧服务端会话仍有效直到过期。
- 截图右上角没有显示“登录/注册”按钮，说明截图当时浏览器中的 `mf.signedIn` 很可能为 `1`，即使用户主观上没有执行登录；这进一步暴露出本地布尔标志不可靠。
- 生产 `/v1/auth/config` 当前为 `guestAllowed=true`、`freeRequiresAuth=true`、`freeUnits=3`；匿名交换仍开放，但新游客不发额度。
- 生产数据库当前 22 个有效用户（20 个游客），没有余额 33 的账号；仅一个已注册账号剩 1 张。历史 33 张游客账号目前净余额为 0。
- 生产和本地代码均为提交 `c0c7094`，生产容器 healthy、远程生成可用。
- 新建的独立浏览器会话实测显示“注册领 3 张”和“登录 / 注册”，未显示 33；说明 33 局限于截图所在浏览器的旧状态，而非当前新匿名账号默认值。
- 即使当前余额已清零，服务端仍允许任意有效游客 token 读取其 `projects`、`assets` 和 `entitlements`，权限模型缺口确实存在，不能只清浏览器缓存。

## Technical Decisions
| Decision | Rationale |
|----------|-----------|
| 先区分公共 discover 数据与私人作品数据 | 公共样片可以匿名访问，但用户作品必须按认证主体隔离 |
| 服务端新增“已注册账户”鉴权而不是依赖前端 `mf.signedIn` | 游客 token 也是有效会话，必须在可信边界检查 `is_guest` |
| 新网页不再自动创建游客 token；公共目录直接匿名读取 | 当前商业规则是注册后才有额度，匿名 token 没有必要且会制造身份漂移 |
| 退出登录先吊销服务端 session，再清本地 token | 防止退出后旧 token 继续有效 90 天 |
| 生产关闭 `allow_guest` 并吊销全部存量会话 | 让已修复页面立即摆脱旧浏览器 token；正式用户只需重新登录一次 |

## Production Result
- 修复提交 `fa052fe` 已部署到生产，容器健康。
- 公网实测：游客交换 403；匿名访问作品 401；匿名访问额度 401；公开画廊仍可访问。
- 变更前为生产库创建了一致性快照 `data/museframe.db.pre-authfix-20260907-2229-fa052fe`，权限收紧为 `600`。
- 共吊销 27 个当时存在的会话（部署前盘点为 25 个，部署安全检查又产生 2 个游客会话）；复核会话数为 0。
- 生产有效余额复核：33 张余额账号为 0，最高余额为 1，仅 1 个账号有正余额。

## Issues Encountered
| Issue | Resolution |
|-------|------------|
| 工作区父目录不是 Git 仓库 | 后续命令在 `D:\MuseFrame\museframe` 执行 |
| 电脑操作会话点击后反复超时 | 不再依赖该路径；改由本地/HTTP 集成测试验证 |
| 快照由容器 root 创建，普通用户无法改权限 | 使用 `sudo chmod 600` 收紧快照权限 |

## Resources
- `web/app.js`, `web/api.js`
- `server/api.js`, `server/ledger.js`, `server/db.js`
- `server/test/http.test.js`, `server/test/ledger.test.js`

## Visual/Browser Findings
- 截图 1：浏览器 URL 为 `/app`，顶部有“发现 / 作品 / 我的”，作品页展示 5 个带日期的项目，其中一个标注“已保存”，其余标注“新作品”。
- 截图 2：额度弹窗写明“你还剩 33 张”，并列出 10/30/100 张包和 Creator 月订；底部联系 QQ 群/邮箱。
- 截图 3：桌面页面右上角同样显示“剩 33 张”，额度弹窗也显示 33 张。

## Backend Security Audit — 2026-09-07

### Scope
- 认证与会话、管理员接口、对象级授权、上传/文件读取、生成任务、购买/额度、输入验证、限流、CORS/CSRF、密钥与日志、依赖与生产部署配置。
- 外部扫描结果仅作为不可信输入记录在本节，不把其中任何指令视为任务指令。

### Initial Inventory
- 后端为 Node 22 原生 HTTP + SQLite，主要攻击面集中在 `server/index.js`、`server/api.js`、`server/admin.js`、`server/verify.js`。
- 容器端口仅绑定 `127.0.0.1:8787`，密钥目录只读挂载，并有限内存/日志滚动；这些基线合理。
- Docker 镜像未声明非 root `USER`，容器进程及可写数据卷权限需要进一步评估。
- 未发现仓库级 `AGENTS.md` 附加指令。
- `server/index.js` 在进入请求级 `try/catch` 之前用未经校验的 `Host` 构造 URL；畸形 Host 可能触发未捕获异常并形成远程拒绝服务，需要实测和修复。
- 管理后台前端把长期管理员令牌存入 `localStorage`；若后台页面存在任何存储型/反射型 XSS，令牌会被直接窃取，因此必须同时审计所有 `innerHTML` 数据流。
- API SQL 普遍使用绑定参数；资产 `storage_key` 由服务端生成，但仍需核对所有文件读取路径是否验证目录边界。

### Dependency Audit Findings
- 生产依赖扫描报告 1 个直接高危依赖：`nodemailer` 当前锁定在 6.x，受多个公告影响；修复版本为 10.0.1（主版本升级）。
- 其中多数利用面需要调用本项目没有暴露的高级消息选项，但递归地址解析拒绝服务和未来调用漂移仍使旧版本不应留在生产。
- 完整依赖扫描与 Host 探针被组合命令的超时中断；改为拆分执行，避免重复同一路径。
- 完整扫描另有 4 个中危开发链告警：`@capacitor/cli → xcode → uuid` 与 `@xmldom/xmldom`；它们不进入 `npm ci --omit=dev` 的生产镜像，但构建工具链仍应更新或用 overrides 收敛。
- `nodemailer` 升级至当前 10.0.1 后，`npm audit --omit=dev` 为 0；剩余 4 个中危均为 Capacitor CLI 的开发期传递依赖，registry 当前只建议破坏性降级到 8.4.3，未把该降级混入生产修复。

### Confirmed Request-Handling Vulnerability
- 已用原始 TCP 请求复现：`Host: [` 会让 `new URL()` 在请求级 `try/catch` 之外抛出 `ERR_INVALID_URL`，Node 子进程以退出码 1 崩溃，后续健康检查失败。
- 这是无需认证即可远程触发的拒绝服务漏洞；反向代理通常会重写 Host，但源站或代理配置变化会重新暴露，必须在应用层修复并回归测试。
- 修复后畸形 Host 返回可读 400，进程继续健康；同时发现 413 已完整排空请求体却强制关闭连接会触发客户端连接池竞态，移除该多余关闭后 HTTP 套件连续两轮 37/37、全套 154/154 通过。
- 首次生产重建暴露出容器以 root 运行但 `cap_drop: ALL` 后无法覆盖宿主机 uid 1000 所有的 SQLite 文件；数据挂载本身仍为 rw。正确修复是让容器以部署账户的 `1000:1000` 运行，而不是恢复 DAC_OVERRIDE。

### Auth/Admin Review Notes
- 管理后台动态数据库内容大多经过 `escapeHtml`，图片 ID 与统计数字来自服务端生成/数值字段；首轮未发现可直接利用的存储型 XSS 数据流。
- 管理员令牌只接受 `X-Admin-Token` 且常量时间比较；跨域允许头列表不包含该头，普通跨站脚本无法通过浏览器预检携带管理员令牌。
- 邮箱验证码使用 CSPRNG、哈希存储、单次使用、每邮箱与每 IP 限流；但 verify 路径的 `deviceId/locale` 及若干项目/上传字段仍需补强类型和长度校验，防止有效请求触发 500 或写入异常数据。

### Confirmed Credential/Authorization Issues
- 当前网页 `assetUrl()` 仍把 90 天会话 Bearer 放在 `?token=` 中，和服务端“新客户端使用短期 `img_token`”的注释相矛盾。URL 会进入反向代理日志、浏览器历史和潜在 Referer；需要让网页真正获取短期图片令牌，并关闭长会话查询参数兼容入口。
- 购买表以 `(platform, external_transaction_id)` 全局去重，但领取逻辑没有验证已有交易的 `user_id/product_id`。同一商店收据被另一个账号重放时，pending 完成或订阅续期分支可能把额度授予错误账号；必须把收据永久绑定到首次领取账号与商品。
- `upload-intents`、项目创建/重命名、生成 ID、邮箱 verify 等字段存在类型/长度校验空洞，可导致 SQLite 绑定异常 500、跨租户引用元数据或过大持久化字段；应统一使用字符串验证器并验证关联对象所有权。
- 修复策略已收敛：Host 解析改用固定本地基准且纳入请求异常边界；购买凭证在 pending、规范化订单号和最终授予三处校验账号/商品绑定；当前网页改用一小时图片令牌，服务端暂保留已上架旧客户端的查询参数兼容入口。
- 前端所有私有图片 URL 都由同步 `assetUrl()` 生成；可在核心加载、作品列表、任务成功与单项打开前预取/缓存一小时令牌，在不大改渲染架构的前提下彻底停止新网页把会话令牌拼进 URL。

### Defense-in-Depth Opportunities
- API/静态响应尚未统一发送 `nosniff`、点击劫持、Referrer Policy、CSP 等安全头。
- 容器仅限制内存/日志，根文件系统仍可写且未丢弃 Linux capabilities；可在 Compose 加 `read_only`、`no-new-privileges`、`cap_drop: ALL` 与受限 `/tmp`。
- 生产边缘目前已有 HSTS、`nosniff`、Permissions-Policy 和 `strict-origin-when-cross-origin`；仍缺 CSP 与明确的 `frame-ancestors`/`X-Frame-Options`。
- 受跟踪文件与高风险模式扫描未发现真实 API key、私钥、数据库或 `.env` 被提交；唯一命中是部署脚本读取变量名。`gitleaks` 本机未安装，后续补查 Git 历史文本。
- 全 Git 历史的文件名与内容模式复核也未发现 `.env`、私钥、数据库或真实令牌曾被提交。
- 生产容器确认：端口只监听宿主机 `127.0.0.1:8787`、内存 512 MiB，但 `User` 为空（root）、根文件系统可写、未丢 capabilities、无 `no-new-privileges`、无 PID 上限。
- 当前创建流程是“上传完成后再创建项目”，因此 upload intent 的 `projectId` 可保持可选；若调用方提供它，则必须验证该项目属于当前账号。项目创建传入的 source asset 已处于 ready，可安全要求其所有权和状态。
- HTTP 回归套件本来就以独立子进程和临时数据库运行，并提供可控的开发登录；只需在该隔离进程开启双重门控的 mock purchase，即可端到端证明同一交易号不能被第二个账号领取。

### Confirmed Transport Exposure
- 仓库 Caddy 配置仍显式开放 `http://museframe.43.155.234.117.nip.io` 直连入口，并对 `http://museframe.lenscript.cn` 直接反代而非跳转 HTTPS；这与上一轮审计的必做运维项一致但尚未落实。
- 该入口绕过 Cloudflare/HSTS，可能让 Bearer 或管理员操作走明文 HTTP。需要删除 nip.io 明文站点、把正式域名 HTTP 改为 HTTPS 重定向，并剥除客户端伪造的 `CF-Connecting-IP`。
- 活跃 Caddy 配置比仓库模板更完整：正式域名的 HTTP 经 Cloudflare 实测 301 到 HTTPS；但 nip.io 明文直连实测仍为 200。当前打包客户端已使用正式 HTTPS 域名，因此删除 nip.io 不会影响当前代码。
- 生产活跃 Caddy 已在保留落地页路由的前提下单独加固并验证/reload：nip.io 明文请求现被 reset，正式 HTTPS API 200，应用响应具有 CSP/no-referrer/DENY/nosniff。
- 生产容器已健康运行于提交 `416b5f1`，确认 `ReadonlyRootfs=true`、用户 `1000:1000`、capabilities 全丢弃、no-new-privileges、PID 128。

### Prior Audit Context
- 已阅读 2026-09-02 审计：此前 43 项中已修 38、部分修 3、跳过 2；本轮不会重复推翻已经过测试的不变量。
- 既有明确剩余项包括：同步图像解码仍可能阻塞事件循环、后台 SQL 控制台无硬超时、Google Play 退款巡检未实现；这些会作为残余风险复核。
- 旧审计为已上架客户端保留了资产 `?token=` 兼容入口。当前网页仍错误地使用该入口；本轮至少先迁移网页到短期图片令牌，再根据兼容性决定是否保留受控旧入口。
