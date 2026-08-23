package handlers

import "testing"

func TestValidDNSList(t *testing.T) {
	for _, ok := range []string{"8.8.8.8", "8.8.8.8,114.114.114.114", "2001:db8::1", " 8.8.8.8 , 1.1.1.1 "} {
		if !validDNSList(ok) {
			t.Errorf("DNS %q should be valid", ok)
		}
	}
	for _, bad := range []string{"", "not-an-ip", "8.8.8.8; rm -rf /", "8.8.8.8' > /etc/cron.d/pwn && #"} {
		if validDNSList(bad) {
			t.Errorf("DNS %q should be invalid", bad)
		}
	}
}

func TestLVMValidators(t *testing.T) {
	if !validLVMName("vg0") || !validLVMName("data-lv") || !validLVMName("lv_01") {
		t.Error("valid names rejected")
	}
	if validLVMName("vg0; rm -rf /") || validLVMName("a`id`") || validLVMName("-lead") {
		t.Error("malicious names accepted")
	}
	if !validLVMDev("/dev/sdb1") || !validLVMDev("/dev/vg0/data") {
		t.Error("valid devices rejected")
	}
	if validLVMDev("sdb1; rm -rf /") || validLVMDev("dev/sda") || validLVMDev("/dev/sda && id") {
		t.Error("malicious devices accepted")
	}
	for _, ok := range []string{"10G", "512M", "100", "1.5T"} {
		if !validLVMSize(ok) {
			t.Errorf("size %q should be valid", ok)
		}
	}
	for _, bad := range []string{"10G; rm", "G10", ""} {
		if validLVMSize(bad) {
			t.Errorf("size %q should be invalid", bad)
		}
	}
	if !validLVMMount("/mnt/data") || !validLVMMount("/") {
		t.Error("valid mounts rejected")
	}
	if validLVMMount("/mnt/../etc") || validLVMMount("mnt/data") || validLVMMount("/mnt; rm -rf /") {
		t.Error("malicious mounts accepted")
	}
}
