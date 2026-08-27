# Kubernetes 管理改造方案（参考 kubevision）

> 目标：把容器管理插件的「Kubernetes 管理」从 **SSH+crictl 只读巡检** 升级为 **中央直连 kubeconfig 的多集群管理**
> 双形态部署：既可作为插件接入 opscore-lite 中央，也可独立运行（模块自治原则）
> 参考项目: https://github.com/gocronx/kubevision (MIT)
> 关联文档: [CONTAINERS-PLUGIN.md](./CONTAINERS-PLUGIN.md) · [ARCHITECTURE.md](./ARCHITECTURE.md)

---

## 一、现状 vs 目标

| 维度 | 现状 | 改造后 |
|------|------|--------|
| 连接方式 | SSH 到目标主机跑 `crictl ps` / `ctr c list` | client-go dynamic client 直连 API Server |
| 数据实时性 | HTTP 轮询 + 5s 缓存 | API 直读 + 缓存（二期 Informer Watch 推送） |
| 功能范围 | Pod 巡检表（只读，无日志无详情） | 多集群注册 / 工作负载 / 网络 / 配置 / 存储 / Node / Event / Pod 日志 |
| 集群数量 | 单节点视角 | 多集群，kubeconfig 注册制 |
| 部署形态 | 仅作为 opscore-lite 插件 | 插件接入 + 独立运行两种模式 |

## 二、交互设计（已确认）

### 2.1 导航层级收敛

```
旧:  📦 容器管理(一级目录)
       ├── Docker 管理   (#/containers/docker)
       └── Kubernetes    (#/containers/k8s)

新:  📦 容器管理 (一级菜单, 点击进入)
       ┌──────────────────────────────────────┐
       │  [Docker] [K8S]   ← 页内一级 Tab      │
       │  (Docker tab = 现有功能不动)           │
       └──────────────────────────────────────┘
```

### 2.2 K8S 页内嵌侧边栏（kubevision 风格）

```
┌────────────────────────────────────────────────────────────────┐
│ [Docker] [K8S]                          ← Tab 切换             │
├───────────────┬────────────────────────────────────────────────┤
│ ◈ 集群         │                                                │
│  ▸ prod-1     │              主内容区                           │
│  ▾ staging    │   ┌──────────────────────────────────────┐     │
│  [+ 注册集群]  │   │ Namespace 选择器  [all ▾]            │     │
│               │   ├──────────────────────────────────────┤     │
│ ◈ 资源分类     │   │ data-table 资源列表                  │     │
│  概览          │   │ name/status/age/... 行操作           │     │
│  工作负载      │   │                                      │     │
│   Pods        │   │                                      │     │
│   Deployments │   │                                      │     │
│   StatefulSets│   └──────────────────────────────────────┘     │
│   DaemonSets  │                                                │
│   Jobs/CronJobs│                                               │
│  网络          │                                                │
│   Services    │   日志/YAML 详情 → 弹层(modal-overlay 复用)      │
│   Ingresses   │                                                │
│  配置          │                                                │
│   ConfigMaps  │                                                │
│   Secrets     │                                                │
│  存储          │                                                │
│   PV/PVC/SC   │                                                │
│  集群          │                                                │
│   Nodes       │                                                │
│   Events      │                                                │
└───────────────┴────────────────────────────────────────────────┘
```

- 左侧栏选中态驱动右侧主区切换；集群未注册时主区显示「+ 注册集群」引导页（上传 kubeconfig 文件）
- 一期资源范围：Pods / Deployments / Services / ConfigMaps / Secrets / Nodes / Events（7 类），StatefulSet/Jobs/Ingress/PV 组二期补齐

## 三、总体架构（双形态）

```
┌───────────────────────────────────────────────────────────────┐
│ 前端 ContainersModule                                          │
│   scope=docker → 现有 Docker 功能                              │
│   scope=k8s    → K8sShell: 内嵌侧栏 + 主内容区                 │
└──────────────────────────┬────────────────────────────────────┘
                           │ REST
            ┌──────────────┴───────────────┐
            ▼ 联邦模式                      ▼ 独立模式
┌──────────────────────────┐   ┌──────────────────────────────┐
│ opscore-lite 中央         │   │ cmd/kubemod/main.go          │
│ /api/plugins/containers/ │   │ 自带端口(默认 :8090)           │
│ k8s/* 经 pluginGuard     │   │ 本地 SQLite/文件存储           │
│ 权限由中央统一管理         │   │ 不依赖中央, 单独登录后续接      │
└──────────────┬───────────┘   └──────────────┬───────────────┘
               └──────────┬───────────────────┘
                          ▼
        internal/kubernetes/   ← 核心包, 与宿主无关
          ├─ cluster.go   Manager: Add(kubeconfig)/Remove/Probe/ListIDs
          │               rest.Config + dynamic.Interface (RWMutex)
          ├─ resource.go  ListResources(gvr, ns) / PodLogs(tail)
          └─ *_test.go    dynamicfake 单测
                          │
                          ▼ client-go HTTPS
                   各集群 API Server (:6443)

持久化(两形态一致):
  元数据表 k8s_clusters: id/name/api_server/version/status/created_at
  kubeconfig 文件: <dataDir>/kubeconfigs/<id>.yaml (0600), 凭据不进 DB
```

## 四、API 设计

统一前缀 `k8sapi := "/api/plugins/containers/k8s"`（联邦）；独立模式下为 `/api/v1/k8s`，handler 层复用同一套函数。

```
POST   {k8sapi}/clusters                 body:{name, kubeconfig(base64或multipart)} → 注册+Probe
GET    {k8sapi}/clusters                 → [{id,name,api_server,version,status}]
DELETE {k8sapi}/clusters/{id}            → 注销(删内存客户端+DB记录+kubeconfig文件)
POST   {k8sapi}/clusters/{id}/probe      → 重测连通性(/version, 5s超时)
GET    {k8sapi}/clusters/{id}/namespaces → ns 下拉数据源
GET    {k8sapi}/clusters/{id}/{res}?ns=  → res ∈ pods|deployments|services|configmaps|secrets|nodes|events
GET    {k8sapi}/clusters/{id}/pods/{ns}/{name}/logs?tail=500 → Pod 日志
```

约束：
- 全部走 `pluginGuard("containers")` —— 插件停用即 403，热生效机制不变
- 一期纯只读；写操作二期单独评审
- 列表复用 `ServeCachedJSON(TTL 5s)`；返回精简结构（name/ns/status/node/restarts/age/ip）
- 操作审计 `[K8S-AUDIT] cluster=%s action=%s`

## 五、阶段拆解

### 阶段 A：internal/kubernetes 核心包（1 天）
- [ ] go.mod 引入 `k8s.io/client-go`, `k8s.io/apimachinery`（dynamic + discovery 最小集）
- [ ] cluster.go Manager + resource.go（与宿主完全解耦，不 import handlers）
- [ ] dynamicfake 单测：Add/Remove/List 各 GVR
- 验收: `go test ./internal/kubernetes/...` 通过

### 阶段 B：联邦模式接入（1 天）
- [ ] CentralStore 新表 `k8s_clusters`（含 migration）
- [ ] handlers/k8s_clusters.go（薄适配层：参数校验→调核心包）
- [ ] main.go containers 插件路由组挂载；启动时按 DB 重载 kubeconfig
- 验收: curl 注册 kubeconfig → probe 返回版本 → 重启服务仍在

### 阶段 C：只读资源 API（1 天）
- [ ] 7 类资源列表 + namespaces + pod logs
- [ ] 参数校验（cluster 存在性、ns 正则白名单）、tail 上限 500 对齐容器日志
- 验收: 对测试集群各端点 curl 返回正确 JSON

### 阶段 D：前端改造（1.5 天）
- [ ] App.tsx 移除容器管理二级导航（#/containers/docker|k8s → #/containers + 页内 tab state）
- [ ] 新组件 `web/src/components/K8sShell.tsx`：内嵌侧栏(集群树+资源分组) + 主内容区
- [ ] 新组件 `K8sResourceTable.tsx`（通用表格，列配置按资源类型映射）
- [ ] 「注册集群」弹窗（上传 kubeconfig → probe 反馈）；Namespace 选择器
- [ ] Pod 日志弹层（modal-overlay 复用）
- 验收: npm run build 通过；真实集群数据渲染正常；原 Docker tab 无回归

### 阶段 E：独立模式启动器（1 天）
- [ ] cmd/kubemod/main.go：加载 internal/kubernetes + 同一套 handlers，监听独立端口
- [ ] 本地 JSON/SQLite 存储（store 包复用）；静态托管前端 dist
- [ ] Makefile 增加 build-kubemod 目标；docker-compose 示例（对齐架构文档阶段5模板）
- 验收: 独立二进制单独跑起来可用，且代码与联邦模式零复制

### 阶段 F（二期候选，另行评审）
- [ ] Informer 实时化（替代 5s 轮询）
- [ ] 写操作：delete pod / scale deployment，dry-run + 回读验证（verified 模式）
- [ ] WebSocket 日志流 follow / StatefulSets / Ingresses / PV 组补齐

## 六、依赖与风险

| 项 | 说明 |
|----|------|
| 新依赖体积 | client-go 全家桶约 +30~50MB 二进制体积，可接受 |
| Go 版本 | go 1.24 与 client-go v0.3x 兼容 ✅ |
| 网络可达性 | 中央必须能直连各集群 API Server (6443)；跨网段需提前打通 |
| kubevision 许可 | MIT，仅借鉴架构模式/接口设计/侧边栏信息架构，不拷贝代码 |
| 兼容性 | 原 SSH+crictl 巡检保留在 Docker tab 的运行时探测里作降级手段 |
| 双形态一致性 | 核心逻辑全部收敛在 internal/kubernetes，handlers 只是薄壳，避免分叉 |
