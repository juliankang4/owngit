package repository

import (
	"context"
	"errors"
	"os"
	"runtime"
	"testing"
	"time"
)

// A rename is refused, with nothing changed, while a Git operation holds the
// repository or a backup reads it, and for a name another repository has or
// that is not a valid name. The storage folder keeps its ID.
func TestRenameRefusesBusyRepositoriesAndTakenNames(t *testing.T) {
	ctx := context.Background()
	manager, remote, _ := newTestRepository(t)
	if _, err := manager.Create(ctx, "Other", ""); err != nil {
		t.Fatal(err)
	}
	unchanged := func() {
		t.Helper()
		stored, _, err := manager.Store.Repository(ctx, "sample")
		noErr(t, err)
		if stored.Address != "sample" || stored.Name != "sample" {
			t.Fatalf("refused rename changed the repository: %+v", stored)
		}
	}

	lock := manager.Locks.For("sample")
	lock.RLock()
	if _, err := manager.Rename(ctx, "sample", "renamed", time.Now()); !errors.Is(err, ErrRepositoryInUse) {
		t.Fatalf("rename while a Git operation reads err=%v", err)
	}
	lock.RUnlock()
	unchanged()

	hold, err := manager.HoldForBackup()
	noErr(t, err)
	hold.Set("sample")
	if _, err := manager.Rename(ctx, "sample", "renamed", time.Now()); !errors.Is(err, ErrBackupReading) {
		t.Fatalf("rename while a backup reads err=%v", err)
	}
	hold.Close()
	unchanged()

	for name, want := range map[string]error{"OTHER": ErrNameTaken, "new": ErrReservedName, "bad/name": ErrInvalidName, "x.git": ErrInvalidName} {
		if _, err := manager.Rename(ctx, "sample", name, time.Now()); !errors.Is(err, want) {
			t.Fatalf("rename to %q err=%v, want %v", name, err, want)
		}
	}
	unchanged()
	if _, err := manager.Rename(ctx, "missing", "free", time.Now()); !errors.Is(err, ErrRepositoryNotFound) {
		t.Fatalf("rename of a missing repository err=%v", err)
	}

	renamed, err := manager.Rename(ctx, "sample", " Renamed ", time.Now())
	noErr(t, err)
	if renamed.ID != "sample" || renamed.Name != "Renamed" || renamed.Address != "renamed" {
		t.Fatalf("renamed=%+v", renamed)
	}
	if _, err := os.Stat(remote); err != nil {
		t.Fatalf("the storage folder moved: %v", err)
	}
	// The new name, and the old one while its alias lasts, are not free for
	// a new repository.
	for _, name := range []string{"renamed", "sample"} {
		if _, err := manager.Create(ctx, name, ""); !errors.Is(err, ErrNameTaken) {
			t.Fatalf("create %q err=%v", name, err)
		}
	}
}

// A backup that starts holding the repository while a rename waits for the
// lock of the new name refuses the rename; it cannot slip in after the
// rename checked for backups.
func TestRenameRefusesABackupThatStartsWhileItWaits(t *testing.T) {
	ctx := context.Background()
	manager, _, _ := newTestRepository(t)
	addressLock := manager.Locks.For("renamed")
	addressLock.Lock()
	done := make(chan error, 1)
	go func() {
		_, err := manager.Rename(ctx, "sample", "renamed", time.Now())
		done <- err
	}()
	// The rename holds the repository and now waits for the new name.
	for !manager.InUse("sample") {
		runtime.Gosched()
	}
	hold, err := manager.HoldForBackup()
	noErr(t, err)
	defer hold.Close()
	hold.Set("sample")
	addressLock.Unlock()
	if err := <-done; !errors.Is(err, ErrBackupReading) {
		t.Fatalf("rename while a backup started holding the repository err=%v", err)
	}
	stored, _, err := manager.Store.Repository(ctx, "sample")
	noErr(t, err)
	if stored.Address != "sample" {
		t.Fatalf("refused rename changed the repository: %+v", stored)
	}
}
