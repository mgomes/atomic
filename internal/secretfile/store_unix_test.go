//go:build darwin || linux

package secretfile

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"
)

func TestUnixStoreUsesExactOwnerOnlyModes(t *testing.T) {
	t.Parallel()

	store := openTestStore(t)
	assertMode(t, store.Root(), 0o700)
	if err := store.Create("archive", []byte("credential")); err != nil {
		t.Fatalf("Create() returned error: %v", err)
	}
	path := filepath.Join(store.Root(), "archive")
	assertMode(t, path, 0o600)
	if err := store.Replace("archive", []byte("rotated")); err != nil {
		t.Fatalf("Replace() returned error: %v", err)
	}
	assertMode(t, path, 0o600)
}

func TestUnixStoreRejectsWidenedDirectoryMode(t *testing.T) {
	t.Parallel()

	store := openTestStore(t)
	if err := os.Chmod(store.Root(), 0o750); err != nil {
		t.Fatalf("Chmod() returned error: %v", err)
	}
	if err := store.Create("archive", []byte("credential")); !errors.Is(err, ErrInsecure) {
		t.Fatalf("Create() error = %v, want ErrInsecure", err)
	}
	if got := mode(t, store.Root()); got != 0o750 {
		t.Errorf("directory mode = %04o, want no silent repair of 0750", got)
	}
}

func TestUnixStoreRejectsWidenedFileMode(t *testing.T) {
	t.Parallel()

	store := openTestStore(t)
	if err := store.Create("archive", []byte("credential")); err != nil {
		t.Fatalf("Create() returned error: %v", err)
	}
	path := filepath.Join(store.Root(), "archive")
	if err := os.Chmod(path, 0o640); err != nil {
		t.Fatalf("Chmod() returned error: %v", err)
	}
	if _, err := store.Read("archive"); !errors.Is(err, ErrInsecure) {
		t.Fatalf("Read() error = %v, want ErrInsecure", err)
	}
	if got := mode(t, path); got != 0o640 {
		t.Errorf("file mode = %04o, want no silent repair of 0640", got)
	}
}

func TestUnixStoreRejectsSymlinkedDirectory(t *testing.T) {
	t.Parallel()

	parent := t.TempDir()
	target := filepath.Join(parent, "target")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatalf("Mkdir() returned error: %v", err)
	}
	link := filepath.Join(parent, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatalf("Symlink() returned error: %v", err)
	}
	if _, err := Open(link); !errors.Is(err, ErrInsecure) {
		t.Fatalf("Open(symlink) error = %v, want ErrInsecure", err)
	}
}

func TestUnixStoreRejectsSymlinkedSecret(t *testing.T) {
	t.Parallel()

	store := openTestStore(t)
	target := filepath.Join(t.TempDir(), "target")
	if err := os.WriteFile(target, []byte("credential"), 0o600); err != nil {
		t.Fatalf("WriteFile() returned error: %v", err)
	}
	if err := os.Symlink(target, filepath.Join(store.Root(), "archive")); err != nil {
		t.Fatalf("Symlink() returned error: %v", err)
	}
	if _, err := store.Read("archive"); !errors.Is(err, ErrInsecure) {
		t.Fatalf("Read(symlink) error = %v, want ErrInsecure", err)
	}
}

func TestUnixStoreRejectsHardLinkedSecret(t *testing.T) {
	t.Parallel()

	store := openTestStore(t)
	if err := store.Create("archive", []byte("credential")); err != nil {
		t.Fatalf("Create() returned error: %v", err)
	}
	path := filepath.Join(store.Root(), "archive")
	if err := os.Link(path, filepath.Join(store.Root(), "archive-copy")); err != nil {
		t.Fatalf("Link() returned error: %v", err)
	}
	if _, err := store.Read("archive"); !errors.Is(err, ErrInsecure) {
		t.Fatalf("Read(hard link) error = %v, want ErrInsecure", err)
	}
}

func TestUnixStoreRejectsFIFOWithoutBlocking(t *testing.T) {
	t.Parallel()

	store := openTestStore(t)
	path := filepath.Join(store.Root(), "archive")
	if err := unix.Mkfifo(path, 0o600); err != nil {
		t.Fatalf("Mkfifo() returned error: %v", err)
	}
	if _, err := store.Read("archive"); !errors.Is(err, ErrInsecure) {
		t.Fatalf("Read(FIFO) error = %v, want ErrInsecure", err)
	}
}

func assertMode(t testing.TB, path string, want os.FileMode) {
	t.Helper()
	if got := mode(t, path); got != want {
		t.Errorf("mode(%q) = %04o, want %04o", path, got, want)
	}
}

func mode(t testing.TB, path string) os.FileMode {
	t.Helper()
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatalf("Lstat(%q) returned error: %v", path, err)
	}
	return info.Mode() & (os.ModePerm | os.ModeSetuid | os.ModeSetgid | os.ModeSticky)
}
