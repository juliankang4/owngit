package recovery

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestBackupHeaderAndRemoval(t *testing.T) {
	root := t.TempDir()
	backup := newTwoRepositoryBackup(t, root)
	header, err := ReadBackupHeader(backup)
	noErr(t, err)
	if header.Version < closedPullRequestBackupVersion || header.CreatedAt.IsZero() {
		t.Fatalf("header: %+v", header)
	}
	if _, err := ReadBackupHeader(root); err == nil {
		t.Fatal("a folder without a manifest has a backup header")
	}

	// Anything a backup does not write stops the removal before it starts.
	for _, extra := range []string{"notes.txt", filepath.Join("repositories", "notes.txt")} {
		path := filepath.Join(backup, extra)
		noErr(t, os.WriteFile(path, []byte("mine"), 0o600))
		if err := RemoveBackup(backup); err == nil || !strings.Contains(err.Error(), "which a backup does not write") {
			t.Fatalf("%s: err=%v", extra, err)
		}
		if _, err := ReadBackupHeader(backup); err != nil {
			t.Fatalf("the refused removal changed the backup: %v", err)
		}
		noErr(t, os.Remove(path))
	}
	noErr(t, RemoveBackup(backup))
	if _, err := os.Lstat(backup); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("backup still there: %v", err)
	}
}

// A new backup is refused before it starts when the folder has less room
// than the last backup there took, for the repositories that still exist.
func TestBackupRoomComesFromTheLastBackup(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("a file of that size would take its space on NTFS; sparse files need a separate call there")
	}
	root := t.TempDir()
	backup := newTwoRepositoryBackup(t, root)
	free, known, err := diskFreeSpace(root)
	noErr(t, err)
	if !known {
		t.Skip("this system does not tell the free space")
	}
	size := free + 1<<30
	if err := os.Truncate(filepath.Join(backup, "repositories", "project.bundle"), int64(size)); err != nil {
		t.Skipf("the file system refuses a sparse file of %d MiB: %v", size>>20, err)
	}
	err = CheckBackupRoom(root, backup, func(string) bool { return true })
	var space *SpaceError
	if !errors.As(err, &space) || space.Dir != root || !strings.Contains(err.Error(), "the size of the last backup here") {
		t.Fatalf("err=%v", err)
	}
	// A repository deleted since then needs no room.
	noErr(t, CheckBackupRoom(root, backup, func(id string) bool { return id != "project" }))
}

// backUpOntoFolder writes a backup of a stopped and of a serving OwnGit
// into destination, an empty folder, verifies and removes each.
func backUpOntoFolder(t *testing.T, destination string) {
	t.Helper()
	ctx := context.Background()
	store, manager := newBackupStore(t, t.TempDir())
	for _, backup := range []string{filepath.Join(destination, "offline"), filepath.Join(destination, "serving")} {
		var err error
		if strings.HasSuffix(backup, "offline") {
			err = Create(ctx, store, manager, backup)
		} else {
			_, err = CreateWhileServing(ctx, store, manager, backup)
		}
		noErr(t, err)
		result, err := Verify(ctx, backup, "", "")
		if err != nil || !result.Verified {
			t.Fatalf("verify %s: %+v %v", backup, result, err)
		}
		noErr(t, RemoveBackup(backup))
	}
	entries, err := os.ReadDir(destination)
	noErr(t, err)
	for _, entry := range entries {
		// macOS keeps its own records on a volume it mounted.
		if !strings.HasPrefix(entry.Name(), ".") {
			t.Fatalf("%s left", entry.Name())
		}
	}
}
