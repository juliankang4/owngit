//go:build darwin

package state

import (
	"bytes"
	"context"
	"errors"
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

func TestMacOSPrivateInputRejectsOtherAccountReadACL(t *testing.T) {
	path := filepath.Join(t.TempDir(), "credential")
	noErr(t, os.WriteFile(path, []byte("synthetic credential\n"), 0o600))
	noErr(t, exec.Command("chmod", "+a", "nobody allow read", path).Run())
	listing, err := exec.Command("ls", "-le", path).CombinedOutput()
	noErr(t, err)
	t.Logf("supplied file before validation:\n%s", listing)
	before, err := extendedSecurity(path, nil, unix.FSOPT_NOFOLLOW)
	noErr(t, err)

	var notPrivate *NotPrivateError
	if err := ValidatePrivateInputFile(path); !errors.As(err, &notPrivate) {
		t.Fatalf("err=%v, want *NotPrivateError", err)
	}
	if notPrivate.Problem != "its access list gives other accounts read access" || notPrivate.Fix != "chmod -N "+shellQuote(path) {
		t.Fatalf("refusal = %+v", notPrivate)
	}
	t.Logf("validation refusal: %s; fix: %s", notPrivate.Problem, notPrivate.Fix)
	after, err := extendedSecurity(path, nil, unix.FSOPT_NOFOLLOW)
	noErr(t, err)
	if !bytes.Equal(after, before) {
		t.Fatal("validation changed the supplied file's access list")
	}

	output, err := exec.Command("sh", "-c", notPrivate.Fix).CombinedOutput()
	if err != nil {
		t.Fatalf("the fix failed: %v: %s", err, output)
	}
	noErr(t, ValidatePrivateInputFile(path))
}

func TestMacOSPrivateInputAcceptsOwnerReadACL(t *testing.T) {
	path := filepath.Join(t.TempDir(), "credential")
	noErr(t, os.WriteFile(path, []byte("synthetic credential\n"), 0o600))
	owner, err := exec.Command("id", "-un").Output()
	noErr(t, err)
	noErr(t, exec.Command("chmod", "+a", strings.TrimSpace(string(owner))+" allow read", path).Run())
	noErr(t, ValidatePrivateInputFile(path))

	ordinary := filepath.Join(t.TempDir(), "ordinary")
	noErr(t, os.WriteFile(ordinary, []byte("synthetic credential\n"), 0o600))
	noErr(t, ValidatePrivateInputFile(ordinary))
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
