package auth

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"owngit/internal/state"
)

// countingManager returns a manager over a new state with a shared access
// password, the state directory, a counter of full password checks and a
// function that moves the manager's clock.
func countingManager(t *testing.T) (*Manager, string, *atomic.Int64, func(time.Duration)) {
	t.Helper()
	directory := filepath.Join(t.TempDir(), "state")
	store, err := state.Open(context.Background(), directory)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	accessHash, _ := HashPassword("shared-password")
	adminHash, _ := HashPassword("admin-password")
	if err := store.CompleteSetup(context.Background(), t.TempDir(), "password", accessHash, adminHash, true); err != nil {
		t.Fatal(err)
	}
	var runs atomic.Int64
	now := time.Unix(1_800_000_000, 0)
	manager := &Manager{
		Store: store,
		Now:   func() time.Time { return now },
		passwordCheck: func(encoded, password string) bool {
			runs.Add(1)
			return CheckPassword(encoded, password)
		},
	}
	return manager, directory, &runs, func(step time.Duration) { now = now.Add(step) }
}

func TestSharedPasswordRunsArgon2idOnceForSeveralRequests(t *testing.T) {
	manager, _, runs, advance := countingManager(t)
	ctx := context.Background()
	for request := 0; request < 8; request++ {
		address := fmt.Sprintf("192.0.2.%d:4000", 10+request%2)
		if err := manager.VerifyCredential(ctx, "general", "shared-password", address); err != nil {
			t.Fatalf("request %d: %v", request, err)
		}
	}
	if got := runs.Load(); got != 1 {
		t.Fatalf("8 requests ran %d full password checks, want 1", got)
	}
	advance(rememberedCheckLife)
	if err := manager.VerifyCredential(ctx, "general", "shared-password", "192.0.2.10:4000"); err != nil {
		t.Fatal(err)
	}
	if got := runs.Load(); got != 2 {
		t.Fatalf("after the entry expired: %d full checks, want 2", got)
	}
}

func TestRememberedPasswordEndsWhenAnotherProcessChangesIt(t *testing.T) {
	manager, directory, runs, _ := countingManager(t)
	ctx := context.Background()
	if err := manager.VerifyCredential(ctx, "general", "shared-password", "192.0.2.10:4000"); err != nil {
		t.Fatal(err)
	}
	other, err := state.Open(ctx, directory)
	if err != nil {
		t.Fatal(err)
	}
	replacement, _ := HashPassword("replacement-password")
	if err := other.SetAccessPassword(ctx, replacement); err != nil {
		t.Fatal(err)
	}
	other.Close()
	if err := manager.VerifyCredential(ctx, "general", "shared-password", "192.0.2.10:4000"); err == nil {
		t.Fatal("the old password was accepted after another process changed it")
	}
	if got := runs.Load(); got != 2 {
		t.Fatalf("the old password ran %d full checks in total, want 2 (it must be checked again)", got)
	}
	if err := manager.VerifyCredential(ctx, "general", "replacement-password", "192.0.2.10:4000"); err != nil {
		t.Fatalf("new password: %v", err)
	}
	// Setting the same password again stores a new salt and hash, so the
	// remembered check ends as well.
	again, _ := HashPassword("replacement-password")
	if err := manager.Store.SetAccessPassword(ctx, again); err != nil {
		t.Fatal(err)
	}
	before := runs.Load()
	if err := manager.VerifyCredential(ctx, "general", "replacement-password", "192.0.2.10:4000"); err != nil {
		t.Fatal(err)
	}
	if runs.Load() != before+1 {
		t.Fatal("a check remembered for the previous hash was reused")
	}
}

func TestRememberedPasswordDoesNotPassABlockedAddress(t *testing.T) {
	manager, _, runs, _ := countingManager(t)
	ctx := context.Background()
	if err := manager.VerifyCredential(ctx, "general", "shared-password", "192.0.2.10:4000"); err != nil {
		t.Fatal(err)
	}
	for attempt := 0; attempt < maximumFailures; attempt++ {
		if err := manager.VerifyCredential(ctx, "general", "wrong-password", "192.0.2.20:4000"); err == nil || errors.Is(err, ErrRateLimited) {
			t.Fatalf("wrong password %d: %v, want an ordinary failure", attempt+1, err)
		}
	}
	if got := runs.Load(); got != 1+maximumFailures {
		t.Fatalf("%d full checks, want one for the correct password and one per wrong password", got)
	}
	if err := manager.VerifyCredential(ctx, "general", "shared-password", "192.0.2.20:4000"); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("remembered password from a blocked address: %v, want the rate limit", err)
	}
	if err := manager.VerifyCredential(ctx, "general", "shared-password", "192.0.2.10:4000"); err != nil {
		t.Fatalf("another address: %v", err)
	}
	if got := runs.Load(); got != 1+maximumFailures {
		t.Fatalf("%d full checks, want no more after the limit", got)
	}
}

func TestWrongPasswordsAndAdministratorChecksAreNotRemembered(t *testing.T) {
	manager, _, runs, _ := countingManager(t)
	ctx := context.Background()
	for attempt := 0; attempt < 2; attempt++ {
		if err := manager.VerifyCredential(ctx, "general", "wrong-password", "192.0.2.30:4000"); err == nil {
			t.Fatal("a wrong password was accepted")
		}
	}
	if got := runs.Load(); got != 2 {
		t.Fatalf("two wrong passwords ran %d full checks, want 2", got)
	}
	// The failures counted: two more reach the limit.
	for attempt := 0; attempt < maximumFailures-2; attempt++ {
		_ = manager.VerifyCredential(ctx, "general", "wrong-password", "192.0.2.30:4000")
	}
	if err := manager.VerifyCredential(ctx, "general", "shared-password", "192.0.2.30:4000"); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("after %d wrong passwords: %v, want the rate limit", maximumFailures, err)
	}
	before := runs.Load()
	for attempt := 0; attempt < 3; attempt++ {
		if err := manager.VerifyCredential(ctx, "admin", "admin-password", "192.0.2.31:4000"); err != nil {
			t.Fatal(err)
		}
	}
	if got := runs.Load() - before; got != 3 {
		t.Fatalf("three administrator checks ran %d full checks, want 3", got)
	}
}

func TestRememberedChecksStayBounded(t *testing.T) {
	var checks rememberedChecks
	now := time.Unix(1_800_000_000, 0)
	var first []byte
	for index := 0; index < rememberedCheckLimit+4; index++ {
		digest := checks.digest("general", "hash", fmt.Sprintf("password-%d", index))
		if index == 0 {
			first = digest
		}
		checks.add(digest, now.Add(time.Duration(index)*time.Second))
	}
	if len(checks.entries) != rememberedCheckLimit {
		t.Fatalf("%d entries, want the limit %d", len(checks.entries), rememberedCheckLimit)
	}
	if checks.contains(first, now.Add(time.Minute)) {
		t.Fatal("the oldest entry was kept past the limit")
	}
	last := checks.digest("general", "hash", fmt.Sprintf("password-%d", rememberedCheckLimit+3))
	if !checks.contains(last, now.Add(time.Minute)) {
		t.Fatal("the newest entry was dropped")
	}
	checks.add(checks.digest("general", "hash", "later"), now.Add(time.Hour))
	if len(checks.entries) != 1 {
		t.Fatalf("%d entries after all others expired, want 1", len(checks.entries))
	}
	if checks.contains(checks.digest("admin", "hash", "later"), now.Add(time.Hour)) {
		t.Fatal("an entry matched another credential kind")
	}
}
