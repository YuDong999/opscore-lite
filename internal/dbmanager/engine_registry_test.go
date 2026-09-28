// 引擎注册的三张表必须一致: engineMetas(界面展示)、engineTypeSupported(建连接白名单)、
// gonavi 工厂(真能造出来)。历史上 redis 就是"前两张有、工厂没有"→ 界面上一个必失败的入口。
// 这条测试把那次的坑钉死: 少任何一张表都直接红。
package dbmanager

import (
	"strings"
	"testing"

	gonaviDB "opscore/internal/dbmanager/gonavi/db"
)

// 已知"声明了但工厂造不出来"的引擎, 各自原因不同, 都在这里显式记一笔而不是默默放过:
//   - mysql_agent: 故意禁用(handlers.go:777 —— lite 版没有 agent 二进制), 引导用原生 MySQL;
//     存量连接还能编辑, 所以引擎元数据留着。这是设计, 不是漂移。
//   - sqlite: 由构建标签控制的可选内嵌驱动 —— 不带 -tags gonavi_sqlite_driver 的构建里
//     工厂没有条目(见 database_sqlite_factory.go), 所以默认构建下它确实造不出来。
//     **2026-09-28 修**: 真正的毛病不在"工厂没条目", 而在 optionalGoDriverBuildIncluded
//     的 lite 版与 full 版**函数体一模一样**(都只查 optionalGoDrivers 全量表, 不看构建标签),
//     于是它对 sqlite 谎报"已包含" → 界面显示"可选 + 已安装 + 就绪"、用户建连接必失败。
//     现在 lite 版如实回答; TestOptionalDriverBuildIncludedMatchesFactory 钉住这个不变量。
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


// TestOptionalDriverBuildIncludedMatchesFactory 钉住一条不变量:
// "IsOptionalGoDriverBuildIncluded(t) 说包含" ⇒ "工厂真能造出 t"。
//
// 为什么要有这条: 这两个条件一旦不一致, 界面就会骗人 ——
//   IsOptionalGoDriverBuildIncluded("sqlite") 曾对默认构建返回 true(lite/full 两个实现
//   函数体一样, 都没看构建标签), 而 databaseFactories 里没有 sqlite 条目。
//   DriverRuntimeSupportStatus 于是给出 (true, ""), /engines 与 /drivers 显示
//   "optional + installed + 无 reason" —— 用户建连接才失败, 且失败信息指向别处。
// 只要这条断言在, 任何"标签没带上却宣称包含"或"宣称包含却没注册工厂"都会当场变红。
func TestOptionalDriverBuildIncludedMatchesFactory(t *testing.T) {
	// 遍历展示名单(而不是 db 包那张未导出的表) —— 将来新增可选驱动会自动纳入这条不变量。
	for _, m := range AllEngineMetas() {
		driverType := string(m.Type)
		if !gonaviDB.IsOptionalGoDriver(driverType) {
			continue
		}
		if !gonaviDB.IsOptionalGoDriverBuildIncluded(driverType) {
			continue
		}
		db, err := gonaviDB.NewDatabase(driverType)
		if err != nil {
			t.Errorf("%s: IsOptionalGoDriverBuildIncluded 说包含, 但工厂造不出来: %v", driverType, err)
			continue
		}
		if db == nil {
			t.Errorf("%s: 工厂返回 nil", driverType)
		}
	}
}

// TestSlimBuildDoesNotClaimEmbeddedDrivers 反向钉: 精简构建下, 标签门控的内嵌驱动
// 必须**不**被宣称包含 —— 这正是上面那个谎报的直接判据。
func TestSlimBuildDoesNotClaimEmbeddedDrivers(t *testing.T) {
	if gonaviDB.IsOptionalGoDriverBuildIncluded("sqlite") {
		if _, err := gonaviDB.NewDatabase("sqlite"); err != nil {
			t.Fatalf("sqlite 被宣称包含, 但工厂造不出来: %v", err)
		}
		return // 真带了 tag 的构建: 宣称包含且工厂可用, 自洽
	}
	if ok, reason := gonaviDB.DriverRuntimeSupportStatus("sqlite"); ok {
		t.Errorf("sqlite 未被宣称包含, 运行时状态却是可用(界面会骗人)")
	} else if strings.TrimSpace(reason) == "" {
		t.Errorf("sqlite 不可用却没给原因")
	}
}
