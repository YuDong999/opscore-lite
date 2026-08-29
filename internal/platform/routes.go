package platform

// ── 命令路由: 根据 PlatformProfile 选择对应发行版的命令与解析策略 ──
// 这是用户要求的"agent 根据探测结果复用清单中对应发行版本去获取对方主机数据"的核心实现.

// DiskListCmds 返回磁盘列表采集命令. 优先 lsblk -J (层级), 老版本回退 -ln (平铺).
// 返回 remote 批量执行用的命令 map: devices 为主命令, devices_fb 为回退命令.
func DiskListCmds(p PlatformProfile) map[string]string {
	if p.HasLSBlkJSON {
		return map[string]string{
			"devices": `lsblk -J -o NAME,SIZE,TYPE,FSTYPE,MOUNTPOINT 2>/dev/null`,
		}
	}
	return map[string]string{
		"devices":     `lsblk -J -o NAME,SIZE,TYPE,FSTYPE,MOUNTPOINT 2>/dev/null`,
		"devices_fb":  `lsblk -ln -o NAME,SIZE,TYPE,FSTYPE,MOUNTPOINT 2>/dev/null`,
	}
}

// UpdateCheckCmd 包管理器无关的"检查可用更新"命令.
func UpdateCheckCmd(p PlatformProfile) (string, bool) {
	switch p.PkgManager {
	case PkgDNF:
		return "dnf check-update --security -q 2>/dev/null", true
	case PkgYUM:
		return "yum check-update --security -q 2>/dev/null", true
	case PkgAPT:
		return "apt-get -s upgrade 2>/dev/null | grep -c '^Inst'", true
	case PkgZypper:
		return "zypper --quiet list-updates 2>/dev/null", true
	case PkgPacman:
		return "pacman -Qu 2>/dev/null", true
	case PkgAPK:
		return "apk version -l '<' 2>/dev/null", true
	}
	return "", false
}

// UpdateInstallCmd 包管理器无关的"安装安全更新"命令.
func UpdateInstallCmd(p PlatformProfile) (string, bool) {
	switch p.PkgManager {
	case PkgDNF:
		return "dnf upgrade --security -y", true
	case PkgYUM:
		return "yum update --security -y", true
	case PkgAPT:
		return "apt-get update && apt-get upgrade -y", true
	case PkgZypper:
		return "zypper --non-interactive update --with-interactive -t patch", true
	case PkgPacman:
		return "pacman -Syu --noconfirm", true
	case PkgAPK:
		return "apk upgrade", true
	}
	return "", false
}

// NeedsRestartCmd 检查是否需要重启(软件包内核更新后).
func NeedsRestartCmd(p PlatformProfile) string {
	switch p.PkgManager {
	case PkgDNF, PkgYUM:
		return "needs-restarting -r 2>/dev/null || true"
	case PkgAPT:
		return "test -f /var/run/reboot-required && echo 'Reboot is required' || echo 'No reboot required'"
	case PkgZypper:
		return "zypper needs-reboot 2>/dev/null || true"
	}
	return "true"
}

// FirewallBackend 探测到的防火墙后端: firewalld / ufw / iptables / none.
func FirewallBackend(p PlatformProfile) string {
	switch {
	case p.HasFirewalld:
		return "firewalld"
	case p.HasUfw:
		return "ufw"
	case p.HasIPTables:
		return "iptables"
	}
	return "none"
}

// ServiceManager 返回服务管理后端: systemd / openrc / sysv / none.
func ServiceManager(p PlatformProfile) string {
	switch p.Init {
	case InitSystemd:
		return "systemd"
	case InitOpenRC:
		return "openrc"
	case InitSysV:
		return "sysv"
	}
	return "none"
}

// DNSCmd 返回 DNS 解析配置读取命令(优先 resolvectl, 否则 /etc/resolv.conf).
func DNSCmd(p PlatformProfile) string {
	if p.HasResolvectl {
		return "resolvectl status 2>/dev/null"
	}
	return "cat /etc/resolv.conf 2>/dev/null"
}

// NetConnCmd 返回网络连接列表命令(nmcli 优先, 否则 ip).
func NetConnCmd(p PlatformProfile) string {
	if p.HasNM {
		return "nmcli -t con show 2>/dev/null"
	}
	return "ip -o addr show 2>/dev/null"
}
