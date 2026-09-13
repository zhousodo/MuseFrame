# Progress Log

## Session: 2026-09-07

### Phase 1: 复现与定位
- **Status:** complete
- **Started:** 2026-09-07
- Actions taken:
  - 阅读并启用 planning-with-files 工作流。
  - 确认实际 Git 仓库位置与初始工作树状态。
  - 从用户截图记录未登录可见作品及 33 张额度现象。
  - 追踪网页启动、退出、作品和额度加载路径，以及后端会话和数据查询。
  - 初步锁定游客会话被当作完整用户会话是核心权限模型缺口。
  - 只读核对线上版本、容器健康状态和用户余额分布；确认当前数据库没有 33 张余额。
  - 用全新独立浏览器会话验证未登录首页显示“注册领 3 张”，没有显示 33。
- Files created/modified:
  - `task_plan.md`（创建）
  - `findings.md`（创建）
  - `progress.md`（创建）

### Phase 2: 设计修复
- **Status:** complete
- Actions taken:
  - 确定服务端以 `is_guest=0` 作为私有资源访问边界。
  - 确定网页不再自动换取游客 token，公共目录保持匿名。
  - 确定退出登录先吊销服务端 token，再清理本地认证状态。
- Files created/modified:

### Phase 3: 实施修复
- **Status:** complete
- Actions taken:
  - 后端新增注册账户门禁并覆盖全部私有资源/额度/生成/购买接口。
  - 前端移除自动游客换令牌，修复认证标志同步和退出会话吊销顺序。
  - 新增游客隔离、服务端退出吊销、浏览器会话三组回归测试。
- Files created/modified:
  - `server/api.js`, `web/api.js`, `web/app.js`, `web/i18n.js`
  - `server/test/http.test.js`, `server/test/admin-grant.test.js`, `server/test/helpers.js`, `server/test/web-session.test.js`
  - `server/tools/deploy.sh`, `README.md`, `OPS.md`

### Phase 4: 验证
- **Status:** complete
- Actions taken:
  - HTTP 回归连续运行 3 次并通过；完整套件 148/148 通过。
  - 语法、部署脚本与差异格式检查通过。

### Phase 5: 部署与交付
- **Status:** complete
- Actions taken:
  - 提交并推送 `fa052fe`，部署到生产。
  - 生产一致性备份后关闭游客令牌并吊销全部 27 个存量会话。
  - 公网验证健康 200、游客交换 403、匿名作品/额度均 401；容器 healthy。
  - 复核生产无 33 张余额账号，当前最高有效余额为 1。

## Test Results
| Test | Input | Expected | Actual | Status |
|------|-------|----------|--------|--------|
| HTTP 鉴权/断连回归 | `node --test server/test/http.test.js` ×3 | 全部通过且无连接复用抖动 | 32/32 ×3 | PASS |
| 完整测试 | `npm test` | 全部通过 | 148/148 | PASS |
| 静态检查 | `node --check`、`bash -n`、`git diff --check` | 无错误 | 无错误 | PASS |

## Error Log
| Timestamp | Error | Attempt | Resolution |
|-----------|-------|---------|------------|
| 2026-09-07 | 父目录执行 git status 返回非仓库 | 1 | 切换到 `D:\MuseFrame\museframe` |
| 2026-09-07 | PowerShell 将字符串中的 `$p:` 解析为非法变量引用 | 1 | 下一次使用 `-f` 格式化输出标签 |
| 2026-09-07 | `rg` 搜索参数含不存在路径/Windows 非法通配符 | 1 | 缩小到真实仓库路径 |
| 2026-09-07 | Web 工具拒绝直接打开生产 API URL | 1 | 使用 `curl.exe` 只读访问公开端点 |
| 2026-09-07 | PowerShell 解析凭据脱敏正则失败 | 1 | 改用简单的敏感行过滤方式 |
| 2026-09-07 | 电脑操作页面点击后超时并重置 | 1-2 | 放弃继续 UI 点击，改用 HTTP 集成测试 |
| 2026-09-07 | 多文件补丁 hunk 格式错误 | 1 | 拆分补丁并移除多余 hunk 标记 |
| 2026-09-07 | 第二轮全套测试的既有大包 HTTP 压力用例出现瞬时 `fetch failed` | 1 | 改跑单文件排除并行资源竞争，再复跑全套 |
| 2026-09-07 | 单跑 HTTP 文件仍在 413 后出现一次 `fetch failed` | 2 | 判定为客户端复用服务端主动关闭的连接；给测试 POST 固定 `Connection: close` |
| 2026-09-07 | HTTP 压力测试与其他文件并行仍偶发断连 | 3 | 标准 `npm test` 改为 `--test-concurrency=1`，隔离真实服务子进程 |
| 2026-09-07 | 文件串行后压力用例仍偶发影响后续请求 | 4 | 找到测试循环未消费 5 个 413 响应体；改为断言并读取每个响应，同时隔离 chunked 连接，恢复标准并行测试命令 |
| 2026-09-07 | 数据库快照属主为容器 root，普通用户 `chmod` 被拒 | 1 | 使用 `sudo chmod 600` 成功收紧权限 |

## 5-Question Reboot Check
| Question | Answer |
|----------|--------|
| Where am I? | Phase 5：完成 |
| Where am I going? | 向用户交付结果 |
| What's the goal? | 阻止匿名读取私人作品/账户额度，并测试保护 |
| What have I learned? | 见 `findings.md` |
| What have I done? | 已完成修复、测试、部署、会话吊销与公网验证 |

## Session: 2026-09-07 — Backend security audit

### Phase 6: 后台攻击面盘点
- **Status:** complete
- Actions taken:
  - 恢复上一轮修复上下文并建立本轮安全审计范围。
  - 完成服务入口、限流、CORS、静态文件与管理员接口的第一轮扫描。
  - 记录畸形 Host 拒绝服务候选风险及后台 `innerHTML`/长期令牌审计点。
  - 运行生产依赖审计，确认旧版 `nodemailer` 为直接高危依赖，目标修复版本 10.0.1。
  - 通过原始 TCP 畸形 Host 请求复现未认证远程进程崩溃（退出码 1）。
  - 审查管理员页面主要 DOM 数据流，未发现未转义的用户可控 HTML 注入路径。
  - 确认网页仍在图片 URL 中泄露长期会话 token，并发现购买收据未绑定首次领取账号/商品。
  - 盘点输入验证缺口及 HTTP/容器纵深防护缺口。
  - 阅读上一轮完整审计与上架报告，恢复已接受的兼容约束和残余风险背景。
  - 完整依赖审计确认生产 1 个高危、开发链 4 个中危告警。
  - 检查生产响应安全头与受跟踪敏感文件；未发现已提交真实密钥。
  - 复核完整 Git 历史，未发现真实密钥或敏感文件进入历史。
  - 远程核对监听与容器安全参数，确认源站仅回环监听，但 Caddy 仓库配置仍保留明文 nip.io/HTTP 入口，容器纵深加固未启用。

### Phase 7: 依赖与部署配置审计
- **Status:** complete
- Actions taken:
  - 完成生产/开发依赖、Git 历史密钥、生产响应头、Caddy 活跃站点、监听端口和容器安全参数审计。
  - 确认正式域名 HTTP 在 Cloudflare 侧 301，但 nip.io 明文直连仍实际开放。

### Phase 8: 修复与回归测试
- **Status:** complete
- 已修复 Host 崩溃、购买凭证跨账号领取、长期会话图片 URL、字段边界与 413 连接复用问题。
- 已升级 nodemailer 10.0.1，加入应用安全头、管理员 tab 级令牌、容器/Caddy 纵深加固。
- HTTP 文件连续两轮 37/37、完整套件 154/154、生产依赖审计 0 漏洞、diff check 全部通过。

### Phase 9: 提交、推送与生产部署
- **Status:** complete
- 安全修复提交 `2599995`、容器身份兼容修复 `416b5f1`、上线审计记录 `00b59c0` 已推送 GitHub main。
- 生产完成两次重建，首次暴露 uid/capability 权限冲突后改为 `1000:1000`，最终容器 healthy。
- 部署脚本全部公网安全回归 PASS；活跃 Caddy 在保留落地页的前提下移除 nip.io、强制 HTTP→HTTPS 并 reload 成功。
- 最终公网健康 200、远程生成可用、队列空；本机/生产/GitHub main 同步到 `00b59c0`。
