package secretfile

import (
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sync"

	"github.com/mgomes/ressik/internal/fsname"
)

const (
	// MaxSize is the largest secret accepted by a Store.
	MaxSize    = 64 << 10
	tempPrefix = ".ressik-secret-"
)

var (
	namePattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,62}$`)

	// ErrExists reports that Create would replace an existing secret.
	ErrExists = errors.New("secret already exists")
	// ErrInsecure reports that a secret file or its directory is not owner-only.
	ErrInsecure = errors.New("secret storage is not protected")
	// ErrInvalidName reports a credential name that is not one portable component.
	ErrInvalidName = errors.New("invalid secret name")
	// ErrTooLarge reports a secret larger than MaxSize.
	ErrTooLarge = errors.New("secret is too large")
	// ErrUnsupported reports that this operating system cannot enforce owner-only files.
	ErrUnsupported = errors.New("protected secret files are unsupported")
)

// ProtectionError describes why an existing path is not safe for secrets.
type ProtectionError struct {
	Path   string
	Reason string
}

// Error returns a human-readable protection failure.
func (e *ProtectionError) Error() string {
	return fmt.Sprintf("%s: %q %s", ErrInsecure, e.Path, e.Reason)
}

// Unwrap allows errors.Is to match ErrInsecure.
func (e *ProtectionError) Unwrap() error {
	return ErrInsecure
}

// Store persists opaque secrets beneath one owner-only directory. Its methods
// are safe for concurrent use; other processes must not write the directory.
type Store struct {
	root    string
	writeMu sync.Mutex
}

// Open creates a missing owner-only store directory and validates an existing
// one without silently changing its protection.
func Open(root string) (*Store, error) {
	resolved, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("resolve secret directory: %w", err)
	}
	resolved = filepath.Clean(resolved)
	if err := ensureProtectedDirectory(resolved); err != nil {
		return nil, fmt.Errorf("open secret directory: %w", err)
	}
	return &Store{root: resolved}, nil
}

// Root returns the absolute store directory.
func (s *Store) Root() string {
	return s.root
}

// Read returns a copy of one secret. The caller should clear the returned
// bytes as soon as it no longer needs them.
func (s *Store) Read(name string) ([]byte, error) {
	path, err := s.path(name)
	if err != nil {
		return nil, err
	}
	if err := validateProtectedDirectory(s.root); err != nil {
		return nil, fmt.Errorf("validate secret directory: %w", err)
	}

	file, err := openProtectedFile(path)
	if err != nil {
		return nil, fmt.Errorf("open secret %q: %w", name, err)
	}
	data, readErr := io.ReadAll(io.LimitReader(file, MaxSize+1))
	closeErr := file.Close()
	if readErr != nil {
		clear(data)
		return nil, fmt.Errorf("read secret %q: %w", name, readErr)
	}
	if closeErr != nil {
		clear(data)
		return nil, fmt.Errorf("close secret %q: %w", name, closeErr)
	}
	if len(data) > MaxSize {
		clear(data)
		return nil, fmt.Errorf("read secret %q: %w (maximum %d bytes)", name, ErrTooLarge, MaxSize)
	}
	return data, nil
}

// Create atomically publishes a new secret and refuses to replace an existing
// name.
func (s *Store) Create(name string, data []byte) error {
	path, err := s.path(name)
	if err != nil {
		return err
	}
	if err := validateData(data); err != nil {
		return fmt.Errorf("create secret %q: %w", name, err)
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if err := validateProtectedDirectory(s.root); err != nil {
		return fmt.Errorf("validate secret directory: %w", err)
	}

	temp, err := s.writeTemp(data)
	if err != nil {
		return fmt.Errorf("create secret %q: %w", name, err)
	}
	if err := publishNoReplace(temp, path); err != nil {
		cleanupErr := removeTemp(temp)
		if isAlreadyExists(err) {
			return errors.Join(fmt.Errorf("create secret %q: %w", name, ErrExists), cleanupErr)
		}
		return errors.Join(fmt.Errorf("publish secret %q: %w", name, err), cleanupErr)
	}
	if err := validatePublishedFile(path); err != nil {
		return errors.Join(
			fmt.Errorf("validate published secret %q: %w", name, err),
			removeTemp(temp),
		)
	}
	if err := removeTemp(temp); err != nil {
		return fmt.Errorf("remove temporary secret after publication: %w", err)
	}
	if err := syncProtectedDirectory(s.root); err != nil {
		return fmt.Errorf("sync secret directory: %w", err)
	}
	return nil
}

// Replace atomically replaces an existing protected secret. It refuses to
// replace a missing or insecure path.
func (s *Store) Replace(name string, data []byte) error {
	path, err := s.path(name)
	if err != nil {
		return err
	}
	if err := validateData(data); err != nil {
		return fmt.Errorf("replace secret %q: %w", name, err)
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if err := validateProtectedDirectory(s.root); err != nil {
		return fmt.Errorf("validate secret directory: %w", err)
	}

	existing, err := openProtectedFile(path)
	if err != nil {
		return fmt.Errorf("open secret %q for replacement: %w", name, err)
	}
	if err := existing.Close(); err != nil {
		return fmt.Errorf("close secret %q before replacement: %w", name, err)
	}

	temp, err := s.writeTemp(data)
	if err != nil {
		return fmt.Errorf("replace secret %q: %w", name, err)
	}
	if err := publishReplace(temp, path); err != nil {
		return errors.Join(
			fmt.Errorf("publish replacement for secret %q: %w", name, err),
			removeTemp(temp),
		)
	}
	if err := validatePublishedFile(path); err != nil {
		return errors.Join(
			fmt.Errorf("validate replacement for secret %q: %w", name, err),
			removeTemp(temp),
		)
	}
	if err := removeTemp(temp); err != nil {
		return fmt.Errorf("remove replaced secret: %w", err)
	}
	if err := syncProtectedDirectory(s.root); err != nil {
		return fmt.Errorf("sync secret directory: %w", err)
	}
	return nil
}

func (s *Store) path(name string) (string, error) {
	if !namePattern.MatchString(name) {
		return "", fmt.Errorf("%w %q: must match %s", ErrInvalidName, name, namePattern)
	}
	if err := fsname.Component(name); err != nil {
		return "", fmt.Errorf("%w %q: %v", ErrInvalidName, name, err)
	}
	return filepath.Join(s.root, name), nil
}

func (s *Store) writeTemp(data []byte) (string, error) {
	contents := append([]byte(nil), data...)
	defer clear(contents)

	for range 10 {
		path := filepath.Join(s.root, tempPrefix+rand.Text())
		err := writeProtectedFile(path, contents)
		if isAlreadyExists(err) {
			continue
		}
		if err != nil {
			return "", err
		}
		return path, nil
	}
	return "", errors.New("allocate a unique temporary secret name")
}

func writeProtectedFile(path string, data []byte) (result error) {
	file, err := createProtectedFile(path)
	if err != nil {
		return fmt.Errorf("create temporary secret: %w", err)
	}
	complete := false
	defer func() {
		if complete {
			return
		}
		result = errors.Join(result, file.Close())
		result = errors.Join(result, os.Remove(path))
	}()

	if err := validateProtectedFile(file, path); err != nil {
		return fmt.Errorf("validate temporary secret: %w", err)
	}
	written, err := file.Write(data)
	if err != nil {
		return fmt.Errorf("write temporary secret: %w", err)
	}
	if written != len(data) {
		return fmt.Errorf("write temporary secret: %w", io.ErrShortWrite)
	}
	if err := file.Sync(); err != nil {
		return fmt.Errorf("sync temporary secret: %w", err)
	}
	if err := validateProtectedFile(file, path); err != nil {
		return fmt.Errorf("revalidate temporary secret: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close temporary secret: %w", err)
	}
	complete = true
	return nil
}

func validateData(data []byte) error {
	if len(data) > MaxSize {
		return fmt.Errorf("%w: got %d bytes, maximum %d", ErrTooLarge, len(data), MaxSize)
	}
	return nil
}

func removeTemp(path string) error {
	err := os.Remove(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

func validatePublishedFile(path string) error {
	file, err := openProtectedFile(path)
	if err != nil {
		return err
	}
	return file.Close()
}
