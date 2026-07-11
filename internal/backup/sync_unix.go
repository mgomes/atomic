//go:build !windows

package backup

import (
	"errors"
	"os"
	"time"
)

func syncRestoreParent(path string) error {
	return syncRestoreDirectory(path)
}

func syncRestoreDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}

func finalizeRestoreDirectory(path string, mode os.FileMode, modifiedAt time.Time) (err error) {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	defer func() {
		err = errors.Join(err, directory.Close())
	}()
	if err := os.Chmod(path, mode); err != nil {
		return err
	}
	if err := os.Chtimes(path, modifiedAt, modifiedAt); err != nil {
		return err
	}
	return directory.Sync()
}
