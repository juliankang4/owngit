//go:build windows

package repository

import (
	"context"
	"os"
	"testing"

	"golang.org/x/sys/windows"
	"owngit/internal/state"
)

func TestWindowsCreatedRepositoryHasOwnerOnlyDACL(t *testing.T) {
	manager, _, _ := newTestRepository(t)
	setInheritedWriteDACL(t, manager.RepositoryRoot())
	created, err := manager.Create(context.Background(), "private-created", "")
	noErr(t, err)
	path, err := manager.Path(created.ID)
	noErr(t, err)
	info, err := os.Stat(path)
	noErr(t, err)
	changeable, _, err := state.OthersCanChange(path, info)
	if err != nil || changeable {
		t.Fatalf("created repository changeable=%v err=%v", changeable, err)
	}
}

func setInheritedWriteDACL(t *testing.T, path string) {
	t.Helper()
	userToken, err := windows.GetCurrentProcessToken().GetTokenUser()
	noErr(t, err)
	everyone, err := windows.CreateWellKnownSid(windows.WinWorldSid)
	noErr(t, err)
	entry := func(sid *windows.SID, mask windows.ACCESS_MASK) windows.EXPLICIT_ACCESS {
		return windows.EXPLICIT_ACCESS{
			AccessPermissions: mask, AccessMode: windows.GRANT_ACCESS, Inheritance: windows.SUB_CONTAINERS_AND_OBJECTS_INHERIT,
			Trustee: windows.TRUSTEE{TrusteeForm: windows.TRUSTEE_IS_SID, TrusteeType: windows.TRUSTEE_IS_UNKNOWN, TrusteeValue: windows.TrusteeValueFromSID(sid)},
		}
	}
	acl, err := windows.ACLFromEntries([]windows.EXPLICIT_ACCESS{
		entry(userToken.User.Sid, windows.GENERIC_ALL), entry(everyone, windows.GENERIC_WRITE),
	}, nil)
	noErr(t, err)
	noErr(t, windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, acl, nil))
}
