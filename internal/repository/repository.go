package repository

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/gofrs/flock"

	"github.com/mgomes/ressik/internal/fsdurable"
	"github.com/mgomes/ressik/internal/object"
)

const (
	formatVersion = "1\n"
	maxFormatSize = 64
	keySize       = 32
)

// ErrBusy reports that a repository lock could not be acquired before its
// context ended.
var ErrBusy = errors.New("repository is busy")

// Repository is a local encrypted content-addressed repository.
type Repository struct {
	root  string
	codec *object.Codec
	lock  *flock.Flock
	gate  chan struct{}
}

// Initialize creates a new repository key and directory layout. It is
// idempotent when the path already contains a valid repository.
func Initialize(root string) (*Repository, error) {
	resolved, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("resolve repository path: %w", err)
	}
	if err := fsdurable.MkdirAll(resolved, 0o700); err != nil {
		return nil, fmt.Errorf("create repository: %w", err)
	}

	keyPath := filepath.Join(resolved, "repository.key")
	key, err := readFile(keyPath, keySize)
	defer func() {
		for i := range key {
			key[i] = 0
		}
	}()
	switch {
	case err == nil:
		if len(key) != keySize {
			return nil, fmt.Errorf("repository key has %d bytes, want %d", len(key), keySize)
		}
	case errors.Is(err, os.ErrNotExist):
		empty, err := repositoryHasNoData(resolved)
		if err != nil {
			return nil, fmt.Errorf("inspect repository before creating key: %w", err)
		}
		if !empty {
			return nil, errors.New("repository key is missing from an existing repository; restore repository.key instead of initializing")
		}
		key = make([]byte, keySize)
		if _, err := rand.Read(key); err != nil {
			return nil, fmt.Errorf("generate repository key: %w", err)
		}
		if err := createSyncedFile(keyPath, key, 0o600); err != nil {
			return nil, fmt.Errorf("create repository key: %w", err)
		}
	default:
		return nil, fmt.Errorf("read repository key: %w", err)
	}

	if err := ensureLayout(resolved); err != nil {
		return nil, err
	}
	formatPath := filepath.Join(resolved, "format")
	if data, err := readFile(formatPath, maxFormatSize); err == nil {
		if string(data) != formatVersion {
			return nil, fmt.Errorf("unsupported repository format %q", string(data))
		}
	} else if errors.Is(err, os.ErrNotExist) {
		if err := createSyncedFile(formatPath, []byte(formatVersion), 0o600); err != nil {
			return nil, fmt.Errorf("write repository format: %w", err)
		}
	} else {
		return nil, fmt.Errorf("read repository format: %w", err)
	}
	return Open(resolved)
}

func repositoryHasNoData(root string) (bool, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return false, err
	}
	for _, entry := range entries {
		if entry.Name() == "repository.lock" && !entry.IsDir() {
			continue
		}
		if entry.IsDir() && (entry.Name() == "blocks" || entry.Name() == "manifests" || entry.Name() == "commits") {
			empty, err := directoryTreeEmpty(filepath.Join(root, entry.Name()))
			if err != nil {
				return false, err
			}
			if empty {
				continue
			}
		}
		return false, nil
	}
	return true, nil
}

func directoryTreeEmpty(root string) (bool, error) {
	empty := true
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path != root && !entry.IsDir() {
			empty = false
			return filepath.SkipAll
		}
		return nil
	})
	return empty, err
}

// Open opens an existing repository without creating a key.
func Open(root string) (*Repository, error) {
	resolved, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("resolve repository path: %w", err)
	}
	format, err := readFile(filepath.Join(resolved, "format"), maxFormatSize)
	if err != nil {
		return nil, fmt.Errorf("read repository format: %w", err)
	}
	if string(format) != formatVersion {
		return nil, fmt.Errorf("unsupported repository format %q", string(format))
	}
	keyPath := filepath.Join(resolved, "repository.key")
	key, err := readFile(keyPath, keySize)
	if err != nil {
		return nil, fmt.Errorf("read repository key: %w", err)
	}
	defer func() {
		for i := range key {
			key[i] = 0
		}
	}()
	if len(key) != keySize {
		return nil, fmt.Errorf("repository key has %d bytes, want %d", len(key), keySize)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(keyPath)
		if err != nil {
			return nil, fmt.Errorf("inspect repository key: %w", err)
		}
		if info.Mode().Perm()&0o077 != 0 {
			return nil, fmt.Errorf("repository key permissions are %04o, want no group or other access", info.Mode().Perm())
		}
	}
	return openWithKey(resolved, key)
}

// Root returns the absolute repository path.
func (r *Repository) Root() string {
	return r.root
}

// Exclusive runs work while holding the repository's cross-process writer
// lock. The lock wait ends when ctx is canceled.
func (r *Repository) Exclusive(ctx context.Context, work func() error) (result error) {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-r.gate:
	}
	defer func() { r.gate <- struct{}{} }()

	locked, err := r.lock.TryLockContext(ctx, 100*time.Millisecond)
	if err != nil {
		return fmt.Errorf("lock repository: %w", err)
	}
	if !locked {
		if err := ctx.Err(); err != nil {
			return err
		}
		return ErrBusy
	}
	defer func() {
		if err := r.lock.Unlock(); err != nil {
			result = errors.Join(result, fmt.Errorf("unlock repository: %w", err))
		}
	}()
	return work()
}

func openWithKey(root string, key []byte) (*Repository, error) {
	codec, err := object.NewCodec(key)
	for i := range key {
		key[i] = 0
	}
	if err != nil {
		return nil, err
	}
	gate := make(chan struct{}, 1)
	gate <- struct{}{}
	return &Repository{
		root:  root,
		codec: codec,
		lock:  flock.New(filepath.Join(root, "repository.lock")),
		gate:  gate,
	}, nil
}

func ensureLayout(root string) error {
	paths := []string{
		filepath.Join(root, "blocks"),
		filepath.Join(root, "manifests"),
		filepath.Join(root, "commits"),
	}
	for shard := range 256 {
		paths = append(paths, filepath.Join(root, "blocks", fmt.Sprintf("%02x", shard)))
	}
	for _, path := range paths {
		if err := os.MkdirAll(path, 0o700); err != nil {
			return fmt.Errorf("create repository directory %q: %w", path, err)
		}
		if err := syncDir(path); err != nil {
			return fmt.Errorf("sync repository directory %q: %w", path, err)
		}
	}
	if err := syncDir(filepath.Join(root, "blocks")); err != nil {
		return fmt.Errorf("sync repository blocks directory: %w", err)
	}
	if err := syncDir(root); err != nil {
		return fmt.Errorf("sync repository directory: %w", err)
	}
	return nil
}
