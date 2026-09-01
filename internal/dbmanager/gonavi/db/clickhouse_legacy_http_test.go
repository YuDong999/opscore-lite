//go:build gonavi_full_drivers || gonavi_clickhouse_driver

package db

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"opscore/internal/dbmanager/gonavi/connection"

	clickhouse "github.com/ClickHouse/clickhouse-go/v2"
)

func TestClickHouseLegacyHTTPQueryPreservesValuesAndRequestOptions(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/clickhouse" {
			t.Errorf("HTTP path = %q", r.URL.Path)
		}
		if got := r.URL.Query().Get("default_format"); got != clickHouseLegacyHTTPFormat {
			t.Errorf("default_format = %q", got)
		}
		if got := r.URL.Query().Get("database"); got != "analytics" {
			t.Errorf("database = %q", got)
		}
		if got := r.URL.Query().Get("client_protocol_version"); got != "" {
			t.Errorf("client_protocol_version should be absent, got %q", got)
		}
		if got := r.URL.Query().Get("max_execution_time"); got != "60" {
			t.Errorf("max_execution_time = %q", got)
		}
		username, password, ok := r.BasicAuth()
		if !ok || username != "reporter" || password != "secret" {
			t.Errorf("basic auth = (%q, %q, %t)", username, password, ok)
		}
		_, _ = io.WriteString(w, strings.Join([]string{
			`["id","id","nullable","created_at","items","attrs"]`,
			`["UInt64","UInt8","Nullable(String)","DateTime","Array(UInt64)","Map(String, UInt64)"]`,
			`[9007199254740993,2,null,"2022-08-03 12:13:14",[1,9007199254740994],{"small":3,"large":9007199254740995}]`,
			"",
		}, "\n"))
	}))
	defer server.Close()

	client := newClickHouseLegacyHTTPTestClientWithOptions(t, server, func(opts *clickhouse.Options) {
		opts.Auth = clickhouse.Auth{Database: "analytics", Username: "reporter", Password: "secret"}
		opts.HttpUrlPath = "clickhouse"
		opts.Settings = clickhouse.Settings{
			"max_execution_time":      60,
			"client_protocol_version": 54485,
		}
	})

	rows, columns, err := client.Query(context.Background(), "SELECT values")
	if err != nil {
		t.Fatalf("legacy query failed: %v", err)
	}
	if !reflect.DeepEqual(columns, []string{"id", "id_2", "nullable", "created_at", "items", "attrs"}) {
		t.Fatalf("columns = %#v", columns)
	}
	if len(rows) != 1 {
		t.Fatalf("rows = %#v", rows)
	}
	row := rows[0]
	if row["id"] != "9007199254740993" || row["id_2"] != int64(2) || row["nullable"] != nil {
		t.Fatalf("scalar values = %#v", row)
	}
	if !reflect.DeepEqual(row["items"], []interface{}{int64(1), "9007199254740994"}) {
		t.Fatalf("items = %#v", row["items"])
	}
	if !reflect.DeepEqual(row["attrs"], map[string]interface{}{"small": int64(3), "large": "9007199254740995"}) {
		t.Fatalf("attrs = %#v", row["attrs"])
	}
}

func TestClickHouseLegacyHTTPStreamDeliversRowsBeforeEOF(t *testing.T) {
	firstChunkWritten := make(chan struct{})
	releaseResponse := make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(releaseResponse) }) }
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Error("test server does not support flushing")
			return
		}
		_, _ = io.WriteString(w, "[\"n\"]\n[\"UInt64\"]\n[1]\n")
		flusher.Flush()
		close(firstChunkWritten)
		<-releaseResponse
		_, _ = io.WriteString(w, "[2]\n")
	}))
	defer func() {
		release()
		server.Close()
	}()

	client := newClickHouseLegacyHTTPTestClient(t, server, nil)
	consumer := &legacyHTTPTestStreamConsumer{firstRow: make(chan struct{})}
	errCh := make(chan error, 1)
	go func() {
		errCh <- client.StreamQuery(context.Background(), "SELECT stream", consumer)
	}()

	select {
	case <-firstChunkWritten:
	case <-time.After(2 * time.Second):
		t.Fatal("server did not write the first response chunk")
	}
	select {
	case <-consumer.firstRow:
	case <-time.After(2 * time.Second):
		t.Fatal("first row was not delivered before response EOF")
	}
	release()
	if err := <-errCh; err != nil {
		t.Fatalf("stream query failed: %v", err)
	}
	if !reflect.DeepEqual(consumer.columns, []string{"n"}) {
		t.Fatalf("columns = %#v", consumer.columns)
	}
	if !reflect.DeepEqual(consumer.values, [][]interface{}{{int64(1)}, {int64(2)}}) {
		t.Fatalf("values = %#v", consumer.values)
	}
}

func TestClickHouseLegacyHTTPQueryReportsExceptionAppendedAfterRows(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "[\"n\"]\n[\"UInt64\"]\n[1]\nCode: 241. DB::Exception: memory limit exceeded. (MEMORY_LIMIT_EXCEEDED)\n")
	}))
	defer server.Close()

	client := newClickHouseLegacyHTTPTestClient(t, server, nil)
	rows, columns, err := client.Query(context.Background(), "SELECT fails_late")
	if err == nil || !strings.Contains(err.Error(), "memory limit exceeded") {
		t.Fatalf("expected trailing ClickHouse exception, got rows=%#v columns=%#v err=%v", rows, columns, err)
	}
	if len(rows) != 1 || len(columns) != 1 {
		t.Fatalf("decoded prefix should remain available, rows=%#v columns=%#v", rows, columns)
	}
}

func TestClickHouseLegacyHTTPExecHandlesSuccessAndErrors(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if got := r.URL.Query().Get("wait_end_of_query"); got != "1" {
			t.Errorf("wait_end_of_query = %q for %q", got, body)
		}
		switch string(body) {
		case "ALTER OK":
			w.WriteHeader(http.StatusOK)
		case "ALTER STATUS ERROR":
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = io.WriteString(w, "Code: 60. DB::Exception: table does not exist. (UNKNOWN_TABLE)")
		case "ALTER TAIL ERROR":
			w.WriteHeader(http.StatusOK)
			_, _ = io.WriteString(w, "Code: 241. DB::Exception: memory limit exceeded. (MEMORY_LIMIT_EXCEEDED)")
		default:
			w.WriteHeader(http.StatusBadRequest)
		}
	}))
	defer server.Close()

	client := newClickHouseLegacyHTTPTestClient(t, server, nil)
	if affected, err := client.Exec(context.Background(), "ALTER OK"); err != nil || affected != 0 {
		t.Fatalf("successful exec = (%d, %v)", affected, err)
	}
	if _, err := client.Exec(context.Background(), "ALTER STATUS ERROR"); err == nil || !strings.Contains(err.Error(), "table does not exist") {
		t.Fatalf("expected HTTP status error, got %v", err)
	}
	if _, err := client.Exec(context.Background(), "ALTER TAIL ERROR"); err == nil || !strings.Contains(err.Error(), "memory limit exceeded") {
		t.Fatalf("expected HTTP 200 body exception, got %v", err)
	}
}

func TestClickHouseApplyChangesUsesLegacyHTTPBackend(t *testing.T) {
	var (
		mu      sync.Mutex
		queries []string
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		queries = append(queries, string(body))
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	legacyClient := newClickHouseLegacyHTTPTestClient(t, server, nil)
	client := &ClickHouseDB{legacyHTTP: legacyClient, database: "analytics"}
	err := client.ApplyChanges("events", connection.ChangeSet{
		Deletes: []map[string]interface{}{{"id": int64(1)}},
		Updates: []connection.UpdateRow{{
			Keys:   map[string]interface{}{"id": int64(2)},
			Values: map[string]interface{}{"name": "updated"},
		}},
		Inserts: []map[string]interface{}{{"id": int64(3), "name": "inserted"}},
	})
	if err != nil {
		t.Fatalf("ApplyChanges with legacy HTTP failed: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(queries) != 3 {
		t.Fatalf("queries = %#v", queries)
	}
	for _, fragment := range []string{"ALTER TABLE `analytics`.`events` DELETE", "ALTER TABLE `analytics`.`events` UPDATE", "INSERT INTO `analytics`.`events`"} {
		if !containsStringFragment(queries, fragment) {
			t.Fatalf("missing query fragment %q in %#v", fragment, queries)
		}
	}
}

func newClickHouseLegacyHTTPTestClient(t *testing.T, server *httptest.Server, settings clickhouse.Settings) *clickHouseLegacyHTTPClient {
	t.Helper()
	return newClickHouseLegacyHTTPTestClientWithOptions(t, server, func(opts *clickhouse.Options) {
		opts.Settings = settings
	})
}

func newClickHouseLegacyHTTPTestClientWithOptions(t *testing.T, server *httptest.Server, configure func(*clickhouse.Options)) *clickHouseLegacyHTTPClient {
	t.Helper()
	parsed, err := url.Parse(server.URL)
	if err != nil {
		t.Fatalf("parse test server URL: %v", err)
	}
	opts := &clickhouse.Options{
		Protocol:    clickhouse.HTTP,
		Addr:        []string{parsed.Host},
		Auth:        clickhouse.Auth{Database: "default"},
		DialTimeout: 2 * time.Second,
		ReadTimeout: 2 * time.Second,
	}
	if configure != nil {
		configure(opts)
	}
	client, err := newClickHouseLegacyHTTPClient(opts)
	if err != nil {
		t.Fatalf("new legacy HTTP client: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })
	return client
}

type legacyHTTPTestStreamConsumer struct {
	mu       sync.Mutex
	columns  []string
	values   [][]interface{}
	firstRow chan struct{}
	once     sync.Once
}

func (c *legacyHTTPTestStreamConsumer) SetColumns(columns []string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.columns = append([]string(nil), columns...)
	return nil
}

func (c *legacyHTTPTestStreamConsumer) ConsumeRow(map[string]interface{}) error {
	return fmt.Errorf("value consumer fast path was not used")
}

func (c *legacyHTTPTestStreamConsumer) ConsumeRowValues(values []interface{}) error {
	c.mu.Lock()
	c.values = append(c.values, append([]interface{}(nil), values...))
	c.mu.Unlock()
	c.once.Do(func() { close(c.firstRow) })
	return nil
}

func containsStringFragment(values []string, fragment string) bool {
	for _, value := range values {
		if strings.Contains(value, fragment) {
			return true
		}
	}
	return false
}
