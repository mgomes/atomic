//go:build windows

package backup

import (
	"os"
	"time"
)

func syncRestoreParent(string) error {
	return nil
}

func syncRestoreDirectory(string) error {
	return nil
}

func finalizeRestoreDirectory(path string, mode os.FileMode, modifiedAt time.Time) error {
	if err := os.Chmod(path, mode); err != nil {
		return err
	}
	return os.Chtimes(path, modifiedAt, modifiedAt)
}
