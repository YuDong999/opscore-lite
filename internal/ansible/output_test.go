package ansible

import "testing"

func TestParseAnsibleOutput_PingJSON(t *testing.T) {
	hosts := []Host{
		{ID: "9aedeedcc05460a6", Addr: "192.168.94.20"},
		{ID: "e433947647e8e773", Addr: "192.168.94.22"},
	}
	out := `9aedeedcc05460a6 | SUCCESS => {
    "changed": false,
    "ping": "pong"
}
e433947647e8e773 | SUCCESS => {
    "changed": false,
    "ping": "pong"
}`
	res := parseAnsibleOutput(out, hosts)
	if len(res) != 2 {
		t.Fatalf("want 2 results, got %d: %+v", len(res), res)
	}
	if res[0].Host != "192.168.94.20" || !res[0].Success || res[0].Output != "pong" {
		t.Errorf("res[0] = %+v", res[0])
	}
	if res[1].Host != "192.168.94.22" || !res[1].Success || res[1].Output != "pong" {
		t.Errorf("res[1] = %+v", res[1])
	}
}

func TestParseAnsibleOutput_Failed(t *testing.T) {
	hosts := []Host{{ID: "a", Addr: "10.0.0.1"}}
	out := `a | FAILED => {
    "changed": false,
    "msg": "Permission denied (publickey,password).",
    "rc": 255
}`
	res := parseAnsibleOutput(out, hosts)
	if len(res) != 1 {
		t.Fatalf("want 1 result, got %d", len(res))
	}
	if res[0].Success || res[0].Output != "Permission denied (publickey,password)." {
		t.Errorf("res[0] = %+v", res[0])
	}
}

func TestParseAnsibleOutput_Unreachable(t *testing.T) {
	hosts := []Host{{ID: "b", Addr: "10.0.0.2"}}
	out := `b | UNREACHABLE! => {
    "changed": false,
    "msg": "Failed to connect to the host via ssh: Connection refused",
    "unreachable": true
}`
	res := parseAnsibleOutput(out, hosts)
	if len(res) != 1 {
		t.Fatalf("want 1 result, got %d", len(res))
	}
	if res[0].Success {
		t.Errorf("unreachable should be failure: %+v", res[0])
	}
}

func TestParseAnsibleOutput_AdhocRc(t *testing.T) {
	hosts := []Host{{ID: "c", Addr: "10.0.0.3"}}
	out := `c | CHANGED | rc=0 >>
hello from remote
second line`
	res := parseAnsibleOutput(out, hosts)
	if len(res) != 1 {
		t.Fatalf("want 1 result, got %d", len(res))
	}
	if !res[0].Success || res[0].Output != "hello from remote" || res[0].Stdout != "hello from remote\nsecond line" {
		t.Errorf("res[0] = %+v", res[0])
	}
}

func TestParseAnsibleOutput_NoMatchFallback(t *testing.T) {
	hosts := []Host{{ID: "d", Addr: "10.0.0.4"}}
	out := "PLAY RECAP ******************************************\nd : ok=2 changed=1 unreachable=0"
	res := parseAnsibleOutput(out, hosts)
	if len(res) != 1 || res[0].Host != "all" || res[0].Output == "" {
		t.Errorf("fallback = %+v", res)
	}
}

func TestParseAnsibleOutput_Empty(t *testing.T) {
	res := parseAnsibleOutput("", []Host{{ID: "e", Addr: "10.0.0.5"}})
	if len(res) != 1 || res[0].Success {
		t.Errorf("empty = %+v", res)
	}
}
