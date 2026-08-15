package config_test

import (
	"path/filepath"
	"testing"

	"github.com/mgomes/atomic/internal/config"
)

func TestPathUsesAtomicEnvironment(t *testing.T) {
	want := filepath.Join(t.TempDir(), "custom.yaml")
	t.Setenv("ATOMIC_CONFIG", want)

	got, err := config.Path("")
	if err != nil {
		t.Fatalf("Path() returned error: %v", err)
	}
	if got != want {
		t.Errorf("Path() = %q, want %q", got, want)
	}
}

func TestDefaultPathsUseAtomicNamespace(t *testing.T) {
	t.Setenv("ATOMIC_CONFIG", "")

	configPath, err := config.Path("")
	if err != nil {
		t.Fatalf("Path() returned error: %v", err)
	}
	if got, want := filepath.Base(filepath.Dir(configPath)), "atomic"; got != want {
		t.Errorf("Path() parent = %q, want %q", got, want)
	}

	repositoryPath, err := config.DefaultRepository()
	if err != nil {
		t.Fatalf("DefaultRepository() returned error: %v", err)
	}
	if got, want := filepath.Base(filepath.Dir(repositoryPath)), "atomic"; got != want {
		t.Errorf("DefaultRepository() parent = %q, want %q", got, want)
	}

	statePath, err := config.DefaultStateDir()
	if err != nil {
		t.Fatalf("DefaultStateDir() returned error: %v", err)
	}
	if got, want := filepath.Base(statePath), "atomic"; got != want {
		t.Errorf("DefaultStateDir() base = %q, want %q", got, want)
	}

	credentialPath, err := config.DefaultCredentialDir(filepath.Join(t.TempDir(), "config.yaml"))
	if err != nil {
		t.Fatalf("DefaultCredentialDir() returned error: %v", err)
	}
	credentialRoot := filepath.Dir(filepath.Dir(filepath.Dir(credentialPath)))
	if got, want := filepath.Base(credentialRoot), "atomic"; got != want {
		t.Errorf("DefaultCredentialDir() namespace = %q, want %q", got, want)
	}
}
