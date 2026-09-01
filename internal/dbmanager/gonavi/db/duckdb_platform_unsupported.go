//go:build (gonavi_full_drivers || gonavi_duckdb_driver) && !(cgo && (duckdb_use_lib || duckdb_use_static_lib || (darwin && (amd64 || arm64)) || (linux && (amd64 || arm64)) || (windows && amd64)))

package db

import (
	"runtime"
)

func duckDBBuildSupportStatus() (bool, string) {
	return false, localizedDriverRuntimeText("db.backend.error.duckdb_build_unavailable", map[string]any{
		"platform": runtime.GOOS + "/" + runtime.GOARCH,
	})
}
