//go:build linux

package secretfile

import (
	"os"

	"golang.org/x/sys/unix"
)

func publishNoReplace(source, destination string) error {
	if err := unix.Renameat2(
		unix.AT_FDCWD,
		source,
		unix.AT_FDCWD,
		destination,
		unix.RENAME_NOREPLACE,
	); err != nil {
		return &os.LinkError{Op: "rename", Old: source, New: destination, Err: err}
	}
	return nil
}

func publishReplace(source, destination string) error {
	if err := unix.Renameat2(
		unix.AT_FDCWD,
		source,
		unix.AT_FDCWD,
		destination,
		unix.RENAME_EXCHANGE,
	); err != nil {
		return &os.LinkError{Op: "exchange", Old: source, New: destination, Err: err}
	}
	return nil
}
