package config_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/mgomes/ressik/internal/config"
)

func TestDefaultCredentialDirNamespacesConfigurations(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	first, err := config.DefaultCredentialDir(filepath.Join(root, "first.yaml"))
	if err != nil {
		t.Fatalf("DefaultCredentialDir(first) returned error: %v", err)
	}
	second, err := config.DefaultCredentialDir(filepath.Join(root, "second.yaml"))
	if err != nil {
		t.Fatalf("DefaultCredentialDir(second) returned error: %v", err)
	}
	if first == second {
		t.Errorf("DefaultCredentialDir() = %q for distinct configurations", first)
	}
	if got := filepath.Base(filepath.Dir(first)); got != "instances" {
		t.Errorf("credential directory parent = %q, want instances", got)
	}
	if got := len(filepath.Base(first)); got != 32 {
		t.Errorf("credential namespace length = %d, want 32", got)
	}
}

func TestDefaultCredentialDirSharesConfigAliases(t *testing.T) {
	root := t.TempDir()
	configPath := filepath.Join(root, "config.yaml")
	if err := os.WriteFile(configPath, []byte("version: 1\nplans: {}\n"), 0o600); err != nil {
		t.Fatalf("WriteFile(config) returned error: %v", err)
	}
	alias := filepath.Join(root, "alias.yaml")
	if err := os.Symlink(configPath, alias); err != nil {
		t.Skipf("config symlinks are unavailable: %v", err)
	}

	direct, err := config.DefaultCredentialDir(configPath)
	if err != nil {
		t.Fatalf("DefaultCredentialDir(config) returned error: %v", err)
	}
	linked, err := config.DefaultCredentialDir(alias)
	if err != nil {
		t.Fatalf("DefaultCredentialDir(alias) returned error: %v", err)
	}
	if direct != linked {
		t.Errorf("DefaultCredentialDir() = %q and %q for aliases", direct, linked)
	}
}
