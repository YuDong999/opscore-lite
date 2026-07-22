package store

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
)

// JSONFile 通用 JSON 文件存储，支持热读写。
type JSONFile struct {
	path string
	mu   sync.RWMutex
}

func New(dir, name string) (*JSONFile, error) {
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, err
	}
	return &JSONFile{path: filepath.Join(dir, name)}, nil
}

func (f *JSONFile) Read(v interface{}) error {
	f.mu.RLock()
	defer f.mu.RUnlock()
	b, err := os.ReadFile(f.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	return json.Unmarshal(b, v)
}

func (f *JSONFile) Write(v interface{}) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(f.path, b, 0644)
}
