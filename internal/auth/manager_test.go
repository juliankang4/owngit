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
	manager := &Manager{Store: store, Now: func() time.Time { return now }, SessionLife: time.Hour}
	for attempt := 0; attempt < 4; attempt++ {
		if _, err := manager.Authenticate(context.Background(), "general", "wrong-password", "192.0.2.4:1234"); err == nil || errors.Is(err, ErrRateLimited) {
			t.Fatalf("attempt %d error=%v, want ordinary credential failure", attempt+1, err)
		}
	}
	if _, err := manager.Authenticate(context.Background(), "general", "shared-password", "192.0.2.4:1234"); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("fifth attempt error=%v, want rate limit before password verification", err)
	}
	now = now.Add(16 * time.Minute)
	session, err := manager.Authenticate(context.Background(), "general", "shared-password", "192.0.2.4:1234")
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
	manager := &Manager{Store: store, SessionLife: time.Hour}
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
		case err != nil:
			checked++
		default:
			t.Fatal("a wrong password was accepted")
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
