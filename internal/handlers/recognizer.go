package handlers

import "strings"

// SvcMeta 是对一个"已知服务"的展示元数据。
type SvcMeta struct {
	Label    string `json:"label"`
	Category string `json:"category"`
	Icon     string `json:"icon"`
}

// knownByProc 按"进程名 / systemd 单元名"识别常见服务。
// 关键:进程名或单元名本身就是事实来源(systemd 单元、/proc 进程表),
// 不像端口那样可以被任意进程占用,因此可以安全地直接标注。
var knownByProc = map[string]SvcMeta{
	"mysql":         {Label: "MySQL", Category: "数据库", Icon: "🐬"},
	"mysqld":        {Label: "MySQL", Category: "数据库", Icon: "🐬"},
	"mariadbd":      {Label: "MariaDB", Category: "数据库", Icon: "🐬"},
	"mariadb":       {Label: "MariaDB", Category: "数据库", Icon: "🐬"},
	"postgres":      {Label: "PostgreSQL", Category: "数据库", Icon: "🐘"},
	"postmaster":    {Label: "PostgreSQL", Category: "数据库", Icon: "🐘"},
	"redis":         {Label: "Redis", Category: "缓存", Icon: "🔴"},
	"redis-server":  {Label: "Redis", Category: "缓存", Icon: "🔴"},
	"mongod":        {Label: "MongoDB", Category: "数据库", Icon: "🍃"},
	"nginx":         {Label: "Nginx", Category: "Web 服务器", Icon: "🌐"},
	"apache2":       {Label: "Apache", Category: "Web 服务器", Icon: "🌐"},
	"httpd":         {Label: "Apache", Category: "Web 服务器", Icon: "🌐"},
	"sshd":          {Label: "OpenSSH", Category: "远程访问", Icon: "🔑"},
	"docker":        {Label: "Docker", Category: "容器", Icon: "🐳"},
	"dockerd":       {Label: "Docker", Category: "容器", Icon: "🐳"},
	"containerd":    {Label: "containerd", Category: "容器", Icon: "🐳"},
	"kubelet":       {Label: "Kubelet", Category: "容器编排", Icon: "☸️"},
	"rabbitmq":      {Label: "RabbitMQ", Category: "消息队列", Icon: "🐰"},
	"elasticsearch": {Label: "Elasticsearch", Category: "搜索", Icon: "🔍"},
	"memcached":     {Label: "Memcached", Category: "缓存", Icon: "🟠"},
	"influxd":       {Label: "InfluxDB", Category: "数据库", Icon: "📈"},
	"etcd":          {Label: "etcd", Category: "协调服务", Icon: "🗄️"},
	"consul":        {Label: "Consul", Category: "协调服务", Icon: "🗂️"},
	"prometheus":    {Label: "Prometheus", Category: "监控", Icon: "🔥"},
	"grafana":       {Label: "Grafana", Category: "监控", Icon: "📊"},
	"alertmanager":  {Label: "Alertmanager", Category: "监控", Icon: "🔔"},
	"php-fpm":       {Label: "PHP-FPM", Category: "Web", Icon: "🐘"},
}

// knownByPort 仅作"常见端口"提示,绝不作为身份结论。
// 真正的身份必须以占用端口的进程(PID→进程名)为准,二者一致才标"已确认"。
// 数据参考自常用端口速查(知乎 p/74782191),仅收录标准服务端口,不含木马/后门端口。
var knownByPort = map[uint16]SvcMeta{
	// 文件传输 / 远程访问
	20:  {Label: "FTP-Data", Category: "文件传输", Icon: "📁"},
	21:  {Label: "FTP", Category: "文件传输", Icon: "📁"},
	22:  {Label: "SSH", Category: "远程访问", Icon: "🔑"},
	23:  {Label: "Telnet", Category: "远程访问", Icon: "🖥️"},
	69:  {Label: "TFTP", Category: "文件传输", Icon: "📁"},
	3389: {Label: "RDP", Category: "远程访问", Icon: "🖥️"},
	5632: {Label: "pcAnywhere", Category: "远程访问", Icon: "🖥️"},
	// 邮件
	25:  {Label: "SMTP", Category: "邮件", Icon: "✉️"},
	110: {Label: "POP3", Category: "邮件", Icon: "✉️"},
	143: {Label: "IMAP", Category: "邮件", Icon: "✉️"},
	465: {Label: "SMTPS", Category: "邮件", Icon: "🔒"},
	587: {Label: "SMTP-Sub", Category: "邮件", Icon: "✉️"},
	993: {Label: "IMAPS", Category: "邮件", Icon: "🔒"},
	995: {Label: "POP3S", Category: "邮件", Icon: "🔒"},
	// DNS / 网络基础
	53:  {Label: "DNS", Category: "网络", Icon: "🌍"},
	67:  {Label: "DHCP", Category: "网络", Icon: "🌍"},
	68:  {Label: "DHCP", Category: "网络", Icon: "🌍"},
	88:  {Label: "Kerberos", Category: "认证", Icon: "🛡️"},
	111: {Label: "RPC", Category: "网络", Icon: "🔗"},
	123: {Label: "NTP", Category: "网络", Icon: "⏱️"},
	135: {Label: "MS-RPC", Category: "Windows", Icon: "🪟"},
	137: {Label: "NetBIOS-NS", Category: "Windows", Icon: "🪟"},
	138: {Label: "NetBIOS-DGM", Category: "Windows", Icon: "🪟"},
	139: {Label: "NetBIOS-SSN", Category: "Windows", Icon: "🪟"},
	161: {Label: "SNMP", Category: "监控", Icon: "📡"},
	162: {Label: "SNMP-Trap", Category: "监控", Icon: "📡"},
	179: {Label: "BGP", Category: "网络", Icon: "🌐"},
	389: {Label: "LDAP", Category: "目录", Icon: "📇"},
	445: {Label: "SMB/CIFS", Category: "文件共享", Icon: "📂"},
	464: {Label: "Kerberos-kpasswd", Category: "认证", Icon: "🛡️"},
	514: {Label: "Syslog", Category: "日志", Icon: "📜"},
	636: {Label: "LDAPS", Category: "目录", Icon: "🔒"},
	2049: {Label: "NFS", Category: "文件共享", Icon: "📂"},
	// Web / 代理
	80:    {Label: "HTTP", Category: "Web", Icon: "🌐"},
	443:   {Label: "HTTPS", Category: "Web", Icon: "🔒"},
	3128:  {Label: "Squid", Category: "代理", Icon: "🧭"},
	7001:  {Label: "WebLogic", Category: "Web", Icon: "🌐"},
	8080:  {Label: "HTTP-Alt", Category: "Web", Icon: "🌐"},
	8081:  {Label: "代理/中间件", Category: "代理", Icon: "🧭"},
	8443:  {Label: "HTTPS-Alt", Category: "Web", Icon: "🔒"},
	9080:  {Label: "WebSphere", Category: "Web", Icon: "🌐"},
	9090:  {Label: "Prometheus", Category: "监控", Icon: "🔥"},
	// 数据库 / 缓存 / 消息
	1433:  {Label: "MS SQL", Category: "数据库", Icon: "🗄️"},
	1434:  {Label: "MS SQL-Mon", Category: "数据库", Icon: "🗄️"},
	1521:  {Label: "Oracle", Category: "数据库", Icon: "🏛️"},
	2181:  {Label: "ZooKeeper", Category: "协调服务", Icon: "🐘"},
	2379:  {Label: "etcd", Category: "协调服务", Icon: "🗄️"},
	3306:  {Label: "MySQL", Category: "数据库", Icon: "🐬"},
	5432:  {Label: "PostgreSQL", Category: "数据库", Icon: "🐘"},
	5672:  {Label: "RabbitMQ", Category: "消息队列", Icon: "🐰"},
	6379:  {Label: "Redis", Category: "缓存", Icon: "🔴"},
	9042:  {Label: "Cassandra", Category: "数据库", Icon: "🗄️"},
	9200:  {Label: "Elasticsearch", Category: "搜索", Icon: "🔍"},
	9300:  {Label: "ES-Transport", Category: "搜索", Icon: "🔍"},
	11211: {Label: "Memcached", Category: "缓存", Icon: "🟠"},
	27017: {Label: "MongoDB", Category: "数据库", Icon: "🍃"},
	// 容器 / 编排 / 监控
	2375:  {Label: "Docker", Category: "容器", Icon: "🐳"},
	2376:  {Label: "Docker-TLS", Category: "容器", Icon: "🐳"},
	6443:  {Label: "K8s-API", Category: "容器编排", Icon: "☸️"},
	8500:  {Label: "Consul", Category: "协调服务", Icon: "🗂️"},
	9092:  {Label: "Kafka", Category: "消息队列", Icon: "📨"},
	9093:  {Label: "Alertmanager", Category: "监控", Icon: "🔔"},
	9100:  {Label: "Node-Exporter", Category: "监控", Icon: "📊"},
	9000:  {Label: "PHP-FPM", Category: "Web", Icon: "🐘"},
}

// recognizeProc 根据进程名 / 单元名识别常见服务。
// 依次尝试:精确 → 前缀 → 子串(最后兜底)。返回识别到的元数据与是否命中。
func recognizeProc(raw string) (SvcMeta, bool) {
	n := strings.ToLower(raw)
	n = strings.TrimSuffix(n, ".service")
	n = strings.TrimSuffix(n, ".socket")
	if m, ok := knownByProc[n]; ok {
		return m, true
	}
	for k, m := range knownByProc {
		if strings.HasPrefix(n, k) {
			return m, true
		}
	}
	for k, m := range knownByProc {
		if strings.Contains(n, k) {
			return m, true
		}
	}
	return SvcMeta{}, false
}

// recognizePort 仅返回端口对应的"常见服务"提示,不表示身份已确认。
func recognizePort(port uint16) (SvcMeta, bool) {
	m, ok := knownByPort[port]
	return m, ok
}

// categoryOfPort 返回端口对应的"常见服务"分类(用于与进程识别结果交叉验证)。
func categoryOfPort(port uint16) string {
	if m, ok := knownByPort[port]; ok {
		return m.Category
	}
	return ""
}
