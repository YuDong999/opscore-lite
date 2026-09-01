//go:build gonavi_gaussdb_driver

package db

import "testing"

func TestBatchWriteDriverCoverageGaussDB(t *testing.T) {
	var driver BatchWriteExecer = (*GaussDB)(nil)
	if driver == nil {
		t.Fatal("expected GaussDB to implement BatchWriteExecer")
	}
}
