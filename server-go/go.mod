// museframe-api —— 留影（MuseFrame）后端（Node.js 零框架 node:http + SQLite → Go + PostgreSQL）。
//
// 依赖选型（写死版本，不允许浮动）：
//   - github.com/jackc/pgx/v5 v5.9.2 —— 与 platform/apps/paida 及 migrations/framework 同版本，
//     同机不出现两套 PostgreSQL 协议实现。
//
// 刻意**不**引入任何 Web 框架：原实现是裸 node:http 手写路由表 + 正则匹配，
// 重写保持同等轻量，路由表就是一个切片，行为可逐条对照。
// 图像解码只用标准库 image/jpeg —— 原实现的 jpeg-js 同样只做基线 JPEG。
//
// 构建（生产机内存小，禁止在生产机 docker build，一律本地交叉编译静态二进制）：
//   CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath \
//     -ldflags "-s -w -X main.version=<tag>" -o dist/museframe-api ./cmd/museframe-api
module museframe-api

go 1.25.0

require github.com/jackc/pgx/v5 v5.9.2

require (
	github.com/jackc/pgpassfile v1.0.0 // indirect
	github.com/jackc/pgservicefile v0.0.0-20240606120523-5a60cdf6a761 // indirect
	github.com/jackc/puddle/v2 v2.2.2 // indirect
	golang.org/x/sync v0.21.0 // indirect
	golang.org/x/text v0.32.0 // indirect
)
