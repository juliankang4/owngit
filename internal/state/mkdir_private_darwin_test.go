//go:build darwin

package state

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

func TestMkdirPrivateStartsWithoutInheritedMacOSACL(t *testing.T) {
	parent := filepath.Join(t.TempDir(), "shared")
	noErr(t, os.Mkdir(parent, 0o700))
	noErr(t, exec.Command("chmod", "+a", "everyone allow list,add_file,search,add_subdirectory,delete_child,file_inherit,directory_inherit", parent).Run())
	child := filepath.Join(parent, "staging")
	noErr(t, MkdirPrivate(child))

	listing, err := exec.Command("ls", "-lde", child).CombinedOutput()
	noErr(t, err)
	if strings.Contains(string(listing), " allow ") {
		t.Fatalf("private directory was visible with an inherited permit entry:\n%s", listing)
	}
	info, err := os.Stat(child)
	noErr(t, err)
	if info.Mode().Perm() != 0o700 {
		t.Fatalf("private directory mode=%o", info.Mode().Perm())
	}
}

func TestMkdirPrivateFallbackRefusesInheritedPermit(t *testing.T) {
	original := mkdirExtended
	t.Cleanup(func() { mkdirExtended = original })
	mkdirExtended = func(uintptr, os.FileMode, uintptr) unix.Errno { return unix.EINVAL }
	parent := filepath.Join(t.TempDir(), "shared")
	noErr(t, os.Mkdir(parent, 0o700))
	noErr(t, exec.Command("chmod", "+a", "everyone allow list,add_file,search,add_subdirectory,delete_child,file_inherit,directory_inherit", parent).Run())
	path := filepath.Join(parent, "staging")
	err := MkdirPrivate(path)
	if err == nil || !strings.Contains(err.Error(), "storage gives new folders inherited access") || !strings.Contains(err.Error(), "chmod -N") || !strings.Contains(err.Error(), "owngit doctor") {
		t.Fatalf("inherited fallback error=%v", err)
	}
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("refused fallback left its empty directory: %v", err)
	}
}

func TestMkdirPrivateCleanupFailureIsNotCreationCollision(t *testing.T) {
	originalMkdir, originalCleanup := mkdirExtended, beforePrivateDirectoryCleanup
	t.Cleanup(func() {
		mkdirExtended = originalMkdir
		beforePrivateDirectoryCleanup = originalCleanup
	})
	mkdirExtended = func(uintptr, os.FileMode, uintptr) unix.Errno { return unix.EINVAL }
	parent := filepath.Join(t.TempDir(), "shared")
	noErr(t, os.Mkdir(parent, 0o700))
	noErr(t, exec.Command("chmod", "+a", "everyone allow list,add_file,search,add_subdirectory,delete_child,file_inherit,directory_inherit", parent).Run())
	path := filepath.Join(parent, "staging")
	blocker := filepath.Join(path, "appeared-before-cleanup")
	beforePrivateDirectoryCleanup = func(string) { noErr(t, os.WriteFile(blocker, []byte("kept"), 0o600)) }
	err := MkdirPrivate(path)
	if err == nil || errors.Is(err, ErrPrivateDirectoryExists) || errors.Is(err, os.ErrExist) {
		t.Fatalf("cleanup failure classification=%v", err)
	}
	if !strings.Contains(err.Error(), "storage gives new folders inherited access") || !strings.Contains(err.Error(), "directory not empty") {
		t.Fatalf("cleanup failure lost a cause: %v", err)
	}
	if content, readErr := os.ReadFile(blocker); readErr != nil || string(content) != "kept" {
		t.Fatalf("remaining blocker=%q err=%v", content, readErr)
	}
	noErr(t, os.Remove(blocker))
	noErr(t, os.Remove(path))
}

func TestMkdirPrivateFallbackContinuesWhenACLQueryIsUnsupported(t *testing.T) {
	originalMkdir, originalGetattr := mkdirExtended, getattrlistErr
	t.Cleanup(func() {
		mkdirExtended = originalMkdir
		getattrlistErr = originalGetattr
	})
	mkdirExtended = func(uintptr, os.FileMode, uintptr) unix.Errno { return unix.EINVAL }
	getattrlistErr = func(uintptr, uintptr, uintptr, uintptr, uintptr, uintptr, uintptr) unix.Errno { return unix.ENOTSUP }
	path := filepath.Join(t.TempDir(), "staging")
	noErr(t, MkdirPrivate(path))
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o700 {
		t.Fatalf("unsupported-query fallback info=%v err=%v", info, err)
	}
}

func TestMkdirPrivateFallsBackOnlyWithoutACLs(t *testing.T) {
	original := mkdirExtended
	t.Cleanup(func() { mkdirExtended = original })
	for _, errno := range []unix.Errno{unix.EINVAL, unix.ENOTSUP, unix.EOPNOTSUPP} {
		t.Run(errno.Error(), func(t *testing.T) {
			mkdirExtended = func(uintptr, os.FileMode, uintptr) unix.Errno { return errno }
			path := filepath.Join(t.TempDir(), "staging")
			noErr(t, MkdirPrivate(path))
			if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o700 {
				t.Fatalf("fallback info=%v err=%v", info, err)
			}
		})
	}

	mkdirExtended = func(uintptr, os.FileMode, uintptr) unix.Errno { return unix.EACCES }
	path := filepath.Join(t.TempDir(), "refused")
	if err := MkdirPrivate(path); err == nil || !errors.Is(err, unix.EACCES) {
		t.Fatalf("permission error=%v", err)
	}
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("permission failure created the directory: %v", err)
	}
}
