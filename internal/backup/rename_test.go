package backup

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRenameNoReplaceRefusesExistingDestination(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	source := filepath.Join(root, "source")
	destination := filepath.Join(root, "destination")
	if err := os.WriteFile(source, []byte("new"), 0o600); err != nil {
		t.Fatalf("WriteFile(source) returned error: %v", err)
	}
	if err := os.WriteFile(destination, []byte("old"), 0o600); err != nil {
		t.Fatalf("WriteFile(destination) returned error: %v", err)
	}

	if err := renameNoReplace(source, destination); err == nil {
		t.Fatal("renameNoReplace() error = nil, want no-clobber error")
	}
	assertFileContents(t, source, "new")
	assertFileContents(t, destination, "old")
}

func TestRenameNoReplacePublishesNewDestination(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	source := filepath.Join(root, "source")
	destination := filepath.Join(root, "destination")
	if err := os.Mkdir(source, 0o700); err != nil {
		t.Fatalf("Mkdir(source) returned error: %v", err)
	}
	if err := os.WriteFile(filepath.Join(source, "file"), []byte("published"), 0o600); err != nil {
		t.Fatalf("WriteFile(source/file) returned error: %v", err)
	}

	if err := renameNoReplace(source, destination); err != nil {
		t.Fatalf("renameNoReplace() returned error: %v", err)
	}
	assertFileContents(t, filepath.Join(destination, "file"), "published")
	if _, err := os.Lstat(source); !os.IsNotExist(err) {
		t.Fatalf("Lstat(source) error = %v, want not exist", err)
	}
}

func assertFileContents(t *testing.T, path, want string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(%s) returned error: %v", path, err)
	}
	if got := string(data); got != want {
		t.Errorf("ReadFile(%s) = %q, want %q", path, got, want)
	}
}
