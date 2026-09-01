package sqlaudit

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestBuildExportIgnoresPaginationAndFiltersAllMatches(t *testing.T) {
	store := openTestStore(t)
	now := time.Now().UnixMilli()
	for index := 0; index < 3; index++ {
		event := sampleEvent("export-"+string(rune('a'+index)), now+int64(index))
		event.ConnectionID = "export-connection"
		if err := store.Append(event); err != nil {
			t.Fatalf("Append returned error: %v", err)
		}
	}
	other := sampleEvent("other", now+10)
	other.ConnectionID = "other-connection"
	if err := store.Append(other); err != nil {
		t.Fatalf("Append other returned error: %v", err)
	}

	payload, err := store.BuildExport(Filter{
		ConnectionID: "export-connection",
		Page:         3,
		PageSize:     1,
	}, "json")
	if err != nil {
		t.Fatalf("BuildExport(json) returned error: %v", err)
	}
	var events []Event
	if err := json.Unmarshal(payload, &events); err != nil {
		t.Fatalf("decode JSON export: %v\npayload=%s", err, payload)
	}
	if len(events) != 3 {
		t.Fatalf("export should ignore pagination and include 3 filtered events, got %d", len(events))
	}
	for _, event := range events {
		if event.ConnectionID != "export-connection" {
			t.Fatalf("export ignored non-pagination filter: %#v", event)
		}
	}
}

func TestBuildCSVExportPreventsFormulaInjection(t *testing.T) {
	store := openTestStore(t)
	event := sampleEvent("csv-formula", time.Now().UnixMilli())
	event.ConnectionID = "=HYPERLINK(\"https://example.test\")"
	event.Database = " @SUM(1+1)"
	if err := store.Append(event); err != nil {
		t.Fatalf("Append returned error: %v", err)
	}
	payload, err := store.BuildExport(Filter{}, "CSV")
	if err != nil {
		t.Fatalf("BuildExport(csv) returned error: %v", err)
	}
	reader := csv.NewReader(bytes.NewReader(payload))
	records, err := reader.ReadAll()
	if err != nil {
		t.Fatalf("read CSV export: %v\npayload=%s", err, payload)
	}
	if len(records) != 2 {
		t.Fatalf("CSV export rows=%d, want header + event", len(records))
	}
	connectionColumn := csvHeaderIndex(t, records[0], "connectionId")
	databaseColumn := csvHeaderIndex(t, records[0], "database")
	if !strings.HasPrefix(records[1][connectionColumn], "'=") {
		t.Fatalf("connection ID formula was not escaped: %q", records[1][connectionColumn])
	}
	if !strings.HasPrefix(records[1][databaseColumn], "'@") {
		t.Fatalf("whitespace-prefixed formula was not escaped: %q", records[1][databaseColumn])
	}
}

func TestBuildExportRejectsUnsupportedFormat(t *testing.T) {
	store := openTestStore(t)
	_, err := store.BuildExport(Filter{}, "xlsx")
	if !errors.Is(err, ErrUnsupportedExportFormat) {
		t.Fatalf("BuildExport error=%v, want ErrUnsupportedExportFormat", err)
	}
}

func TestBuildExportReturnsRecognizableRecordLimitError(t *testing.T) {
	store := openTestStore(t)
	now := time.Now().UnixMilli()
	if err := store.Append(sampleEvent("limit-a", now)); err != nil {
		t.Fatalf("Append first returned error: %v", err)
	}
	if err := store.Append(sampleEvent("limit-b", now+1)); err != nil {
		t.Fatalf("Append second returned error: %v", err)
	}
	for _, format := range []string{"json", "csv"} {
		if _, err := store.buildExportWithLimits(Filter{}, format, 1, maxExportBytes); !errors.Is(err, ErrExportRecordLimit) {
			t.Fatalf("%s export error=%v, want ErrExportRecordLimit", format, err)
		}
	}
}

func TestBuildExportReturnsRecognizableSizeLimitError(t *testing.T) {
	store := openTestStore(t)
	event := sampleEvent("large-export", time.Now().UnixMilli())
	event.Database = strings.Repeat("database", 30)
	if err := store.Append(event); err != nil {
		t.Fatalf("Append returned error: %v", err)
	}
	for _, format := range []string{"json", "csv"} {
		if _, err := store.buildExportWithLimits(Filter{}, format, maxExportRecords, 64); !errors.Is(err, ErrExportSizeLimit) {
			t.Fatalf("%s export error=%v, want ErrExportSizeLimit", format, err)
		}
	}
}

func TestLoadExportEventsClosesRowsBeforeReturning(t *testing.T) {
	store := openTestStore(t)
	now := time.Now().UnixMilli()
	if err := store.Append(sampleEvent("export-connection-release", now)); err != nil {
		t.Fatalf("Append returned error: %v", err)
	}
	events, err := store.loadExportEvents(normalizeFilter(Filter{}), maxExportRecords, maxExportBytes)
	if err != nil || len(events) != 1 {
		t.Fatalf("loadExportEvents events=%#v err=%v", events, err)
	}
	if inUse := store.db.Stats().InUse; inUse != 0 {
		t.Fatalf("loadExportEvents returned before releasing SQLite rows: inUse=%d", inUse)
	}

	done := make(chan error, 1)
	go func() {
		done <- store.Append(sampleEvent("append-after-export-load", now+1))
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Append after export load returned error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("export row cursor retained the store's only SQLite connection")
	}
}

func csvHeaderIndex(t *testing.T, header []string, name string) int {
	t.Helper()
	for index, value := range header {
		if value == name {
			return index
		}
	}
	t.Fatalf("CSV header %q missing from %#v", name, header)
	return -1
}
