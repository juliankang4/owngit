//go:build windows

package state

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/windows"

	"owngit/internal/testfixture"
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
			t.Fatal("strict validation accepted a file still owned by the token default owner")
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
// is accepted as input from both an ordinary and an elevated shell. An
// elevated shell makes the Administrators group the owner of the file, which
// the check for OwnGit's own files still refuses.
func TestWindowsHandMadePrivateFileIsAccepted(t *testing.T) {
	path := filepath.Join(t.TempDir(), "password")
	noErr(t, os.WriteFile(path, []byte("valid-password\n"), 0o600))
	if err := ValidatePrivateInputFile(path); err == nil {
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
	noErr(t, ValidatePrivateInputFile(path))
	file, err := os.Open(path)
	noErr(t, err)
	defer file.Close()
	strict := map[string]error{"path": ValidatePrivateFile(path), "handle": ValidatePrivateFileHandle(file)}
	for source, err := range strict {
		if owner.Equals(user) && err != nil {
			t.Errorf("own-file check by %s refused a file owned by the current user: %v", source, err)
		}
		if !owner.Equals(user) && err == nil {
			t.Errorf("own-file check by %s accepted a file owned by %s", source, owner)
		}
	}
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
	const path = `C:\secrets\it's.txt`
	setOwner := `icacls 'C:\secrets\it''s.txt' /setowner '*` + user.String() + `'`
	replace := `$f = Get-Item -LiteralPath 'C:\secrets\it''s.txt' -ErrorAction Stop; $io = if ($PSVersionTable.PSEdition -eq 'Core') { [IO.FileSystemAclExtensions] } else { [IO.File] }; ` +
		`$acl = $io::GetAccessControl($f, 'Access'); $acl.SetSecurityDescriptorSddlForm('D:P(A;;FA;;;` + user.String() + `)', 'Access'); $io::SetAccessControl($f, $acl)`
	for _, test := range []struct {
		name       string
		descriptor *windows.SECURITY_DESCRIPTOR
		problem    string
		fix        string
	}{
		{"foreign owner", testDescriptor(t, users, true, user),
			"its owner is " + accountName(users) + ", not your account or Administrators", setOwner},
		{"inherited entries", testDescriptor(t, user, false, user),
			"it inherits access entries from its folder", replace},
		{"another account", testDescriptor(t, user, true, user, everyone),
			accountName(everyone) + " can also access it", replace},
		{"two other accounts and a foreign owner", testDescriptor(t, users, true, user, everyone, users),
			"its owner is " + accountName(users) + ", not your account or Administrators; " + accountName(everyone) + ", " + accountName(users) + " can also access it",
			setOwner + "; " + replace},
		{"read only", testDescriptorWith(t, user, []windows.EXPLICIT_ACCESS{testEntry(user, windows.GRANT_ACCESS, windows.GENERIC_READ)}),
			"your account does not have full control of it", replace},
		{"denied", testDescriptorWith(t, user, []windows.EXPLICIT_ACCESS{testEntry(user, windows.DENY_ACCESS, windows.FILE_WRITE_DATA), testEntry(user, windows.GRANT_ACCESS, fileAllAccess)}),
			"it denies your account some access", replace},
		{"another account denied", testDescriptorWith(t, user, []windows.EXPLICIT_ACCESS{testEntry(everyone, windows.DENY_ACCESS, windows.FILE_WRITE_DATA), testEntry(user, windows.GRANT_ACCESS, fileAllAccess)}),
			"its access list also has deny entries for " + accountName(everyone), replace},
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

// setRawDACL stores entries as the DACL of path exactly as given, including
// entries marked as inherited in a protected list, which icacls does not
// remove reliably. The entries are full-access grants; inherited marks the
// ones after the first as inherited.
func setRawDACL(t *testing.T, path string, protected bool, entries []windows.EXPLICIT_ACCESS, inherited bool) {
	t.Helper()
	acl, err := windows.ACLFromEntries(entries, nil)
	noErr(t, err)
	if inherited {
		for index := uint32(1); index < uint32(acl.AceCount); index++ {
			var ace *windows.ACCESS_ALLOWED_ACE
			noErr(t, windows.GetAce(acl, index, &ace))
			ace.Header.AceFlags |= windows.INHERITED_ACE
		}
	}
	descriptor, err := windows.NewSecurityDescriptor()
	noErr(t, err)
	noErr(t, descriptor.SetDACL(acl, true, false))
	control := windows.SECURITY_DESCRIPTOR_CONTROL(windows.SE_DACL_AUTO_INHERITED)
	if protected {
		control |= windows.SE_DACL_PROTECTED
	}
	noErr(t, descriptor.SetControl(windows.SE_DACL_PROTECTED|windows.SE_DACL_AUTO_INHERITED, control))
	name, err := windows.UTF16PtrFromString(path)
	noErr(t, err)
	handle, err := windows.CreateFile(name, windows.WRITE_DAC|windows.READ_CONTROL,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil, windows.OPEN_EXISTING, 0, 0)
	noErr(t, err)
	defer windows.CloseHandle(handle)
	noErr(t, windows.SetKernelObjectSecurity(handle, windows.DACL_SECURITY_INFORMATION, descriptor))
}

// The fix OwnGit prints makes a hand-made file private: running it in
// PowerShell is enough for the file to be accepted, whatever entries the
// file had. Folder names with PowerShell syntax stay text: the fix neither
// runs them nor changes another file.
func TestWindowsNotPrivateFixWorks(t *testing.T) {
	user, _, err := processIdentity()
	noErr(t, err)
	everyone, err := windows.CreateWellKnownSid(windows.WinWorldSid)
	noErr(t, err)
	system, err := windows.CreateWellKnownSid(windows.WinLocalSystemSid)
	noErr(t, err)
	administrators, err := windows.CreateWellKnownSid(windows.WinBuiltinAdministratorsSid)
	noErr(t, err)
	testfixture.ForEachPowerShell(t, func(t *testing.T, shell string) {
		directory := t.TempDir()
		workingDirectory := t.TempDir()
		file := func(name string) string {
			t.Helper()
			path := filepath.Join(directory, name, "password")
			noErr(t, os.MkdirAll(filepath.Dir(path), 0o700))
			noErr(t, os.WriteFile(path, []byte("valid-password\n"), 0o600))
			return path
		}
		// Files that inherit their folder's entries, in folders whose names are
		// PowerShell syntax.
		var paths []string
		for _, folder := range []string{"plain folder", "x$(ni INJ-subexpr)y", "y$HOMEz", "back`tick", "it's", "curly\u2019s", "$(ni INJ-second)"} {
			paths = append(paths, file(folder))
		}
		full := func(sid *windows.SID) windows.EXPLICIT_ACCESS {
			return testEntry(sid, windows.GRANT_ACCESS, fileAllAccess)
		}
		// Explicit entries for another account.
		shared := file("shared")
		setRawDACL(t, shared, true, []windows.EXPLICIT_ACCESS{full(user), testEntry(everyone, windows.GRANT_ACCESS, windows.GENERIC_READ)}, false)
		// A protected list that still holds entries marked as inherited, the
		// state that icacls /inheritance:r left on a Windows Server 2025 runner.
		leftover := file("inherited leftovers")
		setRawDACL(t, leftover, true, []windows.EXPLICIT_ACCESS{full(user), full(system), full(administrators)}, true)
		// Deny entries for the current user and for another account.
		denied := file("denied")
		setRawDACL(t, denied, true, []windows.EXPLICIT_ACCESS{testEntry(user, windows.DENY_ACCESS, windows.FILE_WRITE_EA), testEntry(everyone, windows.DENY_ACCESS, windows.FILE_WRITE_EA), full(user)}, false)
		paths = append(paths, shared, leftover, denied)
		for _, path := range paths {
			err := ValidatePrivateInputFile(path)
			var notPrivate *NotPrivateError
			if !errors.As(err, &notPrivate) {
				t.Fatalf("%s: err=%v, want *NotPrivateError", path, err)
			}
			if notPrivate.Shell != "PowerShell" {
				t.Errorf("%s: fix shell %q", path, notPrivate.Shell)
			}
			t.Logf("%s: %s; fix: %s", path, notPrivate.Problem, notPrivate.Fix)
			command := exec.Command(shell, "-NoLogo", "-NoProfile", "-NonInteractive", "-Command", notPrivate.Fix)
			command.Dir = workingDirectory
			output, err := command.CombinedOutput()
			if err != nil {
				listing, _ := exec.Command("icacls", path).CombinedOutput()
				t.Fatalf("%s: the fix failed: %v\n%s\n%s", path, err, output, listing)
			}
			if err := ValidatePrivateInputFile(path); err != nil {
				listing, _ := exec.Command("icacls", path).CombinedOutput()
				t.Errorf("%s: still refused after the fix: %v\n%s", path, err, listing)
			}
		}
		for _, place := range []string{workingDirectory, directory} {
			entries, err := os.ReadDir(place)
			noErr(t, err)
			for _, entry := range entries {
				if strings.HasPrefix(entry.Name(), "INJ-") {
					t.Errorf("the fix ran code from a folder name: %s", filepath.Join(place, entry.Name()))
				}
			}
		}
	})
}

// enableSecurityPrivilege enables SeSecurityPrivilege, which reading and
// writing audit entries needs, until the test ends, and reports whether the
// process holds it (an elevated administrator does).
func enableSecurityPrivilege(t *testing.T) bool {
	t.Helper()
	var token windows.Token
	noErr(t, windows.OpenProcessToken(windows.CurrentProcess(), windows.TOKEN_ADJUST_PRIVILEGES|windows.TOKEN_QUERY, &token))
	defer token.Close()
	var luid windows.LUID
	name, err := windows.UTF16PtrFromString("SeSecurityPrivilege")
	noErr(t, err)
	noErr(t, windows.LookupPrivilegeValue(nil, name, &luid))
	set := func(attributes uint32) error {
		privileges := windows.Tokenprivileges{PrivilegeCount: 1}
		privileges.Privileges[0] = windows.LUIDAndAttributes{Luid: luid, Attributes: attributes}
		return windows.AdjustTokenPrivileges(token, false, &privileges, 0, nil, nil)
	}
	if err := set(windows.SE_PRIVILEGE_ENABLED); err != nil {
		return false
	}
	t.Cleanup(func() {
		var cleanup windows.Token
		if windows.OpenProcessToken(windows.CurrentProcess(), windows.TOKEN_ADJUST_PRIVILEGES, &cleanup) == nil {
			privileges := windows.Tokenprivileges{PrivilegeCount: 1}
			privileges.Privileges[0] = windows.LUIDAndAttributes{Luid: luid}
			_ = windows.AdjustTokenPrivileges(cleanup, false, &privileges, 0, nil, nil)
			cleanup.Close()
		}
	})
	// AdjustTokenPrivileges also succeeds for a privilege the token does not
	// hold, so the privilege is proved by using it.
	_, err = windows.GetNamedSecurityInfo(os.Getenv("SystemRoot"), windows.SE_FILE_OBJECT, windows.SACL_SECURITY_INFORMATION)
	return err == nil
}

// The printed fix changes only the access list: audit entries that an
// administrator set on the file stay.
func TestWindowsNotPrivateFixKeepsAuditEntries(t *testing.T) {
	if !enableSecurityPrivilege(t) {
		t.Skip("audit entries can be set only with SeSecurityPrivilege, which this process does not hold (run elevated)")
	}
	testfixture.ForEachPowerShell(t, func(t *testing.T, shell string) {
		path := filepath.Join(t.TempDir(), "audited", "password")
		noErr(t, os.MkdirAll(filepath.Dir(path), 0o700))
		noErr(t, os.WriteFile(path, []byte("valid-password\n"), 0o600))
		const audit = "(AU;SA;FR;;;WD)"
		audited, err := windows.SecurityDescriptorFromString("S:" + audit)
		noErr(t, err)
		sacl, _, err := audited.SACL()
		noErr(t, err)
		noErr(t, windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.SACL_SECURITY_INFORMATION, nil, nil, nil, sacl))
		auditEntries := func() string {
			t.Helper()
			descriptor, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.SACL_SECURITY_INFORMATION)
			noErr(t, err)
			return descriptor.String()
		}
		if before := auditEntries(); !strings.Contains(before, audit) {
			t.Fatalf("the audit entry was not set: %s", before)
		}
		var notPrivate *NotPrivateError
		if err := ValidatePrivateInputFile(path); !errors.As(err, &notPrivate) {
			t.Fatalf("err=%v, want *NotPrivateError", err)
		}
		command := exec.Command(shell, "-NoLogo", "-NoProfile", "-NonInteractive", "-Command", notPrivate.Fix)
		command.Dir = t.TempDir()
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("the fix failed: %v\n%s", err, output)
		}
		noErr(t, ValidatePrivateInputFile(path))
		if after := auditEntries(); !strings.Contains(after, audit) {
			t.Fatalf("the fix removed the audit entry: %s", after)
		}
	})
}
