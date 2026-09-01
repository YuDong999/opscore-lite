//go:build gonavi_highgo_driver

package db

import "testing"

func TestBatchWriteDriverCoverageHighGo(t *testing.T) {
	var driver BatchWriteExecer = (*HighGoDB)(nil)
	if driver == nil {
		t.Fatal("expected HighGoDB to implement BatchWriteExecer")
	}
}
