# 项目测绘台账 — dbx 深层体检（聚焦数据库管理链路）

> 方法: project-cartographer skill。范围: dbx 数据库管理核心链路（侧栏树/数据网格/查询编辑器/数据迁移）。
> 状态: 本图为**聚焦图**，非全量——dbx 前端 12.6 万行，专项链路（Redis/MQ/Nacos/JVM/向量）未入账（🚧 留档）。

## 台账

| 节点 | 状态 | 说明 |
|---|---|---|
| DBX-00 总体架构树 | ✅ 已测绘 | → dbx-architecture.md |
| DBX-01 后端 API 面（tauri invoke + http 双通道） | ✅ 已测绘 | api.ts 997行 门面 + http.ts executeQuery 等 |
| DBX-02 侧栏树（ConnectionTree/TreeItem/sidebar lib 60 文件） | ✅ 已测绘 | 懒加载/虚拟滚动/拖拽排序/多选/行内重命名/右键分级菜单 |
| DBX-03 数据网格（dataGrid lib **87 模块** + DataGrid.vue） | ✅ 已测绘 | → feature-matrix.md 逐项对照 |
| DBX-04 查询编辑器（CodeMirror6 + QueryEditor*） | ✅ 已测绘 | 补全/片段/历史/多语句摘要/事务工具条 |
| DBX-05 数据迁移（DataSyncWorkbench/transfer） | ✅ 已测绘 | 与我们 sync 包对齐 |
| DBX-06 AI 面板 / 专项浏览器(Redis/MQ/Nacos/JVM) / 向量 / Dolt | ⛔ 死胡同(不移植) | 用户明确不需要 |
| DBX-07 ER 图 / 字段血缘 / Schema Diff / 执行计划可视化 | 🚧 待测绘 | P3 缺口清单已列 feature-matrix |
| OPS-00 我们项目对照侧 | ✅ 已测绘 | → feature-matrix.md 右列 |
