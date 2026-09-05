# DBX-00 总体架构树（聚焦数据库管理链路）

```
dbx (Vue3 + Tailwind4 + reka-ui + Tauri)
├── 前端 src/
│   ├── components/ 44 域 (sidebar/grid/editor/transfer/objects/diff/diagram/...)
│   ├── stores/ 14 个 Pinia (connection/query/history/settings/transferTask/...)
│   ├── lib/
│   │   ├── backend/ api.ts(997行门面) + tauri.ts(invoke) + http.ts(REST, executeQuery:1075)
│   │   ├── dataGrid/ ★87 模块 — 表格功能全部单元化
│   │   ├── sidebar/ 60 模块 — 树功能全部单元化
│   │   ├── connection/ 25+ 模块 — 连接生命周期/URL解析/健康/传输
│   │   └── editor|diagram|diff|export|import/ ...
│   └── types/database.ts (TreeNode 40+ 类型)
└── 后端 crates/ (Rust)
    ├── dbx-core (连接池/驱动抽象/元数据)
    ├── dbx-web (HTTP API = Docker/Web 形态)
    └── agents/ (Go JDBC 网关 → 90+ 数据源)
```

## 数据库管理三大链路（C 型功能流摘要）

### C-1 打开表数据
`TreeItem 双击 → sidebarDataOpenCoordinator → DataTab 挂载 → DataGrid.vue
→ lib/dataGrid/* (87模块: 分页/排序/筛选/虚拟滚动 canvasDataGridRenderer)
→ backend http.ts executeQuery(:1075) → dbx-core`

### C-2 查询执行
`QueryEditor (CodeMirror6) → QueryEditorToolbar → http.ts executeQuery
→ 结果: 多语句摘要 + 图表(chartData) + 执行计划(explain) + 导出`

### C-3 数据迁移
`transferTaskStore → DataSyncWorkbench(向导: 源/目标/表映射/模式) → dbx-core 批量管道 → transferProgress 进度流`

## 我们 (opscore-lite) 对应结构

```
opscore-lite (React18 + 手写CSS + Tailwind4试点 + Go)
├── 前端 web/src/components/DatabaseManager/ 12 组件 (≈6千行)
│   └── api.ts → /api/dbmanager/* (15 路由)
└── 后端 internal/dbmanager/
    ├── handlers.go (connections/query/metadata/data/export/sync/drivers/slow-sql/table-status/explain/queries)
    ├── sync/ (类型映射+DDL+全量/增量 水位)
    └── gonavi/ (33 种数据库驱动: 12 原生 + 21 agent)
```
