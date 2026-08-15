package backup

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/mgomes/atomic/internal/object"
	"github.com/mgomes/atomic/internal/repository"
	"github.com/mgomes/atomic/merkle"
)

// Verification summarizes one complete repository integrity check.
type Verification struct {
	Snapshots  int
	Blocks     int
	BlockBytes int64
}

// Verify authenticates every committed manifest and unique block and
// recomputes every file, directory, source, and plan Merkle root.
func (e *Engine) Verify(ctx context.Context) (Verification, error) {
	var verification Verification
	err := e.repository.Exclusive(ctx, func() error {
		snapshots, err := e.repository.List(ctx, "")
		if err != nil {
			return err
		}
		blocks := make(map[object.ID]repository.BlockRef)
		for _, snapshot := range snapshots {
			manifest, err := e.repository.Load(ctx, snapshot.ID)
			if err != nil {
				return err
			}
			if err := verifyManifestTree(ctx, manifest, blocks); err != nil {
				return fmt.Errorf("verify snapshot %s: %w", snapshot.ID, err)
			}
		}

		ids := make([]object.ID, 0, len(blocks))
		for id := range blocks {
			ids = append(ids, id)
		}
		sort.Slice(ids, func(i, j int) bool { return ids[i].String() < ids[j].String() })
		for _, id := range ids {
			if _, err := e.repository.ReadBlock(ctx, blocks[id]); err != nil {
				return err
			}
			verification.BlockBytes += int64(blocks[id].Length)
		}
		verification.Snapshots = len(snapshots)
		verification.Blocks = len(ids)
		return nil
	})
	return verification, err
}

func verifyManifestTree(
	ctx context.Context,
	manifest repository.Manifest,
	blocks map[object.ID]repository.BlockRef,
) error {
	planLeaves := make([]merkle.Digest, 0, len(manifest.Sources))
	sources := append([]repository.Source(nil), manifest.Sources...)
	sort.Slice(sources, func(i, j int) bool { return sources[i].ID < sources[j].ID })
	for _, source := range sources {
		if err := ctx.Err(); err != nil {
			return err
		}
		entries := make(map[string]repository.Entry, len(source.Entries))
		children := make(map[string][]repository.Entry)
		for _, entry := range source.Entries {
			if err := ctx.Err(); err != nil {
				return err
			}
			if _, exists := entries[entry.Path]; exists {
				return fmt.Errorf("source %q has duplicate path %q", source.ID, entry.Path)
			}
			entries[entry.Path] = entry
			if entry.Path != "." {
				parent, _ := splitEntryPath(entry.Path)
				children[parent] = append(children[parent], entry)
			}
		}
		for parent := range children {
			sort.Slice(children[parent], func(i, j int) bool {
				return children[parent][i].Path < children[parent][j].Path
			})
		}
		for _, entry := range source.Entries {
			var digest merkle.Digest
			switch entry.Kind {
			case repository.FileEntry:
				var builder merkle.Builder
				for index, ref := range entry.Blocks {
					if previous, exists := blocks[ref.ID]; exists && previous.Length != ref.Length {
						return fmt.Errorf("block %s has conflicting lengths", ref.ID)
					}
					blocks[ref.ID] = ref
					builder.Add(merkle.Leaf(blockLeaf(uint64(index), ref)))
				}
				digest = builder.Digest()
			case repository.SymlinkEntry:
				digest = merkle.Leaf(symlinkLeaf(entry.LinkTarget, entry.LinkKind))
			case repository.DirectoryEntry:
				leaves := make([]merkle.Digest, 0, len(children[entry.Path]))
				for _, child := range children[entry.Path] {
					_, name := splitEntryPath(child.Path)
					leaves = append(leaves, merkle.Leaf(directoryLeaf(name, child)))
				}
				digest = merkle.Root(leaves)
			default:
				return fmt.Errorf("path %q has unknown kind %q", entry.Path, entry.Kind)
			}
			if digest != entry.Digest {
				return fmt.Errorf("path %q Merkle root is %s, want %s", entry.Path, digest, entry.Digest)
			}
		}
		root, exists := entries["."]
		if !exists {
			return fmt.Errorf("source %q has no root entry", source.ID)
		}
		if root.Digest != source.Digest {
			return fmt.Errorf("source %q root is %s, want %s", source.ID, root.Digest, source.Digest)
		}
		planLeaves = append(planLeaves, merkle.Leaf(sourceLeaf(source.ID, root)))
	}
	if root := merkle.Root(planLeaves); root != manifest.Root {
		return fmt.Errorf("plan Merkle root is %s, want %s", root, manifest.Root)
	}
	return nil
}

func splitEntryPath(path string) (string, string) {
	separator := strings.LastIndexByte(path, '/')
	if separator < 0 {
		return ".", path
	}
	return path[:separator], path[separator+1:]
}
