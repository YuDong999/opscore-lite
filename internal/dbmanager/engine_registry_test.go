// 引擎注册的三张表必须一致: engineMetas(界面展示)、engineTypeSupported(建连接白名单)、
// gonavi 工厂(真能造出来)。历史上 redis 就是"前两张有、工厂没有"→ 界面上一个必失败的入口。
// 这条测试把那次的坑钉死: 少任何一张表都直接红。
package dbmanager

import (
	"strings"
	"testing"

	gonaviDB "opscore/internal/dbmanager/gonavi/db"
)

// 已知"声明了但工厂造不出来"的两个引擎, 各自原因不同, 都在这里显式记一笔而不是默默放过:
//   - mysql_agent: 故意禁用(handlers.go:777 —— lite 版没有 agent 二进制), 引导用原生 MySQL;
//     存量连接还能编辑, 所以引擎元数据留着。这是设计, 不是漂移。
//   - sqlite: 真漂移 —— engineMetas 有它、engineTypeSupported 放行它, 但 gonavi 工厂里
//     没有编译期条目(只有 SQL 文本里出现 sqlite_master)。前端把它标成"可启用", 可驱动安装
//     也变不出一个工厂条目来, 所以建连接必然失败。与 2026-09-25 摘掉 redis 时同一类问题,
//     留待产品定夺(补实现 或 从展示名单里摘掉), 这里先把它当**已知例外**记下。
var registryKnownGaps = map[string]string{
	"mysql_agent": "故意禁用(lite 无 agent 二进制)",
	"sqlite":      "工厂无条目: 待补实现或摘除声明",
}

func TestEngineRegistryAgreesWithFactory(t *testing.T) {
	for _, m := range AllEngineMetas() {
		if !engineTypeSupported(m.Type) {
			t.Errorf("%s: 在 engineMetas 里展示, 但建连接的白名单不放行 → 用户选了就用不了", m.Type)
		}
		if why, gap := registryKnownGaps[string(m.Type)]; gap {
			t.Logf("%s: 已知例外(%s)", m.Type, why)
			continue
		}
		db, err := gonaviDB.NewDatabase(string(m.Type))
		if err != nil {
			t.Errorf("%s: 声明支持但工厂造不出来: %v", m.Type, err)
			continue
		}
		if db == nil {
			t.Errorf("%s: 工厂返回了 nil", m.Type)
		}
	}
	// 反向: 白名单里不该有没展示给用户的引擎(custom 一类由前端单独列, 不在此断言)
	for _, e := range []EngineType{"redis", "kafka", "rabbitmq", "mqtt", "rocketmq"} {
		if !engineTypeSupported(e) {
			t.Errorf("%s 应被白名单放行", e)
		}
		if _, ok := GetEngineMeta(e); !ok {
			t.Errorf("%s 缺引擎元数据", e)
		}
	}
}

// Redis 是"没有 SQL 的引擎": 连接默认库留空、不声明 hasSql, 否则界面会给它挂上 SQL 编辑器。
func TestRedisEngineMetaShape(t *testing.T) {
	m, ok := GetEngineMeta(EngineRedis)
	if !ok {
		t.Fatal("redis 缺引擎元数据")
	}
	if m.HasSQL {
		t.Error("redis 不该声明 hasSql: 会挂上 SQL 编辑器入口")
	}
	if m.Category != "keyvalue" {
		t.Errorf("redis 分类 = %s, 期望 keyvalue", m.Category)
	}
	if m.DefaultPort != 6379 {
		t.Errorf("redis 默认端口 = %d, 期望 6379", m.DefaultPort)
	}
	if !strings.Contains(m.Description, "TTL") {
		t.Errorf("redis 描述没提能力: %s", m.Description)
	}
}
