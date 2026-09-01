// SQL 风险分类与生产库感知 (ADR-003)。
// 设计吸收 dbx 的 sql_risk + production_safety 两个机制:
//   - classifySQLRisk: 按语句类型 + 是否带 WHERE 做文本层保守分级;
//   - isProductionConnection: 显式环境标记优先, 否则按名称/主机启发式判定。
// 注意: 这是保守判定而非完整 SQL 解析器 —— 宁可错杀(判高危), 不可漏放。

package dbmanager

import (
	"regexp"
	"strings"
)

// SqlRisk SQL 风险等级(从低到高)。
type SqlRisk string

const (
	RiskSafe     SqlRisk = "safe"     // 只读
	RiskMedium   SqlRisk = "medium"   // 普通写 (INSERT / 带条件的 UPDATE/DELETE)
	RiskHigh     SqlRisk = "high"     // DDL / 管理语句 (CREATE/ALTER/GRANT...)
	RiskCritical SqlRisk = "critical" // 破坏性 (DROP/TRUNCATE/无 WHERE 的 UPDATE/DELETE)
)

var riskOrder = map[SqlRisk]int{
	RiskSafe:     0,
	RiskMedium:   1,
	RiskHigh:     2,
	RiskCritical: 3,
}

// AtLeast 判断风险是否达到某个等级。
func (r SqlRisk) AtLeast(other SqlRisk) bool {
	return riskOrder[r] >= riskOrder[other]
}

// HumanReadable 中文名(用于确认弹窗/审计展示)。
func (r SqlRisk) HumanReadable() string {
	switch r {
	case RiskSafe:
		return "只读"
	case RiskMedium:
		return "写操作"
	case RiskHigh:
		return "结构变更(DDL)"
	case RiskCritical:
		return "高危操作"
	}
	return string(r)
}

var (
	reLeadingWord = regexp.MustCompile(`(?is)^[\s(]*([a-zA-Z]+)`)
	reSQLComment  = regexp.MustCompile(`(?s)--[^\n]*|/\*.*?\*/|#[^\n]*`)
	reWhere       = regexp.MustCompile(`(?is)\bwhere\b`)
)

// stripComments 去掉 SQL 注释, 避免注释里的关键字造成误判。
func stripSQLComments(sql string) string {
	return reSQLComment.ReplaceAllString(sql, " ")
}

// classifySQLRisk 按 SQL 文本判定风险等级与原因。
func classifySQLRisk(engine string, sqlText string) (SqlRisk, string) {
	clean := strings.TrimSpace(stripSQLComments(sqlText))
	if clean == "" {
		return RiskSafe, "空语句"
	}
	m := reLeadingWord.FindStringSubmatch(clean)
	if m == nil {
		// 非字母开头(如括号/杂字符), 保守按 medium 处理
		return RiskMedium, "无法识别语句类型, 按写操作处理"
	}
	kw := strings.ToUpper(m[1])

	switch kw {
	case "SELECT", "SHOW", "DESCRIBE", "DESC", "EXPLAIN", "HELP":
		return RiskSafe, ""
	case "WITH":
		// PG 可写 CTE: WITH ... INSERT/UPDATE/DELETE —— 内含写关键字则按写处理
		u := strings.ToUpper(clean)
		switch {
		case regexp.MustCompile(`\bINSERT\b`).MatchString(u):
			return RiskMedium, "WITH ... INSERT (可写 CTE)"
		case regexp.MustCompile(`\bUPDATE\b`).MatchString(u):
			return RiskMedium, "WITH ... UPDATE (可写 CTE)"
		case regexp.MustCompile(`\bDELETE\b`).MatchString(u):
			return RiskMedium, "WITH ... DELETE (可写 CTE)"
		}
		return RiskSafe, ""
	case "USE":
		// 切库语句由 forbiddenSwitchReason 单独拦截, 这里给保守值兜底
		return RiskHigh, "USE 切库语句(禁止)"
	case "SET":
		return RiskMedium, "SET 会话变量修改"
	case "INSERT", "REPLACE", "CALL", "LOAD":
		return RiskMedium, "数据写入"
	case "UPDATE", "DELETE":
		if !reWhere.MatchString(clean) {
			return RiskCritical, kw + " 不带 WHERE 全表" + map[string]string{"UPDATE": "改写", "DELETE": "删除"}[kw]
		}
		return RiskMedium, "数据" + map[string]string{"UPDATE": "更新", "DELETE": "删除"}[kw]
	case "CREATE", "ALTER", "RENAME", "COMMENT", "GRANT", "REVOKE",
		"ANALYZE", "OPTIMIZE", "FLUSH", "KILL", "LOCK", "UNLOCK", "REFRESH", "VACUUM", "REINDEX":
		return RiskHigh, "结构/管理变更(DDL)"
	case "DROP", "TRUNCATE":
		return RiskCritical, kw + " 破坏性操作"
	default:
		// 未知关键字: 保守按 medium, 不放行为只读
		return RiskMedium, "未知语句类型(" + kw + "), 按写操作处理"
	}
}

var (
	reUseStmt    = regexp.MustCompile(`(?is)(^|;)\s*USE\s+[` + "`" + `"\[]?[a-zA-Z0-9_$]+`)
	reSearchPath = regexp.MustCompile(`(?is)\bSET\s+search_path\b`)
)

// forbiddenSwitchReason 检测禁止的切库行为, 返回原因(空串=放行)。
// 参考 dbx mcp_sql_has_forbidden_database_switch: 防止语句中途 USE 切库绕过连接白名单。
func forbiddenSwitchReason(engine string, sqlText string) string {
	clean := stripSQLComments(sqlText)
	if reUseStmt.MatchString(clean) {
		return "检测到 USE 切库语句: 请为每个库单独建立连接"
	}
	if engine == string(EnginePostgreSQL) && reSearchPath.MatchString(clean) {
		return "检测到 SET search_path 切换 schema: 请为每个 schema 单独建立连接"
	}
	// MySQL 反引号跨库引用 (db.table): 仅当连接默认库为空时阻断 ——
	// 默认库非空时跨库引用是常见合法写法, 不做过度拦截。
	return ""
}

// isProductionConnection 生产库感知 (dbx production_safety):
// 显式 envTag 优先; 未标记时按连接名/主机/库名启发式(包含 prod/production/生产/线上)。
func isProductionConnection(name string, cfg ConnectionConfig) bool {
	switch cfg.EnvTag {
	case "prod":
		return true
	case "dev", "staging":
		return false
	}
	hay := strings.ToLower(name + " " + cfg.Host + " " + cfg.Database)
	for _, kw := range []string{"prod", "production", "生产", "线上"} {
		if strings.Contains(hay, kw) {
			return true
		}
	}
	return false
}
