//go:build windows

package ctxdb

import (
	"os"
	"testing"

	"golang.org/x/sys/windows"
)

// makeDirReadOnly installs an explicit deny-write ACE for the current user on
// dir and its children. os.Chmod(0500) is a no-op for directory write access
// on Windows, so the portable fixture cannot rely on it.
func makeDirReadOnly(t *testing.T, dir string) {
	t.Helper()
	sd, err := windows.GetNamedSecurityInfo(dir, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		t.Fatal(err)
	}
	old, _, err := sd.DACL()
	if err != nil {
		t.Fatal(err)
	}
	user := os.Getenv("USERNAME")
	if user == "" {
		skipOrFail(t, "USERNAME is unset; cannot build a deny ACE")
	}
	// Deny write rights only: FILE_GENERIC_WRITE includes READ_CONTROL and
	// SYNCHRONIZE, which would also block reads once inherited.
	const (
		accessDelete    = 0x00010000 // DELETE
		fileDeleteChild = 0x00000040 // FILE_DELETE_CHILD
	)
	ea := windows.EXPLICIT_ACCESS{
		AccessPermissions: windows.FILE_WRITE_DATA | windows.FILE_APPEND_DATA |
			windows.FILE_WRITE_EA | windows.FILE_WRITE_ATTRIBUTES | fileDeleteChild | accessDelete,
		AccessMode:  windows.DENY_ACCESS,
		Inheritance: windows.SUB_CONTAINERS_AND_OBJECTS_INHERIT,
		Trustee: windows.TRUSTEE{
			TrusteeForm:  windows.TRUSTEE_IS_NAME,
			TrusteeType:  windows.TRUSTEE_IS_USER,
			TrusteeValue: windows.TrusteeValueFromString(user),
		},
	}
	acl, err := windows.ACLFromEntries([]windows.EXPLICIT_ACCESS{ea}, old)
	if err != nil {
		t.Fatal(err)
	}
	if err := windows.SetNamedSecurityInfo(dir, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION, nil, nil, acl, nil); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := windows.SetNamedSecurityInfo(dir, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION, nil, nil, old, nil); err != nil {
			t.Errorf("restore DACL: %v", err)
		}
	})
}

// makeFileReadOnly is a no-op: the deny ACE on the directory is the
// read-only-media fixture; the DB file itself stays readable.
func makeFileReadOnly(t *testing.T, _ string) { t.Helper() }
