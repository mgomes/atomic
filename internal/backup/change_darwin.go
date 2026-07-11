//go:build darwin

package backup

import (
	"fmt"
	"io/fs"
	"syscall"
)

func fileChangeToken(_ string, info fs.FileInfo) (string, error) {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return "", fmt.Errorf("filesystem returned change metadata of type %T", info.Sys())
	}
	return fmt.Sprintf("%x:%x:%x:%x", stat.Dev, stat.Ino, stat.Ctimespec.Sec, stat.Ctimespec.Nsec), nil
}
