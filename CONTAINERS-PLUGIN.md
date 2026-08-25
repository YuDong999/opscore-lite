# 容器管理插件 · 架构文档

> 模块 ID: `containers` · Group: `plugin` · 首个基于热生效机制的插件模块
> 关联文档: [ARCHITECTURE.md](./ARCHITECTURE.md)（宿主整体架构）

---

## 一、总体架构

```
┌─────────────────────────────────────────────────────────────────────────────┐
│                            前端 (React SPA)                                  │
│                                                                             │
│  ┌───────────────────────────────────────────────────────────────────┐      │
│  │ 侧栏三级导航 (App.tsx)                                            │      │
│  │                                                                   │      │
│  │  ┌────────────────┐                                               │      │
│  │  │ 📦 容器管理     │ ← 一级目录 (ContainerNavGroup, 可折叠)        │      │
│  │  └───┬────────┬───┘                                               │      │
│  │   ┌──▼──┐  ┌──▼──────┐                                           │      │
│  │   │Docker│  │Kubernetes│ ← 二级子项 (#/containers/docker|k8s)      │      │
│  │   │ 管理 │  │  管理    │                                           │      │
│  │   └──┬──┘  └──┬──────┘                                           │      │
│  │      ▼        ▼                                                  │      │
│  │  ┌──────────────────────────┐                                    │      │
│  │  │ ContainersModule(scope)  │ ← 三级页内 tab                     │      │
│  │  │ docker: 容器|镜像|连接走向│                                    │      │
│  │  │ k8s:    Pod巡检表(只读)   │                                    │      │
│  │  └──────────────────────────┘                                    │      │
│  └───────────────────────────────────────────────────────────────────┘      │
│                                                                             │
│  弹层: 详情(inspect) / 日志(tail≤300) / 操作确认(modal-overlay)              │
│  api/client.ts: getJSON(SWR TTL 2.5s) + postJSON(写后自动失效缓存)           │
└────────────────────────────────┬────────────────────────────────────────────┘
                                 │ HTTP REST (Bearer Token)
                                 ▼
┌─────────────────────────────────────────────────────────────────────────────┐
│                     Go Server · 插件路由层 (main.go)                          │
│                                                                             │
│   registry.Registry ── 全量注册(含未激活) ──► mux 挂载                        │
│   /api/manifest ── 按 IsPluginActive 实时过滤 ──► 侧栏动态生成                │
│                                                                             │
│   /api/plugins/containers/list    ► ContainerListHandler                    │
│   /api/plugins/containers/detail  ► ContainerDetailHandler                  │
│   /api/plugins/containers/action  ► ContainerActionHandler                  │
│   /api/plugins/containers/images  ► ContainerImagesHandler                  │
│   /api/plugins/containers/logs    ► ContainerLogsHandler                    │
│   /api/plugins/containers/flows   ► ContainerFlowsHandler                   │
│                                                                             │
│        ┌──────────────────────────────────┐                                 │
│        │ pluginGuard("containers")        │ ← 未激活 = 403 (热生效守卫)      │
│        └──────────────────────────────────┘                                 │
└────────────────────────────────┬────────────────────────────────────────────┘
                                 ▼
┌─────────────────────────────────────────────────────────────────────────────┐
│                        Handler 业务层 (containers_plugin.go)                 │
│                                                                             │
│  读路径                          写路径                                      │
│  ├─ probeContainerRuntime       ├─ reContainerName 白名单正则               │
│  │   (支持 &rt=crictl 强制)      ├─ runtime ∈ {docker,podman} 校验           │
│  ├─ collectContainers           ├─ policy 白名单(update-policy)             │
│  │   (复用 apps.go 解析)         ├─ RunOnTarget 分发 ──────┐                 │
│  ├─ collectContainerDetail      ├─ verifyContainerState   │ 回读验证         │
│  │   (inspect: 挂载/env/网络)    │   (轮询 ≤8s → verified) │                 │
│  ├─ images/logs (tail≤500)      └─ [CONTAINER-AUDIT] 日志  │                 │
│  └─ flows (conntrack+ss)                                   │                 │
│                                                            ▼                 │
│  缓存层: ServeCachedJSON(TTL 5s + singleflight)                              │
│          InvalidateRespCache(list/apps) ← 写操作成功后失效                    │
└────────────────────────────────┬────────────────────────────────────────────┘
                                 ▼
┌─────────────────────────────────────────────────────────────────────────────┐
│                              统一执行层                                       │
│                                                                             │
│  ┌─────────────────┐   本机(Linux): sh -c                                    │
│  │ RunOnTarget      │   本机(Windows): 明确报错提示选远程主机                  │
│  │ (target.go)      │   远程: ArgsToLine(Shq 单引号转义防注入)                │
│  └────────┬────────┘        └─► remotePool.ExecLine ─┐                     │
│           │                                          │ 传输故障自动换连重试  │
│  ┌────────▼────────┐                                 │                     │
│  │ execAppsCmds     │  远程 ─► remotePool.Exec(map)   │                     │
│  │ (apps.go 复用)   │                                │                     │
│  └────────┬────────┘                                 │                     │
│           │                                          ▼                     │
│  ┌────────▼────────┐                      ┌──────────────────────┐            │
│  │ runScriptOnTarget│  远程 ─► ExecScript  │  SSH 连接池 (exec.go) │            │
│  │ (flows 专用)     │   哨兵分段·单会话     │  引用计数+失效标记     │            │
│  └─────────────────┘   一次网络往返       │  30s keepalive        │            │
│                                          └──────────┬───────────┘            │
└─────────────────────────────────────────────────────┼────────────────────────┘
                                                      ▼
                                     ┌────────────────────────────┐
                                     │ 目标 Linux 主机              │
                                     │ ├ docker / podman (可写)     │
                                     │ ├ crictl / ctr   (只读)      │
                                     │ └ conntrack/nf_conntrack    │
                                     └────────────────────────────┘
```

---

## 二、插件热生效机制（本插件的运行基础）

```
 启动时                          运行时(接入/停用)
 ────────                       ────────────────
 registerCoreModules             插件中心 UI
   │ 全量 Register(含未激活)         │ POST /api/plugins/containers/activate
   ▼                               ▼
 registry ──RegisterRoutes──► mux module.SetPluginActive(id, true)
                                 │
   /api/manifest                 ├─► CentralStore(SQLite) 持久化
   实时过滤 IsPluginActive ◄──────┘
   │                                 
   ▼                                 
 侧栏即时出现/消失                    
                                    
 API 访问 ──► pluginGuard(id) ──未激活──► 403 {"error":"插件未激活"}
                    │已激活
                    ▼
                 业务逻辑

 ※ 对比旧机制: 未激活模块启动时跳过注册 → 激活需重启服务才能挂载路由
```

---

## 三、写操作安全链路（以 restart 为例）

```
前端确认弹窗                后端校验与执行                    目标主机
────────────               ──────────────────────          ─────────
点击[重启]                  ① pluginGuard 激活检查
  └─► POST /action          ② reContainerName 匹配名称
      {host,name,           ③ runtime ∈ docker/podman
       runtime,action}      ④ argv = [rt,"restart","-t","10",name]
                            ⑤ RunOnTarget(host, argv)
                              ├─ Shq() 参数单引号转义      ──► docker restart -t 10 <name>
                              └─ 传输失败自动弃连重试一次
                            ⑥ verifyContainerState 轮询回读  ──► inspect -f {{.State.Status}}
                              │ 每 500ms, 最长 8s              期望 "running"
                              ├─ 符合 → verified=true
                              └─ 超时 → verified=false(仍返回 ok=false? 否: ok=true+⚠提示)
                            ⑦ InvalidateRespCache(list+apps)
                            ⑧ 审计: [CONTAINER-AUDIT] target/runtime/action/name/verified
                            ⑨ 响应 {ok, verified, target}
前端 ◄────────────────────────
banner: ✓ restart xxx @ 主机名 · 回读验证通过
列表 300ms / 2500ms 双次刷新
```

各动作的回读验证标准：

| 动作 | 执行命令 | 回读标准 |
|---|---|---|
| start | `rt start <name>` | State.Status == running |
| stop | `rt stop -t 10 <name>` | State.Status == exited |
| restart | `rt restart -t 10 <name>` | State.Status == running |
| remove | `rt rm -f <name>` | inspect 报错(对象不存在) |
| update-policy | `rt update --restart=<p> <name>` | RestartPolicy.Name == \<p\> |

---

## 四、连接走向可视化数据流（conntrack 无侵入版）

```
┌──────────────┐  单会话脚本(ExecScript 哨兵分段·1次往返)   ┌──────────────────┐
│ 前端 tab      │ ─── GET /flows?host= ──────────────────► │ ContainerFlows    │
│ ECharts graph │                                         │ Handler           │
│ force 布局    │ ◄─── {nodes[], edges[], note?} ──────── └────────┬──────────┘
└──────────────┘                                                  │ runScriptOnTarget
                                                                  ▼
                                                    echo __OPSCORE_CT__
                                                    (conntrack -L || cat nf_conntrack)|head-400
                                                    echo __OPSCORE_MAP__
                                                    docker inspect 每个 running 容器
                                                      → "IP IP ... 容器名" 映射行
                                                                  │
                              ┌───────────────────────────────────┘
                              ▼
                    ParseSections → CT段 + MAP段
                              │
                    IP→容器名映射(ipToName) + IP 分类
                      容器命中 → container 节点
                      127.x    → host 节点
                      RFC1918/docker网段 → internal 节点
                      其余     → external 节点
                              │
                    conntrack 行解析 src/dst/proto
                    同 (src,dst,proto) 聚合 count
                              │
                              ▼
                    nodes[{id,name,type}] + edges[{source,target,proto,count}]
                    边宽 = min(1+count/4, 5) · 边标签 proto ×count

 升级路径: 未来引入 eBPF collector(Hubble/DeepFlow 式) 时,
 仅需替换 runScriptOnTarget 的数据源并保持 nodes/edges 结构不变。
```

---

## 五、目录与文件清单

```
opscore-lite/
├── main.go                              # modCfg 注册 containers + manifest 实时过滤
├── internal/
│   ├── module/
│   │   ├── manifest.go                  # coreModules 含 containers(Group=plugin)
│   │   └── plugin.go                    # IsPluginActive/SetPluginActive (SQLite 持久化)
│   ├── registry/registry.go             # Module{Manifest,Routes} 注册中心
│   └── handlers/
│       ├── plugin_guard.go              # ★ pluginGuard 403 守卫
│       ├── containers_plugin.go         # ★ 插件全部 handler(list/detail/action/images/logs/flows)
│       ├── cache.go                     # ServeCachedJSON + InvalidateRespCache
│       ├── target.go                    # RunOnTarget/Shq/ArgsToLine/remoteHostByID
│       ├── apps.go                      # 复用: detectRuntime/collectContainers/collectContainerDetail
│       └── snapshotcache.go             # (资源快照缓存, 与本插件无关但同层)
└── web/src/
    ├── App.tsx                          # ★ ContainerNavGroup 一级目录 + 双子路由(/docker,/k8s)
    ├── modules/
    │   ├── ContainersModule.tsx         # ★ scope='docker'|'k8s' 页面组件
    │   └── PluginsModule.tsx            # 插件中心(box 图标映射)
    └── api/client.ts                    # getJSON SWR / postJSON 写后失效
```

---

## 六、关键设计决策

| 决策 | 理由 |
|---|---|
| 共存而非迁移 apps.go | 所有 handler 同包，list 直接调用 collectContainers，零复制；老「应用与容器」tab 保持只读总览定位 |
| crictl/ctr 只读 | K8s 托管运行时由 kubelet 编排，UI 写操作会造成编排冲突（历史设计决策） |
| 写操作统一 RunOnTarget | 与 c47bb54 建立的防火墙/服务分发范式一致，审计含 target/verified |
| flows 用 conntrack 而非 eBPF | SSH 直连架构无法常驻内核探针；conntrack 零依赖覆盖 L3/L4 会话；结构预留 eBPF 替换位 |
| 详情复用 collectContainerDetail | apps.go 已实现四运行时 inspect 解析（挂载/env/限额），避免二次实现 |
| 激活状态存 SQLite(CentralStore) | 重启后保持用户选择，与 Phase1 存储层成果对齐 |
