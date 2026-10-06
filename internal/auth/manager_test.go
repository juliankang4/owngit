package auth

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"owngit/internal/state"
)

// maximumFailures is the number of wrong passwords that pause an address
// while no login limits were saved (state.DefaultLoginLimits).
const maximumFailures = 4

func TestAuthenticationAttemptsAreBoundedAndSessionsAreVersioned(t *testing.T) {
	store, err := state.Open(context.Background(), filepath.Join(t.TempDir(), "state"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	accessHash, _ := HashPassword("shared-password")
	adminHash, _ := HashPassword("admin-password")
	if err := store.CompleteSetup(context.Background(), t.TempDir(), "password", accessHash, adminHash, true); err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1_800_000_000, 0)
	manager := &Manager{Store: store, Now: func() time.Time { return now }}
	for attempt := 0; attempt < 4; attempt++ {
		if _, err := manager.Authenticate(context.Background(), "general", "wrong-password", "192.0.2.4:1234", ""); !errors.Is(err, ErrInvalidCredentials) {
			t.Fatalf("attempt %d error=%v, want ordinary credential failure", attempt+1, err)
		}
	}
	if _, err := manager.Authenticate(context.Background(), "general", "shared-password", "192.0.2.4:1234", ""); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("fifth attempt error=%v, want rate limit before password verification", err)
	}
	now = now.Add(16 * time.Minute)
	session, err := manager.Authenticate(context.Background(), "general", "shared-password", "192.0.2.4:1234", "")
	if err != nil {
		t.Fatalf("correct credential remained blocked after bounded interval: %v", err)
	}
	if _, ok, err := manager.ValidateSession(context.Background(), session.Token, "general"); err != nil || !ok {
		t.Fatalf("new session valid=%v err=%v", ok, err)
	}
	newAccessHash, _ := HashPassword("replacement-password")
	if err := store.SetAccessPassword(context.Background(), newAccessHash); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := manager.ValidateSession(context.Background(), session.Token, "general"); err != nil || ok {
		t.Fatalf("old session survived password change: valid=%v err=%v", ok, err)
	}
}

// Parallel requests with the correct password are never refused by the
// failure limit, and parallel wrong passwords still stop at the limit.
func TestParallelCorrectPasswordsAreAcceptedAndParallelGuessesStopAtTheLimit(t *testing.T) {
	store, err := state.Open(context.Background(), filepath.Join(t.TempDir(), "state"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	accessHash, _ := HashPassword("shared-password")
	adminHash, _ := HashPassword("admin-password")
	if err := store.CompleteSetup(context.Background(), t.TempDir(), "password", accessHash, adminHash, true); err != nil {
		t.Fatal(err)
	}
	manager := &Manager{Store: store}
	parallel := func(count int, password string) []error {
		errs := make([]error, count)
		var group sync.WaitGroup
		for index := range errs {
			group.Add(1)
			go func() {
				defer group.Done()
				_, errs[index] = manager.VerifyCredential(context.Background(), "general", password, "192.0.2.7:4000")
			}()
		}
		group.Wait()
		return errs
	}
	for round := 0; round < 3; round++ {
		for index, err := range parallel(16, "shared-password") {
			if err != nil {
				t.Fatalf("round %d: parallel correct request %d refused: %v", round, index, err)
			}
		}
	}
	checked, limited := 0, 0
	for _, err := range parallel(20, "wrong-password") {
		switch {
		case errors.Is(err, ErrRateLimited):
			limited++
		case errors.Is(err, ErrInvalidCredentials):
			checked++
		default:
			t.Fatalf("a wrong password gave %v", err)
		}
	}
	if checked != maximumFailures || limited != 20-maximumFailures {
		t.Fatalf("parallel guesses: %d checked and %d refused, want %d checked", checked, limited, maximumFailures)
	}
	if _, err := manager.VerifyCredential(context.Background(), "general", "shared-password", "192.0.2.7:4000"); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("correct password after the limit: %v, want the rate limit", err)
	}
	if _, err := manager.VerifyCredential(context.Background(), "general", "shared-password", "192.0.2.8:4000"); err != nil {
		t.Fatalf("another address was refused: %v", err)
	}
	if len(manager.clients) != 0 {
		t.Fatalf("%d idle client turns were kept", len(manager.clients))
	}
}

// A password check that could not be completed is neither a wrong password
// nor a rate limit, keeps its cause, and does not count toward the limit.
func TestUnfinishedPasswordChecksAreNotInvalidCredentials(t *testing.T) {
	broken := func(statement string) func(*testing.T, *Manager) context.Context {
		return func(t *testing.T, manager *Manager) context.Context {
			noError(t, manager.Store.Exec(context.Background(), statement))
			return context.Background()
		}
	}
	for _, failure := range []struct {
		name, password string
		inject         func(*testing.T, *Manager) context.Context
		cause          error
	}{
		{"request deadline passed", "shared-password", func(t *testing.T, _ *Manager) context.Context {
			ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
			t.Cleanup(cancel)
			return ctx
		}, context.DeadlineExceeded},
		{"request ends during the hash of a correct password", "shared-password", func(t *testing.T, manager *Manager) context.Context {
			ctx, cancel := context.WithCancel(context.Background())
			t.Cleanup(cancel)
			manager.passwordCheck = func(encoded, password string) bool {
				cancel()
				return CheckPassword(encoded, password)
			}
			return ctx
		}, context.Canceled},
		{"password lookup fails", "shared-password", broken(`ALTER TABLE passwords RENAME TO passwords_moved`), nil},
		{"wrong password not recorded", "wrong-password", broken(`CREATE TRIGGER refuse_recording BEFORE INSERT ON login_attempts BEGIN SELECT RAISE(ABORT, 'injected failure'); END`), nil},
		{"stored password damaged", "shared-password", broken(`UPDATE passwords SET encoded='not-a-password-hash' WHERE kind='access'`), nil},
		{"stored password missing", "shared-password", broken(`DELETE FROM passwords WHERE kind='access'`), nil},
	} {
		t.Run(failure.name, func(t *testing.T) {
			manager, _, _, _ := countingManager(t)
			ctx := failure.inject(t, manager)
			for attempt := 0; attempt <= maximumFailures; attempt++ {
				_, err := manager.VerifyCredential(ctx, "general", failure.password, "192.0.2.40:4000")
				if err == nil || errors.Is(err, ErrInvalidCredentials) || errors.Is(err, ErrRateLimited) || (failure.cause != nil && !errors.Is(err, failure.cause)) {
					t.Fatalf("attempt %d: %v", attempt+1, err)
				}
			}
		})
	}
}

func noError(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

// A client that chooses its own address cannot guess the administrator
// password without limit: wrong passwords from any addresses pause every
// administrator check, the right password included, until the pause ends or
// the password is replaced. Shared password checks are not affected.
func TestAdministratorFailuresFromChangingAddressesPauseEveryAdministratorCheck(t *testing.T) {
	store, err := state.Open(context.Background(), filepath.Join(t.TempDir(), "state"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	accessHash, _ := HashPassword("shared-password")
	adminHash, _ := HashPassword("admin-password")
	if err := store.CompleteSetup(context.Background(), t.TempDir(), "password", accessHash, adminHash, true); err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1_800_000_000, 0)
	manager := &Manager{Store: store, Now: func() time.Time { return now }}
	check := func(kind, password string, client int) error {
		_, err := manager.VerifyCredential(context.Background(), kind, password, fmt.Sprintf("192.0.2.%d:1234", client))
		return err
	}
	for client := 1; client <= maximumFailures*5; client++ {
		if err := check("admin", "wrong-password", client); !errors.Is(err, ErrInvalidCredentials) {
			t.Fatalf("failure %d from a new address: %v, want an ordinary credential failure", client, err)
		}
	}
	err = check("admin", "admin-password", 200)
	if !errors.Is(err, ErrRateLimited) {
		t.Fatalf("right administrator password after the cap: %v, want a rate limit", err)
	}
	if RetryAfter(err) < 1 {
		t.Fatalf("retry after = %d, want a pause", RetryAfter(err))
	}
	if err := check("general", "shared-password", 200); err != nil {
		t.Fatalf("shared password during the administrator pause: %v", err)
	}
	now = now.Add(16 * time.Minute)
	if err := check("admin", "admin-password", 200); err != nil {
		t.Fatalf("right administrator password after the pause: %v", err)
	}
	// The owner's own address is blocked by its own failures first, then
	// other addresses reach the server-wide cap. The reset frees both.
	for range maximumFailures {
		_ = check("admin", "wrong-password", 300)
	}
	if err := check("admin", "admin-password", 300); !errors.Is(err, ErrRateLimited) || IsServerWide(err) {
		t.Fatalf("owner address before the cap: %v, want its own pause", err)
	}
	for client := 1; client <= maximumFailures*5; client++ {
		_ = check("admin", "wrong-password", client)
	}
	newHash, _ := HashPassword("new-admin-password")
	if err := store.SetAdminPassword(context.Background(), newHash); err != nil {
		t.Fatal(err)
	}
	if err := check("admin", "new-admin-password", 300); err != nil {
		t.Fatalf("new administrator password from a blocked address after reset: %v", err)
	}
}

// Requests that wait for a check slot are held to the server-wide cap, and a
// right password checked against a replaced administrator password does not
// clear it.
func TestServerWideCapHoldsForQueuedChecksAndStalePasswords(t *testing.T) {
	ctx := context.Background()
	store, err := state.Open(ctx, filepath.Join(t.TempDir(), "state"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	accessHash, _ := HashPassword("shared-password")
	adminHash, _ := HashPassword("admin-password")
	if err := store.CompleteSetup(ctx, t.TempDir(), "password", accessHash, adminHash, true); err != nil {
		t.Fatal(err)
	}
	one := state.LoginLimits{Attempts: 1, Window: 10 * time.Minute, Pause: 15 * time.Minute}
	if err := store.SavePolicies(ctx, state.PolicyChange{LoginLimits: &one}); err != nil {
		t.Fatal(err)
	}
	manager := &Manager{Store: store, MaximumConcurrentChecks: 1}
	var group sync.WaitGroup
	results := make([]error, 30)
	for index := range results {
		group.Add(1)
		go func() {
			defer group.Done()
			_, results[index] = manager.VerifyCredential(ctx, "admin", "wrong-password", fmt.Sprintf("192.0.2.%d:1", index+1))
		}()
	}
	group.Wait()
	checked, limited := 0, 0
	for _, err := range results {
		switch {
		case errors.Is(err, ErrRateLimited):
			limited++
		case errors.Is(err, ErrInvalidCredentials):
			checked++
		default:
			t.Fatalf("a wrong password gave %v", err)
		}
	}
	if want := state.ServerWideFailureFactor; checked != want || limited != len(results)-want {
		t.Fatalf("queued guesses: %d checked and %d refused, want %d checked", checked, limited, want)
	}

	// The password is replaced while a check of the old one runs; failures
	// then trip the cap, and the old check still succeeds against its hash.
	store2, err := state.Open(ctx, filepath.Join(t.TempDir(), "state"))
	if err != nil {
		t.Fatal(err)
	}
	defer store2.Close()
	if err := store2.CompleteSetup(ctx, t.TempDir(), "password", accessHash, adminHash, true); err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1_800_000_000, 0)
	stale := &Manager{Store: store2, Now: func() time.Time { return now }}
	stale.passwordCheck = func(string, string) bool {
		newHash, _ := HashPassword("new-admin-password")
		if err := store2.SetAdminPassword(ctx, newHash); err != nil {
			t.Error(err)
		}
		for client := range maximumFailures * state.ServerWideFailureFactor {
			_ = store2.RecordFailedAttempt(ctx, "admin", fmt.Sprintf("198.51.100.%d", client+1), now)
		}
		return true
	}
	if _, err := stale.VerifyCredential(ctx, "admin", "admin-password", "192.0.2.1:1"); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("replaced password: %v, want invalid credentials", err)
	}
	if remaining, err := store2.AdminServerBlocked(ctx, now); err != nil || remaining <= 0 {
		t.Fatalf("server-wide pause after a stale success: %v, %v", remaining, err)
	}
}
