package auth

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"owngit/internal/state"
)

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
// failure limit, and parallel wrong passwords still stop at the limit
// (QA-018).
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
				errs[index] = manager.VerifyCredential(context.Background(), "general", password, "192.0.2.7:4000")
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
	if err := manager.VerifyCredential(context.Background(), "general", "shared-password", "192.0.2.7:4000"); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("correct password after the limit: %v, want the rate limit", err)
	}
	if err := manager.VerifyCredential(context.Background(), "general", "shared-password", "192.0.2.8:4000"); err != nil {
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
				err := manager.VerifyCredential(ctx, "general", failure.password, "192.0.2.40:4000")
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
