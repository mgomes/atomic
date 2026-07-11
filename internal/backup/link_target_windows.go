//go:build windows

package backup

import "path/filepath"

func portableLinkTarget(target string) string {
	return filepath.ToSlash(target)
}
