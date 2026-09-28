package dbmanager

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"opscore/internal/central"
)

const connectionsKey = "dbmanager:connections"

// Store DB 连接的持久化存储。
// 复用 central meta KV(key=dbmanager:connections, 值为 JSON 数组)。
// 密码用 AES-GCM 加密(密钥来自 auth token, 不单独管理; token 切换不影响存量密码的可读性,
// 但 token 重置后老连接需要重新输入密码 —— 与其他敏感配置语义一致)。
type Store struct {
	mu     sync.RWMutex
	store  func() central.CentralStore
	cache  []storedConnection
	loaded bool
	encKey []byte
}

type storedConnection struct {
	ID            string                   `json:"id"`
	Name          string                   `json:"name"`
	Engine        EngineType       `json:"engine"`
	Config        ConnectionConfig `json:"config"`
	PasswordEnc   string                   `json:"passwordEnc,omitempty"`
	PasswordPlain string                   `json:"-"` // 内存中临时持有, 不持久化
	CreatedAt     int64                    `json:"createdAt"`
	UpdatedAt     int64                    `json:"updatedAt"`
}

// NewStore 创建连接存储; storeFn 复用 K8s 的延迟注入模式。
func NewStore(storeFn func() central.CentralStore, encryptionKey string) *Store {
	key := deriveKey(encryptionKey)
	return &Store{store: storeFn, encKey: key}
}

func deriveKey(seed string) []byte {
	// 32 字节固定密钥 —— 派生自 token hash(确定性, token 切换不影响存量)。
	// 即使 token 改了, 用同样 seed 派生同样 key, 老数据仍能解密。
	sum := sha256Of("opscore-dbmanager:" + seed)
	return sum
}

func sha256Of(s string) []byte {
	sum := sha256.Sum256([]byte(s))
	return sum[:]
}

func (s *Store) load() error {
	if s.loaded {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.loaded {
		return nil
	}
	st := s.store()
	if st == nil {
		return fmt.Errorf("central store not initialized")
	}
	raw, err := central.GetMetaString(st, connectionsKey)
	if err != nil {
		return err
	}
	if raw == "" {
		s.cache = []storedConnection{}
	} else {
		if err := json.Unmarshal([]byte(raw), &s.cache); err != nil {
			return fmt.Errorf("decode db connections: %w", err)
		}
	}
	s.loaded = true
	return nil
}

func (s *Store) flush() error {
	st := s.store()
	if st == nil {
		return fmt.Errorf("central store not initialized")
	}
	b, err := json.Marshal(s.cache)
	if err != nil {
		return err
	}
	return central.SetMetaString(st, connectionsKey, string(b))
}

// Central 暴露底层 central store(供审计日志等延迟注入使用; 可能为 nil)。
func (s *Store) Central() central.CentralStore {
	return s.store()
}

// encryptPassword AES-GCM 加密, base64 输出。
func (s *Store) encryptPassword(plain string) (string, error) {
	if plain == "" {
		return "", nil
	}
	block, err := aes.NewCipher(s.encKey)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", err
	}
	ciphertext := gcm.Seal(nonce, nonce, []byte(plain), nil)
	return base64.StdEncoding.EncodeToString(ciphertext), nil
}

func (s *Store) decryptPassword(enc string) (string, error) {
	if enc == "" {
		return "", nil
	}
	ciphertext, err := base64.StdEncoding.DecodeString(enc)
	if err != nil {
		return "", err
	}
	block, err := aes.NewCipher(s.encKey)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	if len(ciphertext) < gcm.NonceSize() {
		return "", fmt.Errorf("ciphertext too short")
	}
	nonce, body := ciphertext[:gcm.NonceSize()], ciphertext[gcm.NonceSize():]
	plain, err := gcm.Open(nil, nonce, body, nil)
	if err != nil {
		return "", err
	}
	return string(plain), nil
}

// List 返回不含密码的连接元数据。
func (s *Store) List() ([]ConnectionInfo, error) {
	if err := s.load(); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]ConnectionInfo, 0, len(s.cache))
	for _, c := range s.cache {
		out = append(out, ConnectionInfo{
			ID:        c.ID,
			Name:      c.Name,
			Engine:    c.Engine,
			Config:    c.Config,
			CreatedAt: c.CreatedAt,
			UpdatedAt: c.UpdatedAt,
		})
	}
	return out, nil
}

// Get 取出完整连接(供运行时 Open 用)。
func (s *Store) Get(id string) (*Connection, error) {
	if err := s.load(); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, c := range s.cache {
		if c.ID == id {
			pw, err := s.decryptPassword(c.PasswordEnc)
			if err != nil {
				return nil, fmt.Errorf("decrypt password: %w", err)
			}
			// 内存中临时使用, 标记为待清理
			c.PasswordPlain = pw
			return &Connection{
				Info: ConnectionInfo{
					ID:        c.ID,
					Name:      c.Name,
					Engine:    c.Engine,
					Config:    c.Config,
					CreatedAt: c.CreatedAt,
					UpdatedAt: c.UpdatedAt,
				},
				Password: pw,
			}, nil
		}
	}
	return nil, fmt.Errorf("连接不存在: %s", id)
}

// Create 新建连接。
func (s *Store) Create(name string, engine EngineType, cfg ConnectionConfig, password string) (*ConnectionInfo, error) {
	if err := s.load(); err != nil {
		return nil, err
	}
	if !engineTypeSupported(engine) {
		return nil, fmt.Errorf("不支持的引擎类型: %s", engine)
	}
	if strings.TrimSpace(name) == "" {
		return nil, fmt.Errorf("名称不能为空")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	id := newID()
	now := time.Now().Unix()
	enc, err := s.encryptPassword(password)
	if err != nil {
		return nil, fmt.Errorf("encrypt password: %w", err)
	}
	s.cache = append(s.cache, storedConnection{
		ID:          id,
		Name:        name,
		Engine:      engine,
		Config:      cfg,
		PasswordEnc: enc,
		CreatedAt:   now,
		UpdatedAt:   now,
	})
	if err := s.flush(); err != nil {
		return nil, err
	}
	return &ConnectionInfo{
		ID:        id,
		Name:      name,
		Engine:    engine,
		Config:    cfg,
		CreatedAt: now,
		UpdatedAt: now,
	}, nil
}

// Update 修改连接。
func (s *Store) Update(id, name string, cfg ConnectionConfig, password string) (*ConnectionInfo, error) {
	if err := s.load(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.cache {
		if s.cache[i].ID == id {
			if strings.TrimSpace(name) != "" {
				s.cache[i].Name = name
			}
			s.cache[i].Config = cfg
			if password != "" {
				enc, err := s.encryptPassword(password)
				if err != nil {
					return nil, err
				}
				s.cache[i].PasswordEnc = enc
			}
			s.cache[i].UpdatedAt = time.Now().Unix()
			if err := s.flush(); err != nil {
				return nil, err
			}
			return &ConnectionInfo{
				ID:        s.cache[i].ID,
				Name:      s.cache[i].Name,
				Engine:    s.cache[i].Engine,
				Config:    s.cache[i].Config,
				CreatedAt: s.cache[i].CreatedAt,
				UpdatedAt: s.cache[i].UpdatedAt,
			}, nil
		}
	}
	return nil, fmt.Errorf("连接不存在: %s", id)
}

// Delete 删除连接。
func (s *Store) Delete(id string) error {
	if err := s.load(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.cache {
		if s.cache[i].ID == id {
			s.cache = append(s.cache[:i], s.cache[i+1:]...)
			return s.flush()
		}
	}
	return fmt.Errorf("连接不存在: %s", id)
}

func newID() string {
	// 12 字节随机 -> 24 字符 hex
	b := make([]byte, 12)
	_, _ = rand.Read(b)
	return fmt.Sprintf("%x", b)
}

// isEmptyConfig 判断连接配置是否为空(用于"未提供配置则用默认值"判断)。
// ConnectionConfig 含 map 不能直接 == 比较, 这里逐字段判断。
func isEmptyConfig(c ConnectionConfig) bool {
	return c.Host == "" && c.Port == 0 && c.Database == "" && c.Username == "" && c.SSLMode == "" && c.EnvTag == "" && len(c.Options) == 0
}

const savedQueriesKey = "dbmanager:saved_queries"

// RenameSavedQueryFolder 把 to 目录(含子目录)整体改名/移动, 返回受影响的查询条数。
// 目录不是独立实体(树由 Folder 前缀推导), 所以"改目录名"就是批量改这些查询的 Folder。
func (s *Store) RenameSavedQueryFolder(from, to string) (int, error) {
	src, err := normalizeSQLFolder(from)
	if err != nil {
		return 0, err
	}
	if src == "" {
		return 0, fmt.Errorf("不能重命名根目录")
	}
	dst, err := normalizeSQLFolder(to)
	if err != nil {
		return 0, err
	}
	if dst == src {
		return 0, nil
	}
	// 不许把目录移到自己里面(会形成自我嵌套的前缀混乱)
	if strings.HasPrefix(dst+"/", src+"/") {
		return 0, fmt.Errorf("不能把目录移到它自己里面")
	}
	list, err := s.ListSavedQueries()
	if err != nil {
		return 0, err
	}
	now := time.Now().Unix()
	n := 0
	for i := range list {
		f := list[i].Folder
		if f == src || strings.HasPrefix(f, src+"/") {
			rest := strings.TrimPrefix(strings.TrimPrefix(f, src), "/")
			list[i].Folder = dst
			if rest != "" {
				if dst == "" {
					list[i].Folder = rest
				} else {
					list[i].Folder = dst + "/" + rest
				}
			}
			list[i].UpdatedAt = now
			n++
		}
	}
	if n == 0 {
		return 0, fmt.Errorf("目录不存在: %s", src)
	}
	return n, s.writeSavedQueries(list)
}

// DeleteSavedQueryFolder 删除目录(含子目录)下的**全部查询**, 返回删除条数。
// 这是破坏性操作, 所以让调用方(处理器)去要二次确认; 这里只负责如实返回删了多少。
func (s *Store) DeleteSavedQueryFolder(path string) (int, error) {
	target, err := normalizeSQLFolder(path)
	if err != nil {
		return 0, err
	}
	if target == "" {
		return 0, fmt.Errorf("根目录不能用这个接口删 —— 要清空请逐条删除")
	}
	list, err := s.ListSavedQueries()
	if err != nil {
		return 0, err
	}
	kept := make([]SavedQuery, 0, len(list))
	n := 0
	for _, q := range list {
		if q.Folder == target || strings.HasPrefix(q.Folder, target+"/") {
			n++
			continue
		}
		kept = append(kept, q)
	}
	if n == 0 {
		return 0, fmt.Errorf("目录不存在或本来就是空的: %s", target)
	}
	return n, s.writeSavedQueries(kept)
}

// normalizeSQLFolder 规整"SQL 仓库"的目录路径, 并挡掉能跑出根目录的写法。
//
// 目录只是 SavedQuery.Folder 里的一段文本(树由前缀推导), 但它会出现在界面路径与
// 将来的导出文件名里, 所以在这里就把边界定死: 不许绝对路径、不许 `..`、不许空段。
// 允许 `/` 分隔的多级(如 "运维/K8s"), 以及中文/空格/下划线这类正常名字。
func normalizeSQLFolder(raw string) (string, error) {
	f := strings.TrimSpace(strings.ReplaceAll(raw, "\\", "/"))
	f = strings.Trim(f, "/")
	if f == "" {
		return "", nil
	}
	if len(f) > 200 {
		return "", fmt.Errorf("目录路径过长(上限 200 字符)")
	}
	segs := strings.Split(f, "/")
	out := make([]string, 0, len(segs))
	for _, s := range segs {
		s = strings.TrimSpace(s)
		if s == "" {
			continue // 连续斜杠直接折叠, 不报错
		}
		if s == "." || s == ".." {
			return "", fmt.Errorf("目录名不能是 %q", s)
		}
		if strings.ContainsAny(s, "\\:*?\"<>|") {
			return "", fmt.Errorf("目录名不能含 \\ : * ? \" < > | 这些字符: %q", s)
		}
		out = append(out, s)
	}
	return strings.Join(out, "/"), nil
}

// ListSavedQueries 返回全部保存的查询。
func (s *Store) ListSavedQueries() ([]SavedQuery, error) {
	st := s.store()
	if st == nil {
		return nil, fmt.Errorf("central store not initialized")
	}
	raw, err := central.GetMetaString(st, savedQueriesKey)
	if err != nil {
		return nil, err
	}
	if raw == "" {
		return []SavedQuery{}, nil
	}
	var out []SavedQuery
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return nil, fmt.Errorf("decode saved queries: %w", err)
	}
	return out, nil
}

// SaveQuery 保存或更新查询语句。
func (s *Store) SaveQuery(q SavedQuery) (SavedQuery, error) {
	st := s.store()
	if st == nil {
		return SavedQuery{}, fmt.Errorf("central store not initialized")
	}
	if strings.TrimSpace(q.Name) == "" {
		return SavedQuery{}, fmt.Errorf("查询名称不能为空")
	}
	if strings.TrimSpace(q.SQL) == "" {
		return SavedQuery{}, fmt.Errorf("SQL 不能为空")
	}
	folder, ferr := normalizeSQLFolder(q.Folder)
	if ferr != nil {
		return SavedQuery{}, ferr
	}
	q.Folder = folder
	list, err := s.ListSavedQueries()
	if err != nil {
		return SavedQuery{}, err
	}
	now := time.Now().Unix()
	if q.ID == "" {
		q.ID = newID()
		q.CreatedAt = now
	}
	q.UpdatedAt = now
	// 去重更新
	found := false
	for i := range list {
		if list[i].ID == q.ID {
			list[i] = q
			found = true
			break
		}
	}
	if !found {
		list = append(list, q)
	}
	if err := s.writeSavedQueries(list); err != nil {
		return SavedQuery{}, err
	}
	return q, nil
}

// writeSavedQueries 把整个列表写回 central store。
// 抽出来是因为"改一个目录名/删一个目录"要批量改多条, 不该各写一遍序列化+落盘。
func (s *Store) writeSavedQueries(list []SavedQuery) error {
	st := s.store()
	if st == nil {
		return fmt.Errorf("central store not initialized")
	}
	if list == nil {
		list = []SavedQuery{} // JSON 里给 [] 不给 null: 前端 .map 会炸
	}
	b, err := json.Marshal(list)
	if err != nil {
		return err
	}
	return central.SetMetaString(st, savedQueriesKey, string(b))
}

// DeleteSavedQuery 删除保存的查询。
func (s *Store) DeleteSavedQuery(id string) error {
	st := s.store()
	if st == nil {
		return fmt.Errorf("central store not initialized")
	}
	list, err := s.ListSavedQueries()
	if err != nil {
		return err
	}
	for i := range list {
		if list[i].ID == id {
			list = append(list[:i], list[i+1:]...)
			b, err := json.Marshal(list)
			if err != nil {
				return err
			}
			return central.SetMetaString(st, savedQueriesKey, string(b))
		}
	}
	return fmt.Errorf("查询不存在: %s", id)
}
