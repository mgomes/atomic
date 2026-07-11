//go:build windows

package backup

import (
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/sys/windows"

	"github.com/mgomes/ressik/internal/repository"
	"github.com/mgomes/ressik/internal/winpath"
)

const allowUnprivilegedSymlink = 0x2

func restoreSymlink(path, target string, kind repository.SymlinkTargetKind) error {
	var flags uint32
	switch kind {
	case repository.FileSymlinkTarget:
	case repository.DirectorySymlinkTarget:
		flags = windows.SYMBOLIC_LINK_FLAG_DIRECTORY
	case repository.UnknownSymlinkTarget:
		return fmt.Errorf("restore symlink %q: target kind is unknown on Windows", path)
	default:
		return fmt.Errorf("restore symlink %q: invalid target kind %q", path, kind)
	}
	newName, err := windows.UTF16PtrFromString(winpath.Extended(path))
	if err != nil {
		return &os.LinkError{Op: "symlink", Old: target, New: path, Err: err}
	}
	targetPath := filepath.FromSlash(target)
	if filepath.IsAbs(targetPath) {
		targetPath = winpath.Extended(targetPath)
	}
	targetName, err := windows.UTF16PtrFromString(targetPath)
	if err != nil {
		return &os.LinkError{Op: "symlink", Old: target, New: path, Err: err}
	}
	err = windows.CreateSymbolicLink(newName, targetName, flags|allowUnprivilegedSymlink)
	if err != nil {
		err = windows.CreateSymbolicLink(newName, targetName, flags)
	}
	if err != nil {
		return &os.LinkError{Op: "symlink", Old: target, New: path, Err: err}
	}
	return nil
}
