//go:build windows

package daemon

import (
	"os"

	"golang.org/x/sys/windows"

	"github.com/mgomes/ressik/internal/winpath"
)

func openState(path string) (*os.File, error) {
	name, err := windows.UTF16PtrFromString(winpath.Extended(path))
	if err != nil {
		return nil, err
	}
	handle, err := windows.CreateFile(
		name,
		windows.GENERIC_READ,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil,
		windows.OPEN_EXISTING,
		windows.FILE_ATTRIBUTE_NORMAL,
		0,
	)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(handle), path), nil
}
