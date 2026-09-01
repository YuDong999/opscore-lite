//go:build gonavi_kingbase_driver

package db

import "testing"

func TestBatchWriteDriverCoverageKingbase(t *testing.T) {
	var driver BatchWriteExecer = (*KingbaseDB)(nil)
	if driver == nil {
		t.Fatal("expected KingbaseDB to implement BatchWriteExecer")
	}
}
