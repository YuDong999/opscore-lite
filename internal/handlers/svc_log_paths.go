package handlers

import (
	"os"
	"path/filepath"
	"strings"
)

// logPaths 按服务名/单元名记录常见日志文件路径。
// 用于补充 journalctl 之外的日志来源。
var logPaths = map[string][]string{
	"nginx":         {"/var/log/nginx/access.log", "/var/log/nginx/error.log"},
	"apache2":       {"/var/log/apache2/access.log", "/var/log/apache2/error.log"},
	"httpd":         {"/var/log/httpd/access_log", "/var/log/httpd/error_log"},
	"postfix":       {"/var/log/mail.log", "/var/log/maillog"},
	"master":        {"/var/log/mail.log", "/var/log/maillog"},
	"qmgr":          {"/var/log/mail.log", "/var/log/maillog"},
	"mysql":         {"/var/log/mysql/error.log"},
	"mysqld":        {"/var/log/mysql/error.log"},
	"mariadbd":      {"/var/log/mariadb/error.log"},
	"mariadb":       {"/var/log/mariadb/error.log"},
	"postgres":      {"/var/log/postgresql/postgresql.log"},
	"postmaster":    {"/var/log/postgresql/postgresql.log"},
	"redis":         {"/var/log/redis/redis-server.log", "/var/log/redis/redis.log"},
	"redis-server":  {"/var/log/redis/redis-server.log", "/var/log/redis/redis.log"},
	"mongod":        {"/var/log/mongodb/mongod.log"},
	"elasticsearch": {"/var/log/elasticsearch/"},
	"memcached":     {"/var/log/memcached.log"},
	"influxd":       {"/var/log/influxdb/influxd.log"},
	"etcd":          {"/var/log/etcd/etcd.log"},
	"consul":        {"/var/log/consul.log"},
	"prometheus":    {"/var/log/prometheus/prometheus.log"},
	"grafana":       {"/var/log/grafana/grafana.log"},
	"alertmanager":  {"/var/log/alertmanager/alertmanager.log"},
	"kubelet":       {"/var/log/kubelet.log", "/var/log/kubelet/kubelet.log"},
	"containerd":    {"/var/log/containerd/containerd.log"},
	"dockerd":       {"/var/log/dockerd.log", "/var/log/docker.log"},
	"docker":        {"/var/log/dockerd.log", "/var/log/docker.log"},
	"kube-apiserver":           {"/var/log/kube-apiserver.log"},
	"kube-controller-manager":  {"/var/log/kube-controller-manager.log"},
	"kube-scheduler":           {"/var/log/kube-scheduler.log"},
	"kube-proxy":               {"/var/log/kube-proxy.log"},
	"calico-node":              {"/var/log/calico/calico.log"},
	"coredns":        {"/var/log/coredns.log"},
	"bird":           {"/var/log/bird/bird.log"},
	"dnsmasq":        {"/var/log/dnsmasq.log"},
	"telegraf":       {"/var/log/telegraf/telegraf.log"},
	"fluentd":        {"/var/log/fluentd/fluentd.log"},
	"fluent-bit":     {"/var/log/fluent-bit/fluent-bit.log"},
	"jenkins":        {"/var/log/jenkins/jenkins.log"},
	"haproxy":        {"/var/log/haproxy.log", "/var/log/haproxy/error.log"},
	"keepalived":     {"/var/log/keepalived.log"},
	"sshd":           {"/var/log/auth.log", "/var/log/secure"},
	"rpcbind":        {"/var/log/rpcbind.log"},
	"node_exporter":  {"/var/log/node_exporter.log"},
	"rabbitmq":       {"/var/log/rabbitmq/rabbit@localhost.log"},
	"zookeeper":      {"/var/log/zookeeper/zookeeper.out"},
}

// knownLogDirs 按服务名记录常见的日志目录（目录内所有 .log 文件）。
var knownLogDirs = map[string]string{
	"elasticsearch": "/var/log/elasticsearch/",
	"postgres":      "/var/log/postgresql/",
	"redis":         "/var/log/redis/",
	"mysql":         "/var/log/mysql/",
	"mariadbd":      "/var/log/mariadb/",
	"mongod":        "/var/log/mongodb/",
	"influxd":       "/var/log/influxdb/",
	"consul":        "/var/log/consul/",
	"prometheus":    "/var/log/prometheus/",
	"grafana":       "/var/log/grafana/",
	"alertmanager":  "/var/log/alertmanager/",
	"containerd":    "/var/log/containerd/",
	"kubelet":       "/var/log/kubelet/",
	"calico-node":   "/var/log/calico/",
	"rabbitmq":      "/var/log/rabbitmq/",
	"zookeeper":     "/var/log/zookeeper/",
	"jenkins":       "/var/log/jenkins/",
}

// detectLogPaths 根据服务单元名/进程名返回可能的日志文件路径。
func detectLogPaths(name string) []string {
	name = strings.ToLower(name)
	name = strings.TrimSuffix(name, ".service")
	name = strings.TrimSuffix(name, ".socket")

	if paths, ok := logPaths[name]; ok {
		return filterExisting(paths)
	}

	for k, paths := range logPaths {
		if strings.HasPrefix(name, k) || strings.Contains(name, k) {
			return filterExisting(paths)
		}
	}

	if dir, ok := knownLogDirs[name]; ok {
		return listLogFiles(dir)
	}

	for k, dir := range knownLogDirs {
		if strings.HasPrefix(name, k) || strings.Contains(name, k) {
			return listLogFiles(dir)
		}
	}

	return nil
}

// filterExisting 只返回实际存在的文件。
func filterExisting(paths []string) []string {
	var out []string
	for _, p := range paths {
		if _, err := os.Stat(p); err == nil {
			out = append(out, p)
		}
	}
	return out
}

// listLogFiles 列出目录下所有 .log 文件（最多 10 个）。
func listLogFiles(dir string) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		if e.Type().IsRegular() && strings.HasSuffix(e.Name(), ".log") {
			out = append(out, filepath.Join(dir, e.Name()))
		}
	}
	if len(out) > 10 {
		out = out[:10]
	}
	return out
}
