package sync

import (
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// quoteValue 把流式读取到的单值内联为目标方言的 SQL 字面量。
// colBase 为目标列的基础类型(parseTypeName().base), 用于数字→boolean、字符串→bytea 等
// "驱动返回的 Go 类型 ≠ 目标列类型"场景。
func quoteValue(v any, dst Dialect, colBase string) string {
	switch t := v.(type) {
	case nil:
		return "NULL"
	case bool:
		if dst == DialectMySQL {
			if t {
				return "1"
			}
			return "0"
		}
		if t {
			return "TRUE"
		}
		return "FALSE"
	case int:
		return formatNumber(int64(t), dst, colBase)
	case int8:
		return formatNumber(int64(t), dst, colBase)
	case int16:
		return formatNumber(int64(t), dst, colBase)
	case int32:
		return formatNumber(int64(t), dst, colBase)
	case int64:
		return formatNumber(t, dst, colBase)
	case uint:
		return formatNumber(int64(t), dst, colBase)
	case uint8:
		return formatNumber(int64(t), dst, colBase)
	case uint16:
		return formatNumber(int64(t), dst, colBase)
	case uint32:
		return formatNumber(int64(t), dst, colBase)
	case uint64:
		return formatNumber(int64(t), dst, colBase)
	case float32:
		return strconv.FormatFloat(float64(t), 'g', -1, 32)
	case float64:
		return strconv.FormatFloat(t, 'g', -1, 64)
	case time.Time:
		return "'" + t.Format("2006-01-02 15:04:05.999999") + "'"
	case []byte:
		// 二进制: MySQL hex literal X'..' / PG hex 格式 '\x..'
		if len(t) == 0 {
			return "''"
		}
		return hexBlob(t, dst)
	case string:
		if isByteaBase(colBase) {
			// GoNavi 归一化把二进制 []byte 转成 "0x<hex>" 字符串; 时间/文本列不受影响。
			if h, ok := hexOfPrefixed(t, "0x"); ok {
				return hexBlobFromHex(h, dst)
			}
		}
		if isBoolBase(colBase) {
			// tinyint(1) → boolean: '0'/'1'/'true'/'false' 归一
			switch strings.ToLower(strings.TrimSpace(t)) {
			case "1", "true", "t", "yes":
				return boolLiteral(true, dst)
			case "0", "false", "f", "no", "":
				return boolLiteral(false, dst)
			}
		}
		// 驱动把 DATETIME/TIMESTAMP 归一为 RFC3339(Nano), 目标方言用空格分隔更稳。
		if s, ok := normalizeRFC3339(t); ok {
			return "'" + s + "'"
		}
		return "'" + escapeLiteral(t, dst) + "'"
	default:
		return "'" + escapeLiteral(fmt.Sprintf("%v", v), dst) + "'"
	}
}

func formatNumber(n int64, dst Dialect, colBase string) string {
	if isBoolBase(colBase) {
		return boolLiteral(n != 0, dst)
	}
	return strconv.FormatInt(n, 10)
}

func isBoolBase(base string) bool {
	return base == "boolean"
}

func isByteaBase(base string) bool {
	switch base {
	case "bytea", "blob", "tinyblob", "mediumblob", "longblob", "binary", "varbinary":
		return true
	}
	return false
}

func boolLiteral(b bool, dst Dialect) string {
	if dst == DialectMySQL {
		if b {
			return "1"
		}
		return "0"
	}
	if b {
		return "TRUE"
	}
	return "FALSE"
}

func hexBlob(b []byte, dst Dialect) string {
	return hexBlobFromHex(hex.EncodeToString(b), dst)
}

func hexBlobFromHex(h string, dst Dialect) string {
	if dst == DialectMySQL {
		return "X'" + h + "'"
	}
	return `'\x` + h + "'"
}

// hexOfPrefixed 识别 GoNavi 归一化的 "0x<hex>" 二进制字符串。
func hexOfPrefixed(s, prefix string) (string, bool) {
	l := strings.ToLower(strings.TrimSpace(s))
	if !strings.HasPrefix(l, prefix) || len(l) == len(prefix) {
		return "", false
	}
	h := l[len(prefix):]
	if len(h)%2 != 0 {
		return "", false
	}
	for _, c := range h {
		if !strings.ContainsRune("0123456789abcdef", c) {
			return "", false
		}
	}
	return h, true
}

// normalizeRFC3339 把驱动归一的 RFC3339 时间字符串转为 "2006-01-02 15:04:05.999999"。
// 非 RFC3339 形态原样返回(ok=false)。
func normalizeRFC3339(s string) (string, bool) {
	t := strings.TrimSpace(s)
	// 快速判定: 形如 YYYY-MM-DDTHH:...
	if len(t) < 19 || t[10] != 'T' || t[4] != '-' || t[7] != '-' {
		return "", false
	}
	if tt, err := time.Parse(time.RFC3339Nano, t); err == nil {
		return tt.Format("2006-01-02 15:04:05.999999"), true
	}
	return "", false
}

func quoteString(s string, dst Dialect) string {
	// 时间类字符串(驱动常以 string 返回)原样加引号即可; 其他字符串按方言转义。
	return "'" + escapeLiteral(s, dst) + "'"
}

// buildInsertSQL 生成一条 multi-row INSERT。colBases 为列名(小写)→目标基础类型。
func buildInsertSQL(schema, table string, columns []string, colBases map[string]string, rows [][]any, dst Dialect, batchBytes int) string {
	var b strings.Builder
	b.WriteString("INSERT INTO ")
	b.WriteString(qualifiedName(schema, table, dst))
	b.WriteString(" (")
	for i, c := range columns {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString(quoteIdent(c, dst))
	}
	b.WriteString(") VALUES ")
	for ri, row := range rows {
		if ri > 0 {
			b.WriteString(",\n")
		}
		b.WriteByte('(')
		for ci, v := range row {
			if ci > 0 {
				b.WriteByte(',')
			}
			base := ""
			if ci < len(columns) {
				base = colBases[strings.ToLower(columns[ci])]
			}
			b.WriteString(quoteValue(v, dst, base))
		}
		b.WriteByte(')')
		if batchBytes > 0 && b.Len() > batchBytes {
			break // 超长时截断本批(调用方按行数分批, 此处仅防御)
		}
	}
	return b.String()
}
