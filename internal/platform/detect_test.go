package platform

import "testing"

func TestParseProbe_Basic(t *testing.T) {
	out := `__OPSCORE_PROBE__
ID=centos
VERSION_ID=7
ID_LIKE=rhel fedora
ARCH=x86_64
INIT=systemd
HAS_NM=1
HAS_RESOLVECTL=0
HAS_JOURNALCTL=1
HAS_FIREWALLD=1
HAS_UFW=0
HAS_PARTED=1
HAS_SMARTCTL=1
HAS_XFSPROGS=1
HAS_LVM=1
HAS_IPTABLES=1
HAS_LSBLK_JSON=0
`
	p := ParseProbe(out)
	if p.Distro != "centos" {
		t.Errorf("Distro=%q", p.Distro)
	}
	if p.Family != FamilyRedHat {
		t.Errorf("Family=%q", p.Family)
	}
	if p.Version != "7" {
		t.Errorf("Version=%q", p.Version)
	}
	if p.Arch != "x86_64" {
		t.Errorf("Arch=%q", p.Arch)
	}
	if p.Init != InitSystemd {
		t.Errorf("Init=%q", p.Init)
	}
	if p.PkgManager != PkgYUM {
		t.Errorf("PkgManager=%q (centos7 应为 yum)", p.PkgManager)
	}
	if p.HasLSBlkJSON {
		t.Error("HasLSBlkJSON 应为 false (util-linux 2.23)")
	}
	if !p.HasFirewalld {
		t.Error("HasFirewalld 应为 true")
	}
	if p.HasUfw {
		t.Error("HasUfw 应为 false")
	}
}

func TestParseProbe_NoSentinel(t *testing.T) {
	// 兼容直接回显、无哨兵前缀的输出
	out := "ID=ubuntu\nVERSION_ID=22.04\nID_LIKE=debian\nARCH=aarch64\nINIT=systemd\nHAS_NM=1\nHAS_RESOLVECTL=1\nHAS_JOURNALCTL=1\nHAS_FIREWALLD=0\nHAS_UFW=1\nHAS_PARTED=1\nHAS_SMARTCTL=1\nHAS_XFSPROGS=1\nHAS_LVM=1\nHAS_IPTABLES=1\nHAS_LSBLK_JSON=1\n"
	p := ParseProbe(out)
	if p.Distro != "ubuntu" {
		t.Errorf("Distro=%q", p.Distro)
	}
	if p.Family != FamilyDebian {
		t.Errorf("Family=%q", p.Family)
	}
	if p.PkgManager != PkgAPT {
		t.Errorf("PkgManager=%q", p.PkgManager)
	}
	if !p.HasLSBlkJSON {
		t.Error("HasLSBlkJSON 应为 true")
	}
}

func TestParseProbe_Empty(t *testing.T) {
	p := ParseProbe("")
	if !p.IsUnknown() {
		t.Error("空输出应判定为 unknown")
	}
}

func TestParseProbe_Kylin(t *testing.T) {
	// 银河麒麟 V10 基于 centos, 默认 yum
	out := "ID=kylin\nVERSION_ID=V10\nID_LIKE=\nARCH=aarch64\nINIT=systemd\nHAS_NM=1\nHAS_RESOLVECTL=0\nHAS_JOURNALCTL=1\nHAS_FIREWALLD=1\nHAS_UFW=0\nHAS_PARTED=1\nHAS_SMARTCTL=0\nHAS_XFSPROGS=1\nHAS_LVM=1\nHAS_IPTABLES=1\nHAS_LSBLK_JSON=0\n"
	p := ParseProbe(out)
	if p.Distro != "kylin" {
		t.Errorf("Distro=%q", p.Distro)
	}
	if p.Family != FamilyKylin {
		t.Errorf("Family=%q (应为 kylin)", p.Family)
	}
	if p.PkgManager != PkgYUM {
		t.Errorf("PkgManager=%q (麒麟 V10 应为 yum)", p.PkgManager)
	}
}
