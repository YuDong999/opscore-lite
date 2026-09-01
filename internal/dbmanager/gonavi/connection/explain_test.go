package connection

import (
	"encoding/json"
	"testing"
)

func TestQueryExecutionRecordJSONIncludesFalseDiagnosable(t *testing.T) {
	record := QueryExecutionRecord{
		SQLText:        "UPDATE users SET active = 1",
		Diagnosable:    false,
		StatementCount: 1,
	}

	payload, err := json.Marshal(record)
	if err != nil {
		t.Fatalf("json.Marshal returned error: %v", err)
	}

	var fields map[string]any
	if err := json.Unmarshal(payload, &fields); err != nil {
		t.Fatalf("json.Unmarshal returned error: %v", err)
	}
	diagnosable, ok := fields["diagnosable"]
	if !ok {
		t.Fatalf("diagnosable=false must be included in the frontend payload: %s", payload)
	}
	if value, ok := diagnosable.(bool); !ok || value {
		t.Fatalf("diagnosable must be the boolean false, got %#v", diagnosable)
	}
}
