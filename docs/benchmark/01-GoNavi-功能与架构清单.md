# GoNavi 功能与架构清单（基准分析）

> 分析方式：本地源码（`ref/GoNavi`）+ web-server 实机运行截图（127.0.0.1:8091，连接 192.168.207.10 MySQL）。
> 运行方式：`frontend npm build` → `go build .` → `GONAVI_WEB_PASSWORD=xxx ./gonavi.exe web-server -addr 127.0.0.1:8091`。

## 1. 整体布局（实测截图）

```
┌─────────────────────────── 顶部工具栏 ───────────────────────────┐
│ GoNavi | 新建查询 | 新建连接 | 管理连接分组 | 批量处理 | SQL工具 │
│         | 驱动管理 | 更多(导出/导入/设置/主题/语言)      [电源]  │
├──────────┬──────────────────────────────────────────┬───────────┤
│ 左侧对象树│  标签页区（TABLE 标签 + SQL 标签 并存）      │ 右侧 AI   │
│ (可折叠)  │  每个标签绑定 [连接·库] 上下文             │ 对话面板  │
│ Host面包屑│ ┌──────────────────────────────────────┐ │ (可收起)  │
│ 筛选tab:  │ │ 编辑器(Monaco) / 数据网格 / 元数据    │ │ 参考过程  │
│ 全部|表|  │ ├──────────────────────────────────────┤ │ 生成的SQL │
│ 视图|序列│ │ 结果区: 数据表|执行摘要|图表|执行计划│ │ Ask/生成  │
│ 函数...   │ │        |导出结果|收起   (多tab切换)  │ │          │
│ 树: 连接→ │ └──────────────────────────────────────┘ │           │
│ 库→分组→表│  底部: 视图切换/图表/分页(首尾页/每页行数) │           │
└──────────┴──────────────────────────────────────────┴───────────┘
```

关键交互（截图实测）：
- 树：`连接(在线绿点) → 库 → [已存查询|表(N)|视图|函数和存储过程|触发器|事件] → 对象(表名+行数徽标)`
- 表节点 hover 卡片：类型/连接/Host/数据库/对象/行数/表大小/创建时间/修改时间
- 表节点右键菜单（分组）：`查看数据`、`置顶表`、`设计表·字段/索引/外键(Ctrl+O)`、`在新标签打开(Ctrl+Enter)`、`新建查询`、`元信息`、`查看 DDL·CREATE TABLE`、`在 ER 图中查看`、`复制[表名/结构DDL/整表/全表为INSERT]`、`维护[重命名F2/备份·SQL Dump/刷新统计信息]`
- 新建连接向导：3 步进度（选类型→配参数→测试保存）；36 种数据源分 9 类（关系型/国产/NoSQL/向量/时序/消息队列/配置中心/其他），带搜索(Ctrl+K)与置顶
- 连接表单：连接串粘贴解析↔反向生成、库显示范围（精确+通配包含/排除）、主从模式、生产连接保护、SSH/SSL 分区（基本/网络与安全/外观/高级）
- 查询编辑器：Monaco 高亮、连接/库/最大行数选择器、右键树表自动填 `SELECT * FROM`、执行摘要（语句/类型/返回行/影响行/耗时/成功徽章）

## 2. 前端组件地图（frontend/src/components，按功能域）

| 功能域 | 组件 | 说明 |
|---|---|---|
| 布局 | Sidebar, TabManager, WorkbenchTabContent, TitleBarPrimaryActions, TitleBarQuickActions | 对象树 + 多标签工作台 + 标题栏 |
| 连接 | ConnectionModal(+Mongo/Redis 分区), ConnectionEnvironmentSelect, ConnectionHealthModal, DriverManagerModal, ConnectionPackagePasswordModal | 连接 CRUD/健康检查/驱动管理/连接包 |
| 对象树 | (Sidebar 内) antd Tree + 对象筛选(全部/表/视图/序列/函数/存储包/事件) + V2TableContextMenu | 懒加载树 + 右键菜单 |
| 查询 | QueryEditor, QueryEditorToolbar(+事务工具条), QueryEditorResultsPanel, MonacoEditor, SnippetSettingsModal | Monaco + 多语句执行摘要 + 片段 |
| 数据网格 | DataGrid*, ~25 个文件 | 核心：DataGridCore(虚拟滚动 dataGridVirtualScroll)、列宽自动、列信息弹层、列快速查找(DataGridColumnQuickFind)、页内查找(PageFind)、分页条、记录视图、单元格右键、剪贴板(复制/粘贴/INSERT导出)、临时值格式化、行级事务日志 |
| 表设计 | TableDesigner, TableDesignerSqlPreview, DataGridV2DdlWorkspace | 可视化改表 + 变更 SQL 预览 |
| 元数据 | TableOverview, TriggerViewer, DefinitionViewer, DataGridV2MetadataViews | 表概览/触发器/定义查看 |
| ER/血缘 | DataGridErDiagram(+model), lineage | ER 关系图、字段血缘 |
| 导入导出 | DataExportDialog, DataImportWorkbench, TableExportWorkbench, SQLExportOptionsDialog, ExportProgress*, ImportJobHistoryPanel, ImportPreviewModal, DatabaseImportExecutionPanel, MySQLGTIDImportModePrompt | 导出(CSV/JSON/XLSX/SQL Dump/INSERT)、导入(CSV/Excel/SQL 文件+GTID)、任务历史 |
| 数据同步 | DataSyncModal, DataSyncWorkbench, data-sync/ | 跨库迁移（我们已对标实现） |
| SQL 文件 | SQLFileExecutionWorkbench, 外部 SQL 文件树节点 | .sql 文件执行与目录管理 |
| 专项 | RedisViewer(+Monitor/CommandEditor), MessageQueueWorkbench(+Publish/Consume), NacosViewer, JVMDiagnostic*, mqtt/ | Redis/MQ/配置中心/JVM 诊断 |
| AI | AIChatPanel, FloatingAIChatWindow, AISettingsModal, ai/ | 侧栏/浮窗 AI 助手 |
| 其他 | FindInDatabaseModal(库内搜索), CloudBackup*, audit/, LogPanel, JVM 系列 | 全局搜索/云备份/审计/日志 |

## 3. 后端结构（Go，Wails App + web-server 双形态）

```
internal/
├── app/          App 核心方法（前端 bindings 一对一）: 连接/查询/导入导出/同步/诊断...
├── db/           33 种数据库驱动（12 内置 + 21 driver-agent 子进程协议）
├── connection/   连接配置模型/DSN 解析/变更集
├── webserver/    web-server 模式: 会话认证(密码+TOTP)/method invoker/事件hub/文件传输
├── ssh/          SSH 隧道/主机密钥信任
├── sqlaudit/     SQL 审计
└── ai*           AI 服务
```

前端调后端两条通道（同一套 App 方法）：
1. Wails 桌面：生成 wailsjs bindings
2. web-server：HTTP POST method invoker + SSE 事件流（`internal/webserver/runtime_bridge.go`）

## 4. 值得 opscore-lite 吸收的点（按价值排序）

1. **左侧懒加载对象树**（连接→库→分组→表，行数徽标、在线状态点、hover 元数据卡、右键菜单）——布局根基
2. **标签页工作台**：TABLE 标签（点表即开数据浏览）与 SQL 标签并存，各自绑定连接·库上下文
3. **表数据浏览即点即用**：右键表 → 查看数据，带类型列头/筛选/分页
4. **查询执行摘要**：多语句逐条（语句/类型/返回行/影响行/耗时/成败）
5. **右键可视化操作组**：查看 DDL/复制为 INSERT/重命名/备份 Dump
6. 连接向导 3 步式 + 连接串解析/生成 + 通配库显示范围
7. 列头筛选/排序控件（DataGridColumnTitle）
