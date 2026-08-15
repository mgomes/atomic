package repository_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mgomes/atomic/internal/object"
	"github.com/mgomes/atomic/internal/repository"
	"github.com/mgomes/atomic/merkle"
)

const testConfigurationID = "11111111111111111111111111111111"

func TestRepositoryDeduplicatesAndCommitsBlocks(t *testing.T) {
	t.Parallel()

	repo, err := repository.Initialize(t.TempDir())
	if err != nil {
		t.Fatalf("Initialize() returned error: %v", err)
	}
	ctx := context.Background()
	plaintext := []byte("one encrypted block")

	var ref repository.BlockRef
	err = repo.Exclusive(ctx, func() error {
		var created bool
		ref, created, err = repo.PutBlock(ctx, plaintext)
		if err != nil {
			return err
		}
		if !created {
			t.Error("PutBlock(first) created = false, want true")
		}
		gotRef, created, err := repo.PutBlock(ctx, plaintext)
		if err != nil {
			return err
		}
		if created {
			t.Error("PutBlock(second) created = true, want false")
		}
		if gotRef != ref {
			t.Errorf("PutBlock(second) ref = %+v, want %+v", gotRef, ref)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("Repository.Exclusive() returned error: %v", err)
	}

	got, err := repo.ReadBlock(ctx, ref)
	if err != nil {
		t.Fatalf("ReadBlock() returned error: %v", err)
	}
	if string(got) != string(plaintext) {
		t.Errorf("ReadBlock() = %q, want %q", got, plaintext)
	}

	id, err := object.RandomID()
	if err != nil {
		t.Fatalf("RandomID() returned error: %v", err)
	}
	createdAt := time.Date(2026, time.July, 10, 12, 0, 0, 0, time.UTC)
	digest := merkle.Leaf([]byte("file"))
	manifest := repository.Manifest{
		Version:         1,
		ConfigurationID: testConfigurationID,
		ID:              id,
		PlanID:          "test",
		PlanName:        "Test",
		CreatedAt:       createdAt,
		Root:            digest,
		ChunkSize:       4 << 20,
		Statistics: repository.Stats{
			Files:          1,
			PlaintextBytes: int64(len(plaintext)),
		},
		Sources: []repository.Source{{
			ID:           "files",
			OriginalPath: "/source",
			Digest:       digest,
			Entries: []repository.Entry{{
				Path:        ".",
				Kind:        repository.FileEntry,
				Mode:        0o600,
				ModifiedAt:  createdAt,
				Size:        int64(len(plaintext)),
				Digest:      digest,
				Blocks:      []repository.BlockRef{ref},
				ChangeToken: "test-change-token",
			}},
		}},
	}
	if err := repo.Exclusive(ctx, func() error {
		return repo.Commit(ctx, manifest)
	}); err != nil {
		t.Fatalf("Repository.Exclusive(Commit) returned error: %v", err)
	}
	snapshots, err := repo.List(ctx, "test")
	if err != nil {
		t.Fatalf("List() returned error: %v", err)
	}
	if got, want := len(snapshots), 1; got != want {
		t.Fatalf("List() returned %d snapshots, want %d", got, want)
	}
	if snapshots[0].ID != id {
		t.Errorf("List()[0].ID = %s, want %s", snapshots[0].ID, id)
	}
}

func TestExclusiveSerializesGoroutinesOnOneRepository(t *testing.T) {
	t.Parallel()

	repo, err := repository.Initialize(t.TempDir())
	if err != nil {
		t.Fatalf("Initialize() returned error: %v", err)
	}
	entered := make(chan struct{})
	release := make(chan struct{})
	firstDone := make(chan error, 1)
	var workers sync.WaitGroup
	workers.Go(func() {
		firstDone <- repo.Exclusive(context.Background(), func() error {
			close(entered)
			<-release
			return nil
		})
	})
	<-entered

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	secondEntered := false
	err = repo.Exclusive(ctx, func() error {
		secondEntered = true
		return nil
	})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("Exclusive(second) error = %v, want deadline exceeded", err)
	}
	if secondEntered {
		t.Error("Exclusive(second) entered while first goroutine held the repository")
	}
	close(release)
	if err := <-firstDone; err != nil {
		t.Errorf("Exclusive(first) returned error: %v", err)
	}
	workers.Wait()
}

func TestInitializeCreatesCompleteLayout(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	if _, err := repository.Initialize(root); err != nil {
		t.Fatalf("Initialize() returned error: %v", err)
	}

	entries, err := os.ReadDir(filepath.Join(root, "blocks"))
	if err != nil {
		t.Fatalf("ReadDir(blocks) returned error: %v", err)
	}
	if got, want := len(entries), 256; got != want {
		t.Fatalf("blocks contains %d entries, want %d", got, want)
	}
	for shard := range 256 {
		name := formatShard(shard)
		info, err := os.Stat(filepath.Join(root, "blocks", name))
		if err != nil {
			t.Fatalf("Stat(block shard %s) returned error: %v", name, err)
		}
		if !info.IsDir() {
			t.Errorf("block shard %s is not a directory", name)
		}
	}

	format, err := os.ReadFile(filepath.Join(root, "format"))
	if err != nil {
		t.Fatalf("ReadFile(format) returned error: %v", err)
	}
	if got, want := string(format), "1\n"; got != want {
		t.Errorf("format = %q, want %q", got, want)
	}
	key, err := os.ReadFile(filepath.Join(root, "repository.key"))
	if err != nil {
		t.Fatalf("ReadFile(repository.key) returned error: %v", err)
	}
	if got, want := len(key), 32; got != want {
		t.Errorf("repository.key has %d bytes, want %d", got, want)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(filepath.Join(root, "repository.key"))
		if err != nil {
			t.Fatalf("Stat(repository.key) returned error: %v", err)
		}
		if got, want := info.Mode().Perm(), os.FileMode(0o600); got != want {
			t.Errorf("repository.key permissions = %04o, want %04o", got, want)
		}
	}

	missingShard := filepath.Join(root, "blocks", "7f")
	if err := os.Remove(missingShard); err != nil {
		t.Fatalf("Remove(block shard) returned error: %v", err)
	}
	if _, err := repository.Initialize(root); err != nil {
		t.Fatalf("Initialize(second) returned error: %v", err)
	}
	assertExists(t, missingShard)
	keyAfter, err := os.ReadFile(filepath.Join(root, "repository.key"))
	if err != nil {
		t.Fatalf("ReadFile(repository.key after second initialize) returned error: %v", err)
	}
	if string(keyAfter) != string(key) {
		t.Error("Initialize(second) replaced the repository key")
	}
}

func TestInitializeRefusesToReplaceMissingRepositoryKey(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	if _, err := repository.Initialize(root); err != nil {
		t.Fatalf("Initialize(first) returned error: %v", err)
	}
	keyPath := filepath.Join(root, "repository.key")
	if err := os.Remove(keyPath); err != nil {
		t.Fatalf("Remove(repository.key) returned error: %v", err)
	}

	if _, err := repository.Initialize(root); err == nil || !strings.Contains(err.Error(), "restore repository.key") {
		t.Fatalf("Initialize(missing key) error = %v, want restore-key error", err)
	}
	assertNotExists(t, keyPath)
}

func TestOpenBoundsAndValidatesRepositoryMetadata(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		path string
		want string
	}{
		{name: "oversized key", path: "repository.key", want: "exceeds 32 bytes"},
		{name: "oversized format", path: "format", want: "exceeds 64 bytes"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			root := t.TempDir()
			if _, err := repository.Initialize(root); err != nil {
				t.Fatalf("Initialize() returned error: %v", err)
			}
			if err := os.Truncate(filepath.Join(root, test.path), 1<<30); err != nil {
				t.Fatalf("Truncate(%s) returned error: %v", test.path, err)
			}
			if _, err := repository.Open(root); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Open() error = %v, want error containing %q", err, test.want)
			}
		})
	}

	t.Run("key must be regular", func(t *testing.T) {
		t.Parallel()

		root := t.TempDir()
		if _, err := repository.Initialize(root); err != nil {
			t.Fatalf("Initialize() returned error: %v", err)
		}
		keyPath := filepath.Join(root, "repository.key")
		if err := os.Remove(keyPath); err != nil {
			t.Fatalf("Remove(repository.key) returned error: %v", err)
		}
		if err := os.Mkdir(keyPath, 0o700); err != nil {
			t.Fatalf("Mkdir(repository.key) returned error: %v", err)
		}
		if _, err := repository.Open(root); err == nil || !strings.Contains(err.Error(), "not a regular file") {
			t.Fatalf("Open() error = %v, want regular-file error", err)
		}
	})
}

func TestCollectRemovesUnreachableAndIncompleteObjects(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	repo, err := repository.Initialize(root)
	if err != nil {
		t.Fatalf("Initialize() returned error: %v", err)
	}
	ctx := context.Background()
	createdAt := time.Date(2026, time.July, 10, 12, 0, 0, 0, time.UTC)

	var kept repository.BlockRef
	var dropped repository.BlockRef
	var snapshotID object.ID
	err = repo.Exclusive(ctx, func() error {
		var err error
		kept, _, err = repo.PutBlock(ctx, []byte("reachable"))
		if err != nil {
			return err
		}
		dropped, _, err = repo.PutBlock(ctx, []byte("unreachable"))
		if err != nil {
			return err
		}
		snapshotID, err = object.RandomID()
		if err != nil {
			return err
		}
		return repo.Commit(ctx, manifestWithBlock(snapshotID, "source", kept, createdAt))
	})
	if err != nil {
		t.Fatalf("create repository fixture: %v", err)
	}

	orphanID, err := object.RandomID()
	if err != nil {
		t.Fatalf("RandomID() returned error: %v", err)
	}
	orphan := filepath.Join(root, "manifests", orphanID.String()+".manifest")
	temporaries := []string{
		filepath.Join(root, "blocks", dropped.ID.String()[:2], ".object-block.tmp"),
		filepath.Join(root, "manifests", ".object-manifest.tmp"),
		filepath.Join(root, "commits", ".object-commit.tmp"),
	}
	for _, path := range append(temporaries, orphan) {
		if err := os.WriteFile(path, []byte("incomplete"), 0o600); err != nil {
			t.Fatalf("WriteFile(%s) returned error: %v", path, err)
		}
	}

	var removed int
	err = repo.Exclusive(ctx, func() error {
		var err error
		removed, err = repo.Collect(ctx)
		return err
	})
	if err != nil {
		t.Fatalf("Collect() returned error: %v", err)
	}
	if got, want := removed, 1; got != want {
		t.Errorf("Collect() removed %d blocks, want %d", got, want)
	}

	assertExists(t, blockPath(root, kept.ID))
	assertNotExists(t, blockPath(root, dropped.ID))
	assertExists(t, filepath.Join(root, "manifests", snapshotID.String()+".manifest"))
	assertNotExists(t, orphan)
	for _, path := range temporaries {
		assertNotExists(t, path)
	}
}

func TestRemoveDeletesCommitBeforeManifest(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	repo, err := repository.Initialize(root)
	if err != nil {
		t.Fatalf("Initialize() returned error: %v", err)
	}
	ctx := context.Background()
	id, err := object.RandomID()
	if err != nil {
		t.Fatalf("RandomID() returned error: %v", err)
	}
	createdAt := time.Date(2026, time.July, 10, 12, 0, 0, 0, time.UTC)
	manifest := manifestWithDirectory(id, "source", createdAt)
	if err := repo.Exclusive(ctx, func() error {
		if err := repo.Commit(ctx, manifest); err != nil {
			return err
		}
		return repo.Remove(id)
	}); err != nil {
		t.Fatalf("commit and remove snapshot: %v", err)
	}

	assertNotExists(t, filepath.Join(root, "commits", id.String()+".commit"))
	assertNotExists(t, filepath.Join(root, "manifests", id.String()+".manifest"))
	snapshots, err := repo.List(ctx, "")
	if err != nil {
		t.Fatalf("List() returned error: %v", err)
	}
	if len(snapshots) != 0 {
		t.Errorf("List() returned %d snapshots, want none", len(snapshots))
	}
}

func TestListUsesEncryptedCommitSummary(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	repo, err := repository.Initialize(root)
	if err != nil {
		t.Fatalf("Initialize() returned error: %v", err)
	}
	id, err := object.RandomID()
	if err != nil {
		t.Fatalf("RandomID() returned error: %v", err)
	}
	createdAt := time.Date(2026, time.July, 10, 12, 0, 0, 0, time.UTC)
	manifest := manifestWithDirectory(id, "source", createdAt)
	if err := repo.Exclusive(context.Background(), func() error {
		return repo.Commit(context.Background(), manifest)
	}); err != nil {
		t.Fatalf("Commit() returned error: %v", err)
	}

	manifestPath := filepath.Join(root, "manifests", id.String()+".manifest")
	sealed, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatalf("ReadFile(manifest) returned error: %v", err)
	}
	sealed[len(sealed)-1] ^= 0xff
	if err := os.WriteFile(manifestPath, sealed, 0o600); err != nil {
		t.Fatalf("WriteFile(tampered manifest) returned error: %v", err)
	}

	snapshots, err := repo.List(context.Background(), "test")
	if err != nil {
		t.Fatalf("List() returned error: %v", err)
	}
	if got, want := len(snapshots), 1; got != want {
		t.Fatalf("List() returned %d snapshots, want %d", got, want)
	}
	if snapshots[0].ID != id || !snapshots[0].CreatedAt.Equal(createdAt) {
		t.Errorf("List()[0] = %+v, want ID %s at %s", snapshots[0], id, createdAt)
	}
	if _, err := repo.Load(context.Background(), id); err == nil {
		t.Error("Load(tampered manifest) error = nil, want integrity error")
	}
}

func TestCommitValidatesSourceIDs(t *testing.T) {
	t.Parallel()

	repo, err := repository.Initialize(t.TempDir())
	if err != nil {
		t.Fatalf("Initialize() returned error: %v", err)
	}
	createdAt := time.Date(2026, time.July, 10, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name     string
		sourceID string
		valid    bool
	}{
		{name: "minimum", sourceID: "a", valid: true},
		{name: "digits and hyphens", sourceID: "a0-b-", valid: true},
		{name: "maximum", sourceID: strings.Repeat("a", 63), valid: true},
		{name: "empty"},
		{name: "uppercase", sourceID: "Uppercase"},
		{name: "underscore", sourceID: "has_underscore"},
		{name: "leading hyphen", sourceID: "-leading"},
		{name: "too long", sourceID: strings.Repeat("a", 64)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			id, err := object.RandomID()
			if err != nil {
				t.Fatalf("RandomID() returned error: %v", err)
			}
			err = repo.Exclusive(context.Background(), func() error {
				return repo.Commit(
					context.Background(),
					manifestWithDirectory(id, test.sourceID, createdAt),
				)
			})
			if test.valid && err != nil {
				t.Errorf("Commit() returned error for valid source ID: %v", err)
			}
			if !test.valid && (err == nil || !strings.Contains(err.Error(), "must match")) {
				t.Errorf("Commit() error = %v, want source ID validation error", err)
			}
		})
	}
}

func manifestWithBlock(
	id object.ID,
	sourceID string,
	ref repository.BlockRef,
	createdAt time.Time,
) repository.Manifest {
	digest := merkle.Leaf([]byte("file"))
	return repository.Manifest{
		Version:         1,
		ConfigurationID: testConfigurationID,
		ID:              id,
		PlanID:          "test",
		PlanName:        "Test",
		CreatedAt:       createdAt,
		Root:            digest,
		ChunkSize:       4 << 20,
		Statistics: repository.Stats{
			Files:          1,
			PlaintextBytes: int64(ref.Length),
		},
		Sources: []repository.Source{{
			ID:           sourceID,
			OriginalPath: "/source",
			Digest:       digest,
			Entries: []repository.Entry{{
				Path:        ".",
				Kind:        repository.FileEntry,
				Mode:        0o600,
				ModifiedAt:  createdAt,
				Size:        int64(ref.Length),
				Digest:      digest,
				Blocks:      []repository.BlockRef{ref},
				ChangeToken: "test-change-token",
			}},
		}},
	}
}

func manifestWithDirectory(id object.ID, sourceID string, createdAt time.Time) repository.Manifest {
	digest := merkle.Leaf([]byte("directory"))
	return repository.Manifest{
		Version:         1,
		ConfigurationID: testConfigurationID,
		ID:              id,
		PlanID:          "test",
		PlanName:        "Test",
		CreatedAt:       createdAt,
		Root:            digest,
		ChunkSize:       4 << 20,
		Statistics: repository.Stats{
			Directories: 1,
		},
		Sources: []repository.Source{{
			ID:           sourceID,
			OriginalPath: "/source",
			Digest:       digest,
			Entries: []repository.Entry{{
				Path:       ".",
				Kind:       repository.DirectoryEntry,
				Mode:       uint32(os.ModeDir | 0o700),
				ModifiedAt: createdAt,
				Digest:     digest,
			}},
		}},
	}
}

func blockPath(root string, id object.ID) string {
	name := id.String()
	return filepath.Join(root, "blocks", name[:2], name+".block")
}

func assertExists(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); err != nil {
		t.Errorf("Stat(%s) returned error: %v", path, err)
	}
}

func assertNotExists(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("Stat(%s) error = %v, want os.ErrNotExist", path, err)
	}
}

func formatShard(shard int) string {
	const hex = "0123456789abcdef"
	return string([]byte{hex[shard>>4], hex[shard&0xf]})
}
