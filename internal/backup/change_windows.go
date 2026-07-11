//go:build windows

package backup

import (
	"fmt"
	"io/fs"
	"os"
	"unsafe"

	"golang.org/x/sys/windows"
)

type fileBasicInfo struct {
	CreationTime   int64
	LastAccessTime int64
	LastWriteTime  int64
	ChangeTime     int64
	FileAttributes uint32
	_              uint32
}

func fileChangeToken(path string, _ fs.FileInfo) (token string, err error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer func() {
		if closeErr := file.Close(); err == nil && closeErr != nil {
			err = closeErr
		}
	}()
	handle := windows.Handle(file.Fd())
	var basic fileBasicInfo
	if err := windows.GetFileInformationByHandleEx(
		handle,
		windows.FileBasicInfo,
		(*byte)(unsafe.Pointer(&basic)),
		uint32(unsafe.Sizeof(basic)),
	); err != nil {
		return "", err
	}
	var identity windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(handle, &identity); err != nil {
		return "", err
	}
	return fmt.Sprintf(
		"%08x:%08x%08x:%016x",
		identity.VolumeSerialNumber,
		identity.FileIndexHigh,
		identity.FileIndexLow,
		uint64(basic.ChangeTime),
	), nil
}
