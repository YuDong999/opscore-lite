# 模块分层：公共基座 + 可选重模块（2026-09-20 定稿）

原则一句话：**不是所有模块都抽，只抽"重依赖外部系统的可选能力"；主机管理核心留在基座。**

判据不是代码量，而是产品/部署语义：这个模块是不是**每台主机都需要的**？是不是**重依赖一个外部系统**（数据库引擎 / K8s·Docker daemon / 日志采集管道 / 构建机）？两个都成立 → 抽成目录模块，未来可独立部署或按需裁剪；都不成立 → 留在基座，不强行目录化。

这与后端 manifest 的 `Group` 字段对齐（`internal/module/manifest.go`）：`containers`/`dbmanager` 已注册为 `plugin`，`resources/services/network/diagnostics/tasks` 为 `core`。`cicd` 后端目前是 `core`，前端按可选模块对待（后端是否降组待定）。

## 公共基座（10 个单文件模块，不抽）

`App.tsx` 外壳 + L1(`components/ui/`) + L2(`components/common/`、`lib/`) + 以下模块：

| 模块 | 文件 | 说明 |
|---|---|---|
| 系统资源 | `ResourcesModule.tsx` (517行) | CPU/内存/磁盘/网络 |
| 服务发现 | `ServicesModule.tsx` (421行) | systemd 服务管理 |
| 防火墙和网络 | `NetworkModule.tsx` (372行) + `FirewallModule.tsx` (376行) + `TopologyPanel.tsx` (367行) | 同一领域三件套 |
| 系统诊断 | `DiagnosticsModule.tsx` (233行) | 网络诊断/登录审计/系统更新 |
| 任务与存储 | `TasksModule.tsx` (1175行) | cron/挂载/LVM/SMART |
| 应用中心 | `AppsSection.tsx` (512行) | |
| 插件中心 | `PluginsModule.tsx` (204行) | |
| 设置 | `SettingsModule.tsx` (227行) | |

这些是装完就该有的主机管理能力，单文件放基座里是对的——抽出去只会增加目录层级，不产生独立价值。

## 目录模块（5 个，独立领地）

| 模块 | 目录 | 独立构建 | 外部依赖 |
|---|---|---|---|
| CI/CD | `cicd/` | ✅ `node scripts/build-module.mjs cicd` | 构建机、Git 仓库、Webhook 源 |
| 容器管理 | `containers/`（docker + k8s 子目录） | ✅ `... build-module.mjs containers` | Docker daemon、K8s apiserver |
| 日志监控 | `logmonitor/`（含 logmonitor-kibana.css） | ✅ `... build-module.mjs logmonitor` | 日志采集管道 |
| 数据库管理 | `dbmanager/`（22 文件 + DbIcons） | ✅ `... build-module.mjs dbmanager`（4.7s，2026-09-20 验证） | MySQL/PG 等数据库引擎 |
| 主机批管 | `ansible/` | —（准基座：虽走 SSH 外部通道，但属于主机管理核心语义，主理人未列入必抽清单） | SSH 主机组 |

四个必抽模块的独立构建均已验证通过。单模块构建产物在 `dist/modules/<name>/module.html`，走 API 服务器代理（不直连 kubelet）。

## 硬性边界（`scripts/check_module_boundary.py` 强制）

1. **基座不 import 目录模块**。唯一合法引用点是 `App.tsx` 的 `lazy(() => import('./modules/<name>'))` 注册（2026-09-20 grep 复核：零穿透）。
2. **目录模块之间互不 import**。需要共享就提升到 L2。
3. **模块的 css 跟组件一起迁**（logmonitor-kibana.css 先例）。
4. 删掉任一目录模块，`npm run build` 必须仍绿——这是"独立模块"的最终判据。

## 灰色地带备忘

- **ansible**：已目录化但语义上偏基座。不回迁（目录化无害），也不计入"四大必抽"。
- **k8s** 是 `containers/` 的子域（`containers/k8s/`），不是独立模块——manifest 里也只注册了 `containers` 一个 ID。
- Network/Services/Containers 曾是聚合壳（组合多面板），边界审计中豁免；Network/Firewall/Topology 拆分属后续项，不阻塞本分层。
