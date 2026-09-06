# 双向映射矩阵 —— 前端交互 ↔ 后端 API

## A. 行映射: 后端能力 → 前端入口

| # | 后端端点 | 前端入口 | 状态 |
|---|---------|---------|------|
| 1 | GET query | D1 查询按钮/Live/分页 | ✅ |
| 2 | GET stats | D1 直方图 / D2 统计 | ✅ |
| 3 | **GET stats/terms** | **无前端入口** | 🕳️ **未暴露** |
| 4 | GET raw | D1 详情抽屉 | ✅ |
| 5 | sources 列表 | D3 源列表 | ✅ |
| 6 | sources/save | D3 新增弹层 | ✅ |
| 7 | sources/delete | D3 删除按钮 | ✅ |
| 8 | scan | 顶栏 / 扫描弹层 | ✅ |
| 9 | delete | **无前端入口** | 🕳️ **未暴露**(批量删日志) |
| 10 | indexes 列表 | D4 索引列表 | ✅ |
| 11 | indexes/save | D4 编辑保存 | ✅ |
| 12 | indexes/get | 编辑态 | ✅ |
| 13 | indexes/delete | D4 删除 | ✅ |
| 14 | indexes/stats | D4 文档/字节 | ✅ |
| 15 | ilm/run | 顶栏按钮 | ✅ |
| 16 | discover/containers | D5 容器列 | ✅ |
| 17 | discover/clusters | D5 集群下拉 | ✅ |
| 18 | ingest | D5 采集入库 | ✅ |
| 19 | discover/pods | D5 Pod 级联 | ✅ (K8S-REFACTOR, 已在实现) |

## B. 列映射: 前端入口 → 后端支撑

| # | 前端交互 | 后端支撑 | 状态 |
|---|---------|---------|------|
| 1 | 字段树 level/source 多选 | query ?level= ERROR,WARN | ✅ |
| 2 | 搜索框 message:xxx | query ?keyword= | ✅ |
| 3 | 已应用 chips | query 多参数 | ✅ |
| 4 | 详情抽屉字段过滤 | query 多参数 | ✅ 但仅 indexId/service 两字段 |
| 5 | 建议下拉 FIELD_HINTS | **无后端, 硬编码 4 条** | 🪝 **静态, 不随数据变** |
| 6 | 字段树 source 子项 | **硬编码 k8s/container/app/file** | 🪝 **静态** |
| 7 | 索引保留/文档数/字节 | indexes/stats | ✅ |
| 8 | ILM 阶段编辑 | indexes/save (ilm JSON) | ✅ |

## 契约 (数据模型连接前后端)

- LogEntry: id/timestamp/service/source/indexId/level/message + raw JSON
- LogSource: name/type/path/service/enabled
- LogIndex: id/name/source/service/deleteAfter/ilm{hot,warm,cold,delete}
- LogQueryResult: total/items/histogram/bucketMs
- LogStatsResult: 级别分布/来源/服务 top / 时间直方图
- TermsResult: field + [{value,count}]

## 🕳️ / 🪝 汇总

| 类型 | 项 | 严重度 | 修复成本 |
|------|----|--------|---------|
| 🕳️ 未暴露 | stats/terms (字段聚合后端现成) | 高 — 功能已做无人用 | 低 (前端加个入口) |
| 🕳️ 未暴露 | delete (批量删日志) | 中 — 需确认语义 | 低 |
| 🪝 悬空 | FIELD_HINTS 静态硬编码 | 中 — 建议应反映真实数据 | 低 (terms 拉真实值) |
| 🪝 悬空 | source 子项静态 | 中 — 与真实数据脱节 | 低 (terms ?field=source) |
| 🪝 悬空 | message 字段点击只是提示 | 低 | 中 |
| 🕳️ 未暴露 | 单资源导出 (流水线/索引/源) | **用户刚提的需求** | 中 — 需新增导出端点 |