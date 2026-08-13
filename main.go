package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"opscore/internal/agent"
	"opscore/internal/ansible"
	"opscore/internal/auth"
	"opscore/internal/central"
	"opscore/internal/handlers"
	"opscore/internal/metrics"
	"opscore/internal/module"
	"opscore/internal/registry"
	"opscore/internal/remote"
)

func main() {
	metrics.Start()

	addr := ":8088"
	if env := os.Getenv("OPCORE_ADDR"); env != "" {
		addr = env
	}
	flagAddr := flag.String("addr", "", "监听地址,如 :8088 或 127.0.0.1:8088(默认 :8088,OPCORE_ADDR 可覆盖)")
	flagDist := flag.String("dist", "", "前端静态资源目录(默认二进制同级 web/dist,与数据目录定位一致)")
	flagData := flag.String("data", "", "数据目录(默认二进制同级 data/,用于配置/备份存储)")
	flagDB := flag.String("database", "", "数据库 DSN (默认 sqlite://<dataDir>/opscore.db, postgres://user:pass@host/db 可覆盖)")
	flagAgentAddr := flag.String("agent-addr", ":8089", "Agent WebSocket 监听地址,如 :8089(默认 :8089,OPCORE_AGENT_LISTEN 可覆盖)")
	flag.Parse()
	if *flagAddr != "" {
		addr = *flagAddr
	}

	dataDir := *flagData
	if dataDir == "" {
		exe, _ := os.Executable()
		dataDir = filepath.Join(filepath.Dir(exe), "data")
	}
	os.MkdirAll(dataDir, 0755)

	distDir := *flagDist
	if distDir == "" {
		exe, _ := os.Executable()
		distDir = filepath.Join(filepath.Dir(exe), "web", "dist")
	}

	logFile, err := os.OpenFile(filepath.Join(dataDir, "opscore.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err == nil {
		log.SetOutput(io.MultiWriter(os.Stderr, logFile))
	}

	dbDSN := *flagDB
	if dbDSN == "" {
		dbDSN = os.Getenv("OPSCORE_DATABASE_URL")
	}

	var cs central.CentralStore
	if strings.HasPrefix(dbDSN, "postgres://") || strings.HasPrefix(dbDSN, "postgresql://") {
		cs, err = central.NewPostgres(dbDSN)
		if err != nil {
			log.Fatalf("init postgres store: %v", err)
		}
		log.Println("数据库: PostgreSQL")
	} else {
		cs, err = central.NewSQLite(dataDir)
		if err != nil {
			log.Fatalf("init sqlite store: %v", err)
		}
		log.Println("数据库: SQLite (可通过 Web UI 迁移到 PostgreSQL)")
	}
	defer cs.Close()
	auth.Init(cs)
	module.InitPluginStore(cs)

	ansibleMgr, err := ansible.NewManager(dataDir)
	if err != nil {
		log.Fatalf("init ansible manager: %v", err)
	}
	handlers.InitAnsible(ansibleMgr)
	sshPool := remote.NewPool()
	handlers.InitPool(sshPool)
	defer sshPool.Close()

	agentAddr := os.Getenv("OPCORE_AGENT_LISTEN")
	if agentAddr == "" {
		if *flagAgentAddr != "" {
			agentAddr = *flagAgentAddr
		} else {
			agentAddr = ":8089"
		}
	}
	agentHub := agent.NewHub()
	if env := os.Getenv("OPCORE_AGENT_SERVER"); env != "" {
		agent.AgentServerAddr = env
		agent.AgentServerAddrExplicit = true
	} else {
		agent.AgentServerAddr = resolveAgentAddr(agentAddr)
	}
	if _, apPort, apErr := net.SplitHostPort(agentAddr); apErr == nil && apPort != "" {
		agent.AgentServerPort = apPort
	}
	handlers.InitAgentHub(agentHub)

	existingHosts := ansibleMgr.ListHosts()
	if len(existingHosts) > 0 {
		log.Printf("[agent] 发现 %d 个已有主机, 尝试部署 agent...", len(existingHosts))
		agent.DeployToAll(sshPool, existingHosts)
	}
	agent.StartWakeLoop(agentHub, sshPool, func() []ansible.Host {
		return ansibleMgr.ListHosts()
	})

	muxAgent := http.NewServeMux()
	muxAgent.HandleFunc("/ws/agent", agentHub.ServeWS)
	agentWS := &http.Server{Addr: agentAddr, Handler: muxAgent}
	go func() {
		log.Println("Agent WebSocket 服务启动 -> ws://" + agentAddr)
		if err := agentWS.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Printf("agent ws server: %v", err)
		}
	}()

	reg := registry.New()
	registerCoreModules(reg)

	mux := http.NewServeMux()

	// 认证 API（不受中间件保护）
	mux.HandleFunc("/api/auth/token", auth.HandleToken)

	// Manifest — 从注册中心读取活跃模块
	mux.HandleFunc("/api/manifest", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(reg.Active())
	})

	// 模块路由 — 从注册中心自动挂载
	reg.RegisterRoutes(mux)

	// 系统管理 API（硬编码，不属于任何模块）
	mux.HandleFunc("/api/system/migration-status", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		status, err := central.GetMigrationStatus(cs)
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
			return
		}
		json.NewEncoder(w).Encode(status)
	})
	mux.HandleFunc("/api/system/migrate", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		var body struct {
			DSN string `json:"dsn"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			json.NewEncoder(w).Encode(map[string]string{"error": "invalid body"})
			return
		}
		sqliteStore, ok := cs.(*central.SQLiteStore)
		if !ok {
			w.WriteHeader(http.StatusBadRequest)
			json.NewEncoder(w).Encode(map[string]string{"error": "当前数据库不是 SQLite，无需迁移"})
			return
		}
		result, err := central.MigrateFromSQLite(sqliteStore, body.DSN)
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
			return
		}
		json.NewEncoder(w).Encode(result)
	})

	// 前端静态资源(SPA)
	fileServer := http.FileServer(http.Dir(distDir))
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		p := strings.TrimPrefix(r.URL.Path, "/")
		if p != "" && !strings.Contains(p, ".") {
			indexPath := distDir + "/index.html"
			if b, err := os.ReadFile(indexPath); err == nil {
				w.Header().Set("Content-Type", "text/html; charset=utf-8")
				w.Header().Set("Cache-Control", "no-cache")
				w.Write(b)
				return
			}
		}
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
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		fileServer.ServeHTTP(w, r)
	})

	log.Println(banner)
	log.Println("OpsCore demo 已启动 -> http://" + addr)
	log.Println("日志文件: " + filepath.Join(dataDir, "opscore.log"))
	log.Fatal(http.ListenAndServe(addr, cors(auth.Middleware(mux))))
}

// resolveAgentAddr returns the WebSocket URL for remote agents to connect back.
// Priority: OPCORE_AGENT_SERVER env > auto-detect non-loopback IP > 127.0.0.1 (warning)
func resolveAgentAddr(listenAddr string) string {
	if env := os.Getenv("OPCORE_AGENT_SERVER"); env != "" {
		log.Printf("[agent] 使用环境变量 OPCORE_AGENT_SERVER=%s", env)
		return env
	}
	_, port, err := net.SplitHostPort(listenAddr)
	if err != nil {
		port = listenAddr
	}
	portPart := ":" + port

	// isRFC1918 判断是否为内网私有地址（优先选择）
	isRFC1918 := func(ip net.IP) bool {
		if ip[0] == 10 { return true }
		if ip[0] == 172 && ip[1] >= 16 && ip[1] <= 31 { return true }
		if ip[0] == 192 && ip[1] == 168 { return true }
		return false
	}

	var best, fallback string
	addrs, err := net.InterfaceAddrs()
	if err == nil {
		for _, a := range addrs {
			ipnet, ok := a.(*net.IPNet)
			if !ok || ipnet.IP.IsLoopback() || ipnet.IP.To4() == nil {
				continue
			}
			url := fmt.Sprintf("ws://%s%s/ws/agent", ipnet.IP.String(), portPart)
			if isRFC1918(ipnet.IP) && best == "" {
				best = url
			} else if fallback == "" {
				fallback = url
			}
		}
	}
	chosen := best
	if chosen == "" {
		chosen = fallback
	}
	if chosen != "" {
		log.Printf("[agent] Agent 服务地址: %s", chosen)
		if best == "" {
			log.Printf("[agent] 注意: 未检测到内网 IP, 可能无法从远端主机连接, 请设置 OPCORE_AGENT_SERVER")
		}
		return chosen
	}
	log.Println("[agent] 警告: 无法检测本机 IP, 请设置 OPCORE_AGENT_SERVER 环境变量")
	return fmt.Sprintf("ws://127.0.0.1%s/ws/agent", portPart)
}

func registerCoreModules(r *registry.Registry) {
	man := func(id, name, icon, routePath, group, desc string) registry.Manifest {
		return registry.Manifest{ID: id, Name: name, Icon: icon, RoutePath: routePath, Group: group, Description: desc}
	}

	type modCfg struct {
		m       registry.Manifest
		routes  []registry.Route
	}

	modules := []modCfg{
		{man("resources", "系统资源", "cpu", "/resources", "core", "CPU / 内存 / 磁盘 / 网络 实时多图式可视化"), []registry.Route{
			{Path: "/api/core/resources", Handler: handlers.Resources},
			{Path: "/api/core/resources/overview", Handler: handlers.MultiOverview},
			{Path: "/api/core/disk/children", Handler: handlers.DiskChildren},
		}},
		{man("services", "服务发现", "server", "/services", "core", "运行服务启停 / 重启,查看单元文件与日志位置,容器与站点检测"), []registry.Route{
			{Path: "/api/core/services", Handler: handlers.ServicesList},
			{Path: "/api/core/services/action", Handler: handlers.ServiceAction},
			{Path: "/api/core/services/logs", Handler: handlers.ServiceLogsHandler},
			{Path: "/api/core/apps", Handler: handlers.AppsHandler},
			{Path: "/api/core/apps/containers/detail", Handler: handlers.AppContainerDetailHandler},
			{Path: "/api/core/apps/sites/stats", Handler: handlers.AppSiteStatsHandler},
		}},
		{man("network", "防火墙和网络", "network", "/network", "core", "网络接口 / 监听端口 / 防火墙状态与规则(高危,需确认+审计)"), []registry.Route{
			{Path: "/api/core/network", Handler: handlers.Network},
			{Path: "/api/core/network/config", Handler: handlers.NetConfigHandler},
			{Path: "/api/core/network/topology", Handler: handlers.TopologyHandler},
			{Path: "/api/core/netconfig/connections", Handler: handlers.NetConnections},
			{Path: "/api/core/netconfig/connection", Handler: handlers.NetConnectionAction},
			{Path: "/api/core/firewall", Handler: handlers.FirewallStatusHandler},
			{Path: "/api/core/firewall/rules", Handler: handlers.FirewallRules},
			{Path: "/api/core/firewall/action", Handler: handlers.FirewallAction},
			{Path: "/api/core/firewall/audit", Handler: handlers.FirewallAudit},
			{Path: "/api/core/firewall/zones", Handler: handlers.FirewallZones},
			{Path: "/api/core/firewall/rich-rules", Handler: handlers.FirewallRichRules},
			{Path: "/api/core/firewall/forward-ports", Handler: handlers.FirewallForwardPorts},
			{Path: "/api/core/lldp", Handler: handlers.LldpHandler},
		}},
		{man("diagnostics", "系统诊断", "activity", "/diagnostics", "core", "网络诊断 / 登录审计 / 系统更新"), []registry.Route{
			{Path: "/api/core/diagnostics", Handler: handlers.DiagnosticsInfo},
			{Path: "/api/core/diagnostics/network", Handler: handlers.NetworkDiagHandler},
			{Path: "/api/core/diagnostics/login-audit", Handler: handlers.LoginAuditHandler},
			{Path: "/api/core/diagnostics/updates", Handler: handlers.UpdatesHandler},
			{Path: "/api/core/diagnostics/updates/install", Handler: handlers.UpdatesInstallHandler},
		}},
		{man("tasks", "任务与存储", "clipboard", "/tasks", "core", "定时任务 / 磁盘挂载 / LVM 管理 / SMART 健康"), []registry.Route{
			{Path: "/api/core/tasks/crontab", Handler: handlers.CrontabHandler},
			{Path: "/api/core/tasks/disks", Handler: handlers.DisksHandler},
			{Path: "/api/core/tasks/disks/action", Handler: handlers.DiskActionHandler},
			{Path: "/api/core/lvm", Handler: handlers.LvmHandler},
		}},
		{man("ansible", "Ansible 多机管理", "terminal", "/ansible", "core", "批量主机管理 / 库存清单 / Playbook / Ad-hoc"), []registry.Route{
			{Path: "/api/ansible/hosts", Handler: handlers.AnsibleHostsList},
			{Path: "/api/ansible/hosts/add", Handler: handlers.AnsibleHostsAdd},
			{Path: "/api/ansible/hosts/batch-add", Handler: handlers.AnsibleHostsBatchAdd},
			{Path: "/api/ansible/hosts/remove", Handler: handlers.AnsibleHostsRemove},
			{Path: "/api/ansible/hosts/update", Handler: handlers.AnsibleHostsUpdate},
			{Path: "/api/ansible/hosts/test", Handler: handlers.AnsibleHostsTest},
			{Path: "/api/ansible/hosts/status", Handler: handlers.HostsStatus},
			{Path: "/api/ansible/ping", Handler: handlers.AnsiblePing},
			{Path: "/api/ansible/adhoc", Handler: handlers.AnsibleAdhoc},
			{Path: "/api/ansible/playbook/exec", Handler: handlers.AnsiblePlaybookExec},
			{Path: "/api/ansible/sse/ping", Handler: handlers.AnsibleSSEPing},
			{Path: "/api/ansible/sse/adhoc", Handler: handlers.AnsibleSSEAdhoc},
			{Path: "/api/ansible/sse/playbook", Handler: handlers.AnsibleSSEPlaybookExec},
			{Path: "/api/ansible/inventories", Handler: handlers.AnsibleInventoriesList},
			{Path: "/api/ansible/inventory/get", Handler: handlers.AnsibleInventoryGet},
			{Path: "/api/ansible/inventory/create", Handler: handlers.AnsibleInventoryCreate},
			{Path: "/api/ansible/inventory/delete", Handler: handlers.AnsibleInventoryDelete},
			{Path: "/api/ansible/inventory/render", Handler: handlers.AnsibleInventoryRender},
			{Path: "/api/ansible/inventory/import-hosts", Handler: handlers.AnsibleInventoryImportHosts},
			{Path: "/api/ansible/inventory/save", Handler: handlers.AnsibleInventorySave},
			{Path: "/api/ansible/inventory/host-add", Handler: handlers.AnsibleInventoryHostAdd},
			{Path: "/api/ansible/inventory/host-remove", Handler: handlers.AnsibleInventoryHostRemove},
			{Path: "/api/ansible/inventory/host-groups", Handler: handlers.AnsibleHostGroups},
			{Path: "/api/ansible/host-groups", Handler: handlers.HostGroupsHandler},
			{Path: "/api/ansible/playbooks", Handler: handlers.AnsiblePlaybooksList},
			{Path: "/api/ansible/playbook/get", Handler: handlers.AnsiblePlaybookGet},
			{Path: "/api/ansible/playbook/save", Handler: handlers.AnsiblePlaybookSave},
			{Path: "/api/ansible/playbook/delete", Handler: handlers.AnsiblePlaybookDelete},
			{Path: "/api/ansible/history", Handler: handlers.AnsibleHistory},
			{Path: "/api/ansible/history/clear", Handler: handlers.AnsibleHistoryClear},
			{Path: "/api/ansible/history/rerun", Handler: handlers.AnsibleHistoryRerun},
			{Path: "/api/ansible/templates", Handler: handlers.AnsibleTemplatesList},
			{Path: "/api/ansible/template/create", Handler: handlers.AnsibleTemplateCreate},
			{Path: "/api/ansible/ssh/keys", Handler: handlers.SSHCmdListKeys},
			{Path: "/api/ansible/ssh/generate", Handler: handlers.SSHCmdGenerate},
			{Path: "/api/ansible/ssh/delete", Handler: handlers.SSHCmdDeleteKey},
			{Path: "/api/ansible/ssh/deploy", Handler: handlers.SSHCmdDeploy},
			{Path: "/api/ansible/ssh/test", Handler: handlers.SSHCmdTest},
			{Path: "/api/ansible/ssh/bind", Handler: handlers.SSHCmdBind},
		}},
		{man("plugins", "插件中心", "puzzle", "/plugins", "plugin", "可插拔模块管理"), []registry.Route{
			{Path: "/api/plugins", Handler: handlers.PluginList},
			{Path: "/api/plugins/", Handler: handlers.PluginAction},
		}},
	}

	for _, m := range modules {
		if m.m.Group == "plugin" && m.m.ID != "plugins" && !module.IsPluginActive(m.m.ID) {
			continue
		}
		r.Register(&registry.Module{Manifest: m.m, Routes: m.routes})
	}
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

const banner = `
  ============================================

 ██████╗ ██████╗ ███████╗ ██████╗ ██████╗ ██████╗ ███████╗
██╔═══██╗██╔══██╗██╔════╝██╔════╝██╔═══██╗██╔══██╗██╔════╝
██║   ██║██████╔╝███████╗██║     ██║   ██║██████╔╝█████╗  
██║   ██║██╔═══╝ ╚════██║██║     ██║   ██║██╔══██╗██╔══╝  
╚██████╔╝██║     ███████║╚██████╗╚██████╔╝██║  ██║███████╗
 ╚═════╝ ╚═╝     ╚══════╝ ╚═════╝ ╚═════╝ ╚═╝  ╚═╝╚══════╝
                                                       
        opscore starting...

      :::        ::::::::::: ::::::::::: :::::::::: 
     :+:            :+:         :+:     :+:         
    +:+            +:+         +:+     +:+          
   +#+            +#+         +#+     +#++:++#      
  +#+            +#+         +#+     +#+            
 #+#            #+#         #+#     #+#             
########## ###########     ###     ##########       

  ============================================`
