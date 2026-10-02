//go:build windows

package state

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

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

func TestWindowsPrivateDirectoryFixReplacesExplicitAndInheritedGrants(t *testing.T) {
	user, _, err := processIdentity()
	noErr(t, err)
	everyone, err := windows.CreateWellKnownSid(windows.WinWorldSid)
	noErr(t, err)
	testfixture.ForEachPowerShell(t, func(t *testing.T, shell string) {
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
		if output, err := command.CombinedOutput(); err != nil {
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
	testfixture.ForEachPowerShell(t, func(t *testing.T, shell string) {
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
		output, err := command.CombinedOutput()
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
	testfixture.ForEachPowerShell(t, func(t *testing.T, shell string) {
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
		if output, err := command.CombinedOutput(); err != nil {
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
	other, token, asOther := otherAccountWithToken(t)
	testfixture.ForEachPowerShell(t, func(t *testing.T, shell string) {
		parent, _ := sharedParent(t)
		root := filepath.Join(parent, "foreign")
		healthy := filepath.Join(root, "a-healthy")
		blocked := filepath.Join(root, "z-blocked")
		var fixtureErr error
		asOther(func() { fixtureErr = os.Mkdir(root, 0o700) })
		noErr(t, fixtureErr)
		t.Cleanup(func() {
			var cleanupErr error
			asOther(func() { cleanupErr = os.RemoveAll(root) })
			if cleanupErr != nil && !os.IsNotExist(cleanupErr) {
				t.Errorf("remove standard-account fixture: %v", cleanupErr)
			}
		})
		inherited := func(sid *windows.SID) windows.EXPLICIT_ACCESS {
			entry := testEntry(sid, windows.GRANT_ACCESS, fileAllAccess)
			entry.Inheritance = windows.SUB_CONTAINERS_AND_OBJECTS_INHERIT
			return entry
		}
		rootACL, err := windows.ACLFromEntries([]windows.EXPLICIT_ACCESS{inherited(other), inherited(current)}, nil)
		noErr(t, err)
		noErr(t, windows.SetNamedSecurityInfo(root, windows.SE_FILE_OBJECT,
			windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, rootACL, nil))
		asOther(func() { fixtureErr = os.WriteFile(healthy, []byte("healthy"), 0o600) })
		noErr(t, fixtureErr)
		noErr(t, os.WriteFile(blocked, []byte("blocked"), 0o600))
		setRawDACL(t, healthy, true, []windows.EXPLICIT_ACCESS{
			testEntry(other, windows.GRANT_ACCESS, fileAllAccess),
			testEntry(current, windows.GRANT_ACCESS, windows.FILE_WRITE_DATA),
		}, false)
		setRawDACL(t, blocked, true, []windows.EXPLICIT_ACCESS{
			testEntry(other, windows.DENY_ACCESS, windows.WRITE_DAC|windows.WRITE_OWNER),
			testEntry(other, windows.GRANT_ACCESS, windows.GENERIC_READ|windows.DELETE),
			testEntry(current, windows.GRANT_ACCESS, fileAllAccess),
		}, false)

		for _, access := range []windows.ACCESS_MASK{windows.WRITE_DAC, windows.WRITE_OWNER} {
			var probeErr error
			asOther(func() {
				name, err := windows.UTF16PtrFromString(blocked)
				if err != nil {
					probeErr = err
					return
				}
				handle, err := windows.CreateFile(name, uint32(access),
					windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil, windows.OPEN_EXISTING, 0, 0)
				probeErr = err
				if err == nil {
					windows.CloseHandle(handle)
				}
			})
			if probeErr == nil {
				t.Fatalf("invalid failure fixture: standard account obtained access %#x to blocked item", access)
			}
		}
		healthyProtection := captureProtectionFingerprints(t, healthy)
		fix := privatePathRepairCommand(root, other, true, true)
		if !strings.Contains(fix, "$ErrorActionPreference = 'Stop'") {
			t.Fatalf("repair lacks a terminating-error scope: %q", fix)
		}
		command := exec.Command(shell, "-NoLogo", "-NoProfile", "-NonInteractive", "-Command", fix)
		command.Dir = root
		command.SysProcAttr = &syscall.SysProcAttr{Token: syscall.Token(token)}
		output, runErr := command.CombinedOutput()
		if runErr == nil {
			t.Fatalf("repair reported success after an intermediate ACL failure:\n%s", output)
		}
		if !strings.Contains(string(output), blocked) || !strings.Contains(string(output), "OwnGit could not repair") {
			t.Fatalf("repair did not report the failed item:\n%s", output)
		}
		assertProtectionFingerprints(t, healthyProtection)
	})
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
