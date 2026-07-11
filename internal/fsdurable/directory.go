package fsdurable

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// MkdirAll creates a directory tree and syncs every newly created entry into
// its parent before returning.
func MkdirAll(path string, mode os.FileMode) error {
	var missing []string
	current := filepath.Clean(path)
	for {
		info, err := os.Stat(current)
		if err == nil {
			if !info.IsDir() {
				return fmt.Errorf("path %q is not a directory", current)
			}
			break
		}
		if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		missing = append(missing, current)
		parent := filepath.Dir(current)
		if parent == current {
			return fmt.Errorf("find existing ancestor of %q", path)
		}
		current = parent
	}

	for offset := range len(missing) {
		index := len(missing) - 1 - offset
		if err := os.Mkdir(missing[index], mode); err != nil {
			if !errors.Is(err, os.ErrExist) {
				return err
			}
			info, statErr := os.Stat(missing[index])
			if statErr != nil {
				return statErr
			}
			if !info.IsDir() {
				return fmt.Errorf("path %q is not a directory", missing[index])
			}
		}
		if err := SyncDir(filepath.Dir(missing[index])); err != nil {
			return err
		}
	}
	return nil
}
