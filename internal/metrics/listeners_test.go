package metrics

import "testing"

func TestParseSS_OldFormat(t *testing.T) {
	out := `State      Recv-Q Send-Q Local Address:Port               Peer Address:Port              
LISTEN     0      128          *:111                      *:*                   users:(("rpcbind",pid=735,fd=8))
LISTEN     0      128    127.0.0.1:10257                    *:*                   users:(("kube-controller",pid=2325,fd=7))
`
	list := ParseSS(out, "TCP")
	if len(list) != 2 {
		t.Fatalf("want 2 listeners, got %d", len(list))
	}
	if list[0].Port != 111 || list[0].Local != "*:111" || list[0].Process != "rpcbind" {
		t.Fatalf("first listener wrong: %+v", list[0])
	}
	if list[1].Port != 10257 || list[1].Process != "kube-controller" {
		t.Fatalf("second listener wrong: %+v", list[1])
	}
}

func TestParseSS_NewFormat(t *testing.T) {
	out := `Netid  State   Recv-Q  Send-Q  Local Address:Port   Peer Address:Port  Process
tcp    LISTEN  0       128     [::]:22               *:*               users:(("sshd",pid=1024,fd=3))
tcp    LISTEN  0       4096   0.0.0.0:8080           *:*               users:(("nginx",pid=7,fd=6))
`
	list := ParseSS(out, "TCP")
	if len(list) != 2 {
		t.Fatalf("want 2 listeners, got %d", len(list))
	}
	if list[0].Port != 22 || list[0].Process != "sshd" || list[0].Local != "[::]:22" {
		t.Fatalf("first listener wrong: %+v", list[0])
	}
	if list[1].Port != 8080 || list[1].Process != "nginx" {
		t.Fatalf("second listener wrong: %+v", list[1])
	}
}

func TestParseSS_NoProcess(t *testing.T) {
	out := "Netid  State   Recv-Q  Send-Q  Local Address:Port   Peer Address:Port\nudp    UNCONN  0       0       0.0.0.0:68              *:*\n"
	list := ParseSS(out, "UDP")
	if len(list) != 1 || list[0].Port != 68 || list[0].Process != "" {
		t.Fatalf("unexpected: %+v", list)
	}
}
