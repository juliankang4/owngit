package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"owngit/internal/auth"
	"owngit/internal/state"
)

// unstableOpens makes the next count state opens fail as if the running
// server wrote to the state directory during inspection, and counts every
// attempt.
func unstableOpens(t *testing.T, count int) *int {
	t.Helper()
	attempts := 0
	original := openLiveStateAttempt
	openLiveStateAttempt = func(ctx context.Context, stateDir string) (*state.Store, error) {
		attempts++
		if attempts <= count {
			return nil, fmt.Errorf("%w: state.sqlite-wal changed", state.ErrInspectionUnstable)
		}
		return original(ctx, stateDir)
	}
	t.Cleanup(func() { openLiveStateAttempt = original })
	return &attempts
}

// QA-062: approve-host and reset-admin work while the running server writes
// to the state directory.
func TestOfflineCommandsRetryWhenTheServerWritesDuringInspection(t *testing.T) {
	root := t.TempDir()
	stateDir := filepath.Join(root, "state")
	store, err := state.Open(context.Background(), stateDir)
	noErr(t, err)
	accessHash, _ := auth.HashPassword("shared-password")
	adminHash, _ := auth.HashPassword("old-admin-password")
	noErr(t, store.CompleteSetup(context.Background(), filepath.Join(root, "repositories"), "password", accessHash, adminHash, true))
	noErr(t, store.Close())

	attempts := unstableOpens(t, 2)
	noErr(t, approveHost([]string{"--state-dir", stateDir, "gitbox.test"}))
	if *attempts != 3 {
		t.Fatalf("approve-host opened the state %d times, want 3", *attempts)
	}

	passwordFile := filepath.Join(root, "new-password")
	noErr(t, os.WriteFile(passwordFile, []byte("new-admin-password\n"), 0o600))
	noErr(t, state.ProtectPrivatePath(passwordFile, false))
	attempts = unstableOpens(t, liveStateAttempts-1)
	noErr(t, resetAdmin([]string{"--state-dir", stateDir, "--password-file", passwordFile}))
	if *attempts != liveStateAttempts {
		t.Fatalf("reset-admin opened the state %d times, want %d", *attempts, liveStateAttempts)
	}

	store, err = state.Open(context.Background(), stateDir)
	noErr(t, err)
	defer store.Close()
	hosts, err := store.TrustedHosts(context.Background())
	noErr(t, err)
	if !slices.Contains(hosts, "gitbox.test") {
		t.Fatalf("trusted hosts=%v, want gitbox.test", hosts)
	}
	encoded, err := store.PasswordHash(context.Background(), "admin")
	noErr(t, err)
	if !auth.CheckPassword(encoded, "new-admin-password") {
		t.Fatal("the new administrator password was not saved")
	}
}

func TestOfflineCommandsStopRetryingAfterFiveAttempts(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), "state")
	store, err := state.Open(context.Background(), stateDir)
	noErr(t, err)
	noErr(t, store.Close())

	attempts := unstableOpens(t, liveStateAttempts)
	err = approveHost([]string{"--state-dir", stateDir, "gitbox.test"})
	if !errors.Is(err, state.ErrInspectionUnstable) || *attempts != liveStateAttempts {
		t.Fatalf("err=%v after %d attempts, want the inspection error after %d", err, *attempts, liveStateAttempts)
	}

	// Other errors are reported at once.
	original := openLiveStateAttempt
	attempts = new(int)
	openLiveStateAttempt = func(ctx context.Context, stateDir string) (*state.Store, error) {
		*attempts++
		return nil, errors.New("state database is newer than this OwnGit")
	}
	defer func() { openLiveStateAttempt = original }()
	err = approveHost([]string{"--state-dir", stateDir, "gitbox.test"})
	if err == nil || !strings.Contains(err.Error(), "newer") || *attempts != 1 {
		t.Fatalf("err=%v after %d attempts, want the error after one attempt", err, *attempts)
	}
}
