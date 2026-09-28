// MQ 能力表与发布命令拼装的测试。
//
// 这里能真验的是"我们自己这一层的一致性"(能力表 ↔ 只读动词白名单 ↔ 审计文本不含正文);
// "驱动是否接受这段 JSON"要靠链路另一端 —— RabbitMQ 那路在 mq_live_test 之外的假 management
// 服务上做过端到端(见本轮实测记录), Kafka/RocketMQ/MQTT 需要真 broker, 本机没有。
package dbmanager

import (
	"strconv"
	"strings"
	"testing"
)

func ptrInt(v int) *int { return &v }

func TestMQCommandShape(t *testing.T) {
	cases := []struct {
		kind    string
		body    mqPublishBody
		want    map[string]any
		wantErr string // 子串
	}{
		{"kafka", mqPublishBody{Destination: "orders", Payload: "v1"},
			map[string]any{"publish": "orders", "value": "v1"}, ""},
		{"kafka", mqPublishBody{Destination: "orders", Payload: "v1", Key: " k1 "},
			map[string]any{"publish": "orders", "value": "v1", "key": "k1"}, ""},
		{"rocketmq", mqPublishBody{Destination: "T", Payload: "b", Tag: "tg", Keys: "a b", DelayLevel: ptrInt(3)},
			map[string]any{"publish": "T", "body": "b", "tag": "tg", "keys": "a b", "delayLevel": 3}, ""},
		{"rocketmq", mqPublishBody{Destination: "T", Payload: "b", DelayLevel: ptrInt(0)},
			map[string]any{"publish": "T", "body": "b"}, ""}, // 0 级=不延迟, 不该把 0 发出去
		{"rabbitmq", mqPublishBody{Destination: "email", Payload: "p", VHost: "billing"},
			map[string]any{"queue": "email", "payload": "p", "vhost": "billing"}, ""},
		{"rabbitmq", mqPublishBody{Destination: "email", Payload: "p", Exchange: "events", RoutingKey: "e.#"},
			map[string]any{"queue": "email", "payload": "p", "exchange": "events", "routing_key": "e.#"}, ""},
		{"rabbitmq", mqPublishBody{Destination: "email", Payload: "p", Exchange: "events"},
			nil, "路由键"},
		{"rabbitmq", mqPublishBody{Destination: "email", Payload: "p", RoutingKey: "rk-1"},
			nil, "一起填"}, // 只填路由键会被驱动的"按队列发布"覆盖成队列名, 所以直接要人补交换机
		{"mqtt", mqPublishBody{Destination: "s/1", Payload: "p", QoS: ptrInt(2), Retain: true},
			map[string]any{"publish": "s/1", "payload": "p", "qos": 2, "retain": true}, ""},
		{"mqtt", mqPublishBody{Destination: "s/1", Payload: "p", QoS: ptrInt(5)}, nil, "QoS"},
		{"kafka", mqPublishBody{Payload: "v"}, nil, "目的地"},
		{"kafka", mqPublishBody{Destination: "orders"}, nil, "正文"},
		{"influx", mqPublishBody{Destination: "a", Payload: "b"}, nil, "不支持"},
	}
	for _, c := range cases {
		got, err := mqCommand(c.kind, c.body)
		if c.wantErr != "" {
			if err == nil || !strings.Contains(err.Error(), c.wantErr) {
				t.Errorf("%s/%#v: 期望报错含 %q, 实际 %v", c.kind, c.body, c.wantErr, err)
			}
			continue
		}
		if err != nil {
			t.Errorf("%s/%#v: 意外报错 %v", c.kind, c.body, err)
			continue
		}
		if len(got) != len(c.want) {
			t.Errorf("%s: 键数不对 got=%v want=%v", c.kind, got, c.want)
			continue
		}
		for k, v := range c.want {
			if got[k] != v {
				t.Errorf("%s: 键 %s got=%v want=%v", c.kind, k, got[k], v)
			}
		}
	}
}

// 审计/拦截文本不能带消息正文: 正文可能很大, 也可能带业务数据。
func TestMQAuditTextCarriesNoPayload(t *testing.T) {
	secret := "身份证号 11010119900307123X"
	cmd, err := mqCommand("kafka", mqPublishBody{Destination: "orders", Payload: secret, Key: "k1"})
	if err != nil {
		t.Fatal(err)
	}
	text := mqAuditText("kafka", cmd)
	if strings.Contains(text, secret) || strings.Contains(text, "110101") {
		t.Errorf("审计文本里出现了消息正文: %s", text)
	}
	wantBytes := "bytes=" + strconv.Itoa(len(secret))
	for _, want := range []string{"PRODUCE kafka", "publish=orders", "key=k1", wantBytes} {
		if !strings.Contains(text, want) {
			t.Errorf("审计文本缺少 %q: %s", want, text)
		}
	}
	if reason := mqPublishReason("kafka", cmd); !strings.Contains(reason, "消息投递") || !strings.Contains(reason, "orders") {
		t.Errorf("拦截原因没写清在干什么: %s", reason)
	}
}

// 能力表里的每个动词都必须过只读白名单 —— 这两份表一个是面板要的、一个是安全判定用的,
// 漂移的结果是"面板上点了却仍被当写拦截", 所以拿一条测试把它们钉在一起。
func TestMQCapabilityVerbsAreReadOnly(t *testing.T) {
	for kind, cap := range mqCapabilities {
		if len(cap.Views) == 0 {
			t.Errorf("%s: 能力表没有任何浏览页签", kind)
		}
		publishable := 0
		for _, v := range cap.Views {
			if !isMQReadStatement(kind, v.List) && classifySQLRiskForTest(kind, v.List) != RiskSafe {
				t.Errorf("%s.%s: 列表动词 %q 不被判为只读", kind, v.Key, v.List)
			}
			if v.Detail != "" {
				s := strings.ReplaceAll(v.Detail, "%s", "demo")
				if classifySQLRiskForTest(kind, s) != RiskSafe {
					t.Errorf("%s.%s: 详情动词 %q 不被判为只读", kind, v.Key, s)
				}
			}
			if v.Peek != "" {
				s := strings.ReplaceAll(v.Peek, "%s", "demo")
				if !isMQReadStatement(kind, s) {
					t.Errorf("%s.%s: 取数动词 %q 没进只读白名单, 点了会被写锁拦", kind, v.Key, s)
				}
			}
			if v.Publish {
				publishable++
			}
		}
		if publishable == 0 {
			t.Errorf("%s: 没有任何可发送的页签", kind)
		}
		if len(cap.Fields) == 0 {
			t.Errorf("%s: 没有发送可选项, 面板表单会是空壳", kind)
		}
	}
}

func classifySQLRiskForTest(engine, sql string) SqlRisk {
	r, _ := classifySQLRisk(engine, sql)
	return r
}
