//go:build gonavi_vastbase_driver

package db

import "testing"

func TestBatchWriteDriverCoverageVastbase(t *testing.T) {
	var driver BatchWriteExecer = (*VastbaseDB)(nil)
	if driver == nil {
		t.Fatal("expected VastbaseDB to implement BatchWriteExecer")
	}
}
