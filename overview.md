# OpsCore 变更概览

> 仓库: https://github.com/YuDong999/opscore-lite
> 运行地址: http://localhost:8081 (nginx 反代) / :8088 (直连)
> v1.0.0 已发布: https://github.com/YuDong999/opscore-lite/releases/tag/v1.0.0

## 1. 核心功能

### 防火墙并入「防火墙和网络」模块
- 侧栏「防火墙和网络」含两个 tab: **网络 / 防火墙**
- 网络 tab: 网络接口 + 监听端口并排
- 防火墙 tab: 状态、启停、端口开关、IP 黑白名单、规则审计链、高危确认弹窗

### 多主题系统 (5 主题)
- Light (北欧蓝)、Obsidian (暗·紫粉)、Forest (亮·翠绿)、Twilight (暗·紫橙)、Amber (暖·黄粉)
- 药丸按钮切换 (TopBar)，Settings 模块卡片预览
- 主题切换平滑过渡: `* { transition: background-color 0.25s ease, ... }`
- 暗色主题设置 `color-scheme: dark`

### 登录认证
- `/api/auth/*` 免 token；其他 `/api/*` 需 `Authorization: Bearer <token>`
- 前端 LoginPage + 401 自动跳转

### 守护中心 (GuardModule)
- 定时任务: crontab CRUD (guard_cron.go)
- 脚本库: 模板化 (awk/行数/磁盘/目录/端口/日志/自定义/计算器) + 代码编辑器双模式
- 备份快照: tar.gz 打包 (guard_backup.go)

### 通用存储层
- `internal/store/store.go` — 泛型 JSON 文件存储 + sync.RWMutex

## 2. UI 优化 (2025-07-23)

### 表格推拉门效果
- 所有 `.data-table` 统一 `overflow: hidden; text-overflow: clip; white-space: nowrap`
- 内容超长直接裁剪，后一列自然挡住前一列溢出，不再换行挤压

### 网络监听端口表
- 列宽比例: 协议 10% / 本地地址 25% / 识别服务 25% / 真实进程/PID 25% / 端口提示 15%

### 服务发现筛选栏
- 状态筛选从 `<th>` 内联 `<select>` 改为表格上方独立筛选栏
- 标签"状态" + 全部/运行中/已退出/失败药丸按钮

### 服务发现表列宽
- 名称 18% / 状态 10% / 说明 20% / CPU% 7% / 内存% 7% / 单元文件 16% / 日志 12% / 操作 10%

## 3. 性能

- `ps -eo pid,%cpu,%mem` 批量查询 CPU/内存，74 个服务 0.2s
- `filepath.WalkDir` du 权限回退 + `partial` 标记
- 后端 `round2()` + 前端 `toFixed(2)` 精度统一

## 4. 构建 & 部署

### GitHub Actions
- `.github/workflows/release.yml`: push `v*` tag 触发
- 5 平台交叉编译: linux/darwin amd64+arm64, windows amd64
- 产物: 二进制 + web/dist 打包为 tar.gz/zip，上传 GitHub Releases

### 一键安装
```bash
curl -fsSL https://raw.githubusercontent.com/YuDong999/opscore-lite/main/opscore-install.sh | bash
```
- 自动检测平台 (linux/darwin/windows, amd64/arm64)
- 下载最新 Release，安装到 `/usr/local/bin/` + web/dist

### Makefile
- `make build` — Docker 感知前端构建 + Go 编译
- `make dev` — 本地开发
- `make release` — 交叉编译 5 平台
- `make clean` — 清理

### 前端构建 (glibc 老系统)
- 本机 glibc 2.17 不支持 node 18+，需用 Docker:
```bash
docker run --rm -v $(pwd)/web:/web -w /web node:18-alpine sh -c "npm install && npm run build"
```

## 5. 部署架构

| 组件 | 端口 | 说明 |
|---|---|---|
| opscore | 8088 | Go 后端 (127.0.0.1) |
| nginx | 8081 | 反向代理 |
| systemd | — | Restart=on-failure |

- 数据目录: `<exe-dir>/data/` (config.json, backups/, scripts/)
- 前端不嵌入二进制，运行时从 `web/dist/` 读取

## 6. 分析: xianyu-auto-reply 代码问题

对比本项目，xianyu-auto-reply 存在以下问题:
- **远程广告默认开启**: 每次部署连接 `xy.zhinianboke.com` 拉取广告
- **外部服务依赖**: 卡券系统默认走 `backend.zhinianboke.com`
- **不透明 Docker 镜像**: 从阿里云个人仓库拉预构建镜像
- **供应链风险**: `curl | bash` 从作者域名下载执行脚本
- **返佣子系统**: `promotion/` 目录是淘宝返佣自动化
