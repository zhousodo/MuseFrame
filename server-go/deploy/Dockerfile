# museframe-api 运行镜像。
#
# 刻意**不做 multi-stage 编译**：生产机内存小，禁止在生产机上 docker build。
# 二进制一律在本地交叉编译好（CGO_ENABLED=0 GOOS=linux GOARCH=amd64），
# 这里只负责把静态二进制装进一个最小基底。
#
# 基底选 distroless static 而不是 scratch：静态 Go 二进制不需要 libc，但
# **需要 CA 根证书**（上游图像 API、Google/Apple 的 JWKS、Play Developer API
# 都是 HTTPS）。scratch 里没有根证书，TLS 会在运行时才炸。
# distroless static 自带 ca-certificates 且以 nonroot 运行。
FROM gcr.io/distroless/static-debian12:nonroot

# dist/museframe-api 由 CI / 本地交叉编译产出：
#   CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath \
#     -ldflags "-s -w -X main.version=<tag>" -o dist/museframe-api ./cmd/museframe-api
COPY dist/museframe-api /museframe-api

ENV PORT=8787 HOST=0.0.0.0
EXPOSE 8787
USER nonroot:nonroot
ENTRYPOINT ["/museframe-api"]
