//go:build darwin

package backup

import (
	"os"

	"golang.org/x/sys/unix"
)

func renameNoReplace(source, destination string) error {
	if err := unix.RenamexNp(source, destination, unix.RENAME_EXCL); err != nil {
		return &os.LinkError{Op: "rename", Old: source, New: destination, Err: err}
	}
	return nil
}
