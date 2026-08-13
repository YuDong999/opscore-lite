# OpsCore Lite 会话总结

## 目标
- 构建 Agent 实时数据采集层，使主机切换瞬时响应（"丝滑"），SSH 轮询作为降级回退

## 重要技术细节
- Agent 二进制：`cmd/agent/`，使用 gopsutil 采集，通过 WebSocket 推送指标快照
- Agent 部署策略：优先使用 `systemd service` 管理进程（CentOS 7+），回退到 `nohup`
- Agent 地址检测：SSH 到目标主机读取 `$SSH_CLIENT` 获取 Server 端 IP（解决 Windows 多网卡/Tailscale 虚拟网卡干扰）
- WebSocket Hub：`internal/agent/hub.go` — `map[hostID]*agentConn` + `sync.RWMutex` + `cachedSnapshot` 30s 过期
- SSH 认证支持私钥路径和密码两种方式
- `remote.Host` 和 `ansible.Host` 都包含 `Password` 字段
- `HostContext` 暴露 `refreshHosts()`，Ansible 模块在添加/更新/删除后触发主机列表刷新
- 多主机概览始终将本地机器作为第一行预置（`id:"本机"`），使用实时 gopsutil 数据
- Tasks/Storage 模块处于"受限模式"（非 root）—— 写操作（crontab、挂载、LVM）风险与已实现的 Services（start/stop）和 Network（防火墙规则）相当

## 工作状态

### 已完成
- **Agent 架构**：`cmd/agent/` 独立二进制，gopsutil 采集 + WebSocket 推送，Server 端 Hub 缓存
- **Agent 部署**：`internal/agent/deploy.go` — SSH 管道推送二进制 + systemd 启动 + 唤醒循环
- **资源缓存回退**：`handlers/resources.go:Resources()` 优先读 agent 缓存，无数据则 SSH 回退
- **主机联动**：`AnsibleHostsAdd` 触发 agent 部署，`AnsibleHostsRemove` 触发 agent 清理
- **地址检测**：`detectServerAddr()` SSH 到远程主机读 `$SSH_CLIENT`，结果缓存避免重复 SSH
- **WebSocket Server**：独立 HTTP Server 在 `:8089`（可配置），`/ws/agent` 路由
- **健康检查循环**：`StartWakeLoop()` 每 60s 检查 agent 在线状态，掉线自动重部署
- **二进制管道传输**：`ExecWithInput()` 绕过 base64/big command 限制，直接 pipe binary 到 SSH stdin
- **Makefile**：交叉编译 targets（linux-amd64, linux-arm64, windows-amd64）
- **Agent URL 修复**：`wsclient.go` 去除重复路径拼接（server 地址已含 `/ws/agent`）
- **SSH 密码认证**：`remote/exec.go` 提取 `dialHost()`；添加 `Host.Password`
- **多主机 SSH 采集**：services 和 network 后端已支持 `?host=` 参数
- **tsc + vite 构建**：通过
- **Go 构建**：通过

### 阻塞中
- (无)

## 数据流路径

```
前端请求资源 → handler.Resources(host=ikun)
  ├─ agentHub.GetSnapshot("ikun") 是否有缓存?
  │   ├─ 有 → 立即返回（毫秒级）
  │   └─ 无 → SSH 回退: remotePool.Exec(ikun, cmds) → 解析文本输出返回
  │
Agent 侧持续循环:
  collector.tick() (每2秒 gopsutil 采集)
    → push(snapshot) → wsClient.snapCh
    → pump() → conn.WriteMessage(WebSocket)
    → Server Hub.cache[hostID] 更新
```

## 相关文件
- `cmd/agent/main.go`：Agent 入口
- `cmd/agent/collector.go`：gopsutil 采集循环
- `cmd/agent/wsclient.go`：WebSocket 客户端（注意 server 地址已含路径）
- `internal/agent/hub.go`：AgentHub — WebSocket Hub + 缓存层
- `internal/agent/deploy.go`：DeployAgent / TryWakeAgent / StartWakeLoop / scpAndStart
- `internal/remote/exec.go`：SSH 连接池，ExecWithInput（二进制管道）
- `internal/remote/cmds.go`：SSH 采集命令集
- `internal/handlers/resources.go`：Resources 处理器 — agent 缓存优先 + SSH 回退
- `internal/handlers/ansible.go`：主机 CRUD 联动 agent 部署/清理
- `internal/handlers/overview.go`：多机概览（本地 + SSH 远程）
- `internal/handlers/services.go`：通过 SSH `remoteServicesList()` 实现 `?host=` 支持
- `internal/handlers/network.go`：通过 SSH 实现 `?host=` 支持
- `main.go`：Hub 初始化、agent WebSocket Server、启动部署 + 唤醒循环
- `web/src/components/HostContext.tsx`：添加 `refreshHosts()`
- `web/src/modules/AnsibleModule.tsx`：主机管理界面
- `web/src/modules/ResourcesModule.tsx`：资源展示（Agent 数据 + SSH 回退）
