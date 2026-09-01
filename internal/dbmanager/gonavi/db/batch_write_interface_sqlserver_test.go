//go:build gonavi_sqlserver_driver

package db

import "testing"

func TestBatchWriteDriverCoverageSQLServer(t *testing.T) {
	var driver BatchWriteExecer = (*SqlServerDB)(nil)
	if driver == nil {
		t.Fatal("expected SqlServerDB to implement BatchWriteExecer")
	}
}
