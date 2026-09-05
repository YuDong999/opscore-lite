// Package sync 跨库同步: Schema 迁移 + 类型映射 + 全量/增量数据搬迁。
//
// 设计(ADR-005):
//   - 方言驱动: 每个源/目标方言组合(dialect pair)对应一份类型映射表 + DDL 生成器 + 值转义规则,
//     即"不同的源端库和不同的目标库使用不同的脚本"。新增引擎对只需注册新的 dialect。
//   - 模式组合: schema_only / schema_full / schema_full_incr / truncate_full / incr_only / verify
//   - 增量策略(Phase 1, 无 binlog CDC): 自增整数主键水位 或 时间戳列水位。
//   - 读端走 gonavi StreamQuery(流式, 省内存), 写端走 ExecContext + multi-row INSERT(内联转义)。
package sync

import (
	"context"
	"sync"
	"time"

	gonaviConnection "opscore/internal/dbmanager/gonavi/connection"
)

// SyncMode 同步模式组合。
type SyncMode string

const (
	// ModeSchemaOnly 仅迁移表结构(建表 DDL), 不搬数据。
	ModeSchemaOnly SyncMode = "schema_only"
	// ModeSchemaFull 同步表结构 + 全量数据。
	ModeSchemaFull SyncMode = "schema_full"
	// ModeSchemaFullIncr 同步表结构 + 全量数据 + 记录水位(供后续增量续传)。
	ModeSchemaFullIncr SyncMode = "schema_full_incr"
	// ModeTruncateFull 清空目标表(若存在)后全量同步(不迁移结构, 要求目标表存在或同任务先建)。
	ModeTruncateFull SyncMode = "truncate_full"
	// ModeIncrOnly 仅增量: 按目标表现有水位(自增主键 max 或时间戳列 max)拉取新增数据。
	ModeIncrOnly SyncMode = "incr_only"
	// ModeVerify 校验: 对比源/目标行数。
	ModeVerify SyncMode = "verify"
)

// Dialect 方言族。Phase 1: mysql 族(mysql/mariadb/goldendb/oceanbase-mysql) 与
// postgres 族(postgres/opengauss/kingbase/highgo/vastbase/gaussdb)。
type Dialect string

const (
	DialectMySQL    Dialect = "mysql"
	DialectPostgres Dialect = "postgres"
)

// IncrementalStrategy 增量水位类型。
type IncrementalStrategy string

const (
	IncrAutoIncrement IncrementalStrategy = "auto_increment" // 整数自增主键水位
	IncrTimestamp     IncrementalStrategy = "timestamp"      // 时间戳列水位
	IncrNone          IncrementalStrategy = "none"           // 无可用增量列
)

// SyncOptions 高级选项。
type SyncOptions struct {
	BatchRows   int    `json:"batchRows,omitempty"`   // 每批 INSERT 行数, 默认 500
	MaxRows     int64  `json:"maxRows,omitempty"`     // 单表全量上限, 默认 0=不限制
	Truncate    bool   `json:"truncate,omitempty"`    // schema_full 模式下是否先清空目标
	WhereClause string `json:"whereClause,omitempty"` // 全量过滤条件(不含 WHERE 关键字)
}

// SyncRequest 同步请求。
type SyncRequest struct {
	SourceID   string   `json:"sourceId"`            // 源连接 ID
	SourceDB   string   `json:"sourceDb"`            // 源库名
	TargetID   string   `json:"targetId"`            // 目标连接 ID
	TargetDB   string   `json:"targetDb"`            // 目标库名
	Tables     []string `json:"tables,omitempty"`    // 指定表列表, 空=源库全部 BASE TABLE
	TableMaps  []TableMap `json:"tableMaps,omitempty"` // 表映射(源→目标自定义名); 非空时优先于 Tables
	Mode       SyncMode `json:"mode"`
	IncrementalColumn string `json:"incrementalColumn,omitempty"` // 增量列(默认自动探测)
	Options    SyncOptions `json:"options,omitempty"`
}

// TableMap 表映射: 源表 → 目标自定义表名(目标不存在时 plan/run 自动建表)。
type TableMap struct {
	Source string `json:"source"`
	Target string `json:"target"`
}

// ColumnMapping 单列的类型映射结果(计划预览用)。
type ColumnMapping struct {
	Name       string `json:"name"`                 // 列名
	Source     string `json:"source"`               // 源列定义, 如 varchar(64) NOT NULL
	Target     string `json:"target"`               // 目标列定义
	Note       string `json:"note,omitempty"`       // 映射备注(如 enum 转换说明)
	IsPK       bool   `json:"isPk"`
	AutoIncr   bool   `json:"autoIncr,omitempty"`
	Nullable   bool   `json:"nullable"`
	Default    string `json:"default,omitempty"`
	Comment    string `json:"comment,omitempty"`
}

// TablePlan 单表迁移计划。
type TablePlan struct {
	Source       string                     `json:"source"`
	Target       string                     `json:"target"`
	CreateDDL    string                     `json:"createDdl,omitempty"`    // 目标建表语句
	Columns      []ColumnMapping            `json:"columns"`
	IndexDDL     []string                   `json:"indexDdl,omitempty"`     // 二级索引语句
	IncrStrategy IncrementalStrategy        `json:"incrStrategy"`
	IncrColumn   string                     `json:"incrColumn,omitempty"`
	SourcePK     string                     `json:"sourcePk,omitempty"`
	Notes        []string                   `json:"notes,omitempty"`
	Skipped      bool                       `json:"skipped,omitempty"`      // true=不可迁移(如无主键+增量)
	SkipReason   string                     `json:"skipReason,omitempty"`
}

// SyncPlan 完整迁移计划(plan 端点响应)。
type SyncPlan struct {
	SourceDialect Dialect     `json:"sourceDialect"`
	TargetDialect Dialect     `json:"targetDialect"`
	Mode          SyncMode    `json:"mode"`
	Tables        []TablePlan `json:"tables"`
	Unsupported   []string    `json:"unsupported,omitempty"` // 不支持同步的引擎对提示
}

// TableProgress 单表进度。
type TableProgress struct {
	Table       string `json:"table"`
	Status      string `json:"status"` // pending / creating / copying / done / failed / skipped
	RowsCopied  int64  `json:"rowsCopied"`
	Err         string `json:"err,omitempty"`
}

// Job 一个同步任务的运行时状态。
type Job struct {
	ID         string          `json:"id"`
	Request    SyncRequest     `json:"request"`
	Plan       *SyncPlan       `json:"plan,omitempty"`
	Status     string          `json:"status"` // running / done / failed / canceled
	StartedAt  time.Time       `json:"startedAt"`
	FinishedAt *time.Time      `json:"finishedAt,omitempty"`
	Tables     []TableProgress `json:"tables"`
	TotalRows  int64           `json:"totalRows"`
	Err        string          `json:"err,omitempty"`

	mu       sync.Mutex
	cancel   context.CancelFunc
}

func (j *Job) updateTable(table string, fn func(*TableProgress)) {
	j.mu.Lock()
	defer j.mu.Unlock()
	for i := range j.Tables {
		if j.Tables[i].Table == table {
			fn(&j.Tables[i])
			return
		}
	}
}

// tableColumns 缓存的单表源结构。
type tableSchema struct {
	columns []gonaviConnection.ColumnDefinition
	indexes []gonaviConnection.IndexDefinition
}
