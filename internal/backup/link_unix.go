//go:build !windows

package backup

import (
	"os"

	"github.com/mgomes/atomic/internal/repository"
)

func restoreSymlink(path, target string, _ repository.SymlinkTargetKind) error {
	return os.Symlink(target, path)
}
