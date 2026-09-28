// MQ 只读动词的分类/分流测试。
//
// 这里要守住的性质只有一条: **被白名单放行的文本, 在驱动侧只可能执行读操作**。
// MQ 引擎的写(发布消息)只有 ExecContext 一条路, 所以"判成只读 ⇒ 走 Query 通道 ⇒ 不可能写库"
// 是成立的方向; 反方向(把写误判成读)才是危险, 因此下面每个"应当放行"的用例都对着驱动的语法抄,
// 每个"不该放行"的用例都刻意做成看着像读的样子。
package dbmanager

import (
	"strings"
	"testing"

	syncpkg "opscore/internal/dbmanager/sync"
)

func TestMQReadStatement(t *testing.T) {
	cases := []struct {
		engine string
		sql    string
		want   bool
		why    string
	}{
		// 驱动确实当读处理的(kafka_impl.go:1192 / rocketmq_impl.go:1371 / rabbitmq_impl.go:877 / mqtt_impl.go:1585)
		{"kafka", "CONSUME FROM orders", true, "最简形式"},
		{"kafka", "consume group billing from orders limit 20", true, "小写 + GROUP + LIMIT"},
		{"kafka", "  CONSUME FROM `orders.v2`  ", true, "反引号名 + 首尾空白"},
		{"kafka", `CONSUME GROUP "g-1" FROM "orders"` + ";", true, "双引号名 + 尾分号"},
		{"kafka", "CONSUME FROM orders OFFSET 100 LIMIT 5", true, "带偏移"},
		{"rocketmq", "CONSUME FROM OrderTopic", true, "RocketMQ 取消息"},
		{"rabbitmq", "CONSUME FROM email", true, "RabbitMQ 取消息(驱动用 ack_requeue_true, 不吞消息)"},
		{"mqtt", "CONSUME FROM sensor/1 QOS 1", true, "MQTT 订阅取数"},
		{"mqtt", "UNSUBSCRIBE FROM sensor/1", true, "撤的是本连接自己的订阅(mqtt_impl.go:578)"},

		// 不该放行的
		{"kafka", "CONSUME", false, "没有 FROM, 驱动不认"},
		{"kafka", "CONSUME orders", false, "缺 FROM"},
		{"rocketmq", "CONSUME GROUP g FROM OrderTopic", false, "RocketMQ 的语法里没有 GROUP"},
		{"mqtt", "CONSUME GROUP g FROM a", false, "MQTT 同样没有 GROUP"},
		{"kafka", "UNSUBSCRIBE FROM orders", false, "UNSUBSCRIBE 只有 MQTT 有"},
		{"mysql", "UNSUBSCRIBE FROM orders", false, "非 MQ 引擎一律不套这套白名单"},
		{"rabbitmq", `{"publish":"email","payload":"hi"}`, false, "发布命令是写: 必须留在 Exec 通道"},
		{"kafka", "DROP TOPIC orders", false, "MQ 也没有删除动词, 别顺手放行"},
		{"mqtt", "", false, "空文本"},
	}
	for _, c := range cases {
		if got := isMQReadStatement(c.engine, c.sql); got != c.want {
			t.Errorf("isMQReadStatement(%q, %q) = %v, 期望 %v (%s)", c.engine, c.sql, got, c.want, c.why)
		}
	}
}

// 风险分级与通道分流必须和上面的判定一致 —— 这两处以前都按 SQL 首词判,
// 结果 CONSUME 被当作"未知语句类型"按写拦截, 解锁后又因为 isReadOnlySQL=false 掉进 Exec 通道,
// 得到一句"Kafka 写入命令必须是 JSON"。
func TestMQReadVerbsAreClassifiedAsRead(t *testing.T) {
	for _, e := range []string{"kafka", "rocketmq", "rabbitmq", "mqtt"} {
		sql := "CONSUME FROM demo"
		if risk, reason := classifySQLRisk(e, sql); risk != RiskSafe {
			t.Errorf("%s: classifySQLRisk(%q) 风险=%v (%s), 期望 RiskSafe", e, sql, risk, reason)
		}
		if !isReadOnlySQL(e, sql) {
			t.Errorf("%s: isReadOnlySQL(%q)=false, 会被路由到 Exec 通道", e, sql)
		}
		if risk, _ := classifyBatchRisk(e, sql); risk != RiskSafe {
			t.Errorf("%s: classifyBatchRisk(%q) 风险=%v, 期望 RiskSafe", e, sql, risk)
		}
	}
	// MQTT 的 UNSUBSCRIBE 同一条路
	if risk, _ := classifySQLRisk("mqtt", "UNSUBSCRIBE FROM a/b"); risk != RiskSafe {
		t.Errorf("mqtt UNSUBSCRIBE 风险=%v, 期望 RiskSafe", risk)
	}
	// 反例: 同样的文本换到 SQL 引擎上仍然是"未知语句按写处理"
	if risk, _ := classifySQLRisk("mysql", "CONSUME FROM demo"); risk == RiskSafe {
		t.Errorf("mysql: CONSUME 被判成 RiskSafe, MQ 白名单泄漏到 SQL 引擎了")
	}
	if isReadOnlySQL("mysql", "CONSUME FROM demo") {
		t.Error("mysql: isReadOnlySQL(CONSUME)=true, 分流也被污染了")
	}
}

// 发布命令必须还是"写": 只读锁要拦, 而且要走 Exec。
func TestMQPublishStaysWrite(t *testing.T) {
	body := `{"publish":"orders","key":"k1","value":"v1"}`
	risk, reason := classifySQLRisk("kafka", body)
	if risk == RiskSafe {
		t.Error("Kafka 发布命令被判成只读")
	}
	if reason == "" {
		t.Error("非只读却没有给出原因, 拦截弹窗会没话可说")
	}
	if isReadOnlySQL("kafka", body) {
		t.Error("发布命令会被路由到 Query 通道(那里只会读)")
	}
}

// 库名口径按引擎分: SQL 引擎的名字要进 SQL, 必须按标识符挡死; MQ 的"库"是 vhost,
// RabbitMQ 默认 vhost 字面就是 "/" —— 按 SQL 口径判它就成了"格式非法", 树里展不开队列。
func TestValidDBNameByEngine(t *testing.T) {
	if validDBName("mysql", "/") {
		t.Error("mysql: 不该放行 \"/\" 这种库名(会被拼进 SQL)")
	}
	if !validDBName("mysql", "sync_src") {
		t.Error("mysql: 正常库名被拒了")
	}
	if validDBName("mysql", "a;DROP TABLE t") {
		t.Error("mysql: 注入形态被放行")
	}
	for _, ok := range []string{"/", "billing", "my/vhost", "opscore-mq"} {
		if !validDBName("rabbitmq", ok) {
			t.Errorf("rabbitmq: vhost %q 该被接受", ok)
		}
	}
	for _, bad := range []string{"", "a\nb", strings.Repeat("x", 200)} {
		if validDBName("rabbitmq", bad) {
			t.Errorf("rabbitmq: 空/控制字符/超长(%d) 该被拒", len(bad))
		}
	}
}

// MQ 不按分号分句: JSON 载荷里的分号是数据, 切开之后每段都不是合法命令。
func TestSplitStatementsForEngineSkipsMQ(t *testing.T) {
	body := `{"publish":"orders","value":"a;b;c"}`
	got := splitStatementsForEngine("kafka", body, syncpkg.DialectMySQL)
	if len(got) != 1 || got[0] != body {
		t.Errorf("MQ 语句被切开了: %#v", got)
	}
	sql := splitStatementsForEngine("mysql", "SELECT 1; SELECT 2", syncpkg.DialectMySQL)
	if len(sql) != 2 {
		t.Errorf("SQL 引擎的分句被误伤: %#v", sql)
	}
}

// 白名单不能变成绕过写锁的后门: "以 CONSUME 开头 + 尾巴上挂一条发布命令"这种混合文本,
// 关键不在它被判成什么, 而在它**只会走 Query 通道** —— MQ 驱动的 Query 通道里没有写,
// 尾部那段 JSON 会被驱动的读语法忽略掉, 不会被执行。下面把这条不变量钉住。
func TestMQPrefixWithTrailingWriteNeverReachesExec(t *testing.T) {
	text := "CONSUME FROM orders; " + `{"publish":"orders","value":"x"}` + ";"
	stmts := splitStatementsForEngine("kafka", text, syncpkg.DialectMySQL)
	if len(stmts) != 1 {
		t.Fatalf("期望整条不切: %#v", stmts)
	}
	if !isReadOnlySQL("kafka", stmts[0]) {
		t.Fatal("这条被判成了写 → 会走 Exec 通道, 尾部 JSON 就有机会被发布")
	}
	// 反向确认: 同样的尾部单独发, 才是该被写锁拦下来的写命令
	if isReadOnlySQL("kafka", `{"publish":"orders","value":"x"}`) {
		t.Fatal("纯发布命令被判成只读")
	}
}
