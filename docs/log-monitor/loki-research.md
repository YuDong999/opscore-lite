# Grafana Loki 与 Promtail 日志监控平台调研报告

> 面向 OpsCore 日志监控平台的参考调研。本报告聚焦「功能是什么 + 怎么实现 + UI 如何布局」，重点服务于日志平台的可模仿性设计。
>
> 调研时间：2026 年 · 基于 Grafana Loki 最新版（v3.x）官方文档与源码

---

## 0. 总览：Loki 是什么

Grafana Loki 是一个**水平可扩展、高可用、多租户**的日志聚合系统，灵感来自 Prometheus（"Prometheus for logs"）。它与传统日志系统（如 ELK/Elasticsearch）最大的差异在于：

- **Elasticsearch**：全文倒排索引（索引日志每行的全部内容）→ 存储贵、检索强。
- **Loki**：只索引日志的**标签（Label）元数据**，日志正文被压缩后存入廉价对象存储（S3/GCS/Azure Blob/文件系统）→ 存储便宜、规模大、全文检索弱但流式过滤快。

**一个完整 Loki 日志栈由 3 部分组成：**
1. **Agent（采集器）**：如 Grafana Alloy（Promtail 的后继）、Promtail、Fluent Bit 等。负责抓取日志 → 打标签 → 通过 HTTP API 推送给 Loki。
2. **Loki 主服务**：负责摄入（ingest）、存储、查询。可按不同部署模式运行。
3. **Grafana**：用于查询和展示日志。也可用 LogCLI 命令行或直接调用 Loki API。

**核心设计哲学：**
> "Loki 不索引日志内容，只索引关于日志的一组标签。一个日志流（log stream）就是一组共享相同标签的日志集合。标签帮助 Loki 在存储中定位日志流，因此优质的标签集合是高效查询的关键。"
> —— 官方 Overview

日志数据被压缩后以 **chunk（块）** 形式存入对象存储；用一个**小索引 + 高度压缩的 chunk** 显著降低运营成本和复杂度。

---

## 1. 整体架构

### 1.1 部署模式（Deployment modes）

| 模式 | 说明 | 适用规模 |
|------|------|---------|
| **Single binary** | 单二进制一体运行，全部组件合进程 | 最小规模（Raspberry Pi 级别） |
| **HA monolithic（高可用单体）** | 单体但多副本，推荐替代旧的 SSD 模式 | 中等规模 |
| **Microservices / Distributed（微服务/分布式）** | 各组件独立进程，原生 Kubernetes 化，可独立水平扩展 | 最大规模（PB/天） |

> 注意：旧的 Simple Scalable Deployment（SSD）模式已废弃，将在 Loki 4.0 移除，HA monolithic 是其推荐替代。

分布式模式各组件副本数示例（Helm chart）：
```
distributor:      3 副本
ingester:         3 副本（默认每 zone 一个，zone-aware）
querier:          3 副本
queryFrontend:    2 副本
queryScheduler:   2 副本
compactor:        1 副本（singleton，必须单实例）
indexGateway:     2 副本
patternIngester:  1 副本
```

### 1.2 核心组件（Components）

| 组件 | 类型 | 职责 |
|------|------|------|
| **Distributor（分发器）** | 无状态 | 接收 agent 推送的写入，校验日志（标签合法性、时间戳范围、日志长度）、限速，然后哈希流并转发给对应 ingester |
| **Ingester（摄入器）** | 有状态 | 在内存中缓冲并构建 chunk，定期刷写（flush）到对象存储；用 WAL（预写日志）保证崩溃后不丢已确认数据 |
| **Query Frontend（查询前端）** | 无状态 | 接收查询请求，**切分（splitting）** 大查询为子查询，排队调度，合并结果 |
| **Query Scheduler（查询调度器）** | 无状态 | 在 frontend 和 querier 之间做公平队列、防止租户间互相 DoS、防止大请求导致 OOM |
| **Querier（查询器）** | 无状态 | 向所有 ingester 查询内存数据，再从对象存储懒加载历史 chunk，去重并聚合 |
| **Index Gateway（索引网关）** | 无状态 | 缓存并服务 TSDB 索引查询，减少 querier 直接从对象存储读索引的开销 |
| **Compactor（压缩器）** | singleton | 压缩/合并索引表、执行日志保留（retention）策略、删除已标记 chunk |
| **Ruler（规则器）** | 无状态 | 周期性评估 LogQL 查询，产生告警/记录规则，对接 Alertmanager |
| **Pattern Ingester（模式摄入器）** | 有状态 | 提取日志模式（pattern）用于压缩与模式分析 |

### 1.3 写入路径（Write path）

```
Agent(Alloy/Promtail) 
   │  HTTP POST /loki/api/v1/push  (带 X-Scope-OrgID 租户头，若启用多租户)
   ▼
Distributor
   │  1. 校验日志（标签、时间戳、长度）
   │  2. 对每个 stream 做一致性哈希，通过 ring 定位目标 ingester
   │  3. 按 replication factor（默认3）转发给多个 ingester 副本
   ▼
Ingester (内存缓冲 + WAL)
   │  4. 为每个 stream 创建 chunk 或追加到已有 chunk（chunk 按 租户+标签集 唯一）
   │  5. 写 WAL 持久化（崩溃可重放）
   │  6. 定期 flush：压缩 chunk → 上传对象存储；索引 → TSDB shipper → 对象存储
   ▼
分布式一致：quorum = floor(replication_factor/2) + 1，如 RF=3 需 2 个 ingester 确认
```

关键点：**chunk 是「租户 + 标签集」维度的唯一数据单元**。ring 是底层分布式哈希表，每个节点注册自己，用于定位写/读归属。

### 1.4 读取路径（Read path）

```
Client(Grafana Explore / LogCLI / API)
   │  HTTP GET  (LogQL 查询)
   ▼
Query Frontend
   │  1. 切分大查询 → 多个子查询
   │  2. 交给 Query Scheduler
   ▼
Querier
   │  3. 先从所有 ingester 拿内存中的实时匹配数据
   │  4. ingester 返回不足/无数据时，从对象存储懒加载 chunk 并执行查询
   │  5. 汇总跨 ingester + 存储的数据，去重，返回子查询结果
   ▼
Query Frontend
   │  6. 等待所有子查询完成
   │  7. 合并为最终结果返回客户端
```

### 1.5 可靠性与高可用（WAL 预写日志）

Ingester 默认把数据缓冲在内存。若崩溃可能有数据丢失，因此引入 **WAL（Write Ahead Log，预写日志）**：
- 写入时先把数据记录到本地文件系统的 WAL，ACK 之后才安全。
- 重启时「重放」WAL 和 checkpoint，恢复内存数据后再注册为就绪。
- 既有内存缓冲的性能/成本优势，又有崩溃后不丢已确认数据的持久性。

关键运维细节：
- ingester 需用 **StatefulSet + 固定持久卷**（重启后挂载同一卷以重放 WAL）。
- 磁盘使用达 90% 时 ingester 会节流（拒绝新写入）以保护 WAL，可通过 `--ingester.wal-disk-full-threshold` 调整。
- 缩容前需调用 `/ingester/shutdown?flush=true` 先刷盘。
- WAL 重放内存上限（`ingester.wal-replay-memory-ceiling`）提供背压，防止大 WAL 把内存打爆。

---

## 2. 标签（Label）机制

### 2.1 为什么标签索引省存储

传统全文索引（ES）为日志**每一行的每个词**建立倒排索引，索引本身庞大且需要昂贵 SSD。而 Loki **只对每个日志流的「标签集」建索引**——标签数量少、基数低，索引体积小到只有 ES 的零头。日志正文只做压缩后线性扫描（brute-force 过滤），不建入库级索引。

类比：**标签相当于「书的目录/分类」，而不是「逐页逐词的索引」**。查询时先用标签（目录）精确定位到少数几个 stream，再只对这几个 stream 的 chunk 做内容过滤，因此读取量小、速度快、成本低。

### 2.2 关键原则：低基数标签

标签是查询性能的生命线。**好的标签 = 低基数（low-cardinality）**，即标签取值数量有限且稳定：

- **应该做成标签**：`env`（prod/staging/dev）、`service`、`namespace`、`level`、`container`、`job` 等。
- **不应该做成标签**：`request_id`、`user_id`、`trace_id` 等每次请求都变化的高基数字段——它们应保留在日志正文中，查询时用 `| json | request_id="..."` 这类管道过滤，而不是当标签。

> 官方建议：使用低基数标签做索引；把高基数数据保留在日志内容里；使用一致的命名约定；丢弃不必要的标签；监控标签基数。可用 `count_over_time({job=~".+"}[1h]) > 10000` 这类查询监控高基数告警。

### 2.3 流（Stream）与系列（Series）

- **Stream（日志流）**：一组共享**完全相同标签集**的日志集合。标签集相同的日志属于同一 stream。**stream 个数 = 标签组合数（基数乘积）**。
- **Series（系列）**：在指标语境下的对应概念。Loki 用 chunk 按 stream 组织日志；一个 chunk 唯一属于一个 (租户, 标签集) 组合。
- **Chunk（块）**：一个 stream 中一段时间的日志，压缩后作为一个对象存储单元。

> 优化目标：**stream 数量要可控**。标签值越分散，stream 越多，索引越大、查询要扫的 chunk 越多。这也是为什么高基数不能做成标签的根因。

---

## 3. 存储引擎

### 3.1 分层：索引 vs 数据（Two-tier storage）

Loki 存储分两大部分：
1. **索引（Index）**：TSDB 格式（默认推荐）或 BoltDB-shipper（旧）。只存 租户→stream→chunk 的映射，体积小。
2. **Chunk 数据（对象存储）**：S3 / GCS / Azure Blob / Alibaba OSS / 文件系统。存压缩后的日志正文，体积大但便宜。

### 3.2 TSDB 索引

- Loki 2.8 引入的 **Single Store TSDB** 是当前推荐索引模式：TSDB 索引文件同样存放在**对象存储**中（与 chunk 同桶），而非本地磁盘。
- 优势：更高效、更快、更可扩展，运维简单（单一存储后端）。
- 运作流程：ingester 把活动索引写到本地 `active_index_directory`，通过 tsdb-shipper 周期上传到对象存储；querier/index-gateway 从对象存储拉取并缓存。

### 3.3 Schema / Period 演进

**schema_config** 定义了 Loki 从对象存储读写的「格式版本」，并允许按时间分段（period）演进。核心概念：

```yaml
schema_config:
  configs:
    - from: 2024-04-01          # 该 schema 生效的起始日期（新装需是过去的日期）
      object_store: s3          # 对象存储后端
      store: tsdb               # 索引类型
      schema: v13               # schema 版本
      index:
        prefix: index_          # 索引表名前缀
        period: 24h             # 索引表周期（天表）
```

关键规则：
- **period：24h**（TSDB/BoltDB-shipper 强制），即**每个租户每天一个索引表**，跨多个天分段存储。
- 演进：可增加新 period 条目（如从某日起把索引类型从 boltdb 升级到 tsdb，或改对象存储桶），但 **schema 变更不可回滚/撤销**——某 schema 写入的数据只能由该 schema 读取。
- 用索引周期配合 retention 可做按天的数据生命周期管理。

### 3.4 热 / 温 / 冷存储分层（Storage tiering）

Loki 本身主要靠 **Compactor retention + 对象存储生命周期策略（lifecycle policy）**来实现分层与降本：

- **热（Hot）**：新写入数据在 ingester 内存 + WAL，查询命中快。
- **温 / 冷（Warm / Cold）**：chunk 落到对象存储后，可通过**对象存储的生命周期策略**（如 S3 的 Standard → Infrequent Access → Glacier / Expire，或 GCS 的 age-based 规则）把旧 chunk 自动迁移到更便宜但更慢的存储类。
- 分层**仅针对 chunk 数据（每租户的 `<tenant_id>/` 前缀）**；索引、集群 seed、delete-request、marker 等对象**绝不能**被生命周期规则删除，否则破坏查询。

> ⚠️ 关键警告：生命周期规则的过期时间必须**长于** retention_period + retention_delete_delay，且必须按租户 chunk 前缀限定范围（不要用空前缀全桶规则），否则会误删 Loki 必须保留的对象导致查询失败/存储损坏。

### 3.5 保留策略（Retention）与 Compaction

保留通过 **Compactor** 实现（`compactor.retention_enabled: true`）。流程：
1. 每个索引表（每天）把多个索引文件**压缩成每个租户每天一个索引文件**。
2. 遍历索引，按租户配置识别要删除的 chunk，从索引中移除引用，并把 chunk 引用写入**marker 文件（标记文件）**。
3. 上传修改后的索引文件。
4. Chunk 不立即删除，由**异步 sweeper** 延迟（`retention_delete_delay`，默认 2h）后删除——留出时间让 Index Gateway 拉到不含已删除 chunk 引用的新索引，避免查询引用到已删 chunk 报错。

保留规则优先级（从上到下取首个匹配）：
1. 多租户级 `retention_stream` 中 priority 最高的匹配规则
2. 全局 `retention_stream` 中 priority 最高的匹配规则
3. 租户级 `retention_period`
4. 全局 `retention_period`
5. 默认 0（永久保留）

可在 `limits_config` 中配置全局 `retention_period`（如 744h=31天）与按流配的 `retention_stream`（selector 用标签匹配，period 至少 24h），也支持 per-tenant 运行时覆盖：

```yaml
limits_config:
  retention_period: 744h
  retention_stream:
  - selector: '{namespace="dev"}'
    priority: 1
    period: 24h
```

---

## 4. 日志查询语言 LogQL

LogQL 受 PromQL 启发，有两种查询类型：**日志查询（返回日志行）** 与 **指标查询（由日志生成指标，logs-to-metrics）**。

### 4.1 流选择器（Stream Selector）

用 `{}` 花括号包裹一组标签匹配器，精确定位日志流：

```
# 精确匹配
{namespace="production", app="api-gateway"}
# 反向匹配
{namespace="production", app!="debug-tool"}
# 正则匹配
{namespace="production", app=~"api-.+"}
# 正则排除
{namespace=~"prod|staging", app!~"test-.+"}
```

运算符：`=`（等于）、`!=`（不等于）、`=~`（正则匹配）、`!~`（正则排除）。

### 4.2 行过滤器（Line Filters）

在流选择器后用 `|` 管道追加字符串/正则过滤：

```
{app="api-gateway"} |= "error"          # 包含字符串
{app="api-gateway"} != "healthcheck"    # 不包含
{app="api-gateway"} |~ "status=[45]\d{2}"  # 正则匹配
{app="api-gateway"} !~ "GET /health"    # 正则排除
```

也支持 `|= "(?i)..."` 大小写不敏感等。多个过滤器按 AND 组合。

### 4.3 解析器（Parsers）

从日志行中**提取字段**（把日志内容变成立即可过滤/展示的键值）。

- **`| json`**：把 JSON 日志的所有顶层字段解析出来。
- **`| logfmt`**：解析 `key=value` 空格分隔格式。
- **`| regexp`**：自命名捕获组，如 `` | regexp `processed in (?P<duration>\d+)ms for user (?P<email>\S+)` ``。
- 还有 `| pattern`、`| docker`、`| unpack` 等。

解析后可对提取字段做**标签过滤表达式**：

```
{app="api-gateway"} | json | level="error"
{app="api-gateway"} | logfmt | addr = ip("192.168.4.0/24")
{app="legacy"} | regexp `(?P<level>INFO|WARN|ERROR)\s+\[(?P<thread>[^\]]+)\]\s+(?P<message>.*)` | level="ERROR"
```

**`| unwrap <field>`**：把日志字段提出来作为数值，配合范围函数做指标计算。

### 4.4 行格式化（Line Format）

`| line_format` 用 Go 模板重排显示内容：

```
{app="api-gateway"} | json | line_format "{{.level}} - {{.message}} [user={{.user_id}}]"
```

`| decolorize` 可剥离 ANSI 颜色码方便显示。`| label_format` 可重命名标签。

### 4.5 指标查询（Logs to Metrics）

对日志流应用范围函数（range functions）与聚合，把日志变成指标（可用于告警、仪表盘、趋势）：

```
# 每秒错误率（按 host 聚合）
sum by (host) (rate({job="mysql"} |= "error" [1m]))

# 一段时间内的日志条数
count_over_time({app="api-gateway"} |= "error" [1h])

# 错误占比
sum(rate({app="x"} | json | level="error" [5m]))
  / sum(rate({app="x"} [5m]))

# 延迟 p99（需 unwrap 数值字段）
quantile_over_time(0.99, {app="x"} | json | __error__="" | unwrap request_time [1m]) by (path)

# 体积/字节
bytes_rate({app="x"} [5m])
sum(bytes_over_time({app="nginx"} [1h])) by (namespace)
```

常用范围函数：`rate`、`count_over_time`、`sum_over_time`、`bytes_over_time`、`quantile_over_time`、`avg_over_time`；聚合用 `sum/avg/max/topk by (...) ( ... )`。

### 4.6 常见模式速查表

| 模式 | 查询示例 | 用途 |
|------|---------|------|
| 错误计数 | `count_over_time({app="x"} |= "error" [1h])` | 错误追踪 |
| 错误率 | `rate({app="x"} |= "error" [5m])` | 告警 |
| 延迟 P99 | `quantile_over_time(0.99, {app="x"} | json \| unwrap latency [5m])` | SLA 监控 |
| 日志体积 | `bytes_rate({app="x"} [5m])` | 容量规划 |
| Top 错误 | `topk(10, sum by (error) (count_over_time({app="x"} \| json [1h])))` | 错误分析 |

### 4.7 组合流水线示例

```
{job="security"}
    |~ "Invalid user.*"                 # 行过滤
    | regexp "(?P<user>\S+ {1,2}){8}"   # 正则解析
    | line_format "USER = {{.user}}"    # 重排
```

LogQL 因此能在一条查询里完成：定位流 → 过滤行 → 结构化解析 → 过滤字段 → 重格式 → 生成指标 的完整流水线。

---

## 5. 采集器配置：Promtail

> **Promtail 已于 2025-02 进入官方 LTS（长期支持）维护模式，预计 2026-03 EOL**，其继任者为 **Grafana Alloy**（基于 OpenTelemetry 的统一遥测采集器，同时采集 logs/metrics/traces）。但 Promtail 配置思想（scrape_config / relabel / pipeline_stages）被 Alloy 沿用，理解它即可迁移。

Promtail 是 Loki 专用日志采集 agent，**作为 DaemonSet 运行在每个节点**，监听日志文件并把日志推送给 Loki，同时附加标签。

### 5.1 顶层配置结构

```yaml
server:
  http_listen_port: 9080

positions:
  filename: /tmp/positions.yaml   # 记录采集进度（断点续传），防止重启重复推送

clients:
  - url: http://loki:3100/loki/api/v1/push
    tenant_id: default            # 多租户时指定租户 ID

scrape_configs:                   # 与 Prometheus scrape_configs 相同
  - job_name: kubernetes-pods
    kubernetes_sd_configs:        # 服务发现（K8s pod）
      - role: pod
    relabel_configs: [...]        # 重新打标签
    pipeline_stages: [...]        # 日志处理管道
```

### 5.2 服务发现与清理（relabel_configs）

`relabel_configs` 用与 Prometheus 相同的机制，动态重写每个 target 的标签集：
- `source_labels`（来源标签，可用 `__meta_kubernetes_*` 等发现元数据）
- `target_label`（目标标签）
- `action`: `replace` / `keep` / `drop` / `labeldrop` / `labelkeep`
- `regex`：正则匹配，支持捕获组
- `replacement`：替换字符串（如 `${1}`）
- 临时标签用 `__tmp` 前缀
- 以 `__` 开头的标签在 relabel 完成后会被移除

### 5.3 示例：K8s 环境打标签（多租户）

```yaml
scrape_configs:
  - job_name: kubernetes-pods
    kubernetes_sd_configs:
      - role: pod
    relabel_configs:
      # 只保留 production/staging 命名空间
      - source_labels: [__meta_kubernetes_namespace]
        regex: '(production|staging)'
        action: keep
      # 把命名空间元数据复刻为 namespace 标签
      - source_labels: [__meta_kubernetes_namespace]
        target_label: namespace
      # Pod 名 → pod 标签
      - source_labels: [__meta_kubernetes_pod_name]
        target_label: pod
      # 容器名 → container 标签
      - source_labels: [__meta_kubernetes_pod_container_name]
        target_label: container
    pipeline_stages:
      - docker: {}                  # 解析 Docker 日志格式
      - json:                       # 解析 JSON 日志字段
          expressions:
            level: level
            msg: message
      - labels:                     # 把提取的字段设成标签
          level:
```

### 5.4 管道阶段（pipeline_stages）

比 relabel 更强大：**Transform 日志内容并产出标签**。
- **`json` / `regexp` / `logfmt` / `docker`**：解析日志正文。
- **`labels`**：把提取的字段提升为标签（**会增大 stream 基数，谨慎**）。
- **`tenant`**：**设置租户 ID**——从 `label` / `source`（提取字段）/ `value` 取值，是实现多租户的关键。
- **`match` + 条件管道**：按条件对部分日志执行后续阶段。
- **`template`**：用 Go 模板变换提取值（如把 DEBUG/INFO/WARN 归一化）。
- **`metric`**：从日志生成 Prometheus 计数器（`inc` 或基于提取值的 `add`）。
- **`replace` / `drop` / `timestamp` / `static_labels`** 等。

### 5.5 多租户（Multi-tenancy）机制

Loki 多租户通过 **HTTP 头 `X-Scope-OrgID`** 实现：agent 在 push 请求中携带租户 ID，Loki 据此将数据/查询完全隔离。配置方式：
- 在 `clients[].tenant_id` 固定某个租户。
- 或用 `pipeline_stages` 中的 `tenant` 阶段，从日志字段/标签动态决定租户（同逻辑在 Alloy 里用 `loki.write` 的 `tenant_attribute`）。
- Promtail 本身是单租户 agent；如需一个 agent 多租户分发，常需按租户跑多个 promtail 进程或做 relabel 拆分。

---

## 6. 前端 / UI

### 6.1 查询入口与查询模式

查询方式按灵活度从低到高：
1. **Logs Drilldown（日志钻取，Grafana 的 App）**：面向日志的引导式探索，减少直接写 LogQL。
2. **Grafana Explore（探索）**：标准日志探索面板，Builder 模式 + Code 模式。
3. **Dashboards（仪表盘）**：把日志查询固化为面板（时间序列 / 表 / 日志面板）。
4. **LogCLI / API**：命令行与接口。

### 6.2 Explore 的 Loki 查询编辑器两种模式

| 模式 | 特点 |
|------|------|
| **Builder mode（构建器）** | 可视化拖拽：先选标签，再加操作（`+ Operations`），系统会给出操作提示（Hints），适合新手 |
| **Code mode（代码）** | 文本编辑器，带**自动补全（autocompletion）、语法高亮、查询校验（validation）**，还内置 **Label Browser（标签浏览器）** 帮助构造 `{...}` 查询 |

操作可用拖拽调整顺序（Order of operations）。

### 6.3 Explore 日志面板布局（重要，需重点模仿）

结合官方文档与 OCI/Lucidworks 的第三方集成描述，Grafana Explore 的 Loki 日志视图布局如下：

```
┌─────────────────────────────────────────────────────────────────┐
│ 顶部导航：Grafana 主菜单（左侧竖排图标）+ 顶栏 │
│  Explore 标题 │ 数据源切换(选 Loki) │ 时间范围按钮(时钟图标) │ 
│  Run query 按钮 │ ⋮ 更多选项  │
└─────────────────────────────────────────────────────────────────┘
┌─ 查询构建区 ─────────────────────────────────────────────────────┐
│ [Log browser ▼]  [查询文本/代码编辑区]  [Add query] [Run] 查询类型 │
│  · Log browser 展开后：(两种方式都能选标签构建查询)             │
│    方式A：下拉选择标签名 → 展开值列表 → 选一个/多个值             │
│    方式B：Label Browser → 步骤1点标签按钮(每标签一button)          │
│                       → 步骤2选值(轻点即选/多选/再点取消)          │
│                       → 步骤3 Show logs 按钮                       │
│   · 选中/反选时实时构建查询串显示在编辑区                             │
└─────────────────────────────────────────────────────────────────┘
┌─ 时间线柱状图（Timeline / Log Volume Histogram）─────────────────┐
│  显示所选时间范围内匹配日志的条数分布（按时间桶的柱状图）          │
│  柱状图按日志级别/流着色（error红、warn黄、info蓝...）             │
│  · 拖动选择柱状图区域 = 时间缩放（zoom in）                        │
│  · 放大镜按钮 = 缩小（zoom out）                                   │
│  · 顶部时间范围按钮控制整体范围                                     │
└─────────────────────────────────────────────────────────────────┘
┌─ 日志结果列表（Log lines / message list）─────────────────────────┐
│  每条日志行：  [时间戳] [标签chips] [级别图标] [消息内容]          │
│  · 消息按级别着色（info/warn/error 不同颜色背景或标识）            │
│  · 点击行左侧时间戳旁箭头 → 展开该行的 Log Details（内联/侧栏）    │
│  · 行内高亮/过滤：点击标签或提取的字段值 → 加为过滤条件             │
│  · 流式日志：Live tail（实时滚动订阅新日志，可选 live 切换）        │
│  · 常见可选：showLabels / showTime / enableLogDetails /          │
│             dedupStrategy(去重) / sortOrder(排序)                 │
└─────────────────────────────────────────────────────────────────┘
```

**关键 UI 细节（供 OpsCore 模仿）：**
- **左侧查询 vs 右侧结果**：Grafana Explore 顶部是查询区，下方是「时间线柱状图 + 日志行列表」两块，非左右分栏（左右分栏是 Log Details 的 sidebar 模式）。查询参照「上查询、下结果」的布局；顶部日志浏览器/查询框，中间是作为时间轴的柱状图，底部是日志行流。
- **标签过滤器（Label filter）**：通过 Label Browser / 标签下拉选标签名与多个取值，把它作为 `{...}` 流选择器的一部分。
- **时间线柱状图（Log Volume Histogram）**：横轴时间、纵轴日志条数，按级别/流着色，支持框选缩放——直观给出日志量的时间分布，帮助快速定位日志爆发点。
- **级别着色与高亮**：error/warn/info 用不同颜色，在时间柱状图与日志行里一致着色；`detected_level` 会被 injected 到日志行（Grafana 自动检测 debug/info/warn/error/fatal/critical/trace/unknown）。
- **字段分组**：Loki 数据源下，日志字段按类型分组展示——**Indexed Labels（索引标签）**、**Parsed fields（解析出的字段）**、**Structured Metadata**。
- **Log Details（日志详情）**：
  - 两种模式：**Inline（内联，默认，日志行下方展开）** 与 **Sidebar（侧栏，右侧滑出）**。
  - 展示该行的标签、解析字段，并提供 **过滤（加/减为查询条件）** 操作。
- **Log context（日志上下文）**：显示命中搜索词在原始日志流中的**前后上下文行**，有助于查 trace 或事件脉络。
- **Data links / Correlations**：把日志消息任何一部分变成跳转链接（到其他面板、dashboard 或外部资源）。
- **Query history**：查询历史（默认保存 2 周），可加星收藏（Starred）、搜索、复制、删除、加注释、定时重跑。
- **Add query**：可加多个查询，结果合并显示在**同一个时间线 + 消息列表**里。
- **仪表盘日志面板**：`type: "logs"` 的 panel，支持 showLabels/showTime/enableLogDetails/dedupStrategy/sortOrder 等 option。

### 6.4 Logs Drilldown（简化探索 App）补充

Grafana 的 Logs Drilldown App 提供更引导式的界面，包含 **Labels 页**和 **Fields 页**：
- **Labels 页**：顶部导航 + 通用区域 → Label 过滤器（搜索/选择标签名）、Grid/Rows 切换、每标签的 **Select / Include 按钮**（进入该标签值分布的可视化，看各值体积）、菜单（跳转 Explore）。
- **Fields 页**：Field 过滤器、**Extract fields from logs** 开关（对日志应用 logfmt/json 解析，让检测到的字段和索引标签、结构化元数据一起出现）、**Volume/Names 切换**（显示字段体积图或仅名称）、Include / Add to filter 按钮。
- 顶部有 **Filter fields**（已选过滤条件，带 × 可移除）。

---

## 7. 关键概念汇总

| 概念 | 定义 | 意义/注意点 |
|------|------|------------|
| **Stream（日志流）** | 共享完全相同标签集的一组日志 | stream 数量 = 标签基数乘积；太多 stream 拖慢查询、撑大索引 |
| **Series（系列）** | 指标语境对应概念 | 与 Prometheus 概念对齐 |
| **Chunk（块）** | 一个 stream 一段时间日志的压缩存储单元 | 按 (租户, 标签集) 唯一；对象存储的读写基本单位 |
| **Label（标签）** | 键值元数据，Loki 唯一索引的内容 | 用低基数标签，高基数放正文 |
| **Retention（保留）** | 日志按策略删除的生命周期 | 由 Compactor 执行，retention_enabled + period；支持全局/按流/按租户 |
| **WAL** | 预写日志，保证已确认写入不丢 | ingester 用它做崩溃恢复 |
| **Replication factor** | 每个 stream 写入的 ingester 副本数 | 默认 3，quorum 需多数确认 |
| **Tenant（租户）** | 数据/请求隔离单位，靠 `X-Scope-OrgID` 头 | 多租户隔离数据与查询 |
| **Index（索引）** | 租户→stream→chunk 的映射 | TSDB 为推荐实现，与 chunk 同放对象存储 |
| **Compactor** | 压缩索引 + 应用保留策略的 singleton | 负责 retention，marker 文件跟踪待删 chunk |

---

## 8. 对 OpsCore 的可借鉴要点（速览）

1. **标签优先的索引设计**：不要全文倒排索引进每行日志；只索引低基数标签，正文压缩入库，能极大降本并支撑高吞吐。
2. **多租户隔离**：用请求头（如 `X-Scope-OrgID`）隔离租户数据与查询，天然支持 SaaS 多客户。
3. **流/块/索引分离**：把「热内存（ingester+WAL）→ 对象存储 chunk + 索引」分层，冷热分层可用对象存储生命周期策略实现，注意别误删索引类对象。
4. **采集管道**：抓日志 → 打标签（relabel）→ 解析（json/logfmt/regexp）→ 设租户，借鉴 Promtail pipeline_stages 的「解析-提炼标签-变换-位租户-生成指标」五段式。
5. **查询语言设计**：一条流水线完成「选流→过滤行→解析→过滤字段→格式化→聚合指标」，LogQL 的 `{}` + `|` 风格简洁且与 PromQL 心智一致。
6. **UI 布局范式**（重点模仿）：
   - 顶部 = 查询构建（标签浏览器 + 代码编辑器双模式）
   - 中部 = **日志量时间柱状图**（按级别/流着色，可框选缩放时间）
   - 底部 = **日志行列表**（时间戳 + 标签 chips + 级别着色 + 可点标签过滤）
   - 点击日志行展开详情（内联/侧栏），日志字段按 索引标签/解析字段/结构化元数据 分组
   - 支持**流式 live tail**、日志上下文、查询历史、多查询合并
   - 字段/标签的「加/减为过滤条件」交互是日志平台的体验核心

---

## 参考来源

- 官方架构与组件文档：grafana.com/docs/loki/latest/get-started/architecture、.../components
- 官方存储文档：.../configure/storage、.../operations/storage/schema、.../operations/storage/tsdb、.../operations/storage/wal、.../operations/storage/retention
- 官方查询文档：.../query、.../query/query_examples
- 官方 Explore 日志文档：grafana.com/docs/grafana/latest/visualizations/explore/logs-integration、.../simplified-exploration/logs/labels-and-fields
- Promtail 配置：grafana.com/docs/loki/latest/send-data/promtail/configuration
- 第三方解析文章（DevOpsCube、OneUptime、Lucidworks、OCI/Oracle Explore 集成说明）辅助 UI 与配置细节
