package platform

import "testing"

func TestPkgInstallCmd(t *testing.T) {
	cases := []struct {
		fam     Family
		distro  string
		version string
		pkg     string
		want    string
	}{
		{FamilyRedHat, "centos", "7", "lldpd", "yum install -y lldpd"},
		{FamilyRedHat, "centos", "8", "lldpd", "dnf install -y lldpd"},
		{FamilyDebian, "ubuntu", "22.04", "lldpd", "apt-get install -y lldpd"},
		{FamilySUSE, "opensuse-leap", "15.5", "lldpd", "zypper --non-interactive install lldpd"},
		{FamilyArch, "arch", "rolling", "lldpd", "pacman -S --noconfirm lldpd"},
		{FamilyAlpine, "alpine", "3.19", "lldpd", "apk add lldpd"},
	}
	for _, c := range cases {
		p := PlatformProfile{
			Family:     c.fam,
			Distro:     c.distro,
			Version:    c.version,
			PkgManager: PkgManagerOf(c.fam, c.distro, c.version),
		}
		got, ok := PkgInstallCmd(p, c.pkg)
		if !ok {
			t.Fatalf("%s/%s: PkgInstallCmd 不应返回 ok=false", c.distro, c.version)
		}
		if got != c.want {
			t.Errorf("%s/%s: got %q want %q", c.distro, c.version, got, c.want)
		}
	}
}

func TestDiskListCmds(t *testing.T) {
	// 支持 -J → 只返回主命令, 无回退
	p := PlatformProfile{HasLSBlkJSON: true}
	cmds := DiskListCmds(p)
	if _, ok := cmds["devices"]; !ok {
		t.Fatal("缺少 devices 命令")
	}
	if _, ok := cmds["devices_fb"]; ok {
		t.Error("支持 -J 时不应有 devices_fb 回退")
	}
	if got := cmds["devices"]; got != `lsblk -J -o NAME,SIZE,TYPE,FSTYPE,MOUNTPOINT 2>/dev/null` {
		t.Errorf("devices 命令=%q", got)
	}

	// 不支持 -J → 返回主命令 + -ln 回退
	p2 := PlatformProfile{HasLSBlkJSON: false}
	cmds2 := DiskListCmds(p2)
	fb, ok := cmds2["devices_fb"]
	if !ok {
		t.Error("不支持 -J 时应有 devices_fb 回退")
	}
	if fb != `lsblk -ln -o NAME,SIZE,TYPE,FSTYPE,MOUNTPOINT 2>/dev/null` {
		t.Errorf("devices_fb=%q", fb)
	}
}

func TestUpdateCheckCmd(t *testing.T) {
	cases := []struct {
		fam Family
		pm  PkgManager
	}{
		{FamilyRedHat, PkgDNF},
		{FamilyDebian, PkgAPT},
		{FamilySUSE, PkgZypper},
		{FamilyArch, PkgPacman},
		{FamilyAlpine, PkgAPK},
		{FamilyKylin, PkgYUM},
	}
	for _, c := range cases {
		if _, ok := UpdateCheckCmd(PlatformProfile{Family: c.fam, PkgManager: c.pm}); !ok {
			t.Errorf("%s: UpdateCheckCmd 应可用", c.fam)
		}
	}
}

func TestNetConnCmd(t *testing.T) {
	withNM := NetConnCmd(PlatformProfile{HasNM: true})
	if withNM != "nmcli -t con show 2>/dev/null" {
		t.Errorf("HasNM=true 应优先 nmcli, got %q", withNM)
	}
	noNM := NetConnCmd(PlatformProfile{HasNM: false})
	if noNM != "ip -o addr show 2>/dev/null" {
		t.Errorf("HasNM=false 应回退 ip, got %q", noNM)
	}
}
