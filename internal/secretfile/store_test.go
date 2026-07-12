package secretfile

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestStoreRoundTrip(t *testing.T) {
	t.Parallel()

	store := openTestStore(t)
	if !filepath.IsAbs(store.Root()) {
		t.Fatalf("Root() = %q, want absolute path", store.Root())
	}
	if err := store.Create("cloud-primary", []byte("first credential")); err != nil {
		t.Fatalf("Create() returned error: %v", err)
	}
	assertSecret(t, store, "cloud-primary", "first credential")

	if err := store.Replace("cloud-primary", []byte("rotated credential")); err != nil {
		t.Fatalf("Replace() returned error: %v", err)
	}
	assertSecret(t, store, "cloud-primary", "rotated credential")
}

func TestCreateRefusesToReplace(t *testing.T) {
	t.Parallel()

	store := openTestStore(t)
	if err := store.Create("archive", []byte("original")); err != nil {
		t.Fatalf("Create() returned error: %v", err)
	}
	if err := store.Create("archive", []byte("replacement")); !errors.Is(err, ErrExists) {
		t.Fatalf("Create(existing) error = %v, want ErrExists", err)
	}
	assertSecret(t, store, "archive", "original")
}

func TestReplaceRequiresExistingProtectedSecret(t *testing.T) {
	t.Parallel()

	store := openTestStore(t)
	if err := store.Replace("missing", []byte("credential")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Replace(missing) error = %v, want os.ErrNotExist", err)
	}
}

func TestAtomicReplacementRefusesMissingDestination(t *testing.T) {
	t.Parallel()

	store := openTestStore(t)
	source := filepath.Join(store.Root(), ".replacement")
	writeRawProtectedFile(t, source, []byte("credential"))
	destination := filepath.Join(store.Root(), "missing")
	if err := publishReplace(source, destination); err == nil {
		t.Fatal("publishReplace(missing) error = nil, want error")
	}
	if _, err := os.Stat(source); err != nil {
		t.Fatalf("Stat(source) after failed replacement returned error: %v", err)
	}
	if _, err := os.Stat(destination); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Stat(destination) error = %v, want os.ErrNotExist", err)
	}
}

func TestStoreRejectsInvalidNames(t *testing.T) {
	t.Parallel()

	store := openTestStore(t)
	invalid := []string{
		"",
		".",
		"..",
		"../escape",
		`escape\name`,
		"Uppercase",
		"contains.dot",
		"-leading-dash",
		strings.Repeat("a", 64),
		"con",
		"com1",
	}
	for _, name := range invalid {
		if err := store.Create(name, []byte("credential")); !errors.Is(err, ErrInvalidName) {
			t.Errorf("Create(%q) error = %v, want ErrInvalidName", name, err)
		}
	}
}

func TestStoreEnforcesSizeLimit(t *testing.T) {
	t.Parallel()

	store := openTestStore(t)
	tooLarge := bytes.Repeat([]byte("x"), MaxSize+1)
	if err := store.Create("oversized", tooLarge); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("Create(oversized) error = %v, want ErrTooLarge", err)
	}

	path := filepath.Join(store.Root(), "raw-oversized")
	writeRawProtectedFile(t, path, tooLarge)
	if _, err := store.Read("raw-oversized"); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("Read(oversized) error = %v, want ErrTooLarge", err)
	}
}

func TestFailedReplacementKeepsOriginal(t *testing.T) {
	t.Parallel()

	store := openTestStore(t)
	if err := store.Create("archive", []byte("original")); err != nil {
		t.Fatalf("Create() returned error: %v", err)
	}
	tooLarge := bytes.Repeat([]byte("x"), MaxSize+1)
	if err := store.Replace("archive", tooLarge); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("Replace(oversized) error = %v, want ErrTooLarge", err)
	}
	assertSecret(t, store, "archive", "original")
}

func TestReadersSeeCompleteSecretsDuringReplacement(t *testing.T) {
	t.Parallel()

	store := openTestStore(t)
	values := [][]byte{
		bytes.Repeat([]byte("a"), 4096),
		bytes.Repeat([]byte("b"), 4096),
	}
	if err := store.Create("rotating", values[0]); err != nil {
		t.Fatalf("Create() returned error: %v", err)
	}

	stop := make(chan struct{})
	errorsFound := make(chan error, 1)
	recordError := func(err error) {
		select {
		case errorsFound <- err:
		default:
		}
	}
	var readers sync.WaitGroup
	for range 4 {
		readers.Go(func() {
			for {
				select {
				case <-stop:
					return
				default:
				}
				data, err := store.Read("rotating")
				if err != nil {
					recordError(err)
					return
				}
				valid := bytes.Equal(data, values[0]) || bytes.Equal(data, values[1])
				clear(data)
				if !valid {
					recordError(errors.New("reader observed a partial secret"))
					return
				}
			}
		})
	}

	for replacement := range 100 {
		if err := store.Replace("rotating", values[(replacement+1)%len(values)]); err != nil {
			close(stop)
			readers.Wait()
			t.Fatalf("Replace(%d) returned error: %v", replacement, err)
		}
	}
	close(stop)
	readers.Wait()
	select {
	case err := <-errorsFound:
		t.Fatal(err)
	default:
	}
}

func TestOpenRejectsNonDirectory(t *testing.T) {
	t.Parallel()

	root := filepath.Join(t.TempDir(), "secrets")
	if err := os.WriteFile(root, []byte("not a directory"), 0o600); err != nil {
		t.Fatalf("WriteFile() returned error: %v", err)
	}
	if _, err := Open(root); !errors.Is(err, ErrInsecure) {
		t.Fatalf("Open(file) error = %v, want ErrInsecure", err)
	}
}

func openTestStore(t testing.TB) *Store {
	t.Helper()
	store, err := Open(filepath.Join(t.TempDir(), "secrets"))
	if err != nil {
		t.Fatalf("Open() returned error: %v", err)
	}
	return store
}

func assertSecret(t testing.TB, store *Store, name, want string) {
	t.Helper()
	data, err := store.Read(name)
	if err != nil {
		t.Fatalf("Read(%q) returned error: %v", name, err)
	}
	defer clear(data)
	if got := string(data); got != want {
		t.Errorf("Read(%q) = %q, want %q", name, got, want)
	}
}

func writeRawProtectedFile(t testing.TB, path string, data []byte) {
	t.Helper()
	file, err := createProtectedFile(path)
	if err != nil {
		t.Fatalf("createProtectedFile() returned error: %v", err)
	}
	if _, err := file.Write(data); err != nil {
		_ = file.Close()
		t.Fatalf("Write() returned error: %v", err)
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		t.Fatalf("Sync() returned error: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("Close() returned error: %v", err)
	}
}

func TestProtectionErrorMatchesSentinel(t *testing.T) {
	t.Parallel()

	err := &ProtectionError{Path: "/credentials/example", Reason: "is public"}
	if !errors.Is(err, ErrInsecure) {
		t.Fatal("errors.Is(ProtectionError, ErrInsecure) = false, want true")
	}
	if got := err.Error(); !strings.Contains(got, fmt.Sprintf("%q", err.Path)) {
		t.Errorf("ProtectionError.Error() = %q, want path", got)
	}
}
