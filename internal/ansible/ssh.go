package ansible

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
)

type SSHKeyPair struct {
	Name        string `json:"name"`
	Fingerprint string `json:"fingerprint"`
	PublicKey   string `json:"publicKey"`
	CreatedAt   string `json:"createdAt"`
}

type SSHDeployReq struct {
	KeyName  string `json:"keyName"`
	Host     string `json:"host"`
	Port     int    `json:"port"`
	User     string `json:"user"`
	Password string `json:"password"`
}

type SSHBindReq struct {
	KeyName string `json:"keyName"`
	HostID  string `json:"hostId"`
}

type SSHManager struct {
	mu       sync.Mutex
	sshDir   string
	keysFile string
	keys     []SSHKeyPair
}

func NewSSHManager(dataDir string) *SSHManager {
	sshDir := filepath.Join(dataDir, "ssh")
	os.MkdirAll(sshDir, 0700)
	m := &SSHManager{
		sshDir:   sshDir,
		keysFile: filepath.Join(dataDir, "ssh_keys.json"),
	}
	loadJSON(m.keysFile, &m.keys)
	if m.keys == nil {
		m.keys = []SSHKeyPair{}
	}
	return m
}

func (m *SSHManager) saveKeys() error {
	return saveJSON(m.keysFile, m.keys)
}

func (m *SSHManager) privKeyPath(name string) string {
	return filepath.Join(m.sshDir, name)
}

func (m *SSHManager) ListKeys() []SSHKeyPair {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]SSHKeyPair, len(m.keys))
	copy(out, m.keys)
	return out
}

func (m *SSHManager) GenerateKey(name string) (*SSHKeyPair, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	for _, k := range m.keys {
		if k.Name == name {
			return nil, fmt.Errorf("密钥名 '%s' 已存在", name)
		}
	}

	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("生成密钥对失败: %w", err)
	}

	sshPub, err := ssh.NewPublicKey(pub)
	if err != nil {
		return nil, fmt.Errorf("转换公钥失败: %w", err)
	}
	pubBytes := ssh.MarshalAuthorizedKey(sshPub)
	fingerprint := ssh.FingerprintSHA256(sshPub)

	privPKCS8, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		return nil, fmt.Errorf("编码私钥失败: %w", err)
	}
	privPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: privPKCS8})

	keyPath := m.privKeyPath(name)
	if err := os.WriteFile(keyPath, privPEM, 0600); err != nil {
		return nil, fmt.Errorf("写入私钥文件失败: %w", err)
	}

	pair := SSHKeyPair{
		Name:        name,
		Fingerprint: fingerprint,
		PublicKey:   strings.TrimSpace(string(pubBytes)),
		CreatedAt:   time.Now().Format("2006-01-02 15:04:05"),
	}
	m.keys = append(m.keys, pair)
	if err := m.saveKeys(); err != nil {
		return nil, err
	}
	return &pair, nil
}

func (m *SSHManager) DeleteKey(name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	idx := -1
	for i, k := range m.keys {
		if k.Name == name {
			idx = i
			break
		}
	}
	if idx == -1 {
		return fmt.Errorf("密钥 '%s' 不存在", name)
	}

	os.Remove(m.privKeyPath(name))
	m.keys = append(m.keys[:idx], m.keys[idx+1:]...)
	return m.saveKeys()
}

func (m *SSHManager) DeployKey(req SSHDeployReq) error {
	if req.Port == 0 {
		req.Port = 22
	}
	if req.User == "" {
		req.User = "root"
	}

	m.mu.Lock()
	var pair *SSHKeyPair
	for i := range m.keys {
		if m.keys[i].Name == req.KeyName {
			pair = &m.keys[i]
			break
		}
	}
	m.mu.Unlock()
	if pair == nil {
		return fmt.Errorf("密钥 '%s' 不存在", req.KeyName)
	}

	config := &ssh.ClientConfig{
		User:            req.User,
		Auth:            []ssh.AuthMethod{ssh.Password(req.Password)},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         10 * time.Second,
	}

	addr := net.JoinHostPort(req.Host, fmt.Sprintf("%d", req.Port))
	client, err := ssh.Dial("tcp", addr, config)
	if err != nil {
		return fmt.Errorf("SSH 连接失败: %w", err)
	}
	defer client.Close()

	mkdirCmd := fmt.Sprintf("mkdir -p ~/.ssh && chmod 700 ~/.ssh")
	if err := runSSHCommand(client, mkdirCmd); err != nil {
		return fmt.Errorf("创建 ~/.ssh 目录失败: %w", err)
	}

	appendCmd := fmt.Sprintf("echo '%s' >> ~/.ssh/authorized_keys && chmod 600 ~/.ssh/authorized_keys", pair.PublicKey)
	if err := runSSHCommand(client, appendCmd); err != nil {
		return fmt.Errorf("写入公钥失败: %w", err)
	}

	return nil
}

func (m *SSHManager) TestConnection(keyName, host, user string, port int) error {
	if port == 0 {
		port = 22
	}
	if user == "" {
		user = "root"
	}

	m.mu.Lock()
	keyPath := m.privKeyPath(keyName)
	m.mu.Unlock()

	privPEM, err := os.ReadFile(keyPath)
	if err != nil {
		return fmt.Errorf("读取私钥文件失败: %w", err)
	}

	signer, err := ssh.ParsePrivateKey(privPEM)
	if err != nil {
		return fmt.Errorf("解析私钥失败: %w", err)
	}

	config := &ssh.ClientConfig{
		User:            user,
		Auth:            []ssh.AuthMethod{ssh.PublicKeys(signer)},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         10 * time.Second,
	}

	addr := net.JoinHostPort(host, fmt.Sprintf("%d", port))
	client, err := ssh.Dial("tcp", addr, config)
	if err != nil {
		return fmt.Errorf("SSH 连接测试失败: %w", err)
	}
	client.Close()
	return nil
}

func (m *SSHManager) GetKeyPath(name string) (string, error) {
	keyPath := m.privKeyPath(name)
	if _, err := os.Stat(keyPath); os.IsNotExist(err) {
		return "", fmt.Errorf("密钥 '%s' 不存在", name)
	}
	return filepath.Abs(keyPath)
}

func runSSHCommand(client *ssh.Client, cmd string) error {
	session, err := client.NewSession()
	if err != nil {
		return fmt.Errorf("创建 session 失败: %w", err)
	}
	defer session.Close()
	return session.Run(cmd)
}
