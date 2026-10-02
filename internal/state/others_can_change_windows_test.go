//go:build windows

package state

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
	"owngit/internal/testfixture"
)

func TestWindowsOthersCanChangeReadsWriteGrantsOnly(t *testing.T) {
	user, defaultOwner, err := processIdentity()
	noErr(t, err)
	everyone, err := windows.CreateWellKnownSid(windows.WinWorldSid)
	noErr(t, err)
	system, err := windows.CreateWellKnownSid(windows.WinLocalSystemSid)
	noErr(t, err)
	administrators, err := windows.CreateWellKnownSid(windows.WinBuiltinAdministratorsSid)
	noErr(t, err)

	for _, test := range []struct {
		name       string
		descriptor *windows.SECURITY_DESCRIPTOR
		want       bool
	}{
		{"current user only", testDescriptor(t, user, true, user), false},
		{"another account can read", testDescriptorWith(t, user, []windows.EXPLICIT_ACCESS{
			testEntry(user, windows.GRANT_ACCESS, fileAllAccess),
			testEntry(everyone, windows.GRANT_ACCESS, windows.GENERIC_READ),
		}), false},
		{"another account can write", testDescriptorWith(t, user, []windows.EXPLICIT_ACCESS{
			testEntry(user, windows.GRANT_ACCESS, fileAllAccess),
			testEntry(everyone, windows.GRANT_ACCESS, windows.FILE_WRITE_DATA),
		}), true},
		{"trusted system and administrators", testDescriptor(t, user, true, user, system, administrators), false},
		{"untrusted owner", testDescriptor(t, everyone, true, user), true},
	} {
		got, err := descriptorAllowsOtherChanges(test.descriptor, user, defaultOwner)
		if err != nil || got != test.want {
			t.Errorf("%s: changeable=%v err=%v, want %v", test.name, got, err, test.want)
		}
	}
}

func forEachPowerShellPrivilegeState(t *testing.T, test func(t *testing.T, shell string, ordinary bool)) {
	t.Helper()
	testfixture.ForEachPowerShell(t, func(t *testing.T, shell string) {
		for _, state := range []struct {
			name     string
			ordinary bool
		}{{"session privileges", false}, {"ordinary elevated window", true}} {
			t.Run(state.name, func(t *testing.T) { test(t, shell, state.ordinary) })
		}
	})
}

func runPowerShellPrivilegeState(t *testing.T, command *exec.Cmd, ordinary bool) (output []byte, err error) {
	t.Helper()
	if !ordinary {
		return command.CombinedOutput()
	}
	withOrdinaryElevatedPrivileges(t, func() { output, err = command.CombinedOutput() })
	return output, err
}

func withOrdinaryElevatedPrivileges(t *testing.T, action func()) {
	t.Helper()
	var token windows.Token
	noErr(t, windows.OpenProcessToken(windows.CurrentProcess(), windows.TOKEN_ADJUST_PRIVILEGES|windows.TOKEN_QUERY, &token))
	defer token.Close()
	var size uint32
	err := windows.GetTokenInformation(token, windows.TokenPrivileges, nil, 0, &size)
	if !errors.Is(err, windows.ERROR_INSUFFICIENT_BUFFER) || size == 0 {
		t.Fatalf("read token privilege size: size=%d err=%v", size, err)
	}
	currentBuffer := make([]byte, size)
	noErr(t, windows.GetTokenInformation(token, windows.TokenPrivileges, &currentBuffer[0], size, &size))
	current := (*windows.Tokenprivileges)(unsafe.Pointer(&currentBuffer[0]))
	changeNotifyName, err := windows.UTF16PtrFromString("SeChangeNotifyPrivilege")
	noErr(t, err)
	var changeNotify windows.LUID
	noErr(t, windows.LookupPrivilegeValue(nil, changeNotifyName, &changeNotify))
	var disabled []windows.LUIDAndAttributes
	for _, privilege := range current.AllPrivileges() {
		if privilege.Attributes&windows.SE_PRIVILEGE_ENABLED != 0 && privilege.Luid != changeNotify {
			disabled = append(disabled, windows.LUIDAndAttributes{Luid: privilege.Luid})
		}
	}
	if len(disabled) == 0 {
		action()
		return
	}
	newStateBuffer, newState := tokenPrivilegesBuffer(disabled)
	previousBuffer := make([]byte, len(newStateBuffer))
	previous := (*windows.Tokenprivileges)(unsafe.Pointer(&previousBuffer[0]))
	var returned uint32
	noErr(t, windows.AdjustTokenPrivileges(token, false, newState, uint32(len(previousBuffer)), previous, &returned))
	defer func() {
		if err := windows.AdjustTokenPrivileges(token, false, previous, 0, nil, nil); err != nil {
			t.Errorf("restore process privileges: %v", err)
		}
	}()
	action()
}

func tokenPrivilegesBuffer(privileges []windows.LUIDAndAttributes) ([]byte, *windows.Tokenprivileges) {
	size := unsafe.Offsetof(windows.Tokenprivileges{}.Privileges) + uintptr(len(privileges))*unsafe.Sizeof(windows.LUIDAndAttributes{})
	buffer := make([]byte, size)
	state := (*windows.Tokenprivileges)(unsafe.Pointer(&buffer[0]))
	state.PrivilegeCount = uint32(len(privileges))
	copy(state.AllPrivileges(), privileges)
	return buffer, state
}

func TestWindowsPrivateDirectoryFixReplacesExplicitAndInheritedGrants(t *testing.T) {
	user, _, err := processIdentity()
	noErr(t, err)
	everyone, err := windows.CreateWellKnownSid(windows.WinWorldSid)
	noErr(t, err)
	forEachPowerShellPrivilegeState(t, func(t *testing.T, shell string, ordinary bool) {
		root := filepath.Join(t.TempDir(), "shared")
		noErr(t, os.Mkdir(root, 0o700))
		setRawDACL(t, root, true, []windows.EXPLICIT_ACCESS{
			testEntry(user, windows.GRANT_ACCESS, fileAllAccess),
			testEntry(everyone, windows.GRANT_ACCESS, windows.GENERIC_WRITE),
		}, false)
		child := filepath.Join(root, "child")
		noErr(t, os.Mkdir(child, 0o700))
		file := filepath.Join(child, "file")
		noErr(t, os.WriteFile(file, []byte("kept"), 0o600))
		setRawDACL(t, child, true, []windows.EXPLICIT_ACCESS{
			testEntry(user, windows.GRANT_ACCESS, fileAllAccess),
			testEntry(everyone, windows.GRANT_ACCESS, windows.FILE_WRITE_DATA),
		}, true)
		setRawDACL(t, file, true, []windows.EXPLICIT_ACCESS{
			testEntry(user, windows.GRANT_ACCESS, fileAllAccess),
			testEntry(everyone, windows.GRANT_ACCESS, windows.FILE_WRITE_DATA),
		}, false)

		fix, err := PrivateDirectoryFix(root, true)
		noErr(t, err)
		if strings.Contains(fix, "icacls") || strings.Contains(fix, "-Recurse") || !strings.Contains(fix, "SetSecurityDescriptorSddlForm") {
			t.Fatalf("fix=%q", fix)
		}
		command := exec.Command(shell, "-NoLogo", "-NoProfile", "-NonInteractive", "-Command", fix)
		command.Dir = t.TempDir()
		if filepath.Clean(command.Dir) == filepath.Clean(root) {
			t.Fatal("repair command working directory unexpectedly equals its target")
		}
		if output, err := runPowerShellPrivilegeState(t, command, ordinary); err != nil {
			t.Fatalf("fix failed: %v\n%s", err, output)
		}
		for _, path := range []string{root, child, file} {
			info, err := os.Stat(path)
			noErr(t, err)
			if changeable, _, err := OthersCanChange(path, info); err != nil || changeable {
				t.Errorf("%s changeable=%v err=%v", path, changeable, err)
			}
		}
		content, err := os.ReadFile(file)
		noErr(t, err)
		if string(content) != "kept" {
			t.Fatalf("repair changed file content: %q", content)
		}
	})
}

func TestWindowsPrivateDirectoryFixDoesNotFollowJunctions(t *testing.T) {
	user, _, err := processIdentity()
	noErr(t, err)
	everyone, err := windows.CreateWellKnownSid(windows.WinWorldSid)
	noErr(t, err)
	forEachPowerShellPrivilegeState(t, func(t *testing.T, shell string, ordinary bool) {
		root := filepath.Join(t.TempDir(), "shared")
		child := filepath.Join(root, "child")
		file := filepath.Join(child, "file")
		noErr(t, os.MkdirAll(child, 0o700))
		noErr(t, os.WriteFile(file, []byte("inside"), 0o600))
		for _, path := range []string{root, child, file} {
			setRawDACL(t, path, true, []windows.EXPLICIT_ACCESS{
				testEntry(user, windows.GRANT_ACCESS, fileAllAccess),
				testEntry(everyone, windows.GRANT_ACCESS, windows.FILE_WRITE_DATA),
			}, false)
		}

		outside := t.TempDir()
		outsideFile := filepath.Join(outside, "outside.txt")
		noErr(t, os.WriteFile(outsideFile, []byte("outside"), 0o600))
		outsideProtection := captureProtectionFingerprints(t, outside, outsideFile)
		junction := filepath.Join(root, "objects-junction")
		linkTestFolder(t, outside, junction)

		fix, err := PrivateDirectoryFix(root, true)
		noErr(t, err)
		if strings.Contains(fix, "icacls") || strings.Contains(fix, "-Recurse") || !strings.Contains(fix, "ReparsePoint") {
			t.Fatalf("junction-safe fix=%q", fix)
		}
		command := exec.Command(shell, "-NoLogo", "-NoProfile", "-NonInteractive", "-Command", fix)
		command.Dir = t.TempDir()
		output, err := runPowerShellPrivilegeState(t, command, ordinary)
		if err != nil {
			t.Fatalf("fix failed: %v\n%s", err, output)
		}
		if !strings.Contains(string(output), "left linked entry unchanged") || !strings.Contains(string(output), junction) {
			t.Fatalf("fix did not report the unchanged junction:\n%s", output)
		}
		assertProtectionFingerprints(t, outsideProtection)
		content, err := os.ReadFile(outsideFile)
		noErr(t, err)
		if string(content) != "outside" {
			t.Fatalf("junction target content=%q", content)
		}
		junctionName, err := windows.UTF16PtrFromString(junction)
		noErr(t, err)
		attributes, err := windows.GetFileAttributes(junctionName)
		if err != nil || attributes&windows.FILE_ATTRIBUTE_REPARSE_POINT == 0 {
			t.Fatalf("junction changed: attributes=%#x err=%v", attributes, err)
		}
		for _, path := range []string{root, child, file} {
			info, err := os.Stat(path)
			noErr(t, err)
			if changeable, _, err := OthersCanChange(path, info); err != nil || changeable {
				t.Errorf("%s changeable=%v err=%v", path, changeable, err)
			}
		}
	})
}

func TestWindowsPrivateDirectoryFixChangesUntrustedOwners(t *testing.T) {
	current, _, err := processIdentity()
	noErr(t, err)
	forEachPowerShellPrivilegeState(t, func(t *testing.T, shell string, ordinary bool) {
		parent, asOther := sharedParent(t)
		root := filepath.Join(parent, "foreign")
		child := filepath.Join(root, "child")
		file := filepath.Join(child, "file")
		var createErr error
		asOther(func() {
			if createErr = os.Mkdir(root, 0o700); createErr != nil {
				return
			}
			if createErr = os.Mkdir(child, 0o700); createErr != nil {
				return
			}
			createErr = os.WriteFile(file, []byte("kept"), 0o600)
		})
		noErr(t, createErr)
		fix, err := PrivateDirectoryFix(root, true)
		noErr(t, err)
		if strings.Contains(fix, "icacls") || strings.Contains(fix, "-Recurse") || !strings.Contains(fix, "SetOwner") {
			t.Fatalf("foreign-owner fix=%q", fix)
		}
		command := exec.Command(shell, "-NoLogo", "-NoProfile", "-NonInteractive", "-Command", fix)
		command.Dir = t.TempDir()
		if filepath.Clean(command.Dir) == filepath.Clean(root) {
			t.Fatal("repair command working directory unexpectedly equals its target")
		}
		if output, err := runPowerShellPrivilegeState(t, command, ordinary); err != nil {
			t.Fatalf("fix failed: %v\n%s", err, output)
		}
		for _, path := range []string{root, child, file} {
			descriptor, err := pathDescriptor(path)
			noErr(t, err)
			owner, _, err := descriptor.Owner()
			noErr(t, err)
			if owner == nil || !owner.Equals(current) {
				t.Errorf("%s owner=%v, want %s", path, owner, current)
			}
		}
	})
}

func TestWindowsPrivateDirectoryFixStopsAtFirstItemFailure(t *testing.T) {
	current, _, err := processIdentity()
	noErr(t, err)
	everyone, err := windows.CreateWellKnownSid(windows.WinWorldSid)
	noErr(t, err)
	system, err := windows.CreateWellKnownSid(windows.WinLocalSystemSid)
	noErr(t, err)
	testfixture.ForEachPowerShell(t, func(t *testing.T, shell string) {
		root := filepath.Join(t.TempDir(), "shared")
		healthy := filepath.Join(root, "a-healthy")
		blocked := filepath.Join(root, "z-blocked")
		noErr(t, os.Mkdir(root, 0o700))
		noErr(t, os.WriteFile(healthy, []byte("healthy"), 0o600))
		noErr(t, os.WriteFile(blocked, []byte("blocked"), 0o600))
		setRawDACL(t, healthy, true, []windows.EXPLICIT_ACCESS{
			testEntry(current, windows.GRANT_ACCESS, fileAllAccess),
			testEntry(everyone, windows.GRANT_ACCESS, windows.FILE_WRITE_DATA),
		}, false)
		blockedACL, err := windows.ACLFromEntries([]windows.EXPLICIT_ACCESS{
			testEntry(everyone, windows.DENY_ACCESS, windows.READ_CONTROL|windows.WRITE_DAC|windows.WRITE_OWNER),
		}, nil)
		noErr(t, err)
		noErr(t, windows.SetNamedSecurityInfo(blocked, windows.SE_FILE_OBJECT,
			windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, blockedACL, nil))
		withWindowsPrivilege(t, "SeRestorePrivilege", func() {
			noErr(t, windows.SetNamedSecurityInfo(blocked, windows.SE_FILE_OBJECT,
				windows.OWNER_SECURITY_INFORMATION, system, nil, nil, nil))
		})
		privateACL, err := ownerOnlyACL(current, false)
		noErr(t, err)
		t.Cleanup(func() {
			var restoreErr error
			withWindowsPrivilege(t, "SeRestorePrivilege", func() {
				restoreErr = windows.SetNamedSecurityInfo(blocked, windows.SE_FILE_OBJECT,
					windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION,
					current, nil, privateACL, nil)
			})
			if restoreErr != nil {
				t.Errorf("restore blocked fixture: %v", restoreErr)
			}
		})

		blockedDescriptor, err := windows.GetNamedSecurityInfo(blocked, windows.SE_FILE_OBJECT,
			windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
		noErr(t, err)
		blockedBefore := blockedDescriptor.String()
		healthyProtection := captureProtectionFingerprints(t, healthy)
		fix, err := PrivateDirectoryFix(root, true)
		noErr(t, err)
		if !strings.Contains(fix, "$ErrorActionPreference = 'Stop'") {
			t.Fatalf("repair lacks a terminating-error scope: %q", fix)
		}
		command := exec.Command(shell, "-NoLogo", "-NoProfile", "-NonInteractive", "-Command", fix)
		command.Dir = root
		var output []byte
		var runErr error
		withOrdinaryElevatedPrivileges(t, func() {
			if _, err := windows.GetNamedSecurityInfo(blocked, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION); !errors.Is(err, windows.ERROR_ACCESS_DENIED) {
				t.Fatalf("invalid failure fixture: DACL read err=%v, want access denied", err)
			}
			output, runErr = command.CombinedOutput()
		})
		if runErr == nil {
			after := blockedBefore
			if descriptor, err := windows.GetNamedSecurityInfo(blocked, windows.SE_FILE_OBJECT,
				windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION); err == nil {
				after = descriptor.String()
			}
			t.Fatalf("fixture item was repaired; precondition invalid\nbefore=%s\nafter=%s\n%s", blockedBefore, after, output)
		}
		if !strings.Contains(string(output), blocked) || !strings.Contains(string(output), "OwnGit could not repair") {
			t.Fatalf("repair did not report the failed item:\n%s", output)
		}
		assertProtectionFingerprints(t, healthyProtection)
	})
}

func withWindowsPrivilege(t *testing.T, name string, action func()) {
	t.Helper()
	var token windows.Token
	noErr(t, windows.OpenProcessToken(windows.CurrentProcess(), windows.TOKEN_ADJUST_PRIVILEGES|windows.TOKEN_QUERY, &token))
	defer token.Close()
	encoded, err := windows.UTF16PtrFromString(name)
	noErr(t, err)
	var luid windows.LUID
	noErr(t, windows.LookupPrivilegeValue(nil, encoded, &luid))
	enabled := windows.Tokenprivileges{PrivilegeCount: 1}
	enabled.Privileges[0] = windows.LUIDAndAttributes{Luid: luid, Attributes: windows.SE_PRIVILEGE_ENABLED}
	var previous windows.Tokenprivileges
	var returned uint32
	noErr(t, windows.AdjustTokenPrivileges(token, false, &enabled, uint32(unsafe.Sizeof(previous)), &previous, &returned))
	defer func() {
		if err := windows.AdjustTokenPrivileges(token, false, &previous, 0, nil, nil); err != nil {
			t.Errorf("restore %s: %v", name, err)
		}
	}()
	action()
}

func TestWindowsOthersCanChangeReportsUnreadableDACL(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing")
	if changeable, fix, err := OthersCanChange(path, nil); err == nil || changeable || fix != "" {
		t.Fatalf("missing path: changeable=%v fix=%q err=%v", changeable, fix, err)
	}

	path = filepath.Join(t.TempDir(), "folder")
	noErr(t, os.Mkdir(path, 0o700))
	info, err := os.Stat(path)
	noErr(t, err)
	if _, _, err := OthersCanChange(path, info); err != nil {
		t.Fatalf("read current DACL: %v", err)
	}
}
