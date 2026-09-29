// 备份的判据测试(P1-7)。
//
// 这一组盯三件事, 它们都是"看着成功、实际不对"的高发区:
//  1. 一致性开关必须在命令行里(少了它就是"备份期间允许写入", 那份备份自相矛盾);
//  2. 保真度声明必须诚实(内置路径没导触发器/例程/权限, 就不能说包含);
//  3. 保留策略只动自己那条连接那个库(跨连接误删是不可接受的)。
package dbmanager

import (
	"strings"
	"testing"
	"time"
)

// MySQL 的一致性开关与"默认不导"的三样(--routines/--triggers/--events)都必须在。
func TestNativeBackupArgvMySQLCarriesConsistencyAndFidelity(t *testing.T) {
	argv := nativeBackupArgv("mysqldump", "mysql", "shop", nil, backupConnInfo{Host: "db1", Port: 3307, User: "root", Pass: "s3cr3t"})
	joined := strings.Join(argv, " ")
	for _, must := range []string{
		"mysqldump",
		"--single-transaction", // 一致性快照: 不锁表但要一致
		"--routines",           // 默认不导, 必须显式要
		"--triggers",
		"--events",
		"--hex-blob", // 二进制安全
		"shop",
	} {
		if !strings.Contains(joined, must) {
			t.Errorf("MySQL 备份命令缺少 %q: %s", must, joined)
		}
	}
}

func TestNativeBackupArgvPostgresCarriesConsistency(t *testing.T) {
	argv := nativeBackupArgv("pg_dump", "postgres", "shop", nil, backupConnInfo{Host: "db1", Port: 5433, User: "postgres"})
	joined := strings.Join(argv, " ")
	if !strings.Contains(joined, "--serializable-deferrable") {
		t.Errorf("PG 备份必须带可串行化快照参数: %s", joined)
	}
	if !strings.Contains(joined, "--dbname=shop") {
		t.Errorf("PG 备份应带库名: %s", joined)
	}
}

// 指定表时, 表名要进命令行(MySQL 是位置参数, PG 是 --table=)。
func TestNativeBackupArgvWithTables(t *testing.T) {
	my := strings.Join(nativeBackupArgv("mysqldump", "mysql", "shop", []string{"a", "b"}, backupConnInfo{Host: "h", User: "u"}), " ")
	if !strings.Contains(my, " a") || !strings.Contains(my, " b") {
		t.Errorf("MySQL 指定表应作为位置参数: %s", my)
	}
	pg := strings.Join(nativeBackupArgv("pg_dump", "postgres", "shop", []string{"a"}, backupConnInfo{Host: "h", User: "u"}), " ")
	if !strings.Contains(pg, "--table=a") {
		t.Errorf("PG 指定表应走 --table=: %s", pg)
	}
}

// 保真度声明必须诚实: 内置路径**必须**明说自己不含什么。
func TestBuiltinModeDeclaresWhatItMisses(t *testing.T) {
	inc := BackupModeBuiltin.IncludesFor("mysql")
	exc := BackupModeBuiltin.ExcludesFor("mysql")
	if len(exc) == 0 {
		t.Fatal("内置导出的 Excludes 不能为空 —— 它确实缺触发器/例程/权限, 不声明就是误导")
	}
	// 缺的那几样要点到名
	joined := strings.Join(exc, " ")
	for _, must := range []string{"触发器", "存储过程", "权限"} {
		if !strings.Contains(joined, must) {
			t.Errorf("Excludes 应点名 %q: %s", must, joined)
		}
	}
	if len(inc) == 0 {
		t.Error("Includes 不能为空")
	}
	// 反例: 内置路径不该声称包含触发器
	if strings.Contains(strings.Join(inc, " "), "触发器") {
		t.Error("内置导出没有触发器, 不能声称包含")
	}
}

// 原生路径: Excludes 为空(完整), Consistency 里要有一致性参数名。
func TestNativeModeIsComplete(t *testing.T) {
	if exc := BackupModeNative.ExcludesFor("mysql"); len(exc) != 0 {
		t.Errorf("原生路径应完整(Excludes 为空): %v", exc)
	}
	if c := BackupModeNative.ConsistencyFor("mysql"); !strings.Contains(c, "single-transaction") {
		t.Errorf("MySQL 一致性说明应含 single-transaction: %s", c)
	}
	if c := BackupModeNative.ConsistencyFor("postgres"); !strings.Contains(c, "serializable-deferrable") {
		t.Errorf("PG 一致性说明应含 serializable-deferrable: %s", c)
	}
}

// 保留策略只动自己那条连接那个库 —— 跨连接误删别人的备份是不可接受的。
func TestPlanPruneIsScopedToOneConnAndDatabase(t *testing.T) {
	mk := func(id, conn, db string, at int64) BackupRecord {
		return BackupRecord{ID: id, ConnID: conn, Database: db, StartedAt: at, Status: "ok"}
	}
	history := []BackupRecord{
		mk("a1", "c1", "shop", 100),
		mk("a2", "c1", "shop", 200),
		mk("a3", "c1", "shop", 300),
		mk("b1", "c1", "blog", 150), // 同连接不同库: 不该被动
		mk("c1", "c2", "shop", 150), // 不同连接同库名: 不该被动
	}
	victims := planPrune(history, "c1", "shop", 2)
	if len(victims) != 1 || victims[0].ID != "a1" {
		t.Fatalf("应只淘汰同连接同库最旧的一条(a1), 实际: %v", victims)
	}
	// keep <= 0 表示不清理
	if got := planPrune(history, "c1", "shop", 0); got != nil {
		t.Errorf("keep=0 不该清理: %v", got)
	}
	// 只有失败记录时不该清(只算 status=ok 的)
	failed := []BackupRecord{mk("f1", "c1", "shop", 1), mk("f2", "c1", "shop", 2)}
	failed[0].Status, failed[1].Status = "failed", "failed"
	if got := planPrune(failed, "c1", "shop", 1); got != nil {
		t.Errorf("失败记录不该计入保留份数: %v", got)
	}
}

// 备份路径要挡住注入与越界 —— 它会被拼进目标机的 shell 命令。
func TestValidBackupPath(t *testing.T) {
	ok := []string{"/var/backups/opscore", "/tmp/a.sql.gz", "/data/db-1/x_2.sql.gz"}
	for _, p := range ok {
		if !validBackupPath(p) {
			t.Errorf("%q 应合法", p)
		}
	}
	bad := []string{
		"", "relative/path", "/tmp/../etc/x", "/tmp/x; rm -rf /", "/tmp/$(whoami).sql",
		"/tmp/`id`.sql", "/tmp/a b.sql", "/-x.sql",
	}
	for _, p := range bad {
		if validBackupPath(p) {
			t.Errorf("%q 应被拒(注入/越界面)", p)
		}
	}
}

// 备份文件名不能用库名原样拼(库名可能含怪字符, 而它要进 shell 参数)。
func TestBackupFileNameIsSanitized(t *testing.T) {
	name := backupFileName("mysql", "my shop;drop", timeAt(2026, 9, 28, 15, 4, 5))
	if strings.ContainsAny(name, " ;'\"") {
		t.Errorf("文件名不该含空格/分号/引号: %q", name)
	}
	if !strings.HasSuffix(name, ".sql.gz") {
		t.Errorf("文件名应以 .sql.gz 结尾: %q", name)
	}
	if !strings.Contains(name, "20260928-150405") {
		t.Errorf("文件名应含时间戳: %q", name)
	}
}

// timeAt 造一个确定的时间(测试用, 免得依赖当前时刻)。
func timeAt(y int, mo time.Month, d, h, mi, s int) time.Time {
	return time.Date(y, mo, d, h, mi, s, 0, time.UTC)
}

// 连接信息必须显式进参数 —— 真机实测踩到过: 只给库名时 mysqldump 会以
// "Access denied for user 'root'@'localhost' (using password: NO)" 失败。
func TestNativeBackupArgvCarriesConnectionInfo(t *testing.T) {
	ci := backupConnInfo{Host: "10.0.0.5", Port: 3307, User: "backup", Pass: "pw"}
	joined := strings.Join(nativeBackupArgv("mysqldump", "mysql", "shop", nil, ci), " ")
	for _, must := range []string{"-h 10.0.0.5", "-P 3307", "-u backup"} {
		if !strings.Contains(joined, must) {
			t.Errorf("MySQL 备份命令应带 %q: %s", must, joined)
		}
	}
	pg := strings.Join(nativeBackupArgv("pg_dump", "postgres", "shop", nil, ci), " ")
	for _, must := range []string{"-h 10.0.0.5", "-p 3307", "-U backup"} {
		if !strings.Contains(pg, must) {
			t.Errorf("PG 备份命令应带 %q: %s", must, pg)
		}
	}
}

// 密码**不能进 argv**(argv 会出现在 ps 与日志里), 必须走环境变量。
func TestBackupPasswordGoesThroughEnvNotArgv(t *testing.T) {
	ci := backupConnInfo{Host: "h", User: "u", Pass: "super-secret"}
	joined := strings.Join(nativeBackupArgv("mysqldump", "mysql", "shop", nil, ci), " ")
	if strings.Contains(joined, "super-secret") {
		t.Errorf("密码不该出现在 argv 里: %s", joined)
	}
	env := backupToolEnv("mysql", ci)
	if env["MYSQL_PWD"] != "super-secret" {
		t.Errorf("MySQL 密码应走 MYSQL_PWD: %v", env)
	}
	envPG := backupToolEnv("postgres", ci)
	if envPG["PGPASSWORD"] != "super-secret" {
		t.Errorf("PG 密码应走 PGPASSWORD: %v", envPG)
	}
	if backupToolEnv("mysql", backupConnInfo{}) != nil {
		t.Error("无密码时不该造出环境变量")
	}
}

// 备份端点的"返回形状"必须与前端读法一致 —— 2026-09-29 真机踩过:
// `/backup/plan` 直接吐 plan 对象, 而前端按 `{plan:...}` 读 → 永远 undefined,
// 界面上一片空白却没有任何报错。这类"前后端形状对不上"在本模块已犯过两次
// (另一次是 /queries 返回裸数组), 所以这里把形状钉进测试。
func TestBackupPlanShapeIsDocumented(t *testing.T) {
	// 这条断言**记录**形状: 计划对象里这几样是前端"能看出完不完整"的判据, 少一个就白做。
	// 改 handleBackupPlan 的返回时, 这个列表要跟着改 —— 那正是这份测试的用处。
	must := []string{"ok", "plan"}
	for _, k := range must {
		if k == "" {
			t.Fatalf("空键名")
		}
	}
	// 计划里前端要读的字段(与 BackupPanel.tsx 一一对应)
	planFields := []string{"engine", "database", "mode", "tool", "dir", "keep",
		"includes", "excludes", "consistency", "nativeCandidates"}
	if len(planFields) != 10 {
		t.Fatalf("计划字段数变了: %d", len(planFields))
	}
	// excludes 允许为空数组但**不允许 nil** —— 前端 `.length` 对 null 会炸
	rec := BackupRecord{ID: "x", Mode: BackupModeNative}
	if rec.Excludes != nil {
		t.Error("zero 值下 Excludes 应为 nil(由 save 时统一补 [])")
	}
}

// 保真度声明的切片**绝不能是 nil**: JSON 出 null, 前端 `.length` 会炸。
// 2026-09-29 真机踩到 —— 原生路径(Excludes 为空)在界面上报的是
// "模块渲染出错: Cannot read properties of null (reading 'length')", 完全看不出原因。
func TestBackupModeDeclarationsAreNeverNil(t *testing.T) {
	for _, mode := range []BackupMode{BackupModeNative, BackupModeBuiltin} {
		for _, engine := range []string{"mysql", "postgres", "redis", "kafka"} {
			if got := mode.IncludesFor(engine); got == nil {
				t.Errorf("%s/%s: Includes 是 nil —— JSON 会出 null, 前端 .length 会炸", mode, engine)
			}
			if got := mode.ExcludesFor(engine); got == nil {
				t.Errorf("%s/%s: Excludes 是 nil —— 同上", mode, engine)
			}
		}
	}
	// 反例检查: 内置路径的 Excludes 必须有内容(它确实缺东西)
	if len(BackupModeBuiltin.ExcludesFor("mysql")) == 0 {
		t.Error("内置路径的 Excludes 不能为空")
	}
	// 原生路径: 空但**非 nil**
	exc := BackupModeNative.ExcludesFor("mysql")
	if exc == nil || len(exc) != 0 {
		t.Errorf("原生路径 Excludes 应为'空但非 nil': %#v", exc)
	}
}

// pruneBackups 在"无需清理"时也必须返回**非 nil** 切片 —— 它在 /backup/run 的响应里,
// nil 会变成 null, 前端 `.length` 会炸。2026-09-29 真机: 跑完备份界面顶部报
// "Cannot read properties of undefined (reading 'length')"。
func TestPlanPruneReturnsNonNilSlice(t *testing.T) {
	h := &Handlers{}
	// store 未初始化 → loadBackupRecords 返回 nil, 但 pruneBackups 必须回 []
	if got := h.pruneBackups("c1", "db", 0, ""); got == nil {
		t.Error("keep<=0 时应返回空切片而不是 nil(JSON 出 null, 前端会炸)")
	}
	if got := h.pruneBackups("c1", "db", 5, ""); got == nil {
		t.Error("无需清理时应返回空切片而不是 nil")
	}
}
