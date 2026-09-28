// 内置 SQL 导出(备份的第二个数据源): 生成 DDL + INSERT 并写进备份文件。
//
// 它与"跨库同步"用的是同一批零件 —— GenerateCreateDDL(建表, 含列注释)、QuoteValue
// (方言感知转义, 二进制走 hex/bytea)。所以这里不重写转义, 只负责编排与分批。
//
// **保真度如实告知**: 不含触发器/例程/权限/序列当前值 —— 这些在 BackupRecord.Excludes 里
// 明写。这里不假装能导全。
package dbmanager

import (
	"context"
	"fmt"
	"strings"
	"time"

	gonaviConnection "opscore/internal/dbmanager/gonavi/connection"
	gonavibase "opscore/internal/dbmanager/gonavi/db"
	syncpkg "opscore/internal/dbmanager/sync"
)

// builtinRowsPerBatch 每个 INSERT 语句塞多少行。太大会撞 max_allowed_packet,
// 太小则语句数爆炸。500 是常见 dump 工具的量级。
const builtinRowsPerBatch = 500

// syncDialectOf 取引擎对应方言(空 = 该引擎不支持内置导出)。
func syncDialectOf(engine string) syncpkg.Dialect { return syncpkg.EngineDialect(engine) }

// emitBuiltinDump 生成整库(或指定表)的 SQL 并写入 w。
func (h *Handlers) emitBuiltinDump(ctx context.Context, db gonavibase.Database, engine string, dialect syncpkg.Dialect, database string, tables []string, w *targetWriter) error {
	write := func(s string) error {
		_, err := w.Write([]byte(s))
		return err
	}

	header := fmt.Sprintf(
		"-- OpsCore 内置导出(非原生工具)\n"+
			"-- 数据库: %s   引擎: %s   时间: %s\n"+
			"-- 一致性: 单事务 REPEATABLE READ\n"+
			"-- 注意: 本导出不含 触发器 / 存储过程 / 函数 / 事件 / 权限(GRANT) / 序列当前值。\n"+
			"--   需要完整恢复请在装有 mysqldump / pg_dump 的机器上使用原生备份。\n\n",
		database, engine, time.Now().Format("2006-01-02 15:04:05"))
	if err := write(header); err != nil {
		return err
	}
	// 导出的库在回放时需要存在
	if isMySQLFamily(engine) {
		if err := write(fmt.Sprintf("CREATE DATABASE IF NOT EXISTS %s;\nUSE %s;\n\n",
			syncpkg.QuoteIdent(database, dialect), syncpkg.QuoteIdent(database, dialect))); err != nil {
			return err
		}
	}

	for _, table := range tables {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("已取消")
		}
		// 表结构
		cols, err := db.GetColumns(database, table)
		if err != nil {
			return fmt.Errorf("读表结构失败 %s: %w", table, err)
		}
		idxs, _ := db.GetIndexes(database, table)
		// GenerateCreateDDL 是本仓唯一的建表语句生成器(含列注释、索引), 与跨库同步共用 ——
		// 内置导出不另写一套 DDL, 否则"同步能建对、备份建不对"这种漂移迟早出现。
		ddl, _, commentDDL, _ := syncpkg.GenerateCreateDDL(database, table, cols, idxs, dialect, dialect)
		if err := write("--\n-- 表结构: " + table + "\n--\nDROP TABLE IF EXISTS " +
			syncpkg.QuoteIdent(table, dialect) + ";\n" + ddl + "\n"); err != nil {
			return err
		}
		for _, c := range commentDDL {
			if err := write(c + "\n"); err != nil {
				return err
			}
		}
		if err := write("\n"); err != nil {
			return err
		}

		// 数据: 分批 SELECT + INSERT
		if err := h.emitTableData(ctx, db, engine, dialect, database, table, cols, w); err != nil {
			return err
		}
	}
	return write("\n-- 导出结束\n")
}

// emitTableData 把一张表的数据按批导出成 INSERT。
func (h *Handlers) emitTableData(ctx context.Context, db gonavibase.Database, engine string, dialect syncpkg.Dialect, database, table string,
	cols []gonaviConnection.ColumnDefinition, w *targetWriter) error {
	colNames := make([]string, 0, len(cols))
	colBases := make(map[string]string, len(cols))
	for _, c := range cols {
		colNames = append(colNames, c.Name)
		if base := columnBaseOf(c.Type); base != "" {
			colBases[strings.ToLower(c.Name)] = base
		}
	}
	tn := syncpkg.QuoteIdent(database, dialect) + "." + syncpkg.QuoteIdent(table, dialect)

	stmt := "SELECT * FROM " + tn
	buf := strings.Builder{}
	buf.WriteString("--\n-- 数据: " + table + "\n--\n")

	// 这里用不截断的查询出口: 内置导出是**产出数据**, 拿预览截断值就是把备份写坏
	// (与 /export、/table-tools 同一条口径, 见 QueryRowsUnbounded 的注释)。
	rows, _, err := syncpkg.QueryRowsUnbounded(ctx, db, stmt)
	if err != nil {
		return fmt.Errorf("读数据失败 %s: %w", table, err)
	}
	if len(rows) == 0 {
		if _, err := w.Write([]byte(buf.String())); err != nil {
			return err
		}
		return nil
	}

	for start := 0; start < len(rows); start += builtinRowsPerBatch {
		end := start + builtinRowsPerBatch
		if end > len(rows) {
			end = len(rows)
		}
		buf.Reset()
		buf.WriteString("INSERT INTO " + tn + " (")
		for i, c := range colNames {
			if i > 0 {
				buf.WriteString(", ")
			}
			buf.WriteString(syncpkg.QuoteIdent(c, dialect))
		}
		buf.WriteString(") VALUES\n")
		for ri, row := range rows[start:end] {
			if ri > 0 {
				buf.WriteString(",\n")
			}
			buf.WriteString("(")
			for ci, c := range colNames {
				if ci > 0 {
					buf.WriteString(", ")
				}
				buf.WriteString(syncpkg.QuoteValue(row[c], dialect, colBases[strings.ToLower(c)]))
			}
			buf.WriteString(")")
		}
		buf.WriteString(";\n")
		if _, err := w.Write([]byte(buf.String())); err != nil {
			return err
		}
	}
	return nil
}

// columnBaseOf 从列类型串里取"基础类型"(给 QuoteValue 判 boolean/bytea 用)。
// 与 data_handler.go 里复制为 INSERT 的判定保持一致。
func columnBaseOf(t string) string {
	lt := strings.ToLower(t)
	switch {
	case strings.Contains(lt, "tinyint(1)"), strings.Contains(lt, "boolean"), strings.Contains(lt, "bool"):
		return "boolean"
	case strings.Contains(lt, "blob"), strings.Contains(lt, "binary"), strings.Contains(lt, "bytea"):
		return "bytea"
	}
	return ""
}
