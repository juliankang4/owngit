//go:build windows

package importsync

import (
	"os"
	"testing"

	"golang.org/x/sys/windows"
	"owngit/internal/state"
)

func TestWindowsInitialImportHasOwnerOnlyDACL(t *testing.T) {
	f := newFixture(t)
	setImportInheritedWriteDACL(t, f.manager.RepositoryRoot())
	inspectedStaging := false
	f.service.afterInitialDirectoryCreated = func() {
		inspectedStaging = true
		directories := unpublishedInitialDirectories(t, f)
		require(t, len(directories) == 1, "unpublished directories=%v", directories)
		info, err := os.Stat(directories[0])
		noErr(t, err)
		changeable, _, err := state.OthersCanChange(directories[0], info)
		require(t, err == nil && !changeable,
			"import staging changeable before verification=%v err=%v", changeable, err)
	}
	f.mustImport(ImportInput{})
	path := repositoryFinalPath(t, f, "project")
	info, err := os.Stat(path)
	noErr(t, err)
	changeable, _, err := state.OthersCanChange(path, info)
	require(t, err == nil && !changeable, "imported repository changeable=%v err=%v", changeable, err)
	require(t, inspectedStaging, "import staging was not inspected before verification")
}

func setImportInheritedWriteDACL(t *testing.T, path string) {
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
