// Package hostkey 提供 SSH 主机公钥的"首次信任"(TOFU)校验存储:
// 首次连接记录指纹, 之后指纹变化即拒绝连接(防中间人攻击)。
package hostkey

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"os"
	"path/filepath"
	"sync"

	"golang.org/x/crypto/ssh"
)

var (
	mu     sync.Mutex
	keys   map[string]string // "host:port" -> fingerprint
	path   string            // known_hosts.json 路径
	loaded bool
)

// SetDataDir 设置存储目录, 须在首次 SSH 连接前调用(通常在 main 里)。
func SetDataDir(dir string) {
	mu.Lock()
	defer mu.Unlock()
	if dir == "" {
		dir = "./data"
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		log.Printf("[hostkey] mkdir %s: %v", dir, err)
		return
	}
	path = filepath.Join(dir, "known_hosts.json")
}

func loadLocked() {
	if loaded || path == "" {
		keys = map[string]string{}
		loaded = true
		return
	}
	keys = map[string]string{}
	b, err := os.ReadFile(path)
	if err == nil {
		_ = json.Unmarshal(b, &keys)
	}
	loaded = true
}

func saveLocked() {
	if path == "" {
		return
	}
	b, err := json.MarshalIndent(keys, "", "  ")
	if err != nil {
		return
	}
	if err := os.WriteFile(path, b, 0o600); err != nil {
		log.Printf("[hostkey] save: %v", err)
	}
}

func fingerprint(key ssh.PublicKey) string {
	sum := sha256.Sum256(key.Marshal())
	return base64.StdEncoding.EncodeToString(sum[:])
}

// Callback 实现 ssh.HostKeyCallback(TOFU): 首次记录, 变更即拒绝。
func Callback(host string, _ net.Addr, key ssh.PublicKey) error {
	fp := fingerprint(key)
	id := host
	mu.Lock()
	defer mu.Unlock()
	loadLocked()
	if old, ok := keys[id]; ok {
		if old != fp {
			return fmt.Errorf("主机 %s 的密钥指纹已变更(旧 %s 新 %s), 拒绝连接; 若确认是合法变更请删除 data/known_hosts.json 中对应条目", id, old, fp)
		}
		return nil
	}
	keys[id] = fp
	saveLocked()
	log.Printf("[hostkey] 首次信任主机 %s 指纹 %s", id, fp[:16]+"...")
	return nil
}
