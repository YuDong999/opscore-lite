package handlers

import "testing"

func TestValidatePort(t *testing.T) {
	cases := map[string]bool{
		"8080": true, "1": true, "65535": true,
		"0": false, "-1": false, "65536": false,
		"": false, "22; rm -rf /": false, "abc": false,
		" 80 ": true, // TrimSpace 后合法
	}
	for in, want := range cases {
		got := validatePort(in) != ""
		if got != want {
			t.Errorf("validatePort(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestValidateProto(t *testing.T) {
	if validateProto("") != "tcp" {
		t.Error("empty proto should default to tcp")
	}
	for _, ok := range []string{"TCP", "udp", "sctp"} {
		if validateProto(ok) == "" {
			t.Errorf("proto %q should be valid", ok)
		}
	}
	for _, bad := range []string{"icmp", "tcp; rm", "a b"} {
		if validateProto(bad) != "" {
			t.Errorf("proto %q should be invalid", bad)
		}
	}
}

func TestValidateCIDR(t *testing.T) {
	if validateCIDR("10.0.0.0/8") == "" {
		t.Error("valid CIDR rejected")
	}
	for _, bad := range []string{"", "0.0.0.0/0'; rm -rf / #", "1.2.3.4`id`"} {
		if validateCIDR(bad) != "" {
			t.Errorf("CIDR %q should be invalid", bad)
		}
	}
}

func TestValidateRichRule(t *testing.T) {
	good := "rule family=ipv4 source address=1.2.3.4 accept"
	if validateRichRule(good) == "" {
		t.Error("valid rich rule rejected")
	}
	for _, bad := range []string{"family=ipv4 accept", "rule x' || rm -rf / #", "rule `id`"} {
		if validateRichRule(bad) != "" {
			t.Errorf("rich rule %q should be invalid", bad)
		}
	}
}

func TestValidateZone(t *testing.T) {
	for _, ok := range []string{"public", "dmz", "work-2"} {
		if validateZone(ok) == "" {
			t.Errorf("zone %q should be valid", ok)
		}
	}
	for _, bad := range []string{"public; rm", "pu blic", ""} {
		if validateZone(bad) != "" {
			t.Errorf("zone %q should be invalid", bad)
		}
	}
}

func TestValidateHostPort(t *testing.T) {
	if s, ok := validateHostPort("10.0.0.2:80"); !ok || s != "10.0.0.2:80" {
		t.Errorf("valid host:port rejected: %q %v", s, ok)
	}
	for _, bad := range []string{"10.0.0.2", "10.0.0.2:", "host;rm:80", ":80"} {
		if _, ok := validateHostPort(bad); ok {
			t.Errorf("host:port %q should be invalid", bad)
		}
	}
}

func TestBuildFirewallCommandRejectsInjection(t *testing.T) {
	p := fwCmdParams{Action: "allow-port", Port: "22; rm -rf /", Proto: "tcp"}
	args, _ := buildFirewallCommand("ufw", p)
	if args != nil {
		t.Errorf("injected port should be rejected, got %v", args)
	}
	p = fwCmdParams{Action: "deny-ip", CIDR: "0.0.0.0/0'; rm -rf / #"}
	args, _ = buildFirewallCommand("ufw", p)
	if args != nil {
		t.Errorf("injected CIDR should be rejected, got %v", args)
	}
}
