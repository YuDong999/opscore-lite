package registry

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func dummyHandler(w http.ResponseWriter, r *http.Request) {
	w.Write([]byte("ok"))
}

func TestRegistry(t *testing.T) {
	r := New()
	m := &Module{
		Manifest: Manifest{ID: "test", Name: "Test", Icon: "cpu", RoutePath: "/test", Group: "core"},
		Routes:   []Route{{Path: "/api/test", Handler: dummyHandler}},
	}
	r.Register(m)

	all := r.All()
	if len(all) != 1 {
		t.Fatalf("expected 1 module, got %d", len(all))
	}
	if all[0].Manifest.ID != "test" {
		t.Fatalf("expected test, got %s", all[0].Manifest.ID)
	}

	active := r.Active()
	if len(active) != 1 {
		t.Fatalf("expected 1 active, got %d", len(active))
	}

	mux := http.NewServeMux()
	r.RegisterRoutes(mux)
	req := httptest.NewRequest("GET", "/api/test", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Body.String() != "ok" {
		t.Fatalf("expected ok, got %s", w.Body.String())
	}
}
