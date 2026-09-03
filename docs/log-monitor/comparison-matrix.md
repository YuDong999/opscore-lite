# OpsCore 日志监控平台 —— 开源项目功能对比属性图

> 本文档横向对比 4 大开源日志/监控项目，提取每个项目的**功能模块、实现机制、前端布局**，作为 OpsCore 日志监控(Loki类) + 资源监控(Grafana/Prometheus类) 模块的设计蓝图。
>
> 配套详报：`loki-research.md`、`fluentbit-research.md`、`kibana-grafana-research.md`（本目录）。

---

## 一、总览对比表

| 维度 | Loki + Promtail | Fluent Bit | ELK/Kibana (ES) | Grafana |
|------|----------------|------------|-----------------|---------|
| **定位** | 日志存储+检索(核心) | 日志采集+传输管道 | 全文检索引擎+可视化 | 可视化+告警聚合 |
| **索引策略** | 只索引**标签(label)**,与正文分离 | 不建索引,只管转发 | **全文倒排索引进每行** | 不存储,查询各数据源 |
| **存储成本** | 低(标签+压缩对象存储) | 无(中转) | 高(全文索引占大头) | 无 |
| **检索能力** | 标签过滤强+行内regex/json | 无检索(输出端查询) | 全文检索最强 | 取决于数据源 |
| **采集能力** | 中(Promtail/Alloy) | **最强**(海量输入插件) | 中(Filebeat/Logstash) | 弱(依赖外部采集) |
| **冷热归档** | 对象存储生命周期 | 输出端路由 | **ILM 四阶段** | 无(靠数据源) |
| **UI** | Grafana Explore | 弱(配置驱动) | **Discover 三块式** | **面板网格** |
| **多租户** | 有(X-Scope-OrgID) | 无(路由分发) | 有(空间/权限) | 有(org) |

---

## 二、按功能模块的属性分解（对比谁强、怎么实现）

### 2.1 采集 (Collection)

| 子能力 | Loki/Promtail | Fluent Bit | ELK/Filebeat | OpsCore 借鉴 |
|--------|--------------|------------|--------------|-------------|
| tail 文件 | ✅ path+标签 | ✅ path通配+游标 | ✅ | ✅(已有 scan) + 游标续读 |
| 容器日志 | ✅ docker | ✅ docker 插件 | ✅ | 已有容器模块, 可联动 |
| k8s | ✅ 强 | ✅ | ✅ | 已有多集群, 联动 |
| syslog | ✅ | ✅ RFC3164/5424 | ✅ | ✅ 需加 |
| systemd journal | ✅ | ✅ systemd_filter | 弱 | ✅ 需加 |
| HTTP/agent | ✅ | ✅ http input | 弱 | ✅ 已有 ingest API |
| **增量续读** | ✅ | ✅ `db`游标 | ✅ | **需补: 防重复采集同一文件** |
| **多行合并** | ✅ | ✅ multiline(强) | ✅ | **需补: stack trace 合并** |

> **结论**: 采集最完整的是 Fluent Bit。OpsCore 应做"采集器"抽象, 复用已有容器/agent 能力, 补齐 syslog/journal/多行/游标。

### 2.2 解析与字段提取 (Parsing)

| 能力 | Promtail(pipeline_stages) | Fluent Bit(parser) | OpsCore 借鉴 |
|------|--------------------------|--------------------|-------------|
| 正则命名捕获 | ✅ regex | ✅ regex `(?<name>)` | ✅ 已有 |
| JSON | ✅ | ✅ | ✅ 已有 |
| logfmt | ✅ | ✅ | ✅ 需加 |
| 多行状态机 | ✅ | ✅ 强 | 需补 |
| 时间格式 | ✅ standardize | ✅ time parser | 需扩 |

### 2.3 匹配规则与路由 (Rules / Routing) —— *对比属性图重点*

| 机制 | Fluent Bit | Logstash | OpsCore 应建 |
|------|-----------|----------|-------------|
| 范围(tag/match) | match 通配 / match_regex | type/patterns | **应用范围: match / match_regex** |
| 字段条件 | **condition.rules(field/op/value)+op(and/or)+default(兜底)** | if 条件语句 | **结构化规则: field+op+value+and/or+default** |
| 操作符 | eq/ne/regex/contains/exists... | ==, =~, in | eq/ne/regex/contains/exists/gt/gte/lt/lte |
| 动作 | 输出路由/过滤/改写 | mutate/grok | 过滤/改写/路由到输出 |
| 兜底 | condition.default | else | **default 兜底, 防静默丢弃** |

> **设计映射（Fluent Bit 三层规则模型）**:
> 1. **作用域** = 哪些数据 (match/match_regex/条件)
> 2. **动作** = 做什么 (过滤/改写字段/路由到某索引或输出)
> 3. **参数** = 动作的键值属性
>
> 完整数据结构见 `fluentbit-research.md §10.2`。**这就是 OpsCore"匹配规则"配置界面的数据模型。**

### 2.4 存储与索引 (Storage / Indexing)

| 能力 | Loki | ES | OpsCore 借鉴 |
|------|------|----|-------------|
| 索引内容 | 标签(低基数) | 全文倒排 | **标签+摘要，正文压缩** |
| 存储单元 | chunk(stream一段) | 分片/段 | 按(源,日期,级别)分文件 |
| 热/温/冷 | 对象存储生命周期 | **ILM hot/warm/cold/delete** | **ILM 式策略** |
| 保留期 | 按流/租户 | 按索引 | 按索引 |

### 2.5 冷热归档 (ILM-style) —— *对比属性图重点*

| 阶段 | ELK ILM | 对应 OpsCore 落地 | 触发条件 |
|------|---------|-----------------|---------|
| **Hot 热** | 高优先读写, rollover | SQLite 索引表(近期) | max size/age 滚动 |
| **Warm 温** | 只读+shrink+forcemerge | 压缩归档文件, 可查 | 热度降低 |
| **Cold 冷** | freeze/慢存储 | 归档 zstd 文件, 按需载入 | 极少访问 |
| **Delete** | 按保留期删 | 定时清理数据/文件 | 到保留期 |

### 2.6 前端布局对比（*重点模仿*）

| 应用 | 布局范式 | OpsCore 采用 |
|------|---------|-------------|
| **Kibana Discover** | 上查询/左字段/中结果直方图+列表/右详情抽屉 | ✅ 日志检索主界面 |
| **Grafana Explore** | 上查询/中时间线柱状图/下日志行流 + Live | ✅(更接近此) |
| **Kibana Logs** | 时间线+实时日志流+级别着色+上下文 | ✅ 实时跟随 |
| **Kibana ILM** | 策略阶段编辑(每阶段条件+动作) | ✅ 冷热归档界面 |
| **Fluent Bit 规则** | 配置驱动(属性表单) | ✅ 规则编辑器表单化 |
| **Grafana Dashboard** | 24列网格面板(type=logs/timeseries/...) | ✅ 监控 tab |

---

## 三、OpsCore 目标功能模块分解（最终架构蓝图）

### 3.1 日志 tab（对标 ELK/EFK + Loki）

```
LogMonitor 日志
├── ① 日志检索(Discover/Explore 式)
│    ├─ 查询构建: 数据源/索引选择 + 类KQL搜索 + 标签/字段过滤 + 多条件(服务/级别/时间/关键词)
│    ├─ 时间线柱状图 ↔ 流线图(可切换) + 框选缩放 + 级别着色
│    ├─ 日志行列表: 时间戳|级别(着色)|标签|message, 字段高亮, Live tail 实时流动
│    └─ 行点击: 详情抽屉(全字段) + 上下文日志 + 字段加/减为过滤
├── ② 索引管理(管理多个"索引"实体)
│    ├─ 索引列表: 名称/数据源/条数/大小/保留期/存储阶段
│    ├─ 索引详情: 字段映射 + ILM 冷热归档配置(hot/warm/cold/delete, 每阶段条件+动作)
│    └─ 新建/编辑/删除索引
├── ③ 匹配规则(对标 Fluent Bit 规则)
│    ├─ 规则列表 + 可视化规则编辑器(作用域 match/条件 rules/动作)
│    └─ 解析器管理(regex/json/logfmt 命名捕获组 → 字段)
├── ④ 数据源接入(保留对接外部能力)
│    ├─ 内置 OpsCore 采集器(文件/syslog/journal/HTTP/容器)
│    ├─ 外部数据源: **Loki** / **Fluent Bit(HTTP)** / ES / 自定义HTTP
│    └─ 解析输出: 写入内置索引
└── ⑤ 采集管理: 源列表(文件/容器/syslog/journal) + 增量游标 + 多行合并
```

### 3.2 监控 tab（对标 Prometheus + Zabbix 的UI, **本期仅归档不实现**）

```
LogMonitor 监控  [文档化, 本期不开发]
├── 指标面板网格(Dashboard, 24列网格): 折线/柱状/仪表/表格/面板
├── 面板类型: timeseries / logs / table / gauge / stat / barchart
├── 变量(template variables): $service / $host 等
├── 指标来源: CPU/内存/磁盘/网络(已有 resources 模块数据) + 日志转指标(logs-to-metrics)
├── 告警: 规则(查询+阈值+周期) → pending/firing → 通知(邮件/Slack/DingTalk/Webhook)
└── 数据源抽象: 统一 query→frame 接口, 面板与数据源解耦
```

---

## 四、结论与优先级

1. **日志 tab 是本次核心**：检索页(Discover/Explore式) + 索引管理(ILM冷热) + 匹配规则 + 数据源接入。
2. **监控 tab 本期只写结构化归档文档**（见 `ARCHITECTURE-monitor.md`），不做代码。
3. 接入外部数据源(Loki/FluentBit HTTP)能力**保留**——通过"数据源"抽象，而非重新发明。

## 五、参考文件索引
- `loki-research.md` — Loki 架构 + Explore UI 详设
- `fluentbit-research.md` — 采集/解析/匹配规则数据模型
- `kibana-grafana-research.md` — Kibana Discover/ILM + Grafana 面板布局
