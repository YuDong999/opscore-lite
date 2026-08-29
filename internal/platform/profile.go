package platform

import (
	"runtime"
	"strings"
)

// Family 发行版族, 用于归并命令/解析策略.
type Family string

const (
	FamilyRedHat Family = "redhat"  // centos/rhel/fedora/rocky/almalinux/anolis/openeuler/kylin/neokylin
	FamilyDebian Family = "debian"  // ubuntu/debian/uos/linuxmint/deepin
	FamilySUSE   Family = "suse"    // opensuse/sles
	FamilyArch   Family = "arch"    // arch/manjaro
	FamilyAlpine Family = "alpine"  // alpine (OpenRC + BusyBox)
	FamilyKylin  Family = "kylin"   // 银河/中标麒麟
	FamilyUOS    Family = "uos"     // 统信 UOS
	FamilyUnknown Family = "unknown"
)

// InitSystem 初始化系统.
type InitSystem string

const (
	InitSystemd InitSystem = "systemd"
	InitSysV    InitSystem = "sysvinit"
	InitOpenRC  InitSystem = "openrc"
	InitRunit   InitSystem = "runit"
	InitUnknown InitSystem = "unknown"
)

// PkgManager 包管理器.
type PkgManager string

const (
	PkgDNF     PkgManager = "dnf"
	PkgYUM     PkgManager = "yum"
	PkgAPT     PkgManager = "apt"
	PkgZypper  PkgManager = "zypper"
	PkgPacman  PkgManager = "pacman"
	PkgAPK     PkgManager = "apk"
	PkgUnknown PkgManager = "unknown"
)

// PlatformProfile 目标主机的探测结果, 所有命令路由与解析都依据它.
type PlatformProfile struct {
	Distro        string
	Family        Family
	Version       string
	Arch          string
	Init          InitSystem
	PkgManager    PkgManager
	HasNM         bool // NetworkManager / nmcli
	HasResolvectl bool // systemd-resolved
	HasJournalctl bool
	HasFirewalld  bool
	HasUfw        bool
	HasParted     bool
	HasLSBlkJSON  bool // lsblk 支持 -J (util-linux >= 2.27)
	HasSmartctl   bool
	HasXFSProgs   bool // mkfs.xfs
	HasLVM        bool
	HasIPTables   bool
	RawOSRelease  map[string]string
}

// IsUnknown 是否探测失败(无任何发行版信息).
func (p PlatformProfile) IsUnknown() bool {
	return p.Distro == "" && p.Family == FamilyUnknown
}

// IsSystemd 是否使用 systemd 管理服务.
func (p PlatformProfile) IsSystemd() bool {
	return p.Init == InitSystemd
}

func (p PlatformProfile) String() string {
	if p.IsUnknown() {
		return "unknown"
	}
	return p.Distro + " " + p.Version + " (" + string(p.Family) + ")"
}

// localArch 返回本机架构(远程探测用 uname -m, 这里仅本机默认值).
func localArch() string {
	switch runtime.GOARCH {
	case "amd64":
		return "x86_64"
	case "arm64":
		return "aarch64"
	case "loong64":
		return "loongarch64"
	case "mips64":
		return "mips64"
	default:
		return runtime.GOARCH
	}
}

// NormalizeArch 把 uname -m 输出归并到标准名.
func NormalizeArch(a string) string {
	switch strings.ToLower(strings.TrimSpace(a)) {
	case "x86_64", "amd64":
		return "x86_64"
	case "aarch64", "arm64":
		return "aarch64"
	case "loongarch64", "loongarch":
		return "loongarch64"
	case "mips64", "mips64el":
		return "mips64"
	case "ppc64le", "ppc64":
		return "ppc64le"
	case "riscv64":
		return "riscv64"
	case "i386", "i686", "x86":
		return "i386"
	default:
		return strings.TrimSpace(a)
	}
}
