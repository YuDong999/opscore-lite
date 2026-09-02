# OpsCore Lite 架构文档

> 仓库: https://github.com/YuDong999/opscore-lite · 更新于 2026-08-25（含插件热生效机制与容器管理插件）
> 子文档: [CONTAINERS-PLUGIN.md](./CONTAINERS-PLUGIN.md)（容器管理插件详设）

## 📊 架构图（图片版）

| 图 | 内容 | 文件 |
|---|---|---|
| 全集 | 六图合一(2x高清) | [docs/architecture-full@2x.png](./docs/architecture-full@2x.png) |
| ① | 系统总体分层架构 | [docs/diagrams/01-overview.png](./docs/diagrams/01-overview.png) |
| ② | 读数据三级缓存闭环 | [docs/diagrams/02-read-loop.png](./docs/diagrams/02-read-loop.png) |
| ③ | 写操作安全闭环(含失败分支) | [docs/diagrams/03-write-loop.png](./docs/diagrams/03-write-loop.png) |
| ④ | 插件热生效生命周期 | [docs/diagrams/04-plugin-hotreload.png](./docs/diagrams/04-plugin-hotreload.png) |
| ⑤ | Agent 部署与心跳自愈闭环 | [docs/diagrams/05-agent-lifecycle.png](./docs/diagrams/05-agent-lifecycle.png) |
| ⑥ | 连接走向可视化管线 | [docs/diagrams/06-flows-pipeline.png](./docs/diagrams/06-flows-pipeline.png) |

源文件(SVG可编辑): [docs/architecture-diagrams.html](./docs/architecture-diagrams.html)


---

## 一、整体架构

```
┌──────────────────────────────────────────────────────────────────────────────┐
│                            前端 (React 18 + Vite + TS + ECharts 5)            │
│                                                                              │
│   App.tsx                                                                    │
│   ├─ /api/auth/token ──► LoginPage (Bearer Token, 401 自动跳转)                │
│   ├─ /api/manifest ────► 动态侧栏 (核心模块组 + 插件组, 实时过滤)                │
│   │      ▲ manifest-changed 事件驱动重拉 → 插件接入/停用即时生效                 │
│   ├─ HostProvider ──────► 主机上下文 (sessionStorage 记忆所选远程主机)           │
│   └─ Routes ───────────► modules/*.tsx (7 核心页 + 插件页)                     │
│                                                                              │
│   api/client.ts: GET SWR 缓存(TTL 2.5s, 切换秒出) · POST 写后失效 · 实时端点排除 │
│   charts/EChart.tsx: ECharts 封装 (init/resize/setOption 增量更新)             │
└───────────────┬──────────────────────────────────────────────────────────────┘
                │ HTTP REST (Bearer) + SSE(Ansible流) + WebSocket(Agent)
                ▼
┌──────────────────────────────────────────────────────────────────────────────┐
│                          Go Server (main.go, 标准库 net/http)                  │
│                                                                              │
│  ┌───────────┐ ┌──────────────────────────────┐ ┌──────────────────────────┐ │
│  │ auth.MW   │ │ 模块注册中心 (热生效)          │ │ HTTP Handlers (29 文件)   │ │
│  │ Bearer    │ │ registry: 全量注册路由         │ │ 8 模块 / 60+ 端点        │ │
│  │ 可选TOKEN │ │ /api/manifest: IsPluginActive │ │ core: resources/services │ │
│  └───────────┘ │ 实时过滤                      │ │  network/diagnostics     │ │
│                │ pluginGuard: 未激活 403       │ │  tasks/ansible/plugins   │ │
│                │ module.SetPluginActive(SQLite)│ │ plugin: containers ★     │ │
│                └──────────────────────────────┘ └──────────────────────────┘ │
│                                                                              │
│  ┌──────────────────────── 统一执行层 ────────────────────────┐               │
│  │                                                            │               │
│  │ ┌──────────────┐ ┌──────────────────┐ ┌─────────────────┐  │               │
│  │ │ metrics      │ │ remote.Pool      │ │ agent.AgentHub   │  │               │
│  │ │ gopsutil     │ │ SSH 连接池        │ │ WebSocket Hub    │  │               │
│  │ │ 本机 2s 后台  │ │ 引用计数+失效标记  │ │ Agent 注册/缓存   │  │               │
│  │ │ 快照+Get()   │ │ 单会话哨兵脚本     │ │ lastSeen 60s清理  │  │               │
│  │ └──────────────┘ │ 30s keepalive    │ └─────────────────┘  │               │
│  │                  │ ExecLine 重试     │                      │               │
│  │                  └──────────────────┘                      │               │
│  │                                                            │               │
│  │ ┌────────────────── 缓存层 (消除切换卡顿) ──────────────┐    │               │
│  │ │ snapshotcache.go: 远程快照 per-host TTL2s + singleflight│  │               │
│  │ │ cache.go: 通用 GET 响应缓存(TTL可配)+singleflight+失效  │    │               │
│  │ │ InvalidateRespCache(): 写操作后定向失效                 │    │               │
│  │ └────────────────────────────────────────────────────────┘   │               │
│  └────────────────────────────────────────────────────────────────┘           │
│                                                                              │
│  ┌──────────────────────────── 持久化层 ────────────────────────────┐          │
│  │ central.CentralStore 接口: SQLiteStore(modernc) ⇄ PostgreSQL(pgx) │          │
│  │ ├─ 模块激活状态 / 认证                                            │          │
│  │ ├─ store.JSONFile: ansible_hosts.json / playbooks / history      │          │
│  │ └─ /api/system/migrate: SQLite→PG 迁移 (Export/Import)           │          │
│  └──────────────────────────────────────────────────────────────────┘          │
└──────────┬──────────────────────────────────┬─────────────────────────────────┘
           │ SSH (remote.Pool)                 │ WebSocket (:8089/ws/agent)
           ▼                                  ▼
┌─────────────────────┐        ┌──────────────────────────────────────────┐
│ 远程 Linux 主机       │        │ 带 Agent 的远程主机                        │
│ (无 Agent, 降级路径)  │        │ cmd/agent (systemd 守护, 自动 SSH 部署)   │
│ ├ SnapshotScript    │        │ ├ collector.go: gopsutil 每 2s 采集       │
│ │  10命令→1次往返    │        │ ├ wsclient.go: 指数退避重连(1s→30s)        │
│ ├ RunOnTarget 写操作 │        │ └ snapshot JSON over WS → hub.cache       │
│ ├ hostkey 校验       │        └──────────────────────────────────────────┘
│ └ docker/podman/crictl│
└─────────────────────┘
```

---

## 二、包依赖图

```
main.go
 ├── internal/registry      # Module{Manifest,Routes} 注册中心 + RegisterRoutes(mux)
 ├── internal/module        # Manifest 定义 + 插件激活状态 (CentralStore 持久化)
 ├── internal/auth          # Bearer Token 中间件 (可选)
 ├── internal/central       # CentralStore 接口: SQLite / PostgreSQL 双实现 + 迁移
 ├── internal/handlers      # 全部 HTTP Handler (29 文件)
 │    ├── internal/metrics  # 本机指标(gopsutil) 2s 后台快照
 │    ├── internal/remote   # SSH 执行引擎: Pool/ExecLine/ExecScript/hostkey
 │    ├── internal/agent    # AgentHub(WS) + Agent 自动部署(systemd)
 │    ├── internal/ansible  # 主机清单/Playbook/SSE 执行
 │    ├── internal/store    # 通用 JSON 文件存储(读写锁)
 │    └── internal/cmds     # 共享常量
 └── cmd/agent              # 独立二进制: WS 连回 Server, 仅推送快照
```

---

## 三、模块注册与插件热生效

```
编译期                              运行期
────────                           ─────────────────────────────
main.go registerCoreModules         插件中心 UI (/plugins)
  8 个 modCfg{Manifest,Routes}        │ POST /api/plugins/{id}/activate
  core: resources/services/network    ▼
        diagnostics/tasks/ansible  module.SetPluginActive(id,bool)
  plugin: plugins/containers★        └─► CentralStore(SQLite) 持久化
        │ 全量注册(不再跳过未激活)
        ▼                          /api/manifest = reg.Active()
      registry.RegisterRoutes ──►    └─ 按 IsPluginActive 实时过滤
      mux.HandleFunc(...)             → 侧栏即时增减, 无需重启
                                    
                                    API 请求 ──► pluginGuard(id)
                                      未激活 → 403 | 已激活 → 放行

模块契约:
  type Manifest struct{ ID, Name, Icon, RoutePath, Group("core"|"plugin"), Description }
新增插件三步: ① modCfg 加一条(Manifest+Routes) ② handler 加 pluginGuard
             ③ 前端 MODULE_MAP 加组件 (+侧栏分组渲染可选)
```

**9 大模块一览：**

| ID | 名称 | Group | 代表能力 |
|---|---|---|---|
| resources | 系统资源 | core | CPU仪表/内存/磁盘树/每核/网络吞吐, 多机大屏 |
| services | 服务发现 | core | systemd 启停+回读验证, 应用与容器只读总览(Nginx站点) |
| network | 防火墙和网络 | core | 接口/监听端口/防火墙读写+审计/拓扑图/netconfig |
| diagnostics | 系统诊断 | core | 网络诊断/登录审计/系统更新 |
| tasks | 任务与存储 | core | cron CRUD/磁盘分区挂载/LVM/SMART |
| ansible | Ansible 多机管理 | core | 清单/Playbook/Ad-hoc/SSE 实时流/SSH 密钥 |
| cicd | CI/CD 流水线 | core | 代码库/镜像仓库连接, K8s·Docker·裸机发布模板, Webhook·cron·手动触发, 进度与实时日志, 凭据中心/脚本库 (见 docs/cicd) |
| plugins | 插件中心 | plugin | Manifest 契约展示/扫描/接入移除 |
| containers | 容器管理 ★ | plugin | Docker 启停删除日志镜像连接走向/K8s 只读巡检 |

---

## 四、数据采集三层架构

```
第三层 HTTP Handler
   handlers.Resources(?host=)
        │
        ├─ agentHub.GetSnapshot(hostID) ──命中──► 毫秒级返回 ◄── 缓存优先
        │        │未命中(不报错, 直接降级)
        ▼        ▼
第二层 Agent(WS推送)          第二层 SSH 回退
  每 2s 全量 gopsutil           cachedRemoteSnapshot(host)      ← 缓存包装
  hub.cache[hostID]              ├─ per-host TTL 2s 命中即返
                                 └─ singleflight 合并并发
                                      │ miss
                                      ▼
                              remoteResourceSnapshot
                              remotePool.ExecScript(SnapshotScript)
                              ★ 10 条命令合并单会话 1 次往返
                                (CPU=/proc/stat 双采样, 含每核占用)
第一层 内核/系统接口
  gopsutil v4 | top/free/df/proc 解析 | conntrack
```

**多机总览** `/api/core/resources/overview`：本机 `metrics.Get()` + 各主机并行 `cachedRemoteSnapshot`（WaitGroup），单台故障不影响整屏。

---

## 五、通信模式

| 协议 | 路径 | 用途 |
|---|---|---|
| REST GET | `/api/core/*` `/api/plugins/*` | 读数据; 前端 SWR 2.5s + 服务端 TTL 缓存双加速 |
| REST POST | `*/action` `*/add` ... | 写操作; 统一范式: 白名单校验→RunOnTarget→回读验证(verified)→审计(target)→响应缓存失效 |
| WebSocket | `:8089/ws/agent` | Agent register/snapshot 心跳(2s), 断线指数退避重连 |
| SSE | `/api/ansible/sse/*` | Playbook/Ad-hoc 逐行实时输出 |
| SSH | remote.Pool | 无 Agent 降级执行 + 写操作分发; hostkey 校验(da13610) |

---

## 六、安全设计

```
写操作统一管线 (firewall/services/containers/tasks-disk 共用):
  参数白名单正则 ──► RunOnTarget 分发 ──► 回读验证 verified ──► 审计记录 target
       │                  │                       │                  │
  reContainerName     本机 exec / 远程 Shq()     systemctl is-active   [FW-AUDIT]
  validUnitName       单引号转义防注入           inspect State.Status  [CONTAINER-AUDIT]
  fwCmdParams 校验    传输故障自动重试           轮询窗口≤6~8s          AuditEntry{TS,Target,...}

其他: TOKEN Bearer 认证(可选) · SSH hostkey 校验 · 日志 tail 上限 · K8s 托管运行时(crictrl/ctr)强制只读
```

---

## 七、构建与部署

```bash
make build          # 前端 vite build + Go 二进制
make agent-all      # agent-linux-{amd64,arm64,loong64} + windows-amd64
./opscore -addr :8088 -dist web/dist -data ./data [-database $DATABASE_URL]
# Windows 开发机: go build -o bin\opscore-server-test.exe . && 启动 -addr 127.0.0.1:8088
```

---

## 八、关键设计决策

| 决策 | 说明 |
|---|---|
| Agent 用 systemd 管理 | 脱离 SSH 会话生命周期(CentOS7 SIGHUP 问题); 无 systemd-run 时回退 nohup |
| Server 地址经 SSH_CLIENT 检测 | 多网卡下 net.InterfaceAddrs 会取到 Tailscale 等虚接口 |
| 二进制走 SSH stdin 管道 | 规避 base64 命令行长度限制(6MB EOF 问题) |
| SSH 单会话脚本(SnapshotScript) | N 次 session 往返→1 次; 冷 ~0.5s, 配合 TTL 缓存 <1ms |
| 连接池引用计数+失效标记 | 消除并发误关连接("use of closed network connection"根因) |
| 三级缓存(前端SWR/响应TTL/快照TTL) | 切换模块/主机秒出数据, singleflight 防雪崩 |
| 插件全量注册+运行时守卫 | 激活/停用热生效, 免重启(2026-08-25 引入, 容器管理为首插件) |
| crictl/ctr 只读 | 避免 UI 写操作与 kubelet 编排冲突 |
