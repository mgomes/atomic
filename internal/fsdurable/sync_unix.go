//go:build !windows

package fsdurable

import "os"

// SyncDir flushes a directory's metadata to stable storage.
func SyncDir(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}
