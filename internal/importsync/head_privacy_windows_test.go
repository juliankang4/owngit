//go:build windows

package importsync

import (
	"testing"

	"golang.org/x/sys/windows"
)

// This fixture deliberately grants an outside account write access. Production
// recovery only reads descriptors and must never repair this permissive DACL.
func permitOtherRepositoryWriters(t *testing.T, path string) {
	t.Helper()
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	noErr(t, err)
	everyone, err := windows.CreateWellKnownSid(windows.WinWorldSid)
	noErr(t, err)
	var entries []windows.EXPLICIT_ACCESS
	for _, sid := range []*windows.SID{user.User.Sid, everyone} {
		entries = append(entries, windows.EXPLICIT_ACCESS{
			AccessPermissions: windows.GENERIC_ALL,
			AccessMode:        windows.GRANT_ACCESS,
			Inheritance:       windows.SUB_CONTAINERS_AND_OBJECTS_INHERIT,
			Trustee: windows.TRUSTEE{TrusteeForm: windows.TRUSTEE_IS_SID, TrusteeType: windows.TRUSTEE_IS_UNKNOWN,
				TrusteeValue: windows.TrusteeValueFromSID(sid)},
		})
	}
	acl, err := windows.ACLFromEntries(entries, nil)
	noErr(t, err)
	noErr(t, windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, acl, nil))
}

func makeRepositoryStorageSharedForTest(t *testing.T, path string) {
	permitOtherRepositoryWriters(t, path)
}

func repositoryDACL(t *testing.T, path string) string {
	t.Helper()
	descriptor, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	noErr(t, err)
	return descriptor.String()
}

func TestWindowsPermissiveDACLDoesNotBlockNormalImport(t *testing.T) {
	f := newFixture(t)
	permitOtherRepositoryWriters(t, f.manager.RepositoryRoot())
	f.commit("initial", "initial\n")
	f.mustImport(ImportInput{})
	assertNormalHEADPublication(t, f)
}

func TestWindowsRecoveryPreservesLockWithPermissiveDACL(t *testing.T) {
	for _, target := range []string{"storage-root", "repository"} {
		t.Run(target, func(t *testing.T) {
			f := killedPublicationFixture(t, "head-locked")
			path := f.manager.RepositoryRoot()
			if target == "repository" {
				path = f.destinationPath()
			}
			permitOtherRepositoryWriters(t, path)
			before := repositoryDACL(t, path)
			assertRecoveryPrivacyRefusal(t, f)
			if after := repositoryDACL(t, path); after != before {
				t.Fatal("recovery rewrote the repository DACL")
			}
		})
	}
}
