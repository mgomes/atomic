//go:build windows

package fsdurable

// SyncDir is a best effort on Windows, where FlushFileBuffers is not
// documented for directory handles.
func SyncDir(string) error {
	return nil
}
