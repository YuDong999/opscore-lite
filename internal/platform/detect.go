package platform

import (
	"bufio"
	"os"
	"os/exec"
	"strings"
)

// lookPath 包装 exec.LookPath, 找不到返回 false.
func lookPath(name string) bool {
	_, err := exec.LookPath(name)
	return err == nil
}

// parseOSReleaseFile 读取 /etc/os-release 片段填充 RawOSRelease + Distro + Version.
func parseOSReleaseFile(path string, p *PlatformProfile) {
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		kv := strings.SplitN(line, "=", 2)
		if len(kv) != 2 {
			continue
		}
		key := strings.TrimSpace(kv[0])
		val := strings.Trim(strings.TrimSpace(kv[1]), `"`)
		p.RawOSRelease[key] = val
		switch key {
		case "ID":
			if p.Distro == "" {
				p.Distro = val
			}
		case "VERSION_ID":
			p.Version = val
		}
	}
}

// lsblkSupportsJSON 探测 lsblk 是否支持 -J (util-linux >= 2.27).
func lsblkSupportsJSON() bool {
	out, err := exec.Command("lsblk", "-J", "-o", "NAME", "-n").CombinedOutput()
	if err != nil {
		return false
	}
	s := strings.TrimSpace(string(out))
	return strings.HasPrefix(s, "{")
}

// detectInit 探测本机初始化系统.
func detectInit() InitSystem {
	if lookPath("systemctl") {
		return InitSystemd
	}
	if lookPath("rc-service") || lookPath("openrc") {
		return InitOpenRC
	}
	if lookPath("sv") {
		return InitRunit
	}
	return InitUnknown
}

// DetectLocal 探测本机平台能力(本机 agent / 调试用).
func DetectLocal() PlatformProfile {
	p := PlatformProfile{RawOSRelease: map[string]string{}}
	p.Arch = localArch()
	parseOSReleaseFile("/etc/os-release", &p)
	if p.Distro == "" {
		parseOSReleaseFile("/usr/lib/os-release", &p)
	}
	p.Family = FamilyOf(p.RawOSRelease["ID"], p.RawOSRelease["ID_LIKE"])
	if pm := PkgManagerOf(p.Family, p.Distro, p.Version); pm != PkgUnknown {
		p.PkgManager = pm
	}
	if k, ok := LookupInventory(p.Distro, p.Version); ok && p.Init == "" {
		p.Init = k.Init
	}
	if p.Init == "" {
		p.Init = detectInit()
	}
	// 命令可用性探测
	p.HasNM = lookPath("nmcli")
	p.HasResolvectl = lookPath("resolvectl")
	p.HasJournalctl = lookPath("journalctl")
	p.HasFirewalld = lookPath("firewall-cmd")
	p.HasUfw = lookPath("ufw")
	p.HasParted = lookPath("parted")
	p.HasSmartctl = lookPath("smartctl")
	p.HasXFSProgs = lookPath("mkfs.xfs")
	p.HasLVM = lookPath("lvm")
	p.HasIPTables = lookPath("iptables")
	p.HasLSBlkJSON = lsblkSupportsJSON()
	return p
}

// ProbeScript 返回一段 shell 脚本, 在远程主机一次执行, 输出以 __OPSCORE_PROBE__ 哨兵
// 起始的 key=value 能力探测结果. 与 remote.SnapshotScript 风格一致, 便于单 SSH 往返.
const ProbeScript = `
echo __OPSCORE_PROBE__
. /etc/os-release 2>/dev/null
echo ID=${ID:-}
echo VERSION_ID=${VERSION_ID:-}
echo ID_LIKE=${ID_LIKE:-}
echo ARCH=$(uname -m 2>/dev/null)
if command -v systemctl >/dev/null 2>&1; then echo INIT=systemd
elif command -v rc-service >/dev/null 2>&1; then echo INIT=openrc
elif command -v sv >/dev/null 2>&1; then echo INIT=runit
else echo INIT=unknown; fi
_h(){ command -v "$1" >/dev/null 2>&1 && echo 1 || echo 0; }
echo HAS_NM=$(_h nmcli)
echo HAS_RESOLVECTL=$(_h resolvectl)
echo HAS_JOURNALCTL=$(_h journalctl)
echo HAS_FIREWALLD=$(_h firewall-cmd)
echo HAS_UFW=$(_h ufw)
echo HAS_PARTED=$(_h parted)
echo HAS_SMARTCTL=$(_h smartctl)
echo HAS_XFSPROGS=$(_h mkfs.xfs)
echo HAS_LVM=$(_h lvm)
echo HAS_IPTABLES=$(_h iptables)
_lj=$(lsblk -J -o NAME -n 2>/dev/null | head -c1)
if [ "$_lj" = "{" ]; then echo HAS_LSBLK_JSON=1; else echo HAS_LSBLK_JSON=0; fi
`

// ParseProbe 解析 ProbeScript 的输出为 PlatformProfile.
// out 可以包含其他文本, 只取 __OPSCORE_PROBE__ 之后到文件末尾的 key=value.
func ParseProbe(out string) PlatformProfile {
	p := PlatformProfile{RawOSRelease: map[string]string{}}
	idx := strings.Index(out, "__OPSCORE_PROBE__")
	if idx < 0 {
		// 没哨兵: 整段当作探测输出尝试解析(兼容直接回显)
		idx = 0
	} else {
		idx += len("__OPSCORE_PROBE__")
	}
	sc := bufio.NewScanner(strings.NewReader(out[idx:]))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || !strings.Contains(line, "=") {
			continue
		}
		kv := strings.SplitN(line, "=", 2)
		key := strings.TrimSpace(kv[0])
		val := strings.TrimSpace(kv[1])
		switch key {
		case "ID":
			p.Distro = val
		case "VERSION_ID":
			p.Version = val
		case "ID_LIKE":
			p.RawOSRelease["ID_LIKE"] = val
		case "ARCH":
			p.Arch = NormalizeArch(val)
		case "INIT":
			p.Init = InitSystem(val)
		case "HAS_NM":
			p.HasNM = val == "1"
		case "HAS_RESOLVECTL":
			p.HasResolvectl = val == "1"
		case "HAS_JOURNALCTL":
			p.HasJournalctl = val == "1"
		case "HAS_FIREWALLD":
			p.HasFirewalld = val == "1"
		case "HAS_UFW":
			p.HasUfw = val == "1"
		case "HAS_PARTED":
			p.HasParted = val == "1"
		case "HAS_SMARTCTL":
			p.HasSmartctl = val == "1"
		case "HAS_XFSPROGS":
			p.HasXFSProgs = val == "1"
		case "HAS_LVM":
			p.HasLVM = val == "1"
		case "HAS_IPTABLES":
			p.HasIPTables = val == "1"
		case "HAS_LSBLK_JSON":
			p.HasLSBlkJSON = val == "1"
		}
	}
	p.Family = FamilyOf(p.Distro, p.RawOSRelease["ID_LIKE"])
	if pm := PkgManagerOf(p.Family, p.Distro, p.Version); pm != PkgUnknown {
		p.PkgManager = pm
	}
	if k, ok := LookupInventory(p.Distro, p.Version); ok && p.Init == "" {
		p.Init = k.Init
	}
	return p
}
