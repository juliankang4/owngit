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
	if err := store.PutBootstrap(ctx, "owner-token", now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}

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
	if err := store.CompleteSetup(ctx, t.TempDir(), "open", "", "admin-hash", true); err != nil {
		t.Fatal(err)
	}
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
	if err := store.PutBootstrap(ctx, "expired", now.Add(-time.Second)); err != nil {
		t.Fatal(err)
	}
	if ok, err := store.RedeemBootstrap(ctx, "expired", "session", "csrf", now, now.Add(time.Minute)); err != nil || ok {
		t.Fatalf("expired capability redeemed=%v err=%v", ok, err)
	}
	if err := store.PutBootstrap(ctx, "first", now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := store.PutBootstrap(ctx, "replacement", now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
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
	if err := store.CompleteSetup(ctx, t.TempDir(), "open", "", "admin-hash", true); err != nil {
		t.Fatal(err)
	}
	if err := store.CompleteSetup(ctx, t.TempDir(), "open", "", "other", true); err == nil {
		t.Fatal("second setup unexpectedly succeeded")
	}
	settings, err := store.Settings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !settings.Initialized || settings.AccessMode != "open" || !settings.InsecureHTTPAccepted {
		t.Fatalf("unexpected settings: %+v", settings)
	}
	if err := store.CreateSession(ctx, "old-general", "general", "csrf", settings.AccessSessionVersion, time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := store.SetAccessPassword(ctx, "access-hash"); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := store.Session(ctx, "old-general", "general", time.Now()); err != nil || ok {
		t.Fatalf("old session survived mode change: ok=%v err=%v", ok, err)
	}
	settings, _ = store.Settings(ctx)
	if settings.AccessMode != "password" || settings.AccessSessionVersion != 2 {
		t.Fatalf("password mode not recorded: %+v", settings)
	}
	if err := store.DisableAccessPassword(ctx); err != nil {
		t.Fatal(err)
	}
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

func TestStateFilesAreOwnerOnly(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "state")
	store, err := Open(context.Background(), directory)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if databaseName != "owngit.sqlite" {
		t.Fatalf("database name = %q, want owngit.sqlite", databaseName)
	}
	for _, path := range []string{directory, filepath.Join(directory, "owngit.sqlite")} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if got := info.Mode().Perm(); got&0o077 != 0 {
			t.Fatalf("%s permissions are %o, want no group/other access", path, got)
		}
	}
}

func openTestStore(t *testing.T) *Store {
	t.Helper()
	store, err := Open(context.Background(), filepath.Join(t.TempDir(), "state"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}
