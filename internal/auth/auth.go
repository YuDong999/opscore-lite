package auth

import (
	"encoding/json"
	"log"
	"net/http"
	"opscore/internal/store"
	"strings"
	"sync"
)

var (
	cfgStore  *store.JSONFile
	cfg       Config
	cfgMu     sync.RWMutex
)

type Config struct {
	Token string `json:"token,omitempty"`
}

func Init(dir string) {
	var err error
	cfgStore, err = store.New(dir, "config.json")
	if err != nil {
		log.Printf("[auth] store init failed: %v", err)
		return
	}
	cfgStore.Read(&cfg)
}

// Middleware 校验 Bearer token；未设置 token 时放行。
func Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// /api/auth/* 不受认证保护
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

		// 也支持 URL query ?token=xxx（用于 SSE / 日志流）
		if r.URL.Query().Get("token") == t {
			next.ServeHTTP(w, r)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		json.NewEncoder(w).Encode(map[string]string{"error": "unauthorized"})
	})
}

// GetToken 返回当前 token。
func GetToken() string {
	cfgMu.RLock()
	defer cfgMu.RUnlock()
	return cfg.Token
}

// SetToken 更新 token。
func SetToken(t string) {
	cfgMu.Lock()
	cfg.Token = t
	cfgMu.Unlock()
	if cfgStore != nil {
		cfgStore.Write(&cfg)
	}
}

// HandleToken 处理 GET/POST /api/auth/token
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
