# OpsCore 日志监控模块 —— 架构规划与设计

> 定位：日志监控 = **日志**（对标 ELK/EFK + Grafana Loki）+ **监控**（对标 Prometheus + Zabbix，本期仅文档化）。
> 本文档为**规划蓝图**，指导实现；配套调研见本目录 `comparison-matrix.md` 及三份 research。

---

## 一、目标与边界

- **日志 tab**：索引管理、多条件检索、Kibana式日志浏览（时间线柱状/流线图 + 实时日志流 + 级别着色 + 字段高亮 + 详情抽屉 + 上下文）、匹配规则、冷热归档、数据源接入（含 Loki / Fluent Bit HTTP）。
- **监控 tab**：指标面板网格 + 告警。**本期只做结构文档**（见 `ARCHITECTURE-monitor.md`），不实现代码。
- **数据源接入**：保留对接外部采集/存储（Loki、Fluent Bit、ES）能力。

---

## 二、总体架构（分层）

```
┌──────────────────────────────────────────────────────────────┐
│                    前端 (React)  LogMonitorModule            │
│  日志检索 │ 索引管理 │ 匹配规则 │ 数据源 │ [监控(预留)]      │
└──────────────┬─────────────────────────────────────────────────┘
               │ REST /api/logmonitor/*  (+可扩展 SSE 实时推送)
┌──────────────▼─────────────────────────────────────────────────┐
│              后端 (Go) internal/logmonitor                     │
│  Handler 层 → Service 层 → Store/Indexer 层                    │
│   · 检索引擎(query 多条件)   · 统计聚合(histogram)             │
│   · 采集器采集(文件/syslog/journal/HTTP)                       │
│   · 数据源接入适配(Loki/FluentBit/ES)                          │
└──────────────┬─────────────────────────────────────────────────┘
               │
┌──────────────▼─────────────────────────────────────────────────┐
│              存储层（分层/冷热）                                │
│  [Hot]  SQLite 元数据索引表(log_meta)  — 近期, 快速检索        │
│  [Warm/Cold] 按(源,日期,级别)分文件 zstd 压缩归档 — 可查         │
│  [外部] 可选对接 Loki / ES 只读查询                             │
└─────────────────────────────────────────────────────────────────┘
```

---

## 三、核心数据模型

### 3.1 索引 (Index) 实体 —— 对标 Kibana Data View + ILM

```jsonc
{
  "id": "idx_order_api",
  "name": "order-api 日志",
  "source": "file",                // 数据来源: file/syslog/journal/http/loki/fluentbit/es
  "sourceConfig": {                // 采集器参数
    "path": "/var/log/order-api/*.log",
    "params": {}
  },
  "fields": [                      // 字段映射(对标 Kibana Data View)
    { "name": "message",    "type": "text",   "indexed": true },
    { "name": "level",      "type": "keyword","indexed": true },
    { "name": "service",    "type": "keyword","indexed": true },
    { "name": "@timestamp", "type": "date",   "indexed": true }
  ],
  "retention": {
    "enabled": true,
    "hotDays": 7,                  // 热：SQLite 索引在内存/ssd
    "warmDays": 30,                // 温：压缩归档可查
    "coldDays": 90,                // 冷：深归档按需载入
    "deleteAfter": 90              // 到期删除
  },
  "ilmPolicy": {                   // 对标 ES ILM 四阶段
    "hot":  { "rolloverSize": "1GB", "rolloverAge": "1d", "priority": 100 },
    "warm": { "readonly": true, "shrink": 1, "priority": 50 },
    "cold": { "freeze": true,  "priority": 0 },
    "delete": { "deleteAfter": "90d" }
  }
}
```

### 3.2 匹配规则 (Rule) —— 对标 Fluent Bit 三层模型

见 `fluentbit-research.md §10.2`。统一数据模型：

```jsonc
{
  "id": "rule_error_to_hot",
  "name": "ERROR 归热库",
  "scope": {                       // 作用域
    "match": "order-api.*",        // 或 match_regex
    "condition": {
      "op": "and",                 // and | or
      "default": false,            // 兜底路由
      "rules": [
        { "field": "level",  "op": "eq",    "value": "ERROR" },
        { "field": "service","op": "regex", "value": "^order.*$" }
      ]
    }
  },
  "actions": [                     // 动作
    { "type": "route",   "params": { "index": "idx_order_hot" } },
    { "type": "drop_if", "params": { ... } }   // 过滤
  ]
}
```

### 3.3 日志条目 (已实现的基础模型)

`log_meta` 表 + 归档文件（见当前 `internal/logmonitor/store.go`）。

| 字段 | 类型 | 索引 | 说明 |
|------|------|------|------|
| id | int | PK | |
| ts | int ms | 是 | 时间戳 |
| level | text | 是 | ERROR/WARN/INFO/DEBUG/FATAL |
| service | text | 是 | 服务名 |
| source | text | 是 | file/syslog/journal/http/loki... |
| file_path | text | - | 归档/来源文件 |
| offset | int | - | 文件偏移 |
| size | int | - | 字节 |
| summary | text | 否(摘要) | 前200字符 |
| raw | blob/text | 否 | 完整原文(冷区) |

复合索引：`(service, level, ts)`、`(service, ts)`、`(ts)`。

---

## 四、API 设计（/api/logmonitor/*）

| 方法 | 路径 | 说明 | 状态 |
|------|------|------|------|
| GET | `/query` | 多条件检索(service/level/source/keyword/startTs/endTs/page) | ✅ 已实现 |
| GET | `/stats` | 统计(级别/服务分布 + histogram) | ✅ 已实现 |
| POST | `/ingest` | 写入(line/lines) | ✅ 已实现 |
| GET/POST | `/sources` `/sources/save` `/sources/delete` | 日志源管理 | ✅ 已实现 |
| POST | `/scan` | 文件扫描入库 | ✅ 已实现 |
| GET | `/raw?id=` | 单条原文 | ✅ 已实现 |
| POST | `/delete` | 批量删除 | ✅ 已实现 |
| GET | `/indexes` | 索引列表 | 🚧 规划 |
| POST | `/indexes/save` | 新建/编辑索引 + ILM 策略 | 🚧 规划 |
| GET | `/indexes/:id` | 索引详情(字段+ILM) | 🚧 规划 |
| POST | `/indexes/:id/ilm` | 应用冷热归档策略 | 🚧 规划 |
| GET | `/rules` | 匹配规则列表 | 🚧 规划 |
| POST | `/rules/save` | 规则 CRUD | 🚧 规划 |
| GET | `/datasources` | 数据源列表 | 🚧 规划 |
| POST | `/datasources/test` | 数据源连通测试 | 🚧 规划 |
| GET | `/stream` (SSE) | 实时日志流(live tail) | 🚧 规划 |

---

## 五、前端布局规划（照 Kibana/Grafana 范式）

见 `kibana-grafana-research.md §C`。详细：

### 5.1 日志检索页（主界面）
```
上: 查询构建区
    [数据源/索引选择 ▾] [搜索框(类KQL)] [级别 ▾] [服务] [时间 ▾] [查询] [Live跟随]
    标签/字段过滤 chips(可 ×)
中: 时间线图 —— 支持 [柱状图|流线图] 切换, 框选缩放, 级别着色
下: 日志行列表 —— 时间戳|级别(色)|标签|message
    点击行 → 右侧详情抽屉(全部字段 + 过滤/复制)
    字段值高亮命中词
    底部: 分页
```
- **Level 着色**：ERROR 红 / WARN 橙 / INFO 蓝 / DEBUG 灰 / FATAL 深红。
- **行点击**：展开详情抽屉（对标 Kibana），显示该行全部字段，字段值可"加/减为过滤条件"。
- **上下文**：查看命中日志前后的上下文行。

### 5.2 索引管理页
- 列表：名称 | 数据源 | 条数 | 大小 | 保留期 | 存储阶段(hot/warm/cold)
- 详情：字段映射表 + ILM 策略编辑（hot/warm/cold/delete 四阶段，每阶段条件+保留天数）
- 新建/编辑：索引配置表单

### 5.3 匹配规则页
- 规则列表
- 可视化规则编辑器：作用域(match/条件) + 动作 + 参数 表单式编辑
- 解析器管理：正则命名捕获组 → 字段映射

### 5.4 数据源页
- 内置采集器(文件/syslog/journal/http/容器)
- 外部数据源：Loki / Fluent Bit(HTTP) / ES / 自定义 HTTP，逐个测试连通

---

## 六、冷热归档实现路径（对标 ILM）

| 阶段 | 存储位置 | 实现方式 | 触发 |
|------|---------|---------|------|
| Hot | SQLite `log_meta` | 实时写入 + 复合索引 | 始终 |
| Warm | `logs/归档/<idx>/<date>.log.zst` | 后台任务把旧 hot 行转归档并保留指针 | ts 超 hotDays 且规则命中 |
| Cold | 深压缩归档 + 删除索引行 | 按需从文件载入查询 | ts 超 warmDays |
| Delete | - | 删除归档文件+记录 | ts 超 deleteAfter |

> 简化落地：不强制分文件，可先"按(索引,日期)分文件 + SQLite 索引带 index_id 字段"，查询时先查索引定位再到对应归档文件读。

---

## 七、数据源接入抽象

```go
type Datasource interface {
    ID() string
    Type() string              // loki / fluentbit / es / internal
    Query(req QueryRequest) (*QueryResult, error)  // 统一检索
    Stream(ctx) (chan LogEntry, error)             // 实时流
    Test() error                                    // 连通测试
}
```

- **内部采集器**：文件/syslog/journal/http → 打标签 → 解析 → 索引。
- **Loki 接入**：调用 Loki HTTP API 查询（LogQL 或标签过滤），映射为统一 `LogEntry`。
- **Fluent Bit 接入**：Fluent Bit HTTP Output 打到 OpsCore `/api/logmonitor/ingest`（已实现）。

---

## 八、里程碑

- **M1（已完成）**：基础检索 + 统计 + 源 + 扫描 + 原文 + 删除。
- **M2（进行中）**：索引管理实体 + ILM 冷热归档策略 + 字段映射。
- **M3**：匹配规则编辑器 + 解析器管理。
- **M4**：数据源抽象 + Loki/FluentBit 接入 + 实时 SSE 流。
- **M5（文档化）**：监控 tab（见 `ARCHITECTURE-monitor.md`）。
