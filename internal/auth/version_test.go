package auth

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"owngit/internal/state"
)

func TestLoginUsesThePasswordVersionItVerified(t *testing.T) {
	ctx := context.Background()
	store, err := state.Open(ctx, filepath.Join(t.TempDir(), "state"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	oldHash, _ := HashPassword("shared-password-old")
	newHash, _ := HashPassword("shared-password-new")
	adminHash, _ := HashPassword("admin-password")
	if err := store.CompleteSetup(ctx, t.TempDir(), "password", oldHash, adminHash, true); err != nil {
		t.Fatal(err)
	}
	settings, err := store.Settings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	oldVersion := settings.AccessSessionVersion
	manager := &Manager{Store: store}
	original, err := manager.Authenticate(ctx, "general", "shared-password-old", "192.0.2.1", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, valid, err := manager.ValidateSession(ctx, original.Token, "general"); err != nil || !valid {
		t.Fatalf("unchanged password session valid=%v err=%v", valid, err)
	}

	// Use another manager so the earlier successful password is not cached.
	manager = &Manager{Store: store}
	// The stored hash has already been read when the concurrent change occurs.
	manager.passwordCheck = func(encoded, password string) bool {
		accepted := CheckPassword(encoded, password)
		if err := store.SetAccessPassword(ctx, newHash); err != nil {
			t.Fatal(err)
		}
		return accepted
	}
	attempt, err := manager.Authenticate(ctx, "general", "shared-password-old", "192.0.2.2", "")
	if !errors.Is(err, ErrInvalidCredentials) || attempt.Token != "" {
		t.Fatalf("replaced password login: token=%q err=%v", attempt.Token, err)
	}
	if _, valid, err := manager.ValidateSession(ctx, original.Token, "general"); err != nil || valid {
		t.Fatalf("earlier session survived password change: valid=%v err=%v", valid, err)
	}
	if err := store.StartSession(ctx, "", "stale-token", "general", "csrf", oldVersion, original.Expires); !errors.Is(err, state.ErrAccessChanged) {
		t.Fatalf("stale session insertion error=%v", err)
	}
	if _, valid, err := manager.ValidateSession(ctx, "stale-token", "general"); err != nil || valid {
		t.Fatalf("stale session stored: valid=%v err=%v", valid, err)
	}
	manager.passwordCheck = nil
	current, err := manager.Authenticate(ctx, "general", "shared-password-new", "192.0.2.2", "")
	if err != nil {
		t.Fatalf("new password login: %v", err)
	}
	if _, valid, err := manager.ValidateSession(ctx, current.Token, "general"); err != nil || !valid {
		t.Fatalf("new password session valid=%v err=%v", valid, err)
	}
}

func TestVerifiedSharedPasswordMustStillBeCurrentForBasicAuth(t *testing.T) {
	ctx := context.Background()
	store, err := state.Open(ctx, filepath.Join(t.TempDir(), "state"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	oldHash, _ := HashPassword("shared-password-old")
	newHash, _ := HashPassword("shared-password-new")
	adminHash, _ := HashPassword("admin-password")
	if err := store.CompleteSetup(ctx, t.TempDir(), "password", oldHash, adminHash, true); err != nil {
		t.Fatal(err)
	}
	manager := &Manager{Store: store}
	manager.passwordCheck = func(encoded, password string) bool {
		accepted := CheckPassword(encoded, password)
		if err := store.SetAccessPassword(ctx, newHash); err != nil {
			t.Fatal(err)
		}
		return accepted
	}
	if _, err := manager.VerifyCredential(ctx, "general", "shared-password-old", "192.0.2.1"); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("replaced password was accepted: %v", err)
	}
	manager.passwordCheck = nil
	if _, err := manager.VerifyCredential(ctx, "general", "shared-password-new", "192.0.2.1"); err != nil {
		t.Fatalf("current password was refused: %v", err)
	}
	if err := store.DisableAccessPassword(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.VerifyCredential(ctx, "general", "shared-password-new", "192.0.2.1"); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("disabled password was accepted: %v", err)
	}
	settings, err := store.Settings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.StartSession(ctx, "", "disabled-token", "general", "csrf", settings.AccessSessionVersion, manager.now().Add(state.DefaultLoginLimits.Pause)); !errors.Is(err, state.ErrAccessChanged) {
		t.Fatalf("disabled mode allowed session insertion: %v", err)
	}
}
