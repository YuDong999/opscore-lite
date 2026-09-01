# dbx 功能与架构清单（基准分析）

> 分析方式：本地源码（`ref/dbx`，与 VM /data/dbx 同源）+ 官方截图（dl.dbxio.com，已存 `shots/dbx-*.png`）。
> 定位：Rust + Tauri，20MB 单体，90+ 数据库，桌面端/Docker/Web/CLI/MCP 五形态。**前端为 Vue3 + reka-ui + Tailwind + CodeMirror6 + vue-echarts**（apps/desktop/src）。

## 1. 官方截图要点（dbx-light.png / dbx-grid.png / dbx-er.png）

- 顶部工具栏：`新建连接 | 新建查询 | 数据传输(跨库迁移) | 驱动管理(N) | 更多`
- 左侧树：`连接(类型图标+状态点) → 库 → schema → 分组[表(N)|视图|存储过程|函数|序列|用户与权限] → 对象`
  - 顶部小工具条（上传/同步/折叠）、对象搜索框、历史按钮、筛选
- 中部多标签：对象标签（`testdb objects@testdb`）与查询标签（`PostgreSQL_vh1q@test_geometry`）并存；标签带类型图标与关闭
- 查询标签顶部：连接选择器 ▾ | 库 ▾ | 选择模式 ▾ | 执行/格式化/历史/片段/打开文件/下载
- 结果区 tabs：`数据表 | 执行N | 执行摘要 | 图表 | 执行计划 | 导出结果 | 收起结果`
  - 执行摘要列：语句/类型/返回行/影响行/耗时 + 成功徽章
- 右侧 AI 面板：对话流 + 参考过程折叠 + 生成 SQL 代码块（可执行）+ Ask / 生成 SQL 输入框 + 连接·库上下文选择
- 底部状态栏：视图切换/图表/引用/跳列 + 分页

## 2. 功能清单（README「功能特性」逐项）

### 连接与驱动
- 90+ 数据库：原生驱动（MySQL/PG/SQLite/Redis/Mongo/DuckDB/ClickHouse/MSSQL/Oracle/ES/Meilisearch/MariaDB/TiDB/OceanBase/openGauss/GaussDB/KWDB/Kingbase/Vastbase/GoldenDB/Doris/StarRocks/DM/TDengine/CockroachDB/InfluxDB/etcd/ZooKeeper/Nacos/Consul...）+ Agent 扩展（H2/Snowflake/Trino/Hive/DB2/Informix/Neo4j/Cassandra/BigQuery/SAP HANA/Teradata/Vertica/Firebird...）+ 自定义 JDBC
- 连接导入：从 DBeaver / Navicat 导入连接配置

### 查询编辑器
CodeMirror6 高亮、元数据感知自动补全、Cmd+Enter、选中执行、SQL 格式化、诊断提示、9 主题、查询历史、SQL 片段、标签页恢复、SQL 文件执行

### AI SQL 助手
自然语言→SQL、解释/优化/修复、内置安全检查执行、Claude/OpenAI/本地/OpenAI 兼容端点

### 数据表格
虚拟滚动、行内编辑+保存前 SQL 预览、WHERE/ORDER BY 控件、DataGrip 风格过滤器、LIKE/NOT LIKE 右键过滤、排序、全文搜索、分页、列宽调整/自动列宽、行号、斑马纹、完整单元格详情；导出/复制 CSV/JSON/Markdown/XLSX/INSERT

### Schema 工具
结构浏览（库/Schema/表/字段/索引/外键/触发器 + 侧栏搜索/置顶）、对象浏览（过程/函数/视图+源码编辑）、表结构编辑器（可审查变更）、ER 关系图、Schema 对比（跨连接）、执行计划可视化、字段血缘、数据库搜索

### 数据操作
导入 CSV/Excel、数据迁移（跨库）、整库导出、数据对比（同步审查）、SQL 文件执行、文件预览（Parquet/CSV/JSON 基于 DuckDB）

### 专项浏览器
Redis（模式搜索/批量键/命令执行器/TTL/全类型）、MongoDB（文档 CRUD/分页/Atlas 直连）

### 生态
MCP Server（连接 allowlist + read_only/safe_write/high_risk_write 三档权限）、CLI（connections list / query）、Web 版（DBX_WEB_PASSWORD 登录）

## 3. 工程结构

```
apps/desktop/          Vue3 前端(Tauri)
  src/components/      44 个功能域目录:
    admin auth backup chart codeSnapshot common config connection consul
    diagram diff docs document dolt editor etcd explain export generate
    grid hbase icons import kv layout lineage meilisearch mq mqtt nacos
    objects quick-open redis search settings sidebar sql-file ssh structure
    tabs transfer ui vector zookeeper
  src/stores/          Pinia 状态
  src/lib/             API 适配层（Tauri invoke / Web HTTP 双通道）
crates/
  dbx-core             核心逻辑(连接池/驱动抽象/元数据)
  dbx-web              Web 服务器(HTTP API + 静态托管, 对应 Docker 部署)
  dbx-cli / dbx-mcp / dbx-sqlite-worker
agents/                Go driver agents(JDBC 网关, 扩展 90+ 库)
packages/              npm 分发: cli / mcp-server(+各平台二进制)
```

## 4. 值得 opscore-lite 吸收的点

1. **结果区多 tab**（数据表/执行摘要/图表/执行计划）——同一结果区承载多种视图
2. **数据传输（跨库迁移）做成一级入口**（我们已做，但在 dbx 是顶部按钮级功能）
3. **连接导入**（DBeaver/Navicat XML 解析）——低成本高感知
4. **quick-open 命令面板**（Ctrl+K 搜表/连接/动作，跳转 + 定位）
5. **虚拟滚动表格 + 列宽自动 + 完整单元格详情弹层**
6. **schema 搜索**（FindInDatabase 同款：大型 schema 快速找对象）
7. 库/schema 层级显式分离（PG 有 schema 层，树里 `库 → schema → 对象`）
