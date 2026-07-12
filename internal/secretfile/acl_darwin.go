//go:build darwin

package secretfile

import (
	"fmt"
	"os"
	"runtime"
	"unsafe"

	"golang.org/x/sys/unix"
)

type darwinAttrList struct {
	bitmapCount uint16
	reserved    uint16
	common      uint32
	volume      uint32
	directory   uint32
	file        uint32
	fork        uint32
}

type darwinAttrReference struct {
	offset int32
	length uint32
}

type darwinExtendedSecurity struct {
	totalLength uint32
	reference   darwinAttrReference
}

func validateExtendedProtection(file *os.File, path string) error {
	attributes := darwinAttrList{
		bitmapCount: unix.ATTR_BIT_MAP_COUNT,
		common:      unix.ATTR_CMN_EXTENDED_SECURITY,
	}
	var result darwinExtendedSecurity
	//nolint:staticcheck // x/sys has no fgetattrlist wrapper; the handle-based syscall avoids cgo.
	_, _, errno := unix.Syscall6(
		unix.SYS_FGETATTRLIST,
		file.Fd(),
		uintptr(unsafe.Pointer(&attributes)),
		uintptr(unsafe.Pointer(&result)),
		unsafe.Sizeof(result),
		0,
		0,
	)
	runtime.KeepAlive(file)
	runtime.KeepAlive(attributes)
	if errno != 0 {
		return fmt.Errorf("extended ACL inspection for %q failed (%v): %w", path, errno, ErrUnsupported)
	}
	if result.totalLength < uint32(unsafe.Sizeof(result)) {
		return fmt.Errorf("filesystem did not report extended ACL metadata for %q: %w", path, ErrUnsupported)
	}
	if result.reference.length != 0 {
		return insecure(path, "has an extended access-control list")
	}
	return nil
}
