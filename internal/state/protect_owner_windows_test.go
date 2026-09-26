//go:build windows

package state

import (
	"os"
	"strings"
	"testing"

	"golang.org/x/sys/windows"
)

// A folder the user owns, whose ACL gives the user Modify but not
// WRITE_OWNER, as a new folder outside the profile inherits it, can still be
// made private: its owner may always change the DACL.
func TestWindowsProtectPrivatePathWithoutWriteOwner(t *testing.T) {
	user, _, err := processIdentity()
	noErr(t, err)
	directory := t.TempDir()
	noErr(t, windows.SetNamedSecurityInfo(directory, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION, user, nil, nil, nil))
	// Modify (read, write, execute, delete), inherited, and nothing else.
	modify, err := windows.SecurityDescriptorFromString("D:P(A;OICI;0x1301bf;;;" + user.String() + ")")
	noErr(t, err)
	dacl, _, err := modify.DACL()
	noErr(t, err)
	noErr(t, windows.SetNamedSecurityInfo(directory, windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, dacl, nil))

	if err := ProtectPrivatePath(directory, true); err != nil {
		t.Fatalf("a folder the user owns without WRITE_OWNER: %v", err)
	}
	noErr(t, validateOwnerOnly(directory, user, true))
}

// A folder that belongs to someone else is still refused, and left as it
// was.
func TestWindowsProtectPrivatePathRefusesAnotherOwner(t *testing.T) {
	directory := os.Getenv("SystemRoot")
	if directory == "" {
		t.Skip("SystemRoot is not set")
	}
	before := securityDescriptorString(t, directory)
	err := ProtectPrivatePath(directory, true)
	if err == nil || !strings.Contains(err.Error(), "must be owned by the current Windows user") {
		t.Fatalf("ProtectPrivatePath(%q) = %v, want the owner refusal", directory, err)
	}
	if after := securityDescriptorString(t, directory); after != before {
		t.Fatalf("the security descriptor changed:\n%s\n%s", before, after)
	}
}

func securityDescriptorString(t *testing.T, path string) string {
	t.Helper()
	descriptor, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT,
		windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	noErr(t, err)
	return descriptor.String()
}
