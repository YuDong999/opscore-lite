// 自有 API 类型层（ADR-001 边界纪律）：
// 前端看到的类型与 GoNavi 底座类型解耦 —— service 层负责双向转换，
// 未来更换/拆分底座时 API 契约不变。
package dbmanager

import (
	"strings"

	gonaviConnection "opscore/internal/dbmanager/gonavi/connection"
)

// EngineType 引擎类型（与 GoNavi 底座 *_impl.go 一一对应）。
type EngineType string

const (
	// 关系型 (23)
	EngineMySQL      EngineType = "mysql"
	EngineMySQLAgent EngineType = "mysql_agent"
	EngineMariaDB    EngineType = "mariadb"
	EnginePostgreSQL EngineType = "postgres"
	EngineOracle     EngineType = "oracle"
	EngineGoldendb   EngineType = "goldendb"
	EngineClickHouse EngineType = "clickhouse"
	EngineSQLServer  EngineType = "sqlserver"
	EngineDuckDB     EngineType = "duckdb"
	EngineDameng     EngineType = "dameng"
	EngineGaussDB    EngineType = "gaussdb"
	EngineOpenGauss  EngineType = "opengauss"
	EngineKingbase   EngineType = "kingbase"
	EngineHighgo     EngineType = "highgo"
	EngineOceanBase  EngineType = "oceanbase"
	EngineStarRocks  EngineType = "starrocks"
	EngineTDengine   EngineType = "tdengine"
	EngineTrino      EngineType = "trino"
	EngineVastbase   EngineType = "vastbase"
	EngineIris       EngineType = "iris"
	EngineDiros      EngineType = "diros"
	EngineSphinx     EngineType = "sphinx"
	EngineSQLite     EngineType = "sqlite"
	// 文档/宽表
	EngineMongoDB EngineType = "mongodb"
	// 向量 (3)
	EngineChroma EngineType = "chroma"
	EngineQdrant EngineType = "qdrant"
	EngineMilvus EngineType = "milvus"
	// 时序/IoT
	EngineIoTDB EngineType = "iotdb"
	// 搜索
	EngineElasticsearch EngineType = "elasticsearch"
	// 消息队列 (4)
	EngineKafka    EngineType = "kafka"
	EngineRabbitMQ EngineType = "rabbitmq"
	EngineRocketMQ EngineType = "rocketmq"
	EngineMQTT     EngineType = "mqtt"
	// 自定义 DSN
	EngineCustom EngineType = "custom"
)

// engineTypeSupported 校验引擎类型是否可创建 GoNavi Database 实例。
// Phase 1 开放所有支持的数据库类型。
func engineTypeSupported(t EngineType) bool {
	switch t {
	case EngineMySQL, EngineMySQLAgent, EngineMariaDB, EnginePostgreSQL, EngineOracle, EngineGoldendb,
		EngineClickHouse, EngineSQLServer, EngineDuckDB, EngineDameng, EngineGaussDB, EngineOpenGauss,
		EngineKingbase, EngineHighgo, EngineOceanBase, EngineStarRocks, EngineTDengine, EngineTrino,
		EngineVastbase, EngineIris, EngineDiros, EngineSphinx, EngineSQLite,
		EngineMongoDB, EngineChroma, EngineQdrant, EngineMilvus, EngineIoTDB, EngineElasticsearch,
		EngineKafka, EngineRabbitMQ, EngineRocketMQ, EngineMQTT, EngineCustom:
		return true
	}
	return false
}

// EngineMeta 引擎元数据（标签 / 分组 / 默认端口 / 协议 / 是否支持事务 / 是否支持 DDL）
type EngineMeta struct {
	Type         EngineType `json:"type"`
	Label        string     `json:"label"`        // 中文显示名
	Short        string     `json:"short"`        // 短名（用于徽章）
	Category     string     `json:"category"`     // relational / document / vector / timeseries / search / mq / custom
	DefaultPort  int        `json:"defaultPort"`
	DefaultDB    string     `json:"defaultDb"`
	HasSQL       bool       `json:"hasSql"`
	HasSchema    bool       `json:"hasSchema"`
	SupportsDML  bool       `json:"supportsDml"`
	SupportsDDL  bool       `json:"supportsDdl"`
	HasTable     bool       `json:"hasTable"`
	HasCollection bool      `json:"hasCollection"` // mq / vector / mongodb
	Color        string     `json:"color"`         // 徽章主色
	Description  string     `json:"description"`
}

// engineMetas 引擎元数据表（前端 + 后端共用）
var engineMetas = map[EngineType]EngineMeta{
	EngineMySQL:      {"mysql", "MySQL", "MySQL", "relational", 3306, "", true, true, true, true, true, false, "#3b82f6", "开源 OLTP, 关系型事实标准"},
	EngineMySQLAgent: {"mysql_agent", "MySQL Agent", "MySQL", "relational", 3306, "", true, true, true, true, true, false, "#3b82f6", "MySQL 兼容 (代理/驱动适配)"},
	EngineMariaDB:    {"mariadb", "MariaDB", "Maria", "relational", 3306, "", true, true, true, true, true, false, "#a855f7", "MySQL 兼容分支, 开源"},
	EnginePostgreSQL: {"postgres", "PostgreSQL", "PG", "relational", 5432, "postgres", true, true, true, true, true, false, "#0ea5e9", "强类型 + JSONB + 高级索引"},
	EngineOracle:     {"oracle", "Oracle", "Oracle", "relational", 1521, "ORCL", true, true, true, true, true, false, "#ef4444", "商业关系型, PL/SQL"},
	EngineGoldendb:   {"goldendb", "GoldenDB", "Gold", "relational", 1888, "", true, true, true, true, true, false, "#f59e0b", "中兴分布式, MySQL 兼容"},
	EngineClickHouse: {"clickhouse", "ClickHouse", "CH", "relational", 9000, "default", true, true, true, true, true, false, "#facc15", "OLAP 列存, 极致分析性能"},
	EngineSQLServer:  {"sqlserver", "SQL Server", "MSSQL", "relational", 1433, "master", true, true, true, true, true, false, "#dc2626", "微软关系型, T-SQL"},
	EngineDuckDB:     {"duckdb", "DuckDB", "DuckDB", "relational", 0, "", true, true, true, true, true, false, "#fde047", "进程内 OLAP, 文件型"},
	EngineDameng:     {"dameng", "达梦 DM", "DM", "relational", 5236, "", true, true, true, true, true, false, "#7c3aed", "国产化关系型, 信创"},
	EngineGaussDB:    {"gaussdb", "GaussDB", "Gauss", "relational", 1888, "", true, true, true, true, true, false, "#10b981", "华为分布式, PostgreSQL 兼容"},
	EngineOpenGauss:  {"opengauss", "openGauss", "oGauss", "relational", 5432, "postgres", true, true, true, true, true, false, "#059669", "华为开源, PostgreSQL 兼容"},
	EngineKingbase:   {"kingbase", "KingbaseES", "King", "relational", 54321, "test", true, true, true, true, true, false, "#0891b2", "人大金仓, PG 兼容"},
	EngineHighgo:     {"highgo", "HighGo", "HG", "relational", 5866, "highgo", true, true, true, true, true, false, "#0d9488", "瀚高, PG 兼容"},
	EngineOceanBase:  {"oceanbase", "OceanBase", "OB", "relational", 2881, "oceanbase", true, true, true, true, true, false, "#0ea5e9", "蚂蚁分布式, MySQL/Oracle 兼容"},
	EngineStarRocks:  {"starrocks", "StarRocks", "SR", "relational", 9030, "", true, true, true, true, true, false, "#0f766e", "极速全场景 MPP"},
	EngineTDengine:   {"tdengine", "TDengine", "TD", "relational", 6041, "", true, true, true, true, true, false, "#dc2626", "时序数据库, 物联网专用"},
	EngineTrino:      {"trino", "Trino", "Trino", "relational", 8080, "", true, true, true, false, true, false, "#f97316", "分布式 SQL 查询引擎 (前 PrestoSQL)"},
	EngineVastbase:   {"vastbase", "Vastbase", "VB", "relational", 5432, "", true, true, true, true, true, false, "#9333ea", "海量数据, PG 兼容"},
	EngineIris:       {"iris", "InterSystems IRIS", "IRIS", "relational", 1972, "", true, true, true, true, true, false, "#e11d48", "多模型, 医疗/金融场景"},
	EngineDiros:      {"diros", "Diros", "Diros", "relational", 1888, "", true, true, true, true, true, false, "#9333ea", "国产化数据库"},
	EngineSphinx:     {"sphinx", "Sphinx", "Sphinx", "relational", 9306, "", true, false, true, true, true, false, "#a3a3a3", "全文检索引擎"},
	EngineSQLite:     {"sqlite", "SQLite", "SQLite", "relational", 0, "", true, true, true, true, true, false, "#52525b", "进程内数据库, 嵌入式"},
	EngineMongoDB:    {"mongodb", "MongoDB", "Mongo", "document", 27017, "admin", true, true, true, true, false, true, "#10b981", "文档型, JSON 原生"},
	EngineChroma:     {"chroma", "Chroma", "Chroma", "vector", 8000, "", false, true, false, false, false, true, "#a78bfa", "向量库, RAG 友好"},
	EngineQdrant:     {"qdrant", "Qdrant", "Qdrant", "vector", 6333, "", false, true, false, false, false, true, "#ef4444", "向量库, Rust 实现, 高性能"},
	EngineMilvus:     {"milvus", "Milvus", "Milvus", "vector", 19530, "", false, true, false, false, false, true, "#06b6d4", "向量库, 大规模 AI 检索"},
	EngineIoTDB:      {"iotdb", "IoTDB", "IoTDB", "timeseries", 6667, "root", true, true, true, true, true, false, "#f59e0b", "时序数据库, 物联网/工业"},
	EngineElasticsearch: {"elasticsearch", "Elasticsearch", "ES", "search", 9200, "", false, true, true, true, true, false, "#10b981", "分布式搜索, 文档型索引"},
	EngineKafka:      {"kafka", "Kafka", "Kafka", "mq", 9092, "", false, true, false, false, false, true, "#1f2937", "高吞吐日志流, 消息队列"},
	EngineRabbitMQ:   {"rabbitmq", "RabbitMQ", "Rabbit", "mq", 5672, "/", false, true, false, false, false, true, "#f97316", "AMQP 标准, 灵活路由"},
	EngineRocketMQ:   {"rocketmq", "RocketMQ", "Rocket", "mq", 9876, "", false, true, false, false, false, true, "#1d4ed8", "阿里开源, 金融级可靠"},
	EngineMQTT:       {"mqtt", "MQTT", "MQTT", "mq", 1883, "", false, true, false, false, false, true, "#8b5cf6", "IoT 消息协议事实标准"},
	EngineCustom:     {"custom", "Custom DSN", "Custom", "custom", 0, "", true, true, true, true, true, false, "#94a3b8", "透传 DSN 到 GoNavi 底座"},
}

// AllEngineMetas 返回全部引擎元数据(供 /api/dbmanager/engines 端点)
func AllEngineMetas() []EngineMeta {
	out := make([]EngineMeta, 0, len(engineMetas))
	for _, m := range engineMetas {
		out = append(out, m)
	}
	return out
}

// GetEngineMeta 查询引擎元数据
func GetEngineMeta(t EngineType) (EngineMeta, bool) {
	m, ok := engineMetas[t]
	return m, ok
}

// EngineTypeConfig 获取引擎类型的连接配置默认值（host/port/database/username/sslMode）。
// 用于前端新建连接时的初始值。
func EngineTypeConfig(t EngineType) map[string]any {
	meta, ok := engineMetas[t]
	if !ok {
		return map[string]any{
			"host": "", "port": 0, "database": "", "username": "", "sslMode": "disable", "envTag": "",
		}
	}
	c := map[string]any{
		"host":     "",
		"port":     meta.DefaultPort,
		"database": meta.DefaultDB,
		"username": "",
		"sslMode":  "disable",
		"envTag":   "",
	}
	switch meta.Category {
	case "relational", "document":
		c["sslMode"] = "preferred"
	case "vector", "search":
		c["sslMode"] = "disable"
	case "mq":
		c["sslMode"] = "disable"
	}
	return c
}

// GetEngineDefaultConfig 兼容历史 wrapper: 返回 ConnectionConfig 形态, 由 handlers.go 重新定义为 ConnectionConfig 类型。
// 此处不重复定义, 见 handlers.go:227

// MaxQueryRows 查询结果最大行数, 防止大表炸内存。
const MaxQueryRows = 5000

// SSHConfig 简化 SSH 隧道配置 (与 GoNavi 底座对齐)
type SSHConfig struct {
	Enabled  bool   `json:"enabled"`
	Host     string `json:"host"`
	Port     int    `json:"port"`
	User     string `json:"user"`
	AuthMode string `json:"authMode"` // password / privateKey
	Password string `json:"password,omitempty"`
	KeyPath  string `json:"keyPath,omitempty"`
}

// SSLConfig SSL/TLS 完整配置 (与 GoNavi 底座对齐)
type SSLConfig struct {
	Mode      string `json:"mode"`      // disable / preferred / required / verify-ca / verify-full / skip-verify
	CACert    string `json:"caCert,omitempty"`
	Cert      string `json:"cert,omitempty"`
	Key       string `json:"key,omitempty"`
	ServerName string `json:"serverName,omitempty"`
}

// ProxyConfig HTTP/SOCKS 代理
type ProxyConfig struct {
	Enabled bool   `json:"enabled"`
	Type    string `json:"type"` // http / socks5
	Host    string `json:"host"`
	Port    int    `json:"port"`
	User    string `json:"user,omitempty"`
	Pass    string `json:"pass,omitempty"`
}

// ConnectionConfig 连接配置（不含密码，密码由 Connection.Password 单独存储）。
// v1: host/port/database/username/sslMode/envTag
// v2: + useSSH/ssh, useProxy/proxy, ssl{cert,key,ca,serverName}, timeout/queryTimeout/maxRows,
//     extraParams(dsn 连接参数), label/group
// 历史 v1 字段保留(JSON 向后兼容)。
type ConnectionConfig struct {
	Host     string            `json:"host"`
	Port     int               `json:"port"`
	Database string            `json:"database"`
	Username string            `json:"username"`
	SSLMode  string            `json:"sslMode"`
	EnvTag   string            `json:"envTag"` // dev / staging / prod (空=未标记, 按名称启发式)
	Options  map[string]string `json:"options,omitempty"`

	// v2 高级选项
	UseSSH        bool       `json:"useSSH,omitempty"`
	SSH           SSHConfig  `json:"ssh,omitempty"`
	UseProxy      bool       `json:"useProxy,omitempty"`
	Proxy         ProxyConfig `json:"proxy,omitempty"`
	SSL           SSLConfig  `json:"ssl,omitempty"`
	TimeoutSec    int        `json:"timeoutSec,omitempty"`    // 连接超时, 默认 15
	QueryTimeoutSec int      `json:"queryTimeoutSec,omitempty"` // 语句超时, 默认 30
	MaxRows       int        `json:"maxRows,omitempty"`        // 单次结果最大行数, 默认 5000
	ExtraParams   string     `json:"extraParams,omitempty"`   // 透传 DSN 额外参数
	Driver        string     `json:"driver,omitempty"`        // custom 时指定 GoNavi driver
	DSN           string     `json:"dsn,omitempty"`           // custom 完整 DSN

	// 展示
	Group string `json:"group,omitempty"` // 手动分组标签
	Icon  string `json:"icon,omitempty"`  // emoji 或字母
	Note  string `json:"note,omitempty"`

	// 引擎特参 (按引擎类型选择性使用)
	MongoReplicaSet    string `json:"mongoReplicaSet,omitempty"`    // MongoDB replica set 名称
	MongoAuthSource    string `json:"mongoAuthSource,omitempty"`    // MongoDB authSource (默认 admin)
	MongoReadPreference string `json:"mongoReadPreference,omitempty"` // MongoDB readPreference (primary/secondary/nearest)
	MongoSRV           bool   `json:"mongoSrv,omitempty"`            // MongoDB use mongodb+srv URI
	ClickHouseProtocol string `json:"clickHouseProtocol,omitempty"` // ClickHouse protocol (auto/http/native)
	OceanBaseProtocol  string `json:"oceanBaseProtocol,omitempty"`  // OceanBase protocol (mysql/oracle)
	Topology           string `json:"topology,omitempty"`           // topology: single/replica/cluster/sentinel
	Hosts              string `json:"hosts,omitempty"`              // multi-host addresses (host:port,host:port)
}

// ToGonaviConfig 转换为 GoNavi 底座的连接配置。
// QueryTimeout 与 Timeout 默认值由 service 层兜底。
func (c ConnectionConfig) ToGonaviConfig(password string) gonaviConnection.ConnectionConfig {
	timeout := c.TimeoutSec
	if timeout <= 0 {
		timeout = 15
	}
	queryTimeout := c.QueryTimeoutSec
	if queryTimeout <= 0 {
		queryTimeout = 30
	}
	cfg := gonaviConnection.ConnectionConfig{
		Host:         c.Host,
		Port:         c.Port,
		Database:     c.Database,
		User:         c.Username,
		Password:     password,
		Timeout:      timeout,
		QueryTimeout: queryTimeout,
		UseSSH:       c.UseSSH,
		SSH: gonaviConnection.SSHConfig{
			Host: c.SSH.Host,
			Port: c.SSH.Port,
			User: c.SSH.User,
		},
		UseProxy: c.UseProxy,
		Proxy: gonaviConnection.ProxyConfig{
			Type: c.Proxy.Type,
			Host: c.Proxy.Host,
			Port: c.Proxy.Port,
			User: c.Proxy.User,
		},
		Driver:           c.Driver,
		DSN:              c.DSN,
		ConnectionParams: c.ExtraParams,
	}
	// SSL
	switch c.SSLMode {
	case "true", "required", "skip-verify", "verify-ca", "verify-full":
		cfg.UseSSL = true
		cfg.SSLMode = c.SSLMode
		if c.SSLMode == "true" {
			cfg.SSLMode = "required"
		}
	case "preferred":
		cfg.UseSSL = true
		cfg.SSLMode = "preferred"
	}
	if c.SSL.Mode != "" {
		cfg.SSLMode = c.SSL.Mode
		cfg.UseSSL = cfg.SSLMode != "disable"
	}
	if c.SSL.CACert != "" {
		cfg.SSLCAPath = c.SSL.CACert
	}
	if c.SSL.Cert != "" {
		cfg.SSLCertPath = c.SSL.Cert
	}
	if c.SSL.Key != "" {
		cfg.SSLKeyPath = c.SSL.Key
	}

	// 引擎特参映射
	if c.MongoReplicaSet != "" {
		cfg.ReplicaSet = c.MongoReplicaSet
	}
	if c.MongoAuthSource != "" {
		cfg.AuthSource = c.MongoAuthSource
	}
	if c.MongoReadPreference != "" {
		cfg.ReadPreference = c.MongoReadPreference
	}
	cfg.MongoSRV = c.MongoSRV
	if c.ClickHouseProtocol != "" {
		cfg.ClickHouseProtocol = c.ClickHouseProtocol
	}
	if c.OceanBaseProtocol != "" {
		cfg.OceanBaseProtocol = c.OceanBaseProtocol
	}
	if c.Topology != "" {
		cfg.Topology = c.Topology
	}
	if c.Hosts != "" {
		cfg.Hosts = strings.Split(c.Hosts, ",")
		for i := range cfg.Hosts {
			cfg.Hosts[i] = strings.TrimSpace(cfg.Hosts[i])
		}
	}

	return cfg
}

// EffectiveMaxRows 返回单次结果最大行数
func (c ConnectionConfig) EffectiveMaxRows() int {
	if c.MaxRows > 0 {
		return c.MaxRows
	}
	return MaxQueryRows
}

// ConnectionInfo 暴露给前端的连接元数据（不含密码）。
type ConnectionInfo struct {
	ID        string           `json:"id"`
	Name      string           `json:"name"`
	Engine    EngineType       `json:"engine"`
	Config    ConnectionConfig `json:"config"`
	CreatedAt int64            `json:"createdAt"`
	UpdatedAt int64            `json:"updatedAt"`
}

// Connection 运行时连接（带密码）。
type Connection struct {
	Info     ConnectionInfo
	Password string
}

// TableInfo 表/视图元数据。
type TableInfo struct {
	Name    string `json:"name"`
	Type    string `json:"type"` // BASE TABLE / VIEW / ...
	Schema  string `json:"schema,omitempty"`
	Comment string `json:"comment,omitempty"`
}

// ColumnInfo 列元数据。
type ColumnInfo struct {
	Name     string `json:"name"`
	Type     string `json:"type"`
	Nullable bool   `json:"nullable"`
	Key      string `json:"key,omitempty"` // PRI / UNI / MUL
	Default  string `json:"default,omitempty"`
	Comment  string `json:"comment,omitempty"`
}

// IndexInfo 索引元数据（聚合后的形态）。
type IndexInfo struct {
	Name    string   `json:"name"`
	Columns []string `json:"columns"`
	Unique  bool     `json:"unique"`
	Primary bool     `json:"primary"`
}

// QueryResult 查询结果。
type QueryResult struct {
	Columns    []string          `json:"columns"`
	Rows       [][]any           `json:"rows"`
	RowCount   int               `json:"rowCount"`
	Affected   int64             `json:"affected"`
	DurationMs int64             `json:"durationMs"`
	Truncated  bool              `json:"truncated"`
	Error      string            `json:"error,omitempty"`
	Statements []StatementResult `json:"statements,omitempty"` // 多语句执行摘要
}

// SavedQuery 用户保存的查询语句。
type SavedQuery struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	SQL       string `json:"sql"`
	Engine    string `json:"engine,omitempty"`     // 关联引擎类型 (空=通用)
	ConnID    string `json:"connId,omitempty"`     // 关联连接 ID (空=任意连接)
	CreatedAt int64  `json:"createdAt"`
	UpdatedAt int64  `json:"updatedAt"`
}
