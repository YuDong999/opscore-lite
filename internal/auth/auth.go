package auth

import (
	"encoding/json"
	"log"
	"net/http"
	"opscore/internal/central"
	"strings"
	"sync"
)

var (
	store central.CentralStore
	cfg   Config
	cfgMu sync.RWMutex
)

type Config struct {
	Token string `json:"token,omitempty"`
}

func Init(cs central.CentralStore) {
	store = cs
	tok, err := cs.GetToken()
	if err != nil {
		log.Printf("[auth] load token: %v", err)
		return
	}
	cfgMu.Lock()
	cfg.Token = tok
	cfgMu.Unlock()
}

func Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/auth/") {
			next.ServeHTTP(w, r)
			return
		}

		cfgMu.RLock()
		t := cfg.Token
		cfgMu.RUnlock()

		if t == "" {
			next.ServeHTTP(w, r)
			return
		}

		auth := r.Header.Get("Authorization")
		if auth == "Bearer "+t {
			next.ServeHTTP(w, r)
			return
		}

		if r.URL.Query().Get("token") == t {
			next.ServeHTTP(w, r)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		json.NewEncoder(w).Encode(map[string]string{"error": "unauthorized"})
	})
}

func GetToken() string {
	cfgMu.RLock()
	defer cfgMu.RUnlock()
	return cfg.Token
}

func SetToken(t string) {
	cfgMu.Lock()
	cfg.Token = t
	cfgMu.Unlock()
	if store != nil {
		if err := store.SetToken(t); err != nil {
			log.Printf("[auth] persist token: %v", err)
		}
	}
}

func HandleToken(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	switch r.Method {
	case http.MethodGet:
		cfgMu.RLock()
		t := cfg.Token
		cfgMu.RUnlock()
		json.NewEncoder(w).Encode(map[string]string{
			"token":     t,
			"configured": boolStr(t != ""),
		})
	case http.MethodPost:
		var body struct {
			Token string `json:"token"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			json.NewEncoder(w).Encode(map[string]string{"error": "invalid body"})
			return
		}
		SetToken(body.Token)
		json.NewEncoder(w).Encode(map[string]string{"ok": "true"})
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func boolStr(b bool) string {
	if b {
		return "true"
	}
	return "false"
}
