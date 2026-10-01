//go:build darwin

package state

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
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

func TestMacOSPrivateInputRejectsGroupReadACL(t *testing.T) {
	path := filepath.Join(t.TempDir(), "credential")
	noErr(t, os.WriteFile(path, []byte("synthetic credential\n"), 0o600))
	noErr(t, exec.Command("chmod", "+a", "group:everyone allow read", path).Run())

	var notPrivate *NotPrivateError
	if err := ValidatePrivateInputFile(path); !errors.As(err, &notPrivate) {
		t.Fatalf("err=%v, want *NotPrivateError", err)
	}
	if notPrivate.Problem != "its access list lets a group read it" || notPrivate.Fix != "chmod -N "+shellQuote(path) {
		t.Fatalf("refusal = %+v", notPrivate)
	}
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

type privateInputOwnerInfo struct {
	os.FileInfo
	stat syscall.Stat_t
}

func (info privateInputOwnerInfo) Sys() any { return &info.stat }

func TestMacOSPrivateInputOwnerMustBeCurrentUserOrRoot(t *testing.T) {
	path := filepath.Join(t.TempDir(), "credential")
	noErr(t, os.WriteFile(path, []byte("synthetic credential\n"), 0o600))
	info, err := os.Stat(path)
	noErr(t, err)
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		t.Fatal("file owner is unavailable")
	}

	own := privateInputOwnerInfo{FileInfo: info, stat: *stat}
	noErr(t, validatePrivateInputOwner(path, own))
	root := own
	root.stat.Uid = 0
	noErr(t, validatePrivateInputOwner(path, root))

	other := own
	other.stat.Uid++
	if other.stat.Uid == 0 || int(other.stat.Uid) == os.Geteuid() {
		other.stat.Uid++
	}
	var owner *PrivateInputOwnerError
	ownerErr := validatePrivateInputOwner(path, other)
	if !errors.As(ownerErr, &owner) {
		t.Fatalf("other owner err=%v, want *PrivateInputOwnerError", ownerErr)
	}
	if owner.Path != path || ownerErr.Error() != path+" must belong to you or root" {
		t.Fatalf("other owner refusal = %v", ownerErr)
	}

	rootInfo, err := os.Stat("/etc/hosts")
	if err != nil {
		t.Fatalf("root-owned control: %v", err)
	}
	rootStat, ok := rootInfo.Sys().(*syscall.Stat_t)
	if !ok || rootStat.Uid != 0 {
		t.Fatalf("/etc/hosts owner = %#v, want root", rootInfo.Sys())
	}
	noErr(t, validatePrivateInputOwner("/etc/hosts", rootInfo))
}

func TestMacOSPrivateInputHandleKeepsValidatedObjectAfterReplacement(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "credential")
	replacement := filepath.Join(directory, "replacement")
	const original = "validated credential\n"
	noErr(t, os.WriteFile(path, []byte(original), 0o600))
	noErr(t, os.WriteFile(replacement, []byte("replacement credential\n"), 0o600))
	noErr(t, exec.Command("chmod", "+a", "nobody allow read", replacement).Run())

	file, err := OpenPrivateInputFile(path)
	noErr(t, err)
	defer file.Close()
	noErr(t, os.Rename(replacement, path))
	content, err := io.ReadAll(file)
	noErr(t, err)
	if string(content) != original {
		t.Fatalf("read %q from the validated handle", content)
	}
	var notPrivate *NotPrivateError
	if err := ValidatePrivateInputFile(path); !errors.As(err, &notPrivate) {
		t.Fatalf("the replacement path is not the unsafe fixture: %v", err)
	}
}

func TestMacOSPrivateInputHandleFollowsFinalSymlink(t *testing.T) {
	directory := t.TempDir()
	target := filepath.Join(directory, "target")
	link := filepath.Join(directory, "credential")
	noErr(t, os.WriteFile(target, []byte("linked credential\n"), 0o600))
	noErr(t, os.Symlink(target, link))

	file, err := OpenPrivateInputFile(link)
	noErr(t, err)
	content, readErr := io.ReadAll(file)
	noErr(t, errors.Join(readErr, file.Close()))
	if string(content) != "linked credential\n" {
		t.Fatalf("linked content = %q", content)
	}

	noErr(t, exec.Command("chmod", "+a", "nobody allow read", target).Run())
	var notPrivate *NotPrivateError
	if file, err := OpenPrivateInputFile(link); !errors.As(err, &notPrivate) {
		if file != nil {
			file.Close()
		}
		t.Fatalf("unsafe linked target err=%v, want *NotPrivateError", err)
	}
}

func TestMacOSPrivateInputACLQueryFailureFailsClosed(t *testing.T) {
	path := filepath.Join(t.TempDir(), "credential")
	noErr(t, os.WriteFile(path, []byte("synthetic credential\n"), 0o600))
	original := getattrlistErr
	t.Cleanup(func() { getattrlistErr = original })
	var calls []uintptr
	getattrlistErr = func(trap, a1, a2, a3, a4, a5, a6 uintptr) unix.Errno {
		calls = append(calls, trap)
		if trap == unix.SYS_FGETATTRLIST {
			return unix.EIO
		}
		return original(trap, a1, a2, a3, a4, a5, a6)
	}
	file, err := OpenPrivateInputFile(path)
	if file != nil {
		file.Close()
	}
	if !errors.Is(err, unix.EIO) {
		t.Fatalf("ACL query err=%v, want EIO", err)
	}
	if len(calls) != 1 || calls[0] != unix.SYS_FGETATTRLIST {
		t.Fatalf("getattrlist calls = %v, want one held-file query", calls)
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
