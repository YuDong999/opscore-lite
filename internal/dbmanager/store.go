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
