package platform

import "testing"

// 多发行版冒烟样本: 各发行版真实 ProbeScript 输出回放, 验证 ParseProbe + 命令路由。
// 覆盖主流 + 国产 + 最小化(BusyBox)环境, 确保清单里的发行版都被正确处理。
type distroSmoke struct {
	name       string
	probe      string
	fam        Family
	pm         PkgManager
	init       InitSystem
	lsblkJSON  bool
	fwBackend  string // 期望 FirewallBackend 结果
	svcManager string // 期望 ServiceManager 结果
}

func TestDistroSmokeMatrix(t *testing.T) {
	cases := []distroSmoke{
		{
			name: "CentOS 7",
			probe: probeBlock("centos", "7", "rhel fedora", "x86_64", "systemd",
				"1", "0", "1", "1", "0", "1", "1", "1", "1", "1", "0"),
			fam: FamilyRedHat, pm: PkgYUM, init: InitSystemd, lsblkJSON: false,
			fwBackend: "firewalld", svcManager: "systemd",
		},
		{
			name: "CentOS Stream 9",
			probe: probeBlock("centos", "9", "rhel fedora", "x86_64", "systemd",
				"1", "1", "1", "1", "0", "1", "1", "1", "1", "1", "1"),
			fam: FamilyRedHat, pm: PkgDNF, init: InitSystemd, lsblkJSON: true,
			fwBackend: "firewalld", svcManager: "systemd",
		},
		{
			name: "openEuler 22.03",
			probe: probeBlock("openeuler", "22.03", "rhel fedora", "aarch64", "systemd",
				"1", "1", "1", "1", "0", "1", "1", "1", "1", "1", "1"),
			fam: FamilyRedHat, pm: PkgDNF, init: InitSystemd, lsblkJSON: true,
			fwBackend: "firewalld", svcManager: "systemd",
		},
		{
			name: "银河麒麟 V10",
			probe: probeBlock("kylin", "V10", "", "aarch64", "systemd",
				"1", "0", "1", "1", "0", "1", "0", "1", "1", "1", "0"),
			fam: FamilyKylin, pm: PkgYUM, init: InitSystemd, lsblkJSON: false,
			fwBackend: "firewalld", svcManager: "systemd",
		},
		{
			name: "统信 UOS 20",
			probe: probeBlock("uos", "20", "debian", "x86_64", "systemd",
				"1", "1", "1", "0", "1", "1", "1", "1", "1", "1", "1"),
			fam: FamilyUOS, pm: PkgAPT, init: InitSystemd, lsblkJSON: true,
			fwBackend: "ufw", svcManager: "systemd",
		},
		{
			name: "Ubuntu 22.04",
			probe: probeBlock("ubuntu", "22.04", "debian", "x86_64", "systemd",
				"1", "1", "1", "0", "1", "1", "1", "1", "1", "1", "1"),
			fam: FamilyDebian, pm: PkgAPT, init: InitSystemd, lsblkJSON: true,
			fwBackend: "ufw", svcManager: "systemd",
		},
		{
			name: "Debian 12",
			probe: probeBlock("debian", "12", "ubuntu debian", "aarch64", "systemd",
				"1", "1", "1", "0", "1", "1", "1", "1", "1", "1", "1"),
			fam: FamilyDebian, pm: PkgAPT, init: InitSystemd, lsblkJSON: true,
			fwBackend: "ufw", svcManager: "systemd",
		},
		{
			name: "openSUSE Leap 15.5",
			probe: probeBlock("opensuse-leap", "15.5", "suse", "x86_64", "systemd",
				"1", "1", "1", "1", "0", "1", "1", "1", "1", "1", "1"),
			fam: FamilySUSE, pm: PkgZypper, init: InitSystemd, lsblkJSON: true,
			fwBackend: "firewalld", svcManager: "systemd",
		},
		{
			name: "Alpine 3.19 (BusyBox/OpenRC)",
			probe: probeBlock("alpine", "3.19", "", "x86_64", "openrc",
				"0", "0", "0", "0", "0", "1", "0", "0", "1", "1", "0"),
			fam: FamilyAlpine, pm: PkgAPK, init: InitOpenRC, lsblkJSON: false,
			fwBackend: "iptables", svcManager: "openrc",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := ParseProbe(c.probe)
			if p.IsUnknown() {
				t.Fatalf("%s: 探测结果为 unknown", c.name)
			}
			if p.Family != c.fam {
				t.Errorf("%s: Family=%q 期望 %q", c.name, p.Family, c.fam)
			}
			if p.PkgManager != c.pm {
				t.Errorf("%s: PkgManager=%q 期望 %q", c.name, p.PkgManager, c.pm)
			}
			if p.Init != c.init {
				t.Errorf("%s: Init=%q 期望 %q", c.name, p.Init, c.init)
			}
			if p.HasLSBlkJSON != c.lsblkJSON {
				t.Errorf("%s: HasLSBlkJSON=%v 期望 %v", c.name, p.HasLSBlkJSON, c.lsblkJSON)
			}
			if got := FirewallBackend(p); got != c.fwBackend {
				t.Errorf("%s: FirewallBackend=%q 期望 %q", c.name, got, c.fwBackend)
			}
			if got := ServiceManager(p); got != c.svcManager {
				t.Errorf("%s: ServiceManager=%q 期望 %q", c.name, got, c.svcManager)
			}
			// 命令路由一致性
			if _, ok := PkgInstallCmd(p, "lldpd"); !ok {
				t.Errorf("%s: PkgInstallCmd 应可用", c.name)
			}
			cmds := DiskListCmds(p)
			if c.lsblkJSON {
				if _, ok := cmds["devices_fb"]; ok {
					t.Errorf("%s: 支持 -J 时不应有 -ln 回退", c.name)
				}
			} else {
				if _, ok := cmds["devices_fb"]; !ok {
					t.Errorf("%s: 不支持 -J 时应有 -ln 回退", c.name)
				}
			}
		})
	}
}

// probeBlock 拼装一段仿 ProbeScript 的探测输出(顺序与 ProbeScript 哨兵后一致)。
func probeBlock(id, ver, idLike, arch, init,
	nm, resolvectl, journalctl, firewalld, ufw, parted, smartctl, xfs, lvm, iptables, lsblkJSON string) string {
	return `__OPSCORE_PROBE__
ID=` + id + `
VERSION_ID=` + ver + `
ID_LIKE=` + idLike + `
ARCH=` + arch + `
INIT=` + init + `
HAS_NM=` + nm + `
HAS_RESOLVECTL=` + resolvectl + `
HAS_JOURNALCTL=` + journalctl + `
HAS_FIREWALLD=` + firewalld + `
HAS_UFW=` + ufw + `
HAS_PARTED=` + parted + `
HAS_SMARTCTL=` + smartctl + `
HAS_XFSPROGS=` + xfs + `
HAS_LVM=` + lvm + `
HAS_IPTABLES=` + iptables + `
HAS_LSBLK_JSON=` + lsblkJSON + `
`
}
