# OpsCore 构建脚本
# ==================
# 开发者:
#   make dev          # 前端 + Go 编译，运行 (需要 Go + Node.js)
#   make web          # 仅重编前端 (前端改动时用)
#   make go           # 仅重编 Go (后端改动时用)
#
# 部署:
#   make build        # 完整构建 (web + go)
#   make install      # 构建 + 安装到 /usr/local/bin
#
# 发布:
#   make release      # 交叉编译 5 平台 (用于 GitHub Releases)

# ---------- 变量 ----------
BINARY    := opscore
GO_FLAGS  := -ldflags="-s -w"
# Docker 前端构建 (glibc 太旧的系统用)
DOCKER_NODE := node:18-alpine

# ---------- 前端 ----------
# 注意: CentOS 7 (glibc 2.17) 无法运行 Node 18+，必须用 Docker 构建
web:
	@if [ -f web/node_modules/.bin/vite ]; then \
		echo "[Docker] 构建前端 (跳过 npm install)..."; \
		docker run --rm -v $$(pwd)/web:/work -w /work $(DOCKER_NODE) ./node_modules/.bin/vite build; \
	else \
		echo "[Docker] 构建前端 (首次需要 npm install)..."; \
		docker run --rm -v $$(pwd)/web:/work -w /work $(DOCKER_NODE) sh -c "npm install && ./node_modules/.bin/vite build"; \
	fi

# 首次或依赖更新时下载 npm 包（不进 node_modules 时不需要）
web-deps:
	@echo "[Docker] 安装前端依赖..."
	docker run --rm -v $$(pwd)/web:/work -w /work $(DOCKER_NODE) npm install

go:
	go build $(GO_FLAGS) -o $(BINARY) .

dev: go
	./$(BINARY) -dist ./web/dist

# ---------- 部署 ----------
build: web
	go build $(GO_FLAGS) -o $(BINARY) .

install: build
	sudo cp $(BINARY) /usr/local/bin/$(BINARY)
	sudo mkdir -p /usr/local/lib/opscore/web
	sudo cp -r web/dist /usr/local/lib/opscore/web/dist
	@echo ""
	@echo "✓ 已安装到 /usr/local/bin/opscore"
	@echo "  运行: opscore"
	@echo ""

# ---------- 交叉编译 (发布用) ----------
release: web
	@echo ">>> 构建 linux/amd64..."
	GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build $(GO_FLAGS) -o dist/opscore-linux-amd64 .
	@echo ">>> 构建 linux/arm64..."
	GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build $(GO_FLAGS) -o dist/opscore-linux-arm64 .
	@echo ">>> 构建 darwin/amd64..."
	GOOS=darwin GOARCH=amd64 CGO_ENABLED=0 go build $(GO_FLAGS) -o dist/opscore-darwin-amd64 .
	@echo ">>> 构建 darwin/arm64..."
	GOOS=darwin GOARCH=arm64 CGO_ENABLED=0 go build $(GO_FLAGS) -o dist/opscore-darwin-arm64 .
	@echo ">>> 构建 windows/amd64..."
	GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build $(GO_FLAGS) -o dist/opscore-windows-amd64.exe .
	@echo ""
	@echo "✓ 全部构建完成，产物在 dist/"
	@ls -lh dist/opscore-*

# ---------- 清理 ----------
clean:
	rm -rf $(BINARY) $(BINARY).exe dist/

.PHONY: web go dev build install release clean
