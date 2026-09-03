# 模块注册中心重构方案（保留，待多分支合并后基于最新 main 执行）

> 状态：**已规划，暂缓实施**。原因：本方案会重构公共骨架 `main.go` / `internal/registry/` / `internal/module/plugin.go`，而这些文件当前正被多个并行的功能分支（cicd / dbmanager / logmonitor 等）基于旧结构修改。若现在由一个分支改动，会导致所有其他分支合并时大面积冲突。
>
> **执行时机**：所有功能分支合并回 main 后，从当时最新的 main 另开一个独立的 refactor 分支，一次改完并尽快合回 main，使其成为后续开发的新基线。
>
> 关联：`docs/log-monitor/ARCHITECTURE-logmonitor.md`、`docs/log-monitor/comparison-matrix.md`。

---

## 一、现状痛点（为何要改）

加一个新模块，需要同时手工改动四处公共文件：

| 位置 | 现状 | 痛点 |
|------|------|------|
| `main.go` `registerCoreModules()` | 一张硬编码清单 `[]modCfg`，core/cicd/插件模块的 Manifest+路由全写死 | 巨型清单，多 agent 必撞 |
| `main.go` `main()` | dbmanager/logmonitor 走独立 `reg.Register(module)`，要先各自建 store | "模块有哪些路由"的事实被拆散在清单+散落调用两处 |
| `internal/module/plugin.go` | `activeMap["dbmanager"]=true`、`activeMap["logmonitor"]=true` 硬编码默认活跃 | 加模块要再手写一行 |
| `web/src/App.tsx` | 手写 `MODULE_MAP` + icon，与后端 manifest 的 ID 必须一一对应 | 清单是动态的，映射是手工的，漏一个模块就白屏/缺失 |

**历史事故**：`.gitignore` 的裸规则 `src/` 曾误伤 `web/src/` 下所有新文件，导致 `LogMonitorModule.tsx` 及 6 个 dbmanager 组件漏提交、前端构建失败（已在 `31e4f1a` 修复，`src/`→`/src/`）。——公开骨架越薄、约定越强，此类"手工不同步"错误越少。

---

## 二、目标架构：模块自描述，注册 = 收集

### 核心思想
把"一个模块 = 一个自包含单元"落实，模块**自己声明**自己的 ID / Manifest / 路由 / 是否默认激活，而不是中心大佬手工点名。

### 1. 后端：统一的模块接口

```go
// internal/registry/module.go
package registry

type Component interface {
    ID() string
    Manifest() Manifest
    Routes() []Route
    DefaultActive() bool   // 是否默认激活
}
```

每个模块（dbmanager / logmonitor / cicd / 各 core 子模块）实现该接口。例如 logmonitor：

```go
type LogMonitor struct { store *...; svc *... }
func (m *LogMonitor) ID() string { return "logmonitor" }
func (m *LogMonitor) Manifest() Manifest { ... }
func (m *LogMonitor) Routes() []Route { /* 自己的路由 */ }
func (m *LogMonitor) DefaultActive() bool { return true }
```

### 2. main.go 只做收集+注册

```go
// 加模块 = 往这个切片加一项；不再散落 Register 调用
components := []registry.Component{
    registry.NewCore(...),   // resources/services/network/... 各自封装
    dbmanager.New(dataDir),
    cicd.New(dataDir),
    logmonitor.New(dataDir, store, svc),
    // ...
}
for _, c := range components {
    reg.Register(&registry.Module{Manifest: c.Manifest(), Routes: c.Routes()})
}
```

### 3. plugin.go 默认激活自动推导

```go
func InitPluginStore(c central.CentralStore) {
    // ...读取持久化 states...
    activeMap = states
    for _, comp := range allComponents {
        if comp.DefaultActive() {
            activeMap[comp.ID()] = true  // 从模块自述推导，删掉逐行硬编码
        }
    }
}
```

### 4. 前端：manifest 与 MODULE_MAP 一致性校验

- 后端 `/api/manifest` 每个模块带上 ID（已有）。
- 前端启动时遍历 manifest 返回的每个 ID，若 `MODULE_MAP` 未命中则 `console.error` 高亮告警。
- 目标：把"引用不存在的模块 → 白屏"变成"立即显式报错"。
- 远期可选：前端也能动态注册（`window.__moduleRegistry.register(id, Component)`），彻底去掉手写 MODULE_MAP。

---

## 三、好处

1. 加模块 = 新建业务目录 + 实现接口 + 前端登记自己那行；**除非撞同目录，多 agent 基本零冲突**。
2. 消灭 `registerCoreModules` 巨型清单、散落 `reg.Register`、plugin 硬编码激活 三处公共碰撞点。
3. 上一轮"漏文件/引用了不存在模块"从静默失败变为显式构建/启动错误。

---

## 四、回归风险与验证

- 改动面：`main.go` 重构 + `internal/registry` 扩展 + 现有各模块改造成接口实现 + `plugin.go` + 前端校验。
- 必须回归验证：
  - `go build ./...` 全量编译通过；
  - `/api/manifest` 返回模块与之前完全一致（id/name/group/icon 不漂移）；
  - `/api/logmonitor/*`、`/api/cicd/*`、`/api/dbmanager/*`、`/api/core/*` 抽样接口可用；
  - 前端 `vite build` 通过，侧栏渲染的模块与 manifest 一致。
- 建议改造前为 manifest 的完整性写一个对照测试（期望模块集合 vs 返回集合）。

---

## 五、范围裁定

- 本次**不做**本方案（避免与并行分支冲突）。
- 待所有功能分支合并回 main 后，基于最新 main 单开 refactor 分支执行，一次合并。
- logmonitor 分支当前继续按"现状方式"加自己的注册（registerCoreModules 一行 + reg.Register + App.tsx 一行 + plugin.go 一行），这类增量冲突最小，git 可自动合并。
