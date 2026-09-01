package db

import (
	"fmt"
	"strings"
)

const shardingSphereTableRulesQuery = "SHOW SHARDING TABLE RULES"

type tableMetadataQueryFunc func(string) ([]map[string]interface{}, []string, error)

func resolveShardingSphereLogicalTables(tables []string, query tableMetadataQueryFunc) []string {
	if len(tables) == 0 || query == nil || !hasNumericShardTableCandidates(tables) {
		return tables
	}

	rulesData, _, err := query(shardingSphereTableRulesQuery)
	if err != nil {
		return tables
	}
	return mergeShardingSphereLogicalTables(tables, rulesData)
}

func getCaseInsensitiveRowString(row map[string]interface{}, keys ...string) string {
	if len(row) == 0 {
		return ""
	}
	values := make(map[string]interface{}, len(row))
	for key, value := range row {
		values[strings.ToLower(key)] = value
	}
	for _, key := range keys {
		value, ok := values[strings.ToLower(key)]
		if !ok || value == nil {
			continue
		}
		text := strings.TrimSpace(fmt.Sprintf("%v", value))
		if text != "" && !strings.EqualFold(text, "<nil>") && !strings.EqualFold(text, "null") {
			return text
		}
	}
	return ""
}

func hasNumericShardTableCandidates(tables []string) bool {
	countByBase := make(map[string]int)
	for _, table := range tables {
		schema, name := splitQualifiedTableName(table)
		base := trimNumericShardSuffix(name)
		if base == "" || base == name {
			continue
		}
		key := strings.ToLower(schema + "." + base)
		countByBase[key]++
		if countByBase[key] > 1 {
			return true
		}
	}
	return false
}

func mergeShardingSphereLogicalTables(tables []string, rulesData []map[string]interface{}) []string {
	logicalTables := make([]string, 0, len(rulesData))
	for _, row := range rulesData {
		logical := getCaseInsensitiveRowString(row, "table", "table_name", "logic_table", "logical_table", "logical_table_name")
		if logical == "" {
			continue
		}
		logicalTables = append(logicalTables, logical)
	}
	if len(logicalTables) == 0 {
		return tables
	}

	result := make([]string, 0, len(tables))
	seen := make(map[string]struct{}, len(tables))
	add := func(table string) {
		if table == "" {
			return
		}
		key := strings.ToLower(table)
		if _, exists := seen[key]; exists {
			return
		}
		seen[key] = struct{}{}
		result = append(result, table)
	}

	for _, table := range tables {
		schema, name := splitQualifiedTableName(table)
		replacement := ""
		for _, logical := range logicalTables {
			logicalSchema, logicalName := splitQualifiedTableName(logical)
			if logicalName == "" {
				continue
			}
			if logicalSchema != "" && schema != "" && !strings.EqualFold(schema, logicalSchema) {
				continue
			}
			if strings.EqualFold(name, logicalName) || isPhysicalShardOfLogicalTable(name, logicalName) {
				if logicalSchema != "" {
					replacement = logical
				} else if schema != "" {
					replacement = fmt.Sprintf("%s.%s", schema, logicalName)
				} else {
					replacement = logicalName
				}
				break
			}
		}
		if replacement != "" {
			add(replacement)
			continue
		}
		add(table)
	}
	return result
}

func splitQualifiedTableName(table string) (string, string) {
	raw := strings.TrimSpace(table)
	if raw == "" {
		return "", ""
	}
	idx := strings.LastIndex(raw, ".")
	if idx <= 0 || idx >= len(raw)-1 {
		return "", raw
	}
	return strings.TrimSpace(raw[:idx]), strings.TrimSpace(raw[idx+1:])
}

func isPhysicalShardOfLogicalTable(name, logicalName string) bool {
	name = strings.TrimSpace(name)
	logicalName = strings.TrimSpace(logicalName)
	if name == "" || logicalName == "" {
		return false
	}
	if !strings.HasPrefix(strings.ToLower(name), strings.ToLower(logicalName)+"_") {
		return false
	}
	suffix := name[len(logicalName)+1:]
	if suffix == "" {
		return false
	}
	for _, r := range suffix {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func trimNumericShardSuffix(name string) string {
	trimmed := strings.TrimSpace(name)
	idx := strings.LastIndex(trimmed, "_")
	if idx <= 0 || idx >= len(trimmed)-1 {
		return trimmed
	}
	for _, r := range trimmed[idx+1:] {
		if r < '0' || r > '9' {
			return trimmed
		}
	}
	return trimmed[:idx]
}
