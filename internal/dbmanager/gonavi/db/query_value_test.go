package db

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	mssql "github.com/microsoft/go-mssqldb"
)

type duckMapLike map[any]any

func TestNormalizeQueryValueWithDBType_BitBytes(t *testing.T) {
	v := normalizeQueryValueWithDBType([]byte{0x00}, "BIT")
	if v != int64(0) {
		t.Fatalf("BIT 0x00 期望为 0，实际=%v(%T)", v, v)
	}

	v = normalizeQueryValueWithDBType([]byte{0x01}, "bit")
	if v != int64(1) {
		t.Fatalf("BIT 0x01 期望为 1，实际=%v(%T)", v, v)
	}

	v = normalizeQueryValueWithDBType([]byte{0x01, 0x02}, "BIT VARYING")
	if v != int64(258) {
		t.Fatalf("BIT 0x0102 期望为 258，实际=%v(%T)", v, v)
	}
}

func TestNormalizeQueryValueWithDBType_BitLargeAsString(t *testing.T) {
	v := normalizeQueryValueWithDBType([]byte{0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff}, "BIT")
	if s, ok := v.(string); !ok || s != "18446744073709551615" {
		t.Fatalf("BIT 0xffffffffffffffff 期望为 string(18446744073709551615)，实际=%v(%T)", v, v)
	}
}

func TestNormalizeQueryValueWithDBType_ByteFallbacks(t *testing.T) {
	v := normalizeQueryValueWithDBType([]byte("abc"), "")
	if v != "abc" {
		t.Fatalf("文本 []byte 期望返回 string，实际=%v(%T)", v, v)
	}

	v = normalizeQueryValueWithDBType([]byte{0x00}, "")
	if v != int64(0) {
		t.Fatalf("未知类型 0x00 期望返回 0，实际=%v(%T)", v, v)
	}

	v = normalizeQueryValueWithDBType([]byte{0xff}, "")
	if v != "0xff" {
		t.Fatalf("未知类型 0xff 期望返回 0xff，实际=%v(%T)", v, v)
	}
}

func TestNormalizeQueryValueWithDBType_UniqueIdentifierBytes(t *testing.T) {
	var guid mssql.UniqueIdentifier
	if err := guid.Scan("6F9619FF-8B86-D011-B42D-00C04FC964FF"); err != nil {
		t.Fatalf("构造 UniqueIdentifier 失败: %v", err)
	}

	rawValue, err := guid.Value()
	if err != nil {
		t.Fatalf("UniqueIdentifier.Value() 失败: %v", err)
	}

	rawBytes, ok := rawValue.([]byte)
	if !ok {
		t.Fatalf("期望驱动值为 []byte，实际=%T", rawValue)
	}

	got := normalizeQueryValueWithDBType(rawBytes, "uniqueidentifier")
	if got != "6F9619FF-8B86-D011-B42D-00C04FC964FF" {
		t.Fatalf("uniqueidentifier 期望展示为 GUID 文本，实际=%v(%T)", got, got)
	}
}

func TestNormalizeQueryValueWithDBType_MapAnyAnyForJSON(t *testing.T) {
	input := duckMapLike{
		"id":    int64(1),
		1:       "one",
		true:    []interface{}{duckMapLike{2: "two"}},
		"bytes": []byte("ok"),
	}

	v := normalizeQueryValueWithDBType(input, "")
	root, ok := v.(map[string]interface{})
	if !ok {
		t.Fatalf("期望转换为 map[string]interface{}，实际=%T", v)
	}

	if root["id"] != int64(1) {
		t.Fatalf("id 字段异常，实际=%v(%T)", root["id"], root["id"])
	}
	if root["1"] != "one" {
		t.Fatalf("数字 key 未被字符串化，实际=%v(%T)", root["1"], root["1"])
	}
	if root["bytes"] != "ok" {
		t.Fatalf("嵌套 []byte 未被转换，实际=%v(%T)", root["bytes"], root["bytes"])
	}

	arr, ok := root["true"].([]interface{})
	if !ok || len(arr) != 1 {
		t.Fatalf("bool key 下的数组结构异常，实际=%v(%T)", root["true"], root["true"])
	}
	nested, ok := arr[0].(map[string]interface{})
	if !ok {
		t.Fatalf("嵌套 map 未被转换，实际=%v(%T)", arr[0], arr[0])
	}
	if nested["2"] != "two" {
		t.Fatalf("嵌套 map 数字 key 未转换，实际=%v(%T)", nested["2"], nested["2"])
	}
}

func TestNormalizeQueryValueWithDBType_UnsafeIntegersAsString(t *testing.T) {
	cases := []struct {
		name  string
		input interface{}
		want  string
	}{
		{name: "int64 overflow", input: int64(9007199254740992), want: "9007199254740992"},
		{name: "int64 underflow", input: int64(-9007199254740992), want: "-9007199254740992"},
		{name: "uint64 overflow", input: uint64(9007199254740992), want: "9007199254740992"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := normalizeQueryValueWithDBType(tc.input, "")
			if got != tc.want {
				t.Fatalf("期望=%q，实际=%v(%T)", tc.want, got, got)
			}
		})
	}
}

func TestNormalizeQueryValueWithDBType_SafeIntegersKeepType(t *testing.T) {
	got := normalizeQueryValueWithDBType(int64(9007199254740991), "")
	if _, ok := got.(int64); !ok {
		t.Fatalf("安全范围 int64 应保持数字类型，实际=%v(%T)", got, got)
	}

	got = normalizeQueryValueWithDBType(uint64(9007199254740991), "")
	if _, ok := got.(uint64); !ok {
		t.Fatalf("安全范围 uint64 应保持数字类型，实际=%v(%T)", got, got)
	}
}

func TestNormalizeQueryValueWithDBType_JSONNumber(t *testing.T) {
	cases := []struct {
		name      string
		input     json.Number
		wantType  string
		wantValue string
	}{
		{name: "safe integer", input: json.Number("9007199254740991"), wantType: "int64", wantValue: "9007199254740991"},
		{name: "unsafe integer", input: json.Number("9007199254740992"), wantType: "string", wantValue: "9007199254740992"},
		{name: "unsafe negative integer", input: json.Number("-9007199254740992"), wantType: "string", wantValue: "-9007199254740992"},
		{name: "decimal", input: json.Number("12.5"), wantType: "float64", wantValue: "12.5"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := normalizeQueryValueWithDBType(tc.input, "")
			switch tc.wantType {
			case "int64":
				v, ok := got.(int64)
				if !ok {
					t.Fatalf("期望 int64，实际=%T", got)
				}
				if v != 9007199254740991 {
					t.Fatalf("期望值=%s，实际=%d", tc.wantValue, v)
				}
			case "string":
				v, ok := got.(string)
				if !ok {
					t.Fatalf("期望 string，实际=%T", got)
				}
				if v != tc.wantValue {
					t.Fatalf("期望值=%s，实际=%s", tc.wantValue, v)
				}
			case "float64":
				v, ok := got.(float64)
				if !ok {
					t.Fatalf("期望 float64，实际=%T", got)
				}
				if v != 12.5 {
					t.Fatalf("期望值=%s，实际=%v", tc.wantValue, v)
				}
			default:
				t.Fatalf("未知断言类型：%s", tc.wantType)
			}
		})
	}
}

type customStructValue struct {
	Name string
	Age  int
}

func (v customStructValue) String() string {
	return fmt.Sprintf("%s-%d", v.Name, v.Age)
}

func TestNormalizeQueryValueWithDBType_StructToString(t *testing.T) {
	got := normalizeQueryValueWithDBType(customStructValue{Name: "alice", Age: 18}, "")
	if got != "alice-18" {
		t.Fatalf("结构体应降级为可读字符串，实际=%v(%T)", got, got)
	}
}

func TestNormalizeQueryValueWithDBType_TimeStructToRFC3339(t *testing.T) {
	input := time.Date(2026, 3, 5, 18, 30, 15, 123456789, time.UTC)
	got := normalizeQueryValueWithDBType(input, "")
	text, ok := got.(string)
	if !ok {
		t.Fatalf("time.Time 应转为字符串，实际=%v(%T)", got, got)
	}
	if text != "2026-03-05T18:30:15.123456789Z" {
		t.Fatalf("time.Time 规整值异常，实际=%s", text)
	}
}

func TestNormalizeQueryValueWithDBTypeAndDialect_OceanBaseOracleBinaryTimestampString(t *testing.T) {
	raw := buildOracleBinaryTimestamp(time.Date(2026, 6, 16, 12, 34, 56, 123456000, time.UTC))

	got := normalizeQueryValueWithDBTypeAndDialect(string(raw), "TYPE_CA", oceanBaseOracleScanDialect)
	if got != "2026-06-16T12:34:56.123456Z" {
		t.Fatalf("OceanBase Oracle 二进制 TIMESTAMP 字符串应解码为 RFC3339，实际=%v(%T)", got, got)
	}
}

func TestNormalizeQueryValueWithDBTypeAndDialect_OceanBaseOracleBinaryTimestampStringWithPrecisionType(t *testing.T) {
	raw := buildOracleBinaryTimestamp(time.Date(2026, 6, 16, 12, 34, 56, 123456000, time.UTC))

	got := normalizeQueryValueWithDBTypeAndDialect(string(raw), "TIMESTAMP(6)", oceanBaseOracleScanDialect)
	if got != "2026-06-16T12:34:56.123456Z" {
		t.Fatalf("OceanBase Oracle TIMESTAMP(6) 字符串应解码为 RFC3339，实际=%v(%T)", got, got)
	}
}

func TestNormalizeQueryValueWithDBTypeAndDialect_OceanBaseOracleBinaryTimestampStringWithGenericCarrierType(t *testing.T) {
	raw := buildOracleBinaryTimestamp(time.Date(2026, 6, 16, 12, 34, 56, 123456000, time.UTC))

	got := normalizeQueryValueWithDBTypeAndDialect(string(raw), "VARCHAR2", oceanBaseOracleScanDialect)
	if got != "2026-06-16T12:34:56.123456Z" {
		t.Fatalf("OceanBase Oracle 泛型载体类型的 TIMESTAMP 字符串应解码为 RFC3339，实际=%v(%T)", got, got)
	}
}

func TestNormalizeQueryValueWithDBTypeAndDialect_OceanBaseOracleBinaryTimestampBytes(t *testing.T) {
	raw := buildOracleBinaryTimestamp(time.Date(2026, 6, 16, 12, 34, 56, 123456000, time.UTC))

	got := normalizeQueryValueWithDBTypeAndDialect(raw, "TYPE_CA", oceanBaseOracleScanDialect)
	if got != "2026-06-16T12:34:56.123456Z" {
		t.Fatalf("OceanBase Oracle 二进制 TIMESTAMP 字节值应解码为 RFC3339，实际=%v(%T)", got, got)
	}
}

func TestNormalizeQueryValueWithDBTypeAndDialect_OceanBaseOracleBinaryTimestampBytesWithGenericCarrierType(t *testing.T) {
	raw := buildOracleBinaryTimestamp(time.Date(2026, 6, 16, 12, 34, 56, 123456000, time.UTC))

	got := normalizeQueryValueWithDBTypeAndDialect(raw, "VARCHAR2", oceanBaseOracleScanDialect)
	if got != "2026-06-16T12:34:56.123456Z" {
		t.Fatalf("OceanBase Oracle 泛型载体类型的 TIMESTAMP 字节值应解码为 RFC3339，实际=%v(%T)", got, got)
	}
}

func TestNormalizeQueryValueWithDBTypeAndDialect_OceanBaseOracleBinaryTimestampBytesWithPrecisionType(t *testing.T) {
	raw := buildOracleBinaryTimestamp(time.Date(2026, 6, 16, 12, 34, 56, 123456000, time.UTC))

	got := normalizeQueryValueWithDBTypeAndDialect(raw, "TIMESTAMP(6)", oceanBaseOracleScanDialect)
	if got != "2026-06-16T12:34:56.123456Z" {
		t.Fatalf("OceanBase Oracle TIMESTAMP(6) 字节值应解码为 RFC3339，实际=%v(%T)", got, got)
	}
}

func TestNormalizeQueryValueWithDBTypeAndDialect_OceanBaseOracleBinaryTimestampStringWithoutTypeName(t *testing.T) {
	raw := buildOracleBinaryTimestamp(time.Date(2026, 6, 16, 12, 34, 56, 123456000, time.UTC))

	got := normalizeQueryValueWithDBTypeAndDialect(string(raw), "", oceanBaseOracleScanDialect)
	if got != "2026-06-16T12:34:56.123456Z" {
		t.Fatalf("OceanBase Oracle 空类型名的 TIMESTAMP 字符串应解码为 RFC3339，实际=%v(%T)", got, got)
	}
}

func TestNormalizeQueryValueWithDBTypeAndDialect_OceanBaseOracleBinaryTimestampBytesWithoutTypeName(t *testing.T) {
	raw := buildOracleBinaryTimestamp(time.Date(2026, 6, 16, 12, 34, 56, 123456000, time.UTC))

	got := normalizeQueryValueWithDBTypeAndDialect(raw, "", oceanBaseOracleScanDialect)
	if got != "2026-06-16T12:34:56.123456Z" {
		t.Fatalf("OceanBase Oracle 空类型名的 TIMESTAMP 字节值应解码为 RFC3339，实际=%v(%T)", got, got)
	}
}

func TestNormalizeQueryValueWithDBTypeAndDialect_OceanBaseOracleMySQLEncodedTimestampString(t *testing.T) {
	raw := buildMySQLBinaryTimestamp(time.Date(2026, 6, 16, 12, 34, 56, 123456000, time.UTC))

	got := normalizeQueryValueWithDBTypeAndDialect(string(raw), "TYPE_CA", oceanBaseOracleScanDialect)
	if got != "2026-06-16T12:34:56.123456Z" {
		t.Fatalf("OceanBase Oracle length-encoded TIMESTAMP 字符串应解码为 RFC3339，实际=%v(%T)", got, got)
	}
}

func TestNormalizeQueryValueWithDBTypeAndDialect_OceanBaseOracleMySQLEncodedTimestampBytes(t *testing.T) {
	raw := buildMySQLBinaryTimestamp(time.Date(2026, 6, 16, 12, 34, 56, 123456000, time.UTC))

	got := normalizeQueryValueWithDBTypeAndDialect(raw, "TYPE_CA", oceanBaseOracleScanDialect)
	if got != "2026-06-16T12:34:56.123456Z" {
		t.Fatalf("OceanBase Oracle length-encoded TIMESTAMP 字节值应解码为 RFC3339，实际=%v(%T)", got, got)
	}
}

func TestNormalizeQueryValueWithDBTypeAndDialect_OceanBaseOracleTypeCALiveTimestampString(t *testing.T) {
	raw := []byte{20, 26, 6, 16, 16, 46, 23, 96, 196, 119, 9, 6}

	got := normalizeQueryValueWithDBTypeAndDialect(string(raw), "TYPE_CA", oceanBaseOracleScanDialect)
	if got != "2026-06-16T16:46:23.158844Z" {
		t.Fatalf("OceanBase Oracle TYPE_CA live TIMESTAMP 字符串应解码为 RFC3339，实际=%v(%T)", got, got)
	}
}

func TestNormalizeQueryValueWithDBTypeAndDialect_OceanBaseOracleTypeCALiveTimestampBytes(t *testing.T) {
	raw := []byte{20, 26, 6, 16, 16, 46, 23, 96, 196, 119, 9, 6}

	got := normalizeQueryValueWithDBTypeAndDialect(raw, "TYPE_CA", oceanBaseOracleScanDialect)
	if got != "2026-06-16T16:46:23.158844Z" {
		t.Fatalf("OceanBase Oracle TYPE_CA live TIMESTAMP 字节值应解码为 RFC3339，实际=%v(%T)", got, got)
	}
}

func TestNormalizeQueryValueWithDBTypeAndDialect_OceanBaseOracleTypeCALiveTimestampWithoutFraction(t *testing.T) {
	raw := []byte{20, 26, 6, 17, 5, 0, 0, 0, 0, 0, 0, 6}

	got := normalizeQueryValueWithDBTypeAndDialect(raw, "TYPE_CA", oceanBaseOracleScanDialect)
	if got != "2026-06-17T05:00:00Z" {
		t.Fatalf("OceanBase Oracle TYPE_CA 零小数 TIMESTAMP 字节值应解码为 RFC3339，实际=%v(%T)", got, got)
	}
}

func buildOracleBinaryTimestamp(tm time.Time) []byte {
	if tm.Location() != time.UTC {
		tm = tm.In(time.UTC)
	}
	buf := []byte{
		byte(tm.Year()/100 + 100),
		byte(tm.Year()%100 + 100),
		byte(tm.Month()),
		byte(tm.Day()),
		byte(tm.Hour() + 1),
		byte(tm.Minute() + 1),
		byte(tm.Second() + 1),
		0,
		0,
		0,
		0,
	}
	binary.BigEndian.PutUint32(buf[7:11], uint32(tm.Nanosecond()))
	return buf
}

func buildMySQLBinaryTimestamp(tm time.Time) []byte {
	if tm.Location() != time.UTC {
		tm = tm.In(time.UTC)
	}
	buf := []byte{11, 0, 0, byte(tm.Month()), byte(tm.Day()), byte(tm.Hour()), byte(tm.Minute()), byte(tm.Second()), 0, 0, 0, 0}
	binary.LittleEndian.PutUint16(buf[1:3], uint16(tm.Year()))
	binary.LittleEndian.PutUint32(buf[8:12], uint32(tm.Nanosecond()/1000))
	return buf
}

func TestNormalizeQueryValueWithDBTypeAndDialect_MySQLDateOnly(t *testing.T) {
	input := time.Date(2025, 10, 1, 0, 0, 0, 0, time.Local)

	got := normalizeQueryValueWithDBTypeAndDialect(input, "DATE", "mysql")
	if got != "2025-10-01" {
		t.Fatalf("MySQL DATE 应只展示日期，实际=%v(%T)", got, got)
	}

	got = normalizeQueryValueWithDBTypeAndDialect(input, "NEWDATE", "mysql")
	if got != "2025-10-01" {
		t.Fatalf("MySQL NEWDATE 应只展示日期，实际=%v(%T)", got, got)
	}
}

func TestNormalizeQueryValueWithDBTypeAndDialect_DatetimeKeepsTime(t *testing.T) {
	input := time.Date(2025, 10, 1, 13, 14, 15, 0, time.UTC)

	got := normalizeQueryValueWithDBTypeAndDialect(input, "DATETIME", "mysql")
	if got != "2025-10-01T13:14:15Z" {
		t.Fatalf("MySQL DATETIME 应保留时间，实际=%v(%T)", got, got)
	}

	got = normalizeQueryValueWithDBTypeAndDialect(input, "DATE", "oracle")
	if got != "2025-10-01T13:14:15Z" {
		t.Fatalf("Oracle DATE 应保留时间语义，实际=%v(%T)", got, got)
	}
}

func TestNormalizeQueryValueWithDBTypeAndDialect_OceanBaseOracleDateMidnightDisplaysDateOnly(t *testing.T) {
	input := time.Date(2025, 10, 1, 0, 0, 0, 0, time.UTC)

	got := normalizeQueryValueWithDBTypeAndDialect(input, "DATE", oceanBaseOracleScanDialect)
	if got != "2025-10-01" {
		t.Fatalf("OceanBase Oracle DATE 的午夜值应只展示日期，实际=%v(%T)", got, got)
	}
}

func TestNormalizeQueryValueWithDBTypeAndDialect_OceanBaseOracleDateKeepsNonMidnightTime(t *testing.T) {
	input := time.Date(2025, 10, 1, 13, 14, 15, 0, time.UTC)

	got := normalizeQueryValueWithDBTypeAndDialect(input, "DATE", oceanBaseOracleScanDialect)
	if got != "2025-10-01T13:14:15Z" {
		t.Fatalf("OceanBase Oracle DATE 非午夜值应保留时间，实际=%v(%T)", got, got)
	}
}

func TestNormalizeQueryValueWithDBType_ZeroTemporalValues(t *testing.T) {
	zero := time.Time{}
	cases := []struct {
		name     string
		dbType   string
		wantText string
	}{
		{name: "date", dbType: "DATE", wantText: "0000-00-00"},
		{name: "newdate", dbType: "NEWDATE", wantText: "0000-00-00"},
		{name: "datetime", dbType: "DATETIME", wantText: "0000-00-00 00:00:00"},
		{name: "timestamp", dbType: "TIMESTAMP", wantText: "0000-00-00 00:00:00"},
		{name: "time", dbType: "TIME", wantText: "00:00:00"},
		{name: "year", dbType: "YEAR", wantText: "0000"},
		{name: "unknown", dbType: "", wantText: "0000-00-00 00:00:00"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := normalizeQueryValueWithDBType(zero, tc.dbType)
			text, ok := got.(string)
			if !ok {
				t.Fatalf("期望 string，实际=%v(%T)", got, got)
			}
			if text != tc.wantText {
				t.Fatalf("dbType=%s 期望=%s，实际=%s", tc.dbType, tc.wantText, text)
			}
		})
	}
}
