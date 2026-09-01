package sync

import (
	"fmt"
	"strconv"
	"strings"

	gonaviConnection "opscore/internal/dbmanager/gonavi/connection"
)

// EngineDialect 把 dbmanager 引擎类型归一为方言族。
// MySQL 兼容族与 PostgreSQL 兼容族之外的引擎返回 "" (Phase 1 不支持同步)。
func EngineDialect(engine string) Dialect {
	switch engine {
	case "mysql", "mysql_agent", "mariadb", "goldendb":
		return DialectMySQL
	case "postgres", "opengauss", "kingbase", "highgo", "vastbase", "gaussdb":
		return DialectPostgres
	}
	return ""
}

// parsedType 解析形如 varchar(64) / decimal(10,2) / int unsigned 的类型名。
type parsedType struct {
	base  string // 小写基础类型
	len   int    // 长度/精度, 无则 -1
	scale int    // 小数位, 无则 -1
	unsigned bool
}

func parseTypeName(raw string) parsedType {
	t := strings.ToLower(strings.TrimSpace(raw))
	t = strings.TrimPrefix(t, "national ")
	if i := strings.Index(t, "("); i >= 0 {
		base := strings.TrimSpace(t[:i])
		inner := t[i+1:]
		if j := strings.LastIndex(inner, ")"); j >= 0 {
			inner = inner[:j]
		}
		parts := strings.Split(inner, ",")
		l, err := strconv.Atoi(strings.TrimSpace(parts[0]))
		if err != nil {
			l = -1
		}
		s := -1
		if len(parts) > 1 {
			if v, err := strconv.Atoi(strings.TrimSpace(parts[1])); err == nil {
				s = v
			}
		}
		return parsedType{base: base, len: l, scale: s, unsigned: strings.Contains(t, "unsigned")}
	}
	return parsedType{base: strings.TrimSpace(strings.TrimSuffix(t, " unsigned")), len: -1, scale: -1, unsigned: strings.Contains(t, "unsigned")}
}

// mapMySQLToPG MySQL 类型 → PostgreSQL 类型。
// 返回目标类型文本与备注。
func mapMySQLToPG(src parsedType) (dst string, note string) {
	switch src.base {
	case "tinyint":
		if src.len == 1 && !src.unsigned {
			return "boolean", "tinyint(1) → boolean"
		}
		if src.unsigned {
			return "smallint", "tinyint unsigned → smallint"
		}
		return "smallint", ""
	case "smallint":
		if src.unsigned {
			return "integer", "smallint unsigned → integer"
		}
		return "smallint", ""
	case "mediumint":
		if src.unsigned {
			return "bigint", "mediumint unsigned → bigint"
		}
		return "integer", ""
	case "int", "integer":
		if src.unsigned {
			return "bigint", "int unsigned → bigint"
		}
		return "integer", ""
	case "bigint":
		if src.unsigned {
			// bigint unsigned 上限超 PG bigint, 超范围值需 numeric
			return "numeric(20,0)", "bigint unsigned → numeric(20,0)"
		}
		return "bigint", ""
	case "float":
		return "real", ""
	case "double", "real":
		return "double precision", ""
	case "decimal", "numeric":
		if src.len > 0 {
			return fmt.Sprintf("numeric(%d,%d)", src.len, maxInt(src.scale, 0)), ""
		}
		return "numeric", ""
	case "bit":
		if src.len == 1 {
			return "boolean", "bit(1) → boolean"
		}
		return "bit", "bit(n) 数据需按位串校验"
	case "boolean", "bool":
		return "boolean", ""
	case "char":
		if src.len > 0 {
			return fmt.Sprintf("char(%d)", src.len), ""
		}
		return "char", ""
	case "varchar":
		if src.len > 0 {
			return fmt.Sprintf("varchar(%d)", src.len), ""
		}
		return "varchar", ""
	case "tinytext", "text", "mediumtext", "longtext":
		return "text", ""
	case "binary", "varbinary":
		return "bytea", ""
	case "tinyblob", "blob", "mediumblob", "longblob":
		return "bytea", ""
	case "date":
		return "date", ""
	case "time":
		return "time without time zone", ""
	case "datetime":
		// fsp(小数秒精度)被解析进 len
		if src.len > 0 {
			return fmt.Sprintf("timestamp(%d) without time zone", src.len), ""
		}
		return "timestamp without time zone", ""
	case "timestamp":
		if src.len > 0 {
			return fmt.Sprintf("timestamp(%d) with time zone", src.len), ""
		}
		return "timestamp with time zone", ""
	case "year":
		return "smallint", "year → smallint"
	case "json":
		return "jsonb", "json → jsonb"
	case "enum":
		return "varchar(255)", "enum → varchar(保留原值字符串)"
	case "set":
		return "text", "set → text(逗号分隔原样存储)"
	case "geometry", "point", "linestring", "polygon", "multipoint", "multilinestring", "multipolygon", "geomcollection", "geometrycollection":
		return "text", "空间类型 → text(WKT 文本, 不做空间校验)"
	default:
		return "text", "未识别类型 " + src.base + " → text"
	}
}

// mapPGToMySQL PostgreSQL 类型 → MySQL 类型。
func mapPGToMySQL(src parsedType) (dst string, note string) {
	switch src.base {
	case "smallint", "int2":
		return "smallint", ""
	case "integer", "int", "int4":
		return "int", ""
	case "bigint", "int8":
		return "bigint", ""
	case "real", "float4":
		return "float", ""
	case "double precision", "float8":
		return "double", ""
	case "numeric", "decimal":
		if src.len > 0 {
			return fmt.Sprintf("decimal(%d,%d)", src.len, maxInt(src.scale, 0)), ""
		}
		return "decimal(65,10)", "numeric 未指定精度 → decimal(65,10)"
	case "money":
		return "decimal(19,4)", "money → decimal(19,4)"
	case "boolean", "bool":
		return "tinyint(1)", "boolean → tinyint(1)"
	case "bit", "varbit":
		return "binary(8)", "bit → binary(8)"
	case "bytea":
		return "blob", "bytea → blob"
	case "char", "bpchar":
		if src.len > 0 {
			return fmt.Sprintf("char(%d)", src.len), ""
		}
		return "char(1)", ""
	case "varchar", "character varying":
		if src.len > 0 {
			return fmt.Sprintf("varchar(%d)", src.len), ""
		}
		return "text", "varchar 无长度 → text"
	case "text", "citext":
		return "text", ""
	case "name":
		return "varchar(63)", ""
	case "date":
		return "date", ""
	case "time", "time without time zone":
		return "time", ""
	case "time with time zone", "timetz":
		return "time", "time with time zone → time(丢时区)"
	case "timestamp", "timestamp without time zone":
		if src.scale > 0 {
			return fmt.Sprintf("datetime(%d)", minInt(src.scale, 6)), ""
		}
		return "datetime", ""
	case "timestamptz", "timestamp with time zone":
		return "datetime(6)", "timestamptz → datetime(6)(MySQL 无时区, 存 UTC 需应用层约定)"
	case "interval":
		return "varchar(64)", "interval → varchar(64)(ISO 8601 文本)"
	case "json", "jsonb":
		return "json", ""
	case "uuid":
		return "char(36)", "uuid → char(36)"
	case "inet", "cidr", "macaddr", "macaddr8":
		return "varchar(53)", "网络地址 → varchar"
	case "xml":
		return "text", ""
	case "array", "_text", "_int4", "_int8", "_numeric", "_varchar", "_bytea", "_jsonb", "_uuid", "_timestamptz":
		return "json", "数组 → json"
	default:
		return "text", "未识别类型 " + src.base + " → text"
	}
}

// mapColumn 把源列定义映射为目标列定义文本(含 NOT NULL / DEFAULT / 说明)。
// src 展示为源方言的原始定义。失败时返回不可迁移标记。
func mapColumn(col gonaviConnection.ColumnDefinition, srcDialect, dstDialect Dialect) ColumnMapping {
	out := ColumnMapping{
		Name:     col.Name,
		Nullable: strings.EqualFold(col.Nullable, "YES"),
		Default:  "",
		Comment:  col.Comment,
		IsPK:     strings.EqualFold(col.Key, "PRI"),
	}
	if col.Default != nil {
		out.Default = *col.Default
	}
	out.Source = col.Type
	if !out.Nullable {
		out.Source += " NOT NULL"
	}

	pt := parseTypeName(col.Type)
	extra := strings.ToLower(col.Extra)
	// 注意: MySQL 的 "DEFAULT_GENERATED" 仅表示字面 DEFAULT 表达式, 不是自增;
	// 生成列的 "stored/virtual generated" 也不是。只认明确的自增标记。
	out.AutoIncr = strings.Contains(extra, "auto_increment") ||
		strings.Contains(extra, "identity") ||
		strings.Contains(extra, "nextval(")
	var dst string
	var note string
	switch {
	case srcDialect == DialectMySQL && dstDialect == DialectPostgres:
		dst, note = mapMySQLToPG(pt)
	case srcDialect == DialectPostgres && dstDialect == DialectMySQL:
		dst, note = mapPGToMySQL(pt)
	case srcDialect == dstDialect:
		dst = normalizeSameType(pt, dstDialect)
		note = "同方言直迁"
	default:
		dst = "text"
		note = "未注册方言对, 降级 text"
	}
	out.Note = note
	out.Target = dst
	if !out.Nullable {
		out.Target += " NOT NULL"
	}
	if out.AutoIncr {
		out.Target = strings.TrimSuffix(out.Target, " NOT NULL") // 自增列不加 NOT NULL(由 DDL 生成器处理)
		out.Target = stripAutoDefault(out.Target)                 // PG/MySQL 自增列不带字面 DEFAULT
	}
	return out
}

// stripAutoDefault 去掉自增列的字面默认值(如 nextval(...) / 0)。
func stripAutoDefault(t string) string {
	if i := strings.Index(t, " DEFAULT "); i > 0 {
		return strings.TrimSpace(t[:i])
	}
	return t
}

// normalizeSameType 同方言迁移时轻度规范化(剥 charset/unsigned 展示差异)。
func normalizeSameType(pt parsedType, d Dialect) string {
	if d == DialectMySQL {
		switch pt.base {
		case "int", "integer":
			return "int"
		case "tinytext":
			return "text"
		case "tinyblob":
			return "blob"
		case "datetime", "timestamp":
			if pt.scale > 0 {
				return fmt.Sprintf("%s(%d)", pt.base, pt.scale)
			}
			return pt.base
		}
	} else {
		switch pt.base {
		case "int4":
			return "integer"
		case "int8":
			return "bigint"
		case "bpchar":
			if pt.len > 0 {
				return fmt.Sprintf("char(%d)", pt.len)
			}
			return "char"
		case "timestamptz":
			return "timestamp with time zone"
		}
	}
	if pt.len > 0 && pt.scale > 0 {
		return fmt.Sprintf("%s(%d,%d)", pt.base, pt.len, pt.scale)
	}
	if pt.len > 0 {
		return fmt.Sprintf("%s(%d)", pt.base, pt.len)
	}
	return pt.base
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
