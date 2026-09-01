// ConnectionPool 维护按连接 ID 缓存的 GoNavi Database 实例（实现底座，ADR-001 分层的 drivers 层）。
// 简单 LRU 淘汰(超过 poolSize 后关闭最久未用)；Database 实例自带底层驱动连接池。
package dbmanager

import (
	"fmt"
	"sync"
	"time"

	gonavibase "opscore/internal/dbmanager/gonavi/db"
)

type pooledDatabase struct {
	db       gonavibase.Database
	lastUsed time.Time
	conn     *Connection
}

// DatabasePool 按 connID 缓存已连接的 GoNavi Database 实例。
type DatabasePool struct {
	mu       sync.Mutex
	conns    map[string]*pooledDatabase
	order    []string
	poolSize int
	store    *Store
}

func NewDatabasePool(store *Store, poolSize int) *DatabasePool {
	if poolSize <= 0 {
		poolSize = 8
	}
	return &DatabasePool{
		conns:    make(map[string]*pooledDatabase),
		poolSize: poolSize,
		store:    store,
	}
}

// Acquire 获取(或建立)指定连接的 Database 实例。
func (p *DatabasePool) Acquire(connID string) (gonavibase.Database, *Connection, error) {
	p.mu.Lock()
	if pc, ok := p.conns[connID]; ok {
		pc.lastUsed = time.Now()
		p.touchOrder(connID)
		p.mu.Unlock()
		return pc.db, pc.conn, nil
	}
	p.mu.Unlock()

	conn, err := p.store.Get(connID)
	if err != nil {
		return nil, nil, err
	}
	db, err := openGonaviDatabase(conn)
	if err != nil {
		return nil, nil, err
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	// 双重检查(并发建连时丢弃多开的实例)
	if pc, ok := p.conns[connID]; ok {
		_ = db.Close()
		pc.lastUsed = time.Now()
		return pc.db, pc.conn, nil
	}
	// LRU 淘汰
	for len(p.order) >= p.poolSize {
		old := p.order[0]
		p.order = p.order[1:]
		if oldPC, ok := p.conns[old]; ok {
			_ = oldPC.db.Close()
			delete(p.conns, old)
		}
	}
	p.conns[connID] = &pooledDatabase{db: db, lastUsed: time.Now(), conn: conn}
	p.order = append(p.order, connID)
	return db, conn, nil
}

// openGonaviDatabase 通过 GoNavi 工厂创建并连接 Database 实例。
func openGonaviDatabase(conn *Connection) (gonavibase.Database, error) {
	db, err := gonavibase.NewDatabase(string(conn.Info.Engine))
	if err != nil {
		return nil, fmt.Errorf("创建引擎实例失败: %w", err)
	}
	// GoNavi Connect 无 ctx 参数, 超时由 config.Timeout(秒)控制(types.go 已设 15s)
	if err := db.Connect(conn.Info.Config.ToGonaviConfig(conn.Password)); err != nil {
		_ = db.Close()
		return nil, err
	}
	return db, nil
}

// AcquireForSync 供 sync 包使用: 返回缓存的 Database 实例与引擎类型。
func (p *DatabasePool) AcquireForSync(connID string) (gonavibase.Database, string, error) {
	db, conn, err := p.Acquire(connID)
	if err != nil {
		return nil, "", err
	}
	return db, string(conn.Info.Engine), nil
}

func (p *DatabasePool) touchOrder(id string) {
	for i, x := range p.order {
		if x == id {
			p.order = append(p.order[:i], p.order[i+1:]...)
			p.order = append(p.order, id)
			return
		}
	}
}

// Release 主动关闭一个连接(删除/编辑后清理)。
func (p *DatabasePool) Release(connID string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if pc, ok := p.conns[connID]; ok {
		_ = pc.db.Close()
		delete(p.conns, connID)
	}
	for i, x := range p.order {
		if x == connID {
			p.order = append(p.order[:i], p.order[i+1:]...)
			return
		}
	}
}

// Close 关闭池中所有连接。
func (p *DatabasePool) Close() {
	p.mu.Lock()
	defer p.mu.Unlock()
	for id, pc := range p.conns {
		_ = pc.db.Close()
		delete(p.conns, id)
	}
	p.order = nil
}
