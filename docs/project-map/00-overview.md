# OpsCore 日志监控 —— 总体架构树 (Overview)

## 体系定位

OpScore 的日志监控是**自研轻量日志平台**，对标 Loki 的存储策略 + Kibana 的 Discover 交互 + Grafana 的聚合展示。技术栈: Go 后端(plugin 架构) + React/Vite 前端(TypeScript)。

## 顶层树 (5 大分支)

```
opscore-lite
├── 1. Frontend Web (React/Vite) ─ doctree 02-frontend
│   └── modules/LogMonitorModule.tsx (5 tabs × 交互点 32 个已测绘)
├── 2. Backend logmonitor plugin (Go) ─ doctree 01-backend
│   ├── handlers.go (路由注册 20+ 端点)
│   ├── store.go (Starlight/LevelDB 存储 + 查询)
│   ├── service.go (检索/聚合业务)
│   ├── collect.go (采集器: tail/syslog/journal/es/loki)
│   ├── alerter.go (告警评估 goroutine)
│   ├── archiver.go (ILM 清理/归档)
│   └── model.go (数据模型)
├── 3. 参考项目交互层 (Kibana/Grafana/Loki) ─ doctree 02-reference
├── 4. 双向映射矩阵 ─ 04-mapping.md
└── 5. 缺口清单与建议 ─ 05-insights.md
```

## 子树索引

| 编号 | 文档 | 内容 |
|------|------|------|
| 02-frontend | `02-frontend.md` | Discover/统计/源/索引/采集 5 个功能簇 |
| 02-reference | `02-reference.md` | 参考项目的 UI 交互点清单 |
| 04-mapping | `04-mapping.md` | 前端交互点 ↔ 后端 API 矩阵 |
| 05-insights | `05-insights.md` | 缺口清单 (按可借鉴度排序) |

## 覆盖范围声明

本图覆盖 `web/src/modules/LogMonitorModule.tsx` + `internal/logmonitor/*` 全部源码路径。参考项目仅测绘**交互层**（前端 UI 行为），后端实现属于 `🚧 边界外`（不测 ELK/Loki 内部存储细节，已有 `docs/log-monitor/*-research.md` 涵盖）。