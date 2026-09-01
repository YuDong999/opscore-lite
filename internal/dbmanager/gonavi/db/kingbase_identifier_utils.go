package db

import (
	"regexp"
	"strings"
)

func normalizeKingbaseIdentCommon(raw string) string {
	value := strings.TrimSpace(raw)
	if value == "" {
		return ""
	}

	// 兼容被多次 JSON 序列化后的转义引号：
	// \\\"schema\\\" -> \"schema\" -> "schema"
	for i := 0; i < 8; i++ {
		next := strings.TrimSpace(value)
		next = strings.ReplaceAll(next, `\\\"`, `\"`)
		next = strings.ReplaceAll(next, `\"`, `"`)
		if next == value {
			break
		}
		value = next
	}
	value = strings.TrimSpace(value)

	stripWrapperOnce := func(text string) string {
		t := strings.TrimSpace(text)
		if strings.HasPrefix(t, `\`) && len(t) > 1 {
			t = strings.TrimSpace(strings.TrimPrefix(t, `\`))
		}
		if strings.HasSuffix(t, `\`) && len(t) > 1 {
			t = strings.TrimSpace(strings.TrimSuffix(t, `\`))
		}
		if len(t) >= 4 && strings.HasPrefix(t, `\"`) && strings.HasSuffix(t, `\"`) {
			return strings.TrimSpace(t[2 : len(t)-2])
		}
		if len(t) >= 2 && strings.HasPrefix(t, `"`) && strings.HasSuffix(t, `"`) {
			return strings.TrimSpace(t[1 : len(t)-1])
		}
		if len(t) >= 2 && strings.HasPrefix(t, "`") && strings.HasSuffix(t, "`") {
			return strings.TrimSpace(t[1 : len(t)-1])
		}
		if len(t) >= 2 && strings.HasPrefix(t, "[") && strings.HasSuffix(t, "]") {
			return strings.TrimSpace(t[1 : len(t)-1])
		}
		return t
	}

	for i := 0; i < 8; i++ {
		next := stripWrapperOnce(value)
		if next == value {
			break
		}
		value = next
	}
	value = strings.TrimSpace(value)

	// 兼容错误的二次引用与残留反斜杠。
	value = strings.ReplaceAll(value, `\"`, `"`)
	value = strings.ReplaceAll(value, `""`, "")
	value = strings.TrimSpace(value)

	for i := 0; i < 8; i++ {
		next := strings.TrimSpace(value)
		changed := false
		if strings.HasPrefix(next, `\`) && len(next) > 1 {
			next = strings.TrimSpace(strings.TrimPrefix(next, `\`))
			changed = true
		}
		if strings.HasSuffix(next, `\`) && len(next) > 1 {
			next = strings.TrimSpace(strings.TrimSuffix(next, `\`))
			changed = true
		}
		if !changed || next == value {
			break
		}
		value = next
	}

	return strings.TrimSpace(value)
}

// NormalizeKingbaseIdentifier removes nested client-side quoting from a Kingbase identifier.
func NormalizeKingbaseIdentifier(raw string) string {
	return normalizeKingbaseIdentCommon(raw)
}

func normalizeKingbaseIdentifier(raw string) string {
	return normalizeKingbaseIdentCommon(raw)
}

// QuoteKingbaseIdentifier quotes a Kingbase identifier only when the dialect requires it.
func QuoteKingbaseIdentifier(raw string) string {
	return quoteKingbaseIdent(raw)
}

// kingbaseIdentNeedsQuote 判断标识符是否需要双引号包裹。
// 与前端 sql.ts 中 needsQuote 逻辑保持一致。
func kingbaseIdentNeedsQuote(ident string) bool {
	if ident == "" {
		return false
	}
	// 不是合法裸标识符格式（必须以字母或下划线开头，仅含字母、数字、下划线）
	if matched, _ := regexp.MatchString(`^[a-zA-Z_][a-zA-Z0-9_]*$`, ident); !matched {
		return true
	}
	// 包含大写字母时需要引号保护（KingbaseES/PostgreSQL 默认将未加引号的标识符折叠为小写）
	for _, r := range ident {
		if r >= 'A' && r <= 'Z' {
			return true
		}
	}
	// 是 SQL 保留字
	return isKingbaseReservedWord(ident)
}

// isKingbaseReservedWord 检查是否为常见 SQL 保留字（简化版，与前端保持一致）。
func isKingbaseReservedWord(ident string) bool {
	switch strings.ToLower(ident) {
	case "select", "from", "where", "table", "index", "user", "order", "group", "by",
		"limit", "offset", "and", "or", "not", "null", "true", "false", "key",
		"primary", "foreign", "references", "default", "constraint",
		"create", "drop", "alter", "insert", "update", "delete", "set", "values", "into",
		"join", "left", "right", "inner", "outer", "on", "as", "is", "in", "like",
		"between", "case", "when", "then", "else", "end", "having", "distinct",
		"all", "any", "exists", "union", "except", "intersect",
		"column", "check", "unique", "with", "grant", "revoke", "trigger",
		"begin", "commit", "rollback", "schema", "database", "view", "function",
		"procedure", "sequence", "type", "domain", "role", "session", "current",
		"authorization", "cross", "full", "natural", "some", "cast", "fetch",
		"for", "to", "do", "if", "return", "returns", "declare", "cursor", "server", "owner":
		return true
	}
	return false
}

func quoteKingbaseIdent(name string) string {
	n := normalizeKingbaseIdentCommon(name)
	if n == "" {
		return "\"\""
	}
	if !kingbaseIdentNeedsQuote(n) {
		return n
	}
	n = strings.ReplaceAll(n, `"`, `""`)
	return `"` + n + `"`
}

// SplitKingbaseQualifiedName splits a Kingbase schema-qualified identifier safely.
func SplitKingbaseQualifiedName(raw string) (schema string, table string) {
	return splitKingbaseQualifiedNameCommon(raw)
}

// SplitSQLQualifiedName splits a schema-qualified SQL identifier without splitting dots inside quotes.
func SplitSQLQualifiedName(raw string) (schema string, table string) {
	return splitSQLQualifiedNameCommon(raw)
}

func splitKingbaseQualifiedNameCommon(raw string) (schema string, table string) {
	text := strings.TrimSpace(raw)
	if text == "" {
		return "", ""
	}

	sep := findKingbaseQualifiedSeparator(text)
	if sep < 0 {
		return "", normalizeKingbaseIdentCommon(text)
	}

	schemaPart := normalizeKingbaseIdentCommon(text[:sep])
	tablePart := normalizeKingbaseIdentCommon(text[sep+1:])

	if tablePart == "" {
		if schemaPart == "" {
			return "", normalizeKingbaseIdentCommon(text)
		}
		return "", schemaPart
	}
	if schemaPart == "" {
		return "", tablePart
	}
	return schemaPart, tablePart
}

func splitSQLQualifiedNameCommon(raw string) (schema string, table string) {
	text := normalizeSQLIdentifierEscapes(strings.TrimSpace(raw))
	if text == "" {
		return "", ""
	}

	sep := findSQLQualifiedSeparator(text)
	if sep < 0 {
		return "", normalizeSQLIdentPartCommon(text)
	}

	schemaPart := normalizeSQLIdentPartCommon(text[:sep])
	tablePart := normalizeSQLIdentPartCommon(text[sep+1:])

	if tablePart == "" {
		if schemaPart == "" {
			return "", normalizeSQLIdentPartCommon(text)
		}
		return "", schemaPart
	}
	if schemaPart == "" {
		return "", tablePart
	}
	return schemaPart, tablePart
}

func normalizeSQLIdentifierEscapes(raw string) string {
	value := strings.TrimSpace(raw)
	for i := 0; i < 4; i++ {
		next := strings.TrimSpace(value)
		next = strings.ReplaceAll(next, `\\\"`, `\"`)
		next = strings.ReplaceAll(next, `\"`, `"`)
		if next == value {
			break
		}
		value = next
	}
	return strings.TrimSpace(value)
}

func normalizeSQLIdentPartCommon(raw string) string {
	value := normalizeSQLIdentifierEscapes(strings.TrimSpace(raw))
	if value == "" {
		return ""
	}
	if len(value) >= 2 {
		first := value[0]
		last := value[len(value)-1]
		switch {
		case first == '"' && last == '"':
			return strings.TrimSpace(strings.ReplaceAll(value[1:len(value)-1], `""`, `"`))
		case first == '`' && last == '`':
			return strings.TrimSpace(strings.ReplaceAll(value[1:len(value)-1], "``", "`"))
		case first == '[' && last == ']':
			return strings.TrimSpace(strings.ReplaceAll(value[1:len(value)-1], "]]", "]"))
		}
	}
	return value
}

func findSQLQualifiedSeparator(raw string) int {
	inDouble := false
	inBacktick := false
	inBracket := false

	for i := 0; i < len(raw); i++ {
		ch := raw[i]

		if inDouble {
			if ch == '\\' && i+1 < len(raw) && raw[i+1] == '"' {
				inDouble = false
				i++
				continue
			}
			if ch == '"' {
				if i+1 < len(raw) && raw[i+1] == '"' {
					i++
					continue
				}
				inDouble = false
			}
			continue
		}

		if inBacktick {
			if ch == '`' {
				inBacktick = false
			}
			continue
		}

		if inBracket {
			if ch == ']' {
				inBracket = false
			}
			continue
		}

		switch ch {
		case '\\':
			if i+1 < len(raw) && raw[i+1] == '"' {
				inDouble = true
				i++
			}
		case '"':
			inDouble = true
		case '`':
			inBacktick = true
		case '[':
			inBracket = true
		case '.':
			return i
		}
	}

	return -1
}

func findKingbaseQualifiedSeparator(raw string) int {
	inDouble := false
	inBacktick := false
	inBracket := false
	escaped := false

	for i := 0; i < len(raw); i++ {
		ch := raw[i]
		if escaped {
			escaped = false
			continue
		}

		if ch == '\\' {
			escaped = true
			continue
		}

		if inDouble {
			if ch == '"' {
				// SQL 双引号转义："" 代表字面量 "
				if i+1 < len(raw) && raw[i+1] == '"' {
					i++
					continue
				}
				inDouble = false
			}
			continue
		}

		if inBacktick {
			if ch == '`' {
				inBacktick = false
			}
			continue
		}

		if inBracket {
			if ch == ']' {
				inBracket = false
			}
			continue
		}

		switch ch {
		case '"':
			inDouble = true
		case '`':
			inBacktick = true
		case '[':
			inBracket = true
		case '.':
			return i
		}
	}

	return -1
}

// buildKingbaseSearchPathCommon 统一构建 Kingbase search_path。
// 返回 search_path SQL 片段和规范化后的 schema 列表（用于调试/扩展）。
func buildKingbaseSearchPathCommon(rawSchemas []string) (string, []string) {
	if len(rawSchemas) == 0 {
		return "", nil
	}

	seen := make(map[string]struct{}, len(rawSchemas)+1)
	quotedParts := make([]string, 0, len(rawSchemas)+1)
	normalizedSchemas := make([]string, 0, len(rawSchemas)+1)

	appendSchema := func(raw string) {
		cleaned := normalizeKingbaseIdentCommon(raw)
		if cleaned == "" {
			return
		}
		if strings.EqualFold(cleaned, "public") {
			cleaned = "public"
		}
		key := strings.ToLower(cleaned)
		if _, ok := seen[key]; ok {
			return
		}
		seen[key] = struct{}{}
		normalizedSchemas = append(normalizedSchemas, cleaned)
		escaped := strings.ReplaceAll(cleaned, `"`, `""`)
		quotedParts = append(quotedParts, `"`+escaped+`"`)
	}

	for _, raw := range rawSchemas {
		appendSchema(raw)
	}
	if _, ok := seen["public"]; !ok {
		appendSchema("public")
	}

	if len(quotedParts) == 0 {
		return "", normalizedSchemas
	}
	return strings.Join(quotedParts, ", "), normalizedSchemas
}
