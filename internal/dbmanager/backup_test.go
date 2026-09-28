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
	argv := nativeBackupArgv("mysqldump", "mysql", "shop", nil, "/tmp/x.sql.gz")
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
	argv := nativeBackupArgv("pg_dump", "postgres", "shop", nil, "/tmp/x.sql.gz")
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
	my := strings.Join(nativeBackupArgv("mysqldump", "mysql", "shop", []string{"a", "b"}, "/tmp/x"), " ")
	if !strings.Contains(my, " a") || !strings.Contains(my, " b") {
		t.Errorf("MySQL 指定表应作为位置参数: %s", my)
	}
	pg := strings.Join(nativeBackupArgv("pg_dump", "postgres", "shop", []string{"a"}, "/tmp/x"), " ")
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
