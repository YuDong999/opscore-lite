//go:build gonavi_full_drivers || gonavi_starrocks_driver

package db

import (
	"strings"
	"testing"
)

func TestBuildStarRocksColumnsQuery_UsesInformationSchemaColumnKey(t *testing.T) {
	t.Parallel()

	query := buildStarRocksColumnsQuery("test_db", "cross_border_erp_erp_sales_order")

	if !strings.Contains(query, "FROM information_schema.columns") {
		t.Fatalf("StarRocks columns query should use information_schema.columns, got=%s", query)
	}
	if !strings.Contains(query, "COLUMN_KEY") {
		t.Fatalf("StarRocks columns query should expose COLUMN_KEY as Key, got=%s", query)
	}
	if !strings.Contains(query, "TABLE_SCHEMA = 'test_db'") {
		t.Fatalf("StarRocks columns query should filter by schema, got=%s", query)
	}
	if !strings.Contains(query, "TABLE_NAME = 'cross_border_erp_erp_sales_order'") {
		t.Fatalf("StarRocks columns query should filter by table name, got=%s", query)
	}
}

func TestBuildStarRocksColumnsQuery_UsesCurrentDatabaseWhenDbNameEmpty(t *testing.T) {
	t.Parallel()

	query := buildStarRocksColumnsQuery("", "orders")

	if !strings.Contains(query, "TABLE_SCHEMA = DATABASE()") {
		t.Fatalf("StarRocks columns query should fall back to current database, got=%s", query)
	}
	if !strings.Contains(query, "TABLE_NAME = 'orders'") {
		t.Fatalf("StarRocks columns query should filter by table name, got=%s", query)
	}
}

func TestBuildStarRocksColumnDefinitions_MarksPrimaryKeyColumns(t *testing.T) {
	t.Parallel()

	columns := buildStarRocksColumnDefinitions([]map[string]interface{}{
		{
			"Field":   "id",
			"Type":    "bigint",
			"Null":    "NO",
			"Key":     "pri",
			"Default": nil,
			"Extra":   "",
			"Comment": "订单ID",
		},
		{
			"Field":   "order_no",
			"Type":    "varchar(64)",
			"Null":    "YES",
			"Key":     "",
			"Default": "",
			"Extra":   "",
			"Comment": "订单号",
		},
	})

	if len(columns) != 2 {
		t.Fatalf("unexpected column count: %d", len(columns))
	}
	if columns[0].Name != "id" || columns[0].Key != "PRI" {
		t.Fatalf("StarRocks primary key column was not marked as PRI: %+v", columns[0])
	}
	if columns[1].Name != "order_no" || columns[1].Key != "" {
		t.Fatalf("StarRocks non-primary column key should stay empty: %+v", columns[1])
	}
	if columns[0].Default != nil {
		t.Fatalf("nil default should remain nil: %+v", columns[0])
	}
	if columns[1].Default == nil || *columns[1].Default != "" {
		t.Fatalf("empty string default should be preserved: %+v", columns[1])
	}
}
