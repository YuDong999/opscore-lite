package ansible

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

func autoID() string {
	b := make([]byte, 8)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// normalize 补全主机默认值: 平台空按 linux, 默认端口按平台 (linux→22, win→5985)
func (h *Host) normalize() {
	if h.Platform != "win" {
		h.Platform = "linux"
	}
	if h.Port == 0 {
		if h.Platform == "win" {
			h.Port = 5985
		} else {
			h.Port = 22
		}
	}
}

// defaultPort 返回主机的有效探测端口 (未落库时按平台默认)
func (h Host) defaultPort() int {
	if h.Port != 0 {
		return h.Port
	}
	if h.Platform == "win" {
		return 5985
	}
	return 22
}

type Host struct {
	ID       string   `json:"id"`
	Addr     string   `json:"addr"`
	Port     int      `json:"port"`
	User     string   `json:"user"`
	Alias    string   `json:"alias"`
	SSHKey   string   `json:"sshKey,omitempty"`
	Password string   `json:"password,omitempty"`
	Groups   []string `json:"groups,omitempty"`
	Platform string   `json:"platform,omitempty"` // linux / win, 空按 linux
	IsLocal  bool     `json:"isLocal,omitempty"`  // 本机合成条目标记
	Hostname string   `json:"hostname,omitempty"` // 本机条目展示用
}

type Inventory struct {
	ID          string                `json:"id"`
	Name        string                `json:"name"`
	Description string                `json:"description"`
	Groups      map[string]*Group     `json:"groups"`
	Hosts       map[string]*HostEntry `json:"hosts"`
}

type Group struct {
	Name     string            `json:"name"`
	Parent   string            `json:"parent,omitempty"`
	Vars     map[string]string `json:"vars,omitempty"`
	Children []string          `json:"children,omitempty"`
}

type HostEntry struct {
	Host
	Groups []string          `json:"groups"`
	Vars   map[string]string `json:"vars,omitempty"`
}

type Playbook struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Content     string `json:"content"`
	Path        string `json:"path,omitempty"`
}

type Result struct {
	Host    string `json:"host"`
	Success bool   `json:"success"`
	Output  string `json:"output"`
	Stdout  string `json:"stdout"`
	Stderr  string `json:"stderr"`
	Changed bool   `json:"changed"`
}

type RunContext struct {
	Hosts       []string `json:"hosts,omitempty"`
	InventoryID string   `json:"inventoryId,omitempty"`
	Module      string   `json:"module,omitempty"`
	Args        string   `json:"args,omitempty"`
	CheckMode   bool     `json:"checkMode,omitempty"`
	Tags        string   `json:"tags,omitempty"`
	ExtraVars   string   `json:"extraVars,omitempty"`
	Limit       string   `json:"limit,omitempty"`
	Forks       int      `json:"forks,omitempty"`
	PlaybookID  string   `json:"playbookId,omitempty"`
}

type ExecRecord struct {
	ID       string      `json:"id"`
	Time     time.Time   `json:"time"`
	Type     string      `json:"type"` // ping, adhoc, playbook
	Target   string      `json:"target"`
	Results  []Result    `json:"results"`
	Success  bool        `json:"success"`
	Duration string      `json:"duration"`
	Run      *RunContext `json:"run,omitempty"`
}

type Manager struct {
	mu            sync.Mutex
	dataDir       string
	hostFile      string
	inventoryFile string
	playbookDir   string
	historyFile   string
	hostGroupFile string
	hosts         []Host
	hostGroups    []Group
	inventories   []*Inventory
	history       []ExecRecord
	SSH           *SSHManager
}

func NewManager(dataDir string) (*Manager, error) {
	m := &Manager{
		dataDir:       dataDir,
		hostFile:      filepath.Join(dataDir, "ansible_hosts.json"),
		inventoryFile: filepath.Join(dataDir, "ansible_inventories.json"),
		playbookDir:   filepath.Join(dataDir, "playbooks"),
		historyFile:   filepath.Join(dataDir, "ansible_history.json"),
		hostGroupFile: filepath.Join(dataDir, "ansible_host_groups.json"),
	}
	m.SSH = NewSSHManager(dataDir)
	os.MkdirAll(m.playbookDir, 0755)

	loadJSON(m.hostFile, &m.hosts)
	loadJSON(m.inventoryFile, &m.inventories)
	loadJSON(m.historyFile, &m.history)
	loadJSON(m.hostGroupFile, &m.hostGroups)

	if m.hosts == nil {
		m.hosts = []Host{}
	}
	migrated := false
	for i := range m.hosts {
		if m.hosts[i].ID == "" {
			m.hosts[i].ID = autoID()
			migrated = true
		}
	}
	if migrated {
		saveJSON(m.hostFile, m.hosts)
	}
	if m.inventories == nil {
		m.inventories = []*Inventory{}
	}
	if m.history == nil {
		m.history = []ExecRecord{}
	}
	if m.hostGroups == nil {
		m.hostGroups = []Group{}
	}
	return m, nil
}

func loadJSON(path string, v any) {
	data, err := os.ReadFile(path)
	if err == nil {
		json.Unmarshal(data, v)
	}
}

func saveJSON(path string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0644)
}

// ===== Host CRUD =====

func (m *Manager) ListHosts() []Host {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Host, len(m.hosts))
	copy(out, m.hosts)
	return out
}

func (m *Manager) AddHost(h Host) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, x := range m.hosts {
		if x.Addr == h.Addr {
			return fmt.Errorf("主机 %s (%s) 已存在", h.ID, h.Addr)
		}
	}
	if h.ID == "" {
		h.ID = autoID()
	}
	h.normalize()
	m.hosts = append(m.hosts, h)
	return saveJSON(m.hostFile, m.hosts)
}

func (m *Manager) AddHosts(batch []Host) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	added := 0
	existing := map[string]bool{}
	for _, x := range m.hosts {
		existing[x.ID] = true
		existing[x.Addr] = true
	}
	for _, h := range batch {
		if existing[h.Addr] {
			continue
		}
		if h.ID == "" || existing[h.ID] {
			h.ID = autoID()
		}
		h.normalize()
		m.hosts = append(m.hosts, h)
		existing[h.ID] = true
		existing[h.Addr] = true
		added++
	}
	if added > 0 {
		return added, saveJSON(m.hostFile, m.hosts)
	}
	return 0, nil
}

func (m *Manager) RemoveHost(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for _, h := range m.hosts {
		if h.ID != id {
			m.hosts[n] = h
			n++
		}
	}
	if n == len(m.hosts) {
		return fmt.Errorf("主机 %s 不存在", id)
	}
	m.hosts = m.hosts[:n]
	return saveJSON(m.hostFile, m.hosts)
}

func (m *Manager) RemoveHosts(ids []string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	set := make(map[string]bool, len(ids))
	for _, id := range ids {
		set[id] = true
	}
	n := 0
	for _, h := range m.hosts {
		if !set[h.ID] {
			m.hosts[n] = h
			n++
		}
	}
	m.hosts = m.hosts[:n]
	return saveJSON(m.hostFile, m.hosts)
}

func (m *Manager) UpdateHost(h Host) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i := range m.hosts {
		if m.hosts[i].ID == h.ID {
			m.hosts[i].Addr = h.Addr
			m.hosts[i].Port = h.Port
			m.hosts[i].User = h.User
			m.hosts[i].Alias = h.Alias
			m.hosts[i].SSHKey = h.SSHKey
			if h.Platform != "" {
				m.hosts[i].Platform = h.Platform
			}
			return saveJSON(m.hostFile, m.hosts)
		}
	}
	return fmt.Errorf("主机 %s 不存在", h.ID)
}

// SetHostname 更新主机的 hostname (拓扑/诊断采集结果回填), 空值或不变化不写盘
func (m *Manager) SetHostname(id, hostname string) error {
	hostname = strings.TrimSpace(hostname)
	if hostname == "" {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for i := range m.hosts {
		if m.hosts[i].ID == id && m.hosts[i].Hostname != hostname {
			m.hosts[i].Hostname = hostname
			return saveJSON(m.hostFile, m.hosts)
		}
	}
	return nil
}

func (m *Manager) BindKeyToHost(keyName, hostID string) error {
	keyPath, err := m.SSH.GetKeyPath(keyName)
	if err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for i := range m.hosts {
		if m.hosts[i].ID == hostID {
			m.hosts[i].SSHKey = keyPath
			return saveJSON(m.hostFile, m.hosts)
		}
	}
	return fmt.Errorf("主机 %s 不存在", hostID)
}

// ===== Global host groups (all 为隐式根组, 组定义存 hostGroups, 成员以 Host.Groups 为准) =====

func (m *Manager) saveHostGroupsLocked() error {
	return saveJSON(m.hostGroupFile, m.hostGroups)
}

func (m *Manager) groupExistsLocked(name string) bool {
	for _, g := range m.hostGroups {
		if g.Name == name {
			return true
		}
	}
	return false
}

func validGroupName(name string) error {
	if name == "" {
		return fmt.Errorf("组名不能为空")
	}
	if name == "all" {
		return fmt.Errorf("all 是隐式根组, 不能作为自定义组名")
	}
	if name == "ungrouped" {
		return fmt.Errorf("ungrouped 是隐式视图, 不能作为自定义组名")
	}
	for _, ch := range name {
		if !(ch >= 'a' && ch <= 'z' || ch >= 'A' && ch <= 'Z' || ch >= '0' && ch <= '9' || ch == '_' || ch == '-' || ch == '.') {
			return fmt.Errorf("组名只能包含字母、数字、下划线、连字符和点")
		}
	}
	return nil
}

// ListHostGroups 返回组定义与全量主机, 由 handler 组装树/成员视图
func (m *Manager) ListHostGroups() ([]Group, []Host) {
	m.mu.Lock()
	defer m.mu.Unlock()
	groups := make([]Group, len(m.hostGroups))
	copy(groups, m.hostGroups)
	hosts := make([]Host, len(m.hosts))
	copy(hosts, m.hosts)
	return groups, hosts
}

func (m *Manager) CreateHostGroup(name, parent string) error {
	if err := validGroupName(name); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.groupExistsLocked(name) {
		return fmt.Errorf("组 %s 已存在", name)
	}
	if parent != "" && parent != "all" && !m.groupExistsLocked(parent) {
		return fmt.Errorf("父组 %s 不存在", parent)
	}
	m.hostGroups = append(m.hostGroups, Group{Name: name, Parent: parent})
	return m.saveHostGroupsLocked()
}

func (m *Manager) RemoveHostGroup(name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.groupExistsLocked(name) {
		return fmt.Errorf("组 %s 不存在", name)
	}
	out := m.hostGroups[:0]
	for _, g := range m.hostGroups {
		if g.Name == name {
			continue
		}
		if g.Parent == name {
			g.Parent = ""
		}
		out = append(out, g)
	}
	m.hostGroups = out
	changed := false
	for i := range m.hosts {
		n := 0
		for _, gn := range m.hosts[i].Groups {
			if gn != name {
				m.hosts[i].Groups[n] = gn
				n++
			}
		}
		if n != len(m.hosts[i].Groups) {
			m.hosts[i].Groups = m.hosts[i].Groups[:n]
			changed = true
		}
	}
	if err := m.saveHostGroupsLocked(); err != nil {
		return err
	}
	if changed {
		return saveJSON(m.hostFile, m.hosts)
	}
	return nil
}

// SetHostsGroups 覆盖指定主机的组列表
func (m *Manager) SetHostsGroups(hostIDs, groups []string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, g := range groups {
		if g != "" && g != "all" && !m.groupExistsLocked(g) {
			return fmt.Errorf("组 %s 不存在", g)
		}
	}
	set := make(map[string]bool, len(hostIDs))
	for _, id := range hostIDs {
		set[id] = true
	}
	changed := false
	for i := range m.hosts {
		if set[m.hosts[i].ID] {
			m.hosts[i].Groups = append([]string(nil), groups...)
			changed = true
		}
	}
	if !changed {
		return fmt.Errorf("未找到指定主机")
	}
	return saveJSON(m.hostFile, m.hosts)
}

// AddHostsToGroup 将主机加入组 (组不存在则自动创建, all 为隐式无需加入)
func (m *Manager) AddHostsToGroup(hostIDs []string, group string) error {
	if group == "" || group == "all" {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.groupExistsLocked(group) {
		m.hostGroups = append(m.hostGroups, Group{Name: group})
	}
	set := make(map[string]bool, len(hostIDs))
	for _, id := range hostIDs {
		set[id] = true
	}
	changed := false
	for i := range m.hosts {
		if !set[m.hosts[i].ID] {
			continue
		}
		has := false
		for _, g := range m.hosts[i].Groups {
			if g == group {
				has = true
				break
			}
		}
		if !has {
			m.hosts[i].Groups = append(m.hosts[i].Groups, group)
			changed = true
		}
	}
	if err := m.saveHostGroupsLocked(); err != nil {
		return err
	}
	if changed {
		return saveJSON(m.hostFile, m.hosts)
	}
	return nil
}

// RemoveHostsFromGroup 将主机移出组
func (m *Manager) RemoveHostsFromGroup(hostIDs []string, group string) error {
	if group == "" {
		return fmt.Errorf("组名不能为空")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	set := make(map[string]bool, len(hostIDs))
	for _, id := range hostIDs {
		set[id] = true
	}
	changed := false
	for i := range m.hosts {
		if !set[m.hosts[i].ID] {
			continue
		}
		n := 0
		for _, g := range m.hosts[i].Groups {
			if g != group {
				m.hosts[i].Groups[n] = g
				n++
			}
		}
		if n != len(m.hosts[i].Groups) {
			m.hosts[i].Groups = m.hosts[i].Groups[:n]
			changed = true
		}
	}
	if !changed {
		return fmt.Errorf("未找到指定主机")
	}
	return saveJSON(m.hostFile, m.hosts)
}

// ===== Batch host expansion =====

func ExpandIPRange(input string) ([]string, error) {
	input = strings.TrimSpace(input)
	if input == "" {
		return nil, fmt.Errorf("输入为空")
	}

	// 逗号分隔列表: 每段递归解析, 支持混写(范围/后缀/CIDR)
	if strings.Contains(input, ",") {
		var out []string
		for _, part := range strings.Split(input, ",") {
			ips, err := ExpandIPRange(part)
			if err != nil {
				return nil, err
			}
			out = append(out, ips...)
		}
		return dedupeIPs(out), nil
	}

	// 含 / : 优先按 CIDR 展开(仅当输入 IP 是标准网络地址, 如 192.168.94.0/24),
	// 否则按后缀列表展开 (192.168.94.22/25 = .22 与 .25 两个 IP)
	if strings.Contains(input, "/") {
		ipPart := strings.TrimSpace(strings.SplitN(input, "/", 2)[0])
		if ip := net.ParseIP(ipPart); ip != nil && ip.To4() != nil {
			if _, ipnet, err := net.ParseCIDR(input); err == nil && ipnet.IP.To4() != nil && ip.Equal(ipnet.IP) {
				return expandCIDR(ipnet)
			}
			return expandSlashSuffix(input)
		}
		return nil, fmt.Errorf("无效 IP 列表: %s", input)
	}

	if strings.Contains(input, "-") {
		return expandDashRange(input)
	}

	ip := net.ParseIP(input)
	if ip == nil {
		return nil, fmt.Errorf("无效 IP: %s", input)
	}
	return []string{input}, nil
}

// expandCIDR 展开整个网段。仅支持 /24 及更小的段(≤256 个 IP), 防止误输入海量 IP。
func expandCIDR(ipnet *net.IPNet) ([]string, error) {
	ones, bits := ipnet.Mask.Size()
	if bits != 32 {
		return nil, fmt.Errorf("仅支持 IPv4 CIDR")
	}
	start := ipnet.IP.To4()
	count := 1 << (bits - ones)
	if count > 256 {
		return nil, fmt.Errorf("网段过大(>256 个 IP), 请缩小掩码(如 /24)")
	}
	var out []string
	for i := 0; i < count; i++ {
		ip := make(net.IP, 4)
		copy(ip, start)
		ip[3] += byte(i)
		out = append(out, ip.String())
	}
	return out, nil
}

// expandSlashSuffix 展开前缀 + 数字列表: 192.168.94.22/23/24/25 → .22/.23/.24/.25
func expandSlashSuffix(input string) ([]string, error) {
	parts := strings.Split(input, "/")
	ip := net.ParseIP(strings.TrimSpace(parts[0]))
	if ip == nil {
		return nil, fmt.Errorf("无效 IP 列表: %s", input)
	}
	v4 := ip.To4()
	if v4 == nil {
		return nil, fmt.Errorf("仅支持 IPv4 后缀列表: %s", input)
	}
	out := []string{fmt.Sprintf("%d.%d.%d.%d", v4[0], v4[1], v4[2], v4[3])}
	for _, p := range parts[1:] {
		p = strings.TrimSpace(p)
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 || n > 255 {
			return nil, fmt.Errorf("无效 IP 后缀: %s", p)
		}
		out = append(out, fmt.Sprintf("%d.%d.%d.%d", v4[0], v4[1], v4[2], n))
	}
	return dedupeIPs(out), nil
}

func expandDashRange(input string) ([]string, error) {
	parts := strings.SplitN(input, "-", 2)
	startStr := strings.TrimSpace(parts[0])
	endStr := strings.TrimSpace(parts[1])

	startIP := net.ParseIP(startStr)
	if startIP == nil {
		return nil, fmt.Errorf("无效 IP 范围: %s", input)
	}
	start := startIP.To4()
	if start == nil {
		return nil, fmt.Errorf("仅支持 IPv4 范围: %s", input)
	}

	// 末位简写: 192.168.94.22-30 → .22~.30 (闭区间)
	endIP := net.ParseIP(endStr)
	if endIP == nil {
		n, err := strconv.Atoi(endStr)
		if err != nil || n < 0 || n > 255 {
			return nil, fmt.Errorf("无效 IP 范围: %s", input)
		}
		if n < int(start[3]) {
			return nil, fmt.Errorf("IP 范围起始大于结束: %s", input)
		}
		var out []string
		for i := int(start[3]); i <= n; i++ {
			out = append(out, fmt.Sprintf("%d.%d.%d.%d", start[0], start[1], start[2], i))
		}
		return out, nil
	}

	// 完整 IP 两端: 10.2.22.1-10.2.22.10 (支持跨段, 闭区间)
	end := endIP.To4()
	if end == nil {
		return nil, fmt.Errorf("仅支持 IPv4 范围: %s", input)
	}
	toU32 := func(b []byte) uint32 {
		return uint32(b[0])<<24 | uint32(b[1])<<16 | uint32(b[2])<<8 | uint32(b[3])
	}
	startU, endU := toU32(start), toU32(end)
	if startU > endU {
		return nil, fmt.Errorf("IP 范围起始大于结束: %s", input)
	}
	var out []string
	for u := startU; u <= endU; u++ {
		out = append(out, fmt.Sprintf("%d.%d.%d.%d", byte(u>>24), byte(u>>16), byte(u>>8), byte(u)))
	}
	return out, nil
}

func dedupeIPs(ips []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, ip := range ips {
		if !seen[ip] {
			seen[ip] = true
			out = append(out, ip)
		}
	}
	return out
}

// ===== Inventory management =====

func (m *Manager) ListInventories() []*Inventory {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.inventories
}

func (m *Manager) GetInventory(id string) *Inventory {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, inv := range m.inventories {
		if inv.ID == id {
			return inv
		}
	}
	return nil
}

func (m *Manager) CreateInventory(inv *Inventory) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, x := range m.inventories {
		if x.ID == inv.ID {
			return fmt.Errorf("清单 %s 已存在", inv.ID)
		}
	}
	if inv.Groups == nil {
		inv.Groups = map[string]*Group{}
	}
	if inv.Hosts == nil {
		inv.Hosts = map[string]*HostEntry{}
	}
	if _, ok := inv.Groups["all"]; !ok {
		inv.Groups["all"] = &Group{Name: "all", Vars: map[string]string{}}
	}
	m.inventories = append(m.inventories, inv)
	return saveJSON(m.inventoryFile, m.inventories)
}

func (m *Manager) DeleteInventory(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for _, inv := range m.inventories {
		if inv.ID != id {
			m.inventories[n] = inv
			n++
		}
	}
	if n == len(m.inventories) {
		return fmt.Errorf("清单 %s 不存在", id)
	}
	m.inventories = m.inventories[:n]
	return saveJSON(m.inventoryFile, m.inventories)
}

func (m *Manager) AddInventoryHost(invID string, entry *HostEntry) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, inv := range m.inventories {
		if inv.ID == invID {
			if _, ok := inv.Hosts[entry.ID]; ok {
				return fmt.Errorf("主机 %s 已在清单中", entry.ID)
			}
			inv.Hosts[entry.ID] = entry
			return saveJSON(m.inventoryFile, m.inventories)
		}
	}
	return fmt.Errorf("清单 %s 不存在", invID)
}

func (m *Manager) RemoveInventoryHost(invID, hostID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, inv := range m.inventories {
		if inv.ID == invID {
			delete(inv.Hosts, hostID)
			return saveJSON(m.inventoryFile, m.inventories)
		}
	}
	return fmt.Errorf("清单 %s 不存在", invID)
}

func (m *Manager) SaveInventory(inv *Inventory) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i, x := range m.inventories {
		if x.ID == inv.ID {
			m.inventories[i] = inv
			return saveJSON(m.inventoryFile, m.inventories)
		}
	}
	return fmt.Errorf("清单 %s 不存在", inv.ID)
}

func (m *Manager) AddInventoryHostFromGlobal(invID, hostID string, groups []string) (*HostEntry, error) {
	m.mu.Lock()
	var host *Host
	for i := range m.hosts {
		if m.hosts[i].ID == hostID {
			host = &m.hosts[i]
			break
		}
	}
	m.mu.Unlock()
	if host == nil {
		return nil, fmt.Errorf("主机 %s 不存在", hostID)
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	for _, inv := range m.inventories {
		if inv.ID == invID {
			if _, ok := inv.Hosts[hostID]; ok {
				return nil, fmt.Errorf("主机 %s 已在清单中", hostID)
			}
			if len(groups) == 0 {
				groups = []string{"all"}
			}
			entry := &HostEntry{
				Host:   *host,
				Groups: groups,
				Vars:   map[string]string{},
			}
			inv.Hosts[hostID] = entry
			return entry, saveJSON(m.inventoryFile, m.inventories)
		}
	}
	return nil, fmt.Errorf("清单 %s 不存在", invID)
}

// SetHostGroups 批量把全局主机导入清单并设置所属组。
// op: "set"(默认, 覆盖) / "add"(合并, 不覆盖原有组) / "remove"(从组移除, 空则回退 all)
// 已在清单中的主机仅更新 Groups，不报错。
func (m *Manager) SetHostGroups(invID string, hostIDs, groups []string, op string) (added, updated, skipped int, err error) {
	if len(groups) == 0 {
		groups = []string{"all"}
	}
	switch op {
	case "", "set", "add", "remove":
	default:
		return 0, 0, 0, fmt.Errorf("无效操作: %s", op)
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	mergeGroups := func(a, b []string) []string {
		seen := map[string]bool{}
		var out []string
		for _, x := range append(append([]string{}, a...), b...) {
			if !seen[x] {
				seen[x] = true
				out = append(out, x)
			}
		}
		return out
	}
	applyGroups := func(entry *HostEntry) {
		switch op {
		case "add":
			entry.Groups = mergeGroups(entry.Groups, groups)
		case "remove":
			drop := map[string]bool{}
			for _, g := range groups {
				drop[g] = true
			}
			var kept []string
			for _, g := range entry.Groups {
				if !drop[g] {
					kept = append(kept, g)
				}
			}
			if len(kept) == 0 {
				kept = []string{"all"}
			}
			entry.Groups = kept
		default:
			entry.Groups = groups
		}
	}

	hostMap := make(map[string]*Host, len(m.hosts))
	for i := range m.hosts {
		hostMap[m.hosts[i].ID] = &m.hosts[i]
	}

	for _, inv := range m.inventories {
		if inv.ID != invID {
			continue
		}
		for _, hostID := range hostIDs {
			if entry, ok := inv.Hosts[hostID]; ok {
				applyGroups(entry)
				updated++
				continue
			}
			h, ok := hostMap[hostID]
			if !ok || op == "remove" {
				skipped++
				continue
			}
			inv.Hosts[hostID] = &HostEntry{
				Host:   *h,
				Groups: groups,
				Vars:   map[string]string{},
			}
			added++
		}
		if added+updated > 0 {
			return added, updated, skipped, saveJSON(m.inventoryFile, m.inventories)
		}
		return 0, 0, skipped, nil
	}
	return 0, 0, 0, fmt.Errorf("清单 %s 不存在", invID)
}

func (m *Manager) ImportHostsToInventory(invID string, hosts []*HostEntry) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, inv := range m.inventories {
		if inv.ID == invID {
			added := 0
			for _, h := range hosts {
				if _, ok := inv.Hosts[h.ID]; !ok {
					inv.Hosts[h.ID] = h
					added++
				}
			}
			if added > 0 {
				return added, saveJSON(m.inventoryFile, m.inventories)
			}
			return 0, nil
		}
	}
	return 0, fmt.Errorf("清单 %s 不存在", invID)
}

func (inv *Inventory) RenderINI() string {
	var buf bytes.Buffer

	buf.WriteString("# Ansible Inventory: " + inv.Name + "\n")
	buf.WriteString("# " + inv.Description + "\n\n")

	groupOrder := []string{"all"}
	seen := map[string]bool{"all": true}

	for name, g := range inv.Groups {
		if name == "all" || seen[name] {
			continue
		}
		if g.Parent == "" {
			groupOrder = append(groupOrder, name)
			seen[name] = true
		}
	}
	for name := range inv.Groups {
		if name == "all" || seen[name] {
			continue
		}
		groupOrder = append(groupOrder, name)
		seen[name] = true
	}

	for _, gName := range groupOrder {
		g := inv.Groups[gName]
		buf.WriteString(fmt.Sprintf("[%s]\n", gName))

		for _, h := range inv.Hosts {
			for _, hg := range h.Groups {
				if hg == gName {
					buf.WriteString(fmt.Sprintf("%s ansible_host=%s ansible_port=%d ansible_user=%s", h.ID, h.Addr, h.Port, h.User))
					if h.SSHKey != "" {
						buf.WriteString(fmt.Sprintf(" ansible_ssh_private_key_file=%s", h.SSHKey))
					}
					for k, v := range h.Vars {
						buf.WriteString(fmt.Sprintf(" %s=%s", k, v))
					}
					buf.WriteByte('\n')
				}
			}
		}

		if len(g.Vars) > 0 {
			buf.WriteString(fmt.Sprintf("[%s:vars]\n", gName))
			for k, v := range g.Vars {
				buf.WriteString(fmt.Sprintf("%s=%s\n", k, v))
			}
		}

		if g.Parent != "" {
			buf.WriteString(fmt.Sprintf("[%s:children]\n%s\n", gName, g.Parent))
		}

		buf.WriteByte('\n')
	}

	return buf.String()
}

// ===== Playbook management =====

func (m *Manager) ListPlaybooks() []*Playbook {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []*Playbook{}
	entries, _ := os.ReadDir(m.playbookDir)
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".yml") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(m.playbookDir, e.Name()))
		if err != nil {
			continue
		}
		id := strings.TrimSuffix(e.Name(), ".yml")
		p := &Playbook{
			ID:      id,
			Name:    id,
			Content: string(data),
			Path:    filepath.Join(m.playbookDir, e.Name()),
		}
		out = append(out, p)
	}
	return out
}

func (m *Manager) GetPlaybook(id string) *Playbook {
	path := filepath.Join(m.playbookDir, id+".yml")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	return &Playbook{
		ID:      id,
		Name:    id,
		Content: string(data),
		Path:    path,
	}
}

func (m *Manager) SavePlaybook(p *Playbook) error {
	if p.ID == "" {
		p.ID = strings.ReplaceAll(p.Name, " ", "_")
	}
	path := filepath.Join(m.playbookDir, p.ID+".yml")
	p.Path = path
	return os.WriteFile(path, []byte(p.Content), 0644)
}

func (m *Manager) DeletePlaybook(id string) error {
	path := filepath.Join(m.playbookDir, id+".yml")
	if _, err := os.Stat(path); err != nil {
		return fmt.Errorf("playbook %s 不存在", id)
	}
	return os.Remove(path)
}

// ===== Execution =====

func (m *Manager) Ping(hosts []string, inventoryID string) ([]Result, error) {
	return m.runAnsible(hosts, inventoryID, "-m", "ping")
}

// PingTCP 主机连通性检测: ICMP 优先 (需原始套接字权限), 无权限/超时/被防火墙拦截时自动降级 TCP 探测管理端口。
// 服务器端跨平台可用 (Windows 无管理员权限时自动走 TCP)。
func (m *Manager) PingTCP(hosts []string) []Result {
	all := m.ListHosts()
	if len(hosts) == 0 {
		for _, h := range all {
			hosts = append(hosts, h.ID)
		}
	}
	byID := map[string]Host{}
	for _, h := range all {
		byID[h.ID] = h
	}
	start := time.Now()
	var results []Result
	for _, id := range hosts {
		h, ok := byID[id]
		if !ok {
			results = append(results, Result{Host: id, Success: false, Output: "主机不存在"})
			continue
		}
		if d, err := icmpPing(h.Addr, 2*time.Second); err == nil {
			results = append(results, Result{Host: id, Success: true, Output: fmt.Sprintf("ICMP 可达 (%s)", d.Round(time.Millisecond))})
			continue
		}
		addr := net.JoinHostPort(h.Addr, strconv.Itoa(h.defaultPort()))
		conn, err := net.DialTimeout("tcp", addr, 3*time.Second)
		if err != nil {
			results = append(results, Result{Host: id, Success: false, Output: "ICMP 不可达, TCP " + addr + " 连接失败 (" + err.Error() + ")"})
			continue
		}
		conn.Close()
		results = append(results, Result{Host: id, Success: true, Output: fmt.Sprintf("ICMP 不可达, TCP 端口 %d 可达", h.defaultPort())})
	}
	m.saveHistory("ping", "", &RunContext{Hosts: hosts, Module: "ping", Args: "icmp+tcp"}, results, time.Since(start))
	return results
}

func (m *Manager) Adhoc(hosts []string, inventoryID, module, args string) ([]Result, error) {
	cmd := []string{"-m", module}
	if args != "" {
		cmd = append(cmd, "-a", args)
	}
	return m.runAnsible(hosts, inventoryID, cmd...)
}

func (m *Manager) RunPlaybook(playbookID, inventoryID string, checkMode bool, tags, extraVars, limit string, forks int) ([]Result, error) {
	playbook := m.GetPlaybook(playbookID)
	if playbook == nil {
		return nil, fmt.Errorf("playbook %s 不存在", playbookID)
	}
	return m.runAnsiblePlaybook(playbook.Path, inventoryID, checkMode, tags, extraVars, limit, forks)
}

func (m *Manager) runAnsible(hosts []string, inventoryID string, extraArgs ...string) ([]Result, error) {
	workDir, err := os.MkdirTemp("", "opscore-ansible-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(workDir)

	inventoryPath, targetHosts, err := m.prepareInventory(workDir, hosts, inventoryID)
	if err != nil {
		return nil, err
	}

	args := []string{"-i", inventoryPath, "all"}
	args = append(args, extraArgs...)
	cmd := exec.Command("ansible", args...)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	start := time.Now()

	if runErr := cmd.Run(); runErr != nil {
		log.Printf("[ansible] 子进程退出码非零(可能部分失败): %v", runErr)
	}
	elapsed := time.Since(start)

	out := strings.TrimSpace(stdout.String())
	var results []Result
	if strings.HasPrefix(out, "{") {
		results = parseAnsibleJSON(out, targetHosts)
	} else if out != "" {
		results = parseAnsibleOutput(out, targetHosts)
	} else {
		errMsg := strings.TrimSpace(stderr.String())
		if strings.Contains(errMsg, "ansible-core") || strings.Contains(errMsg, "not found") {
			return nil, fmt.Errorf("ansible 未安装或不在 PATH 中")
		}
		return nil, fmt.Errorf("执行失败: %s", errMsg)
	}

	mod, arg := "", ""
	for i, a := range extraArgs {
		if a == "-m" && i+1 < len(extraArgs) {
			mod = extraArgs[i+1]
		}
		if a == "-a" && i+1 < len(extraArgs) {
			arg = extraArgs[i+1]
		}
	}
	rc := &RunContext{Hosts: hosts, InventoryID: inventoryID, Module: mod, Args: arg}
	m.saveHistory("adhoc", "", rc, results, elapsed)
	return results, nil
}

func (m *Manager) runAnsiblePlaybook(playbookPath, inventoryID string, checkMode bool, tags, extraVars, limit string, forks int) ([]Result, error) {
	workDir, err := os.MkdirTemp("", "opscore-ansible-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(workDir)

	inventoryPath, targetHosts, err := m.prepareInventory(workDir, nil, inventoryID)
	if err != nil {
		return nil, err
	}

	args := []string{"-i", inventoryPath, playbookPath, "--json"}
	if checkMode {
		args = append(args, "--check")
	}
	if tags != "" {
		args = append(args, "--tags", tags)
	}
	if extraVars != "" {
		args = append(args, "--extra-vars", extraVars)
	}
	if limit != "" {
		args = append(args, "--limit", limit)
	}
	if forks > 0 {
		args = append(args, "--forks", fmt.Sprintf("%d", forks))
	}

	cmd := exec.Command("ansible-playbook", args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	start := time.Now()

	if runErr := cmd.Run(); runErr != nil {
		log.Printf("[ansible] 子进程退出码非零(可能部分失败): %v", runErr)
	}
	elapsed := time.Since(start)

	out := strings.TrimSpace(stdout.String())
	var results []Result
	if strings.HasPrefix(out, "{") {
		results = parseAnsibleJSON(out, targetHosts)
	} else if out != "" {
		results = parseAnsibleOutput(out, targetHosts)
	} else {
		errMsg := strings.TrimSpace(stderr.String())
		if errMsg == "" {
			errMsg = "执行失败(无输出)"
		}
		results = []Result{{Host: "all", Success: false, Output: errMsg}}
	}

	rc := &RunContext{InventoryID: inventoryID, CheckMode: checkMode, Tags: tags, ExtraVars: extraVars, Limit: limit, Forks: forks}
	m.saveHistory("playbook", playbookPath, rc, results, elapsed)
	return results, nil
}

func (m *Manager) prepareInventory(workDir string, hosts []string, inventoryID string) (string, []Host, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	var targetHosts []Host
	var inventoryPath string

	if inventoryID != "" {
		for _, inv := range m.inventories {
			if inv.ID == inventoryID {
				content := inv.RenderINI()
				inventoryPath = filepath.Join(workDir, "inventory.ini")
				os.WriteFile(inventoryPath, []byte(content), 0644)

				if len(hosts) > 0 {
					set := make(map[string]bool, len(hosts))
					for _, id := range hosts {
						set[id] = true
					}
					for _, h := range m.hosts {
						if set[h.ID] {
							targetHosts = append(targetHosts, h)
						}
					}
				} else {
					for _, h := range m.hosts {
						targetHosts = append(targetHosts, h)
					}
				}
				return inventoryPath, targetHosts, nil
			}
		}
		return "", nil, fmt.Errorf("清单 %s 不存在", inventoryID)
	}

	if len(hosts) == 0 {
		targetHosts = make([]Host, len(m.hosts))
		copy(targetHosts, m.hosts)
	} else {
		set := make(map[string]bool, len(hosts))
		for _, id := range hosts {
			set[id] = true
		}
		for _, h := range m.hosts {
			if set[h.ID] {
				targetHosts = append(targetHosts, h)
			}
		}
	}

	if len(targetHosts) == 0 {
		return "", nil, fmt.Errorf("无可用主机")
	}

	inventoryPath = filepath.Join(workDir, "inventory.ini")
	writeSimpleInventory(inventoryPath, targetHosts)
	return inventoryPath, targetHosts, nil
}

func writeSimpleInventory(path string, hosts []Host) {
	var buf bytes.Buffer
	buf.WriteString("[all]\n")
	for _, h := range hosts {
		buf.WriteString(fmt.Sprintf("%s ansible_host=%s ansible_port=%d ansible_user=%s", h.ID, h.Addr, h.Port, h.User))
		if h.SSHKey != "" {
			buf.WriteString(fmt.Sprintf(" ansible_ssh_private_key_file=%s", h.SSHKey))
		}
		buf.WriteByte('\n')
	}
	os.WriteFile(path, buf.Bytes(), 0644)
}

func (m *Manager) saveHistory(rtype, target string, rc *RunContext, results []Result, elapsed time.Duration) {
	allSuccess := len(results) > 0
	for _, r := range results {
		if !r.Success {
			allSuccess = false
			break
		}
	}
	rec := ExecRecord{
		ID:       fmt.Sprintf("exec_%d", time.Now().UnixNano()),
		Time:     time.Now(),
		Type:     rtype,
		Target:   target,
		Results:  results,
		Success:  allSuccess,
		Duration: elapsed.Round(time.Millisecond).String(),
		Run:      rc,
	}
	m.mu.Lock()
	m.history = append(m.history, rec)
	if len(m.history) > 100 {
		m.history = m.history[len(m.history)-100:]
	}
	saveJSON(m.historyFile, m.history)
	m.mu.Unlock()
}

func (m *Manager) History() []ExecRecord {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]ExecRecord, len(m.history))
	copy(out, m.history)
	return out
}

func (m *Manager) ClearHistory() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.history = nil
	return saveJSON(m.historyFile, m.history)
}

func (m *Manager) Rerun(id string) (string, *RunContext, error) {
	m.mu.Lock()
	var rec *ExecRecord
	for i := range m.history {
		if m.history[i].ID == id {
			rec = &m.history[i]
			break
		}
	}
	m.mu.Unlock()
	if rec == nil {
		return "", nil, fmt.Errorf("记录 %s 不存在", id)
	}
	if rec.Run == nil {
		return rec.Type, nil, fmt.Errorf("该记录无执行上下文，无法重跑")
	}
	return rec.Type, rec.Run, nil
}

func parseAnsibleJSON(output string, hosts []Host) []Result {
	var raw map[string]any
	if err := json.Unmarshal([]byte(output), &raw); err != nil {
		return []Result{{Host: "all", Success: false, Output: output}}
	}

	aliasMap := map[string]string{}
	for _, h := range hosts {
		aliasMap[h.ID] = h.Alias
	}

	var results []Result
	for hostID, hostData := range raw {
		if hostMap, ok := hostData.(map[string]any); ok {
			r := Result{Host: hostID, Success: true}
			if a, ok := aliasMap[hostID]; ok && a != "" {
				r.Host = a
			}
			if hostMap["unreachable"] == true || hostMap["failed"] == true {
				r.Success = false
			}
			if c, ok := hostMap["changed"]; ok {
				r.Changed = c == true
			}
			msg, _ := json.MarshalIndent(hostMap, "", "  ")
			r.Output = string(msg)
			if so, ok := hostMap["stdout"]; ok {
				r.Stdout = fmt.Sprintf("%v", so)
			}
			if se, ok := hostMap["stderr"]; ok {
				r.Stderr = fmt.Sprintf("%v", se)
			}
			results = append(results, r)
		}
	}
	return results
}
