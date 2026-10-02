//go:build windows

package state

import (
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/windows"
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
