//go:build windows

package secretfile

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"unsafe"

	"golang.org/x/sys/windows"

	"github.com/mgomes/ressik/internal/fsdurable"
	"github.com/mgomes/ressik/internal/winpath"
)

const fileAllAccess windows.ACCESS_MASK = windows.STANDARD_RIGHTS_REQUIRED | windows.SYNCHRONIZE | 0x1ff

var replaceFileW = windows.NewLazySystemDLL("kernel32.dll").NewProc("ReplaceFileW")

func ensureProtectedDirectory(path string) error {
	if err := fsdurable.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	name, err := windows.UTF16PtrFromString(winpath.Extended(path))
	if err != nil {
		return err
	}
	attributes, err := privateSecurityAttributes(true)
	if err != nil {
		return err
	}
	err = windows.CreateDirectory(name, attributes)
	runtime.KeepAlive(attributes)
	if err != nil && !isAlreadyExists(err) {
		return err
	}
	return validateProtectedDirectory(path)
}

func validateProtectedDirectory(path string) error {
	file, err := openWindowsObject(path, true, windows.READ_CONTROL|windows.FILE_READ_ATTRIBUTES)
	if err != nil {
		return err
	}
	return file.Close()
}

func openProtectedFile(path string) (*os.File, error) {
	return openWindowsObject(path, false, windows.GENERIC_READ|windows.READ_CONTROL)
}

func createProtectedFile(path string) (*os.File, error) {
	name, err := windows.UTF16PtrFromString(winpath.Extended(path))
	if err != nil {
		return nil, err
	}
	attributes, err := privateSecurityAttributes(false)
	if err != nil {
		return nil, err
	}
	handle, err := windows.CreateFile(
		name,
		windows.GENERIC_READ|windows.GENERIC_WRITE|windows.READ_CONTROL,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		attributes,
		windows.CREATE_NEW,
		windows.FILE_ATTRIBUTE_NORMAL,
		0,
	)
	runtime.KeepAlive(attributes)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(handle), path)
	if file == nil {
		return nil, errors.Join(errors.New("wrap protected secret handle"), windows.CloseHandle(handle))
	}
	if err := validateProtectedFile(file, path); err != nil {
		return nil, errors.Join(err, file.Close(), removeTemp(path))
	}
	return file, nil
}

func validateProtectedFile(file *os.File, path string) error {
	return validateWindowsHandle(windows.Handle(file.Fd()), path, false)
}

func openWindowsObject(path string, directory bool, access uint32) (*os.File, error) {
	name, err := windows.UTF16PtrFromString(winpath.Extended(path))
	if err != nil {
		return nil, err
	}
	flags := uint32(windows.FILE_FLAG_OPEN_REPARSE_POINT)
	if directory {
		flags |= windows.FILE_FLAG_BACKUP_SEMANTICS
	}
	handle, err := windows.CreateFile(
		name,
		access,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil,
		windows.OPEN_EXISTING,
		flags,
		0,
	)
	if err != nil {
		return nil, err
	}
	if err := validateWindowsHandle(handle, path, directory); err != nil {
		return nil, errors.Join(err, windows.CloseHandle(handle))
	}
	file := os.NewFile(uintptr(handle), path)
	if file == nil {
		return nil, errors.Join(errors.New("wrap protected object handle"), windows.CloseHandle(handle))
	}
	return file, nil
}

func validateWindowsHandle(handle windows.Handle, path string, directory bool) error {
	fileType, err := windows.GetFileType(handle)
	if err != nil {
		return err
	}
	if fileType != windows.FILE_TYPE_DISK {
		return insecure(path, "is not a disk file")
	}

	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(handle, &info); err != nil {
		return err
	}
	if info.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		return insecure(path, "is a reparse point")
	}
	isDirectory := info.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY != 0
	if directory && !isDirectory {
		return insecure(path, "is not a directory")
	}
	if !directory && isDirectory {
		return insecure(path, "is not a regular file")
	}
	if !directory && info.NumberOfLinks > 1 {
		return insecure(path, fmt.Sprintf("has %d hard links, want at most 1", info.NumberOfLinks))
	}

	descriptor, err := windows.GetSecurityInfo(
		handle,
		windows.SE_FILE_OBJECT,
		windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION,
	)
	if err != nil {
		return err
	}
	if descriptor == nil {
		return insecure(path, "has no security descriptor")
	}
	control, _, err := descriptor.Control()
	if err != nil {
		return err
	}
	if control&windows.SE_DACL_PROTECTED == 0 {
		return insecure(path, "inherits access rules")
	}

	owner, _, err := descriptor.Owner()
	if err != nil {
		return err
	}
	currentSID, err := currentUserSID()
	if err != nil {
		return err
	}
	if owner == nil || !owner.Equals(currentSID) {
		return insecure(path, "is not owned by the current user")
	}

	dacl, _, err := descriptor.DACL()
	if err != nil {
		return insecure(path, "does not have a private access list")
	}
	if dacl == nil || dacl.AceCount != 1 {
		return insecure(path, "does not grant access exclusively to the current user")
	}
	var ace *windows.ACCESS_ALLOWED_ACE
	if err := windows.GetAce(dacl, 0, &ace); err != nil {
		return err
	}
	if ace == nil || ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE {
		return insecure(path, "does not have one current-user allow rule")
	}
	wantFlags := uint8(0)
	if directory {
		wantFlags = windows.OBJECT_INHERIT_ACE | windows.CONTAINER_INHERIT_ACE
	}
	if ace.Header.AceFlags != wantFlags {
		return insecure(path, "has unexpected access-rule inheritance")
	}
	aceSID := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
	if !aceSID.Equals(currentSID) {
		return insecure(path, "grants access to an identity other than the current user")
	}
	if ace.Mask != windows.GENERIC_ALL && ace.Mask&fileAllAccess != fileAllAccess {
		return insecure(path, "does not grant the current user full control")
	}

	runtime.KeepAlive(descriptor)
	runtime.KeepAlive(currentSID)
	return nil
}

func privateSecurityAttributes(directory bool) (*windows.SecurityAttributes, error) {
	sid, err := currentUserSID()
	if err != nil {
		return nil, err
	}
	inheritance := uint32(windows.NO_INHERITANCE)
	if directory {
		inheritance = windows.SUB_CONTAINERS_AND_OBJECTS_INHERIT
	}
	acl, err := windows.ACLFromEntries([]windows.EXPLICIT_ACCESS{{
		AccessPermissions: windows.GENERIC_ALL,
		AccessMode:        windows.SET_ACCESS,
		Inheritance:       inheritance,
		Trustee: windows.TRUSTEE{
			TrusteeForm:  windows.TRUSTEE_IS_SID,
			TrusteeType:  windows.TRUSTEE_IS_USER,
			TrusteeValue: windows.TrusteeValueFromSID(sid),
		},
	}}, nil)
	if err != nil {
		return nil, err
	}
	descriptor, err := windows.NewSecurityDescriptor()
	if err != nil {
		return nil, err
	}
	if err := descriptor.SetOwner(sid, false); err != nil {
		return nil, err
	}
	if err := descriptor.SetDACL(acl, true, false); err != nil {
		return nil, err
	}
	if err := descriptor.SetControl(windows.SE_DACL_PROTECTED, windows.SE_DACL_PROTECTED); err != nil {
		return nil, err
	}
	descriptor, err = descriptor.ToSelfRelative()
	runtime.KeepAlive(acl)
	runtime.KeepAlive(sid)
	if err != nil {
		return nil, err
	}
	return &windows.SecurityAttributes{
		Length:             uint32(unsafe.Sizeof(windows.SecurityAttributes{})),
		SecurityDescriptor: descriptor,
	}, nil
}

func currentUserSID() (*windows.SID, error) {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return nil, err
	}
	return user.User.Sid.Copy()
}

func syncProtectedDirectory(string) error {
	return nil
}

func publishNoReplace(source, destination string) error {
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

func publishReplace(source, destination string) error {
	replacedPath, err := windows.UTF16PtrFromString(winpath.Extended(destination))
	if err != nil {
		return &os.LinkError{Op: "replace", Old: source, New: destination, Err: err}
	}
	replacementPath, err := windows.UTF16PtrFromString(winpath.Extended(source))
	if err != nil {
		return &os.LinkError{Op: "replace", Old: source, New: destination, Err: err}
	}
	result, _, callErr := replaceFileW.Call(
		uintptr(unsafe.Pointer(replacedPath)),
		uintptr(unsafe.Pointer(replacementPath)),
		0,
		0,
		0,
		0,
	)
	runtime.KeepAlive(replacedPath)
	runtime.KeepAlive(replacementPath)
	if result == 0 {
		if callErr == windows.ERROR_SUCCESS {
			callErr = windows.ERROR_GEN_FAILURE
		}
		return &os.LinkError{Op: "replace", Old: source, New: destination, Err: callErr}
	}
	return nil
}

func isAlreadyExists(err error) bool {
	return errors.Is(err, os.ErrExist) ||
		errors.Is(err, windows.ERROR_FILE_EXISTS) ||
		errors.Is(err, windows.ERROR_ALREADY_EXISTS)
}

func insecure(path, reason string) error {
	return &ProtectionError{Path: path, Reason: reason}
}
