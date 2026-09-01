package db

import (
	"net/url"
	"testing"

	"opscore/internal/dbmanager/gonavi/connection"
)

func TestMergeConnectionParamsFromConfigWithAllowlistCanonicalizesAndFilters(t *testing.T) {
	t.Parallel()

	params := url.Values{}
	cfg := connection.ConnectionConfig{
		URI:              "postgres://u:p@db.local/app?application_name=from-uri&unknown_uri=bad",
		ConnectionParams: "Application_Name=from-config&statement_timeout=3000&timezone=Asia%2FShanghai&unknown_config=bad",
	}

	mergeConnectionParamsFromConfigWithAllowlist(params, cfg, postgresConnectionParamNames, "postgres")

	if got := params.Get("application_name"); got != "from-config" {
		t.Fatalf("application_name = %q, want from-config", got)
	}
	if got := params.Get("statement_timeout"); got != "3000" {
		t.Fatalf("statement_timeout = %q, want 3000", got)
	}
	if got := params.Get("TimeZone"); got != "Asia/Shanghai" {
		t.Fatalf("TimeZone = %q, want Asia/Shanghai", got)
	}
	if got := params.Get("unknown_uri"); got != "" {
		t.Fatalf("unknown_uri should be filtered, got %q", got)
	}
	if got := params.Get("unknown_config"); got != "" {
		t.Fatalf("unknown_config should be filtered, got %q", got)
	}
}

func TestSQLServerConnectionParamAllowlistMapsADOSynonyms(t *testing.T) {
	t.Parallel()

	params := url.Values{}
	mergeConnectionParamValuesWithAllowlist(params, url.Values{
		"Application Name":          []string{"GoNavi"},
		"Initial Catalog":           []string{"appdb"},
		"UID":                       []string{"sa"},
		"Trust Server Certificate":  []string{"true"},
		"Column Encryption Setting": []string{"Enabled"},
		"ignored":                   []string{"bad"},
	}, sqlServerConnectionParamNames)

	if got := params.Get("app name"); got != "GoNavi" {
		t.Fatalf("app name = %q, want GoNavi", got)
	}
	if got := params.Get("database"); got != "appdb" {
		t.Fatalf("database = %q, want appdb", got)
	}
	if got := params.Get("user id"); got != "sa" {
		t.Fatalf("user id = %q, want sa", got)
	}
	if got := params.Get("trustservercertificate"); got != "true" {
		t.Fatalf("trustservercertificate = %q, want true", got)
	}
	if got := params.Get("columnencryption"); got != "Enabled" {
		t.Fatalf("columnencryption = %q, want Enabled", got)
	}
	if got := params.Get("ignored"); got != "" {
		t.Fatalf("ignored should be filtered, got %q", got)
	}
}

func TestDamengConnectionParamAllowlistMapsUppercaseAliases(t *testing.T) {
	t.Parallel()

	params := url.Values{}
	mergeConnectionParamValuesWithAllowlist(params, url.Values{
		"SSL_CERT_PATH":   []string{"/cert.pem"},
		"SSL_KEY_PATH":    []string{"/key.pem"},
		"CONNECT_TIMEOUT": []string{"5000"},
		"unknown":         []string{"bad"},
	}, damengConnectionParamNames)

	if got := params.Get("sslCertPath"); got != "/cert.pem" {
		t.Fatalf("sslCertPath = %q, want /cert.pem", got)
	}
	if got := params.Get("sslKeyPath"); got != "/key.pem" {
		t.Fatalf("sslKeyPath = %q, want /key.pem", got)
	}
	if got := params.Get("connectTimeout"); got != "5000" {
		t.Fatalf("connectTimeout = %q, want 5000", got)
	}
	if got := params.Get("unknown"); got != "" {
		t.Fatalf("unknown should be filtered, got %q", got)
	}
}
