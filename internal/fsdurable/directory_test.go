package fsdurable_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/mgomes/ressik/internal/fsdurable"
)

func TestMkdirAllCreatesNestedDirectories(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "one", "two", "three")
	if err := fsdurable.MkdirAll(path, 0o700); err != nil {
		t.Fatalf("MkdirAll() returned error: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat() returned error: %v", err)
	}
	if !info.IsDir() {
		t.Errorf("MkdirAll() path mode = %s, want directory", info.Mode())
	}
}

func TestMkdirAllRejectsNonDirectoryAncestor(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	file := filepath.Join(root, "file")
	if err := os.WriteFile(file, []byte("not a directory"), 0o600); err != nil {
		t.Fatalf("WriteFile() returned error: %v", err)
	}
	if err := fsdurable.MkdirAll(filepath.Join(file, "child"), 0o700); err == nil {
		t.Error("MkdirAll() error = nil, want non-directory error")
	}
}
