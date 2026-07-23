package main

import (
	"flag"
	"log"
	"net/http"
	"os"
	"opscore/internal/auth"
	"opscore/internal/handlers"
	"opscore/internal/metrics"
	"path/filepath"
	"strings"
)

func main() {
	metrics.Start()

	addr := ":8088"
	if env := os.Getenv("OPCORE_ADDR"); env != "" {
		addr = env
	}
	flagAddr := flag.String("addr", "", "监听地址,如 :8088 或 127.0.0.1:8088(默认 :8088,OPCORE_ADDR 可覆盖)")
	flagDist := flag.String("dist", "./web/dist", "前端静态资源目录(默认 ./web/dist,相对二进制路径)")
	flagData := flag.String("data", "", "数据目录(默认二进制同级 data/,用于配置/备份存储)")
	flag.Parse()
	if *flagAddr != "" {
		addr = *flagAddr
	}

	dataDir := *flagData
	if dataDir == "" {
		exe, _ := os.Executable()
		dataDir = filepath.Join(filepath.Dir(exe), "data")
	}
	auth.Init(dataDir)

	mux := http.NewServeMux()
	// ── 认证 API（不受中间件保护） ──
	mux.HandleFunc("/api/auth/token", auth.HandleToken)

	// ── 核心模块 API ──
	mux.HandleFunc("/api/manifest", handlers.Manifest)
	mux.HandleFunc("/api/core/resources", handlers.Resources)
	mux.HandleFunc("/api/core/disk/children", handlers.DiskChildren)
	mux.HandleFunc("/api/core/services", handlers.ServicesList)
	mux.HandleFunc("/api/core/services/action", handlers.ServiceAction)
	mux.HandleFunc("/api/core/services/logs", handlers.ServiceLogsHandler)
	mux.HandleFunc("/api/core/network", handlers.Network)
	mux.HandleFunc("/api/core/firewall", handlers.FirewallStatusHandler)
	mux.HandleFunc("/api/core/firewall/rules", handlers.FirewallRules)
	mux.HandleFunc("/api/core/firewall/action", handlers.FirewallAction)
	mux.HandleFunc("/api/core/firewall/audit", handlers.FirewallAudit)

	// ── 系统诊断 API ──
	mux.HandleFunc("/api/core/diagnostics", handlers.DiagnosticsInfo)
	mux.HandleFunc("/api/core/diagnostics/network", handlers.NetworkDiagHandler)
	mux.HandleFunc("/api/core/diagnostics/login-audit", handlers.LoginAuditHandler)
	mux.HandleFunc("/api/core/diagnostics/updates", handlers.UpdatesHandler)
	// ── 任务与存储 API ──
	mux.HandleFunc("/api/core/tasks/crontab", handlers.CrontabHandler)
	mux.HandleFunc("/api/core/tasks/disks", handlers.DisksHandler)
	mux.HandleFunc("/api/core/tasks/disks/action", handlers.DiskActionHandler)
	// ── 网络配置 API ──
	mux.HandleFunc("/api/core/network/config", handlers.NetConfigHandler)

	// ── 前端静态资源(SPA) ──
	fileServer := http.FileServer(http.Dir(*flagDist))
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		p := strings.TrimPrefix(r.URL.Path, "/")
		if p != "" && !strings.Contains(p, ".") {
			indexPath := *flagDist + "/index.html"
			if b, err := os.ReadFile(indexPath); err == nil {
				w.Header().Set("Content-Type", "text/html; charset=utf-8")
				w.Write(b)
				return
			}
		}
		// 设置正确的 Content-Type
		if strings.HasSuffix(p, ".css") {
			w.Header().Set("Content-Type", "text/css; charset=utf-8")
		} else if strings.HasSuffix(p, ".js") {
			w.Header().Set("Content-Type", "application/javascript")
		} else if strings.HasSuffix(p, ".svg") {
			w.Header().Set("Content-Type", "image/svg+xml")
		} else if strings.HasSuffix(p, ".woff") {
			w.Header().Set("Content-Type", "font/woff")
		} else if strings.HasSuffix(p, ".woff2") {
			w.Header().Set("Content-Type", "font/woff2")
		}
		fileServer.ServeHTTP(w, r)
	})

	log.Println("OpsCore demo 已启动 -> http://" + addr)
	log.Fatal(http.ListenAndServe(addr, cors(auth.Middleware(mux))))
}

func cors(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		h.ServeHTTP(w, r)
	})
}
