// sql_editability 规则：判断结果集是否可编辑（dbx 的 sql_editability.rs 逻辑复现）。
// 规则：单表 + 含主键 + 无聚合/分组/排序/窗口函数 → 可编辑；否则只读。
// Phase 1 先实现核心判定，后续可扩展为配置化。
package dbmanager

import (
	"fmt"
	"strings"
)

// IsEditableResult 判定查询结果是否可编辑。
func IsEditableResult(sqlText string, columns []ColumnInfo, rows [][]any) bool {
	if len(rows) == 0 {
		return false // 空结果集不编辑
	}

	// 1. 检查是否为单表（无 JOIN/UNION）
	if !isSingleTableQuery(sqlText) {
		return false
	}

	// 2. 检查是否有主键（至少一个主键列）
	if !hasPrimaryKey(columns) {
		return false
	}

	// 3. 检查是否有聚合/分组/排序/窗口函数
	if hasAggregation(sqlText) || hasGroupBy(sqlText) || hasOrderBy(sqlText) || hasWindowFunction(sqlText) {
		return false
	}

	// 4. 检查是否为 SELECT *（SELECT * 可编辑，指定列需谨慎）
	if isSelectStar(sqlText) {
		return false
	}

	return true
}

// isSingleTableQuery 简单判定是否为单表查询（无 JOIN/UNION）。
func isSingleTableQuery(sqlText string) bool {
	clean := strings.ToUpper(strings.TrimSpace(sqlText))
	// 排除 JOIN/UNION/INTERSECT/EXCEPT
	if strings.Contains(clean, " JOIN ") || strings.Contains(clean, " UNION ") ||
		strings.Contains(clean, " INTERSECT ") || strings.Contains(clean, " EXCEPT ") {
		return false
	}
	return true
}

// hasPrimaryKey 检查列定义中是否有主键。
func hasPrimaryKey(columns []ColumnInfo) bool {
	for _, c := range columns {
		if c.Key == "PRI" {
			return true
		}
	}
	return false
}

// hasAggregation 检查是否有聚合函数。
func hasAggregation(sqlText string) bool {
	clean := strings.ToUpper(sqlText)
	aggregations := []string{"SUM(", "COUNT(", "AVG(", "MIN(", "MAX(", "GROUP_CONCAT(", "LISTAGG("}
	for _, agg := range aggregations {
		if strings.Contains(clean, agg) {
			return true
		}
	}
	return false
}

// hasGroupBy 检查是否有 GROUP BY。
func hasGroupBy(sqlText string) bool {
	clean := strings.ToUpper(sqlText)
	return strings.Contains(clean, " GROUP BY ")
}

// hasOrderBy 检查是否有 ORDER BY。
func hasOrderBy(sqlText string) bool {
	clean := strings.ToUpper(sqlText)
	return strings.Contains(clean, " ORDER BY ")
}

// hasWindowFunction 检查是否有窗口函数。
func hasWindowFunction(sqlText string) bool {
	clean := strings.ToUpper(sqlText)
	windowFunctions := []string{"OVER(", "ROW_NUMBER()", "RANK()", "DENSE_RANK()", "LEAD(", "LAG(", "FIRST_VALUE(", "LAST_VALUE(", "NTILE("}
	for _, wf := range windowFunctions {
		if strings.Contains(clean, wf) {
			return true
		}
	}
	return false
}

// isSelectStar 检查是否为 SELECT *。
func isSelectStar(sqlText string) bool {
	clean := strings.ToUpper(strings.TrimSpace(sqlText))
	return strings.HasPrefix(clean, "SELECT *")
}

// DMLBinding 将表格编辑映射到 SQL UPDATE 语句。
// 规则：主键列值不变，非主键列的修改生成 UPDATE ... SET col=val WHERE pk=val。
func DMLBinding(table string, columns []ColumnInfo, originalRows, editedRows [][]any) ([]string, error) {
	if len(originalRows) != len(editedRows) {
		return nil, fmt.Errorf("行数不匹配")
	}

	var updateSQLs []string
	pkIndices := make([]int, 0) // 主键列索引

	// 收集主键列索引
	for i, col := range columns {
		if col.Key == "PRI" {
			pkIndices = append(pkIndices, i)
		}
	}

	if len(pkIndices) == 0 {
		return nil, fmt.Errorf("表 %s 没有主键，无法生成 UPDATE", table)
	}

	for rowIdx := 0; rowIdx < len(originalRows); rowIdx++ {
		original := originalRows[rowIdx]
		edited := editedRows[rowIdx]

		// 检查主键值是否一致
		pkChanged := false
		for _, pkIdx := range pkIndices {
			if original[pkIdx] != edited[pkIdx] {
				pkChanged = true
				break
			}
		}
		if pkChanged {
			return nil, fmt.Errorf("行 %d 主键值被修改，禁止生成 UPDATE", rowIdx+1)
		}

		// 收集需要更新的列
		setClauses := make([]string, 0)
		for colIdx, col := range columns {
			if col.Key == "PRI" {
				continue // 跳过主键
			}
			if original[colIdx] != edited[colIdx] {
				setClauses = append(setClauses, fmt.Sprintf("%s = %v", col.Name, edited[colIdx]))
			}
		}

		if len(setClauses) > 0 {
			whereClause := buildWhereClause(columns, pkIndices, original)
			updateSQL := fmt.Sprintf("UPDATE %s SET %s WHERE %s",
				table, strings.Join(setClauses, ", "), whereClause)
			updateSQLs = append(updateSQLs, updateSQL)
		}
	}

	return updateSQLs, nil
}

// buildWhereClause 构建主键 WHERE 子句（使用真实列名）。
func buildWhereClause(columns []ColumnInfo, pkIndices []int, row []any) string {
	conditions := make([]string, 0)
	for _, pkIdx := range pkIndices {
		colName := columns[pkIdx].Name
		val := row[pkIdx]
		if val == nil {
			conditions = append(conditions, fmt.Sprintf("%s IS NULL", colName))
		} else {
			conditions = append(conditions, fmt.Sprintf("%s = %v", colName, val))
		}
	}
	return strings.Join(conditions, " AND ")
}