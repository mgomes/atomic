//go:build windows

package repository

import "github.com/mgomes/ressik/internal/fsdurable"

func syncDir(path string) error {
	return fsdurable.SyncDir(path)
}
