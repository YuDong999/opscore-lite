package db

import "testing"

func TestDecodeJSONWithUseNumber_QueryRowsPreserveUnsafeInteger(t *testing.T) {
	raw := []byte(`[{"id":9007199254740993,"safe":123,"nested":{"n":9007199254740992},"arr":[9007199254740992,1],"decimal":1.25}]`)
	var out []map[string]interface{}

	if err := decodeJSONWithUseNumber(raw, &out); err != nil {
		t.Fatalf("解码失败: %v", err)
	}
	if len(out) != 1 {
		t.Fatalf("期望 1 行，实际 %d", len(out))
	}

	row := out[0]
	if got, ok := row["id"].(string); !ok || got != "9007199254740993" {
		t.Fatalf("id 应为 string 且保持精度，实际=%v(%T)", row["id"], row["id"])
	}
	if got, ok := row["safe"].(int64); !ok || got != 123 {
		t.Fatalf("safe 应为 int64(123)，实际=%v(%T)", row["safe"], row["safe"])
	}
	nested, ok := row["nested"].(map[string]interface{})
	if !ok {
		t.Fatalf("nested 类型异常：%T", row["nested"])
	}
	if got, ok := nested["n"].(string); !ok || got != "9007199254740992" {
		t.Fatalf("nested.n 应为 string 且保持精度，实际=%v(%T)", nested["n"], nested["n"])
	}
	arr, ok := row["arr"].([]interface{})
	if !ok || len(arr) != 2 {
		t.Fatalf("arr 类型异常：%v(%T)", row["arr"], row["arr"])
	}
	if got, ok := arr[0].(string); !ok || got != "9007199254740992" {
		t.Fatalf("arr[0] 应为 string 且保持精度，实际=%v(%T)", arr[0], arr[0])
	}
	if got, ok := arr[1].(int64); !ok || got != 1 {
		t.Fatalf("arr[1] 应为 int64(1)，实际=%v(%T)", arr[1], arr[1])
	}
	if got, ok := row["decimal"].(float64); !ok || got != 1.25 {
		t.Fatalf("decimal 应为 float64(1.25)，实际=%v(%T)", row["decimal"], row["decimal"])
	}
}

func TestDecodeJSONWithUseNumber_TypedStruct(t *testing.T) {
	type item struct {
		ID   int64  `json:"id"`
		Name string `json:"name"`
	}

	var out []item
	if err := decodeJSONWithUseNumber([]byte(`[{"id":7,"name":"ok"}]`), &out); err != nil {
		t.Fatalf("解码失败: %v", err)
	}
	if len(out) != 1 || out[0].ID != 7 || out[0].Name != "ok" {
		t.Fatalf("结构体解码结果异常：%+v", out)
	}
}
