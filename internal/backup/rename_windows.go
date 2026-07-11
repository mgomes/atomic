//go:build windows

package backup

import (
	"os"

	"golang.org/x/sys/windows"

	"github.com/mgomes/ressik/internal/winpath"
)

func renameNoReplace(source, destination string) error {
	oldPath, err := windows.UTF16PtrFromString(winpath.Extended(source))
	if err != nil {
		return &os.LinkError{Op: "rename", Old: source, New: destination, Err: err}
	}
	newPath, err := windows.UTF16PtrFromString(winpath.Extended(destination))
	if err != nil {
		return &os.LinkError{Op: "rename", Old: source, New: destination, Err: err}
	}
	if err := windows.MoveFileEx(oldPath, newPath, windows.MOVEFILE_WRITE_THROUGH); err != nil {
		return &os.LinkError{Op: "rename", Old: source, New: destination, Err: err}
	}
	return nil
}
