package db

import (
	"debug/pe"
	"encoding/binary"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidateWindowsExecutableMachineIgnoresCOFFStringTableEOF(t *testing.T) {
	path := filepath.Join(t.TempDir(), "oceanbase-driver-agent-windows-amd64.exe")
	writeMinimalWindowsPEWithBrokenStringTable(t, path, peMachineAmd64)

	if file, err := pe.Open(path); err == nil {
		_ = file.Close()
		t.Fatal("fixture should reproduce debug/pe string table failure")
	} else if !strings.Contains(err.Error(), "string table") {
		t.Fatalf("fixture should fail in debug/pe string table parsing, got %v", err)
	}

	if err := validateWindowsExecutableMachineForArch(path, "amd64"); err != nil {
		t.Fatalf("valid machine header should pass without reading optional string table: %v", err)
	}
}

func TestValidateWindowsExecutableMachineRejectsMachineMismatch(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sqlserver-driver-agent-windows-arm64.exe")
	writeMinimalWindowsPE(t, path, peMachineArm64)

	err := validateWindowsExecutableMachineForArch(path, "amd64")
	if err == nil {
		t.Fatal("expected machine mismatch to be rejected")
	}
	if !strings.Contains(err.Error(), "windows-arm64") || !strings.Contains(err.Error(), "windows-amd64") {
		t.Fatalf("expected architecture labels in error, got %v", err)
	}
}

func TestValidateWindowsExecutableMachineRejectsNonPEFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "oceanbase-driver-agent-windows-amd64.exe")
	if err := os.WriteFile(path, []byte("not a windows executable"), 0o644); err != nil {
		t.Fatalf("write fixture failed: %v", err)
	}

	err := validateWindowsExecutableMachineForArch(path, "amd64")
	if err == nil {
		t.Fatal("expected non-PE file to be rejected")
	}
	if !strings.Contains(err.Error(), "无法识别为有效的 Windows 可执行文件") {
		t.Fatalf("expected executable validation error, got %v", err)
	}
}

func writeMinimalWindowsPE(t *testing.T, path string, machine uint16) {
	t.Helper()

	const peOffset = 0x80
	content := make([]byte, peOffset+4+20)
	content[0] = 'M'
	content[1] = 'Z'
	binary.LittleEndian.PutUint32(content[peHeaderOffsetAddr:], peOffset)
	copy(content[peOffset:], []byte{'P', 'E', 0, 0})
	binary.LittleEndian.PutUint16(content[peOffset+4:], machine)

	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatalf("write PE fixture failed: %v", err)
	}
}

func writeMinimalWindowsPEWithBrokenStringTable(t *testing.T, path string, machine uint16) {
	t.Helper()

	const peOffset = 0x80
	const symbolTableOffset = peOffset + 4 + 20
	content := make([]byte, symbolTableOffset+18)
	content[0] = 'M'
	content[1] = 'Z'
	binary.LittleEndian.PutUint32(content[peHeaderOffsetAddr:], peOffset)
	copy(content[peOffset:], []byte{'P', 'E', 0, 0})
	coffHeader := content[peOffset+4:]
	binary.LittleEndian.PutUint16(coffHeader[0:], machine)
	binary.LittleEndian.PutUint32(coffHeader[8:], symbolTableOffset)
	binary.LittleEndian.PutUint32(coffHeader[12:], 1)

	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatalf("write PE fixture failed: %v", err)
	}
}
