package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"opscore/internal/agent"
	"opscore/internal/ansible"
	"opscore/internal/auth"
	"opscore/internal/central"
	"opscore/internal/cicd"
	"opscore/internal/dbmanager"
	"opscore/internal/kubernetes"
	"opscore/internal/handlers"
	"opscore/internal/hostkey"
	"opscore/internal/logmonitor"
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
	flagTLSCert := flag.String("tls-cert", "", "TLS 证书路径(设置后启用 HTTPS)")
	flagTLSKey := flag.String("tls-key", "", "TLS 私钥路径(与 -tls-cert 同时设置生效)")
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
	hostkey.SetDataDir(dataDir)

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

	// K8s 多集群管理: kubeconfig 凭据落盘 data/kubeconfigs/, 元数据经 central 持久化
	k8sMgr := kubernetes.NewManager()
	handlers.InitK8s(k8sMgr, filepath.Join(dataDir, "kubeconfigs"), func() central.CentralStore { return cs })
	handlers.StartK8sMetricsSampler(dataDir)

	ansibleMgr, err := ansible.NewManager(dataDir)
	if err != nil {
		log.Fatalf("init ansible manager: %v", err)
	}
	handlers.InitAnsible(ansibleMgr)
	sshPool := remote.NewPool()
	handlers.InitPool(sshPool)
	defer sshPool.Close()

	// CI/CD 引擎: 领域层(internal/cicd)与执行通道(handlers.CicdExec)在此缝合
	cicdEngine, err := cicd.NewEngine(dataDir)
	if err != nil {
		log.Fatalf("init cicd engine: %v", err)
	}
	cicdEngine.Exec = handlers.CicdExec
	cicdEngine.Collect = handlers.CicdCollect
	cicdEngine.Push = handlers.CicdPush
	handlers.InitCicd(cicdEngine)
	defer cicdEngine.Stop()

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

	// DB 管理模块(独立初始化, 需要访问 cs + 加密密钥)
	dbStore := dbmanager.NewStore(func() central.CentralStore { return cs }, auth.GetToken())
	dbPool := dbmanager.NewDatabasePool(dbStore, 8)
	dbMod := dbmanager.Module(dbStore, dbPool)
	reg.Register(dbMod)
	defer dbPool.Close()

	// 日志监控模块(独立包, 使用 SQLite 元数据索引 + 分级文件存储)
	logStore, err := logmonitor.RequireStore(filepath.Join(dataDir, "logmeta.db"))
	if err != nil {
		log.Fatalf("init log monitor store: %v", err)
	}
	defer logStore.Close()
	logSvc := logmonitor.NewService(logStore)
	lmMod := logmonitor.Module(logStore, logSvc, filepath.Join(dataDir, "logs"))
	reg.Register(lmMod)

	mux := http.NewServeMux()

	// 认证 API（不受中间件保护）
	mux.HandleFunc("/api/auth/token", auth.HandleToken)

	// Manifest — 从注册中心读取模块; 插件按激活状态实时过滤(接入/停用热生效, 无需重启)
	mux.HandleFunc("/api/manifest", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		all := reg.Active()
		fmt.Printf("DEBUG: All modules: %v\n", all)
		out := make([]registry.Manifest, 0, len(all))
		for _, m := range all {
			if m.Group == "plugin" && m.ID != "plugins" && !module.IsPluginActive(m.ID) {
				continue
			}
			out = append(out, m)
		}
		fmt.Printf("DEBUG: Filtered modules: %v\n", out)
		json.NewEncoder(w).Encode(out)
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
	handler := cors(auth.Middleware(mux))
	if *flagTLSCert != "" && *flagTLSKey != "" {
		log.Printf("HTTPS 监听 %s (cert=%s)", addr, *flagTLSCert)
		log.Fatal(http.ListenAndServeTLS(addr, *flagTLSCert, *flagTLSKey, handler))
		return
	}
	if auth.GetToken() == "" {
		log.Printf("[安全警告] 未设置访问 Token 且未启用 TLS, 所有 API 处于无鉴权明文状态; 生产环境请 POST /api/auth/token 设置并配合 -tls-cert/-tls-key 或反向代理使用")
	}
	log.Fatal(http.ListenAndServe(addr, handler))
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
		if ip[0] == 10 {
			return true
		}
		if ip[0] == 172 && ip[1] >= 16 && ip[1] <= 31 {
			return true
		}
		if ip[0] == 192 && ip[1] == 168 {
			return true
		}
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
		m      registry.Manifest
		routes []registry.Route
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
			{Path: "/api/core/platform/inventory", Handler: handlers.PlatformInventoryHandler},
			{Path: "/api/core/platform/profile", Handler: handlers.PlatformProfileHandler},
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
		{man("cicd", "CI/CD 流水线", "cicd", "/cicd", "core", "流水线编排 / 多主机构建部署 / Webhook·定时·手动触发 / 实时日志"), []registry.Route{
			{Path: "/api/cicd/pipelines", Handler: handlers.CicdPipelines},
			{Path: "/api/cicd/pipeline/get", Handler: handlers.CicdPipelineGet},
			{Path: "/api/cicd/pipeline/save", Handler: handlers.CicdPipelineSave},
			{Path: "/api/cicd/pipeline/delete", Handler: handlers.CicdPipelineDelete},
			{Path: "/api/cicd/pipeline/run", Handler: handlers.CicdPipelineRun},
			{Path: "/api/cicd/run/cancel", Handler: handlers.CicdRunCancel},
			{Path: "/api/cicd/run/approve", Handler: handlers.CicdRunApprove},
			{Path: "/api/cicd/artifact/download", Handler: handlers.CicdArtifactDownload},
			{Path: "/api/cicd/pipeline/export", Handler: handlers.CicdPipelineExport},
			{Path: "/api/cicd/pipeline/import", Handler: handlers.CicdPipelineImport},
			{Path: "/api/cicd/pipeline/nextfire", Handler: handlers.CicdNextFire},
			{Path: "/api/cicd/runs", Handler: handlers.CicdRuns},
			{Path: "/api/cicd/run/get", Handler: handlers.CicdRunGet},
			{Path: "/api/cicd/run/log", Handler: handlers.CicdRunLog},
			{Path: "/api/cicd/run/stream", Handler: handlers.CicdRunStream},
			{Path: "/api/cicd/webhook/", Handler: handlers.CicdWebhook},
			{Path: "/api/cicd/overview", Handler: handlers.CicdOverview},
			{Path: "/api/cicd/credentials", Handler: handlers.CicdCredentials},
			{Path: "/api/cicd/credential/save", Handler: handlers.CicdCredentialSave},
			{Path: "/api/cicd/credential/delete", Handler: handlers.CicdCredentialDelete},
			{Path: "/api/cicd/repos", Handler: handlers.CicdRepos},
			{Path: "/api/cicd/repo/save", Handler: handlers.CicdRepoSave},
			{Path: "/api/cicd/repo/delete", Handler: handlers.CicdRepoDelete},
			{Path: "/api/cicd/repo/test", Handler: handlers.CicdRepoTest},
			{Path: "/api/cicd/registries", Handler: handlers.CicdRegistries},
			{Path: "/api/cicd/registry/save", Handler: handlers.CicdRegistrySave},
			{Path: "/api/cicd/registry/delete", Handler: handlers.CicdRegistryDelete},
			{Path: "/api/cicd/registry/test", Handler: handlers.CicdRegistryTest},
			{Path: "/api/cicd/scripts", Handler: handlers.CicdScripts},
			{Path: "/api/cicd/script/save", Handler: handlers.CicdScriptSave},
			{Path: "/api/cicd/script/delete", Handler: handlers.CicdScriptDelete},
		}},
		{man("plugins", "插件中心", "puzzle", "/plugins", "plugin", "可插拔模块管理"), []registry.Route{
			{Path: "/api/plugins", Handler: handlers.PluginList},
			{Path: "/api/plugins/", Handler: handlers.PluginAction},
		}},
		{man("containers", "容器管理", "box", "/containers", "plugin", "Docker 管理(启停/删除/日志/镜像/连接走向/策略修改) + Kubernetes 多集群管理(只读)"), []registry.Route{
			{Path: "/api/plugins/containers/list", Handler: handlers.ContainerListHandler},
			{Path: "/api/plugins/containers/detail", Handler: handlers.ContainerDetailHandler},
			{Path: "/api/plugins/containers/action", Handler: handlers.ContainerActionHandler},
			{Path: "/api/plugins/containers/images", Handler: handlers.ContainerImagesHandler},
			{Path: "/api/plugins/containers/logs", Handler: handlers.ContainerLogsHandler},
			{Path: "/api/plugins/containers/flows", Handler: handlers.ContainerFlowsHandler},
			{Path: "/api/plugins/containers/docker/image/action", Handler: handlers.DockerImageActionHandler},
			{Path: "/api/plugins/containers/docker/registries", Handler: handlers.DockerRegistriesHandler},
			{Path: "/api/plugins/containers/docker/build", Handler: handlers.DockerBuildHandler},
			{Path: "/api/plugins/containers/docker/pull/async", Handler: handlers.DockerPullAsyncHandler},
			{Path: "/api/plugins/containers/docker/pull/progress", Handler: handlers.DockerPullProgressHandler},
			{Path: "/api/plugins/containers/docker/compose", Handler: handlers.DockerComposeHandler},
			{Path: "/api/plugins/containers/docker/swarm", Handler: handlers.DockerSwarmHandler},
			{Path: "/api/plugins/containers/docker/swarm/action", Handler: handlers.DockerSwarmActionHandler},
			{Path: "/api/plugins/containers/docker/exec", Handler: handlers.DockerExecHandler},
			{Path: "/api/plugins/containers/docker/container/run", Handler: handlers.DockerContainerRunHandler},
			{Path: "/api/plugins/containers/docker/container/config", Handler: handlers.DockerContainerConfigHandler},
			{Path: "/api/plugins/containers/k8s/clusters", Handler: handlers.K8sClustersHandler},
			{Path: "/api/plugins/containers/k8s/kubeconfig/default", Handler: handlers.K8sDefaultKubeconfigHandler},
			{Path: "/api/plugins/containers/k8s/cluster/action", Handler: handlers.K8sClusterActionHandler},
			{Path: "/api/plugins/containers/k8s/apply", Handler: handlers.K8sApplyHandler},
			{Path: "/api/plugins/containers/k8s/overview", Handler: handlers.K8sOverviewHandler},
			{Path: "/api/plugins/containers/k8s/pod/detail", Handler: handlers.K8sPodDetailHandler},
			{Path: "/api/plugins/containers/k8s/pod/related", Handler: handlers.K8sPodRelatedHandler},
			{Path: "/api/plugins/containers/k8s/pod/log", Handler: handlers.K8sPodLogHandler},
			{Path: "/api/plugins/containers/k8s/pod/exec", Handler: handlers.K8sPodExecHandler},
			{Path: "/api/plugins/containers/k8s/yaml", Handler: handlers.K8sYamlHandler},
			{Path: "/api/plugins/containers/k8s/resource/action", Handler: handlers.K8sResourceActionHandler},
			{Path: "/api/plugins/containers/k8s/replicas", Handler: handlers.K8sReplicasHandler},
			{Path: "/api/plugins/containers/k8s/rollout/history", Handler: handlers.K8sRolloutHistoryHandler},
			{Path: "/api/plugins/containers/k8s/yaml/save", Handler: handlers.K8sYamlSaveHandler},
			{Path: "/api/plugins/containers/k8s/metrics/nodes", Handler: handlers.K8sNodeMetricsHandler},
			{Path: "/api/plugins/containers/k8s/metrics/pods", Handler: handlers.K8sPodMetricsHandler},
			{Path: "/api/plugins/containers/k8s/metrics/history", Handler: handlers.K8sMetricsHistoryHandler},
			{Path: "/api/plugins/containers/k8s/resources", Handler: handlers.K8sResourcesHandler},
			{Path: "/api/plugins/containers/k8s/logs", Handler: handlers.K8sPodLogsHandler},
			{Path: "/api/plugins/containers/k8s/pod/containers", Handler: handlers.K8sPodContainersHandler},
		}},
	}

	// 全量注册路由(含未激活插件): 侧栏由 /api/manifest 实时过滤,
	// 插件 API 由 handler 内 pluginGuard 守卫 —— 接入/停用即时生效, 不再依赖重启
	for _, m := range modules {
		r.Register(&registry.Module{Manifest: m.m, Routes: m.routes})
	}
}

func cors(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if origin != "" {
			if isLocalOrigin(origin) {
				w.Header().Set("Access-Control-Allow-Origin", origin)
				w.Header().Set("Vary", "Origin")
			}
		}
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
		w.Header().Set("Access-Control-Allow-Credentials", "true")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		h.ServeHTTP(w, r)
	})
}

func isLocalOrigin(origin string) bool {
	u, err := url.Parse(origin)
	if err != nil {
		return false
	}
	host := u.Hostname()
	switch host {
	case "localhost", "127.0.0.1", "::1":
		return true
	}
	return false
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
