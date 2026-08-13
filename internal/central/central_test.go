package central

import (
	"os"
	"testing"
)

func newTestStore(t *testing.T) *SQLiteStore {
	t.Helper()
	dir, err := os.MkdirTemp("", "central-test-*")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	s, err := NewSQLite(dir)
	if err != nil {
		t.Fatalf("NewSQLite: %v", err)
	}
	return s
}

func TestPing(t *testing.T) {
	s := newTestStore(t)
	if err := s.Ping(); err != nil {
		t.Fatalf("Ping: %v", err)
	}
}

func TestToken(t *testing.T) {
	s := newTestStore(t)

	tok, err := s.GetToken()
	if err != nil {
		t.Fatalf("GetToken: %v", err)
	}
	if tok != "" {
		t.Fatalf("expected empty, got %q", tok)
	}

	if err := s.SetToken("my-secret-token"); err != nil {
		t.Fatalf("SetToken: %v", err)
	}

	tok, err = s.GetToken()
	if err != nil {
		t.Fatalf("GetToken after set: %v", err)
	}
	if tok != "my-secret-token" {
		t.Fatalf("expected my-secret-token, got %q", tok)
	}
}

func TestModuleState(t *testing.T) {
	s := newTestStore(t)

	active, err := s.GetModuleState("plugins")
	if err != nil {
		t.Fatalf("GetModuleState: %v", err)
	}
	if active {
		t.Fatal("expected false for unset module")
	}

	if err := s.SetModuleState("plugins", true); err != nil {
		t.Fatalf("SetModuleState: %v", err)
	}

	active, err = s.GetModuleState("plugins")
	if err != nil {
		t.Fatalf("GetModuleState after set: %v", err)
	}
	if !active {
		t.Fatal("expected true after activation")
	}

	all, err := s.GetAllModuleStates()
	if err != nil {
		t.Fatalf("GetAllModuleStates: %v", err)
	}
	if !all["plugins"] {
		t.Fatal("GetAllModuleStates should contain plugins=true")
	}
}

func TestExportImport(t *testing.T) {
	s := newTestStore(t)
	s.SetToken("tok1")
	s.SetModuleState("modA", true)
	s.SetModuleState("modB", false)

	exp, err := s.Export()
	if err != nil {
		t.Fatalf("Export: %v", err)
	}
	if len(exp) != 3 {
		t.Fatalf("expected 3 keys, got %d", len(exp))
	}

	s2 := newTestStore(t)
	if err := s2.Import(exp); err != nil {
		t.Fatalf("Import: %v", err)
	}

	tok, _ := s2.GetToken()
	if tok != "tok1" {
		t.Fatalf("imported token = %q, want tok1", tok)
	}
	a, _ := s2.GetModuleState("modA")
	if !a {
		t.Fatal("imported modA should be true")
	}
}

func TestClose(t *testing.T) {
	s := newTestStore(t)
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}
