//go:build !linux && !darwin && !windows

package backup

import (
	"fmt"
	"io/fs"
)

func fileChangeToken(_ string, info fs.FileInfo) (string, error) {
	return fmt.Sprintf("metadata:%d:%d:%d", info.Size(), info.Mode(), info.ModTime().UnixNano()), nil
}
