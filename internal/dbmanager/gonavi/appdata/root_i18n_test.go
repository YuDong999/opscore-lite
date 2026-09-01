package appdata

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSetActiveRootUsesCreateDataDirectorySentinel(t *testing.T) {
	homeDir := t.TempDir()
	t.Setenv("HOME", homeDir)
	t.Setenv("USERPROFILE", homeDir)

	blockingPath := filepath.Join(t.TempDir(), "blocked-root")
	if err := os.WriteFile(blockingPath, []byte("blocked"), 0o644); err != nil {
		t.Fatalf("write blocking path: %v", err)
	}

	_, err := SetActiveRoot(blockingPath)
	if err == nil {
		t.Fatal("expected SetActiveRoot to fail when target path is an existing file")
	}
	if !errors.Is(err, ErrSetActiveRootCreateDataDirectory) {
		t.Fatalf("expected create-data-directory sentinel, got %v", err)
	}
	if strings.Contains(err.Error(), "创建数据目录失败") {
		t.Fatalf("expected no raw Chinese create-data-directory wrapper, got %q", err.Error())
	}
}

func TestSetActiveRootUsesCreateBootstrapDirectorySentinel(t *testing.T) {
	homeDir := t.TempDir()
	t.Setenv("HOME", homeDir)
	t.Setenv("USERPROFILE", homeDir)

	defaultRoot := filepath.Join(homeDir, ".gonavi")
	if err := os.WriteFile(defaultRoot, []byte("blocked"), 0o644); err != nil {
		t.Fatalf("write blocking default root: %v", err)
	}

	_, err := SetActiveRoot(filepath.Join(t.TempDir(), "custom-root"))
	if err == nil {
		t.Fatal("expected SetActiveRoot to fail when default bootstrap directory is blocked by a file")
	}
	if !errors.Is(err, ErrSetActiveRootCreateBootstrapDirectory) {
		t.Fatalf("expected create-bootstrap-directory sentinel, got %v", err)
	}
	if strings.Contains(err.Error(), "创建默认引导目录失败") {
		t.Fatalf("expected no raw Chinese create-bootstrap-directory wrapper, got %q", err.Error())
	}
}
