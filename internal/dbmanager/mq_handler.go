// MQ 管理端点: 能力清单 + 发送消息。
//
// 为什么要有 mqViews/mqPublishFields 这张表: 面板要按引擎显隐(哪些动词能用、发送时能带哪些字段),
// 而这些事实目前只存在于驱动内部。写在这里是第三份, 但它是唯一"面板会读的那一份" ——
// 所以把它当契约看: 每条都对着驱动的语法抄, 并在 mq_handler_test.go 里用驱动实际接受的
// JSON 键名逐条钉住, 漂移会让测试先红。
//
// 发送消息走的是"前端只发表达意图, 协议文本后端拼"这条既有口径(与 apply-edit/apply-ddl/apply-alter 一致),
// 不再要求用户手敲 {"publish":"orders","value":"..."}。
package dbmanager

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

// mqCol 是列的展示偏好: 排在前面 + 中文名。这是**偏好不是白名单** —— 面板会把没列在这里的
// 列照样追加显示。驱动的列名直接来自结构体字段/响应字段(还按字母序排, 于是 arguments 这种
// 一坨会跑到第一列), 写死白名单的话驱动加一列我们就少看一眼, 那种丢数据的方式最难查。
type mqCol struct {
	Key   string `json:"key"`
	Label string `json:"label"`
}

// mqView 是面板里的一个浏览页签: 列表动词 + 可选的详情/取数动词。
type mqView struct {
	Key     string  `json:"key"`
	Label   string  `json:"label"`
	List    string  `json:"list"`
	Detail  string  `json:"detail,omitempty"` // %s = 选中的名字
	Peek    string  `json:"peek,omitempty"`   // %s = 选中的名字
	Publish bool    `json:"publishable"`      // 这个页签上的东西能不能往里发
	Note    string  `json:"note,omitempty"`
	Cols    []mqCol `json:"cols,omitempty"`
}

// mqField 是发送消息表单里的一个可选项。
type mqField struct {
	Name       string `json:"name"`
	Label      string `json:"label"`
	Kind       string `json:"kind"` // text | int | bool
	Hint       string `json:"hint,omitempty"`
	Min        *int   `json:"min,omitempty"`
	Max        *int   `json:"max,omitempty"`
	DefaultVal any    `json:"default,omitempty"`
}

type mqCapability struct {
	Engine string    `json:"engine"`
	Views  []mqView  `json:"views"`
	Fields []mqField `json:"fields"`
	Note   string    `json:"note,omitempty"`
}

var (
	mqInt0, mqInt2 = 0, 2
	mqInt9         = 9
)

// mc 把 (key, 中文名) 成对写成列偏好。
func mc(pairs ...string) []mqCol {
	out := make([]mqCol, 0, len(pairs)/2)
	for i := 0; i+1 < len(pairs); i += 2 {
		out = append(out, mqCol{Key: pairs[i], Label: pairs[i+1]})
	}
	return out
}

// Kafka 消费组的列名来自结构体字段(GroupID/CurrentOffset...), RocketMQ 的来自 map 键
// (group/current_offset...), 同一件事两种写法 —— 两边都列上, 反正偏好列表允许有对不上的项。
var mqGroupCols = mc(
	"group", "消费组", "GroupID", "消费组",
	"state", "状态", "State", "状态",
	"topic", "Topic", "Topic", "Topic",
	"partition", "分区", "Partition", "分区", "queue_id", "队列号",
	"current_offset", "已提交位点", "CurrentOffset", "已提交位点",
	"log_end_offset", "末尾位点", "LogEndOffset", "末尾位点",
	"lag", "积压", "Lag", "积压",
	"member", "成员", "member_id", "成员", "MemberID", "成员",
	"client_id", "客户端", "ClientID", "客户端",
	"client_host", "来源", "ClientHost", "来源",
)

// 读动词口径: kafka_impl.go:1188-1192, rocketmq_impl.go:1366-1371,
// rabbitmq_impl.go:871-877, mqtt_impl.go:1582-1585
var mqCapabilities = map[string]mqCapability{
	"kafka": {
		Engine: "kafka",
		Views: []mqView{
			{Key: "topics", Label: "Topic", List: "SHOW TOPICS", Detail: "DESCRIBE TOPIC %s",
				Peek: "CONSUME FROM %s", Publish: true,
				Cols: mc("topic", "Topic", "partition_count", "分区数", "internal", "内部"),
				Note: "取数按消费组读取, 不提交位点(驱动里 CommitInterval=0, 也没有 CommitOffsets)"},
			{Key: "groups", Label: "消费组", List: "SHOW CONSUMER GROUPS", Detail: "DESCRIBE CONSUMER GROUP %s",
				Cols: mqGroupCols},
		},
		Fields: []mqField{{Name: "key", Label: "消息键 Key", Kind: "text",
			Hint: "同一 Key 落到同一分区"}},
	},
	"rocketmq": {
		Engine: "rocketmq",
		Views: []mqView{
			{Key: "topics", Label: "Topic", List: "SHOW TOPICS", Detail: "DESCRIBE TOPIC %s",
				Peek: "CONSUME FROM %s", Publish: true,
				Cols: mc("topic", "Topic", "queue_count", "队列数", "system_topic", "系统 topic",
					"total_approximate_count", "估算总数"),
				Note: "死信/轨迹要先在 broker 侧开启, 本版没有做"},
			// 消费组页签**故意不放**: 真 broker 上实测, 驱动对 SHOW CONSUMER GROUPS 直接回
			// "消费组成员、队列进度和 Lag 查询不可用: 当前客户端未公开 broker 路由及对应运维 API"。
			// 能力表的意义就是"界面只挂真能用的入口", 挂一个必报错的页签等于骗人去点。
			// 动词本身还在(查询编辑器里手敲可用, 报错文案也说清了去哪儿查), 只是不做成面板入口。
		},
		Fields: []mqField{
			{Name: "tag", Label: "标签 Tag", Kind: "text", Hint: "订阅端按 Tag 过滤"},
			{Name: "keys", Label: "业务键 Keys", Kind: "text", Hint: "多个用空格分隔, 之后可按 Key 查"},
			{Name: "delayLevel", Label: "延迟级别", Kind: "int", Min: &mqInt0, Max: &mqInt9,
				Hint: "0=不延迟; 级别对应 broker 的 messageDelayLevel 表"},
		},
	},
	"rabbitmq": {
		Engine: "rabbitmq",
		Views: []mqView{
			{Key: "queues", Label: "队列", List: "SHOW QUEUES", Detail: "DESCRIBE QUEUE %s",
				Peek: "CONSUME FROM %s", Publish: true,
				Cols: mc("queue", "队列", "state", "状态", "messages", "消息数", "messages_ready", "待取",
					"messages_unacknowledged", "未确认", "consumers", "消费者", "node", "节点", "vhost", "VHost",
					"durable", "持久化", "auto_delete", "自动删除", "policy", "策略", "arguments", "参数"),
				Note: "取数用 ack_requeue_true: 读完退回队列, 不会把消息吃掉"},
			{Key: "exchanges", Label: "交换机", List: "SHOW EXCHANGES", Detail: "DESCRIBE EXCHANGE %s",
				Cols: mc("exchange_display", "交换机", "type", "类型", "durability", "持久化", "durable", "持久化",
					"auto_delete", "自动删除", "internal", "内部", "vhost", "VHost")},
			{Key: "vhosts", Label: "VHost", List: "SHOW VHOSTS",
				Cols: mc("vhost", "VHost", "tracing", "跟踪", "default_queue_type", "默认队列类型", "description", "说明")},
		},
		Fields: []mqField{
			{Name: "vhost", Label: "VHost", Kind: "text", Hint: "留空用连接默认"},
			{Name: "exchange", Label: "交换机", Kind: "text",
				Hint: "填了就必须同时填路由键(否则会用队列名当路由键)"},
			{Name: "routingKey", Label: "路由键", Kind: "text", Hint: "和交换机一起填; 按队列发布不用填"},
		},
	},
	"mqtt": {
		Engine: "mqtt",
		Views: []mqView{
			{Key: "topics", Label: "订阅", List: "SHOW TOPICS", Detail: "DESCRIBE TOPIC %s",
				Peek: "CONSUME FROM %s", Publish: true,
				Cols: mc("topic", "订阅", "qos", "QoS", "retain", "保留", "wildcard", "通配", "source", "来源"),
				Note: "列的是**连接配置里声明的 topic 过滤器**(含默认 topic), 不是 broker 的 topic 清单 —— MQTT 协议没有列举能力; 用「取数」临时订阅的 topic 也不会出现在这里(真机实测: 取过数之后仍然是空)"},
		},
		Fields: []mqField{
			{Name: "qos", Label: "QoS", Kind: "int", Min: &mqInt0, Max: &mqInt2, DefaultVal: 1},
			{Name: "retain", Label: "保留消息 Retain", Kind: "bool"},
		},
	},
}

// ===== /api/dbmanager/mq/capabilities =====

func (h *Handlers) handleMQCapabilities(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	conn, err := h.store.Get(r.URL.Query().Get("id"))
	if err != nil {
		writeErr(w, "连接不存在: "+err.Error(), http.StatusBadRequest)
		return
	}
	kind := mqEngineKind(string(conn.Info.Engine))
	if kind == "" {
		writeErr(w, "该连接不是消息队列数据源", http.StatusBadRequest)
		return
	}
	cap := mqCapabilities[kind]
	cap.Engine = kind
	writeJSON(w, map[string]any{"ok": true, "capability": cap})
}

// ===== /api/dbmanager/mq/publish =====

type mqPublishBody struct {
	ID          string `json:"id"`
	Destination string `json:"destination"`
	Exchange    string `json:"exchange,omitempty"`
	RoutingKey  string `json:"routingKey,omitempty"`
	VHost       string `json:"vhost,omitempty"`
	Key         string `json:"key,omitempty"`
	Tag         string `json:"tag,omitempty"`
	Keys        string `json:"keys,omitempty"`
	DelayLevel  *int   `json:"delayLevel,omitempty"`
	QoS         *int   `json:"qos,omitempty"`
	Retain      bool   `json:"retain,omitempty"`
	Payload     string `json:"payload"`
	Confirm     bool   `json:"confirm"`
}

// mqCommand 把表达意图翻成驱动接受的发布 JSON。驱动只认自己那几个键, 所以这里逐引擎列出来。
func mqCommand(kind string, b mqPublishBody) (map[string]any, error) {
	dest := strings.TrimSpace(b.Destination)
	if dest == "" {
		return nil, fmt.Errorf("要先有目的地(topic / 队列名)")
	}
	if b.Payload == "" {
		return nil, fmt.Errorf("消息正文不能为空")
	}
	switch kind {
	case "kafka":
		cmd := map[string]any{"publish": dest, "value": b.Payload}
		if k := strings.TrimSpace(b.Key); k != "" {
			cmd["key"] = k
		}
		return cmd, nil
	case "rocketmq":
		cmd := map[string]any{"publish": dest, "body": b.Payload}
		if t := strings.TrimSpace(b.Tag); t != "" {
			cmd["tag"] = t
		}
		if k := strings.TrimSpace(b.Keys); k != "" {
			cmd["keys"] = k
		}
		if b.DelayLevel != nil && *b.DelayLevel > 0 {
			cmd["delayLevel"] = *b.DelayLevel
		}
		return cmd, nil
	case "rabbitmq":
		cmd := map[string]any{"queue": dest, "payload": b.Payload}
		if v := strings.TrimSpace(b.VHost); v != "" {
			cmd["vhost"] = v
		}
		ex := strings.TrimSpace(b.Exchange)
		rk := strings.TrimSpace(b.RoutingKey)
		if ex == "" && rk != "" {
			// 不报这句的话, 用户填的路由键会被驱动的"按队列发布"覆盖成队列名 ——
			// 静默改语义比报错危险。
			return nil, fmt.Errorf("路由键要和交换机一起填; 按队列发布不用填路由键")
		}
		if ex != "" {
			if rk == "" {
				// 驱动在"给了队列没给路由键"时会拿队列名当路由键 —— 对交换机投递来说这不是用户想要的,
				// 与其静默用一个看不出来的键, 不如要人补上。
				return nil, fmt.Errorf("指定了交换机就要同时给出路由键(空路由键请用「按队列发布」并把交换机留空)")
			}
			cmd["exchange"] = ex
			cmd["routing_key"] = rk
		}
		return cmd, nil
	case "mqtt":
		cmd := map[string]any{"publish": dest, "payload": b.Payload}
		if b.QoS != nil {
			if *b.QoS < 0 || *b.QoS > 2 {
				return nil, fmt.Errorf("QoS 只能是 0/1/2")
			}
			cmd["qos"] = *b.QoS
		}
		if b.Retain {
			cmd["retain"] = true
		}
		return cmd, nil
	}
	return nil, fmt.Errorf("该引擎不支持从这里发送消息")
}

func (h *Handlers) handleMQPublish(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var body mqPublishBody
	r.Body = http.MaxBytesReader(w, r.Body, 4<<20) // 消息体上限 4MB: 真实 broker 也都在这个量级以下拒绝
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, "请求体不合法(或消息过大): "+err.Error(), http.StatusBadRequest)
		return
	}
	conn, err := h.store.Get(body.ID)
	if err != nil {
		writeErr(w, "连接不存在: "+err.Error(), http.StatusBadRequest)
		return
	}
	kind := mqEngineKind(string(conn.Info.Engine))
	if kind == "" {
		writeErr(w, "该连接不是消息队列数据源", http.StatusBadRequest)
		return
	}
	cmd, err := mqCommand(kind, body)
	if err != nil {
		writeErr(w, err.Error(), http.StatusBadRequest)
		return
	}
	db, engine, err := h.pool.AcquireForSync(body.ID)
	if err != nil {
		writeErr(w, "连接不可用: "+err.Error(), http.StatusBadRequest)
		return
	}
	text, err := json.Marshal(cmd)
	if err != nil {
		writeErr(w, "拼装发布命令失败: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// 审计与拦截用的文本**不带消息正文**: 正文可能很大、也可能含业务敏感数据,
	// 而审计要回答的是"谁往哪儿投了多大一条", 不是"内容是什么"。
	auditText := mqAuditText(kind, cmd)
	risk, reason := RiskMedium, mqPublishReason(kind, cmd)
	if h.interceptWrite(w, conn, body.ID, auditText, risk, reason, body.Confirm) {
		return
	}

	affected, execErr := db.Exec(string(text))
	decision, detail := "executed", reason
	if execErr != nil {
		decision, detail = "failed", execErr.Error()
	}
	h.audit.Append(AuditEntry{
		ConnID: body.ID, ConnName: conn.Info.Name, Engine: engine,
		SQL: auditText, Risk: string(risk), Decision: decision, Detail: detail,
	})
	if execErr != nil {
		writeJSON(w, map[string]any{"ok": false, "statement": auditText, "error": execErr.Error()})
		return
	}
	writeJSON(w, map[string]any{"ok": true, "statement": auditText, "affected": affected})
}

// mqPublishReason 说清这次拦的是"往某个目的地投递" —— 以前这条走 /query,
// 拦下来时原因只会写"无法识别语句类型, 按写操作处理", 用户看不出自己在干什么。
func mqPublishReason(kind string, cmd map[string]any) string {
	dest, _ := cmd["publish"].(string)
	if dest == "" {
		dest, _ = cmd["queue"].(string)
	}
	return fmt.Sprintf("消息投递: 1 条 → %s/%s", kind, dest)
}

// mqAuditText 是审计/界面展示用的语句形态, 与驱动内部的 JSON 命令一一对应但可读。
func mqAuditText(kind string, cmd map[string]any) string {
	parts := []string{"PRODUCE", kind}
	for _, k := range []string{"publish", "queue", "vhost", "exchange", "routing_key", "key", "tag", "keys", "delayLevel", "qos", "retain"} {
		if v, ok := cmd[k]; ok && fmt.Sprint(v) != "" {
			parts = append(parts, fmt.Sprintf("%s=%v", k, v))
		}
	}
	n := 0
	for _, k := range []string{"value", "body", "payload"} {
		if s, ok := cmd[k].(string); ok {
			n = len(s)
			break
		}
	}
	return fmt.Sprintf("%s bytes=%d", strings.Join(parts, " "), n)
}
