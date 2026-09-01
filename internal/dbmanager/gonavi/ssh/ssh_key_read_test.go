package ssh

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"opscore/internal/dbmanager/gonavi/connection"
)

func TestConnectSSHReturnsErrorWhenPrivateKeyMissing(t *testing.T) {
	t.Cleanup(CloseAllSSHClients)

	missing := filepath.Join(t.TempDir(), "id_ed25519")
	_, err := connectSSH(connection.SSHConfig{
		Host:    "127.0.0.1",
		Port:    1,
		User:    "root",
		KeyPath: missing,
	})
	if err == nil {
		t.Fatal("expected error for missing private key")
	}
	message := err.Error()
	if !strings.Contains(message, "failed to read SSH private key") {
		t.Fatalf("expected read failure message, got %q", message)
	}
	if !strings.Contains(message, missing) {
		t.Fatalf("expected key path in error, got %q", message)
	}
}

func TestConnectSSHReturnsErrorWhenPrivateKeyInvalid(t *testing.T) {
	t.Cleanup(CloseAllSSHClients)

	keyPath := filepath.Join(t.TempDir(), "custom_deploy_key")
	if err := os.WriteFile(keyPath, []byte("not-a-private-key"), 0o600); err != nil {
		t.Fatalf("write key: %v", err)
	}

	_, err := connectSSH(connection.SSHConfig{
		Host:    "127.0.0.1",
		Port:    1,
		User:    "root",
		KeyPath: keyPath,
	})
	if err == nil {
		t.Fatal("expected error for invalid private key")
	}
	message := err.Error()
	if !strings.Contains(message, "failed to parse SSH private key") {
		t.Fatalf("expected parse failure message, got %q", message)
	}
	if !strings.Contains(message, keyPath) {
		t.Fatalf("expected key path in error, got %q", message)
	}
}
