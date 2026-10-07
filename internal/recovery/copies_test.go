package recovery

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestBackupCopyOpenAndRemoval(t *testing.T) {
	root := t.TempDir()
	backup := newTwoRepositoryBackup(t, root)
	folder, err := OpenBackupFolder(root)
	noErr(t, err)
	defer folder.Close()
	if _, err := folder.Open("missing"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("missing backup: %v", err)
	}
	if runtime.GOOS != "windows" {
		noErr(t, os.Symlink(backup, filepath.Join(root, "linked")))
		if _, err := folder.Open("linked"); !errors.Is(err, ErrNotABackup) {
			t.Fatalf("a link to a backup was opened: %v", err)
		}
	}

	// Anything a backup does not write, a file named like a bundle
	// included, stops the removal before anything is removed.
	for _, extra := range []string{"notes.txt", filepath.Join("repositories", "notes.txt"), filepath.Join("repositories", "foreign.bundle")} {
		extraPath := filepath.Join(backup, extra)
		noErr(t, os.WriteFile(extraPath, []byte("mine"), 0o600))
		opened, err := folder.Open(filepath.Base(backup))
		noErr(t, err)
		if opened.CreatedAt.IsZero() {
			t.Fatal("no creation time")
		}
		if err := opened.Remove(); err == nil || !strings.Contains(err.Error(), filepath.Base(extra)) {
			t.Fatalf("%s: err=%v", extra, err)
		}
		opened.Close()
		for _, kept := range []string{extra, manifestName, filepath.Join("repositories", "project.bundle")} {
			if _, err := os.Stat(filepath.Join(backup, kept)); err != nil {
				t.Fatalf("the refused removal removed %s: %v", kept, err)
			}
		}
		noErr(t, os.Remove(extraPath))
	}
	opened, err := folder.Open(filepath.Base(backup))
	noErr(t, err)
	noErr(t, opened.Remove())
	if _, err := os.Lstat(backup); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("backup still there: %v", err)
	}
}

// appleDouble is the start of an AppleDouble file as macOS writes one: the
// magic number and version 2.
var appleDouble = append([]byte{0x00, 0x05, 0x16, 0x07, 0x00, 0x02, 0x00, 0x00}, make([]byte, 74)...)

// The companions a file system keeps beside a backup's files go with the
// backup. Anything else named like one, and a "._" file without its file,
// stops the removal before anything is removed, like any other entry.
func TestBackupRemovalTakesCompanionsAlong(t *testing.T) {
	companions := []string{"._" + manifestName, filepath.Join("repositories", "._project.bundle")}
	backupWithCompanions := func(t *testing.T) (*BackupFolder, string) {
		root := t.TempDir()
		backup := newTwoRepositoryBackup(t, root)
		folder, err := OpenBackupFolder(root)
		noErr(t, err)
		t.Cleanup(folder.Close)
		for _, name := range companions {
			noErr(t, os.WriteFile(filepath.Join(backup, name), appleDouble, 0o600))
		}
		return folder, backup
	}
	for _, entry := range []struct {
		name string
		make func(backup, fake string) error
	}{
		{"file without its file", func(backup, _ string) error {
			return os.WriteFile(filepath.Join(backup, "._notes.txt"), appleDouble, 0o600)
		}},
		{"file that is no AppleDouble file", func(_, fake string) error {
			return os.WriteFile(fake, []byte("user data, not attributes"), 0o600)
		}},
		{"folder", func(_, fake string) error {
			if err := os.Mkdir(fake, 0o700); err != nil {
				return err
			}
			return os.WriteFile(filepath.Join(fake, "data"), appleDouble, 0o600)
		}},
		{"link", func(backup, fake string) error {
			if runtime.GOOS == "windows" {
				return errors.ErrUnsupported
			}
			return os.Symlink(filepath.Join(backup, "._"+manifestName), fake)
		}},
		{"file larger than a companion", func(_, fake string) error {
			return os.WriteFile(fake, append(appleDouble, make([]byte, companionLimit)...), 0o600)
		}},
	} {
		t.Run(entry.name, func(t *testing.T) {
			folder, backup := backupWithCompanions(t)
			if err := entry.make(backup, filepath.Join(backup, "._repositories")); errors.Is(err, errors.ErrUnsupported) {
				t.Skip("making a symbolic link needs a privilege on Windows")
			} else {
				noErr(t, err)
			}
			opened, err := folder.Open(filepath.Base(backup))
			noErr(t, err)
			defer opened.Close()
			if err := opened.Remove(); err == nil || !strings.Contains(err.Error(), "which a backup does not write") {
				t.Fatalf("removal: %v", err)
			}
			for _, kept := range append([]string{manifestName, filepath.Join("repositories", "project.bundle")}, companions...) {
				if _, err := os.Lstat(filepath.Join(backup, kept)); err != nil {
					t.Fatalf("the refused removal removed %s: %v", kept, err)
				}
			}
		})
	}

	folder, backup := backupWithCompanions(t)
	noErr(t, os.WriteFile(filepath.Join(backup, "._repositories"), appleDouble, 0o600))
	opened, err := folder.Open(filepath.Base(backup))
	noErr(t, err)
	noErr(t, opened.Remove())
	if _, err := os.Lstat(backup); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("backup still there: %v", err)
	}
}

// A backup is removed through the folder that was opened and checked: when
// another backup takes its name after it was opened, the other backup is
// not touched and the removal fails.
func TestBackupRemovalStaysWithTheOpenedFolder(t *testing.T) {
	root := t.TempDir()
	selected := newTwoRepositoryBackup(t, root)
	other := filepath.Join(root, "other")
	noErr(t, os.CopyFS(other, os.DirFS(selected)))
	folder, err := OpenBackupFolder(root)
	noErr(t, err)
	defer folder.Close()
	opened, err := folder.Open(filepath.Base(selected))
	noErr(t, err)
	defer opened.Close()
	moved := filepath.Join(root, "moved")
	if err := os.Rename(selected, moved); err != nil {
		if runtime.GOOS == "windows" {
			// The opened backup cannot be renamed while it is held.
			return
		}
		t.Fatal(err)
	}
	noErr(t, os.Rename(other, selected))
	if err := opened.Remove(); err == nil {
		t.Fatal("the removal reported success after another backup took the name")
	}
	for _, name := range []string{manifestName, filepath.Join("repositories", "project.bundle")} {
		if _, err := os.Stat(filepath.Join(selected, name)); err != nil {
			t.Fatalf("the other backup lost %s: %v", name, err)
		}
		if _, err := os.Stat(filepath.Join(moved, name)); !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("the selected backup kept %s: %v", name, err)
		}
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
	folder, err := OpenBackupFolder(root)
	noErr(t, err)
	defer folder.Close()
	opened, err := folder.Open(filepath.Base(backup))
	noErr(t, err)
	defer opened.Close()
	needed, err := opened.Size(func(string) bool { return true })
	noErr(t, err)
	err = folder.CheckRoom(needed)
	var space *SpaceError
	if !errors.As(err, &space) || !strings.Contains(err.Error(), "the size of the last backup here") {
		t.Fatalf("err=%v", err)
	}
	// A repository deleted since then needs no room.
	needed, err = opened.Size(func(id string) bool { return id != "project" })
	noErr(t, err)
	noErr(t, folder.CheckRoom(needed))
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
			_, err = CreateWithReport(ctx, store, manager, backup)
		} else {
			_, err = CreateWhileServing(ctx, store, manager, backup)
		}
		noErr(t, err)
		result, err := Verify(ctx, backup, "", "")
		if err != nil || !result.Verified {
			t.Fatalf("verify %s: %+v %v", backup, result, err)
		}
		folder, err := OpenBackupFolder(destination)
		noErr(t, err)
		opened, err := folder.Open(filepath.Base(backup))
		noErr(t, err)
		noErr(t, opened.Remove())
		folder.Close()
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
