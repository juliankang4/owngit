package state

import (
	"context"
	"errors"
	"strings"
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

// A raw log is kept for the chosen time from when its check started. A new
// choice applies at once to every log kept: a log past it can no longer be
// read and the next cleanup deletes it, while Keep indefinitely keeps them
// all readable through any cleanup. Durable attempt records stay.
func TestRawLogRetentionAppliesToTheLogsKept(t *testing.T) {
	store, ctx, now := newProjectStore(t)
	task, err := store.CreateTask(ctx, "project", "Retention", now)
	noErr(t, err)
	_, older := recordAttemptWithLog(t, store, attemptFor(task, "1111111111111111111111111111111111111111", now, AttemptFailed), "older output")
	_, newer := recordAttemptWithLog(t, store, attemptFor(task, "2222222222222222222222222222222222222222", now.Add(20*24*time.Hour), AttemptFailed), "newer output")
	if want := now.Add(30 * 24 * time.Hour); !older.LogExpiresAt.Equal(want) {
		t.Fatalf("default expiry %v, want %v", older.LogExpiresAt, want)
	}
	save := func(retention CheckLogRetention) {
		t.Helper()
		noErr(t, store.SavePolicies(ctx, PolicyChange{CheckLogs: &retention}))
	}
	logState := func(attempt CheckAttempt, at time.Time) string {
		t.Helper()
		reloaded, exists, err := store.CheckAttemptByID(ctx, attempt.RepositoryID, attempt.ID)
		noErr(t, err)
		if !exists {
			t.Fatalf("attempt %s is gone", attempt.ID)
		}
		_, found, err := store.ReadCheckLog(reloaded.LogID, reloaded.LogExpiresAt, at)
		noErr(t, err)
		return found
	}

	save(KeepCheckLogs)
	later := now.Add(3 * 365 * 24 * time.Hour)
	if removed, err := store.PruneCheckLogs(ctx, later); err != nil || removed != 0 {
		t.Fatalf("a cleanup under Keep indefinitely removed %d, err=%v", removed, err)
	}
	if got := logState(older, later); got != CheckLogFound {
		t.Fatalf("a log kept indefinitely reads as %s", got)
	}
	reloaded, _, err := store.CheckAttemptByID(ctx, older.RepositoryID, older.ID)
	noErr(t, err)
	if CheckLogExpiry(reloaded.LogExpiresAt) != nil {
		t.Fatalf("a log kept indefinitely has the expiry %v", reloaded.LogExpiresAt)
	}

	save(CheckLogs7Days)
	cleanup := now.Add(10 * 24 * time.Hour)
	if got := logState(older, cleanup); got != CheckLogExpired {
		t.Fatalf("a log past the new time reads as %s", got)
	}
	if removed, err := store.PruneCheckLogs(ctx, cleanup); err != nil || removed != 1 {
		t.Fatalf("the next cleanup removed %d, err=%v", removed, err)
	}
	if got := logState(newer, cleanup); got != CheckLogFound {
		t.Fatalf("a log within the new time reads as %s", got)
	}
	if _, exists, err := store.CheckAttemptByID(ctx, older.RepositoryID, older.ID); err != nil || !exists {
		t.Fatalf("the attempt record went with its log: exists=%v err=%v", exists, err)
	}

	noErr(t, store.Exec(ctx, `UPDATE metadata SET value='45' WHERE key='check_log_retention_days'`))
	var policyErr *PolicyError
	if _, err := store.CheckLogRetention(ctx); !errors.As(err, &policyErr) {
		t.Fatalf("an unknown retention read with err=%v", err)
	}
	third := attemptFor(task, "3333333333333333333333333333333333333333", now.Add(21*24*time.Hour), AttemptFailed)
	_, _, err = store.RegisterCheckAttempt(ctx, third)
	noErr(t, err)
	// The result is kept without its raw log, and the attempt says which
	// setting to set again.
	_, stored, err := store.CompleteCheckAttempt(ctx, completionFor(third, "third output"), third.CreatedAt)
	noErr(t, err)
	if stored.Status != AttemptFailed || len(stored.Results) != 1 || stored.Results[0].OutputExcerpt != "out" ||
		stored.LogID != "" || stored.LogExpiresAt != nil || !strings.Contains(stored.LogError, "--check-logs") {
		t.Fatalf("attempt stored under an unknown retention: %+v", stored)
	}
	if count, err := store.TableRowCount(ctx, "check_raw_logs"); err != nil || count != 1 {
		t.Fatalf("raw logs=%d err=%v, want only the newer one", count, err)
	}
}
