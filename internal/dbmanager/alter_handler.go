package dbmanager

// apply-alter: 列级结构变更(加列/删列/改列)的语句由后端拼。
// 契约: POST {id, database, table, cols:[{kind:add|drop|modify, name, origName, type, nullable, default, comment}], confirm}
//   - confirm=false → 只回将执行的语句列表(预览), 不落库;
//   - confirm=true  → 走 interceptWrite 安全链后在一个事务里逐条执行。
//
// 为什么要有这个端点: 此前"表结构"编辑器在前端拼 ALTER 字符串(QUOTE_ID + 手搓 MODIFY/ALTER COLUMN),
// 与全站"写 SQL/DDL 一律后端拼"的口径不一致, 而且方言只分了两套、默认值直接裸拼 ——
// `DEFAULT now()` 这种能过, `DEFAULT 'a;b'` 就是语法层面的事故。
// 现在前端只说"哪一列变成什么样", 标识符引用、类型白名单、默认值/注释转义、方言分支都在后端。
//
// 一个诚实的限制: MySQL 系 DDL 会**隐式提交**, 所以整批里某条失败时, 前面的改动撤不回来
// —— 这不是"回滚失败"而是引擎能力。因此失败信息里明确说"前 N 条已生效", 不谎报 rolledBack。

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"

	gonaviDB "opscore/internal/dbmanager/gonavi/db"
	"opscore/internal/dbmanager/sync"
)

// 一次提交的列变更上限: 结构编辑器一次改几十列已经算多了, 再大就该走迁移工具。
const maxAlterCols = 100

type alterCol struct {
	Kind     string `json:"kind"` // add | drop | modify
	Name     string `json:"name"` // 目标列名(drop 时就是要删的列)
	OrigName string `json:"origName,omitempty"`
	Type     string `json:"type,omitempty"`
	Nullable bool   `json:"nullable"`
	Default  string `json:"default,omitempty"`
	Comment  string `json:"comment,omitempty"`
}

type applyAlterBody struct {
	ID       string     `json:"id"`
	Database string     `json:"database"`
	Table    string     `json:"table"`
	Cols     []alterCol `json:"cols"`
	Confirm  bool       `json:"confirm"`
}

// 列类型白名单式校验: 允许 类型名 + 可选(长度[,标度]) + 少量后缀词(int unsigned / timestamp with time zone),
// 引号、分号、注释符一律进不来。
var reColType = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]*(\s*\(\s*\d{1,10}\s*(,\s*\d{1,10}\s*)?\s*\))?(\s+[A-Za-z][A-Za-z0-9_]*){0,4}$`)

// 默认值里允许当**表达式**发的几种(其余一律按字符串字面量转义后发出):
// 数字 / NULL / 布尔 / 当前时间族。用户填 'abc' 会成 DEFAULT 'abc', 填 CURRENT_TIMESTAMP 会成 DEFAULT CURRENT_TIMESTAMP。
var reDefaultExpr = regexp.MustCompile(`(?i)^(NULL|TRUE|FALSE|-?\d+(\.\d+)?|CURRENT_(TIMESTAMP|DATE|TIME)(\(\d*\))?|SYSDATE|LOCALTIME(STAMP)?(\(\d*\))?)$`)

func safeColType(t string) error {
	t = strings.TrimSpace(t)
	if t == "" {
		return errors.New("列类型不能为空")
	}
	if !reColType.MatchString(t) {
		return errors.New("列类型不合法: " + t)
	}
	return nil
}

// defaultClause 生成 DEFAULT 片段(含前缀空格); 空字符串 = 不带默认值。
func defaultClause(engine, d string) string {
	d = strings.TrimSpace(d)
	if d == "" {
		return ""
	}
	if reDefaultExpr.MatchString(d) {
		return " DEFAULT " + d
	}
	return " DEFAULT " + gonaviDB.FormatLiteralForDialect(engine, d)
}

// buildAlterSQLs 按方言把列变更翻成语句列表。tableRef 已由调用方按方言引用好(db.table)。
func buildAlterSQLs(engine string, dialect sync.Dialect, tableRef string, cols []alterCol) ([]string, error) {
	qi := func(n string) string { return sync.QuoteIdent(n, dialect) }
	mysql := dialect == sync.DialectMySQL
	var out []string
	for i, c := range cols {
		if !validIdentifier(c.Name) {
			return nil, fmt.Errorf("第 %d 条列名非法: %s", i+1, c.Name)
		}
		switch c.Kind {
		case "drop":
			out = append(out, "ALTER TABLE "+tableRef+" DROP COLUMN "+qi(c.Name))
			continue
		case "add", "modify":
		default:
			return nil, fmt.Errorf("第 %d 条 kind 只支持 add/drop/modify", i+1)
		}
		if err := safeColType(c.Type); err != nil {
			return nil, fmt.Errorf("第 %d 条: %w", i+1, err)
		}
		typ := strings.TrimSpace(c.Type)
		def := defaultClause(engine, c.Default)
		nullPart := ""
		if !c.Nullable {
			nullPart = " NOT NULL"
		}
		cmt := strings.TrimSpace(c.Comment)

		if mysql {
			// MySQL 系: 一列一条完整定义(MODIFY/CHANGE 都会按新定义重建列)
			def0 := qi(c.Name) + " " + typ + nullPart + def
			if cmt != "" {
				def0 += " COMMENT " + gonaviDB.FormatLiteralForDialect(engine, cmt)
			}
			switch {
			case c.Kind == "add":
				out = append(out, "ALTER TABLE "+tableRef+" ADD COLUMN "+def0)
			case c.OrigName != "" && c.OrigName != c.Name:
				if !validIdentifier(c.OrigName) {
					return nil, fmt.Errorf("第 %d 条原列名非法: %s", i+1, c.OrigName)
				}
				// MariaDB 没有 RENAME COLUMN, CHANGE COLUMN 两边都认
				out = append(out, "ALTER TABLE "+tableRef+" CHANGE COLUMN "+qi(c.OrigName)+" "+def0)
			default:
				out = append(out, "ALTER TABLE "+tableRef+" MODIFY COLUMN "+def0)
			}
			continue
		}

		// 标准/PG 族: 一个属性一条语句, 顺序 = 改名 → 类型 → 可空 → 默认 → 注释
		if c.Kind == "add" {
			out = append(out, "ALTER TABLE "+tableRef+" ADD COLUMN "+qi(c.Name)+" "+typ+nullPart+def)
		} else {
			newName := c.Name
			if c.OrigName != "" && c.OrigName != newName {
				if !validIdentifier(c.OrigName) {
					return nil, fmt.Errorf("第 %d 条原列名非法: %s", i+1, c.OrigName)
				}
				out = append(out, "ALTER TABLE "+tableRef+" RENAME COLUMN "+qi(c.OrigName)+" TO "+qi(newName))
			}
			col := qi(newName)
			out = append(out, "ALTER TABLE "+tableRef+" ALTER COLUMN "+col+" TYPE "+typ)
			if c.Nullable {
				out = append(out, "ALTER TABLE "+tableRef+" ALTER COLUMN "+col+" DROP NOT NULL")
			} else {
				out = append(out, "ALTER TABLE "+tableRef+" ALTER COLUMN "+col+" SET NOT NULL")
			}
			if strings.TrimSpace(c.Default) != "" {
				out = append(out, "ALTER TABLE "+tableRef+" ALTER COLUMN "+col+" SET"+def)
			} else {
				out = append(out, "ALTER TABLE "+tableRef+" ALTER COLUMN "+col+" DROP DEFAULT")
			}
		}
		if cmt != "" {
			out = append(out, "COMMENT ON COLUMN "+tableRef+"."+qi(c.Name)+" IS "+gonaviDB.FormatLiteralForDialect(engine, cmt))
		}
	}
	if len(out) == 0 {
		return nil, errors.New("没有可执行的列变更")
	}
	return out, nil
}

func (h *Handlers) handleApplyAlter(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	var body applyAlterBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, "invalid body", http.StatusBadRequest)
		return
	}
	if len(body.Cols) == 0 {
		writeErr(w, "cols 为空", http.StatusBadRequest)
		return
	}
	if len(body.Cols) > maxAlterCols {
		writeErr(w, fmt.Sprintf("一次最多 %d 条列变更", maxAlterCols), http.StatusBadRequest)
		return
	}
	database, table, tableSchema, ok := validateTableTarget(w, body.ID, body.Database, body.Table)
	if !ok {
		return
	}
	conn, err := h.store.Get(body.ID)
	if err != nil {
		writeErr(w, "连接不存在: "+err.Error(), http.StatusBadRequest)
		return
	}
	db, engine, err := h.pool.AcquireForSync(body.ID)
	if err != nil {
		writeErr(w, "连接不可用: "+err.Error(), http.StatusBadRequest)
		return
	}
	dialect := sync.EngineDialect(engine)
	if dialect == "" {
		writeErr(w, "该引擎暂不支持在线结构变更", http.StatusBadRequest)
		return
	}
	qi := func(n string) string { return sync.QuoteIdent(n, dialect) }
	tableRef := qi(database) + "." + qi(table)
	if tableSchema != "" {
		tableRef = qi(tableSchema) + "." + qi(table)
	}
	sqls, berr := buildAlterSQLs(engine, dialect, tableRef, body.Cols)
	if berr != nil {
		writeErr(w, berr.Error(), http.StatusBadRequest)
		return
	}

	if !body.Confirm {
		writeJSON(w, map[string]any{"ok": true, "sqls": sqls, "needsConfirm": true})
		return
	}

	// 整批按最高风险过安全链(删列是 critical)
	risk, reason := RiskSafe, ""
	for _, s := range sqls {
		if rk, rs := classifySQLRisk(engine, s); rk.AtLeast(risk) && rk != risk {
			risk, reason = rk, rs
		}
	}
	joined := strings.Join(sqls, ";\n")
	if h.interceptWrite(w, conn, body.ID, joined, risk, reason, true) {
		return
	}

	total, failedAt, execErr := execAllInTx(r.Context(), db, sqls)
	if execErr != nil {
		// MySQL 系 DDL 隐式提交: 到失败点之前的语句已经落地, 说"已回滚"就是骗人
		rolledBack := failedAt == 0
		detail := execErr.Error()
		if failedAt > 0 {
			detail = fmt.Sprintf("第 %d 条失败: %v", failedAt, execErr)
			if sync.EngineDialect(string(conn.Info.Engine)) == sync.DialectMySQL && !errors.Is(execErr, errNoTxDriver) {
				detail += "（MySQL 系 DDL 会隐式提交, 前 " + fmt.Sprint(failedAt-1) + " 条已生效）"
			}
		}
		h.audit.Append(AuditEntry{
			ConnID: body.ID, ConnName: conn.Info.Name, Engine: string(conn.Info.Engine),
			SQL: joined, Risk: string(risk), Decision: "failed", Detail: detail,
		})
		writeJSON(w, map[string]any{"ok": false, "affected": 0, "failedAt": failedAt, "rolledBack": rolledBack, "error": detail})
		return
	}
	h.audit.Append(AuditEntry{
		ConnID: body.ID, ConnName: conn.Info.Name, Engine: string(conn.Info.Engine),
		SQL: joined, Risk: string(risk), Decision: "executed",
		Detail: fmt.Sprintf("%s（%d 条结构变更已在同一事务提交）", reason, len(sqls)),
	})
	writeJSON(w, map[string]any{"ok": true, "affected": total, "count": len(sqls)})
}
