package auth

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"owngit/internal/state"
)

func TestAdminLoginUsesThePasswordVersionItVerified(t *testing.T) {
	for _, choice := range []state.AdminConfirmation{state.DefaultAdminConfirmation, state.ConfirmEveryTime} {
		t.Run(string(choice), func(t *testing.T) {
			ctx := context.Background()
			store, err := state.Open(ctx, filepath.Join(t.TempDir(), "state"))
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			accessHash, _ := HashPassword("shared-password")
			oldHash, _ := HashPassword("admin-password-old")
			newHash, _ := HashPassword("admin-password-new")
			if err := store.CompleteSetup(ctx, t.TempDir(), "password", accessHash, oldHash, true); err != nil {
				t.Fatal(err)
			}
			manager := &Manager{Store: store}
			if err := manager.SetAdminConfirmation(ctx, choice); err != nil {
				t.Fatal(err)
			}
			settings, err := store.Settings(ctx)
			if err != nil {
				t.Fatal(err)
			}
			oldVersion := settings.AdminSessionVersion
			original, err := manager.Authenticate(ctx, "admin", "admin-password-old", "192.0.2.1", "")
			if err != nil {
				t.Fatalf("unchanged administrator password: %v", err)
			}
			if _, valid, err := manager.ValidateSession(ctx, original.Token, "admin"); err != nil || !valid {
				t.Fatalf("unchanged password session valid=%v err=%v", valid, err)
			}
			manager.passwordCheck = func(encoded, password string) bool {
				accepted := CheckPassword(encoded, password)
				if err := store.SetAdminPassword(ctx, newHash); err != nil {
					t.Fatal(err)
				}
				return accepted
			}
			attempt, err := manager.Authenticate(ctx, "admin", "admin-password-old", "192.0.2.2", "")
			if !errors.Is(err, ErrInvalidCredentials) || attempt.Token != "" {
				t.Fatalf("replaced password login: token=%q err=%v", attempt.Token, err)
			}
			if _, valid, err := manager.ValidateSession(ctx, original.Token, "admin"); err != nil || valid {
				t.Fatalf("earlier session survived replacement: valid=%v err=%v", valid, err)
			}
			if _, err := manager.StartAdminSession(ctx, "", oldVersion); !errors.Is(err, ErrInvalidCredentials) {
				t.Fatalf("stale administrator session insertion: %v", err)
			}
			manager.passwordCheck = nil
			current, err := manager.Authenticate(ctx, "admin", "admin-password-new", "192.0.2.2", "")
			if err != nil {
				t.Fatalf("current password login: %v", err)
			}
			if _, valid, err := manager.ValidateSession(ctx, current.Token, "admin"); err != nil || !valid {
				t.Fatalf("current password session valid=%v err=%v", valid, err)
			}
		})
	}
}

func TestAdminConfirmationRejectsAReplacedPassword(t *testing.T) {
	ctx := context.Background()
	store, err := state.Open(ctx, filepath.Join(t.TempDir(), "state"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	accessHash, _ := HashPassword("shared-password")
	oldHash, _ := HashPassword("admin-password-old")
	newHash, _ := HashPassword("admin-password-new")
	if err := store.CompleteSetup(ctx, t.TempDir(), "password", accessHash, oldHash, true); err != nil {
		t.Fatal(err)
	}
	manager := &Manager{Store: store}
	manager.passwordCheck = func(encoded, password string) bool {
		accepted := CheckPassword(encoded, password)
		if err := store.SetAdminPassword(ctx, newHash); err != nil {
			t.Fatal(err)
		}
		return accepted
	}
	if _, err := manager.VerifyCredential(ctx, "admin", "admin-password-old", "192.0.2.1"); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("replaced administrator password authorized a change: %v", err)
	}
	manager.passwordCheck = nil
	if _, err := manager.VerifyCredential(ctx, "admin", "admin-password-new", "192.0.2.1"); err != nil {
		t.Fatalf("current administrator password refused: %v", err)
	}
}
