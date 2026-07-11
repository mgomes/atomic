package command

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRuntimePathsNamespacesDefaultStateByConfig(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	first, err := runtimePaths(filepath.Join(root, "first.yaml"), "")
	if err != nil {
		t.Fatalf("runtimePaths(first) returned error: %v", err)
	}
	second, err := runtimePaths(filepath.Join(root, "second.yaml"), "")
	if err != nil {
		t.Fatalf("runtimePaths(second) returned error: %v", err)
	}
	if first.state == second.state {
		t.Errorf("runtimePaths() state = %q for distinct configs", first.state)
	}
	if filepath.Base(filepath.Dir(first.state)) != "instances" {
		t.Errorf("runtimePaths().state = %q, want an instances namespace", first.state)
	}
}

func TestRuntimePathsPreservesExplicitStateDirectory(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	want := filepath.Join(root, "state")
	got, err := runtimePaths(filepath.Join(root, "config.yaml"), want)
	if err != nil {
		t.Fatalf("runtimePaths() returned error: %v", err)
	}
	if got.state != want {
		t.Errorf("runtimePaths().state = %q, want %q", got.state, want)
	}
}

func TestRuntimePathsSharesStateAcrossConfigAliases(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	configPath := filepath.Join(root, "config.yaml")
	if err := os.WriteFile(configPath, []byte("version: 1\nplans: {}\n"), 0o600); err != nil {
		t.Fatalf("WriteFile(config) returned error: %v", err)
	}
	alias := filepath.Join(root, "alias.yaml")
	if err := os.Symlink(configPath, alias); err != nil {
		t.Skipf("config symlinks are unavailable: %v", err)
	}

	direct, err := runtimePaths(configPath, "")
	if err != nil {
		t.Fatalf("runtimePaths(config) returned error: %v", err)
	}
	linked, err := runtimePaths(alias, "")
	if err != nil {
		t.Fatalf("runtimePaths(alias) returned error: %v", err)
	}
	if direct.state != linked.state {
		t.Errorf("runtimePaths() states = %q and %q for aliases", direct.state, linked.state)
	}
	if direct.config != linked.config {
		t.Errorf("runtimePaths() configs = %q and %q for aliases", direct.config, linked.config)
	}
}
