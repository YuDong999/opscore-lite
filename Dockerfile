# ---------- Stage 1: 构建前端 (web/dist) ----------
FROM node:22-bookworm AS web
WORKDIR /src/web
# 优先用 lockfile 复现依赖
COPY web/package.json web/package-lock.json* ./
RUN npm ci || npm install
COPY web/ ./
RUN npm run build

# ---------- Stage 2: 编译后端 (静态二进制) ----------
FROM golang:1.24-bookworm AS go
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
# 把前端产物塞进 embed 目录
COPY --from=web /src/web/dist ./web/dist
# 静态链接,零 C 依赖,可在 scratch/alpine 直接跑
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o /out/opscore .

# ---------- Stage 3: 运行时 ----------
# 用 alpine 而非 scratch:保留 sh 便于 docker exec 排查;镜像仍仅 ~15MB
FROM alpine:3.20
RUN apk add --no-cache ca-certificates
WORKDIR /app
COPY --from=go /out/opscore /app/opscore
# 默认监听 9090;可通过 OPCORE_ADDR 覆盖(见 docker-compose.yml)
ENV OPCORE_ADDR=:9090
EXPOSE 9090
ENTRYPOINT ["/app/opscore"]
