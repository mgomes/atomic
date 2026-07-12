//go:build windows

package secretfile

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"golang.org/x/sys/windows"
)

func TestWindowsStoreRejectsAdditionalTrustee(t *testing.T) {
	store := openTestStore(t)
	if err := store.Create("archive", []byte("credential")); err != nil {
		t.Fatalf("Create() returned error: %v", err)
	}
	path := filepath.Join(store.Root(), "archive")
	grantWorldRead(t, path)

	if _, err := store.Read("archive"); !errors.Is(err, ErrInsecure) {
		t.Fatalf("Read(public ACL) error = %v, want ErrInsecure", err)
	}
}

func TestWindowsStoreRejectsReparsePoint(t *testing.T) {
	store := openTestStore(t)
	target := filepath.Join(t.TempDir(), "target")
	if err := os.WriteFile(target, []byte("credential"), 0o600); err != nil {
		t.Fatalf("WriteFile() returned error: %v", err)
	}
	if err := os.Symlink(target, filepath.Join(store.Root(), "archive")); err != nil {
		t.Skipf("creating a file symlink is unavailable: %v", err)
	}
	if _, err := store.Read("archive"); !errors.Is(err, ErrInsecure) {
		t.Fatalf("Read(reparse point) error = %v, want ErrInsecure", err)
	}
}

func grantWorldRead(t testing.TB, path string) {
	t.Helper()
	userSID, err := currentUserSID()
	if err != nil {
		t.Fatalf("currentUserSID() returned error: %v", err)
	}
	worldSID, err := windows.CreateWellKnownSid(windows.WinWorldSid)
	if err != nil {
		t.Fatalf("CreateWellKnownSid() returned error: %v", err)
	}
	acl, err := windows.ACLFromEntries([]windows.EXPLICIT_ACCESS{
		{
			AccessPermissions: windows.GENERIC_ALL,
			AccessMode:        windows.SET_ACCESS,
			Trustee: windows.TRUSTEE{
				TrusteeForm:  windows.TRUSTEE_IS_SID,
				TrusteeType:  windows.TRUSTEE_IS_USER,
				TrusteeValue: windows.TrusteeValueFromSID(userSID),
			},
		},
		{
			AccessPermissions: windows.GENERIC_READ,
			AccessMode:        windows.GRANT_ACCESS,
			Trustee: windows.TRUSTEE{
				TrusteeForm:  windows.TRUSTEE_IS_SID,
				TrusteeType:  windows.TRUSTEE_IS_WELL_KNOWN_GROUP,
				TrusteeValue: windows.TrusteeValueFromSID(worldSID),
			},
		},
	}, nil)
	if err != nil {
		t.Fatalf("ACLFromEntries() returned error: %v", err)
	}
	if err := windows.SetNamedSecurityInfo(
		path,
		windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION,
		nil,
		nil,
		acl,
		nil,
	); err != nil {
		t.Fatalf("SetNamedSecurityInfo() returned error: %v", err)
	}
	runtime.KeepAlive(userSID)
	runtime.KeepAlive(worldSID)
}
