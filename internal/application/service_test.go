package application_test

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/mgomes/ressik/internal/application"
	"github.com/mgomes/ressik/internal/repository"
)

func TestCheckRejectsPhysicalRepositoryOverlap(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("creating symbolic links requires elevated privileges on some Windows hosts")
	}

	root := t.TempDir()
	physical := filepath.Join(root, "physical")
	source := filepath.Join(physical, "source")
	repositoryPath := filepath.Join(source, "repository")
	if err := os.MkdirAll(source, 0o700); err != nil {
		t.Fatalf("MkdirAll(source) returned error: %v", err)
	}
	if _, err := repository.Initialize(repositoryPath); err != nil {
		t.Fatalf("repository.Initialize() returned error: %v", err)
	}
	alias := filepath.Join(root, "alias")
	if err := os.Symlink(physical, alias); err != nil {
		t.Fatalf("Symlink(alias) returned error: %v", err)
	}
	configPath := filepath.Join(root, "config.yaml")
	configData := "version: 1\n" +
		"repository: " + filepath.ToSlash(repositoryPath) + "\n" +
		"plans:\n" +
		"  documents:\n" +
		"    name: Documents\n" +
		"    enabled: true\n" +
		"    sources:\n" +
		"      files:\n" +
		"        path: " + filepath.ToSlash(filepath.Join(alias, "source")) + "\n"
	if err := os.WriteFile(configPath, []byte(configData), 0o600); err != nil {
		t.Fatalf("WriteFile(config.yaml) returned error: %v", err)
	}

	_, err := application.New(configPath).Check(true)
	if err == nil || !strings.Contains(err.Error(), "physically overlaps") {
		t.Fatalf("Check(true) error = %v, want physical-overlap error", err)
	}
}

func TestCheckRepositoryAllowsOfflineSources(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	repositoryPath := filepath.Join(root, "repository")
	if _, err := repository.Initialize(repositoryPath); err != nil {
		t.Fatalf("repository.Initialize() returned error: %v", err)
	}
	configPath := filepath.Join(root, "config.yaml")
	configData := "version: 1\n" +
		"repository: ./repository\n" +
		"plans:\n" +
		"  archived:\n" +
		"    name: Archived disk\n" +
		"    enabled: false\n" +
		"    sources:\n" +
		"      files:\n" +
		"        path: ./offline\n"
	if err := os.WriteFile(configPath, []byte(configData), 0o600); err != nil {
		t.Fatalf("WriteFile(config.yaml) returned error: %v", err)
	}

	if _, err := application.New(configPath).CheckRepository(); err != nil {
		t.Fatalf("CheckRepository() returned error for offline source: %v", err)
	}
	if _, err := application.New(configPath).Check(true); err == nil {
		t.Error("Check(true) error = nil for offline source")
	}
}
