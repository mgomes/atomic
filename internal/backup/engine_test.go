package backup_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mgomes/atomic/internal/backup"
	"github.com/mgomes/atomic/internal/cancelerr"
	"github.com/mgomes/atomic/internal/chunk"
	"github.com/mgomes/atomic/internal/config"
	"github.com/mgomes/atomic/internal/object"
	"github.com/mgomes/atomic/internal/repository"
)

func TestBackupRestoreDedupAndRetention(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	source := filepath.Join(root, "source")
	if err := os.MkdirAll(filepath.Join(source, "nested"), 0o700); err != nil {
		t.Fatalf("MkdirAll(source) returned error: %v", err)
	}
	writeTestFile(t, filepath.Join(source, "one.txt"), "shared content")
	writeTestFile(t, filepath.Join(source, "two.txt"), "shared content")
	writeTestFile(t, filepath.Join(source, "nested", "three.txt"), "unique content")

	repo, err := repository.Initialize(filepath.Join(root, "repository"))
	if err != nil {
		t.Fatalf("Initialize() returned error: %v", err)
	}
	engine, err := backup.New(repo)
	if err != nil {
		t.Fatalf("backup.New() returned error: %v", err)
	}
	plan := config.Plan{
		Name:    "Test files",
		Enabled: true,
		Sources: map[string]config.Source{"files": {Path: source}},
		Retention: config.Retention{
			KeepLast: 2,
		},
	}

	first, err := engine.Backup(context.Background(), "test", plan)
	if err != nil {
		t.Fatalf("Backup(first) returned error: %v", err)
	}
	if got, want := first.Statistics.NewBlocks, 2; got != want {
		t.Errorf("Backup(first).NewBlocks = %d, want %d", got, want)
	}
	if got, want := first.Statistics.ReusedBlocks, 1; got != want {
		t.Errorf("Backup(first).ReusedBlocks = %d, want %d", got, want)
	}

	second, err := engine.Backup(context.Background(), "test", plan)
	if err != nil {
		t.Fatalf("Backup(second) returned error: %v", err)
	}
	if got := second.Statistics.NewBlocks; got != 0 {
		t.Errorf("Backup(second).NewBlocks = %d, want 0", got)
	}
	if got, want := second.Statistics.ReusedBlocks, 3; got != want {
		t.Errorf("Backup(second).ReusedBlocks = %d, want %d", got, want)
	}
	verification, err := engine.Verify(context.Background())
	if err != nil {
		t.Fatalf("Verify() returned error: %v", err)
	}
	if got, want := verification.Snapshots, 2; got != want {
		t.Errorf("Verify().Snapshots = %d, want %d", got, want)
	}
	if got, want := verification.Blocks, 2; got != want {
		t.Errorf("Verify().Blocks = %d, want %d", got, want)
	}

	restore := filepath.Join(root, "restore")
	if err := engine.Restore(context.Background(), second.ID, backup.RestoreOptions{Destination: restore}); err != nil {
		t.Fatalf("Restore(second) returned error: %v", err)
	}
	checkTestFile(t, filepath.Join(restore, "files", "one.txt"), "shared content")
	checkTestFile(t, filepath.Join(restore, "files", "two.txt"), "shared content")
	checkTestFile(t, filepath.Join(restore, "files", "nested", "three.txt"), "unique content")

	writeTestFile(t, filepath.Join(source, "one.txt"), "changed content")
	plan.Retention.KeepLast = 1
	third, err := engine.Backup(context.Background(), "test", plan)
	if err != nil {
		t.Fatalf("Backup(third) returned error: %v", err)
	}
	snapshots, err := repo.List(context.Background(), "test")
	if err != nil {
		t.Fatalf("List() returned error: %v", err)
	}
	if got, want := len(snapshots), 1; got != want {
		t.Fatalf("List() returned %d snapshots after retention, want %d", got, want)
	}
	if snapshots[0].ID != third.ID {
		t.Errorf("List()[0].ID = %s, want third snapshot %s", snapshots[0].ID, third.ID)
	}
}

func TestBackupAppliesChangedIgnorePatterns(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	source := filepath.Join(root, "source")
	if err := os.Mkdir(source, 0o700); err != nil {
		t.Fatalf("Mkdir(source) returned error: %v", err)
	}
	writeTestFile(t, filepath.Join(source, "keep.txt"), "kept")
	writeTestFile(t, filepath.Join(source, "skip.tmp"), "ignored sometimes")
	repo, err := repository.Initialize(filepath.Join(root, "repository"))
	if err != nil {
		t.Fatalf("Initialize() returned error: %v", err)
	}
	unfiltered, err := backup.New(repo)
	if err != nil {
		t.Fatalf("backup.New(unfiltered) returned error: %v", err)
	}
	filtered, err := backup.New(repo, "*.tmp")
	if err != nil {
		t.Fatalf("backup.New(filtered) returned error: %v", err)
	}
	plan := testPlan(source, 0)

	first, err := unfiltered.Backup(context.Background(), "test", plan)
	if err != nil {
		t.Fatalf("Backup(unfiltered first) returned error: %v", err)
	}
	if !manifestContainsPath(t, repo, first.ID, "skip.tmp") {
		t.Error("Backup(unfiltered first) omitted skip.tmp")
	}

	second, err := filtered.Backup(context.Background(), "test", plan)
	if err != nil {
		t.Fatalf("Backup(filtered) returned error: %v", err)
	}
	if manifestContainsPath(t, repo, second.ID, "skip.tmp") {
		t.Error("Backup(filtered) captured skip.tmp from prior snapshot metadata")
	}
	if second.Root == first.Root {
		t.Errorf("Backup(filtered).Root = %s, want a root distinct from %s", second.Root, first.Root)
	}
	restore := filepath.Join(root, "restore-first")
	if err := filtered.Restore(context.Background(), first.ID, backup.RestoreOptions{Destination: restore}); err != nil {
		t.Fatalf("Restore(unfiltered snapshot with filtered engine) returned error: %v", err)
	}
	checkTestFile(t, filepath.Join(restore, "files", "skip.tmp"), "ignored sometimes")

	third, err := unfiltered.Backup(context.Background(), "test", plan)
	if err != nil {
		t.Fatalf("Backup(unfiltered third) returned error: %v", err)
	}
	if !manifestContainsPath(t, repo, third.ID, "skip.tmp") {
		t.Error("Backup(unfiltered third) did not recapture skip.tmp")
	}
	if third.Root != first.Root {
		t.Errorf("Backup(unfiltered third).Root = %s, want original root %s", third.Root, first.Root)
	}
}

func TestBackupCollectsNewBlocksAfterCanceledCapture(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	source := filepath.Join(root, "source")
	if err := os.WriteFile(source, bytes.Repeat([]byte("x"), 3*chunk.DefaultSize), 0o600); err != nil {
		t.Fatalf("WriteFile(source) returned error: %v", err)
	}
	repo, engine := newTestEngine(t, filepath.Join(root, "repository"))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan error, 1)
	go func() {
		_, err := engine.Backup(ctx, "test", testPlan(source, 1))
		done <- err
	}()

	for countBlockObjects(t, repo.Root()) == 0 {
		select {
		case err := <-done:
			t.Fatalf("Backup() returned %v before storing a block", err)
		default:
		}
	}
	cancel()

	err := <-done
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Backup() error = %v, want context.Canceled", err)
	}
	if !cancelerr.Only(err) {
		t.Fatalf("Backup() error = %v, want only context cancellation", err)
	}
	collectOrphans(t, repo)
	if got := countBlockObjects(t, repo.Root()); got != 0 {
		t.Errorf("Collect() after canceled Backup() left %d block objects, want 0", got)
	}
}

func TestBackupReusesPriorFileWithoutOpeningBlock(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	source := filepath.Join(root, "source")
	writeTestFile(t, source, "authenticated once")
	repo, engine := newTestEngine(t, filepath.Join(root, "repository"))
	plan := testPlan(source, 2)

	first, err := engine.Backup(context.Background(), "test", plan)
	if err != nil {
		t.Fatalf("Backup(first) returned error: %v", err)
	}
	manifest, err := repo.Load(context.Background(), first.ID)
	if err != nil {
		t.Fatalf("Load(first) returned error: %v", err)
	}
	ref := manifest.Sources[0].Entries[0].Blocks[0]
	blockPath := filepath.Join(repo.Root(), "blocks", ref.ID.String()[:2], ref.ID.String()+".block")
	sealed, err := os.ReadFile(blockPath)
	if err != nil {
		t.Fatalf("ReadFile(block) returned error: %v", err)
	}
	sealed[len(sealed)-1] ^= 0xff
	if err := os.WriteFile(blockPath, sealed, 0o600); err != nil {
		t.Fatalf("WriteFile(tampered block) returned error: %v", err)
	}

	second, err := engine.Backup(context.Background(), "test", plan)
	if err != nil {
		t.Fatalf("Backup(second) returned error: %v", err)
	}
	if got := second.Statistics.NewBlocks; got != 0 {
		t.Errorf("Backup(second).NewBlocks = %d, want 0", got)
	}
	if got, want := second.Statistics.ReusedBlocks, 1; got != want {
		t.Errorf("Backup(second).ReusedBlocks = %d, want %d", got, want)
	}
	if got, want := second.Statistics.PlaintextBytes, int64(len("authenticated once")); got != want {
		t.Errorf("Backup(second).PlaintextBytes = %d, want %d", got, want)
	}
	if second.Root != first.Root {
		t.Errorf("Backup(second).Root = %s, want %s", second.Root, first.Root)
	}
	if _, err := repo.ReadBlock(context.Background(), ref); err == nil {
		t.Error("ReadBlock(tampered) returned nil error, want authentication error")
	}
	if _, err := engine.Verify(context.Background()); err == nil {
		t.Error("Verify(tampered block) error = nil, want integrity error")
	}
	if _, err := engine.BackupFull(context.Background(), "test", plan); err == nil {
		t.Error("BackupFull(tampered block) error = nil, want integrity error")
	}
}

func TestBackupRecreatesMissingPriorBlock(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	source := filepath.Join(root, "source")
	writeTestFile(t, source, "replace missing block")
	repo, engine := newTestEngine(t, filepath.Join(root, "repository"))
	plan := testPlan(source, 2)

	first, err := engine.Backup(context.Background(), "test", plan)
	if err != nil {
		t.Fatalf("Backup(first) returned error: %v", err)
	}
	manifest, err := repo.Load(context.Background(), first.ID)
	if err != nil {
		t.Fatalf("Load(first) returned error: %v", err)
	}
	ref := manifest.Sources[0].Entries[0].Blocks[0]
	blockPath := filepath.Join(repo.Root(), "blocks", ref.ID.String()[:2], ref.ID.String()+".block")
	if err := os.Remove(blockPath); err != nil {
		t.Fatalf("Remove(block) returned error: %v", err)
	}

	second, err := engine.Backup(context.Background(), "test", plan)
	if err != nil {
		t.Fatalf("Backup(second) returned error: %v", err)
	}
	if got, want := second.Statistics.NewBlocks, 1; got != want {
		t.Errorf("Backup(second).NewBlocks = %d, want %d", got, want)
	}
	if got := second.Statistics.ReusedBlocks; got != 0 {
		t.Errorf("Backup(second).ReusedBlocks = %d, want 0", got)
	}
	if _, err := repo.ReadBlock(context.Background(), ref); err != nil {
		t.Errorf("ReadBlock(recreated) returned error: %v", err)
	}
}

func TestBackupDetectsSameSizeRewriteWithRestoredModificationTime(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	source := filepath.Join(root, "source")
	writeTestFile(t, source, "old bytes")
	before, err := os.Stat(source)
	if err != nil {
		t.Fatalf("Stat(source) returned error: %v", err)
	}
	_, engine := newTestEngine(t, filepath.Join(root, "repository"))
	plan := testPlan(source, 2)
	first, err := engine.Backup(context.Background(), "test", plan)
	if err != nil {
		t.Fatalf("Backup(first) returned error: %v", err)
	}

	time.Sleep(10 * time.Millisecond)
	writeTestFile(t, source, "new bytes")
	if err := os.Chtimes(source, before.ModTime(), before.ModTime()); err != nil {
		t.Fatalf("Chtimes(source) returned error: %v", err)
	}
	second, err := engine.Backup(context.Background(), "test", plan)
	if err != nil {
		t.Fatalf("Backup(second) returned error: %v", err)
	}
	if second.Root == first.Root {
		t.Errorf("Backup(second).Root = first root %s after same-size rewrite", second.Root)
	}
	if got, want := second.Statistics.NewBlocks, 1; got != want {
		t.Errorf("Backup(second).NewBlocks = %d, want %d", got, want)
	}
	destination := filepath.Join(root, "restore")
	if err := engine.Restore(context.Background(), second.ID, backup.RestoreOptions{Destination: destination}); err != nil {
		t.Fatalf("Restore(second) returned error: %v", err)
	}
	checkTestFile(t, filepath.Join(destination, "files"), "new bytes")
}

func TestBackupRootDistinguishesFileAndDirectorySources(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	fileSource := filepath.Join(root, "empty-file")
	writeTestFile(t, fileSource, "")
	directorySource := filepath.Join(root, "empty-directory")
	if err := os.Mkdir(directorySource, 0o700); err != nil {
		t.Fatalf("Mkdir(empty-directory) returned error: %v", err)
	}
	_, engine := newTestEngine(t, filepath.Join(root, "repository"))

	fileSnapshot, err := engine.Backup(context.Background(), "file", testPlan(fileSource, 1))
	if err != nil {
		t.Fatalf("Backup(file source) returned error: %v", err)
	}
	directorySnapshot, err := engine.Backup(context.Background(), "directory", testPlan(directorySource, 1))
	if err != nil {
		t.Fatalf("Backup(directory source) returned error: %v", err)
	}
	if fileSnapshot.Root == directorySnapshot.Root {
		t.Errorf("file and directory snapshot roots are both %s, want distinct roots", fileSnapshot.Root)
	}
}

func TestBackupRootIncludesChildModificationTime(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	source := filepath.Join(root, "source")
	if err := os.Mkdir(source, 0o700); err != nil {
		t.Fatalf("Mkdir(source) returned error: %v", err)
	}
	child := filepath.Join(source, "child")
	writeTestFile(t, child, "unchanged content")
	_, engine := newTestEngine(t, filepath.Join(root, "repository"))
	plan := testPlan(source, 2)

	first, err := engine.Backup(context.Background(), "test", plan)
	if err != nil {
		t.Fatalf("Backup(first) returned error: %v", err)
	}
	info, err := os.Stat(child)
	if err != nil {
		t.Fatalf("Stat(child) returned error: %v", err)
	}
	modified := info.ModTime().Add(2 * time.Second)
	if err := os.Chtimes(child, modified, modified); err != nil {
		t.Fatalf("Chtimes(child) returned error: %v", err)
	}
	second, err := engine.Backup(context.Background(), "test", plan)
	if err != nil {
		t.Fatalf("Backup(second) returned error: %v", err)
	}
	if second.Root == first.Root {
		t.Errorf("snapshot roots are both %s after child mtime changed, want distinct roots", second.Root)
	}
}

func TestRestoreRejectsRepositoryDestination(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	source := filepath.Join(root, "source")
	writeTestFile(t, source, "restore somewhere safe")
	repo, engine := newTestEngine(t, filepath.Join(root, "repository"))
	summary, err := engine.Backup(context.Background(), "test", testPlan(source, 1))
	if err != nil {
		t.Fatalf("Backup() returned error: %v", err)
	}

	err = engine.Restore(context.Background(), summary.ID, backup.RestoreOptions{
		Destination: filepath.Join(repo.Root(), "restored"),
	})
	if err == nil {
		t.Fatal("Restore(repository destination) error = nil, want overlap error")
	}
}

func TestRestoreDoesNotReplaceExistingDestination(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	source := filepath.Join(root, "source")
	writeTestFile(t, source, "snapshot data")
	_, engine := newTestEngine(t, filepath.Join(root, "repository"))
	summary, err := engine.Backup(context.Background(), "test", testPlan(source, 1))
	if err != nil {
		t.Fatalf("Backup() returned error: %v", err)
	}
	destination := filepath.Join(root, "restore")
	if err := os.Mkdir(destination, 0o700); err != nil {
		t.Fatalf("Mkdir(destination) returned error: %v", err)
	}
	sentinel := filepath.Join(destination, "keep")
	writeTestFile(t, sentinel, "untouched")

	err = engine.Restore(context.Background(), summary.ID, backup.RestoreOptions{Destination: destination})
	if err == nil {
		t.Fatal("Restore(existing destination) error = nil, want refusal")
	}
	checkTestFile(t, sentinel, "untouched")
}

func TestRestoreRequiresExistingParent(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	source := filepath.Join(root, "source")
	writeTestFile(t, source, "snapshot data")
	_, engine := newTestEngine(t, filepath.Join(root, "repository"))
	summary, err := engine.Backup(context.Background(), "test", testPlan(source, 1))
	if err != nil {
		t.Fatalf("Backup() returned error: %v", err)
	}
	missingParent := filepath.Join(root, "missing")
	destination := filepath.Join(missingParent, "restore")

	if err := engine.Restore(context.Background(), summary.ID, backup.RestoreOptions{Destination: destination}); err == nil {
		t.Fatal("Restore(missing parent) error = nil, want refusal")
	}
	if _, err := os.Lstat(missingParent); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Lstat(missing parent) error = %v, want not exist", err)
	}
}

func TestFailedRestoreRemovesStagingWithRestrictiveDirectoryModes(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	first := filepath.Join(root, "first")
	if err := os.Mkdir(first, 0o700); err != nil {
		t.Fatalf("Mkdir(first) returned error: %v", err)
	}
	writeTestFile(t, filepath.Join(first, "kept.txt"), "first source")
	if err := os.Chmod(first, 0o500); err != nil {
		t.Fatalf("Chmod(first) returned error: %v", err)
	}
	defer os.Chmod(first, 0o700)
	second := filepath.Join(root, "second")
	writeTestFile(t, second, "second source")
	repo, engine := newTestEngine(t, filepath.Join(root, "repository"))
	plan := config.Plan{
		Name: "Two sources",
		Sources: map[string]config.Source{
			"a-first":  {Path: first},
			"b-second": {Path: second},
		},
		Retention: config.Retention{KeepLast: 1},
	}
	summary, err := engine.Backup(context.Background(), "test", plan)
	if err != nil {
		t.Fatalf("Backup() returned error: %v", err)
	}
	manifest, err := repo.Load(context.Background(), summary.ID)
	if err != nil {
		t.Fatalf("Load() returned error: %v", err)
	}
	var ref repository.BlockRef
	for _, source := range manifest.Sources {
		if source.ID == "b-second" {
			ref = source.Entries[0].Blocks[0]
		}
	}
	blockPath := filepath.Join(repo.Root(), "blocks", ref.ID.String()[:2], ref.ID.String()+".block")
	sealed, err := os.ReadFile(blockPath)
	if err != nil {
		t.Fatalf("ReadFile(block) returned error: %v", err)
	}
	sealed[len(sealed)-1] ^= 0xff
	if err := os.WriteFile(blockPath, sealed, 0o600); err != nil {
		t.Fatalf("WriteFile(tampered block) returned error: %v", err)
	}

	destination := filepath.Join(root, "restore")
	if err := engine.Restore(context.Background(), summary.ID, backup.RestoreOptions{Destination: destination}); err == nil {
		t.Fatal("Restore(tampered block) error = nil, want integrity error")
	}
	staging, err := filepath.Glob(filepath.Join(root, ".atomic-restore-*"))
	if err != nil {
		t.Fatalf("Glob(staging) returned error: %v", err)
	}
	if len(staging) != 0 {
		t.Errorf("failed Restore() left staging paths: %v", staging)
	}
}

func TestBackupReturnsCommittedSummaryWhenRetentionFails(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	source := filepath.Join(root, "source")
	writeTestFile(t, source, "retained content")
	repo, engine := newTestEngine(t, filepath.Join(root, "repository"))
	plan := testPlan(source, 2)
	if _, err := engine.Backup(context.Background(), "test", plan); err != nil {
		t.Fatalf("Backup(first) returned error: %v", err)
	}
	invalidBlock := filepath.Join(repo.Root(), "blocks", "ff", "not-an-id.block")
	if err := os.MkdirAll(filepath.Dir(invalidBlock), 0o700); err != nil {
		t.Fatalf("MkdirAll(invalid block parent) returned error: %v", err)
	}
	if err := os.WriteFile(invalidBlock, []byte("invalid"), 0o600); err != nil {
		t.Fatalf("WriteFile(invalid block) returned error: %v", err)
	}
	plan.Retention.KeepLast = 1

	summary, err := engine.Backup(context.Background(), "test", plan)
	if err == nil {
		t.Fatal("Backup(second) returned nil error, want retention error")
	}
	if summary.ID == (object.ID{}) {
		t.Error("Backup(second).ID is zero after commit, want committed snapshot ID")
	}
	if _, err := repo.Load(context.Background(), summary.ID); err != nil {
		t.Errorf("Load(committed summary) returned error: %v", err)
	}
}

func TestRetentionPreservesOlderSnapshotsWhenCurrentBlockIsCorrupt(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	source := filepath.Join(root, "source")
	writeTestFile(t, source, "old content")
	repo, engine := newTestEngine(t, filepath.Join(root, "repository"))
	plan := testPlan(source, 3)
	first, err := engine.Backup(context.Background(), "test", plan)
	if err != nil {
		t.Fatalf("Backup(first) returned error: %v", err)
	}
	writeTestFile(t, source, "new content")
	second, err := engine.Backup(context.Background(), "test", plan)
	if err != nil {
		t.Fatalf("Backup(second) returned error: %v", err)
	}
	manifest, err := repo.Load(context.Background(), second.ID)
	if err != nil {
		t.Fatalf("Load(second) returned error: %v", err)
	}
	ref := manifest.Sources[0].Entries[0].Blocks[0]
	blockPath := filepath.Join(repo.Root(), "blocks", ref.ID.String()[:2], ref.ID.String()+".block")
	sealed, err := os.ReadFile(blockPath)
	if err != nil {
		t.Fatalf("ReadFile(block) returned error: %v", err)
	}
	sealed[len(sealed)-1] ^= 0xff
	if err := os.WriteFile(blockPath, sealed, 0o600); err != nil {
		t.Fatalf("WriteFile(tampered block) returned error: %v", err)
	}
	plan.Retention.KeepLast = 1

	third, err := engine.Backup(context.Background(), "test", plan)
	if err == nil {
		t.Fatal("Backup(third) error = nil, want retention authentication error")
	}
	if third.ID.IsZero() {
		t.Fatal("Backup(third).ID is zero, want committed snapshot")
	}
	snapshots, err := repo.List(context.Background(), "test")
	if err != nil {
		t.Fatalf("List() returned error: %v", err)
	}
	if got, want := len(snapshots), 3; got != want {
		t.Fatalf("List() returned %d snapshots, want %d preserved snapshots", got, want)
	}
	destination := filepath.Join(root, "restore-old")
	if err := engine.Restore(context.Background(), first.ID, backup.RestoreOptions{Destination: destination}); err != nil {
		t.Fatalf("Restore(first) returned error: %v", err)
	}
	checkTestFile(t, filepath.Join(destination, "files"), "old content")
}

func TestBackupCancellationReturnsPromptly(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "large-source")
	file, err := os.OpenFile(source, os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatalf("OpenFile(source) returned error: %v", err)
	}
	if err := file.Truncate(1 << 30); err != nil {
		_ = file.Close()
		t.Fatalf("Truncate(source) returned error: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("Close(source) returned error: %v", err)
	}
	repo, engine := newTestEngine(t, filepath.Join(root, "repository"))
	ctx, cancel := context.WithCancel(context.Background())
	var workers sync.WaitGroup
	defer func() {
		cancel()
		workers.Wait()
	}()
	done := make(chan error, 1)
	workers.Go(func() {
		_, err := engine.Backup(ctx, "test", testPlan(source, 0))
		done <- err
	})

	deadline := time.NewTimer(5 * time.Second)
	poll := time.NewTicker(time.Millisecond)
	defer deadline.Stop()
	defer poll.Stop()
	wroteBlock := false
	for !wroteBlock {
		select {
		case err := <-done:
			t.Fatalf("Backup() completed before cancellation: %v", err)
		case <-poll.C:
			matches, err := filepath.Glob(filepath.Join(repo.Root(), "blocks", "*", "*.block"))
			if err != nil {
				t.Fatalf("Glob(blocks) returned error: %v", err)
			}
			wroteBlock = len(matches) > 0
		case <-deadline.C:
			t.Fatal("Backup() did not publish a block within 5 seconds")
		}
	}
	cancel()

	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("Backup() error = %v, want context cancellation", err)
		}
		if !cancelerr.Only(err) {
			t.Errorf("Backup() error = %v, want only context cancellation", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Backup() did not return promptly after cancellation")
	}
}

func TestRetentionIsScopedToConfiguration(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	firstSource := filepath.Join(root, "first-source")
	secondSource := filepath.Join(root, "second-source")
	writeTestFile(t, firstSource, "first version")
	writeTestFile(t, secondSource, "other config")
	repo, engine := newTestEngine(t, filepath.Join(root, "repository"))
	firstConfig := "11111111111111111111111111111111"
	secondConfig := "22222222222222222222222222222222"
	plan := testPlan(firstSource, 1)
	if _, err := engine.BackupConfig(context.Background(), firstConfig, "documents", plan); err != nil {
		t.Fatalf("BackupConfig(first) returned error: %v", err)
	}
	if _, err := engine.BackupConfig(
		context.Background(),
		secondConfig,
		"documents",
		testPlan(secondSource, 1),
	); err != nil {
		t.Fatalf("BackupConfig(second config) returned error: %v", err)
	}
	writeTestFile(t, firstSource, "next version!")
	if _, err := engine.BackupConfig(context.Background(), firstConfig, "documents", plan); err != nil {
		t.Fatalf("BackupConfig(first retained) returned error: %v", err)
	}

	firstSnapshots, err := repo.ListConfig(context.Background(), firstConfig, "documents")
	if err != nil {
		t.Fatalf("ListConfig(first) returned error: %v", err)
	}
	secondSnapshots, err := repo.ListConfig(context.Background(), secondConfig, "documents")
	if err != nil {
		t.Fatalf("ListConfig(second) returned error: %v", err)
	}
	if got, want := len(firstSnapshots), 1; got != want {
		t.Errorf("first configuration snapshots = %d, want %d", got, want)
	}
	if got, want := len(secondSnapshots), 1; got != want {
		t.Errorf("second configuration snapshots = %d, want %d", got, want)
	}
}

func newTestEngine(t *testing.T, path string) (*repository.Repository, *backup.Engine) {
	t.Helper()
	repo, err := repository.Initialize(path)
	if err != nil {
		t.Fatalf("Initialize() returned error: %v", err)
	}
	engine, err := backup.New(repo)
	if err != nil {
		t.Fatalf("backup.New() returned error: %v", err)
	}
	return repo, engine
}

func testPlan(source string, keepLast int) config.Plan {
	return config.Plan{
		Name:      "Test files",
		Enabled:   true,
		Sources:   map[string]config.Source{"files": {Path: source}},
		Retention: config.Retention{KeepLast: keepLast},
	}
}

func collectOrphans(t *testing.T, repo *repository.Repository) {
	t.Helper()
	err := repo.Exclusive(context.Background(), func() error {
		_, err := repo.Collect(context.Background())
		return err
	})
	if err != nil {
		t.Fatalf("Collect() returned error: %v", err)
	}
}

func countBlockObjects(t *testing.T, root string) int {
	t.Helper()
	count := 0
	err := filepath.WalkDir(filepath.Join(root, "blocks"), func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".block") {
			count++
		}
		return nil
	})
	if err != nil {
		t.Fatalf("WalkDir(blocks) returned error: %v", err)
	}
	return count
}

func writeTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("WriteFile(%q) returned error: %v", path, err)
	}
}

func checkTestFile(t *testing.T, path, want string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(%q) returned error: %v", path, err)
	}
	if got := string(data); got != want {
		t.Errorf("ReadFile(%q) = %q, want %q", path, got, want)
	}
}

func manifestContainsPath(
	t *testing.T,
	repo *repository.Repository,
	id object.ID,
	path string,
) bool {
	t.Helper()
	manifest, err := repo.Load(context.Background(), id)
	if err != nil {
		t.Fatalf("Repository.Load(%s) returned error: %v", id, err)
	}
	for _, source := range manifest.Sources {
		for _, entry := range source.Entries {
			if entry.Path == path {
				return true
			}
		}
	}
	return false
}
