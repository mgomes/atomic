package backup

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/mgomes/ressik/internal/object"
	"github.com/mgomes/ressik/internal/pathcheck"
	"github.com/mgomes/ressik/internal/repository"
	"github.com/mgomes/ressik/merkle"
)

// RestoreOptions controls how a snapshot is materialized.
type RestoreOptions struct {
	Destination string
}

// Restore authenticates and materializes one complete snapshot beneath the
// requested destination.
func (e *Engine) Restore(ctx context.Context, id object.ID, options RestoreOptions) error {
	destination, err := filepath.Abs(options.Destination)
	if err != nil {
		return fmt.Errorf("resolve restore destination: %w", err)
	}
	return e.repository.Exclusive(ctx, func() (workErr error) {
		manifest, err := e.repository.Load(ctx, id)
		if err != nil {
			return err
		}
		if err := verifyManifestTree(ctx, manifest, make(map[object.ID]repository.BlockRef)); err != nil {
			return fmt.Errorf("verify snapshot Merkle tree: %w", err)
		}
		if _, err := os.Lstat(destination); err == nil {
			return fmt.Errorf("restore destination already exists: %s", destination)
		} else if !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("inspect restore destination: %w", err)
		}
		parent := filepath.Dir(destination)
		parentInfo, err := os.Stat(parent)
		if err != nil {
			return fmt.Errorf("inspect restore parent: %w", err)
		}
		if !parentInfo.IsDir() {
			return fmt.Errorf("restore parent is not a directory: %s", parent)
		}
		protected, err := protectedRepositoryDirectories(e.repository.Root())
		if err != nil {
			return err
		}
		insideRepository, err := protected.Contains(parent)
		if err != nil {
			return fmt.Errorf("compare restore destination with repository: %w", err)
		}
		if insideRepository {
			return errors.New("restore destination physically overlaps the Ressik repository")
		}
		staging, err := os.MkdirTemp(parent, ".ressik-restore-*")
		if err != nil {
			return fmt.Errorf("create restore staging directory: %w", err)
		}
		defer func() {
			if staging == "" {
				return
			}
			if err := cleanupRestoreTree(staging); err != nil {
				workErr = errors.Join(workErr, fmt.Errorf("clean restore staging directory: %w", err))
			}
		}()
		workDir := filepath.Join(staging, ".ressik-work")
		if err := os.Mkdir(workDir, 0o700); err != nil {
			return fmt.Errorf("create restore work directory: %w", err)
		}
		var directories []restoredDirectory
		for _, source := range manifest.Sources {
			restored, err := e.restoreSource(ctx, staging, workDir, source)
			if err != nil {
				return fmt.Errorf("restore source %q: %w", source.ID, err)
			}
			directories = append(directories, restored...)
		}
		if err := os.Remove(workDir); err != nil {
			return fmt.Errorf("remove restore work directory: %w", err)
		}
		if err := finalizeDirectories(directories); err != nil {
			return fmt.Errorf("finalize restored directories: %w", err)
		}
		if err := syncRestoreDirectory(staging); err != nil {
			return fmt.Errorf("sync restore staging directory: %w", err)
		}
		if err := renameNoReplace(staging, destination); err != nil {
			return fmt.Errorf("publish restore: %w", err)
		}
		staging = ""
		if err := syncRestoreParent(parent); err != nil {
			return fmt.Errorf("restore published but parent sync failed: %w", err)
		}
		return nil
	})
}

func protectedRepositoryDirectories(root string) (*pathcheck.IdentitySet, error) {
	paths := []string{
		root,
		filepath.Join(root, "blocks"),
		filepath.Join(root, "manifests"),
		filepath.Join(root, "commits"),
	}
	for shard := range 256 {
		paths = append(paths, filepath.Join(root, "blocks", fmt.Sprintf("%02x", shard)))
	}
	protected, err := pathcheck.NewIdentitySet(paths)
	if err != nil {
		return nil, fmt.Errorf("record repository directory identities: %w", err)
	}
	return protected, nil
}

type restoredDirectory struct {
	path  string
	entry repository.Entry
}

func (e *Engine) restoreSource(
	ctx context.Context,
	destination string,
	workDir string,
	source repository.Source,
) ([]restoredDirectory, error) {
	root := filepath.Join(destination, source.ID)
	entries := append([]repository.Entry(nil), source.Entries...)
	var directories []restoredDirectory
	sort.SliceStable(entries, func(i, j int) bool {
		leftPriority := restorePriority(entries[i].Kind)
		rightPriority := restorePriority(entries[j].Kind)
		if leftPriority != rightPriority {
			return leftPriority < rightPriority
		}
		leftDepth := restoreDepth(entries[i].Path)
		rightDepth := restoreDepth(entries[j].Path)
		if leftDepth != rightDepth {
			return leftDepth < rightDepth
		}
		return entries[i].Kind == repository.DirectoryEntry && entries[j].Kind != repository.DirectoryEntry
	})
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		path, err := restorePath(root, entry.Path)
		if err != nil {
			return nil, err
		}
		switch entry.Kind {
		case repository.DirectoryEntry:
			if err := os.Mkdir(path, 0o700); err != nil {
				return nil, err
			}
			directories = append(directories, restoredDirectory{path: path, entry: entry})
		case repository.FileEntry:
			if err := e.restoreFile(ctx, workDir, path, entry); err != nil {
				return nil, err
			}
		case repository.SymlinkEntry:
			if err := restoreSymlink(path, entry.LinkTarget, entry.LinkKind); err != nil {
				return nil, err
			}
		default:
			return nil, fmt.Errorf("unknown entry kind %q", entry.Kind)
		}
	}
	return directories, nil
}

func finalizeDirectories(directories []restoredDirectory) error {
	sort.Slice(directories, func(i, j int) bool {
		return restoreDepth(directories[i].entry.Path) > restoreDepth(directories[j].entry.Path)
	})
	for _, directory := range directories {
		if err := finalizeRestoreDirectory(
			directory.path,
			os.FileMode(directory.entry.Mode).Perm(),
			directory.entry.ModifiedAt,
		); err != nil {
			return err
		}
	}
	return nil
}

func restoreDepth(path string) int {
	if path == "." {
		return 0
	}
	return 1 + strings.Count(path, "/")
}

func (e *Engine) restoreFile(
	ctx context.Context,
	workDir string,
	path string,
	entry repository.Entry,
) error {
	if _, err := os.Lstat(path); err == nil {
		return fmt.Errorf("restore path already exists on this filesystem: %s", path)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	parent, err := os.Stat(filepath.Dir(path))
	if err != nil {
		return err
	}
	if !parent.IsDir() {
		return fmt.Errorf("restore parent is not a directory: %s", filepath.Dir(path))
	}
	temp, err := os.CreateTemp(workDir, ".restore-*.tmp")
	if err != nil {
		return err
	}
	tempName := temp.Name()
	defer func() { _ = os.Remove(tempName) }()

	var builder merkle.Builder
	for index, ref := range entry.Blocks {
		plaintext, err := e.repository.ReadBlock(ctx, ref)
		if err != nil {
			_ = temp.Close()
			return err
		}
		if _, err := temp.Write(plaintext); err != nil {
			_ = temp.Close()
			return err
		}
		builder.Add(merkle.Leaf(blockLeaf(uint64(index), ref)))
	}
	if got := builder.Digest(); got != entry.Digest {
		_ = temp.Close()
		return fmt.Errorf("restored file %q has Merkle root %s, want %s", entry.Path, got, entry.Digest)
	}
	if err := temp.Chmod(os.FileMode(entry.Mode).Perm()); err != nil {
		_ = temp.Close()
		return err
	}
	if err := os.Chtimes(tempName, entry.ModifiedAt, entry.ModifiedAt); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Sync(); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	if err := renameNoReplace(tempName, path); err != nil {
		return err
	}
	return nil
}

func cleanupRestoreTree(root string) error {
	var chmodErr error
	walkErr := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			chmodErr = errors.Join(chmodErr, err)
			return nil
		}
		if entry.IsDir() {
			if err := os.Chmod(path, 0o700); err != nil {
				chmodErr = errors.Join(chmodErr, err)
			}
		}
		return nil
	})
	removeErr := os.RemoveAll(root)
	return errors.Join(chmodErr, walkErr, removeErr)
}

func restorePriority(kind repository.EntryKind) int {
	switch kind {
	case repository.DirectoryEntry:
		return 0
	case repository.FileEntry:
		return 1
	case repository.SymlinkEntry:
		return 2
	default:
		return 3
	}
}

func restorePath(root, relative string) (string, error) {
	path := root
	if relative != "." {
		path = filepath.Join(root, filepath.FromSlash(relative))
	}
	resolved, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(root, resolved)
	if err != nil {
		return "", err
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("restore path %q escapes destination", relative)
	}
	return resolved, nil
}
