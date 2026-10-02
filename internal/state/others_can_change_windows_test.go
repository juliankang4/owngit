//go:build windows

package state

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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
		if strings.Contains(fix, "/grant:r") || !strings.Contains(fix, "SetSecurityDescriptorSddlForm") {
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
		if !strings.Contains(fix, "/setowner") || !strings.Contains(fix, "/T") {
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
