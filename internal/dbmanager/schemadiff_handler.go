package dbmanager

// 结构对比(schema diff)的两个端点:
//   POST /api/dbmanager/schema-diff        只读: 两侧元数据比对, 返回差异 + 建议语句
//   POST /api/dbmanager/schema-diff/apply  执行: 按用户勾选的 change key 重算差异, 只发选中那部分语句
//
// 为什么 apply 要**重算**而不是照前端传来的 SQL 执行:
//   1) 客户端传 SQL = 把"后端拼"的纪律让出去(见 apply-ddl / apply-alter 的由来);
//   2) 从看到比对结果到按下按钮这几秒里, 目标表可能已经被别人改过 —— 重算才是此刻的事实。
// 代价是 apply 前多读一次元数据, 换来"看到什么就等于执行什么"不会漂。
//
// v1 有意保守: 主键差异、跨方言族的类型差异只报告不生成语句(见 sync/schemadiff.go);
// 删表/删列/删索引一律 Destructive, 由前端默认不勾选 + 风险链二次确认兜住。
// 函数/视图/触发器/分区/权限不在 v1 覆盖面内。

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"sort"
	"strings"
	"sync"

	syncpkg "opscore/internal/dbmanager/sync"
)

// 一次比对的表数上限: 再多就该用同步工具做全库迁移, 不是"对比一下"的体量。
const maxDiffTables = 300

// 元数据读取并发上限。dbx 的经验值: 默认 6, MySQL 上表多时降到 2 —— 一次比对每表要发
// 3~4 条元数据查询, 打太满会把 information_schema 排成长队, 反而更慢(也把对方连接压疼)。
const diffWorkers = 6

func diffWorkersFor(engine string, tables int) int {
	w := diffWorkers
	if (engine == "mysql" || engine == "mariadb") && tables > 30 {
		w = 2
	}
	if w < 1 {
		return 1
	}
	return w
}

type schemaDiffEnd struct {
	ID       string `json:"id"`
	Database string `json:"database"`
	Schema   string `json:"schema,omitempty"` // PG 族的命名空间; MySQL 留空
}

// qualified 驱动侧的表名: 有模式时要 "schema.table"(驱动的 GetColumns 认这个形式)。
func (e schemaDiffEnd) qualified(t string) string {
	if e.Schema != "" {
		return e.Schema + "." + t
	}
	return t
}

func (e schemaDiffEnd) namespace() string {
	if e.Schema != "" {
		return e.Schema
	}
	return e.Database
}

// diffSide 一侧的引擎信息 + 全部表快照
type diffSide struct {
	engine    string
	dialect   syncpkg.Dialect
	namespace string // 建表/引用用的库名或模式名
	snaps     map[string]syncpkg.TableSnapshot
	tableRef  func(string) string
}

type schemaDiffBody struct {
	Src     schemaDiffEnd       `json:"src"`
	Dst     schemaDiffEnd       `json:"dst"`
	Tables  []string            `json:"tables,omitempty"` // 空=两侧表名并集
	Options syncpkg.DiffOptions `json:"options"`
	Keys    []string            `json:"keys,omitempty"` // apply 用: 选中的 change key
	// Sqls 是「人改过预览 SQL」时的覆盖批次。它不新增信任面 —— 这个模块的查询页本来就能执行任意 SQL,
	// 差别只是语句从哪来。但风险链/写锁/审计一步都不能少(见 apply 里两条路径共用同一段安全检查)。
	// 空 = 没编辑, 走"按 key 重算并取选中语句"。
	Sqls    []string `json:"sqls,omitempty"`
	Confirm bool     `json:"confirm"`
}

func (h *Handlers) loadDiffSide(end schemaDiffEnd, want []string) (*diffSide, error) {
	if end.ID == "" || strings.TrimSpace(end.Database) == "" {
		return nil, fmt.Errorf("缺少连接或库名")
	}
	if _, err := h.store.Get(end.ID); err != nil {
		return nil, fmt.Errorf("连接不存在: %w", err)
	}
	db, engine, err := h.pool.AcquireForSync(end.ID)
	if err != nil {
		return nil, fmt.Errorf("连接不可用: %w", err)
	}
	dialect := syncpkg.EngineDialect(engine)
	if dialect == "" {
		return nil, fmt.Errorf("该引擎暂不支持结构对比(v1 覆盖 MySQL 系与 PG 系)")
	}
	names := want
	if len(names) == 0 {
		got, terr := db.GetTables(end.Database)
		if terr != nil {
			return nil, fmt.Errorf("取表清单失败: %w", terr)
		}
		names = got
	}
	if len(names) > maxDiffTables {
		return nil, fmt.Errorf("一次最多对比 %d 张表(当前 %d 张), 请勾选部分表", maxDiffTables, len(names))
	}
	qi := func(n string) string { return syncpkg.QuoteIdent(n, dialect) }
	ns := end.namespace()
	si := &diffSide{engine: engine, dialect: dialect, namespace: ns, snaps: map[string]syncpkg.TableSnapshot{},
		tableRef: func(t string) string { return qi(ns) + "." + qi(t) }}

	var mu sync.Mutex
	var wg sync.WaitGroup
	sem := make(chan struct{}, diffWorkersFor(engine, len(names)))
	for _, t := range names {
		wg.Add(1)
		go func(t string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			cols, cerr := db.GetColumns(ns2db(end), end.qualified(t))
			if cerr != nil {
				// 单表读不到不终止整次比对: 不存快照, 由 diff 侧按"该侧没有这张表"处理
				log.Printf("[schema-diff] 读 %s.%s 元数据失败: %v", end.Database, t, err)
				return
			}
			if len(cols) == 0 {
				// **零列 = 这张表在这一侧不存在**, 不能当"一张空表"存下来:
				// 驱动对不存在的表往往不报错只回空(实测 PG), 当成空表就会把"该建表"误判成"逐列 ADD"。
				log.Printf("[schema-diff] %s.%s 读到 0 列, 按不存在处理", end.Database, t)
				return
			}
			idxs, _ := db.GetIndexes(ns2db(end), end.qualified(t))
			fks, _ := db.GetForeignKeys(ns2db(end), end.qualified(t))
			mu.Lock()
			si.snaps[t] = syncpkg.TableSnapshot{Table: t, Columns: cols, Indexes: idxs, FKs: fks}
			mu.Unlock()
		}(t)
	}
	wg.Wait()
	return si, nil
}

// ns2db: 驱动的元数据接口按"库名"定位, 模式是拼在表名里的(schema.table)。
func ns2db(end schemaDiffEnd) string { return end.Database }

// diffSides 计算两侧差异(两个端点共用同一段, 保证"预览=执行时看到的")
func diffSides(src, dst *diffSide, tables []string, opt syncpkg.DiffOptions) ([]syncpkg.TableDiff, []string) {
	seen := map[string]bool{}
	var names []string
	for _, t := range tables {
		if t != "" && !seen[t] {
			seen[t] = true
			names = append(names, t)
		}
	}
	if len(names) == 0 {
		for t := range src.snaps {
			names = append(names, t)
		}
		for t := range dst.snaps {
			if _, ok := src.snaps[t]; !ok {
				names = append(names, t)
			}
		}
		sort.Strings(names)
	}
	var notes []string
	out := make([]syncpkg.TableDiff, 0, len(names))
	for _, t := range names {
		s, sok := src.snaps[t]
		d, dok := dst.snaps[t]
		switch {
		case sok && !dok:
			out = append(out, syncpkg.CreateOnlyDiff(s, src.dialect, dst.dialect, dst.engine, dst.namespace, t))
		case !sok && dok:
			out = append(out, syncpkg.DropOnlyDiff(d, dst.dialect, dst.tableRef(t)))
		default:
			td := syncpkg.DiffTable(s, d, src.dialect, dst.dialect, dst.engine, dst.tableRef(t), opt)
			for _, n := range td.Notes {
				notes = append(notes, t+": "+n)
			}
			if td.Action != "none" {
				out = append(out, td)
			}
		}
	}
	return out, notes
}

func (h *Handlers) handleSchemaDiff(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	var body schemaDiffBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, "invalid body", http.StatusBadRequest)
		return
	}
	src, dst, diffs, notes, err := h.runDiff(body)
	if err != nil {
		writeJSON(w, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	summary := map[string]int{"create": 0, "modify": 0, "delete": 0, "changes": 0, "destructive": 0}
	for _, td := range diffs {
		summary[td.Action]++
		for _, ch := range td.Changes {
			summary["changes"]++
			if ch.Destructive {
				summary["destructive"]++
			}
		}
	}
	writeJSON(w, map[string]any{
		"ok": true, "tables": diffs, "notes": notes, "summary": summary,
		"src": src.describeSide(), "dst": dst.describeSide(),
	})
}

func (h *Handlers) runDiff(body schemaDiffBody) (*diffSide, *diffSide, []syncpkg.TableDiff, []string, error) {
	src, err := h.loadDiffSide(body.Src, body.Tables)
	if err != nil {
		return nil, nil, nil, nil, fmt.Errorf("源侧: %w", err)
	}
	dst, err := h.loadDiffSide(body.Dst, body.Tables)
	if err != nil {
		return nil, nil, nil, nil, fmt.Errorf("目标侧: %w", err)
	}
	diffs, notes := diffSides(src, dst, body.Tables, body.Options)
	return src, dst, diffs, notes, nil
}

func (d *diffSide) describeSide() string { return d.engine }

func (h *Handlers) handleSchemaDiffApply(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	var body schemaDiffBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, "invalid body", http.StatusBadRequest)
		return
	}
	if len(body.Keys) == 0 && len(body.Sqls) == 0 {
		writeErr(w, "没有勾选任何变更", http.StatusBadRequest)
		return
	}
	if !body.Confirm {
		writeErr(w, "需要 confirm=true 才执行(预览请走 /schema-diff)", http.StatusBadRequest)
		return
	}
	conn, err := h.store.Get(body.Dst.ID)
	if err != nil {
		writeErr(w, "目标连接不存在: "+err.Error(), http.StatusBadRequest)
		return
	}
	db, engine, err := h.pool.AcquireForSync(body.Dst.ID)
	if err != nil {
		writeErr(w, "目标连接不可用: "+err.Error(), http.StatusBadRequest)
		return
	}
	// 语句来源两条路:
	//   默认 —— 按 key 从"此刻重算"的差异里取, 保证看到什么就执行什么, 差异变了会报 stale 而不是少做;
	//   编辑 —— 用户在预览里改过文本, 这时以文本为准(不再比 key), 但下面的风险链/写锁/审计一步不省。
	var sqls []string
	edited := len(body.Sqls) > 0
	if edited {
		for _, raw := range body.Sqls {
			if q := strings.TrimRight(strings.TrimSpace(raw), ";"); q != "" {
				sqls = append(sqls, q)
			}
		}
	} else {
		_, _, diffs, _, derr := h.runDiff(body)
		if derr != nil {
			writeJSON(w, map[string]any{"ok": false, "error": derr.Error()})
			return
		}
		want := map[string]bool{}
		for _, k := range body.Keys {
			want[k] = true
		}
		matched := map[string]bool{}
		for _, td := range diffs {
			for _, ch := range td.Changes {
				if !want[ch.Key] {
					continue
				}
				matched[ch.Key] = true
				sqls = append(sqls, ch.Sqls...)
			}
		}
		var missing []string
		for k := range want {
			if !matched[k] {
				missing = append(missing, k)
			}
		}
		if len(missing) > 0 {
			sort.Strings(missing)
			writeJSON(w, map[string]any{"ok": false, "stale": true, "unmatched": missing,
				"error": "有 " + fmt.Sprint(len(missing)) + " 项在目标库上已经不存在差异或定义变了(两侧已重新比对), 请刷新后重试"})
			return
		}
	}
	if len(sqls) == 0 {
		writeJSON(w, map[string]any{"ok": true, "noop": true, "message": "选中项已经没有差异, 未执行任何语句"})
		return
	}

	risk, reason := RiskSafe, ""
	for _, sq := range sqls {
		if rk, rs := classifySQLRisk(engine, sq); rk.AtLeast(risk) && rk != risk {
			risk, reason = rk, rs
		}
	}
	joined := strings.Join(sqls, ";\n")
	if h.interceptWrite(w, conn, body.Dst.ID, joined, risk, reason, true) {
		return
	}
	total, failedAt, execErr := execAllInTx(r.Context(), db, sqls)
	if execErr != nil {
		detail := execErr.Error()
		if failedAt > 0 {
			detail = fmt.Sprintf("第 %d 条失败: %v", failedAt, execErr)
			if syncpkg.EngineDialect(string(conn.Info.Engine)) == syncpkg.DialectMySQL && !errors.Is(execErr, errNoTxDriver) {
				detail += "（MySQL 系 DDL 隐式提交, 前 " + fmt.Sprint(failedAt-1) + " 条已生效）"
			}
		}
		h.audit.Append(AuditEntry{
			ConnID: body.Dst.ID, ConnName: conn.Info.Name, Engine: string(conn.Info.Engine),
			SQL: joined, Risk: string(risk), Decision: "failed", Detail: detail,
		})
		writeJSON(w, map[string]any{"ok": false, "affected": 0, "failedAt": failedAt,
			"rolledBack": failedAt == 0, "error": detail})
		return
	}
	h.audit.Append(AuditEntry{
		ConnID: body.Dst.ID, ConnName: conn.Info.Name, Engine: string(conn.Info.Engine),
		SQL: joined, Risk: string(risk), Decision: "executed",
		Detail: fmt.Sprintf("%s（结构对比: %d 条语句已在同一事务提交%s）", reason, len(sqls),
			map[bool]string{true: ", 为用户编辑后的文本"}[edited]),
	})
	writeJSON(w, map[string]any{"ok": true, "affected": total, "count": len(sqls)})
}
