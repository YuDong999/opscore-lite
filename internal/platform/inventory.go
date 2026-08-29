package platform

import (
	"strconv"
	"strings"
)

// DistroKnown 已知发行版清单(兼容性矩阵展示 + 探测前的默认值).
// 这是 opscore 计划支持的发行版基线, 新增发行版时在此登记即可.
type DistroKnown struct {
	Distro     string
	Family     Family
	Versions   []string
	PkgManager PkgManager
	Init       InitSystem
	Notes      string
}

// DistroInventory 多发行版清单: 覆盖主流 + 国产系统, 按族归类.
// 用户要求建立的"多发行版清单"即来源于此, 前端可展示, 后端探测失败时可回退到默认值.
var DistroInventory = []DistroKnown{
	// ── RedHat 族 ──
	{Distro: "centos", Family: FamilyRedHat, Versions: []string{"7", "8", "9", "stream9"}, PkgManager: PkgDNF, Init: InitSystemd, Notes: "CentOS 7 默认 yum 且 util-linux 2.23 不支持 lsblk -J; 8+ 默认 dnf"},
	{Distro: "rhel", Family: FamilyRedHat, Versions: []string{"7", "8", "9"}, PkgManager: PkgDNF, Init: InitSystemd},
	{Distro: "fedora", Family: FamilyRedHat, Versions: []string{"38", "39", "40", "41"}, PkgManager: PkgDNF, Init: InitSystemd},
	{Distro: "rocky", Family: FamilyRedHat, Versions: []string{"8", "9"}, PkgManager: PkgDNF, Init: InitSystemd},
	{Distro: "almalinux", Family: FamilyRedHat, Versions: []string{"8", "9"}, PkgManager: PkgDNF, Init: InitSystemd},
	{Distro: "anolis", Family: FamilyRedHat, Versions: []string{"8", "23", "7"}, PkgManager: PkgDNF, Init: InitSystemd, Notes: "龙蜥 Anolis OS"},
	{Distro: "opencloudos", Family: FamilyRedHat, Versions: []string{"8", "9"}, PkgManager: PkgDNF, Init: InitSystemd},
	{Distro: "openeuler", Family: FamilyRedHat, Versions: []string{"20.03", "22.03", "24.03"}, PkgManager: PkgDNF, Init: InitSystemd, Notes: "openEuler"},
	{Distro: "tencentos", Family: FamilyRedHat, Versions: []string{"2.4", "3.1"}, PkgManager: PkgDNF, Init: InitSystemd, Notes: "腾讯 TencentOS Server"},
	// ── 国产(基于 redhat) ──
	{Distro: "kylin", Family: FamilyKylin, Versions: []string{"V10", "V10SP1", "V10SP2", "V10SP3"}, PkgManager: PkgYUM, Init: InitSystemd, Notes: "银河麒麟 V10 基于 CentOS, 默认 yum/dnf, 部分环境无 smartctl/xfsprogs"},
	{Distro: "neokylin", Family: FamilyKylin, Versions: []string{"7"}, PkgManager: PkgYUM, Init: InitSystemd, Notes: "中标麒麟"},
	// ── Debian 族 ──
	{Distro: "ubuntu", Family: FamilyDebian, Versions: []string{"20.04", "22.04", "24.04"}, PkgManager: PkgAPT, Init: InitSystemd, Notes: "默认 netplan + systemd-networkd / NetworkManager"},
	{Distro: "debian", Family: FamilyDebian, Versions: []string{"11", "12"}, PkgManager: PkgAPT, Init: InitSystemd},
	{Distro: "linuxmint", Family: FamilyDebian, Versions: []string{"21", "22"}, PkgManager: PkgAPT, Init: InitSystemd},
	{Distro: "deepin", Family: FamilyDebian, Versions: []string{"20", "23"}, PkgManager: PkgAPT, Init: InitSystemd},
	// ── 国产(基于 debian) ──
	{Distro: "uos", Family: FamilyUOS, Versions: []string{"20", "20SP1", "1050", "1070"}, PkgManager: PkgAPT, Init: InitSystemd, Notes: "统信 UOS 基于 Debian"},
	// ── SUSE 族 ──
	{Distro: "opensuse-leap", Family: FamilySUSE, Versions: []string{"15.4", "15.5", "15.6"}, PkgManager: PkgZypper, Init: InitSystemd},
	{Distro: "sles", Family: FamilySUSE, Versions: []string{"15"}, PkgManager: PkgZypper, Init: InitSystemd},
	// ── Arch / Alpine ──
	{Distro: "arch", Family: FamilyArch, Versions: []string{"rolling"}, PkgManager: PkgPacman, Init: InitSystemd},
	{Distro: "alpine", Family: FamilyAlpine, Versions: []string{"3.18", "3.19", "3.20"}, PkgManager: PkgAPK, Init: InitOpenRC, Notes: "OpenRC 初始化; BusyBox 工具集, ip/mount/lsblk 参数可能精简"},
}

// FamilyOf 根据 os-release 的 ID / ID_LIKE 推断族; 国产系统优先按具体 ID 匹配.
func FamilyOf(id, idLike string) Family {
	id = strings.ToLower(strings.TrimSpace(id))
	idLike = strings.ToLower(strings.TrimSpace(idLike))
	switch id {
	case "kylin", "neokylin", "kyli", "kylin-linux", "kylin-release":
		return FamilyKylin
	case "uos", "uniontech", "uos-20", "uniontechos":
		return FamilyUOS
	}
	if strings.Contains(idLike, "rhel") || strings.Contains(idLike, "fedora") || strings.Contains(idLike, "centos") {
		return FamilyRedHat
	}
	if strings.Contains(idLike, "debian") || strings.Contains(idLike, "ubuntu") {
		return FamilyDebian
	}
	if strings.Contains(idLike, "suse") {
		return FamilySUSE
	}
	if strings.Contains(idLike, "arch") {
		return FamilyArch
	}
	if strings.Contains(idLike, "alpine") {
		return FamilyAlpine
	}
	switch id {
	case "centos", "rhel", "fedora", "rocky", "almalinux", "anolis", "openeuler", "oracle", "amazon", "tencentos", "opencloudos":
		return FamilyRedHat
	case "ubuntu", "debian", "linuxmint", "raspbian", "deepin":
		return FamilyDebian
	case "opensuse", "opensuse-leap", "sles", "sled":
		return FamilySUSE
	case "arch", "manjaro", "endeavouros":
		return FamilyArch
	case "alpine":
		return FamilyAlpine
	}
	return FamilyUnknown
}

// PkgManagerOf 根据族 + 发行版 + 版本推包管理器默认值(实际以探测为准).
func PkgManagerOf(fam Family, distro, version string) PkgManager {
	switch fam {
	case FamilyDebian, FamilyUOS:
		return PkgAPT
	case FamilySUSE:
		return PkgZypper
	case FamilyArch:
		return PkgPacman
	case FamilyAlpine:
		return PkgAPK
	case FamilyKylin:
		return PkgYUM
	case FamilyRedHat:
		if isMajorLE(version, 7) { // centos7/rhel7/neokylin7 等用 yum
			return PkgYUM
		}
		return PkgDNF
	}
	return PkgUnknown
}

// InitOf 根据族 + 探测结果推初始化系统默认值.
func InitOf(fam Family, hasSystemctl bool) InitSystem {
	if hasSystemctl {
		return InitSystemd
	}
	if fam == FamilyAlpine {
		return InitOpenRC
	}
	return InitUnknown
}

// isMajorLE 取版本主号(兼容 "7" / "7.9" / "V10"), 判断 <= major.
func isMajorLE(version string, major int) bool {
	if version == "" {
		return false
	}
	// 去掉 V/v 前缀, 取第一段
	seg := strings.SplitN(strings.TrimSpace(version), ".", 2)[0]
	seg = strings.TrimLeft(seg, "Vv")
	n, err := strconv.Atoi(seg)
	if err != nil {
		return false
	}
	return n <= major
}

// LookupInventory 在清单中查找已知发行版的默认能力(探测失败时的回退).
func LookupInventory(distro, version string) (DistroKnown, bool) {
	d := strings.ToLower(strings.TrimSpace(distro))
	for _, k := range DistroInventory {
		if strings.ToLower(k.Distro) == d {
			return k, true
		}
	}
	return DistroKnown{}, false
}
