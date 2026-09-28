package db

import (
	"database/sql"
	"fmt"
	"runtime"
	"strconv"
	"strings"
	"unicode/utf8"

	"opscore/internal/dbmanager/gonavi/connection"
)

// streamRowsPeriodicGCInterval 控制 streamRowsForDialect 每处理多少行主动触发一次 runtime.GC。
//
// 背景：大结果集（88W+ 行）流式扫描时，每行 scanner 会分配 []interface{} 和 map[string]interface{}，
// Go 默认 GOGC=100 下堆翻倍才触发 GC，瞬时峰值可达数据总量 5-8 倍。
// 这里周期性主动 GC，让内存在扫描过程中及时回收，避免 RSS 单调爬升。
//
// 取值 50000：每 5W 行触发一次 GC，对 88W 行导出场景约触发 18 次，CPU 开销可忽略；
// 同时保证单次 GC 之间累积的临时对象不超过几百 MB，避免 GC 间隙堆膨胀。
const streamRowsPeriodicGCInterval = 50000

// interactiveOracleLargeObjectPreviewBytes bounds Oracle large objects before
// they cross the Wails bridge. The streaming export path stays unbounded.
const interactiveOracleLargeObjectPreviewBytes = 4 * 1024

// interactiveLargeObjectPreviewBytes 是**跨方言**的大对象预览阈值(P1-13)。
// 上游(dbx)的建议是"搬思想但默认改成不限长, 只在超阈值时启用" —— 这里就取这个口径:
// 小于阈值的值原样返回(不被截断), 超过的给预览 + 提示"这可以分片读/下载"。
// 真正的整值取回走 /api/dbmanager/cell/read 与 /cell/download, 不靠把网格撑大。
const interactiveLargeObjectPreviewBytes = 4 * 1024

// largeObjectHint 告诉用户"被截断了, 以及怎么拿全" —— 只说被截断不给出口,
// 用户只能看到半截数据却不知道下一步该干什么。
const largeObjectHint = "(值已被截断用于预览; 在单元格详情里用「查看完整值 / 下载」按主键分片取回)"

func scanRows(rows *sql.Rows) ([]map[string]interface{}, []string, error) {
	return scanRowsForDialect(rows, "")
}

func streamRows(rows *sql.Rows, consumer QueryStreamConsumer) error {
	return streamRowsForDialect(rows, "", consumer)
}

type queryRowScanner struct {
	columns     []string
	dbTypeNames []string
	dialect     string
	values      []interface{}
	normalized  []interface{}
	valuePtrs   []interface{}
}

type queryRowsScanner interface {
	scanCurrentPreviewRow(*sql.Rows) (map[string]interface{}, error)
	scanCurrentRow(*sql.Rows) (map[string]interface{}, error)
	scanCurrentRowValues(*sql.Rows) ([]interface{}, error)
}

func scanRowsForDialect(rows *sql.Rows, dialect string) ([]map[string]interface{}, []string, error) {
	return scanRowsForDialectWithPreview(rows, dialect, true)
}

func scanRowsUnboundedForDialect(rows *sql.Rows, dialect string) ([]map[string]interface{}, []string, error) {
	return scanRowsForDialectWithPreview(rows, dialect, false)
}

func scanRowsForDialectWithPreview(rows *sql.Rows, dialect string, boundOracleLargeObjects bool) ([]map[string]interface{}, []string, error) {
	columns, err := rows.Columns()
	if err != nil {
		return nil, nil, err
	}
	columns = ensureUniqueQueryColumnNames(columns)

	colTypes, err := rows.ColumnTypes()
	if err != nil || len(colTypes) != len(columns) {
		colTypes = nil
	}

	scanner := newQueryRowScanner(columns, colTypes, dialect)
	return scanRowsWithScanner(rows, columns, scanner, boundOracleLargeObjects)
}

func scanRowsWithScanner(rows *sql.Rows, columns []string, scanner queryRowsScanner, boundOracleLargeObjects bool) ([]map[string]interface{}, []string, error) {
	resultData := make([]map[string]interface{}, 0)

	var rowNumber int64
	for rows.Next() {
		rowNumber++
		var (
			entry map[string]interface{}
			err   error
		)
		if boundOracleLargeObjects {
			entry, err = scanner.scanCurrentPreviewRow(rows)
		} else {
			entry, err = scanner.scanCurrentRow(rows)
		}
		if err != nil {
			return resultData, columns, newQueryRowScanError(rowNumber, columns, err)
		}
		resultData = append(resultData, entry)
	}

	if err := rows.Err(); err != nil {
		return resultData, columns, err
	}
	return resultData, columns, nil
}

func streamRowsForDialect(rows *sql.Rows, dialect string, consumer QueryStreamConsumer) error {
	if consumer == nil {
		return fmt.Errorf("query stream consumer required")
	}

	columns, err := rows.Columns()
	if err != nil {
		return err
	}
	columns = ensureUniqueQueryColumnNames(columns)

	colTypes, err := rows.ColumnTypes()
	if err != nil || len(colTypes) != len(columns) {
		colTypes = nil
	}

	scanner := newQueryRowScanner(columns, colTypes, dialect)
	return streamRowsWithScanner(rows, columns, consumer, scanner)
}

func streamRowsWithScanner(rows *sql.Rows, columns []string, consumer QueryStreamConsumer, scanner queryRowsScanner) error {
	if err := consumer.SetColumns(columns); err != nil {
		return err
	}
	valueConsumer, useValueConsumer := consumer.(QueryStreamValueConsumer)

	// processedRows 用于周期性触发 GC，见 streamRowsPeriodicGCInterval 注释。
	// 注意：此路径同时被 driver-agent 进程（OceanBase 等 optional driver）和
	// 主进程的 in-process 流式查询调用，所以一处加 GC 即可覆盖两端。
	var processedRows int64

	var rowNumber int64
	for rows.Next() {
		rowNumber++
		if useValueConsumer {
			values, err := scanner.scanCurrentRowValues(rows)
			if err != nil {
				return newQueryRowScanError(rowNumber, columns, err)
			}
			if err := valueConsumer.ConsumeRowValues(values); err != nil {
				return err
			}
		} else {
			entry, err := scanner.scanCurrentRow(rows)
			if err != nil {
				return newQueryRowScanError(rowNumber, columns, err)
			}
			if err := consumer.ConsumeRow(entry); err != nil {
				return err
			}
		}

		processedRows++
		if processedRows%streamRowsPeriodicGCInterval == 0 {
			runtime.GC()
			// 自适应抬升 driver-agent 进程的内存 soft limit。
			// 主进程未启用 soft limit（未调 InitMemorySoftLimit），此调用是 no-op。
			MaybeGrowMemoryLimit()
		}
	}

	return rows.Err()
}

func newQueryRowScanError(rowNumber int64, columns []string, err error) error {
	return fmt.Errorf("scan query row %d (columns: %s): %w", rowNumber, strings.Join(columns, ", "), err)
}

func newQueryRowScanner(columns []string, colTypes []*sql.ColumnType, dialect string) *queryRowScanner {
	values := make([]interface{}, len(columns))
	valuePtrs := make([]interface{}, len(columns))
	for i := range columns {
		valuePtrs[i] = &values[i]
	}
	dbTypeNames := make([]string, len(columns))
	for i := range columns {
		if colTypes != nil && i < len(colTypes) && colTypes[i] != nil {
			dbTypeNames[i] = colTypes[i].DatabaseTypeName()
		}
	}
	return &queryRowScanner{
		columns:     columns,
		dbTypeNames: dbTypeNames,
		dialect:     dialect,
		values:      values,
		normalized:  make([]interface{}, len(columns)),
		valuePtrs:   valuePtrs,
	}
}

func (s *queryRowScanner) scanCurrentRowValues(rows *sql.Rows) ([]interface{}, error) {
	return s.scanCurrentRowValuesWithPreview(rows, false)
}

func (s *queryRowScanner) scanCurrentRowValuesWithPreview(rows *sql.Rows, boundOracleLargeObjects bool) ([]interface{}, error) {
	if err := rows.Scan(s.valuePtrs...); err != nil {
		return nil, err
	}
	for i := range s.columns {
		if boundOracleLargeObjects {
			s.normalized[i] = normalizeInteractiveQueryValue(s.values[i], s.dbTypeNames[i], s.dialect)
		} else {
			s.normalized[i] = normalizeQueryValueWithDBTypeAndDialect(s.values[i], s.dbTypeNames[i], s.dialect)
		}
	}
	return s.normalized, nil
}

func normalizeInteractiveQueryValue(value interface{}, databaseTypeName, dialect string) interface{} {
	switch typedValue := value.(type) {
	case []byte:
		if len(typedValue) > interactiveLargeObjectPreviewBytes && isBinaryLargeObjectType(databaseTypeName, dialect) {
			preview := normalizeQueryValueWithDBTypeAndDialect(
				typedValue[:interactiveLargeObjectPreviewBytes],
				databaseTypeName,
				dialect,
			)
			previewText, ok := preview.(string)
			if !ok {
				previewText = fmt.Sprint(preview)
			}
			// 提示追加在**末尾**而不是塞进前缀: 前缀 "[BLOB preview: N/M bytes] " 是既有
			// 可解析格式(测试与可能的解析方都依赖它), 动它会连带改语义。
			return fmt.Sprintf(
				"[BLOB preview: %d/%d bytes] %s\n%s",
				interactiveLargeObjectPreviewBytes,
				len(typedValue),
				previewText,
				largeObjectHint,
			)
		}
	case string:
		if len(typedValue) > interactiveLargeObjectPreviewBytes && isTextLargeObjectType(databaseTypeName, dialect) {
			preview := truncateUTF8Prefix(typedValue, interactiveLargeObjectPreviewBytes)
			return fmt.Sprintf(
				"[CLOB preview: %d/%d bytes] %s\n%s",
				len(preview),
				len(typedValue),
				preview,
				largeObjectHint,
			)
		}
	}

	return normalizeQueryValueWithDBTypeAndDialect(value, databaseTypeName, dialect)
}

// isBinaryLargeObjectType 判定"这个列类型的大值该不该只给预览"。
//
// 原来只认 Oracle 的类型名(OCIBLOBLOCATOR/LONGRAW/LONGVARRAW) —— 于是 MySQL 的
// blob/longblob、PG 的 bytea、SQL Server 的 varbinary(max) 全都把整个值塞进网格,
// 几 MB 的二进制直接把界面拖死(P1-13 把它扩成按方言的判定表)。
//
// **判据刻意保守**: 只有"声明上就可能非常大"的类型才算, 否则会给普通列误打
// "已被截断"的标签。所以 BINARY(16)/VARBINARY(255)/RAW 这种定长小二进制**不算** ——
// 它们本来就短, 截断提示只会误导。判定同时看阈值(调用方只对超阈值的大值才问这个函数),
// 所以这里宁可少认几个, 也不要多认。
func isBinaryLargeObjectType(databaseTypeName, dialect string) bool {
	typeName := strings.ToUpper(strings.ReplaceAll(strings.TrimSpace(databaseTypeName), " ", ""))
	if typeName == "" {
		return false
	}
	// Oracle 族的 locator 名(不含下面关键词, 单列出来)
	switch typeName {
	case "OCIBLOBLOCATOR", "LONGRAW", "LONGVARRAW":
		return true
	}
	// 带长度限定的: 只有"超大"那几档才算(binary(16) 这类不算)
	switch typeName {
	case "BLOB", "LONGBLOB", "MEDIUMBLOB", "TINYBLOB",
		"BYTEA", "IMAGE", "NTEXT", "VARBINARY(MAX)", "BINARY(MAX)",
		"GEOMETRY", "GEOGRAPHY":
		return true
	}
	// 长度很长的 varbinary/binary(如 varbinary(65535)) 也算 —— 解析出上限再判
	if n, ok := parseTypeSize(typeName); ok && n >= 65535 &&
		(strings.HasPrefix(typeName, "VARBINARY(") || strings.HasPrefix(typeName, "BINARY(")) {
		return true
	}
	return false
}

// isTextLargeObjectType 判定"文本大对象"(CLOB/TEXT 等)。
func isTextLargeObjectType(databaseTypeName, dialect string) bool {
	typeName := strings.ToUpper(strings.ReplaceAll(strings.TrimSpace(databaseTypeName), " ", ""))
	if typeName == "" {
		return false
	}
	switch typeName {
	case "OCICLOBLOCATOR", "LONG", "LONGVARCHAR":
		return true
	}
	// 只认"声明上就很长"的文本类型; 普通的 VARCHAR/TEXT 不在此列 ——
	// PG 的 text 与 MySQL 的 longtext 才是真无上限的那两个。
	switch typeName {
	case "CLOB", "NCLOB", "LONGTEXT", "MEDIUMTEXT", "TINYTEXT", "XML", "JSONB", "JSON":
		return true
	}
	return false
}

// parseTypeSize 从 "VARBINARY(65535)" / "BINARY(2000)" 里取括号里的数字。
func parseTypeSize(typeName string) (int, bool) {
	i := strings.IndexByte(typeName, '(')
	j := strings.IndexByte(typeName, ')')
	if i < 0 || j <= i+1 {
		return 0, false
	}
	n, err := strconv.Atoi(typeName[i+1 : j])
	if err != nil {
		return 0, false
	}
	return n, true
}

func truncateUTF8Prefix(value string, maxBytes int) string {
	if maxBytes <= 0 {
		return ""
	}
	if len(value) <= maxBytes {
		return value
	}

	end := maxBytes
	for end > 0 && !utf8.RuneStart(value[end]) {
		end--
	}
	return value[:end]
}

func (s *queryRowScanner) scanCurrentPreviewRow(rows *sql.Rows) (map[string]interface{}, error) {
	normalized, err := s.scanCurrentRowValuesWithPreview(rows, true)
	if err != nil {
		return nil, err
	}
	entry := make(map[string]interface{}, len(s.columns))
	for i, col := range s.columns {
		entry[col] = normalized[i]
	}
	return entry, nil
}

func (s *queryRowScanner) scanCurrentRow(rows *sql.Rows) (map[string]interface{}, error) {
	normalized, err := s.scanCurrentRowValues(rows)
	if err != nil {
		return nil, err
	}
	entry := make(map[string]interface{}, len(s.columns))
	for i, col := range s.columns {
		entry[col] = normalized[i]
	}
	return entry, nil
}

func ensureUniqueQueryColumnNames(columns []string) []string {
	if len(columns) == 0 {
		return columns
	}

	uniqueColumns := make([]string, len(columns))
	taken := make(map[string]struct{}, len(columns))
	nextSuffix := make(map[string]int, len(columns))

	for idx, column := range columns {
		base := column
		if base == "" {
			base = fmt.Sprintf("column_%d", idx+1)
		}

		candidate := base
		if _, exists := taken[candidate]; exists {
			suffix := nextSuffix[base]
			if suffix < 2 {
				suffix = 2
			}
			for {
				candidate = fmt.Sprintf("%s_%d", base, suffix)
				if _, exists := taken[candidate]; !exists {
					break
				}
				suffix++
			}
			nextSuffix[base] = suffix + 1
		} else {
			nextSuffix[base] = 2
		}

		uniqueColumns[idx] = candidate
		taken[candidate] = struct{}{}
	}

	return uniqueColumns
}

// scanMultiRows 遍历 sql.Rows 中的所有结果集，将每个结果集作为 ResultSetData 返回。
// 利用 rows.NextResultSet() 支持一次 query 返回多个结果集的场景。
func scanMultiRows(rows *sql.Rows) ([]connection.ResultSetData, error) {
	return scanMultiRowsForDialect(rows, "")
}

func scanMultiRowsForDialect(rows *sql.Rows, dialect string) ([]connection.ResultSetData, error) {
	var results []connection.ResultSetData
	for {
		data, cols, err := scanRowsForDialect(rows, dialect)
		if err != nil {
			return results, err
		}
		if data == nil {
			data = make([]map[string]interface{}, 0)
		}
		if cols == nil {
			cols = []string{}
		}
		results = append(results, connection.ResultSetData{
			Rows:    data,
			Columns: cols,
		})
		if !rows.NextResultSet() {
			break
		}
	}
	if len(results) == 0 {
		results = []connection.ResultSetData{{
			Rows:    make([]map[string]interface{}, 0),
			Columns: []string{},
		}}
	}
	if err := rows.Err(); err != nil {
		return results, err
	}
	return results, nil
}
