package repository

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/mgomes/atomic/internal/object"
)

// Collect removes blocks that are unreachable from every committed snapshot.
// It also removes incomplete manifests and temporary object files left by
// interrupted writes. Callers must hold the exclusive repository lock.
func (r *Repository) Collect(ctx context.Context) (removed int, err error) {
	modifiedDirs := make(map[string]bool)
	defer func() {
		err = errors.Join(err, syncModifiedDirs(ctx, modifiedDirs))
	}()

	reachable, err := r.reachableBlocks(ctx)
	if err != nil {
		return 0, err
	}
	if err := r.collectBlocks(ctx, reachable, modifiedDirs, &removed); err != nil {
		return removed, fmt.Errorf("collect repository blocks: %w", err)
	}
	if err := r.collectManifests(ctx, modifiedDirs); err != nil {
		return removed, fmt.Errorf("collect repository manifests: %w", err)
	}
	if err := r.collectTemps(ctx, filepath.Join(r.root, "commits"), modifiedDirs); err != nil {
		return removed, fmt.Errorf("collect repository commit temporaries: %w", err)
	}
	return removed, nil
}

func (r *Repository) reachableBlocks(ctx context.Context) (map[object.ID]bool, error) {
	snapshots, err := r.List(ctx, "")
	if err != nil {
		return nil, err
	}
	reachable := make(map[object.ID]bool)
	for _, snapshot := range snapshots {
		manifest, err := r.Load(ctx, snapshot.ID)
		if err != nil {
			return nil, err
		}
		for _, source := range manifest.Sources {
			for _, entry := range source.Entries {
				for _, ref := range entry.Blocks {
					reachable[ref.ID] = true
				}
			}
		}
	}
	return reachable, nil
}

func (r *Repository) collectBlocks(
	ctx context.Context,
	reachable map[object.ID]bool,
	modifiedDirs map[string]bool,
	removed *int,
) error {
	root := filepath.Join(r.root, "blocks")
	return filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if isObjectTemp(entry.Name()) {
			_, err := removeRepositoryFile(path, modifiedDirs)
			return err
		}
		if !strings.HasSuffix(entry.Name(), ".block") {
			return nil
		}

		id, err := object.ParseID(strings.TrimSuffix(entry.Name(), ".block"))
		if err != nil {
			return fmt.Errorf("parse block filename %q: %w", entry.Name(), err)
		}
		if reachable[id] {
			return nil
		}
		deleted, err := removeRepositoryFile(path, modifiedDirs)
		if err != nil {
			return fmt.Errorf("remove unreferenced block %s: %w", id, err)
		}
		if deleted {
			(*removed)++
		}
		return nil
	})
}

func (r *Repository) collectManifests(ctx context.Context, modifiedDirs map[string]bool) error {
	directory := filepath.Join(r.root, "manifests")
	entries, err := os.ReadDir(directory)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		path := filepath.Join(directory, entry.Name())
		if isObjectTemp(entry.Name()) {
			if _, err := removeRepositoryFile(path, modifiedDirs); err != nil {
				return err
			}
			continue
		}
		if !strings.HasSuffix(entry.Name(), ".manifest") {
			continue
		}

		id, err := object.ParseID(strings.TrimSuffix(entry.Name(), ".manifest"))
		if err != nil {
			return fmt.Errorf("parse manifest filename %q: %w", entry.Name(), err)
		}
		if _, err := os.Stat(r.commitPath(id)); err == nil {
			continue
		} else if !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("inspect snapshot %s commit marker: %w", id, err)
		}
		if _, err := removeRepositoryFile(path, modifiedDirs); err != nil {
			return fmt.Errorf("remove orphan manifest %s: %w", id, err)
		}
	}
	return nil
}

func (r *Repository) collectTemps(ctx context.Context, directory string, modifiedDirs map[string]bool) error {
	entries, err := os.ReadDir(directory)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.IsDir() || !isObjectTemp(entry.Name()) {
			continue
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if _, err := removeRepositoryFile(filepath.Join(directory, entry.Name()), modifiedDirs); err != nil {
			return err
		}
	}
	return nil
}

func removeRepositoryFile(path string, modifiedDirs map[string]bool) (bool, error) {
	if err := os.Remove(path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, err
	}
	modifiedDirs[filepath.Dir(path)] = true
	return true, nil
}

func syncModifiedDirs(ctx context.Context, modifiedDirs map[string]bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	directories := make([]string, 0, len(modifiedDirs))
	for directory := range modifiedDirs {
		directories = append(directories, directory)
	}
	sort.Strings(directories)
	var syncErr error
	for _, directory := range directories {
		if err := ctx.Err(); err != nil {
			return errors.Join(syncErr, err)
		}
		if err := syncDir(directory); err != nil {
			syncErr = errors.Join(
				syncErr,
				fmt.Errorf("sync collected object directory %q: %w", directory, err),
			)
		}
	}
	return syncErr
}

func isObjectTemp(name string) bool {
	return strings.HasPrefix(name, ".object-") &&
		strings.HasSuffix(name, ".tmp") &&
		len(name) > len(".object-.tmp")
}
