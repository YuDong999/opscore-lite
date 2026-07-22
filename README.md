# OpsCore · 运维控制台

从最核心做起的最小可运行运维控制台。Go 单二进制 + React 前端，无外部依赖。

## 技术栈
- 后端:Go + gopsutil(v4) 采集系统指标,标准库 net/http
- 前端:React 18 + Vite + TypeScript + ECharts 5
- 前端运行时从目录读取(不 embed),前后端分离

## 核心模块
1. **系统资源** — 内存(波浪 liquidfill)、CPU(仪表盘 + 实时折线)、磁盘(饼图 + 可点击下钻)、每核(柱状)、网络吞吐、系统负载
2. **服务发现** — systemctl 列出运行单元,支持 启动/停止/重启 按钮 + 日志查看;非 Linux 降级为进程列表
3. **网络** — 网络接口、监听端口(LISTEN sockets)。端口身份以真实进程(PID→进程名)为准
4. **防火墙** — 状态卡、端口开关、IP 黑白名单、规则列表。高危操作二次确认 + 审计链
5. **守护中心** — 定时任务(cron)、脚本库(模板化快速脚本)、备份快照

## 快速安装

> **前置条件一览**
> | 方式 | 需要 Go | 需要 Node.js | 需要 Docker | 平台 |
> |---|---|---|---|---|
> | 一键安装 | ✗ | ✗ | ✗ | Linux / macOS |
> | 手动下载 | ✗ | ✗ | ✗ | Linux / macOS / Windows |
> | 源码构建 | ✓ 1.24+ | ✓ 18+ | ✗ | Linux / macOS / Windows |
> | Docker | ✗ | ✗ | ✓ 20.10+ | Linux / macOS / Windows |

### 方式一:一键安装 (推荐,零依赖)
```bash
curl -fsSL https://raw.githubusercontent.com/YuDong999/opscore-lite/main/install.sh | bash
```
- **前置条件**: 无 (只需 curl + bash)
- 自动检测平台,下载最新预构建二进制 + 前端文件
- 安装到 `/usr/local/bin/opscore`

### 方式二:手动下载 (零依赖)
从 [GitHub Releases](https://github.com/YuDong999/opscore-lite/releases) 下载对应平台的压缩包:
```bash
# Linux AMD64
curl -fsSL https://github.com/YuDong999/opscore-lite/releases/latest/download/opscore-linux-amd64.tar.gz | tar xz
./opscore

# macOS ARM64 (Apple Silicon)
curl -fsSL https://github.com/YuDong999/opscore-lite/releases/latest/download/opscore-darwin-arm64.tar.gz | tar xz
./opscore

# Windows
# 下载 opscore-windows-amd64.zip, 解压后运行 opscore.exe
```
- **前置条件**: 无

### 方式三:源码构建 (需要开发环境)
```bash
git clone https://github.com/YuDong999/opscore-lite.git
cd opscore-lite
make build    # 构建前端 + 编译 Go 二进制
./opscore     # 启动 (默认 :8088)
```
- **前置条件**: Go 1.24+ 和 Node.js 18+
- 前端改动只需 `make web`(无需重编 Go)

### 方式四:Docker (需要 Docker)
```bash
docker compose up -d --build
```
- **前置条件**: Docker 20.10+ 和 Docker Compose 2.0+
- 适合不想装 Go/Node.js 的服务器

## 常用命令
```bash
opscore                           # 启动 (默认 :8088)
opscore -addr 127.0.0.1:8088      # 指定监听地址 (配合 nginx)
opscore -dist ./web/dist          # 指定前端目录
opscore -data /path/to/data       # 指定数据目录 (配置/备份)
```

## 开发
```bash
make dev      # 前端 + Go 编译 + 运行
make web      # 仅重编前端 (前端改动时用,无需重编 Go)
make go       # 仅重编 Go (后端改动时用)
```

## 开机自启 (systemd)
```bash
sudo cp deploy/opscore.service /etc/systemd/system/
sudo systemctl daemon-reload && sudo systemctl enable --now opscore
```

## nginx 反代
```bash
sudo cp deploy/nginx-opscore.conf /etc/nginx/conf.d/opscore.conf
sudo nginx -t && sudo systemctl reload nginx
```
参考 `deploy/nginx-opscore.conf` 修改 server_name 和端口。

## 端口说明
- 默认监听 `:8088` (避开 Prometheus 9090 / nginx 8080/8081)
- 如需 nginx 反代:OpsCore 起 `-addr 127.0.0.1:8088`, nginx 反代 `http://127.0.0.1:8088`

## 平台说明
- 服务启停需要 Linux + systemd 环境且有相应权限
- Windows/macOS 上服务模块会降级为进程列表展示
- 防火墙在 Windows 上为只读(写入仅预览);Linux + 特权下生效
- Windows 挂载点为盘符(如 `C:`),下钻端点会自动归一化为 `C:\`
