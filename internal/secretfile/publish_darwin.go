//go:build darwin

package secretfile

import (
	"os"

	"golang.org/x/sys/unix"
)

func publishNoReplace(source, destination string) error {
	if err := unix.RenamexNp(source, destination, unix.RENAME_EXCL); err != nil {
		return &os.LinkError{Op: "rename", Old: source, New: destination, Err: err}
	}
	return nil
}

func publishReplace(source, destination string) error {
	if err := unix.RenamexNp(source, destination, unix.RENAME_SWAP); err != nil {
		return &os.LinkError{Op: "exchange", Old: source, New: destination, Err: err}
	}
	return nil
}
