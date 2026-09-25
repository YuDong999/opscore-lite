package logmonitor

import (
	"strings"
	"testing"
)

func TestPollErrLimiterSuppressesRepeatsButKeepsFirstAndChanges(t *testing.T) {
	l := &pollErrLimiter{}
	base := int64(1_700_000_000_000)

	if got := l.decide("集群 1 (k8s)", "无 kubeconfig", base); got != "poll 集群 1 (k8s) 失败: 无 kubeconfig" {
		t.Fatalf("首次必须立刻打, got %q", got)
	}
	if got := l.decide("集群 1 (k8s)", "无 kubeconfig", base+10_000); got != "" {
		t.Fatalf("同一故障在冷却期内应被压掉, got %q", got)
	}
	if got := l.decide("集群 1 (k8s)", "无 kubeconfig", base+pollErrCooldownMs); !strings.Contains(got, "已持续 2 轮") {
		t.Fatalf("冷却到期应打一次并带上被压的轮数, got %q", got)
	}
	if got := l.decide("集群 1 (k8s)", "换了个故障", base+1); got != "poll 集群 1 (k8s) 失败: 换了个故障" {
		t.Fatalf("故障内容变了要立即打, got %q", got)
	}
	l.recover("集群 1 (k8s)")
	if got := l.decide("集群 1 (k8s)", "换了个故障", base+2); got != "poll 集群 1 (k8s) 失败: 换了个故障" {
		t.Fatalf("恢复成功后再次失败要立即打, got %q", got)
	}
}

func TestPollErrLimiterKeepsSeparateKeys(t *testing.T) {
	l := &pollErrLimiter{}
	if l.decide("容器 dbx", "docker 不在 PATH", 1000) == "" || l.decide("容器 pg-test", "docker 不在 PATH", 1000) == "" {
		t.Fatal("不同源的首次失败应各自立即打")
	}
}
