# OpsCore Lite 架构文档

## 一、整体架构

```
┌────────────────────────────────────────────────────────────────────────┐
│                       前端 (React SPA)                                 │
│  web/src/ │ App.tsx │ modules/*.tsx │ api/client.ts                  │
└────────────────────────┬───────────────────────────────────────────────┘
                         │ HTTP REST + SSE + WebSocket
                         ▼
┌────────────────────────────────────────────────────────────────────────┐
│                        Go 服务器 (main.go)                             │
│                                                                        │
│  ┌──────────────┐  ┌────────────────┐  ┌────────────────────────┐    │
│  │  认证层      │  │  模块注册中心   │  │  HTTP 处理器 (40+)     │    │
│  │ auth.MW     │  │  registry       │  │  handlers.*            │    │
│  │ Bearer Token│  │  动态路由挂载    │  │  7 个功能域             │    │
│  └──────────────┘  └───────┬────────┘  └──────────┬─────────────┘    │
│                            │                      │                   │
│  ┌─────────────────────────▼──────────────────────▼──────────────┐   │
│  │                    数据采集/执行层                             │   │
│  │                                                               │   │
│  │  ┌──────────────┐  ┌────────────────┐  ┌────────────────┐   │   │
│  │  │ metrics      │  │ remote.Pool    │  │ agent.AgentHub │   │   │
│  │  │ gopsutil     │  │ SSH 连接池     │  │ WebSocket Hub  │   │   │
│  │  │ 本机 2s 采集  │  │ 远程命令执行    │  │ Agent 连接管理  │   │   │
│  │  └──────────────┘  └────────────────┘  └────────────────┘   │   │
│  └──────────────────────────┬───────────────────────────────────┘   │
│                             │                                       │
│  ┌─────────────────────────▼───────────────────────────────────┐   │
│  │                      持久化层                                │   │
│  │  ┌──────────────┐  ┌────────────────┐  ┌────────────────┐   │   │
│  │  │ central      │  │ store.JSONFile │  │ ansible.Manager│   │   │
│  │  │ SQLite/      │  │ 通用 JSON 存储  │  │ 主机/库存/历史  │   │   │
│  │  │ PostgreSQL   │  │ (读写锁保护)    │  │ JSON 持久化    │   │   │
│  │  └──────────────┘  └────────────────┘  └────────────────┘   │   │
│  └──────────────────────────────────────────────────────────────┘   │
└──────────────────────────────────────────────────────────────────────┘
          │                              │
          │ SSH (remote.Pool)             │ WebSocket (agent.Hub)
          ▼                              ▼
┌────────────────────┐    ┌──────────────────────────────────────┐
│ 远程 Linux 主机     │    │ 带 Agent 的远程主机                   │
│ (无 Agent)         │    │ agent-linux-amd64 (systemd 守护)     │
│ └─ 命令执行采集    │    │ └─ gopsutil → 每2秒 WS 推送          │
└────────────────────┘    └──────────────────────────────────────┘
```

## 二、模块耦合关系

### 2.1 包依赖图（自上而下）

```
main.go
 ├── internal/registry      (模块注册 + 路由挂载)
 ├── internal/module        (模块状态 + Manifest)
 ├── internal/auth          (认证中间件)
 ├── internal/central       (中心存储抽象)
 ├── internal/handlers      (HTTP 处理器)
 │    ├── internal/metrics  (本机指标缓存)
 │    ├── internal/remote   (SSH 执行引擎)
 │    ├── internal/agent    (Agent Hub + 部署)
 │    ├── internal/ansible  (Ansible 管理)
 │    └── internal/store    (JSON 持久化)
 └── cmd/agent              (独立二进制, 仅 WS 连接 Server)
```

### 2.2 耦合方式

| 耦合类型 | 示例 | 解耦手段 |
|---------|------|---------|
| **函数参数注入** | `handlers.InitPool(pool)`、`handlers.InitAgentHub(hub)` | 包级全局变量，main.go 启动时注入 |
| **接口抽象** | `central.CentralStore` 接口 | SQLiteStore / PostgreSQLStore 可互换 |
| **JSON 文件** | `store.JSONFile` 通用读写 | Read/Write 接口，读写锁保护 |
| **网络协议** | Agent ↔ Server 通过 WebSocket | 二进制独立，协议解耦（JSON over WS） |
| **SSH 协议** | Server ↔ 远程主机通过 SSH | 无 Agent 降级方案，命令集在 `cmds.go` 定义 |

## 三、数据采集机制

### 3.1 三层采集架构

```
┌──────────────────────────────────────────┐
│          第三层: HTTP Handler             │
│  handlers.Resources(w, r)                │
│  └─ 优先 agentHub.GetSnapshot()          │
│     └─ 回退 remotePool.Exec()            │
└──────────────────────────────────────────┘
                   │
    ┌──────────────┴──────────────┐
    ▼                             ▼
┌──────────────┐          ┌──────────────┐
│ 第二层: Agent │         │ 第二层: SSH  │
│ 实时 WebSocket│         │ 命令执行     │
│ 每 2 秒推送   │         │ 每次请求执行  │
│ 缓存最新快照   │         │ 阻塞等待结果  │
└──────────────┘          └──────────────┘
    │                             │
    ▼                             ▼
┌──────────────┐          ┌──────────────┐
│ 第一层:      │          │ 第一层:      │
│ gopsutil    │          │ shell 命令   │
│ 跨平台系统   │          │ top/free/df  │
│ 指标库       │          │ /proc 解析   │
└──────────────┘          └──────────────┘
```

### 3.2 本机采集（metrics 包）

```go
// internal/metrics/metrics.go
func Start()                    // 启动后台 goroutine
func Get() Snapshot             // 读取最新缓存（非阻塞）

// 后台循环 (每 2 秒):
for {
    mu.Lock()
    current = tick()            // gopsutil 全量采集
    mu.Unlock()
    time.Sleep(2 * time.Second)
}
```

采集内容：CPU（总量+单核）、内存（总量+已用+Swap）、磁盘（每分区）、网络（每网卡速率+总量）、Load、Hostname、Uptime、Platform、Docker 检测。

### 3.3 Agent 远程采集（cmd/agent）

```
┌───────────────────────────────────────────┐
│           远程主机上的 Agent                │
│                                           │
│  collector.go                             │
│    newCollector()                         │
│      └─ go run(push)                      │
│           └─ tick() (每 2 秒)             │
│                ├─ gopsutil 全量采集        │
│                └─ push(Snapshot)          │
│                     └─ snapCh ← snap     │
│                                           │
│  wsclient.go                              │
│    run(stop)                              │
│      ├─ Dial("ws://server:8089/ws/agent") │
│      ├─ 发送 register {hostID}            │
│      └─ pump()                            │
│           └─ 每 2 秒:                     │
│                snapCh → conn.WriteMessage │
│                {type:"snapshot", data:...} │
│                                           │
└─────────────────────┬─────────────────────┘
                      │ WebSocket
                      ▼
┌───────────────────────────────────────────┐
│           Server 侧 AgentHub               │
│                                           │
│  hub.go                                   │
│    ServeWS(ws)                            │
│      ├─ 收到 "register"                   │
│      │   conns[hostID] = agentConn        │
│      │   日志: "registered"               │
│      └─ 收到 "snapshot"                   │
│           cache[hostID] = Snapshot        │
│           lastSeen = now                  │
│                                           │
│    GetSnapshot(hostID)                    │
│      └─ 读 cache[hostID], 返回 &Snap, ok  │
│                                           │
│    cleanLoop() (每 15 秒)                 │
│      └─ lastSeen > 60s → 断开删除         │
└───────────────────────────────────────────┘
```

### 3.4 SSH 远程采集（无 Agent 降级）

```go
// internal/remote/cmds.go
var Cmds = map[string]string{
    "CpuUsage":  `top -bn1 | grep "Cpu(s)" | awk '{print $2}'`,
    "MemInfo":   `free -b | awk '/^Mem/ {print $2,$3,$4}'`,
    "DiskInfo":  `df -B1 --exclude-type=tmpfs --exclude-type=devtmpfs ...`,
    "NetDev":    `cat /proc/net/dev | tail -n +3 | awk '{print $1,$2,$10}'`,
    "Hostname":  `hostname`,
    "Uptime":    `cat /proc/uptime | awk '{print $1}'`,
    "OsRelease": `cat /etc/*release 2>/dev/null | head -1`,
    "LoadAvg":   `cat /proc/loadavg | awk '{print $1,$2,$3}'`,
    "CpuModel":  `cat /proc/cpuinfo | grep "model name" | head -1 | cut -d: -f2`,
    "CpuCores":  `nproc`,
}
```

## 四、通信模式

### 4.1 HTTP REST（主通信）

```
前端 → Server:
  GET  /api/core/resources?host=ikun     → handlers.Resources
  POST /api/ansible/hosts/add            → handlers.AnsibleHostsAdd
  POST /api/ansible/adhoc                → handlers.AnsibleAdhoc

Server → 前端:
  Content-Type: application/json
  标准响应: {data} 或 {error: "..."}
  状态码: 200, 202(Agent在线但无数据), 400, 404, 502
```

### 4.2 WebSocket（Agent → Server）

```
Agent → Server: ws://server:8089/ws/agent
  register:  {type:"register", hostID:"ikun"}
  snapshot:  {type:"snapshot", data:{CPU,Memory,Disk,...}}
  心跳: 每 2 秒 snapshot 消息自带心跳

Server → Agent:
  registered: {type:"registered"}  (register 回执)
```

Agent 端重连策略：
```
首次 backoff = 1s
每次失败 ×2，上限 30s
成功连接后重置为 1s
```

### 4.3 SSH（Server → 远程主机）

```
Server → 远程主机:
  连接: ssh.Dial("tcp", "192.168.94.20:22", config)
  认证: 私钥签名 / 密码
  执行: session.CombinedOutput("cmd")
  结果: 字符串文本输出

连接池:
  Pool.conns map[hostID]*ssh.Client
  保活: 每 30s keepalive@openssh.com
  超时断开自动重连
```

### 4.4 SSE（Server → 前端，Ansible 实时流）

```
Server → 前端:
  Content-Type: text/event-stream
  data: {type:"line", payload:"ok: ..."}
  data: {type:"result", payload:[...]}
  data: {type:"done"}
```

## 五、消息回传路径

### 5.1 资源数据（最短路径）

```
Agent 采集 (每 2s)
  → WebSocket snapshot 消息
  → Server AgentHub.cache[hostID] = Snapshot
  → 前端 GET /api/core/resources?host=ikun
  → handler.Resources()
      → agentHub.GetSnapshot(hostID)
      → WriteJSON(w, snap)  ← 毫秒级响应
```

### 5.2 资源数据（SSH 回退路径）

```
前端 GET /api/core/resources?host=ikun
  → agentHub.GetSnapshot("ikun") → false
  → agentHub.IsOnline("ikun") → false
  → remotePool.Exec(ikun, remote.Cmds)
      → SSH 连接 → 执行 11 个shell命令
      → 解析文本输出 → metrics.Snapshot
  → WriteJSON(w, snap)  ← 秒级响应（取决于网络延迟）
```

### 5.3 多机概览

```
前端 GET /api/core/resources/overview
  → handlers.MultiOverview()
  → metrics.Get()           // 本机数据
  → ansibleMgr.ListHosts()  // 所有远程主机
  → for each host:
      remotePool.Exec(host, remote.Cmds)
      → 解析 → OverviewHost
  → WriteJSON(w, {hosts: [...], updated: now})
```

### 5.4 Ansible Playbook 执行

```
前端 POST /api/ansible/sse/playbook
  → handlers.AnsibleSSEPlaybookExec()
  → SSE Header (text/event-stream)
  → ansibleMgr.SSERunPlaybook(id, inventory, emit)
      → ansible-playbook 命令
      → 逐行读取 stdout → emit("line")
      → 完成后解析结果 → emit("result")
      → emit("done")
  → 前端 EventSource 逐行接收
```

### 5.5 Agent 部署流程

```
Server 启动 / 主机添加 / 主机掉线
  → agent.DeployAgent/DeployToAll/TryWakeAgent
  → doDeploy(pool, host)
      1. pickAgentBinary()         // bin/agent-linux-amd64
      2. detectServerAddr()         // SSH $SSH_CLIENT → server IP
      3. pool.Exec("kill old")     // 清理旧进程
      4. pool.ExecWithInput("cat > /tmp/opscore-agent")
         // 管道写入二进制 (6MB)
      5. pool.Exec("chmod +x")
      6. ExecWithInput("cat > /etc/systemd/system/opscore-agent.service")
         // 写入 systemd 单元文件
      7. pool.Exec("systemctl daemon-reload && systemctl restart")
         // systemd 管理 agent 进程
      8. agent 进程启动
         → WebSocket 连接 Server
         → register + snapshot 循环
```

## 六、关键设计决策

### 6.1 为什么 Agent 用 systemd 管理而不是 nohup？

**问题**：SSH session 关闭后，`nohup ... &` 后台进程在 CentOS 7 上会被 SIGHUP 杀死。
**解决**：`systemd-run --scope` 或 systemd service 创建独立 cgroup，彻底脱离 SSH 会话生命周期。
**回退**：CentOS 7 无 systemd-run 时回退到 `nohup`。

### 6.2 为什么 Server 地址通过 SSH_CLIENT 检测？

**问题**：`net.InterfaceAddrs()` 在多网卡环境下返回第一个非 lo 地址（如 Tailscale `198.18.0.1`），而不是内网 IP。
**解决**：SSH 到目标主机，读取 `$SSH_CLIENT` 环境变量（SSH 客户端 IP），确保 Agent 连接正确的内网地址。

### 6.3 为什么二进制通过 SSH stdin 管道传输？

**问题**：`echo base64... | base64 -d` 在 6MB 二进制上触发命令行长度限制，导致 EOF 错误。
**解决**：`ExecWithInput()` 通过 `session.StdinPipe()` 直接写入二进制数据，不受命令行长度限制。

### 6.4 为什么 Agent URL 会重复拼接？

**问题**：`detectServerAddr()` 返回 `ws://ip:8089/ws/agent`（已含路径），但 `wsclient.go` 又拼接了 `/ws/agent`，导致最终 URL 为 `.../ws/agent/ws/agent`。
**解决**：Agent 直接使用 `w.server` 作为连接地址，Server 端保证 `AgentServerAddr` 是完整 URL。

## 七、配置与环境变量

| 变量 | 默认值 | 说明 |
|------|--------|------|
| `OPCORE_AGENT_SERVER` | (自动检测) | Agent WebSocket Server 地址，显式设置跳过 SSH_CLIENT 检测 |
| `OPCORE_AGENT_LISTEN` | `:8089` | Agent WebSocket 监听地址 |
| `--agent-addr` | `:8089` | 命令行参数，等价于 `OPCORE_AGENT_LISTEN` |
| `--data` | `./data` | 数据目录（SQLite + JSON + 日志） |
| `--port` | `8088` | HTTP 服务端口 |
| `TOKEN` | (空=无认证) | Bearer Token |
| `DATABASE_URL` | (空=SQLite) | PostgreSQL 连接串 |

## 八、构建与部署

```bash
# 构建 Server (Windows)
go build -ldflags="-s -w" -o bin/opscore-server.exe .

# 构建 Agent (Linux 交叉编译)
GOOS=linux GOARCH=amd64 go build -ldflags="-s -w" -o bin/agent-linux-amd64 ./cmd/agent/

# 启动
opscore-server.exe --data ./data

# Agent 部署（服务端自动完成，无需手动操作）
# 当添加主机或启动时，Server 自动 SSH 部署 Agent
```

## 九、术语表

| 术语 | 说明 |
|------|------|
| Agent | 部署在远程主机上的独立二进制，负责采集指标并通过 WebSocket 上报 |
| Hub | Server 端的 Agent 连接管理器，维护 WebSocket 连接和指标缓存 |
| Snapshot | 单次全量系统指标快照，包含 CPU/内存/磁盘/网络/Load 等 |
| SSH Pool | 持久化 SSH 连接池，30 秒保活，自动重连 |
| SSE | Server-Sent Events，用于 Ansible 实时执行流推送 |
| CentralStore | 中心存储接口，支持 SQLite 和 PostgreSQL 实现 |
| Registry | 模块注册中心，管理功能模块的注册和路由自动挂载 |
