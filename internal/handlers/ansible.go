package handlers

import (
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/shirou/gopsutil/v4/host"

	"opscore/internal/agent"
	"opscore/internal/ansible"
	"opscore/internal/remote"
)

var ansibleMgr *ansible.Manager
var remotePool *remote.Pool
var agentHub *agent.AgentHub

func InitAnsible(mgr *ansible.Manager) {
	ansibleMgr = mgr
}

func InitPool(pool *remote.Pool) {
	remotePool = pool
}

func InitAgentHub(hub *agent.AgentHub) {
	agentHub = hub
	go watchAgentVersions()
}

// watchAgentVersions 定期扫描在线 agent, 版本落后于当前 server 期望版本时自动推送新 agent。
// 解决 server 升级后旧 agent 一直占着快照、新采集逻辑永不生效的问题。
func watchAgentVersions() {
	for {
		time.Sleep(60 * time.Second)
		if agentHub == nil || remotePool == nil {
			continue
		}
		for _, id := range agentHub.OnlineIDs() {
			if agentHub.NeedsUpdate(id) {
				log.Printf("[agent] %s 版本过旧, 自动推送新 agent", id)
				TryUpdateAgent(id)
			}
		}
	}
}

// HostsStatus 批量探测主机在线状态 (SSH 实时反馈); body 可选 ids 限制范围, 空=全部
func HostsStatus(w http.ResponseWriter, r *http.Request) {
	hosts := ansibleMgr.ListHosts()
	ids := map[string]bool{}
	if r.Method == "POST" {
		var body struct {
			IDs []string `json:"ids"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		for _, id := range body.IDs {
			ids[id] = true
		}
	}
	out := map[string]map[string]bool{}
	var mu sync.Mutex
	var wg sync.WaitGroup
	for i := range hosts {
		h := hosts[i]
		if len(ids) > 0 && !ids[h.ID] {
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			mu.Lock()
			out[h.ID] = map[string]bool{"online": h.CheckOnline()}
			mu.Unlock()
		}()
	}
	wg.Wait()
	WriteJSON(w, out)
}

// EnsureAgent 检查 Agent 是否在线，若离线则通过 SSH 推送新 Agent
// 部署成功清告警，部署失败或 SSH 不可达则记录告警
func EnsureAgent(hostID string) {
	if agentHub == nil || remotePool == nil {
		return
	}
	if agentHub.IsOnline(hostID) {
		return
	}
	tryDeployAgent(hostID)
}

// TryUpdateAgent 无论 Agent 是否在线，都尝试推送新 Agent（用于 Agent 有旧数据需更新时）
func TryUpdateAgent(hostID string) {
	if agentHub == nil || remotePool == nil {
		return
	}
	tryDeployAgent(hostID)
}

func tryDeployAgent(hostID string) {
	hosts := ansibleMgr.ListHosts()
	for _, h := range hosts {
		if h.ID != hostID {
			continue
		}
		go func() {
			if err := agent.TryWakeAgent(remotePool, h); err != nil {
				agentHub.SetAlert(hostID, "Agent 部署失败: "+err.Error())
				log.Printf("[agent] %s 部署失败: %v", hostID, err)
			} else {
				agentHub.ClearAlert(hostID)
			}
		}()
		return
	}
	agentHub.SetAlert(hostID, "主机未在 Ansible 清单中配置，无法部署 Agent")
}

func resolveAnsibleHost(hostID string) *ansible.Host {
	hosts := ansibleMgr.ListHosts()
	for i := range hosts {
		if hosts[i].ID == hostID || hosts[i].Alias == hostID || strings.EqualFold(hosts[i].Hostname, hostID) {
			return &hosts[i]
		}
	}
	return nil
}

// resolveRemoteHost 将 ansible.Host 转为 remote.Host 并解析 SSH 密钥路径。
func resolveRemoteHost(h ansible.Host) remote.Host {
	keyPath := ""
	if h.SSHKey != "" {
		p, err := ansibleMgr.SSH.GetKeyPath(h.SSHKey)
		if err == nil {
			keyPath = p
		}
	}
	port := h.Port
	if port == 0 {
		port = 22
	}
	user := h.User
	if user == "" {
		user = "root"
	}
	return remote.Host{
		ID:       h.ID,
		Addr:     h.Addr,
		Port:     port,
		User:     user,
		Alias:    h.Alias,
		SSHKey:   keyPath,
		Password: h.Password,
	}
}

func writeErr(w http.ResponseWriter, msg string, code int) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

// ===== Hosts =====

// localHostEntry 本机合成条目: 不落清单文件, id 为空串与前端"本机"选择语义一致
func localHostEntry() ansible.Host {
	platform := "linux"
	if runtime.GOOS == "windows" {
		platform = "win"
	}
	entry := ansible.Host{ID: "", Alias: "本机", Platform: platform, IsLocal: true}
	entry.Addr = localIPv4()
	if hi, err := host.Info(); err == nil {
		entry.Hostname = hi.Hostname
		if hi.OS != "" {
			entry.Hostname = hi.Hostname + " (" + hi.OS + ")"
		}
	}
	return entry
}

func localIPv4() string {
	// 优先: 与清单任一主机同子网(/24)的接口 (如 VMnet8 192.168.94.1 vs FlClash 198.18.0.1)
	for _, h := range ansibleMgr.ListHosts() {
		if h.Addr == "" {
			continue
		}
		hostIP := net.ParseIP(h.Addr).To4()
		if hostIP == nil {
			continue
		}
		for _, a := range ifaceAddrs() {
			if ipn, ok := a.(*net.IPNet); ok && !ipn.IP.IsLoopback() && ipn.IP.To4() != nil {
				ones, _ := ipn.Mask.Size()
				if ones == 24 {
					m := net.CIDRMask(24, 32)
					if hostIP.Mask(m).Equal(ipn.IP.To4().Mask(m)) {
						return ipn.IP.String()
					}
				}
			}
		}
	}
	for _, a := range ifaceAddrs() {
		if ipn, ok := a.(*net.IPNet); ok && !ipn.IP.IsLoopback() && ipn.IP.To4() != nil {
			return ipn.IP.String()
		}
	}
	return "127.0.0.1"
}

func ifaceAddrs() []net.Addr {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return nil
	}
	return addrs
}

func AnsibleHostsList(w http.ResponseWriter, r *http.Request) {
	hosts := ansibleMgr.ListHosts()
	hosts = append([]ansible.Host{localHostEntry()}, hosts...)
	WriteJSON(w, hosts)
}

func AnsibleHostsAdd(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var h ansible.Host
	if err := json.NewDecoder(r.Body).Decode(&h); err != nil {
		writeErr(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := ansibleMgr.AddHost(h); err != nil {
		writeErr(w, err.Error(), http.StatusBadRequest)
		return
	}
	if agentHub != nil && remotePool != nil {
		agent.DeployAgent(remotePool, h)
	}
	WriteJSON(w, map[string]string{"ok": "true"})
}

func AnsibleHostsBatchAdd(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var body struct {
		IPRange string `json:"ipRange"`
		User    string `json:"user"`
		Port    int    `json:"port"`
		SSHKey  string `json:"sshKey"`
		Prefix  string `json:"prefix"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, err.Error(), http.StatusBadRequest)
		return
	}
	ips, err := ansible.ExpandIPRange(body.IPRange)
	if err != nil {
		writeErr(w, err.Error(), http.StatusBadRequest)
		return
	}
	port := body.Port
	if port == 0 {
		port = 22
	}
	user := body.User
	if user == "" {
		user = "root"
	}
	var batch []ansible.Host
	for i, ip := range ips {
		id := fmt.Sprintf("%s%s", body.Prefix, ip)
		batch = append(batch, ansible.Host{
			ID:     id,
			Addr:   ip,
			Port:   port,
			User:   user,
			Alias:  fmt.Sprintf("node-%d", i+1),
			SSHKey: body.SSHKey,
		})
	}
	added, err := ansibleMgr.AddHosts(batch)
	if err != nil {
		writeErr(w, err.Error(), http.StatusInternalServerError)
		return
	}
	WriteJSON(w, map[string]any{"ok": "true", "added": added, "total": len(ips)})
}

func AnsibleHostsUpdate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var h ansible.Host
	if err := json.NewDecoder(r.Body).Decode(&h); err != nil {
		writeErr(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := ansibleMgr.UpdateHost(h); err != nil {
		writeErr(w, err.Error(), http.StatusBadRequest)
		return
	}
	if remotePool != nil {
		remotePool.Remove(h.ID)
	}
	invalidateSnapshot(h.ID)
	WriteJSON(w, map[string]string{"ok": "true"})
}

func AnsibleHostsTest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		Addr     string `json:"addr"`
		Port     int    `json:"port"`
		User     string `json:"user"`
		Password string `json:"password"`
		SSHKey   string `json:"sshKey"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, err.Error(), http.StatusBadRequest)
		return
	}
	port := req.Port
	if port == 0 {
		port = 22
	}
	user := req.User
	if user == "" {
		user = "root"
	}
	h := remote.Host{
		Addr:     req.Addr,
		Port:     port,
		User:     user,
		Password: req.Password,
		SSHKey:   req.SSHKey,
	}
	err := remote.TestHost(h)
	if err != nil {
		WriteJSON(w, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	WriteJSON(w, map[string]any{"ok": true})
}

func AnsibleHostsRemove(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var body struct {
		ID   string   `json:"id"`
		IDs  []string `json:"ids"`
		All  bool     `json:"all"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, err.Error(), http.StatusBadRequest)
		return
	}
	if body.All {
		hosts := ansibleMgr.ListHosts()
		ids := make([]string, len(hosts))
		for i, h := range hosts {
			ids[i] = h.ID
		}
		for _, id := range ids {
			if agentHub != nil {
				agentHub.RemoveHost(id)
			}
			if remotePool != nil {
				remotePool.Remove(id)
			}
		}
		WriteJSON(w, map[string]any{"ok": "true", "removed": len(ids)})
		return
	}
	ids := body.IDs
	if body.ID != "" {
		ids = append(ids, body.ID)
	}
	if len(ids) == 0 {
		writeErr(w, "请指定要删除的主机", http.StatusBadRequest)
		return
	}
	if err := ansibleMgr.RemoveHosts(ids); err != nil {
		writeErr(w, err.Error(), http.StatusInternalServerError)
		return
	}
	for _, id := range ids {
		if agentHub != nil {
			agentHub.RemoveHost(id)
		}
		if remotePool != nil {
			remotePool.Remove(id)
		}
	}
	WriteJSON(w, map[string]any{"ok": "true", "removed": len(ids)})
}

// ===== Inventories =====

func AnsibleInventoriesList(w http.ResponseWriter, r *http.Request) {
	WriteJSON(w, ansibleMgr.ListInventories())
}

func AnsibleInventoryGet(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("id")
	if id == "" {
		writeErr(w, "missing id", http.StatusBadRequest)
		return
	}
	inv := ansibleMgr.GetInventory(id)
	if inv == nil {
		writeErr(w, "not found", http.StatusNotFound)
		return
	}
	WriteJSON(w, inv)
}

func AnsibleInventoryCreate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var inv ansible.Inventory
	if err := json.NewDecoder(r.Body).Decode(&inv); err != nil {
		writeErr(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := ansibleMgr.CreateInventory(&inv); err != nil {
		writeErr(w, err.Error(), http.StatusBadRequest)
		return
	}
	WriteJSON(w, map[string]string{"ok": "true"})
}

func AnsibleInventoryDelete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var body struct{ ID string `json:"id"` }
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := ansibleMgr.DeleteInventory(body.ID); err != nil {
		writeErr(w, err.Error(), http.StatusBadRequest)
		return
	}
	WriteJSON(w, map[string]string{"ok": "true"})
}

func AnsibleInventoryRender(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("id")
	if id == "" {
		writeErr(w, "missing id", http.StatusBadRequest)
		return
	}
	inv := ansibleMgr.GetInventory(id)
	if inv == nil {
		writeErr(w, "not found", http.StatusNotFound)
		return
	}
	WriteJSON(w, map[string]string{"ini": inv.RenderINI()})
}

func AnsibleInventoryImportHosts(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var body struct {
		InventoryID string             `json:"inventoryId"`
		Hosts       []*ansible.HostEntry `json:"hosts"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, err.Error(), http.StatusBadRequest)
		return
	}
	added, err := ansibleMgr.ImportHostsToInventory(body.InventoryID, body.Hosts)
	if err != nil {
		writeErr(w, err.Error(), http.StatusInternalServerError)
		return
	}
	WriteJSON(w, map[string]any{"ok": "true", "added": added})
}

func AnsibleInventorySave(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var inv ansible.Inventory
	if err := json.NewDecoder(r.Body).Decode(&inv); err != nil {
		writeErr(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := ansibleMgr.SaveInventory(&inv); err != nil {
		writeErr(w, err.Error(), http.StatusInternalServerError)
		return
	}
	WriteJSON(w, map[string]string{"ok": "true"})
}

func AnsibleInventoryHostAdd(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var body struct {
		InventoryID string `json:"inventoryId"`
		HostID      string `json:"hostId"`
		Groups      []string `json:"groups"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, err.Error(), http.StatusBadRequest)
		return
	}
	entry, err := ansibleMgr.AddInventoryHostFromGlobal(body.InventoryID, body.HostID, body.Groups)
	if err != nil {
		writeErr(w, err.Error(), http.StatusBadRequest)
		return
	}
	WriteJSON(w, entry)
}

func AnsibleInventoryHostRemove(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var body struct {
		InventoryID string `json:"inventoryId"`
		HostID      string `json:"hostId"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := ansibleMgr.RemoveInventoryHost(body.InventoryID, body.HostID); err != nil {
		writeErr(w, err.Error(), http.StatusBadRequest)
		return
	}
	WriteJSON(w, map[string]string{"ok": "true"})
}

func AnsibleHostGroups(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var body struct {
		InventoryID string   `json:"inventoryId"`
		HostIDs     []string `json:"hostIds"`
		Groups      []string `json:"groups"`
		Op          string   `json:"op"` // set(默认) / add / remove
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, err.Error(), http.StatusBadRequest)
		return
	}
	if body.InventoryID == "" || len(body.HostIDs) == 0 {
		writeErr(w, "inventoryId 与 hostIds 不能为空", http.StatusBadRequest)
		return
	}
	added, updated, skipped, err := ansibleMgr.SetHostGroups(body.InventoryID, body.HostIDs, body.Groups, body.Op)
	if err != nil {
		writeErr(w, err.Error(), http.StatusInternalServerError)
		return
	}
	WriteJSON(w, map[string]any{"ok": "true", "added": added, "updated": updated, "skipped": skipped})
}

// HostGroupsHandler 全局主机组管理 (与 Inventory 解耦, all 为隐式根组, ungrouped 为隐式未分组视图)
func HostGroupsHandler(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		groups, hosts := ansibleMgr.ListHostGroups()
		type gv struct {
			Name     string          `json:"name"`
			Parent   string          `json:"parent,omitempty"`
			Children []string        `json:"children,omitempty"`
			Members  []ansible.Host  `json:"members"`
		}
		byName := map[string]*gv{}
		for i := range groups {
			g := &groups[i]
			byName[g.Name] = &gv{Name: g.Name, Parent: g.Parent, Members: []ansible.Host{}}
		}
		for _, g := range byName {
			if g.Parent != "" && byName[g.Parent] != nil {
				byName[g.Parent].Children = append(byName[g.Parent].Children, g.Name)
			}
		}
		ungrouped := []ansible.Host{}
		for _, h := range hosts {
			if len(h.Groups) == 0 {
				ungrouped = append(ungrouped, h)
				continue
			}
			for _, gn := range h.Groups {
				if g, ok := byName[gn]; ok {
					g.Members = append(g.Members, h)
				}
			}
		}
		list := make([]*gv, 0, len(byName))
		for _, g := range byName {
			list = append(list, g)
		}
		WriteJSON(w, map[string]any{
			"groups":    list,
			"ungrouped": ungrouped,
			"total":     len(hosts),
		})

	case http.MethodPost:
		var body struct {
			Op      string   `json:"op"` // create / remove / set / add / del
			Group   string   `json:"group"`
			Parent  string   `json:"parent,omitempty"`
			HostIDs []string `json:"hostIds"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeErr(w, "请求格式错误", http.StatusBadRequest)
			return
		}
		var err error
		switch body.Op {
		case "create":
			err = ansibleMgr.CreateHostGroup(body.Group, body.Parent)
		case "remove":
			err = ansibleMgr.RemoveHostGroup(body.Group)
		case "set":
			err = ansibleMgr.SetHostsGroups(body.HostIDs, []string{body.Group})
		case "add":
			err = ansibleMgr.AddHostsToGroup(body.HostIDs, body.Group)
		case "del":
			err = ansibleMgr.RemoveHostsFromGroup(body.HostIDs, body.Group)
		default:
			writeErr(w, "op 必须是 create/remove/set/add/del", http.StatusBadRequest)
			return
		}
		if err != nil {
			writeErr(w, err.Error(), http.StatusBadRequest)
			return
		}
		WriteJSON(w, map[string]any{"ok": "true"})

	default:
		writeErr(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// ===== Playbooks =====

func AnsiblePlaybooksList(w http.ResponseWriter, r *http.Request) {
	WriteJSON(w, ansibleMgr.ListPlaybooks())
}

func AnsiblePlaybookGet(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("id")
	if id == "" {
		writeErr(w, "missing id", http.StatusBadRequest)
		return
	}
	p := ansibleMgr.GetPlaybook(id)
	if p == nil {
		writeErr(w, "not found", http.StatusNotFound)
		return
	}
	WriteJSON(w, p)
}

func AnsiblePlaybookSave(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var p ansible.Playbook
	if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
		writeErr(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := ansibleMgr.SavePlaybook(&p); err != nil {
		writeErr(w, err.Error(), http.StatusInternalServerError)
		return
	}
	WriteJSON(w, map[string]string{"ok": "true"})
}

func AnsiblePlaybookDelete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var body struct{ ID string `json:"id"` }
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := ansibleMgr.DeletePlaybook(body.ID); err != nil {
		writeErr(w, err.Error(), http.StatusBadRequest)
		return
	}
	WriteJSON(w, map[string]string{"ok": "true"})
}

// ===== Execution =====

func AnsiblePing(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var body struct {
		Hosts       []string `json:"hosts"`
		InventoryID string   `json:"inventoryId"`
	}
	json.NewDecoder(r.Body).Decode(&body)
	if _, err := exec.LookPath("ansible"); err != nil {
		// 无 ansible 环境 (如 Windows 服务器): 降级为 TCP 连通性检测
		WriteJSON(w, ansibleMgr.PingTCP(body.Hosts))
		return
	}
	results, err := ansibleMgr.Ping(body.Hosts, body.InventoryID)
	if err != nil {
		writeErr(w, err.Error(), http.StatusInternalServerError)
		return
	}
	WriteJSON(w, results)
}

func AnsibleAdhoc(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var body struct {
		Hosts       []string `json:"hosts"`
		InventoryID string   `json:"inventoryId"`
		Module      string   `json:"module"`
		Args        string   `json:"args"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, err.Error(), http.StatusBadRequest)
		return
	}
	if body.Module == "" {
		body.Module = "shell"
	}
	results, err := ansibleMgr.Adhoc(body.Hosts, body.InventoryID, body.Module, body.Args)
	if err != nil {
		writeErr(w, err.Error(), http.StatusInternalServerError)
		return
	}
	WriteJSON(w, results)
}

func AnsiblePlaybookExec(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var body struct {
		PlaybookID  string `json:"playbookId"`
		InventoryID string `json:"inventoryId"`
		CheckMode   bool   `json:"checkMode"`
		Tags        string `json:"tags"`
		ExtraVars   string `json:"extraVars"`
		Limit       string `json:"limit"`
		Forks       int    `json:"forks"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, err.Error(), http.StatusBadRequest)
		return
	}
	if body.PlaybookID == "" {
		writeErr(w, "playbookId 不能为空", http.StatusBadRequest)
		return
	}
	results, err := ansibleMgr.RunPlaybook(body.PlaybookID, body.InventoryID, body.CheckMode, body.Tags, body.ExtraVars, body.Limit, body.Forks)
	if err != nil {
		writeErr(w, err.Error(), http.StatusInternalServerError)
		return
	}
	WriteJSON(w, results)
}

// ===== SSE Execution =====

func serveSSE(w http.ResponseWriter, r *http.Request, run func(emit func(ansible.SSEEvent))) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeErr(w, "SSE not supported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	emit := func(evt ansible.SSEEvent) {
		data, _ := json.Marshal(evt)
		fmt.Fprintf(w, "data: %s\n\n", data)
		flusher.Flush()
	}

	done := make(chan struct{})
	go func() {
		run(emit)
		close(done)
	}()

	select {
	case <-done:
	case <-r.Context().Done():
	}
}

func AnsibleSSEPing(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var body struct {
		Hosts       []string `json:"hosts"`
		InventoryID string   `json:"inventoryId"`
	}
	json.NewDecoder(r.Body).Decode(&body)
	serveSSE(w, r, func(emit func(ansible.SSEEvent)) {
		ansibleMgr.SSERunPing(body.Hosts, body.InventoryID, emit)
	})
}

func AnsibleSSEAdhoc(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var body struct {
		Hosts       []string `json:"hosts"`
		InventoryID string   `json:"inventoryId"`
		Module      string   `json:"module"`
		Args        string   `json:"args"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, err.Error(), http.StatusBadRequest)
		return
	}
	if body.Module == "" {
		body.Module = "shell"
	}
	serveSSE(w, r, func(emit func(ansible.SSEEvent)) {
		ansibleMgr.SSERunAdhoc(body.Hosts, body.InventoryID, body.Module, body.Args, emit)
	})
}

func AnsibleSSEPlaybookExec(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var body struct {
		PlaybookID  string `json:"playbookId"`
		InventoryID string `json:"inventoryId"`
		CheckMode   bool   `json:"checkMode"`
		Tags        string `json:"tags"`
		ExtraVars   string `json:"extraVars"`
		Limit       string `json:"limit"`
		Forks       int    `json:"forks"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, err.Error(), http.StatusBadRequest)
		return
	}
	if body.PlaybookID == "" {
		writeErr(w, "playbookId 不能为空", http.StatusBadRequest)
		return
	}
	serveSSE(w, r, func(emit func(ansible.SSEEvent)) {
		ansibleMgr.SSERunPlaybook(body.PlaybookID, body.InventoryID, body.CheckMode, body.Tags, body.ExtraVars, body.Limit, body.Forks, emit)
	})
}

// ===== History =====

func AnsibleHistory(w http.ResponseWriter, r *http.Request) {
	WriteJSON(w, ansibleMgr.History())
}

func AnsibleHistoryClear(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	ansibleMgr.ClearHistory()
	WriteJSON(w, map[string]string{"ok": "true"})
}

func AnsibleHistoryRerun(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var body struct{ ID string `json:"id"` }
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, err.Error(), http.StatusBadRequest)
		return
	}
	rtype, rc, err := ansibleMgr.Rerun(body.ID)
	if err != nil {
		writeErr(w, err.Error(), http.StatusBadRequest)
		return
	}
	WriteJSON(w, map[string]any{"type": rtype, "run": rc})
}

// ===== Templates =====

func AnsibleTemplatesList(w http.ResponseWriter, r *http.Request) {
	WriteJSON(w, ansibleMgr.ListTemplates())
}

func AnsibleTemplateCreate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var body struct {
		TemplateID string `json:"templateId"`
		NewID      string `json:"newId"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, err.Error(), http.StatusBadRequest)
		return
	}
	p, err := ansibleMgr.CreateFromTemplate(body.TemplateID, body.NewID)
	if err != nil {
		writeErr(w, err.Error(), http.StatusBadRequest)
		return
	}
	WriteJSON(w, p)
}
