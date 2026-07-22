package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

type BackupSnapshot struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	SrcPath  string `json:"srcPath"`
	DstPath  string `json:"dstPath"`
	Size     int64  `json:"size"`
	Created  string `json:"created"`
}

var backupDir string

func InitBackup(dir string) {
	backupDir = filepath.Join(dir, "backups")
	os.MkdirAll(backupDir, 0755)
}

// GuardBackupList 列出所有备份快照
func GuardBackupList(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	snapshots, err := listBackups()
	if err != nil {
		json.NewEncoder(w).Encode(map[string]any{"snapshots": []BackupSnapshot{}, "error": err.Error()})
		return
	}
	json.NewEncoder(w).Encode(map[string]any{"snapshots": snapshots})
}

type backupAction struct {
	Action  string `json:"action"`
	SrcPath string `json:"srcPath"`
	DstPath string `json:"dstPath"`
	Name    string `json:"name"`
	ID      string `json:"id"`
}

// GuardBackupAction 创建/删除/恢复备份
func GuardBackupAction(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	var body backupAction
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": "invalid body"})
		return
	}

	switch body.Action {
	case "create":
		if body.SrcPath == "" {
			w.WriteHeader(http.StatusBadRequest)
			json.NewEncoder(w).Encode(map[string]string{"error": "srcPath required"})
			return
		}
		// 检查源路径存在
		info, err := os.Stat(body.SrcPath)
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			json.NewEncoder(w).Encode(map[string]string{"error": "源路径不存在"})
			return
		}
		name := body.Name
		if name == "" {
			name = info.Name()
		}
		ts := time.Now().Format("20060102_150405")
		snapID := fmt.Sprintf("%s_%s", ts, name)
		snapDir := filepath.Join(backupDir, snapID)
		os.MkdirAll(snapDir, 0755)

		dstPath := body.DstPath
		if dstPath == "" {
			dstPath = filepath.Join(snapDir, "backup.tar.gz")
		} else {
			os.MkdirAll(filepath.Dir(dstPath), 0755)
			dstPath = filepath.Join(snapDir, filepath.Base(dstPath))
		}

		// 使用 tar.gz 备份
		cmd := exec.Command("tar", "czf", dstPath, "-C", filepath.Dir(body.SrcPath), filepath.Base(body.SrcPath))
		if out, err := cmd.CombinedOutput(); err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			json.NewEncoder(w).Encode(map[string]string{"error": string(out)})
			return
		}

		// 写元数据
		meta := map[string]string{
			"id":      snapID,
			"name":    name,
			"srcPath": body.SrcPath,
			"dstPath": dstPath,
			"created": time.Now().Format(time.RFC3339),
		}
		metaBytes, _ := json.Marshal(meta)
		os.WriteFile(filepath.Join(snapDir, "meta.json"), metaBytes, 0644)

		json.NewEncoder(w).Encode(map[string]string{"ok": "true", "id": snapID})

	case "delete":
		if body.ID == "" {
			w.WriteHeader(http.StatusBadRequest)
			json.NewEncoder(w).Encode(map[string]string{"error": "id required"})
			return
		}
		snapDir := filepath.Join(backupDir, body.ID)
		if err := os.RemoveAll(snapDir); err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
			return
		}
		json.NewEncoder(w).Encode(map[string]string{"ok": "true"})

	case "restore":
		if body.ID == "" {
			w.WriteHeader(http.StatusBadRequest)
			json.NewEncoder(w).Encode(map[string]string{"error": "id required"})
			return
		}
		snapDir := filepath.Join(backupDir, body.ID)
		// 找到 tar.gz 文件
		var tarFile string
		filepath.Walk(snapDir, func(path string, info os.FileInfo, err error) error {
			if strings.HasSuffix(path, ".tar.gz") {
				tarFile = path
			}
			return nil
		})
		if tarFile == "" {
			w.WriteHeader(http.StatusNotFound)
			json.NewEncoder(w).Encode(map[string]string{"error": "备份文件不存在"})
			return
		}
		// 恢复到原路径
		metaBytes, _ := os.ReadFile(filepath.Join(snapDir, "meta.json"))
		var meta map[string]string
		json.Unmarshal(metaBytes, &meta)
		restorePath := meta["srcPath"]
		if restorePath == "" {
			restorePath = "/"
		}
		cmd := exec.Command("tar", "xzf", tarFile, "-C", filepath.Dir(restorePath))
		if out, err := cmd.CombinedOutput(); err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			json.NewEncoder(w).Encode(map[string]string{"error": string(out)})
			return
		}
		json.NewEncoder(w).Encode(map[string]string{"ok": "true"})

	default:
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": "unknown action"})
	}
}

func listBackups() ([]BackupSnapshot, error) {
	entries, err := os.ReadDir(backupDir)
	if err != nil {
		return nil, err
	}
	var snapshots []BackupSnapshot
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		metaBytes, err := os.ReadFile(filepath.Join(backupDir, e.Name(), "meta.json"))
		if err != nil {
			continue
		}
		var meta map[string]string
		json.Unmarshal(metaBytes, &meta)

		// 计算大小
		var size int64
		filepath.Walk(filepath.Join(backupDir, e.Name()), func(path string, info os.FileInfo, err error) error {
			if err == nil && !info.IsDir() {
				size += info.Size()
			}
			return nil
		})

		snapshots = append(snapshots, BackupSnapshot{
			ID:      meta["id"],
			Name:    meta["name"],
			SrcPath: meta["srcPath"],
			DstPath: meta["dstPath"],
			Size:    size,
			Created: meta["created"],
		})
	}
	_ = regexp.MustCompile("") // 确保 regexp 被引用
	return snapshots, nil
}
