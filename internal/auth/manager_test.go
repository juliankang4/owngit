package auth

import (
	"context"
	"errors"
	"path/filepath"
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
