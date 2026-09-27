//go:build darwin

package state

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// An access list that a folder passes on survives the owner-only mode, so
// protecting a private folder or file removes it, and a file made in the
// folder afterwards inherits nothing.
func TestProtectPrivateRemovesMacOSAccessLists(t *testing.T) {
	allows := func(path string) bool {
		list, err := exec.Command("ls", "-lde", path).CombinedOutput()
		noErr(t, err)
		return strings.Contains(string(list), "allow")
	}
	folder := filepath.Join(t.TempDir(), "state")
	inherited := filepath.Join(folder, "inherited")
	noErr(t, os.Mkdir(folder, 0o755))
	noErr(t, exec.Command("chmod", "+a", "everyone allow read,list,file_inherit,directory_inherit", folder).Run())
	noErr(t, os.WriteFile(inherited, nil, 0o600))
	if !allows(inherited) {
		t.Fatal("the test folder passed on no entry")
	}
	noErr(t, ProtectPrivatePath(folder, true))
	file, err := os.Open(inherited)
	noErr(t, err)
	defer file.Close()
	noErr(t, ProtectPrivateHandle(file, false))
	later := filepath.Join(folder, "later")
	noErr(t, os.WriteFile(later, nil, 0o600))
	for _, path := range []string{folder, inherited, later} {
		if allows(path) {
			t.Errorf("%s keeps an entry that allows access", path)
		}
	}
}
