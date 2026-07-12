//go:build darwin

package secretfile

import (
	"errors"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestDarwinStoreRejectsFileACL(t *testing.T) {
	t.Parallel()

	store := openTestStore(t)
	if err := store.Create("archive", []byte("credential")); err != nil {
		t.Fatalf("Create() returned error: %v", err)
	}
	path := filepath.Join(store.Root(), "archive")
	addDarwinACL(t, path, "everyone allow read")
	assertMode(t, path, 0o600)

	if _, err := store.Read("archive"); !errors.Is(err, ErrInsecure) {
		t.Fatalf("Read(file with ACL) error = %v, want ErrInsecure", err)
	}
}

func TestDarwinStoreRejectsDirectoryACL(t *testing.T) {
	t.Parallel()

	store := openTestStore(t)
	addDarwinACL(t, store.Root(), "everyone allow list,search")
	assertMode(t, store.Root(), 0o700)

	if err := store.Create("archive", []byte("credential")); !errors.Is(err, ErrInsecure) {
		t.Fatalf("Create(directory with ACL) error = %v, want ErrInsecure", err)
	}
}

func addDarwinACL(t testing.TB, path, entry string) {
	t.Helper()
	output, err := exec.Command("chmod", "+a", entry, path).CombinedOutput()
	if err != nil {
		t.Fatalf("chmod +a returned error: %v: %s", err, output)
	}
}
