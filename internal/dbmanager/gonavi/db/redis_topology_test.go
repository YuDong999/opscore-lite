package db

import (
	"strings"
	"testing"

	"opscore/internal/dbmanager/gonavi/connection"
)

// Redis 三种拓扑的判定测试(第 5 项)。
//
// 这一组防的是"用户填了集群/哨兵配置, 我们却按单机连" —— 那种错误会让人以为
// "集群支持了", 实际只连到了种子节点里的一个, 看到的是分片数据的一部分。
func TestRedisUniversalOptionsTopologySelection(t *testing.T) {
	cases := []struct {
		name       string
		cfg        connection.ConnectionConfig
		wantTopo   string
		wantAddrs  int
		wantMaster string
	}{
		{
			name:      "单机: 只有 host/port",
			cfg:       connection.ConnectionConfig{Host: "127.0.0.1", Port: 6379},
			wantTopo:  redisTopologySingle,
			wantAddrs: 1,
		},
		{
			name:      "单机: host 没带端口时补 6379",
			cfg:       connection.ConnectionConfig{Host: "redis.local"},
			wantTopo:  redisTopologySingle,
			wantAddrs: 1,
		},
		{
			name:      "cluster: 显式 topology + 多地址",
			cfg:       connection.ConnectionConfig{Topology: "cluster", Hosts: []string{"a:6379", "b:6379", "c:6379"}},
			wantTopo:  redisTopologyCluster,
			wantAddrs: 3,
		},
		{
			name:      "cluster: 给了多地址但没写 topology, 也应判为 cluster",
			cfg:       connection.ConnectionConfig{Hosts: []string{"a:6379", "b:6379"}},
			wantTopo:  redisTopologyCluster,
			wantAddrs: 2,
		},
		{
			name: "sentinel: topology + master + 哨兵列表",
			cfg: connection.ConnectionConfig{Topology: "sentinel", RedisSentinelMaster: "mymaster",
				Hosts: []string{"s1:26379", "s2:26379"}},
			wantTopo:   redisTopologySentinel,
			wantAddrs:  2,
			wantMaster: "mymaster",
		},
		{
			name:       "sentinel: 只填了 master 名(没写 topology)也认",
			cfg:        connection.ConnectionConfig{RedisSentinelMaster: "mymaster", Hosts: []string{"s1:26379"}},
			wantTopo:   redisTopologySentinel,
			wantAddrs:  1,
			wantMaster: "mymaster",
		},
	}
	for _, c := range cases {
		uopts, topo, err := redisUniversalOptions(c.cfg)
		if err != nil {
			t.Errorf("%s: 不该报错: %v", c.name, err)
			continue
		}
		if topo != c.wantTopo {
			t.Errorf("%s: 拓扑 = %s, 期望 %s", c.name, topo, c.wantTopo)
		}
		if len(uopts.Addrs) != c.wantAddrs {
			t.Errorf("%s: 地址数 = %d, 期望 %d (%v)", c.name, len(uopts.Addrs), c.wantAddrs, uopts.Addrs)
		}
		if uopts.MasterName != c.wantMaster {
			t.Errorf("%s: master = %q, 期望 %q", c.name, uopts.MasterName, c.wantMaster)
		}
	}
}

// 冲突/缺项必须报错, 不能猜。
func TestRedisUniversalOptionsRejectsBadConfig(t *testing.T) {
	cases := []struct {
		name string
		cfg  connection.ConnectionConfig
		want string // 错误里应含的关键字
	}{
		{"没有地址", connection.ConnectionConfig{}, "缺少地址"},
		{"说是 sentinel 但没 master", connection.ConnectionConfig{Topology: "sentinel", Hosts: []string{"s:26379"}}, "master"},
		{"cluster 还给了 master 名", connection.ConnectionConfig{Topology: "cluster", RedisSentinelMaster: "m",
			Hosts: []string{"a:1", "b:2"}}, "Cluster"},
	}
	for _, c := range cases {
		_, _, err := redisUniversalOptions(c.cfg)
		if err == nil {
			t.Errorf("%s: 应报错", c.name)
			continue
		}
		if !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: 错误信息应含 %q, 实得: %v", c.name, c.want, err)
		}
	}
}

// 哨兵凭据要与数据节点凭据分开传(哨兵常与数据节点不同账号)。
func TestRedisUniversalOptionsSentinelCredentials(t *testing.T) {
	uopts, _, err := redisUniversalOptions(connection.ConnectionConfig{
		Topology: "sentinel", RedisSentinelMaster: "m",
		Hosts:                 []string{"s1:26379"},
		User:                  "datauser",
		Password:              "datapass",
		RedisSentinelUser:     "sentuser",
		RedisSentinelPassword: "sentpass",
	})
	if err != nil {
		t.Fatal(err)
	}
	if uopts.Username != "datauser" || uopts.Password != "datapass" {
		t.Errorf("数据节点凭据不对: %q/%q", uopts.Username, uopts.Password)
	}
	if uopts.SentinelUsername != "sentuser" || uopts.SentinelPassword != "sentpass" {
		t.Errorf("哨兵凭据应单独传: %q/%q", uopts.SentinelUsername, uopts.SentinelPassword)
	}
}

// normalizeRedisHosts: 补端口、拆多地址的容错。
func TestNormalizeRedisHosts(t *testing.T) {
	got := normalizeRedisHosts(connection.ConnectionConfig{Hosts: []string{"a", "b:6380"}})
	if len(got) != 2 || got[0] != "a:6379" || got[1] != "b:6380" {
		t.Errorf("应给缺端口的补 6379: %v", got)
	}
	// 只有 host/port 时也要能出地址
	got = normalizeRedisHosts(connection.ConnectionConfig{Host: "h", Port: 6381})
	if len(got) != 1 || got[0] != "h:6381" {
		t.Errorf("单机地址: %v", got)
	}
	// 都没有 → 空
	if got := normalizeRedisHosts(connection.ConnectionConfig{}); got != nil {
		t.Errorf("无地址应返回 nil: %v", got)
	}
}
