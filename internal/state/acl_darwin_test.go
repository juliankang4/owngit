//go:build darwin

package state

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

func TestMacOSVolumesWithoutAccessLists(t *testing.T) {
	original := getattrlistErr
	t.Cleanup(func() { getattrlistErr = original })
	file, err := os.Open(t.TempDir())
	noErr(t, err)
	defer file.Close()

	for _, test := range []struct {
		name  string
		errno unix.Errno
	}{
		{"EINVAL", unix.EINVAL},
		{"ENOTSUP", unix.ENOTSUP},
		{"EOPNOTSUPP", unix.EOPNOTSUPP},
	} {
		t.Run(test.name, func(t *testing.T) {
			var calls []uintptr
			getattrlistErr = func(trap, _, _, _, _, _, _ uintptr) unix.Errno {
				calls = append(calls, trap)
				return test.errno
			}
			fix, err := ChangeAccessListFix("unused")
			if err != nil || fix != "" {
				t.Fatalf("check returned fix %q, error %v", fix, err)
			}
			noErr(t, clearAccessListOf(file))
			if len(calls) != 2 || calls[0] != unix.SYS_GETATTRLIST || calls[1] != unix.SYS_FGETATTRLIST {
				t.Fatalf("getattrlist calls = %v", calls)
			}
		})
	}
}

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
	createdPath := filepath.Join(folder, "created")
	created, err := CreatePrivateFile(createdPath)
	noErr(t, err)
	noErr(t, created.Close())
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
	for _, path := range []string{folder, inherited, createdPath, later} {
		if allows(path) {
			t.Errorf("%s keeps an entry that allows access", path)
		}
	}

	walState := filepath.Join(t.TempDir(), "wal-state")
	noErr(t, os.Mkdir(walState, 0o755))
	noErr(t, exec.Command("chmod", "+a", "everyone allow read,list,file_inherit,directory_inherit", walState).Run())
	createCrashedWALFixture(t, walState, true, commitBaselineThenChangeVersion(""))
	wal := filepath.Join(walState, databaseName+walSuffix)
	shm := filepath.Join(walState, databaseName+shmSuffix)
	if !allows(wal) || !allows(shm) {
		t.Fatal("WAL or SHM inherited no access entry")
	}
	held, err := OpenDirectory(walState, false)
	noErr(t, err)
	defer held.Close()
	inspection, err := inspectState(context.Background(), held)
	noErr(t, err)
	noErr(t, inspection.accept(context.Background(), walState))
	noErr(t, inspection.release())
	if allows(wal) || allows(shm) {
		t.Error("WAL or SHM keeps an entry after acceptance")
	}
}
