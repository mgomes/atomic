package backup

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/mgomes/atomic/internal/chunk"
	"github.com/mgomes/atomic/internal/config"
	"github.com/mgomes/atomic/internal/ignore"
	"github.com/mgomes/atomic/internal/object"
	"github.com/mgomes/atomic/internal/repository"
)

func TestFullLifecycle(t *testing.T) {
	if os.Getenv("ATOMIC_FULL_TESTS") != "1" {
		t.Skip("set ATOMIC_FULL_TESTS=1 to run the full lifecycle test")
	}

	ctx := context.Background()
	root := t.TempDir()
	corpus := newFullCorpus(t, filepath.Join(root, "source"))
	patterns := []string{"*.tmp", "cache"}
	matcher, err := ignore.Compile(patterns)
	if err != nil {
		t.Fatalf("ignore.Compile() returned error: %v", err)
	}
	includeAll, err := ignore.Compile(nil)
	if err != nil {
		t.Fatalf("ignore.Compile(nil) returned error: %v", err)
	}
	repo, err := repository.Initialize(filepath.Join(root, "repository"))
	if err != nil {
		t.Fatalf("repository.Initialize() returned error: %v", err)
	}
	engine, err := New(repo, patterns...)
	if err != nil {
		t.Fatalf("New() returned error: %v", err)
	}

	backupTime := time.Date(2026, time.February, 3, 4, 5, 6, 0, time.UTC)
	engine.now = func() time.Time { return backupTime }
	plan := config.Plan{
		Name:    "Full lifecycle",
		Enabled: true,
		Sources: map[string]config.Source{
			"explicit": {Path: corpus.explicit},
			"tree":     {Path: corpus.root},
		},
		Retention: config.Retention{
			KeepLast: 1,
			KeepFor:  config.Duration(36 * time.Hour),
		},
	}
	firstTree := snapshotFullTree(t, corpus.root, matcher)
	firstExplicit := snapshotFullTree(t, corpus.explicit, includeAll)

	first, err := engine.Backup(ctx, "full", plan)
	if err != nil {
		t.Fatalf("Backup(first) returned error: %v", err)
	}
	firstManifest, err := repo.Load(ctx, first.ID)
	if err != nil {
		t.Fatalf("Load(first) returned error: %v", err)
	}
	firstBlocks := fullManifestBlocks(t, firstManifest)
	firstReferences := fullManifestReferences(firstManifest)
	if got, want := first.Statistics.NewBlocks, len(firstBlocks); got != want {
		t.Errorf("Backup(first).NewBlocks = %d, want %d", got, want)
	}
	if got, want := first.Statistics.ReusedBlocks, firstReferences-len(firstBlocks); got != want {
		t.Errorf("Backup(first).ReusedBlocks = %d, want %d", got, want)
	}
	if got, want := first.Statistics.StoredBytes, fullBlockBytes(firstBlocks); got != want {
		t.Errorf("Backup(first).StoredBytes = %d, want %d", got, want)
	}

	empty := fullManifestEntry(t, firstManifest, "tree", "empty.bin")
	minus := fullManifestEntry(t, firstManifest, "tree", "boundary/minus.bin")
	exact := fullManifestEntry(t, firstManifest, "tree", "boundary/exact.bin")
	above := fullManifestEntry(t, firstManifest, "tree", "boundary/above.bin")
	one := fullManifestEntry(t, firstManifest, "tree", "one.bin")
	multi := fullManifestEntry(t, firstManifest, "tree", "multi.bin")
	duplicate := fullManifestEntry(t, firstManifest, "tree", "duplicate-multi.bin")
	if got := len(empty.Blocks); got != 0 {
		t.Errorf("empty.bin has %d blocks, want 0", got)
	}
	if diff := cmp.Diff([]uint32{chunk.DefaultSize - 1}, fullBlockLengths(minus.Blocks)); diff != "" {
		t.Fatalf("minus.bin block lengths mismatch (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff([]uint32{chunk.DefaultSize}, fullBlockLengths(exact.Blocks)); diff != "" {
		t.Fatalf("exact.bin block lengths mismatch (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff([]uint32{chunk.DefaultSize, 1}, fullBlockLengths(above.Blocks)); diff != "" {
		t.Fatalf("above.bin block lengths mismatch (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff([]uint32{1}, fullBlockLengths(one.Blocks)); diff != "" {
		t.Fatalf("one.bin block lengths mismatch (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff(
		[]uint32{chunk.DefaultSize, chunk.DefaultSize, uint32(len(corpus.blockC))},
		fullBlockLengths(multi.Blocks),
	); diff != "" {
		t.Fatalf("multi.bin block lengths mismatch (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff(multi.Blocks, duplicate.Blocks); diff != "" {
		t.Errorf("duplicate multi-file block references mismatch (-multi +duplicate):\n%s", diff)
	}
	if diff := cmp.Diff(exact.Blocks, above.Blocks[:1]); diff != "" {
		t.Errorf("exact and above first block references mismatch (-exact +above):\n%s", diff)
	}
	if diff := cmp.Diff(exact.Blocks, multi.Blocks[:1]); diff != "" {
		t.Errorf("exact and multi first block references mismatch (-exact +multi):\n%s", diff)
	}
	if diff := cmp.Diff(one.Blocks, above.Blocks[1:]); diff != "" {
		t.Errorf("one-byte and above tail references mismatch (-one +above):\n%s", diff)
	}
	keep := fullManifestEntry(t, firstManifest, "tree", "deep/nested/keep.txt")
	explicit := fullManifestEntry(t, firstManifest, "explicit", ".")
	if diff := cmp.Diff(keep.Blocks, explicit.Blocks); diff != "" {
		t.Errorf("explicit ignored root did not deduplicate (-tree +explicit):\n%s", diff)
	}
	requireFullManifestPathsAbsent(
		t,
		firstManifest,
		"tree",
		"cache",
		"cache/opaque.bin",
		"explicit.tmp",
		"ignored.tmp",
	)
	requireFullVerification(t, engine, 1, firstBlocks)

	backupTime = backupTime.Add(24 * time.Hour)
	corpus.mutate(t)
	secondTree := snapshotFullTree(t, corpus.root, matcher)
	secondExplicit := snapshotFullTree(t, corpus.explicit, includeAll)
	second, err := engine.Backup(ctx, "full", plan)
	if err != nil {
		t.Fatalf("Backup(second) returned error: %v", err)
	}
	secondManifest, err := repo.Load(ctx, second.ID)
	if err != nil {
		t.Fatalf("Load(second) returned error: %v", err)
	}
	secondBlocks := fullManifestBlocks(t, secondManifest)
	secondReferences := fullManifestReferences(secondManifest)
	newSecondBlocks := fullBlockDifference(secondBlocks, firstBlocks)
	if got, want := second.Statistics.NewBlocks, len(newSecondBlocks); got != want {
		t.Errorf("Backup(second).NewBlocks = %d, want %d", got, want)
	}
	if got, want := second.Statistics.ReusedBlocks, secondReferences-len(newSecondBlocks); got != want {
		t.Errorf("Backup(second).ReusedBlocks = %d, want %d", got, want)
	}
	if got, want := second.Statistics.StoredBytes, fullReferenceBytes(newSecondBlocks); got != want {
		t.Errorf("Backup(second).StoredBytes = %d, want %d", got, want)
	}
	if second.Root == first.Root {
		t.Errorf("Backup(second).Root = first root %s after source mutations", second.Root)
	}

	secondMulti := fullManifestEntry(t, secondManifest, "tree", "multi.bin")
	if got, want := len(secondMulti.Blocks), 3; got != want {
		t.Fatalf("second multi.bin has %d blocks, want %d", got, want)
	}
	if secondMulti.Blocks[0] != multi.Blocks[0] {
		t.Error("multi.bin first block was not reused after a middle-block change")
	}
	if secondMulti.Blocks[1] == multi.Blocks[1] {
		t.Error("multi.bin middle block was reused after its contents changed")
	}
	if secondMulti.Blocks[2] != multi.Blocks[2] {
		t.Error("multi.bin final block was not reused after a middle-block change")
	}
	renamed := fullManifestEntry(t, secondManifest, "tree", "renamed-multi.bin")
	if diff := cmp.Diff(duplicate.Blocks, renamed.Blocks); diff != "" {
		t.Errorf("renamed file block references mismatch (-before +after):\n%s", diff)
	}
	reusedAdded := fullManifestEntry(t, secondManifest, "tree", "reused-added.bin")
	if diff := cmp.Diff(multi.Blocks[1:2], reusedAdded.Blocks); diff != "" {
		t.Errorf("new file did not reuse an existing block (-want +got):\n%s", diff)
	}
	secondAbove := fullManifestEntry(t, secondManifest, "tree", "boundary/above.bin")
	secondExact := fullManifestEntry(t, secondManifest, "tree", "boundary/exact.bin")
	if diff := cmp.Diff(secondExact.Blocks, secondAbove.Blocks); diff != "" {
		t.Errorf("truncated file did not reuse the exact-size block (-exact +truncated):\n%s", diff)
	}
	if got := fullManifestEntry(t, secondManifest, "tree", "shape").Kind; got != repository.DirectoryEntry {
		t.Errorf("shape kind = %q, want %q", got, repository.DirectoryEntry)
	}
	fullManifestEntry(t, secondManifest, "tree", "shape/child.txt")
	requireFullManifestPathsAbsent(
		t,
		secondManifest,
		"tree",
		"boundary/minus.bin",
		"duplicate-multi.bin",
		"ignored.tmp",
		"cache",
	)
	snapshots, err := repo.List(ctx, "full")
	if err != nil {
		t.Fatalf("List(after second) returned error: %v", err)
	}
	if got, want := len(snapshots), 2; got != want {
		t.Fatalf("List(after second) returned %d snapshots, want %d", got, want)
	}
	if snapshots[0].ID != second.ID || snapshots[1].ID != first.ID {
		t.Errorf("List(after second) IDs = [%s, %s], want [%s, %s]", snapshots[0].ID, snapshots[1].ID, second.ID, first.ID)
	}
	requireFullVerification(t, engine, 2, mergeFullBlocks(t, firstBlocks, secondBlocks))
	restoreFullSnapshot(t, engine, first.ID, filepath.Join(root, "restore-first"), includeAll, firstTree, firstExplicit)
	restoreFullSnapshot(t, engine, second.ID, filepath.Join(root, "restore-second"), includeAll, secondTree, secondExplicit)

	backupTime = backupTime.Add(48 * time.Hour)
	third, err := engine.BackupFull(ctx, "full", plan)
	if err != nil {
		t.Fatalf("BackupFull(third) returned error: %v", err)
	}
	thirdManifest, err := repo.Load(ctx, third.ID)
	if err != nil {
		t.Fatalf("Load(third) returned error: %v", err)
	}
	thirdBlocks := fullManifestBlocks(t, thirdManifest)
	thirdReferences := fullManifestReferences(thirdManifest)
	if third.Root != second.Root {
		t.Errorf("BackupFull(third).Root = %s, want unchanged root %s", third.Root, second.Root)
	}
	if got := third.Statistics.NewBlocks; got != 0 {
		t.Errorf("BackupFull(third).NewBlocks = %d, want 0", got)
	}
	if got, want := third.Statistics.ReusedBlocks, thirdReferences; got != want {
		t.Errorf("BackupFull(third).ReusedBlocks = %d, want %d", got, want)
	}
	if got := third.Statistics.StoredBytes; got != 0 {
		t.Errorf("BackupFull(third).StoredBytes = %d, want 0", got)
	}
	if diff := cmp.Diff(secondBlocks, thirdBlocks); diff != "" {
		t.Errorf("unchanged full backup block set mismatch (-second +third):\n%s", diff)
	}

	snapshots, err = repo.List(ctx, "full")
	if err != nil {
		t.Fatalf("List(after third) returned error: %v", err)
	}
	if got, want := len(snapshots), 1; got != want {
		t.Fatalf("List(after third) returned %d snapshots, want %d", got, want)
	}
	if snapshots[0].ID != third.ID {
		t.Errorf("List(after third)[0].ID = %s, want %s", snapshots[0].ID, third.ID)
	}
	for _, expired := range []object.ID{first.ID, second.ID} {
		if _, err := repo.Load(ctx, expired); err == nil {
			t.Errorf("Load(expired %s) returned nil error", expired)
		}
	}
	historicalBlocks := mergeFullBlocks(t, firstBlocks, secondBlocks)
	collected := fullBlockDifference(historicalBlocks, thirdBlocks)
	if len(collected) == 0 {
		t.Fatal("retention did not leave any historical-only blocks to test collection")
	}
	for _, ref := range collected {
		requireFullBlockPresence(t, repo, ref, false)
	}
	for _, ref := range thirdBlocks {
		requireFullBlockPresence(t, repo, ref, true)
	}
	requireFullVerification(t, engine, 1, thirdBlocks)
	restoreFullSnapshot(t, engine, third.ID, filepath.Join(root, "restore-third"), includeAll, secondTree, secondExplicit)

	thirdMulti := fullManifestEntry(t, thirdManifest, "tree", "multi.bin")
	if got, want := len(thirdMulti.Blocks), 3; got != want {
		t.Fatalf("third multi.bin has %d blocks, want %d", got, want)
	}
	if thirdMulti.Blocks[0].ID == thirdMulti.Blocks[1].ID ||
		thirdMulti.Blocks[0].ID == thirdMulti.Blocks[2].ID ||
		thirdMulti.Blocks[1].ID == thirdMulti.Blocks[2].ID {
		t.Fatal("third multi.bin does not have three distinct block IDs")
	}
	corruptions := []struct {
		name string
		ref  repository.BlockRef
	}{
		{name: "first", ref: thirdMulti.Blocks[0]},
		{name: "middle", ref: thirdMulti.Blocks[1]},
		{name: "last", ref: thirdMulti.Blocks[2]},
	}
	for _, corruption := range corruptions {
		t.Run("corrupt_"+corruption.name+"_block", func(t *testing.T) {
			cloneRoot := cloneFullRepository(t, repo.Root())
			corruptFullBlock(t, cloneRoot, corruption.ref)
			clone, err := repository.Open(cloneRoot)
			if err != nil {
				t.Fatalf("repository.Open(clone) returned error: %v", err)
			}
			cloneEngine, err := New(clone)
			if err != nil {
				t.Fatalf("New(clone) returned error: %v", err)
			}
			if _, err := cloneEngine.Verify(ctx); err == nil {
				t.Error("Verify() returned nil error for a corrupted block")
			}
			destination := filepath.Join(filepath.Dir(cloneRoot), "restore")
			if err := cloneEngine.Restore(ctx, third.ID, RestoreOptions{Destination: destination}); err == nil {
				t.Error("Restore() returned nil error for a corrupted block")
			}
			if _, err := os.Lstat(destination); !errors.Is(err, os.ErrNotExist) {
				t.Errorf("failed restore destination error = %v, want os.ErrNotExist", err)
			}
		})
	}
}

func fullBlockLengths(refs []repository.BlockRef) []uint32 {
	lengths := make([]uint32, len(refs))
	for i, ref := range refs {
		lengths[i] = ref.Length
	}
	return lengths
}

func mergeFullBlocks(
	t *testing.T,
	sets ...map[object.ID]repository.BlockRef,
) map[object.ID]repository.BlockRef {
	t.Helper()

	merged := make(map[object.ID]repository.BlockRef)
	for _, blocks := range sets {
		for id, ref := range blocks {
			if previous, exists := merged[id]; exists && previous.Length != ref.Length {
				t.Fatalf("block %s has lengths %d and %d", id, previous.Length, ref.Length)
			}
			merged[id] = ref
		}
	}
	return merged
}

func requireFullVerification(
	t *testing.T,
	engine *Engine,
	snapshots int,
	blocks map[object.ID]repository.BlockRef,
) {
	t.Helper()

	verification, err := engine.Verify(context.Background())
	if err != nil {
		t.Fatalf("Verify() returned error: %v", err)
	}
	if got := verification.Snapshots; got != snapshots {
		t.Errorf("Verify().Snapshots = %d, want %d", got, snapshots)
	}
	if got, want := verification.Blocks, len(blocks); got != want {
		t.Errorf("Verify().Blocks = %d, want %d", got, want)
	}
	if got, want := verification.BlockBytes, fullBlockBytes(blocks); got != want {
		t.Errorf("Verify().BlockBytes = %d, want %d", got, want)
	}
}

func requireFullBlockPresence(
	t *testing.T,
	repo *repository.Repository,
	ref repository.BlockRef,
	want bool,
) {
	t.Helper()

	present, err := repo.HasBlock(context.Background(), ref)
	if err != nil {
		t.Fatalf("HasBlock(%s) returned error: %v", ref.ID, err)
	}
	if present != want {
		t.Errorf("HasBlock(%s) = %t, want %t", ref.ID, present, want)
	}
}

func restoreFullSnapshot(
	t *testing.T,
	engine *Engine,
	id object.ID,
	destination string,
	includeAll ignore.Matcher,
	wantTree map[string]treeEntry,
	wantExplicit map[string]treeEntry,
) {
	t.Helper()

	if err := engine.Restore(
		context.Background(),
		id,
		RestoreOptions{Destination: destination},
	); err != nil {
		t.Fatalf("Restore(%s) returned error: %v", id, err)
	}
	requireFullTree(t, filepath.Join(destination, "tree"), includeAll, wantTree)
	requireFullTree(t, filepath.Join(destination, "explicit"), includeAll, wantExplicit)
}
