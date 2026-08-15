//go:build darwin || linux

package secretfile

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"

	"golang.org/x/sys/unix"

	"github.com/mgomes/atomic/internal/fsdurable"
)

const (
	directoryMode = 0o700
	secretMode    = 0o600
)

func ensureProtectedDirectory(path string) error {
	if err := fsdurable.MkdirAll(filepath.Dir(path), directoryMode); err != nil {
		return err
	}
	err := os.Mkdir(path, directoryMode)
	switch {
	case err == nil:
		if err := os.Chmod(path, directoryMode); err != nil {
			return err
		}
		if err := fsdurable.SyncDir(filepath.Dir(path)); err != nil {
			return err
		}
	case !errors.Is(err, os.ErrExist):
		return err
	}
	return validateProtectedDirectory(path)
}

func validateProtectedDirectory(path string) error {
	if err := validatePath(path, true); err != nil {
		return err
	}
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		if errors.Is(err, unix.ELOOP) {
			return insecure(path, "is a symbolic link")
		}
		return err
	}
	file := os.NewFile(uintptr(fd), path)
	validationErr := validateInfo(file, path, true)
	return errors.Join(validationErr, file.Close())
}

func openProtectedFile(path string) (*os.File, error) {
	if err := validatePath(path, false); err != nil {
		return nil, err
	}
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if err != nil {
		if errors.Is(err, unix.ELOOP) {
			return nil, insecure(path, "is a symbolic link")
		}
		return nil, err
	}
	file := os.NewFile(uintptr(fd), path)
	if err := validateProtectedFile(file, path); err != nil {
		return nil, errors.Join(err, file.Close())
	}
	return file, nil
}

func createProtectedFile(path string) (*os.File, error) {
	fd, err := unix.Open(
		path,
		unix.O_RDWR|unix.O_CREAT|unix.O_EXCL|unix.O_CLOEXEC|unix.O_NOFOLLOW,
		secretMode,
	)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), path)
	if err := file.Chmod(secretMode); err != nil {
		return nil, errors.Join(err, file.Close(), removeTemp(path))
	}
	if err := validateProtectedFile(file, path); err != nil {
		return nil, errors.Join(err, file.Close(), removeTemp(path))
	}
	return file, nil
}

func validateProtectedFile(file *os.File, path string) error {
	return validateInfo(file, path, false)
}

func validatePath(path string, directory bool) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	return validateFileInfo(info, path, directory)
}

func validateInfo(file *os.File, path string, directory bool) error {
	info, err := file.Stat()
	if err != nil {
		return err
	}
	if err := validateFileInfo(info, path, directory); err != nil {
		return err
	}
	return validateExtendedProtection(file, path)
}

func validateFileInfo(info os.FileInfo, path string, directory bool) error {
	if directory {
		if !info.IsDir() {
			return insecure(path, "is not a directory")
		}
	} else if !info.Mode().IsRegular() {
		return insecure(path, "is not a regular file")
	}

	wantMode := os.FileMode(secretMode)
	if directory {
		wantMode = directoryMode
	}
	const protectionBits = os.ModePerm | os.ModeSetuid | os.ModeSetgid | os.ModeSticky
	if got := info.Mode() & protectionBits; got != wantMode {
		return insecure(path, fmt.Sprintf("has mode %04o, want %04o", got, wantMode))
	}

	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return insecure(path, "has no Unix ownership metadata")
	}
	if stat.Uid != uint32(os.Geteuid()) {
		return insecure(path, fmt.Sprintf("is owned by UID %d, want effective UID %d", stat.Uid, os.Geteuid()))
	}
	if !directory && stat.Nlink > 1 {
		return insecure(path, fmt.Sprintf("has %d hard links, want at most 1", stat.Nlink))
	}
	return nil
}

func syncProtectedDirectory(path string) error {
	return fsdurable.SyncDir(path)
}

func isAlreadyExists(err error) bool {
	return errors.Is(err, os.ErrExist)
}

func insecure(path, reason string) error {
	return &ProtectionError{Path: path, Reason: reason}
}
