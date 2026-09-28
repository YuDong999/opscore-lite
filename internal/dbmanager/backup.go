// 数据库备份(P1-7)。设计取舍见下, 它决定了这个文件的形状。
//
// ## 一个执行器 + 两个数据源
// 执行器只做一件事: **把字节流写进文件**。作业状态、压缩、目标目录、保留 N 份、历史记录
// 全部共用; 差别只在"谁来产生这串字节":
//   - 原生: 目标机上的 mysqldump / pg_dump(一致性快照是它们的参数, 保真度全覆盖)
//   - 内置: 我们自己生成 DDL + INSERT(跨引擎可用, 但**不含触发器/例程/权限**)
//
// ## 备份记录必须写明"用了哪种方式、没包含什么"
// 这是本模块最重要的一条: 两条路径的保真度不同, 而用户**必须能一眼看出手里这份能不能完整恢复**。
// 所以每条历史都带 Mode / Consistency / Includes / Excludes —— 把"完整不完整"变成显式声明,
// 而不是让人默认它是完整的。选内置路径时 Excludes 里会明写触发器/例程/权限/序列。
//
// ## 在"数据所在的机器"上执行
// 不把整库经网络拉到 server 再写盘(那既慢又浪费)。本机连接直接跑; 远程连接走
// RunOnTarget 在目标机执行。备份文件也落在**目标机**上(路径由调用方给)。
//
// ## 刻意没做
// - **不做增量**: 增量是另一个量级的事(位点/时间戳/一致性), 做错比不做更危险。
// - **不设行数上限**: dbx 是每表 1 万行(`DATABASE_EXPORT_ROW_LIMIT`), 那不是备份是取样。
//   要限量由调用方显式给 maxRows。
package dbmanager

import (
	"context"
	"fmt"
	"path"
	"regexp"
	"sort"
	"strings"
	"time"
)

// BackupMode 说明这份备份是怎么产生的 —— 它决定"能不能完整恢复"。
type BackupMode string

const (
	BackupModeNative   BackupMode = "native"  // 目标机上的 mysqldump / pg_dump
	BackupModeBuiltin  BackupMode = "builtin" // 内置 SQL 导出(DDL + INSERT)
	BackupModeNativeNO BackupMode = "native-unavailable"
)

// BackupRecord 一条备份记录。**它不是日志, 是"这份备份是什么"的声明** ——
// 所以 Includes/Excludes 与 Mode 一样是必填信息, 前端要显式展示。
type BackupRecord struct {
	ID          string     `json:"id"`
	ConnID      string     `json:"connId"`
	ConnName    string     `json:"connName"`
	Engine      string     `json:"engine"`
	Database    string     `json:"database"`
	Mode        BackupMode `json:"mode"`
	Tool        string     `json:"tool,omitempty"`        // 原生路径: 用的哪个工具 + 版本
	Consistency string     `json:"consistency,omitempty"` // 原生路径: 一致性参数; 内置路径: 事务隔离级别
	HostID      string     `json:"hostId,omitempty"`      // 备份落在哪台机器
	FilePath    string     `json:"filePath"`              // 目标机上的绝对路径
	SizeBytes   int64      `json:"sizeBytes"`
	Tables      []string   `json:"tables,omitempty"` // 备份了哪些表(空=全库)
	Includes    []string   `json:"includes"`         // 这份备份**包含**什么
	Excludes    []string   `json:"excludes"`         // 这份备份**不含**什么(空 = 完整)
	DurationMs  int64      `json:"durationMs"`
	StartedAt   int64      `json:"startedAt"`
	Status      string     `json:"status"` // ok / failed
	Error       string     `json:"error,omitempty"`
}

// IncludesFor 给出某条路径下"这份备份包含什么"的诚实描述。
func (m BackupMode) IncludesFor(engine string) []string {
	switch m {
	case BackupModeNative:
		base := []string{"表结构", "数据", "索引", "主键/外键", "视图", "触发器", "存储过程/函数", "事件", "权限(GRANT)", "序列/自增位点"}
		if isMySQLFamily(engine) {
			return append(base, "建库语句(CREATE DATABASE)")
		}
		return base
	case BackupModeBuiltin:
		// 内置路径**必须如实说明缺什么** —— 它缺的正是"能不能完整恢复"的关键部分
		return []string{"表结构(含列注释)", "索引", "数据"}
	}
	return nil
}

// ExcludesFor 给出"这份备份不含什么"。内置路径这里是**非空**的, 且必须展示给用户。
func (m BackupMode) ExcludesFor(engine string) []string {
	if m == BackupModeBuiltin {
		return []string{"触发器", "存储过程/函数", "事件", "权限(GRANT)", "序列当前值", "表空间/分区定义细节"}
	}
	return nil // 原生路径: 完整
}

// ConsistencyFor 说明这份备份的一致性口径 —— 没有它, "备份中途有人写入"就是一份自相矛盾的备份。
func (m BackupMode) ConsistencyFor(engine string) string {
	if m != BackupModeNative {
		return "内置导出: 单事务 REPEATABLE READ(导出期间不阻塞写入, 但快照一致)"
	}
	if isMySQLFamily(engine) {
		return "--single-transaction(InnoDB 一致性快照, 不锁表)"
	}
	return "--serializable-deferrable(可串行化快照)"
}

// isMySQLFamily 判断是不是 MySQL 系(含 MariaDB/GoldenDB 等兼容分支) —— 决定用哪个工具与参数。
func isMySQLFamily(engine string) bool {
	switch strings.ToLower(strings.TrimSpace(engine)) {
	case "mysql", "mysql_agent", "mariadb", "goldendb":
		return true
	}
	return false
}

func isPostgresFamily(engine string) bool {
	switch strings.ToLower(strings.TrimSpace(engine)) {
	case "postgres", "opengauss", "kingbase", "highgo", "vastbase", "gaussdb":
		return true
	}
	return false
}

// detectNativeBackupTool 探测目标机上有没有可用的原生备份工具, 返回命令与版本。
// 探测用 --version(只读), 失败就当没有 —— 不做"猜一个试试"。
func detectNativeBackupTool(hostID, engine string) (tool string, version string, ok bool) {
	candidates := nativeToolCandidates(engine)
	for _, c := range candidates {
		if _, err := LookPathOnTarget(hostID, c); err != nil {
			continue
		}
		out, err := RunOnTargetQuiet(hostID, []string{c, "--version"})
		if err != nil {
			continue
		}
		return c, firstLine(out), true
	}
	return "", "", false
}

// nativeToolCandidates 按引擎给候选工具名(顺序即优先级)。
func nativeToolCandidates(engine string) []string {
	if isMySQLFamily(engine) {
		return []string{"mysqldump", "mariadb-dump"} // MariaDB 新版把 mysqldump 改名了
	}
	if isPostgresFamily(engine) {
		return []string{"pg_dump"}
	}
	return nil
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return strings.TrimSpace(s[:i])
	}
	return s
}

// validBackupPath 校验备份文件路径。它是**在目标机上**由我们拼给 shell 的,
// 所以必须挡住注入: 只允许常规字符, 且不许以 - 开头(免得被当成参数)。
var reBackupPath = regexp.MustCompile(`^/[A-Za-z0-9._/\-]{1,400}$`)

func validBackupPath(p string) bool {
	if !reBackupPath.MatchString(p) {
		return false
	}
	if strings.Contains(p, "..") {
		return false
	}
	return !strings.HasPrefix(path.Base(p), "-")
}

// validBackupTarget 校验库名/表名(拼进工具参数用)。
func validBackupTarget(s string) bool { return validIdentifier(s) }

// backupFileName 生成备份文件名。
// 刻意**不用库名做文件名**: 库名可能含怪字符, 而文件名要进 shell 参数。
func backupFileName(engine, database string, at time.Time) string {
	safe := strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' || r == '-' {
			return r
		}
		return '_'
	}, database)
	if len(safe) > 40 {
		safe = safe[:40]
	}
	return fmt.Sprintf("%s-%s-%s.sql.gz", strings.ToLower(engine), safe, at.Format("20060102-150405"))
}

// nativeBackupArgv 拼原生备份命令。**所有值经 shell 转义由调用方负责**(见 RunOnTargetQuiet 的契约)。
//
// 关键: 一致性参数是必须项, 不是可选项 —— 少了它就是"备份期间允许写入", 那份备份自相矛盾。
func nativeBackupArgv(tool, engine, database string, tables []string, filePath string) []string {
	argv := []string{tool}
	if isMySQLFamily(engine) {
		// --single-transaction: InnoDB 一致性快照不锁表
		// --routines --triggers --events: 这三样默认**不导**, 必须显式要
		// --hex-blob: 二进制安全
		// --set-gtid-purged=OFF: 免得在目标库回放时污染 GTID
		argv = append(argv,
			"--single-transaction", "--routines", "--triggers", "--events",
			"--hex-blob", "--set-gtid-purged=OFF", "--default-character-set=utf8mb4",
			database)
		if len(tables) > 0 {
			argv = append(argv, tables...)
		}
		return argv
	}
	// pg_dump: -Fp 纯文本(与 mysqldump 的输出口径一致, 便于人读与 grep)
	// 一致性靠 --serializable-deferrable(要求目标库非只读且有权限)
	argv = append(argv,
		"--format=plain", "--no-owner", "--no-privileges=false",
		"--serializable-deferrable",
		"--dbname="+database)
	for _, t := range tables {
		argv = append(argv, "--table="+t)
	}
	return argv
}

// splitTableArgs 把命令行风格的表名参数排序去重(便于测试与稳定输出)。
func splitTableArgs(in []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(in))
	for _, t := range in {
		t = strings.TrimSpace(t)
		if t == "" || seen[t] {
			continue
		}
		seen[t] = true
		out = append(out, t)
	}
	sort.Strings(out)
	return out
}

// backupPruneKeep / backupDefaultDir 是默认值; 由请求覆盖。
const (
	backupDefaultKeep = 5
	backupDefaultDir  = "/var/backups/opscore"
)

// planPrune 算出该删哪些旧备份(保留最近 keep 份)。
// **只看同一条连接 + 同一个库**的历史, 不跨连接误删别人的备份。
func planPrune(history []BackupRecord, connID, database string, keep int) []BackupRecord {
	if keep <= 0 {
		return nil
	}
	var mine []BackupRecord
	for _, r := range history {
		if r.ConnID == connID && r.Database == database && r.Status == "ok" {
			mine = append(mine, r)
		}
	}
	// 新的在前
	sort.Slice(mine, func(i, j int) bool { return mine[i].StartedAt > mine[j].StartedAt })
	if len(mine) <= keep {
		return nil
	}
	return mine[keep:]
}

// backupTimeout 给一次备份的总时限(大库要久, 但也不能无限挂着)。
func backupTimeout(sizeHint int64) time.Duration {
	d := 30 * time.Minute
	if sizeHint > 0 {
		// 按体量放宽: 每 GB 加 5 分钟, 上限 6 小时
		d += time.Duration(sizeHint/(1<<30)) * 5 * time.Minute
	}
	if d > 6*time.Hour {
		d = 6 * time.Hour
	}
	return d
}

// ctxWithBackupTimeout 是上面那个时限的便捷包装。
func ctxWithBackupTimeout(parent context.Context, sizeHint int64) (context.Context, context.CancelFunc) {
	return context.WithTimeout(parent, backupTimeout(sizeHint))
}
