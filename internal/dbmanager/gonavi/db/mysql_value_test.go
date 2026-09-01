package db

import (
	"bytes"
	"testing"
)

func TestNormalizeMySQLValueForWrite_ConvertsBitTextToBytes(t *testing.T) {
	t.Parallel()

	columnTypes := map[string]string{"enabled": "bit(1)"}

	cases := []struct {
		name  string
		value interface{}
		want  []byte
	}{
		{name: "string one", value: "1", want: []byte{1}},
		{name: "string zero", value: "0", want: []byte{0}},
		{name: "bool true", value: true, want: []byte{1}},
		{name: "bool false", value: false, want: []byte{0}},
		{name: "float integral", value: float64(1), want: []byte{1}},
		{name: "binary literal", value: "b'1'", want: []byte{1}},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := normalizeMySQLValueForWrite("enabled", tc.value, columnTypes)
			gotBytes, ok := got.([]byte)
			if !ok {
				t.Fatalf("期望 bit 写入值被转换为 []byte，实际=%T(%v)", got, got)
			}
			if !bytes.Equal(gotBytes, tc.want) {
				t.Fatalf("bit 写入值不符合预期，want=%v got=%v", tc.want, gotBytes)
			}
		})
	}
}

func TestNormalizeMySQLValueForInsert_ConvertsBitTextToBytes(t *testing.T) {
	t.Parallel()

	columnTypes := map[string]string{"enabled": "bit(1)"}

	got, omit := normalizeMySQLValueForInsert("enabled", "1", columnTypes)
	if omit {
		t.Fatalf("bit(1) 插入值不应被省略")
	}
	gotBytes, ok := got.([]byte)
	if !ok {
		t.Fatalf("期望 bit 插入值被转换为 []byte，实际=%T(%v)", got, got)
	}
	if !bytes.Equal(gotBytes, []byte{1}) {
		t.Fatalf("bit 插入值不符合预期，want=%v got=%v", []byte{1}, gotBytes)
	}
}

func TestNormalizeMySQLValueForWrite_KeepsNonBitTextUntouched(t *testing.T) {
	t.Parallel()

	columnTypes := map[string]string{"name": "varchar(255)"}
	got := normalizeMySQLValueForWrite("name", "1", columnTypes)
	if text, ok := got.(string); !ok || text != "1" {
		t.Fatalf("非 bit 列不应被转换，实际=%T(%v)", got, got)
	}
}
