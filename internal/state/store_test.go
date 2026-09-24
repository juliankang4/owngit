package state

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestBootstrapRedemptionIsAtomic(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	now := time.Unix(1_800_000_000, 0)
	noErr(t, store.PutBootstrap(ctx, "owner-token", now.Add(time.Minute)))

	var redeemed atomic.Int32
	var wait sync.WaitGroup
	for index := 0; index < 12; index++ {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			ok, err := store.RedeemBootstrap(ctx, "owner-token", fmt.Sprintf("session-%d", index), fmt.Sprintf("csrf-%d", index), now, now.Add(time.Minute))
			if err != nil {
				t.Errorf("redeem: %v", err)
				return
			}
			if ok {
				redeemed.Add(1)
			}
		}(index)
	}
	wait.Wait()
	if got := redeemed.Load(); got != 1 {
		t.Fatalf("redeemed %d times, want exactly once", got)
	}
}

func TestBootstrapWriteAtomicallyRejectsCompletedSetup(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	noErr(t, store.CompleteSetup(ctx, t.TempDir(), "open", "", "admin-hash", true))
	if err := store.PutBootstrap(ctx, "late-token", time.Now().Add(time.Minute)); !errors.Is(err, ErrSetupComplete) {
		t.Fatalf("PutBootstrap after setup error=%v, want ErrSetupComplete", err)
	}
	snapshot, err := store.BootstrapSnapshot(ctx)
	if err != nil || snapshot.Present {
		t.Fatalf("completed setup regained bootstrap capability: snapshot=%+v err=%v", snapshot, err)
	}
}

func TestBootstrapExpiryAndReissueInvalidateOldCapability(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	now := time.Unix(1_800_000_000, 0)
	noErr(t, store.PutBootstrap(ctx, "expired", now.Add(-time.Second)))
	if ok, err := store.RedeemBootstrap(ctx, "expired", "session", "csrf", now, now.Add(time.Minute)); err != nil || ok {
		t.Fatalf("expired capability redeemed=%v err=%v", ok, err)
	}
	noErr(t, store.PutBootstrap(ctx, "first", now.Add(time.Minute)))
	noErr(t, store.PutBootstrap(ctx, "replacement", now.Add(time.Minute)))
	if ok, err := store.RedeemBootstrap(ctx, "first", "old-session", "csrf", now, now.Add(time.Minute)); err != nil || ok {
		t.Fatalf("reissued old capability redeemed=%v err=%v", ok, err)
	}
	if ok, err := store.RedeemBootstrap(ctx, "replacement", "new-session", "csrf", now, now.Add(time.Minute)); err != nil || !ok {
		t.Fatalf("replacement capability redeemed=%v err=%v", ok, err)
	}
}

func TestSetupAndCredentialModeTransitions(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	noErr(t, store.CompleteSetup(ctx, t.TempDir(), "open", "", "admin-hash", true))
	if err := store.CompleteSetup(ctx, t.TempDir(), "open", "", "other", true); err == nil {
		t.Fatal("second setup unexpectedly succeeded")
	}
	settings, err := store.Settings(ctx)
	noErr(t, err)
	if !settings.Initialized || settings.AccessMode != "open" || !settings.InsecureHTTPAccepted {
		t.Fatalf("unexpected settings: %+v", settings)
	}
	noErr(t, store.CreateSession(ctx, "old-general", "general", "csrf", settings.AccessSessionVersion, time.Now().Add(time.Hour)))
	noErr(t, store.SetAccessPassword(ctx, "access-hash"))
	if _, ok, err := store.Session(ctx, "old-general", "general", time.Now()); err != nil || ok {
		t.Fatalf("old session survived mode change: ok=%v err=%v", ok, err)
	}
	settings, _ = store.Settings(ctx)
	if settings.AccessMode != "password" || settings.AccessSessionVersion != 2 {
		t.Fatalf("password mode not recorded: %+v", settings)
	}
	noErr(t, store.DisableAccessPassword(ctx))
	settings, _ = store.Settings(ctx)
	if settings.AccessMode != "open" || settings.AccessSessionVersion != 3 {
		t.Fatalf("open mode not restored: %+v", settings)
	}
}

func TestOnlyOneConcurrentSetupCompletionWins(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	var completed atomic.Int32
	var wait sync.WaitGroup
	base := t.TempDir()
	for index := 0; index < 8; index++ {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			root := filepath.Join(base, fmt.Sprintf("repositories-%d", index))
			if err := store.CompleteSetup(ctx, root, "open", "", fmt.Sprintf("admin-hash-%d", index), true); err == nil {
				completed.Add(1)
			}
		}(index)
	}
	wait.Wait()
	if completed.Load() != 1 {
		t.Fatalf("setup completed %d times, want once", completed.Load())
	}
}

func TestSQLiteFileURIKeepsWindowsDriveInThePathAndEscapesReservedBytes(t *testing.T) {
	if got, want := sqliteFileURI("C:/OwnGit state/#?%.sqlite"), "file:///C:/OwnGit%20state/%23%3F%25.sqlite?_txlock=immediate"; got != want {
		t.Fatalf("Windows SQLite URI = %q, want %q", got, want)
	}
	if got, want := sqliteFileURI("/tmp/OwnGit state/#?%.sqlite"), "file:///tmp/OwnGit%20state/%23%3F%25.sqlite?_txlock=immediate"; got != want {
		t.Fatalf("Unix SQLite URI = %q, want %q", got, want)
	}
}

func TestStateOpenHandlesEscapedPathCharacters(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "OwnGit state # %")
	store, err := Open(context.Background(), directory)
	noErr(t, err)
	defer store.Close()
	if _, err := store.Settings(context.Background()); err != nil {
		t.Fatalf("read state from escaped path: %v", err)
	}
	if _, err := os.Stat(filepath.Join(directory, databaseName)); err != nil {
		t.Fatalf("state database was not created at the requested path: %v", err)
	}
}

func TestStateFilesAreOwnerOnly(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "state")
	store, err := Open(context.Background(), directory)
	noErr(t, err)
	defer store.Close()
	if databaseName != "owngit.sqlite" {
		t.Fatalf("database name = %q, want owngit.sqlite", databaseName)
	}
	assertStateStoragePrivate(t, directory)
}

func openTestStore(t *testing.T) *Store {
	t.Helper()
	store, err := Open(context.Background(), filepath.Join(t.TempDir(), "state"))
	noErr(t, err)
	t.Cleanup(func() { _ = store.Close() })
	return store
}

// noErr stops the test on an unexpected error. t.Helper keeps the failure
// line at the caller.
func noErr(t testing.TB, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

// newProjectStore opens a store that holds repository "project" and returns
// the context and fixed time its setup used.
func newProjectStore(t *testing.T) (*Store, context.Context, time.Time) {
	t.Helper()
	store := openTestStore(t)
	ctx := context.Background()
	now := time.Unix(1_800_000_000, 0)
	noErr(t, store.AddRepository(ctx, Repository{ID: "project", Name: "Project", CreatedAt: now}))
	return store, ctx, now
}

// newProjectTask creates the task most check tests record attempts for.
func newProjectTask(t *testing.T, store *Store, ctx context.Context, now time.Time) Task {
	t.Helper()
	task, err := store.CreateTask(ctx, "project", "Fix the build", now)
	noErr(t, err)
	return task
}

// The update check is on unless it was turned off, survives a restart, and a
// state written before the setting existed reads as on.
func TestUpdateCheckSettingDefaultsOnAndPersists(t *testing.T) {
	ctx := context.Background()
	directory := filepath.Join(t.TempDir(), "state")
	store, err := Open(ctx, directory)
	noErr(t, err)
	settings, err := store.Settings(ctx)
	noErr(t, err)
	if !settings.UpdateCheck {
		t.Fatal("a state without the setting reads as off")
	}
	noErr(t, store.SetUpdateCheck(ctx, false))
	noErr(t, store.Close())

	store, err = Open(ctx, directory)
	noErr(t, err)
	defer store.Close()
	if settings, err := store.Settings(ctx); err != nil || settings.UpdateCheck {
		t.Fatalf("after restart settings=%+v err=%v, want the check off", settings, err)
	}
	noErr(t, store.SetUpdateCheck(ctx, true))
	if settings, err := store.Settings(ctx); err != nil || !settings.UpdateCheck {
		t.Fatalf("settings=%+v err=%v, want the check on", settings, err)
	}

	// A value this build did not write is refused rather than guessed.
	_, err = store.db.ExecContext(ctx, `UPDATE metadata SET value='maybe' WHERE key=?`, updateCheckKey)
	noErr(t, err)
	if _, err := store.Settings(ctx); err == nil {
		t.Fatal("an unknown update check value was accepted")
	}
}
