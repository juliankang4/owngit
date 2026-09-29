package state

import (
	"context"
	"errors"
	"testing"
	"time"
)

// Nothing saved means each policy's default. A saved value that does not
// parse, is out of bounds or carries a field this build does not know is a
// PolicyError where the policy is read, never the default; saving the
// policy again replaces it.
func TestPoliciesReadTheirDefaultsAndRefuseWhatTheyCannotUse(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	if session, err := store.GeneralSession(ctx); err != nil || session != Session12Hours {
		t.Fatalf("session=%q err=%v", session, err)
	}
	if branch, err := store.InitialBranch(ctx); err != nil || branch != "main" {
		t.Fatalf("branch=%q err=%v", branch, err)
	}
	if limits, err := store.GitTransferLimits(ctx); err != nil || limits != DefaultGitTransferLimits {
		t.Fatalf("limits=%+v err=%v", limits, err)
	}
	for _, stored := range []struct{ key, value string }{
		{"general_session_seconds", "5"},
		{"general_session_seconds", "9223372036854775807"},
		{"initial_branch", "has space"},
		{"git_transfer_limits", `{"maximum_bytes":0}`},
		{"git_transfer_limits", `{"operation_seconds":9223372036854775807}`},
		{"git_transfer_limits", `{"concurrent":8}`},
		{"git_transfer_limits", `{} {}`},
	} {
		noErr(t, store.Exec(ctx, `INSERT INTO metadata(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, stored.key, stored.value))
		var err error
		switch stored.key {
		case "general_session_seconds":
			_, err = store.GeneralSession(ctx)
		case "initial_branch":
			_, err = store.InitialBranch(ctx)
		default:
			_, err = store.GitTransferLimits(ctx)
		}
		var policyErr *PolicyError
		if !errors.As(err, &policyErr) || policyErr.Key != stored.key {
			t.Fatalf("%s=%s read with err=%v, want a PolicyError", stored.key, stored.value, err)
		}
	}
	session, branch := Session30Days, "trunk"
	limits := GitTransferLimits{MaximumBytes: 64 << 30, Operation: 24 * time.Hour}
	noErr(t, store.SavePolicies(ctx, PolicyChange{Session: &session, InitialBranch: &branch, GitTransfer: &limits}))
	if saved, err := store.GeneralSession(ctx); err != nil || saved != session {
		t.Fatalf("session=%q err=%v", saved, err)
	}
	if saved, err := store.InitialBranch(ctx); err != nil || saved != branch {
		t.Fatalf("branch=%q err=%v", saved, err)
	}
	if saved, err := store.GitTransferLimits(ctx); err != nil || saved != limits {
		t.Fatalf("limits=%+v err=%v", saved, err)
	}
}

// A change with one value out of bounds saves none of them.
func TestSavePoliciesSavesAllOrNothing(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	branch := "trunk"
	tooLong := GitTransferLimits{MaximumBytes: 4 << 30, Operation: 25 * time.Hour}
	if err := store.SavePolicies(ctx, PolicyChange{InitialBranch: &branch, GitTransfer: &tooLong}); err == nil {
		t.Fatal("a transfer longer than 24 hours was saved")
	}
	if saved, err := store.InitialBranch(ctx); err != nil || saved != "main" {
		t.Fatalf("branch=%q err=%v after a refused change", saved, err)
	}
}
