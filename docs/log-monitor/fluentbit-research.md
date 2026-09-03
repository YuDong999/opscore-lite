# 开源日志采集处理管道调研报告：Fluent Bit 与 Fluentd

> 面向 **OpsCore 日志监控平台** 的参考调研。重点分析 Fluent Bit 的管线架构、解析规则、路由规则、匹配规则等"规则配置"是如何被设计成属性/字段的，以期为我们的"匹配规则"配置界面设计提供参考。
>
> 资料来源：Fluent Bit 官方文档（docs.fluentbit.io，2026 最新版）、GitHub fluent/fluent-bit、社区实践文章。

---

## 0. 结论速览（TL;DR）

- **Fluent Bit** 是 CNCF 项目，用 C 语言编写、体积小（~650KB）、内存占用低，是 Fluentd 的高性能轻量替代品。二者共享同源设计理念（tag + match 路由 + 插件管线），但 Fluent Bit 更侧重边缘/daemonset 侧，Fluentd 更侧重集中式服务端。
- **规则的本质是「键值对属性」**：几乎所有的解析、过滤、路由规则都被建模成 `Key Value`（经典配置）或 `键: 值`（YAML 配置）属性块（官方称之为 *properties*）。
- **路由的三层设计**（对我们最重要的部分）：
  1. **Tag 路由**（数据块级）：Input 打 `tag`，Output/Filter 用 `match`/`match_regex` 匹配 —— 最传统、最核心的"多条件分发"机制。
  2. **条件路由**（记录级，v3+ 新增）：在 Input 里定义 `routes`，用 `condition.rules`（`field`/`op`/`value`）对每条记录做细分发。
  3. **Label 匹配**（稳定性增强）：用 `alias` 标签做持久化路由，重启/重排配置后仍稳定。
- **匹配规则被设计成一组可组合的原子字段**：`match`（通配符）、`match_regex`（正则）、`condition.op`（and/or）、`condition.rules[]`（field+op+value），这与我们要做的可视化"匹配规则"配置界面天然对应。

---

## 1. Fluent Bit 架构：插件管线

数据在 Fluent Bit 中按固定顺序流经各阶段，每个阶段是一个插件（plugin）。官方管线图：

```
Input → Processors → Parser → Filter → Buffer → Routing → Output
```

| 阶段 | 职责 | 关键点 |
|------|------|--------|
| **Input 输入插件** | 从各种来源收集数据 | tail 文件、systemd journal、syslog、tcp/udp、docker、k8s、HTTP 等 |
| **Processors 处理器** | 直接在单个 input/output 上增删改数据 | 不参与 tag 匹配，属于局部增强 |
| **Parser 解析器** | 把非结构化数据转成结构化字段 | regex/json/ltsv/logfmt/csv 等 |
| **Filter 过滤器** | 交付前改写、增删、过滤数据 | grep、record_modifier、multiline、geoip 等 |
| **Buffer 缓冲** | 统一、持久化的数据暂存 | 内存 / 文件系统 / 内存环形缓冲三种模式 |
| **Routing 路由** | 把数据分发到一个或多个目标 | 依赖 tag 与 match 规则 |
| **Output 输出插件** | 定义数据目的地 | ES、Loki、S3、Kafka、HTTP、文件等 |

> **注**：在上游 3.x 时代管线是 `Input → Parser → Filter → Buffer → Router → Output`；在 v5 中官方明确加入了 **Processors** 阶段并把它置于 Parser 之前，且 Processors 与 Parsers 都不是全局统一的块，而是挂在单个 input/output 上的子键。两者都不使用 tag 匹配，与全局 Filters/Outputs 形成对照。

### 1.1 插件实例（Instance）与属性（Property）

官方文档反复强调一个概念：当一个插件被加载时，会创建一个内部 **instance（实例）**，每个实例拥有**独立的配置**，这些配置键经常被称作 **properties（属性）**。

> 这对我们设计的启发：Fluent Bit 把「一条规则」等同于「一个插件实例 + 一组属性」，天然适合做成 UI 里的一行配置/一张卡片。

### 1.2 全局专有插件键

所有 input/filter/output 插件都支持若干公共键（在 YAML 中是子键）：

- `name`（必填）：加载哪个插件
- `match` / `match_regex`：**仅 filter 和 output 有**，用于 tag 匹配。`match` 大小写敏感，支持 `*` 通配符；`match_regex` 用完整正则；两者同配时 `match_regex` 优先
- `tag`（仅 input 有，除 forward 外必填）：给数据打标签
- `alias`：给实例起别名（配合 Label 路由做稳定分发）
- `log_level`：该插件日志级别

---

## 2. 输入源（Input）支持

输入插件负责采集。常见日志类输入：

| 输入插件 | 说明 | 代表属性 |
|----------|------|----------|
| **tail** | 跟踪文件末尾，最适合日志文件/容器日志 | `path`（可通配，如 `/var/log/containers/*.log`）、`tag`、`parser`、`multiline.parser`、`mem_buf_limit`、`read_from_head`、`db`（增量游标持久化）、`skip_long_lines` |
| **systemd** | 读取 journal | `path`、`tag`、`systemd_filter`（如 `_SYSTEMD_UNIT=nginx.service`） |
| **syslog** | 监听 syslog（RFC3164/5424） | `listen`、`port`、`mode`（unix_tcp/unix_udp/tcp/udp） |
| **tcp / udp** | 裸 TCP/UDP 接收 | `listen`、`port`、`format`、`separator` |
| **docker**（容器） | watch docker 容器日志 | 常与 tail + docker parser 配合 |
| **kubernetes**（k8s） | k8s 采集（通常为 tail 的扩展） | 多与 `kubernetes` filter 配合打 metadata |
| **http** | HTTP Input，接收 webhook/Agent POST | `listen`、`port`、`uri`、`http_server.*`（http2、buffer、max_connections 等） |
| **dummy / cpu / mem** | 调试与指标源 | 生成假数据 |

> 官方强调：Input 打了 `tag`，路由就靠它。例如 k8s 常见做法 `Tag kube.*`，后面 filter/output 都用 `Match kube.*`。

### 2.1 HTTP Input 的共享监听设置

HTTP 类输入共享一套 `http_server` 子配置：`http2`（默认 true）、`buffer_max_size`（4M）、`buffer_chunk_size`（512K）、`max_connections`、`workers`、`ingress_queue_event_limit`、`idle_timeout` 等——这些都属于"背压 + 资源限制"的设计。

---

## 3. 解析器（Parser）与规则

Parser 把**非结构化日志转成结构化字段**。定义在 `parsers`/`parsers_file` 中（YAML 的 `parsers` section 或独立文件）。这是 OpsCore 最需要模仿的部分之一。

### 3.1 支持的格式（`format`）

- **`json`**：把整条记录按 JSON 解析成嵌套字段
- **`regex`**：用 Ruby 正则 + 命名捕获组 `(?<name>...)` 提取字段（最常用、最灵活）
- **`ltsv`**：Labeled Tab-Separated Values（`key:value\tkey:value`）
- **`logfmt`**：k=v 风格（`logfmt_no_bare_keys` 可拒绝无值裸键）
- **`csv`**：CSV（在 v5 文档目录中列出，可用于分隔符解析）

### 3.2 解析器属性（把规则建模成字段）

| 属性 | 说明 |
|------|------|
| `name` | 解析器唯一名 |
| `format` | `json` / `regex` / `ltsv` / `logfmt` / `csv` |
| `regex` | regex 格式必填，Ruby 正则 + 命名捕获组 |
| `time_key` | 时间戳所在字段名 |
| `time_format` | 时间格式（用 `strptime`/`strftime`，支持 `%L` 毫秒/纳秒小数秒）|
| `time_keep` | 解析时间后是否保留原始时间字段 |
| `time_offset` / `time_zone` / `time_strict` / `time_system_timezone` | 时区与偏移处理 |
| `types` | 为字段指定类型：`string` / `integer` / `bool` / `float` / `hex`（如 `types code:integer`）|
| `skip_empty_values` | 是否跳过空值 |
| `decode_field` / `decode_field_as` | 解码器，见下 |

### 3.3 用 Parser 把非结构化日志结构化（经典示例）

```yaml
# YAML 定义的 Parser
parsers:
  - name: cri
    format: regex
    regex: ^(?<time>[^ ]+) (?<stream>stdout|stderr) (?<logtag>[^ ]*) (?<message>.*)$
    time_key: time
    time_format: %Y-%m-%dT%H:%M:%S.%L%z
    time_keep: On
```

```conf
# 经典配置
[PARSER]
    Name        apache
    Format      regex
    Regex       ^(?<host>[^ ]*) [^ ]* (?<user>[^ ]*) \[(?<time>[^\]]*)\] "(?<method>\S+)(?: +(?<path>[^\"]*?))? \S*" (?<code>[^ ]*) (?<size>[^ ]*)(?: "(?<referer>[^\"]*)" "(?<agent>[^\"]*)")?
    Time_Key    time
    Time_Format %d/%b/%Y:%H:%M:%S %z
```

> 关键设计：**正则命名捕获组 `(?<name>...)` 的组名直接成为输出记录的字段名**。这是"非结构化 → 结构化"的原子机制，也是我们做"字段提取规则"UI 时应该直接暴露的概念。

### 3.4 解码器（Decoder / decode_field）

当某个字段里还内含另一种编码（如字段字符串里嵌套 JSON、转义字符）时，用解码器二次解析：

- `decode_field json <field>`：把该字段进一步解析为 JSON 结构并**追加**到原记录
- `decode_field_as json <field>`：解码后**替换**原字段
- 解码类型：`json`、`escaped`、`escaped_utf8`、`mysql_quoted`

> 启发：解码器 = "在字段上再套一层解析"，适合做级联解析配置（先拆外层，再解内层）。

### 3.5 在 Input 上挂 Parser

Parser 通过 Input 的 `parser` 属性被应用。一个 Input 可配多级 parser 解析（先用 parse 拆结构，再对某字段二次解析）：

```yaml
pipeline:
  inputs:
    - name: tail
      path: /var/log/containers/*.log
      tag: kube.*
      parser: cri          # 先把 CRI 行拆出 time/stream/logtag/message
  filters:
    - name: parser
      match: kube.*
      parser: json         # 再把 message 字段按 json 二次解析
      key_name: message
      reserve_data: on
```

---

## 4. 过滤（Filter）

Filter 在交付前改写/过滤数据。常见过滤器（均为全局块，用 `match` 指定作用范围）：

| Filter | 作用 | 关键属性 |
|--------|------|----------|
| **grep** | 按字段值正则 保留/排除 记录 | `regex KEY REGEX`、`exclude KEY REGEX`、`logical_op`（AND/OR/legacy）|
| **record_modifier** | 增/删/白名单字段、加 UUID | `record key value`、`remove_key`、`allowlist_key`、`uuid_key` |
| **parser** | 对指定字段再次解析 | `key_name`、`parser`、`reserve_data`、`preserve_key` |
| **multiline** | 多行拼接（栈堆栈） | `multiline.parser`、`multiline.key_content`、`mode`（parser/partial_message）|
| **kubernetes** | 打 k8s metadata、合并容器日志 | `kube_tag_prefix`、`merge_log`、`keep_log`、`k8s-logging.*` |
| **threshold / geoip** | 阈值告警、IP 转地理位置 | 阈值、DB 路径 |
| **rewrite_tag** | 改 tag 重新路由 | `rule key old new` |

### 4.1 Grep 过滤（最贴近"筛选"配置）

```yaml
pipeline:
  filters:
    - name: grep
      match: '*'
      regex: log aa            # 保留 log 字段匹配 aa 的记录
      logical_op: or
```
```conf
[FILTER]
  Name       grep
  Match      *
  Logical_Op or
  Regex      value something
  Regex      value error
```

- 支持**嵌套字段**：用 Record Accessor `$kubernetes['labels']['app']`
- 可配置多条 `regex`/`exclude`，用 `logical_op`（AND/OR）组合
- 若要排除"字段缺失/非法"的记录，可用 `regex iot_timestamp ^\d{4}-\d{2}-\d{2}` 让缺失键失败被过滤

### 4.2 Record_Modifier（字段增删改）

```yaml
pipeline:
  filters:
    - name: record_modifier
      match: '*'
      record:
        - hostname ${HOSTNAME}   # 追加字段
        - product Awesome_Tool
      remove_key:
        - Swap.used               # 删除字段
      allowlist_key:
        - Mem.total               # 白名单：只保留这些
```

---

## 5. 路由（Router）—— 多条件分发的核心

这是 OpsCore 最需要借鉴的部分。官方明确列出**三种路由机制**，可组合使用。

### 5.1 Tag 路由（数据块级，最传统）

核心两个概念：

- **Tag（标签）**：Input 阶段给数据打的可读标识
- **Match（匹配）**：Output/Filter 上配置的、用于挑选带某 tag 数据的规则

```
数据由 Input 生成时带有 tag → 路由读 Input 的 tag 和 Output 的 match → 
如果数据的 tag 匹配某个 output 的 match，则送到该 output；不匹配任何 match 则被删除。
```

```yaml
pipeline:
  inputs:
    - name: tail
      path: /var/log/app1.log
      tag: app1.*          # 打标签
    - name: tail
      path: /var/log/app2.log
      tag: app2.*
  outputs:
    - name: es
      match: 'app1.*'      # app1 的日志 → Elasticsearch
    - name: loki
      match: 'app2.*'      # app2 的日志 → Loki
    - name: stdout
      match: '*'           # 兜底：全部给自己打印一份
```

这就是"**把不同 tag 的日志路由到不同 output**"即**多条件分发**的最基本实现。`match` 支持 `*` 通配符。

### 5.2 条件路由（记录级，v3+ 新增，按内容细分发）

与 tag 路由按整块数据分发不同，条件路由**逐条记录**根据内容评估条件。它在 Input 配置里加一个 `routes` 块：

```yaml
pipeline:
  inputs:
    - name: input_plugin_name
      tag: input_tag
      routes:
        logs:
          - name: route_name
            condition:
              op: and          # 或 or
              rules:
                - field: myfield
                  op: eq        # 比较操作符
                  value: myvalue
            to:
              outputs:
                - output_alias
          - name: default_route
            condition:
              default: true     # 兜底匹配其余记录
            to:
              outputs:
                - fallback_output
  outputs:
    - name: output_plugin_name
      alias: output_alias       # 供路由引用
```

`routes` 块的参数模型（**这是一个非常清晰的"规则配置"数据模型，强烈建议直接参考**）：

| 参数 | 说明 |
|------|------|
| `condition` | 决定哪些记录匹配该路由的条件块 |
| `condition.default` | 设为 `true` 时匹配所有未匹配其他路由的记录（兜底路由）|
| `condition.op` | 组合多条规则的逻辑符：`and` 或 `or` |
| `condition.rules` | 对每条记录评估的规则数组 |
| `rule.field` | 字段名 |
| `rule.op` | 比较操作符 |
| `rule.value` | 比较值 |
| `name` | 路由唯一名 |
| `per_record_routing` | `true` 时启用逐记录评估（默认 false）|
| `to.outputs` | 匹配记录发往的 output 名/别名数组 |

> 这套 `condition.rules`（field + op + value）+ `condition.op`（and/or）+ `default`（兜底）的模型，几乎可以直接映射到我们的可视化"匹配规则"配置界面：**一个路由 = 一组条件 + 逻辑组合 + 目标输出**。

### 5.3 Label 路由（稳定性增强）

Label-based matching 把标签和插件名存进 chunk 元数据，重启/配置重排后仍能稳定路由。匹配顺序：

1. 先用存储的标签匹配 output 的 `alias`
2. 再匹配自动生成的 `{plugin}.{seq}` 名（如 `stdout.0`）
3. 最后回退到数字 ID

同时做 **plugin type 检查**（防止路由到同 alias 但不同类型的 output）。适合"启用存储积压 + 重启恢复路由"的场景。

### 5.4 如何选择路由方式（官方建议表）

| 场景 | 推荐方式 |
|------|----------|
| 一个输入所有数据到特定输出 | Tag 路由 |
| 按来源/输入类型分发 | Tag 路由 |
| 按内容逐条分发记录 | 条件路由 |
| 按严重级别/字段值拆分日志 | 条件路由 |
| 对数据子集施加不同处理 | 条件路由 |
| 不做内容检查的分发 | Tag 路由 |
| 配置变更后仍稳定路由 | Label 匹配（配合直接路由）|
| 重启 + 存储积压恢复 | Label 匹配（配合直接路由）|

---

## 6. 输出（Output）插件

一个 Output = 一个数据目的地。多种输出可并行。通用键：`name`、`match`/`match_regex`、`log_level`、`storage.total_limit_size`（文件缓冲队列上限）。

各目的地基础配置示例：

```yaml
pipeline:
  outputs:
    # Elasticsearch
    - name: es
      match: '*'
      host: 192.168.2.3
      port: 9200
      index: my_index
      # 常用：logstash_format、logstash_prefix、replace_dots、retry_limit_false
    # Loki
    - name: loki
      match: '*'
      host: 127.0.0.1
      port: 3100
      labels: job=fluentbit     # 指定 Loki 标签
    # Amazon S3
    - name: s3
      match: '*'
      bucket: your-bucket
      region: us-east-1
      store_dir: /tmp/fluent-bit/s3   # 本地暂存
      total_file_size: 50M
      upload_timeout: 10m
    # Kafka
    - name: kafka
      match: '*'
      brokers: 192.168.1.3:9092
      topics: test
    # HTTP
    - name: http
      match: '*'
      host: 192.168.2.3
      port: 80
      uri: /something
      format: json
```

---

## 7. 多行日志拼接（Multiline）

堆栈跟踪会跨多行，需要合并成一条。Fluent Bit 提供多层级的 multiline 能力。

### 7.1 内置多行解析器（自动检测）

| Parser | 处理对象 |
|--------|----------|
| `cri` | CRI-O 容器产出的日志（支持拼接）|
| `docker` | Docker 容器日志（把超 16KB 被拆分的 partial 消息拼回，用 `_p` 字段判断 P/F）|
| `go` | Go panic 栈追踪（`panic:` + goroutine）|
| `java` | Java 异常栈（Exception/Error/Throwable + stack frame）|
| `python` | Python Traceback（`Traceback (most recent call last):`）|
| `ruby` | Ruby 异常回溯 |

### 7.2 自定义多行解析器：状态机（state machine）

多行解析器本质上是一个**基于正则 + 超时的状态机**，把行拼起来。核心属性：

| 属性 | 说明 |
|------|------|
| `name` | 唯一名（最好前缀 `multiline_`）|
| `type` | `regex` / `endswith` / `equal`（eq）|
| `rule` | 匹配规则（见下），仅 regex 用 |
| `flush_timeout` | 超时（ms）后把未终止的多行缓冲刷出（默认 4s）|
| `parser` | 先套一个基础 parser 再匹配正则 |
| `key_content` | 结构化消息中要处理/拼接的字段 |
| `key_group` | 分组字段（如按 docker 的 `stream` 区分 stdout/stderr）|
| `key_pattern` | 用另一字段做匹配、另一字段拼接内容 |
| `match_string` | 用于 endswith/equal 类型 |

**规则（rule）三要素**：`state name` + `regex pattern` + `next state`。第一个 state 必须叫 `start_state`（匹配多行消息第一行），后续 continuation state 自行命名。可多个 continuation 解决复杂场景。

```yaml
multiline_parsers:
  - name: multiline-regex-test
    type: regex
    flush_timeout: 1000
    rules:
      - state: start_state
        regex: '/([a-zA-Z]+ \d+ \d+\:\d+\:\d+)(.*)/'
        next_state: cont
      - state: cont
        regex: '/^\s+at.*/'
        next_state: cont
```

### 7.3 把多行拼到不同语言（k8s 场景实例）

```conf
[MULTILINE_PARSER]
    Name          multiline_java
    Type          regex
    Parser        cri
    Key_Content   log
    Flush_Timeout 5000
    Rule          "start_state"  "/^\d{4}-\d{2}-\d{2}\s+\d{2}:\d{2}:\d{2}\s+\w+\s+.*/"  "cont"
    Rule          "cont"        "/^(\s+at\s+.*|Caused by:.*|\s*\.\.\. \d+ more|[A-Za-z_][A-Za-z0-9_.$]*(Error|Exception):.*)/" "cont"
```

在 input 里启用：

```conf
[INPUT]
    Name                tail
    Path                /var/log/containers/*.log
    Tag                 kube.*
    multiline.parser    multiline_java
```

或用 **multiline filter**（把拆分记录重发回管线头部重新拼接，适合非 chunk 型输入如 Forward）：

```yaml
pipeline:
  filters:
    - name: multiline
      match: '*'
      multiline.key_content: log
      multiline.parser: go,multiline-regex-test   # 支持逗号分隔多格式
```

> ⚠️ 重要坑（官方警示）：因为多行 filter 会把拼接记录**重发到管线头部**，所以**不能有多个 multiline filter 匹配同一 tag**（会死循环，issue #5235）；多格式要用单个 filter 的逗号分隔列表。且 multiline filter 应作为**第一个 filter**，否则前面 filter 会被执行两遍。

---

## 8. 缓冲（Buffer）与背压

Ingest 后、路由前，数据先在缓冲中暂存。官方引擎把输入插件发出的记录打包成 **chunk（块）**，平均约 2MB。

### 8.1 三种缓冲模式

| 模式 | `storage.type` | 行为 |
|------|----------------|------|
| **Memory-only（仅内存）** | `memory` | 数据在内存，路由完成后 flush 出内存。达到 `mem_buf_limit` 后暂停输入插件 |
| **Filesystem（文件系统混合）** | `filesystem` | 通过 `mmap(2)` 在磁盘存一份；内存够则同步在内存，不够则只留磁盘直到有空间 |
| **Memory ring buffer** | `memrb` | 定长环形缓冲，满了**丢弃最老数据**而非暂停，适合容忍丢旧数据、保留最新的场景 |

### 8.2 关键背压/缓冲参数

- 全局（service 段 `storage` 块）：`storage.type`、`storage.path`（文件路径）、`storage.inherit`（允许 input 继承全局默认）、`storage.max_chunks_up`
- Input 级：`mem_buf_limit`、`storage.type`、`storage.pause_on_chunks_overlimit`（on/off）、`rate_gate.max_bytes` / `rate_gate.max_records`（超过速率的暂停采集）
- Output 级：`storage.total_limit_size`（该 output 目标在文件系统的队列上限，满了丢最老）

```yaml
service:
  storage:
    path: /var/fluent-bit/state        # 启用文件缓冲需要全局路径
    max_chunks_up: 128

pipeline:
  inputs:
    - name: cpu
      storage.type: filesystem          # 单个 input 用文件缓冲
  outputs:
    - name: stackdriver
      match: '*'
      storage.total_limit_size: 5M      # 断网时最多缓冲 5M 最新数据
```

> 设计启发：缓冲/背压是"可靠性 + 资源上限"的权衡，在 UI 里应提供"内存上限 / 是否落盘 / 上限超了是暂停还是丢数据"这三类可视化选项。

---

## 9. 与 Fluentd 的对照（简要）

| 维度 | Fluent Bit | Fluentd |
|------|-----------|---------|
| 语言/体积 | C，~650KB，超轻量 | Ruby，重量级，需插件 gem |
| 定位 | 边缘/daemonset/高性能采集 | 集中式服务端聚合 |
| 配置 | `fluent-bit.conf`（经典）/ `fluent-bit.yaml` | `fluentd.conf`（仅类经典语法）|
| 配置单元 | `[INPUT]/[FILTER]/[OUTPUT]/[PARSER]/[MULTILINE_PARSER]`；`@INCLUDE` | `<source>/<filter>/<match>` 标签块；`@type` |
| 匹配 | `match`（通配符）、`match_regex` | `tag` + `match`（`**`/`*`/`{a,b}` 通配）|
| 路由顺序 | 全局块顺序执行 | `<match>` 块顺序匹配，第一个命中执行 |
| 多行 | 多行解析器/多行 filter | `fluent-plugin-concat`、`detect-exceptions` |
| 缓冲 | chunk + 内存/文件/memrb | `buffer_path` + `buffer_type`（memory/file 等）|

Fluentd 的多行拼接常借助插件，例如：

```conf
<filter>
  @type concat
  key log
  stream_identity_key container_id
  multiline_start_regexp /^-e:2:in `\/'/
  multiline_end_regexp /^-e:4:in/
</filter>
```

---

## 10. 对 OpsCore「匹配规则」配置界面的设计建议

基于以上调研，Fluent Bit 把"规则"几乎都建模成**结构化属性/字段**，非常适合做可视化配置。建议 OpsCore 抽象出以下可复用模型：

### 10.1 统一的三层规则模型
1. **作用域（match/tag）**：规则先界定"管哪些数据"——用 `match`（通配）或 `match_regex`（正则）或 `condition`（字段条件）。UI 上就是一个"应用范围"选择器。
2. **动作（Action）**：解析 / 过滤 / 改写字段 / 路由到某输出。UI 上就是插件类型选择。
3. **参数（properties）**：每个动作的键值对参数。UI 上就是表单字段。

### 10.2 推荐的条件规则数据结构（直接参考条件路由）

```jsonc
{
  "routes": [
    {
      "name": "error-to-es",
      "condition": {
        "op": "and",            // and | or
        "default": false,       // 是否为兜底路由
        "rules": [
          { "field": "level", "op": "eq", "value": "ERROR" },
          { "field": "service", "op": "regex", "value": "^pay.*$" }
        ]
      },
      "to": { "outputs": ["es-primary"] }
    }
  ]
}
```

- 比较操作符建议提供：`eq`、`ne`、`regex`、`contains`、`exists`、`missing`、数值比较（`gt`/`gte`/`lt`/`lte`）
- `default` 兜底路由对应 Fluent Bit 的 `condition.default`，保证"未匹配数据有归宿"，避免静默丢弃
- 保留"字段缺失即不匹配"的语义（参考 grep 排除缺失键技巧）

### 10.3 可视化映射建议
- **Tags → 分组/命名空间**：输入侧 tag 类似标签分类，UI 上可作为"来源分组"
- **Parser regex 命名捕获组 → 字段提取规则**：展示一组 `字段名 = 正则片段` 的映射卡片，让用户直观看到"非结构化 → 结构化"
- **Multiline 规则 → 状态机编辑器**：start_state + continuation states 的可视连线，或者降级为"起始正则 + 续行正则 + 超时"三个字段
- **缓冲策略 → 三选一单选 + 上限数字输入**（内存/文件/环形 + 上限 + 超限策略：暂停或丢弃）

### 10.4 需要注意的坑（避免踩雷）
- **数据不匹配任何 match 会被删除** —— 路由规则务必配兜底
- **multiline filter 不能配多个匹配同一 tag**（死循环）
- **multiline filter 应放第一个 filter**（否则重复处理）
- **文件缓冲必须配全局 `storage.path`**，且 input 才继承
- 经典配置与 YAML 只是语法不同，**数据模型一致**，选择其一即可（建议 YAML，便于程序化生成/校验）

---

## 附录：参考链接

- Fluent Bit 数据管线：https://docs.fluentbit.io/manual/concepts/data-pipeline
- 路由（Router）：https://docs.fluentbit.io/manual/data-pipeline/router
- 缓冲（Buffering）：https://docs.fluentbit.io/manual/data-pipeline/buffering
- 解析器配置：https://docs.fluentbit.io/manual/data-pipeline/parsers/configuring-parser
- 多行解析：https://docs.fluentbit.io/manual/data-pipeline/parsers/multiline-parsing
- 过滤列表：https://docs.fluentbit.io/manual/data-pipeline/filters
- Grep 过滤：https://docs.fluentbit.io/manual/data-pipeline/filters/grep
- Record Modifier：https://docs.fluentbit.io/manual/data-pipeline/filters/record-modifier
- 输入插件：https://docs.fluentbit.io/manual/data-pipeline/inputs
- 输出插件：https://docs.fluentbit.io/manual/data-pipeline/outputs
- GitHub：https://github.com/fluent/fluent-bit
