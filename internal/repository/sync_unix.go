//go:build !windows

package repository

import "github.com/mgomes/atomic/internal/fsdurable"

func syncDir(path string) error {
	return fsdurable.SyncDir(path)
}
