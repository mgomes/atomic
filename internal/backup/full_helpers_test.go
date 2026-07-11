package backup

import (
	"crypto/sha256"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/mgomes/ressik/internal/chunk"
	"github.com/mgomes/ressik/internal/ignore"
	"github.com/mgomes/ressik/internal/object"
	"github.com/mgomes/ressik/internal/repository"
)

type fullCorpus struct {
	root     string
	explicit string
	blockA   []byte
	blockB   []byte
	blockC   []byte
	blockD   []byte
}

type treeEntry struct {
	kind        string
	permissions uint32
	size        int64
	modifiedAt  time.Time
	digest      [sha256.Size]byte
	linkTarget  string
}

func newFullCorpus(t *testing.T, root string) fullCorpus {
	t.Helper()

	modifiedAt := time.Date(2026, time.January, 2, 3, 4, 5, 0, time.UTC)
	corpus := fullCorpus{
		root:     root,
		explicit: filepath.Join(root, "explicit.tmp"),
		blockA:   fullBlock(17, chunk.DefaultSize),
		blockB:   fullBlock(53, chunk.DefaultSize),
		blockC:   fullBlock(89, 257*1024+31),
		blockD:   fullBlock(131, chunk.DefaultSize),
	}

	writeFullFile(t, filepath.Join(root, "empty.bin"), nil, modifiedAt)
	writeFullFile(t, filepath.Join(root, "one.bin"), []byte{0xe7}, modifiedAt)
	writeFullFile(t, filepath.Join(root, "boundary", "minus.bin"), corpus.blockA[:chunk.DefaultSize-1], modifiedAt)
	writeFullFile(t, filepath.Join(root, "boundary", "exact.bin"), corpus.blockA, modifiedAt)
	writeFullFile(t, filepath.Join(root, "boundary", "above.bin"), joinFullBlocks(corpus.blockA, []byte{0xe7}), modifiedAt)
	multi := joinFullBlocks(corpus.blockA, corpus.blockB, corpus.blockC)
	writeFullFile(t, filepath.Join(root, "multi.bin"), multi, modifiedAt)
	writeFullFile(t, filepath.Join(root, "duplicate-multi.bin"), multi, modifiedAt)
	writeFullFile(t, filepath.Join(root, "deep", "nested", "keep.txt"), []byte("shared small block\n"), modifiedAt)
	writeFullFile(t, filepath.Join(root, "shape"), []byte("a file before becoming a directory\n"), modifiedAt)
	writeFullFile(t, corpus.explicit, []byte("shared small block\n"), modifiedAt)
	writeFullFile(t, filepath.Join(root, "ignored.tmp"), []byte("ignored file\n"), modifiedAt)
	writeFullFile(t, filepath.Join(root, "cache", "opaque.bin"), corpus.blockD, modifiedAt)
	if err := os.MkdirAll(filepath.Join(root, "empty-dir"), 0o750); err != nil {
		t.Fatalf("MkdirAll(empty-dir) returned error: %v", err)
	}

	return corpus
}

func (c fullCorpus) mutate(t *testing.T) {
	t.Helper()

	modifiedAt := time.Date(2026, time.January, 2, 4, 5, 6, 0, time.UTC)
	if err := os.Remove(filepath.Join(c.root, "boundary", "minus.bin")); err != nil {
		t.Fatalf("Remove(minus.bin) returned error: %v", err)
	}
	if err := os.Rename(
		filepath.Join(c.root, "duplicate-multi.bin"),
		filepath.Join(c.root, "renamed-multi.bin"),
	); err != nil {
		t.Fatalf("Rename(duplicate-multi.bin) returned error: %v", err)
	}
	writeFullFile(t, filepath.Join(c.root, "multi.bin"), joinFullBlocks(c.blockA, c.blockD, c.blockC), modifiedAt)
	writeFullFile(t, filepath.Join(c.root, "boundary", "above.bin"), c.blockA, modifiedAt)
	writeFullFile(
		t,
		filepath.Join(c.root, "deep", "nested", "keep.txt"),
		[]byte("shared small block\nwith an appended line\n"),
		modifiedAt,
	)
	if err := os.Remove(filepath.Join(c.root, "shape")); err != nil {
		t.Fatalf("Remove(shape) returned error: %v", err)
	}
	writeFullFile(t, filepath.Join(c.root, "shape", "child.txt"), []byte("directory child\n"), modifiedAt)
	writeFullFile(t, filepath.Join(c.root, "reused-added.bin"), c.blockB, modifiedAt)
	writeFullFile(t, filepath.Join(c.root, "new.bin"), []byte("new file\n"), modifiedAt)
	writeFullFile(t, filepath.Join(c.root, "ignored.tmp"), []byte("changed but still ignored\n"), modifiedAt)
	writeFullFile(t, filepath.Join(c.root, "cache", "opaque.bin"), c.blockA, modifiedAt)
}

func fullBlock(seed byte, size int) []byte {
	data := make([]byte, size)
	for i := range data {
		data[i] = byte((i*31 + int(seed)*17 + i/251) % 251)
	}
	return data
}

func joinFullBlocks(blocks ...[]byte) []byte {
	size := 0
	for _, block := range blocks {
		size += len(block)
	}
	joined := make([]byte, 0, size)
	for _, block := range blocks {
		joined = append(joined, block...)
	}
	return joined
}

func writeFullFile(t *testing.T, path string, data []byte, modifiedAt time.Time) {
	t.Helper()

	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatalf("MkdirAll(%q) returned error: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, data, 0o640); err != nil {
		t.Fatalf("WriteFile(%q) returned error: %v", path, err)
	}
	if err := os.Chtimes(path, modifiedAt, modifiedAt); err != nil {
		t.Fatalf("Chtimes(%q) returned error: %v", path, err)
	}
}

func fullManifestEntry(
	t *testing.T,
	manifest repository.Manifest,
	sourceID string,
	path string,
) repository.Entry {
	t.Helper()

	for _, source := range manifest.Sources {
		if source.ID != sourceID {
			continue
		}
		for _, entry := range source.Entries {
			if entry.Path == path {
				return entry
			}
		}
		t.Fatalf("manifest source %q does not contain %q", sourceID, path)
	}
	t.Fatalf("manifest does not contain source %q", sourceID)
	return repository.Entry{}
}

func requireFullManifestPathsAbsent(
	t *testing.T,
	manifest repository.Manifest,
	sourceID string,
	paths ...string,
) {
	t.Helper()

	for _, path := range paths {
		for _, source := range manifest.Sources {
			if source.ID != sourceID {
				continue
			}
			for _, entry := range source.Entries {
				if entry.Path == path {
					t.Errorf("manifest source %q unexpectedly contains %q", sourceID, path)
				}
			}
		}
	}
}

func fullManifestBlocks(t *testing.T, manifest repository.Manifest) map[object.ID]repository.BlockRef {
	t.Helper()

	blocks := make(map[object.ID]repository.BlockRef)
	for _, source := range manifest.Sources {
		for _, entry := range source.Entries {
			for _, ref := range entry.Blocks {
				if previous, exists := blocks[ref.ID]; exists && previous.Length != ref.Length {
					t.Fatalf("block %s has lengths %d and %d", ref.ID, previous.Length, ref.Length)
				}
				blocks[ref.ID] = ref
			}
		}
	}
	return blocks
}

func fullManifestReferences(manifest repository.Manifest) int {
	references := 0
	for _, source := range manifest.Sources {
		for _, entry := range source.Entries {
			references += len(entry.Blocks)
		}
	}
	return references
}

func fullBlockBytes(blocks map[object.ID]repository.BlockRef) int64 {
	var size int64
	for _, ref := range blocks {
		size += int64(ref.Length)
	}
	return size
}

func fullReferenceBytes(refs []repository.BlockRef) int64 {
	var size int64
	for _, ref := range refs {
		size += int64(ref.Length)
	}
	return size
}

func fullBlockDifference(
	left map[object.ID]repository.BlockRef,
	right map[object.ID]repository.BlockRef,
) []repository.BlockRef {
	var difference []repository.BlockRef
	for id, ref := range left {
		if _, exists := right[id]; !exists {
			difference = append(difference, ref)
		}
	}
	sort.Slice(difference, func(i, j int) bool {
		return difference[i].ID.String() < difference[j].ID.String()
	})
	return difference
}

func snapshotFullTree(t *testing.T, root string, matcher ignore.Matcher) map[string]treeEntry {
	t.Helper()

	entries := make(map[string]treeEntry)
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return fmt.Errorf("make %q relative to %q: %w", path, root, err)
		}
		relative = filepath.ToSlash(relative)
		if relative != "." && matcher.Match(relative) {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return fmt.Errorf("inspect %q: %w", path, err)
		}
		state := treeEntry{
			permissions: uint32(info.Mode().Perm()),
			modifiedAt:  info.ModTime().UTC(),
		}
		if runtime.GOOS == "windows" {
			state.permissions = 0
		}
		switch {
		case info.Mode().IsRegular():
			data, err := os.ReadFile(path)
			if err != nil {
				return fmt.Errorf("read %q: %w", path, err)
			}
			state.kind = "file"
			state.size = info.Size()
			state.digest = sha256.Sum256(data)
		case info.IsDir():
			state.kind = "directory"
		case info.Mode()&os.ModeSymlink != 0:
			target, err := os.Readlink(path)
			if err != nil {
				return fmt.Errorf("read link %q: %w", path, err)
			}
			state.kind = "symlink"
			state.linkTarget = target
			state.modifiedAt = time.Time{}
		default:
			return fmt.Errorf("unsupported filesystem mode %s at %q", info.Mode(), path)
		}
		entries[relative] = state
		return nil
	})
	if err != nil {
		t.Fatalf("snapshot tree %q returned error: %v", root, err)
	}
	return entries
}

func requireFullTree(t *testing.T, root string, matcher ignore.Matcher, want map[string]treeEntry) {
	t.Helper()

	got := snapshotFullTree(t, root, matcher)
	if diff := cmp.Diff(want, got, cmp.AllowUnexported(treeEntry{})); diff != "" {
		t.Errorf("restored tree %q mismatch (-want +got):\n%s", root, diff)
	}
}

func cloneFullRepository(t *testing.T, source string) string {
	t.Helper()

	destination := filepath.Join(t.TempDir(), "repository")
	err := filepath.WalkDir(source, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		if relative == "repository.lock" {
			return nil
		}
		target := filepath.Join(destination, relative)
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return os.MkdirAll(target, info.Mode().Perm())
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("repository contains unsupported mode %s at %q", info.Mode(), path)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, info.Mode().Perm())
	})
	if err != nil {
		t.Fatalf("clone repository %q returned error: %v", source, err)
	}
	return destination
}

func corruptFullBlock(t *testing.T, repositoryRoot string, ref repository.BlockRef) {
	t.Helper()

	name := ref.ID.String()
	path := filepath.Join(repositoryRoot, "blocks", name[:2], name+".block")
	file, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		t.Fatalf("OpenFile(%q) returned error: %v", path, err)
	}
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		t.Fatalf("Stat(%q) returned error: %v", path, err)
	}
	if info.Size() == 0 {
		_ = file.Close()
		t.Fatalf("block %q is empty", path)
	}
	var data [1]byte
	if _, err := file.ReadAt(data[:], info.Size()-1); err != nil {
		_ = file.Close()
		t.Fatalf("ReadAt(%q) returned error: %v", path, err)
	}
	data[0] ^= 0xff
	if _, err := file.WriteAt(data[:], info.Size()-1); err != nil {
		_ = file.Close()
		t.Fatalf("WriteAt(%q) returned error: %v", path, err)
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		t.Fatalf("Sync(%q) returned error: %v", path, err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("Close(%q) returned error: %v", path, err)
	}
}
