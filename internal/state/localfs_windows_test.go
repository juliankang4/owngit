//go:build windows

package state

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"golang.org/x/sys/windows"
)

func TestWindowsNetworkPathRecognizesUNCAndFinalUNCForms(t *testing.T) {
	for _, path := range []string{`\\server\share\state`, `//server/share/state`, `\\?\UNC\server\share\state`} {
		if !windowsNetworkPath(path) {
			t.Errorf("windowsNetworkPath(%q)=false, want true", path)
		}
	}
	if windowsNetworkPath(`C:\local\state`) {
		t.Fatal("local drive path was classified as a network path")
	}
}

func TestWindowsPrivateOwnerAcceptanceIsLimitedToProcessSIDs(t *testing.T) {
	user, defaultOwner, err := processIdentity()
	noErr(t, err)
	foreign, err := windows.StringToSid("S-1-0-0")
	noErr(t, err)
	if !ownerMatchesProcess(user, user, defaultOwner) || !ownerMatchesProcess(defaultOwner, user, defaultOwner) {
		t.Fatal("current user or token owner was rejected")
	}
	if foreign.Equals(user) || foreign.Equals(defaultOwner) {
		t.Fatal("foreign-owner fixture unexpectedly belongs to the process token")
	}
	if ownerMatchesProcess(foreign, user, defaultOwner) {
		t.Fatal("an owner outside the process token was accepted")
	}
}

func TestWindowsFullFileAccessAcceptsCanonicalMasksOnly(t *testing.T) {
	if !hasFullFileAccess(fileAllAccess) || !hasFullFileAccess(windows.GENERIC_ALL) {
		t.Fatal("canonical full-access mask was rejected")
	}
	if hasFullFileAccess(fileAllAccess &^ windows.WRITE_DAC) {
		t.Fatal("partial file access was accepted as full control")
	}
}

func TestWindowsCreatePrivateFileProtectsTheHeldObject(t *testing.T) {
	directory := t.TempDir()
	ordinaryPath := filepath.Join(directory, "ordinary")
	ordinaryName, err := windows.UTF16PtrFromString(ordinaryPath)
	noErr(t, err)
	ordinaryHandle, err := windows.CreateFile(ordinaryName, windows.GENERIC_WRITE,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil, windows.CREATE_NEW, windows.FILE_ATTRIBUTE_NORMAL, 0)
	noErr(t, err)
	ordinary := os.NewFile(uintptr(ordinaryHandle), ordinaryPath)
	ordinaryMoved := ordinaryPath + ".moved"
	if err := os.Rename(ordinaryPath, ordinaryMoved); err != nil {
		_ = ordinary.Close()
		t.Fatalf("replacement positive control failed: %v", err)
	}
	noErr(t, ordinary.Close())

	path := filepath.Join(directory, "private")
	file, err := CreatePrivateFile(path)
	noErr(t, err)
	user, _, err := processIdentity()
	if err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := validateOwnerOnlyHandle(windows.Handle(file.Fd()), user, false); err != nil {
		_ = file.Close()
		t.Fatalf("creation descriptor: %v", err)
	}
	if err := ProtectPrivateHandle(file, false); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := validateOwnerOnlyHandle(windows.Handle(file.Fd()), user, false); err != nil {
		_ = file.Close()
		t.Fatalf("protected descriptor: %v", err)
	}
	if err := os.Rename(path, path+".moved"); err == nil {
		_ = file.Close()
		t.Fatal("private handle allowed path replacement")
	}
	if _, err := file.WriteString("private\n"); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	noErr(t, file.Close())
	noErr(t, ValidatePrivateFile(path))
	if duplicate, err := CreatePrivateFile(path); !os.IsExist(err) {
		if duplicate != nil {
			_ = duplicate.Close()
		}
		t.Fatalf("exclusive create error=%v, want an existing-file error", err)
	}
}

func TestWindowsStateTargetResolutionAndOwnerOnlyACL(t *testing.T) {
	directory := t.TempDir()
	resolved, err := finalWindowsPath(directory)
	noErr(t, err)
	if windowsNetworkPath(resolved) {
		t.Fatalf("local temporary directory resolved to network path %q", resolved)
	}
	noErr(t, ensureLocalStateFilesystem(directory))
	noErr(t, ProtectPrivatePath(directory, true))
	user, defaultOwner, err := processIdentity()
	noErr(t, err)
	if err := validateOwnerOnly(directory, user, true); err != nil {
		t.Fatalf("protected directory: %v", err)
	}
	privateFile := filepath.Join(directory, "private")
	noErr(t, os.WriteFile(privateFile, []byte("private"), 0o600))
	if !defaultOwner.Equals(user) {
		if err := ValidatePrivateFile(privateFile); err == nil {
			t.Fatal("validation accepted a file whose ACL is still inherited")
		}
	}
	noErr(t, ProtectPrivatePath(privateFile, false))
	noErr(t, ValidatePrivateFile(privateFile))
}

// testDescriptor builds a security descriptor with owner and a DACL that grants
// full access to each of grants.
func testDescriptor(t *testing.T, owner *windows.SID, protected bool, grants ...*windows.SID) *windows.SECURITY_DESCRIPTOR {
	t.Helper()
	entries := make([]windows.EXPLICIT_ACCESS, 0, len(grants))
	for _, sid := range grants {
		entries = append(entries, windows.EXPLICIT_ACCESS{
			AccessPermissions: fileAllAccess,
			AccessMode:        windows.GRANT_ACCESS,
			Inheritance:       windows.NO_INHERITANCE,
			Trustee: windows.TRUSTEE{
				TrusteeForm:  windows.TRUSTEE_IS_SID,
				TrusteeType:  windows.TRUSTEE_IS_UNKNOWN,
				TrusteeValue: windows.TrusteeValueFromSID(sid),
			},
		})
	}
	acl, err := windows.ACLFromEntries(entries, nil)
	noErr(t, err)
	descriptor, err := windows.NewSecurityDescriptor()
	noErr(t, err)
	noErr(t, descriptor.SetOwner(owner, false))
	noErr(t, descriptor.SetDACL(acl, true, false))
	if protected {
		noErr(t, descriptor.SetControl(windows.SE_DACL_PROTECTED, windows.SE_DACL_PROTECTED))
	}
	return descriptor
}

// A file read as a secret must grant access only to the current user. Its
// owner may be that user or the Administrators group (what an elevated shell
// assigns), but no other account, and files OwnGit protects itself keep the
// exact owner.
func TestWindowsPrivateInputRule(t *testing.T) {
	user, _, err := processIdentity()
	noErr(t, err)
	administrators, err := windows.CreateWellKnownSid(windows.WinBuiltinAdministratorsSid)
	noErr(t, err)
	users, err := windows.CreateWellKnownSid(windows.WinBuiltinUsersSid)
	noErr(t, err)
	everyone, err := windows.CreateWellKnownSid(windows.WinWorldSid)
	noErr(t, err)
	if user.Equals(administrators) || user.Equals(users) {
		t.Fatal("the current user is unexpectedly a builtin group")
	}
	for _, test := range []struct {
		name       string
		descriptor *windows.SECURITY_DESCRIPTOR
		input      bool
		protected  bool
	}{
		{"user owner, user only", testDescriptor(t, user, true, user), true, true},
		{"Administrators owner, user only", testDescriptor(t, administrators, true, user), true, false},
		{"Users owner, user only", testDescriptor(t, users, true, user), false, false},
		{"user owner, inherited entries allowed", testDescriptor(t, user, false, user), false, false},
		{"user owner, Everyone also granted", testDescriptor(t, user, true, user, everyone), false, false},
		{"Administrators owner, Administrators also granted", testDescriptor(t, administrators, true, user, administrators), false, false},
		{"Administrators owner, only Administrators granted", testDescriptor(t, administrators, true, administrators), false, false},
	} {
		err := validatePrivateInputDescriptor(test.descriptor, user)
		if (err == nil) != test.input {
			t.Errorf("%s: input validation error=%v, want accepted=%v", test.name, err, test.input)
		}
		err = validateOwnerOnlyDescriptor(test.descriptor, user, false)
		if (err == nil) != test.protected {
			t.Errorf("%s: protected-path validation error=%v, want accepted=%v", test.name, err, test.protected)
		}
	}
}

// A file written by hand and restricted the way the docs describe (inheritance
// removed and full access granted to the current user, owner left as created)
// is accepted from both an ordinary and an elevated shell. An elevated shell
// makes the Administrators group the owner of the file.
func TestWindowsHandMadePrivateFileIsAccepted(t *testing.T) {
	path := filepath.Join(t.TempDir(), "password")
	noErr(t, os.WriteFile(path, []byte("valid-password\n"), 0o600))
	if err := ValidatePrivateFile(path); err == nil {
		t.Fatal("a file with inherited access entries was accepted")
	}
	user, defaultOwner, err := processIdentity()
	noErr(t, err)
	acl, err := ownerOnlyACL(user, false)
	noErr(t, err)
	// Like icacls /inheritance:r /grant:r: the DACL changes, the owner does not.
	noErr(t, windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, acl, nil))
	descriptor, err := pathDescriptor(path)
	noErr(t, err)
	owner, _, err := descriptor.Owner()
	noErr(t, err)
	if !owner.Equals(defaultOwner) {
		t.Fatalf("owner=%s, want the token default owner %s", owner, defaultOwner)
	}
	t.Logf("file owner %s (current user %s)", owner, user)
	noErr(t, ValidatePrivateFile(path))
	file, err := os.Open(path)
	noErr(t, err)
	defer file.Close()
	noErr(t, ValidatePrivateFileHandle(file))
}

// testDescriptorWith builds a protected descriptor from explicit entries.
func testDescriptorWith(t *testing.T, owner *windows.SID, entries []windows.EXPLICIT_ACCESS) *windows.SECURITY_DESCRIPTOR {
	t.Helper()
	acl, err := windows.ACLFromEntries(entries, nil)
	noErr(t, err)
	descriptor, err := windows.NewSecurityDescriptor()
	noErr(t, err)
	noErr(t, descriptor.SetOwner(owner, false))
	noErr(t, descriptor.SetDACL(acl, true, false))
	noErr(t, descriptor.SetControl(windows.SE_DACL_PROTECTED, windows.SE_DACL_PROTECTED))
	return descriptor
}

func testEntry(sid *windows.SID, mode windows.ACCESS_MODE, mask windows.ACCESS_MASK) windows.EXPLICIT_ACCESS {
	return windows.EXPLICIT_ACCESS{
		AccessPermissions: mask, AccessMode: mode, Inheritance: windows.NO_INHERITANCE,
		Trustee: windows.TRUSTEE{TrusteeForm: windows.TRUSTEE_IS_SID, TrusteeType: windows.TRUSTEE_IS_UNKNOWN, TrusteeValue: windows.TrusteeValueFromSID(sid)},
	}
}

// A refused secret file is explained: the owner, inherited entries, the other
// accounts that can access it, or a missing or partial grant to the current
// user, with the icacls command that fixes it.
func TestWindowsNotPrivateExplainsAndFixes(t *testing.T) {
	user, _, err := processIdentity()
	noErr(t, err)
	users, err := windows.CreateWellKnownSid(windows.WinBuiltinUsersSid)
	noErr(t, err)
	everyone, err := windows.CreateWellKnownSid(windows.WinWorldSid)
	noErr(t, err)
	const path = `C:\secrets\password.txt`
	trustee := `"*` + user.String() + `"`
	grant := `icacls "` + path + `" /inheritance:r /grant:r "*` + user.String() + `:F"`
	for _, test := range []struct {
		name       string
		descriptor *windows.SECURITY_DESCRIPTOR
		problem    string
		fix        string
	}{
		{"foreign owner", testDescriptor(t, users, true, user),
			"its owner is " + accountName(users) + ", not your account or Administrators",
			`icacls "` + path + `" /setowner ` + trustee},
		{"inherited entries", testDescriptor(t, user, false, user),
			"it inherits access entries from its folder", grant},
		{"another account", testDescriptor(t, user, true, user, everyone),
			accountName(everyone) + " can also access it", grant + ` /remove "*` + everyone.String() + `"`},
		{"two other accounts and a foreign owner", testDescriptor(t, users, true, user, everyone, users),
			"its owner is " + accountName(users) + ", not your account or Administrators; " + accountName(everyone) + ", " + accountName(users) + " can also access it",
			`icacls "` + path + `" /setowner ` + trustee + "; " + grant + ` /remove "*` + everyone.String() + `" "*` + users.String() + `"`},
		{"read only", testDescriptorWith(t, user, []windows.EXPLICIT_ACCESS{testEntry(user, windows.GRANT_ACCESS, windows.GENERIC_READ)}),
			"your account does not have full control of it", grant},
		{"denied", testDescriptorWith(t, user, []windows.EXPLICIT_ACCESS{testEntry(user, windows.DENY_ACCESS, windows.FILE_WRITE_DATA), testEntry(user, windows.GRANT_ACCESS, fileAllAccess)}),
			"it denies your account some access",
			`icacls "` + path + `" /inheritance:r /remove:d ` + trustee + ` /grant:r "*` + user.String() + `:F"`},
	} {
		err := validatePrivateInput(test.descriptor, user, path)
		var notPrivate *NotPrivateError
		if !errors.As(err, &notPrivate) {
			t.Errorf("%s: err=%v, want *NotPrivateError", test.name, err)
			continue
		}
		if notPrivate.Problem != test.problem || notPrivate.Fix != test.fix {
			t.Errorf("%s:\n problem %q\n want    %q\n fix     %q\n want    %q", test.name, notPrivate.Problem, test.problem, notPrivate.Fix, test.fix)
		}
	}
	if err := validatePrivateInput(testDescriptor(t, user, true, user), user, path); err != nil {
		t.Fatalf("a private descriptor was refused: %v", err)
	}
}

// The fix OwnGit prints makes a hand-made file private: running it in
// PowerShell is enough for the file to be accepted.
func TestWindowsNotPrivateFixWorks(t *testing.T) {
	user, _, err := processIdentity()
	noErr(t, err)
	everyone, err := windows.CreateWellKnownSid(windows.WinWorldSid)
	noErr(t, err)
	directory := t.TempDir()
	inherited := filepath.Join(directory, "inherited password")
	noErr(t, os.WriteFile(inherited, []byte("valid-password\n"), 0o600))
	shared := filepath.Join(directory, "shared password")
	noErr(t, os.WriteFile(shared, []byte("valid-password\n"), 0o600))
	acl, err := windows.ACLFromEntries([]windows.EXPLICIT_ACCESS{
		testEntry(user, windows.GRANT_ACCESS, fileAllAccess),
		testEntry(everyone, windows.GRANT_ACCESS, windows.GENERIC_READ),
	}, nil)
	noErr(t, err)
	noErr(t, windows.SetNamedSecurityInfo(shared, windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, acl, nil))
	for _, path := range []string{inherited, shared} {
		err := ValidatePrivateFile(path)
		var notPrivate *NotPrivateError
		if !errors.As(err, &notPrivate) {
			t.Fatalf("%s: err=%v, want *NotPrivateError", path, err)
		}
		t.Logf("%s: %s; fix: %s", filepath.Base(path), notPrivate.Problem, notPrivate.Fix)
		output, err := exec.Command("powershell.exe", "-NoLogo", "-NoProfile", "-NonInteractive", "-Command", notPrivate.Fix).CombinedOutput()
		if err != nil {
			t.Fatalf("%s: the fix failed: %v\n%s", path, err, output)
		}
		noErr(t, ValidatePrivateFile(path))
	}
}
