package state

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

// recordAttempt registers and completes one attempt in one step, which is what
// a helper does when it has already run the checks.
func recordAttempt(t *testing.T, store *Store, attempt CheckAttempt) (Task, CheckAttempt) {
	t.Helper()
	ctx := context.Background()
	task, registered, err := store.RegisterCheckAttempt(ctx, attempt)
	if err != nil {
		t.Fatalf("register attempt: %v", err)
	}
	completion := CheckCompletion{
		AttemptID: registered.ID, RepositoryID: registered.RepositoryID, TaskID: registered.TaskID,
		Results: attempt.Results, Cancelled: attempt.Status == AttemptCancelled,
		FinishedAt: attempt.FinishedAt, WorktreeState: attempt.WorktreeState,
	}
	task, stored, err := store.CompleteCheckAttempt(ctx, completion, attempt.CreatedAt)
	if err != nil {
		t.Fatalf("complete attempt: %v", err)
	}
	return task, stored
}

// completeTestSetup marks the store initialized so portable validation runs.
func completeTestSetup(t *testing.T, store *Store) {
	t.Helper()
	if err := store.CompleteSetup(context.Background(), t.TempDir(), "open", "", "admin-hash", true); err != nil {
		t.Fatal(err)
	}
}

// recordAttemptWithLog registers and completes one attempt with a raw log.
func recordAttemptWithLog(t *testing.T, store *Store, attempt CheckAttempt, log string) (Task, CheckAttempt) {
	t.Helper()
	ctx := context.Background()
	task, registered, err := store.RegisterCheckAttempt(ctx, attempt)
	if err != nil {
		t.Fatalf("register attempt: %v", err)
	}
	completion := CheckCompletion{
		AttemptID: registered.ID, RepositoryID: registered.RepositoryID, TaskID: registered.TaskID,
		Results: attempt.Results, Cancelled: attempt.Status == AttemptCancelled,
		FinishedAt: attempt.FinishedAt, WorktreeState: attempt.WorktreeState, Log: log,
	}
	task, stored, err := store.CompleteCheckAttempt(ctx, completion, attempt.CreatedAt)
	if err != nil {
		t.Fatalf("complete attempt: %v", err)
	}
	return task, stored
}

func TestCorrectionCyclesBelongToTheTaskAcrossRevisions(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	now := time.Unix(1_800_000_000, 0)
	if err := store.AddRepository(ctx, Repository{ID: "project", Name: "Project", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	task, err := store.CreateTask(ctx, "project", "Fix the build", now)
	if err != nil {
		t.Fatal(err)
	}
	revisions := []string{
		"1111111111111111111111111111111111111111",
		"2222222222222222222222222222222222222222",
		"3333333333333333333333333333333333333333",
		"4444444444444444444444444444444444444444",
		"5555555555555555555555555555555555555555",
	}
	// The first real verdict is the initial check, not a correction.
	task, _ = recordAttempt(t, store, attemptFor(task, revisions[0], now, AttemptFailed))
	if task.CorrectionCyclesUsed != 0 || !task.InitialCheckDone || task.Status != TaskActive {
		t.Fatalf("initial check consumed a cycle: %+v", task)
	}
	// Three reserved rounds across three revisions exhaust the task-scoped
	// budget, whether the following check passes or fails.
	for index := 1; index <= CorrectionCycleLimit; index++ {
		cycleID := cycleIDFor(index)
		task, _, err = store.ReserveCorrectionCycle(ctx, "project", task.ID, cycleID, now.Add(time.Duration(index)*time.Minute))
		if err != nil {
			t.Fatal(err)
		}
		if task.CorrectionCyclesUsed != index {
			t.Fatalf("after %d reservations cycles=%d", index, task.CorrectionCyclesUsed)
		}
		attempt := attemptFor(task, revisions[index], now.Add(time.Duration(index)*time.Minute), AttemptFailed)
		attempt.CycleID = cycleID
		task, _ = recordAttempt(t, store, attempt)
		if task.CorrectionCyclesUsed != index {
			t.Fatalf("a completed round changed the budget to %d", task.CorrectionCyclesUsed)
		}
	}
	if task.Status != TaskExhausted || task.CorrectionCyclesRemaining() != 0 {
		t.Fatalf("task after three corrections = %+v", task)
	}
	// A later passing manual rerun resolves the task without erasing the
	// recorded budget.
	task, _ = recordAttempt(t, store, attemptFor(task, revisions[4], now.Add(4*time.Minute), AttemptPassed))
	if task.Status != TaskResolved || task.CorrectionCyclesUsed != CorrectionCycleLimit {
		t.Fatalf("resolved task = %+v", task)
	}
	// The budget is exhausted, so no further round can be reserved.
	if _, _, err := store.ReserveCorrectionCycle(ctx, "project", task.ID, cycleIDFor(9), now.Add(5*time.Minute)); !errors.Is(err, ErrCorrectionBudgetExhausted) {
		t.Fatalf("reservation after exhaustion error=%v", err)
	}
}

func TestCleanupFailureCannotResolveTaskOrLoseSubmittedFacts(t *testing.T) {
	zero := 0
	cleanupPassed := CheckResult{
		Name: "cleanup", Command: "exit 0", Status: AttemptPassed, ExitCode: &zero,
		DurationMS: 1000, OutputExcerpt: "out", CleanupError: "owned process exit was not confirmed",
	}
	cleanupError := cleanupPassed
	cleanupError.Status = AttemptError
	cancelled := CheckResult{Name: "remaining", Command: "exit 0", Status: AttemptCancelled}
	tests := []struct {
		name    string
		results []CheckResult
	}{
		{name: "submitted pass cleanup first", results: []CheckResult{cleanupPassed, cancelled}},
		{name: "submitted pass cleanup last", results: []CheckResult{cancelled, cleanupPassed}},
		{name: "execution error cleanup first", results: []CheckResult{cleanupError, cancelled}},
		{name: "execution error cleanup last", results: []CheckResult{cancelled, cleanupError}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := openTestStore(t)
			ctx := context.Background()
			now := time.Unix(1_800_000_000, 0)
			if err := store.AddRepository(ctx, Repository{ID: "project", Name: "Project", CreatedAt: now}); err != nil {
				t.Fatal(err)
			}
			task, err := store.CreateTask(ctx, "project", "Cleanup failed", now)
			if err != nil {
				t.Fatal(err)
			}
			attempt := attemptFor(task, "1111111111111111111111111111111111111111", now, AttemptPassed)
			attempt.Status = AttemptCancelled
			attempt.Results = append([]CheckResult(nil), test.results...)
			attempt.Checks = make([]CheckDefinition, 0, len(attempt.Results))
			for _, result := range attempt.Results {
				attempt.Checks = append(attempt.Checks, CheckDefinition{Name: result.Name, Command: result.Command})
			}
			task, stored := recordAttempt(t, store, attempt)

			if task.Status != TaskActive || stored.Status != AttemptError {
				t.Fatalf("cleanup failure resolved task: task=%+v attempt=%+v", task, stored)
			}
			if !stored.SubmittedCancelled || stored.ExitCode == nil || *stored.ExitCode != 0 {
				t.Fatalf("cleanup failure lost submitted cancellation or exit code: %+v", stored)
			}
			if len(stored.Results) != len(attempt.Results) {
				t.Fatalf("cleanup failure changed result count: %+v", stored.Results)
			}
			for index, want := range attempt.Results {
				got := stored.Results[index]
				if got.Name != want.Name || got.Command != want.Command || got.Status != want.Status || got.CleanupError != want.CleanupError {
					t.Fatalf("result %d changed submitted facts: got=%+v want=%+v", index, got, want)
				}
			}
			if stored.Summary != "2 checks: 1 error, 1 cancelled" {
				t.Fatalf("cleanup failure summary=%q", stored.Summary)
			}

			completion := CheckCompletion{
				AttemptID: stored.ID, RepositoryID: stored.RepositoryID, TaskID: stored.TaskID,
				Results: attempt.Results, Cancelled: true, FinishedAt: attempt.FinishedAt, WorktreeState: attempt.WorktreeState,
			}
			_, replayed, err := store.CompleteCheckAttempt(ctx, completion, now.Add(time.Minute))
			if err != nil || replayed.ID != stored.ID || replayed.CompletionDigest != stored.CompletionDigest || replayed.Status != AttemptError {
				t.Fatalf("exact cleanup replay=%+v err=%v", replayed, err)
			}
			changed := completion
			changed.Results = append([]CheckResult(nil), completion.Results...)
			for index := range changed.Results {
				if changed.Results[index].CleanupError != "" {
					changed.Results[index].CleanupError = "different cleanup failure"
				}
			}
			if _, _, err := store.CompleteCheckAttempt(ctx, changed, now.Add(2*time.Minute)); !errors.Is(err, ErrAttemptConflict) {
				t.Fatalf("changed cleanup replay error=%v", err)
			}
		})
	}
}

func TestOrdinaryErrorAndCancellationKeepCancellationPrecedence(t *testing.T) {
	results := []CheckResult{
		{Name: "error", Command: "exit 1", Status: AttemptError},
		{Name: "remaining", Command: "exit 0", Status: AttemptCancelled},
	}
	for _, ordered := range [][]CheckResult{results, {results[1], results[0]}} {
		if status := AggregateAttemptStatus(ordered, true); status != AttemptCancelled {
			t.Fatalf("ordinary error plus cancellation status=%q", status)
		}
	}
}

func TestSuccessfulCorrectionStillCountsAndRetriesReuseTheRound(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	now := time.Unix(1_800_000_000, 0)
	if err := store.AddRepository(ctx, Repository{ID: "project", Name: "Project", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	task, err := store.CreateTask(ctx, "project", "Successful correction", now)
	if err != nil {
		t.Fatal(err)
	}
	task, _ = recordAttempt(t, store, attemptFor(task, "1111111111111111111111111111111111111111", now, AttemptFailed))
	cycleID := cycleIDFor(1)
	task, cycle, err := store.ReserveCorrectionCycle(ctx, "project", task.ID, cycleID, now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if cycle.Sequence != 1 || task.CorrectionCyclesUsed != 1 {
		t.Fatalf("reserved cycle=%+v task=%+v", cycle, task)
	}
	// A retransmitted reservation returns the same round without counting twice.
	task, again, err := store.ReserveCorrectionCycle(ctx, "project", task.ID, cycleID, now.Add(2*time.Minute))
	if err != nil || again.ID != cycleID || task.CorrectionCyclesUsed != 1 {
		t.Fatalf("retransmitted reservation cycle=%+v task=%+v err=%v", again, task, err)
	}
	// The correction succeeds, and the round still counts.
	passing := attemptFor(task, "2222222222222222222222222222222222222222", now.Add(3*time.Minute), AttemptPassed)
	passing.CycleID = cycleID
	task, stored := recordAttempt(t, store, passing)
	if task.Status != TaskResolved || task.CorrectionCyclesUsed != 1 {
		t.Fatalf("successful correction task=%+v", task)
	}
	if stored.CycleID != cycleID {
		t.Fatalf("attempt lost its correction round: %+v", stored)
	}
	// A manual rerun and an unavailable run consume nothing.
	task, _ = recordAttempt(t, store, attemptFor(task, "3333333333333333333333333333333333333333", now.Add(4*time.Minute), AttemptFailed))
	if task.CorrectionCyclesUsed != 1 {
		t.Fatalf("manual rerun consumed a cycle: %+v", task)
	}
	task, _ = recordAttempt(t, store, attemptFor(task, "4444444444444444444444444444444444444444", now.Add(5*time.Minute), AttemptUnavailable))
	if task.CorrectionCyclesUsed != 1 {
		t.Fatalf("unavailable run consumed a cycle: %+v", task)
	}
	cycles, err := store.CheckCycles(ctx, "project", task.ID)
	if err != nil || len(cycles) != 1 || cycles[0].AttemptID != stored.ID {
		t.Fatalf("cycles=%+v err=%v", cycles, err)
	}
}

func TestUnavailableAndRetransmitDoNotConsumeCorrections(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	now := time.Unix(1_800_000_000, 0)
	if err := store.AddRepository(ctx, Repository{ID: "project", Name: "Project", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	task, err := store.CreateTask(ctx, "project", "Unavailable environment", now)
	if err != nil {
		t.Fatal(err)
	}
	// An unavailable environment is not a real verdict, so it is neither the
	// initial check nor a correction.
	task, _ = recordAttempt(t, store, attemptFor(task, "1111111111111111111111111111111111111111", now, AttemptUnavailable))
	if task.CorrectionCyclesUsed != 0 || task.InitialCheckDone {
		t.Fatalf("unavailable run consumed a cycle: %+v", task)
	}
	// The first real verdict is still the initial check.
	task, _ = recordAttempt(t, store, attemptFor(task, "2222222222222222222222222222222222222222", now.Add(time.Minute), AttemptFailed))
	if task.CorrectionCyclesUsed != 0 || !task.InitialCheckDone {
		t.Fatalf("initial check after an unavailable run: %+v", task)
	}
	// A retransmit of the same attempt is idempotent.
	retransmit := attemptFor(task, "2222222222222222222222222222222222222222", now.Add(time.Minute), AttemptFailed)
	task, _ = recordAttempt(t, store, retransmit)
	if task.CorrectionCyclesUsed != 0 {
		t.Fatalf("retransmit consumed a cycle: %+v", task)
	}
	attempts, err := store.CheckAttempts(ctx, "project")
	if err != nil || len(attempts) != 2 {
		t.Fatalf("stored attempts=%d err=%v", len(attempts), err)
	}
}

func TestReusedAttemptIdentityWithDifferentContentIsRejected(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	now := time.Unix(1_800_000_000, 0)
	if err := store.AddRepository(ctx, Repository{ID: "project", Name: "Project", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	task, err := store.CreateTask(ctx, "project", "Conflict", now)
	if err != nil {
		t.Fatal(err)
	}
	first := attemptFor(task, "1111111111111111111111111111111111111111", now, AttemptFailed)
	if _, _, err := store.RegisterCheckAttempt(ctx, first); err != nil {
		t.Fatal(err)
	}
	conflict := first
	conflict.RevisionOID = "9999999999999999999999999999999999999999"
	if _, _, err := store.RegisterCheckAttempt(ctx, conflict); !errors.Is(err, ErrAttemptConflict) {
		t.Fatalf("conflicting registration error=%v", err)
	}
	attempts, err := store.CheckAttempts(ctx, "project")
	if err != nil || len(attempts) != 1 {
		t.Fatalf("stored attempts=%d err=%v", len(attempts), err)
	}
}

func TestExactRegistrationReplayUsesTheOriginalServerFacts(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	now := time.Unix(1_800_000_000, 0)
	if err := store.AddRepository(ctx, Repository{ID: "project", Name: "Project", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	task, err := store.CreateTask(ctx, "project", "Clock-independent replay", now)
	if err != nil {
		t.Fatal(err)
	}
	first := attemptFor(task, "1111111111111111111111111111111111111111", now, AttemptFailed)
	first.CredentialID = "credential-one"
	_, registered, err := store.RegisterCheckAttempt(ctx, first)
	if err != nil {
		t.Fatal(err)
	}

	replay := first
	replay.CreatedAt = now.Add(2 * time.Second)
	_, replayed, err := store.RegisterCheckAttempt(ctx, replay)
	if err != nil {
		t.Fatalf("exact replay after server clock advance: %v", err)
	}
	if !replayed.CreatedAt.Equal(registered.CreatedAt) || replayed.Sequence != registered.Sequence || replayed.RegistrationDigest != registered.RegistrationDigest {
		t.Fatalf("replay changed immutable registration: first=%+v replay=%+v", registered, replayed)
	}

	for name, change := range map[string]func(*CheckAttempt){
		"payload":    func(attempt *CheckAttempt) { attempt.WorktreeState = WorktreeDirty },
		"provenance": func(attempt *CheckAttempt) { attempt.CredentialID = "credential-two" },
	} {
		t.Run(name, func(t *testing.T) {
			conflict := replay
			change(&conflict)
			if _, _, err := store.RegisterCheckAttempt(ctx, conflict); !errors.Is(err, ErrAttemptConflict) {
				t.Fatalf("changed replay error=%v", err)
			}
		})
	}
	attempts, err := store.CheckAttempts(ctx, "project")
	if err != nil || len(attempts) != 1 || attempts[0].RegistrationDigest != registered.RegistrationDigest {
		t.Fatalf("stored attempts=%+v err=%v", attempts, err)
	}
	completeTestSetup(t, store)
	if _, err := store.RecoverySnapshot(ctx); err != nil {
		t.Fatalf("exact replay invalidated portable evidence: %v", err)
	}
}

func TestOldRegistrationReplayDoesNotChangeNewerConfigurationHistory(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	now := time.Unix(1_800_000_000, 0)
	if err := store.AddRepository(ctx, Repository{ID: "project", Name: "Project", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	task, err := store.CreateTask(ctx, "project", "Configuration replay", now)
	if err != nil {
		t.Fatal(err)
	}
	first := attemptFor(task, "1111111111111111111111111111111111111111", now, AttemptFailed)
	_, original, err := store.RegisterCheckAttempt(ctx, first)
	if err != nil {
		t.Fatal(err)
	}
	newer := attemptFor(task, "2222222222222222222222222222222222222222", now.Add(time.Minute), AttemptFailed)
	newer.Checks = []CheckDefinition{{Name: "lint", Command: "go vet ./..."}}
	_, latestAttempt, err := store.RegisterCheckAttempt(ctx, newer)
	if err != nil {
		t.Fatal(err)
	}
	if original.ConfigurationVersion != 1 || latestAttempt.ConfigurationVersion != 2 {
		t.Fatalf("configuration versions original=%d newer=%d", original.ConfigurationVersion, latestAttempt.ConfigurationVersion)
	}

	replay := first
	replay.CreatedAt = now.Add(2 * time.Minute)
	_, replayed, err := store.RegisterCheckAttempt(ctx, replay)
	if err != nil || replayed.ConfigurationVersion != original.ConfigurationVersion {
		t.Fatalf("old exact replay=%+v err=%v", replayed, err)
	}
	changed := replay
	changed.Checks = []CheckDefinition{{Name: "security", Command: "go test ./internal/security"}}
	if _, _, err := store.RegisterCheckAttempt(ctx, changed); !errors.Is(err, ErrAttemptConflict) {
		t.Fatalf("old changed replay error=%v", err)
	}

	configuration, exists, err := store.LatestCheckConfiguration(ctx, "project")
	if err != nil || !exists || configuration.Version != 2 || configuration.Checks[0].Name != "lint" {
		t.Fatalf("latest configuration=%+v exists=%v err=%v", configuration, exists, err)
	}
	var configurationCount int
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM check_configurations WHERE repository_id=?`, "project").Scan(&configurationCount); err != nil {
		t.Fatal(err)
	}
	if configurationCount != 2 {
		t.Fatalf("configuration count=%d want 2", configurationCount)
	}
	attempts, err := store.CheckAttempts(ctx, "project")
	if err != nil || len(attempts) != 2 || attempts[0].Sequence != original.Sequence || !attempts[0].CreatedAt.Equal(original.CreatedAt) || attempts[0].RegistrationDigest != original.RegistrationDigest {
		t.Fatalf("configuration replay changed registrations: attempts=%+v err=%v", attempts, err)
	}
}

func TestSimultaneousDuplicateUploadsStoreOneAttempt(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	now := time.Unix(1_800_000_000, 0)
	if err := store.AddRepository(ctx, Repository{ID: "project", Name: "Project", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	task, err := store.CreateTask(ctx, "project", "Simultaneous", now)
	if err != nil {
		t.Fatal(err)
	}
	attempt := attemptFor(task, "1111111111111111111111111111111111111111", now, AttemptFailed)
	if _, _, err := store.RegisterCheckAttempt(ctx, attempt); err != nil {
		t.Fatal(err)
	}
	completion := CheckCompletion{
		AttemptID: attempt.ID, RepositoryID: "project", TaskID: task.ID, Results: attempt.Results,
		FinishedAt: attempt.FinishedAt, WorktreeState: attempt.WorktreeState, Log: "shared log",
	}
	var wait sync.WaitGroup
	errs := make([]error, 4)
	for index := range errs {
		wait.Add(1)
		go func(slot int) {
			defer wait.Done()
			_, _, errs[slot] = store.CompleteCheckAttempt(ctx, completion, now)
		}(index)
	}
	wait.Wait()
	for index, err := range errs {
		if err != nil {
			t.Fatalf("concurrent completion %d error=%v", index, err)
		}
	}
	attempts, err := store.CheckAttempts(ctx, "project")
	if err != nil || len(attempts) != 1 {
		t.Fatalf("stored attempts=%d err=%v", len(attempts), err)
	}
	if attempts[0].LogDigest == "" {
		t.Fatal("the accepted log digest was not recorded")
	}
	content, logState, err := store.ReadCheckLog(attempts[0].LogID, attempts[0].LogExpiresAt, now)
	if err != nil || logState != CheckLogFound || string(content) != "shared log" {
		t.Fatalf("accepted log=%q state=%q err=%v", content, logState, err)
	}
	stored, _, err := store.Task(ctx, "project", task.ID)
	if err != nil || stored.CorrectionCyclesUsed != 0 {
		t.Fatalf("task after concurrent duplicates=%+v err=%v", stored, err)
	}
}

func TestAcceptedLogAndResultSurviveAConflictingRetransmit(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	now := time.Unix(1_800_000_000, 0)
	if err := store.AddRepository(ctx, Repository{ID: "project", Name: "Project", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	task, err := store.CreateTask(ctx, "project", "Immutable completion", now)
	if err != nil {
		t.Fatal(err)
	}
	attempt := attemptFor(task, "1111111111111111111111111111111111111111", now, AttemptFailed)
	if _, _, err := store.RegisterCheckAttempt(ctx, attempt); err != nil {
		t.Fatal(err)
	}
	completion := CheckCompletion{
		AttemptID: attempt.ID, RepositoryID: "project", TaskID: task.ID, Results: attempt.Results,
		FinishedAt: attempt.FinishedAt, WorktreeState: attempt.WorktreeState, Log: "first log",
	}
	_, stored, err := store.CompleteCheckAttempt(ctx, completion, now)
	if err != nil {
		t.Fatal(err)
	}
	originalExpiry := *stored.LogExpiresAt
	// An identical retransmit returns the original result and expiry.
	_, again, err := store.CompleteCheckAttempt(ctx, completion, now.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if !again.LogExpiresAt.Equal(originalExpiry) || again.CompletionDigest != stored.CompletionDigest {
		t.Fatalf("identical retransmit changed the accepted result: %+v", again)
	}
	// A changed log must not replace the accepted bytes.
	changed := completion
	changed.Log = "second log"
	if _, _, err := store.CompleteCheckAttempt(ctx, changed, now.Add(2*time.Hour)); !errors.Is(err, ErrAttemptConflict) {
		t.Fatalf("changed log error=%v", err)
	}
	assertAcceptedLog(t, store, stored, now.Add(2*time.Hour), "first log")
	// An empty replacement log must not delete the accepted bytes.
	empty := completion
	empty.Log = ""
	if _, _, err := store.CompleteCheckAttempt(ctx, empty, now.Add(3*time.Hour)); !errors.Is(err, ErrAttemptConflict) {
		t.Fatalf("empty replacement log error=%v", err)
	}
	assertAcceptedLog(t, store, stored, now.Add(3*time.Hour), "first log")
	// A conflicting result must not mutate the accepted result.
	conflicting := completion
	conflicting.Results = append([]CheckResult(nil), attempt.Results...)
	conflicting.Results[0].Status = AttemptPassed
	if _, _, err := store.CompleteCheckAttempt(ctx, conflicting, now.Add(4*time.Hour)); !errors.Is(err, ErrAttemptConflict) {
		t.Fatalf("conflicting result error=%v", err)
	}
	assertAcceptedLog(t, store, stored, now.Add(5*time.Hour), "first log")
	// Expiry changes only log readability. An exact replay returns the original
	// record without extending the accepted expiry.
	_, expiredReplay, err := store.CompleteCheckAttempt(ctx, completion, originalExpiry.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if expiredReplay.LogExpiresAt == nil || !expiredReplay.LogExpiresAt.Equal(originalExpiry) {
		t.Fatalf("expired replay changed the accepted expiry: %+v", expiredReplay)
	}
	if state := store.CheckLogState(expiredReplay.LogID, expiredReplay.LogExpiresAt, originalExpiry.Add(time.Hour)); state != CheckLogExpired {
		t.Fatalf("expired replay log state=%q", state)
	}
	reloaded, exists, err := store.CheckAttemptByID(ctx, "project", attempt.ID)
	if err != nil || !exists || reloaded.Status != AttemptFailed || reloaded.CompletionDigest != stored.CompletionDigest {
		t.Fatalf("reloaded attempt=%+v exists=%v err=%v", reloaded, exists, err)
	}
}

func TestLateOlderFailureDoesNotOverrideNewerSuccess(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	now := time.Unix(1_800_000_000, 0)
	if err := store.AddRepository(ctx, Repository{ID: "project", Name: "Project", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	task, err := store.CreateTask(ctx, "project", "Late older failure", now)
	if err != nil {
		t.Fatal(err)
	}
	olderRevision := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	newerRevision := "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	// The older revision starts first, so it receives the lower sequence.
	older := attemptFor(task, olderRevision, now, AttemptFailed)
	if _, _, err := store.RegisterCheckAttempt(ctx, older); err != nil {
		t.Fatal(err)
	}
	newer := attemptFor(task, newerRevision, now.Add(time.Minute), AttemptPassed)
	if _, _, err := store.RegisterCheckAttempt(ctx, newer); err != nil {
		t.Fatal(err)
	}
	// The newer revision succeeds first.
	task, _, err = store.CompleteCheckAttempt(ctx, CheckCompletion{
		AttemptID: newer.ID, RepositoryID: "project", TaskID: newer.TaskID, Results: newer.Results,
		FinishedAt: newer.FinishedAt, WorktreeState: newer.WorktreeState,
	}, now.Add(2*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if task.Status != TaskResolved {
		t.Fatalf("task after the newer pass = %+v", task)
	}
	// The older revision finishes much later, with a client clock far ahead.
	// The server-issued sequence, not the finish time, decides the order.
	task, _, err = store.CompleteCheckAttempt(ctx, CheckCompletion{
		AttemptID: older.ID, RepositoryID: "project", TaskID: older.TaskID, Results: older.Results,
		FinishedAt: now.Add(10 * time.Hour), WorktreeState: older.WorktreeState,
	}, now.Add(10*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if task.Status != TaskResolved || task.CorrectionCyclesUsed != 0 {
		t.Fatalf("late older failure overrode newer state: %+v", task)
	}
	if task.LastAppliedAttemptID != newer.ID {
		t.Fatalf("applied attempt=%q want %q", task.LastAppliedAttemptID, newer.ID)
	}
	// Both attempts stay bound to the revision they actually tested.
	oldAttempt, exists, err := store.LatestCheckAttemptForRevision(ctx, "project", olderRevision)
	if err != nil || !exists || oldAttempt.Status != AttemptFailed {
		t.Fatalf("older attempt=%+v exists=%v err=%v", oldAttempt, exists, err)
	}
	newAttempt, exists, err := store.LatestCheckAttemptForRevision(ctx, "project", newerRevision)
	if err != nil || !exists || newAttempt.Status != AttemptPassed {
		t.Fatalf("newer attempt=%+v exists=%v err=%v", newAttempt, exists, err)
	}
}

func TestNewRegistrationDoesNotInheritAnOlderSuccess(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	now := time.Unix(1_800_000_000, 0)
	if err := store.AddRepository(ctx, Repository{ID: "project", Name: "Project", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	task, err := store.CreateTask(ctx, "project", "New work", now)
	if err != nil {
		t.Fatal(err)
	}
	task, _ = recordAttempt(t, store, attemptFor(task, "1111111111111111111111111111111111111111", now, AttemptPassed))
	if task.Status != TaskResolved {
		t.Fatalf("task after a pass = %+v", task)
	}
	// A new registration is in flight, so the task is no longer resolved.
	pending := attemptFor(task, "2222222222222222222222222222222222222222", now.Add(time.Minute), AttemptFailed)
	task, registered, err := store.RegisterCheckAttempt(ctx, pending)
	if err != nil {
		t.Fatal(err)
	}
	if task.Status != TaskActive || task.PendingAttemptID != registered.ID {
		t.Fatalf("task after a new registration = %+v", task)
	}
	if registered.Status != AttemptPending || !registered.FinishedAt.IsZero() {
		t.Fatalf("registered attempt=%+v", registered)
	}
	// A missing completion stays visible.
	attempts, err := store.CheckAttempts(ctx, "project")
	if err != nil || len(attempts) != 2 || attempts[1].Status != AttemptPending {
		t.Fatalf("attempts=%+v err=%v", attempts, err)
	}
}

func TestLateResultStaysBoundToItsRevision(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	now := time.Unix(1_800_000_000, 0)
	if err := store.AddRepository(ctx, Repository{ID: "project", Name: "Project", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	task, err := store.CreateTask(ctx, "project", "Late result", now)
	if err != nil {
		t.Fatal(err)
	}
	oldRevision := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	newRevision := "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	recordAttempt(t, store, attemptFor(task, oldRevision, now, AttemptFailed))
	recordAttempt(t, store, attemptFor(task, newRevision, now.Add(time.Minute), AttemptFailed))
	late := attemptFor(task, oldRevision, now.Add(2*time.Minute), AttemptPassed)
	recordAttempt(t, store, late)
	oldAttempt, exists, err := store.LatestCheckAttemptForRevision(ctx, "project", oldRevision)
	if err != nil || !exists || oldAttempt.Status != AttemptPassed {
		t.Fatalf("late result for the original revision: exists=%v attempt=%+v err=%v", exists, oldAttempt, err)
	}
	newAttempt, exists, err := store.LatestCheckAttemptForRevision(ctx, "project", newRevision)
	if err != nil || !exists || newAttempt.Status != AttemptFailed {
		t.Fatalf("newer revision inherited the late result: exists=%v attempt=%+v err=%v", exists, newAttempt, err)
	}
}

func TestCheckConfigurationIsVersionedByContent(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	now := time.Unix(1_800_000_000, 0)
	if err := store.AddRepository(ctx, Repository{ID: "project", Name: "Project", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	task, err := store.CreateTask(ctx, "project", "Versioned configuration", now)
	if err != nil {
		t.Fatal(err)
	}
	first := attemptFor(task, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", now, AttemptFailed)
	if _, registered, err := store.RegisterCheckAttempt(ctx, first); err != nil {
		t.Fatal(err)
	} else if registered.ConfigurationVersion != 1 {
		t.Fatalf("first configuration version = %d", registered.ConfigurationVersion)
	}
	second := attemptFor(task, "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", now.Add(time.Minute), AttemptFailed)
	if _, registered, err := store.RegisterCheckAttempt(ctx, second); err != nil {
		t.Fatal(err)
	} else if registered.ConfigurationVersion != 1 {
		t.Fatalf("identical configuration created version %d", registered.ConfigurationVersion)
	}
	third := attemptFor(task, "cccccccccccccccccccccccccccccccccccccccc", now.Add(2*time.Minute), AttemptFailed)
	third.Checks = []CheckDefinition{{Name: "lint", Command: "go vet ./..."}}
	third.Results[0].Name = "lint"
	third.Results[0].Command = "go vet ./..."
	if _, registered, err := store.RegisterCheckAttempt(ctx, third); err != nil {
		t.Fatal(err)
	} else if registered.ConfigurationVersion != 2 {
		t.Fatalf("changed configuration version = %d, want 2", registered.ConfigurationVersion)
	}
	latest, exists, err := store.LatestCheckConfiguration(ctx, "project")
	if err != nil || !exists || latest.Version != 2 || len(latest.Checks) != 1 || latest.Checks[0].Name != "lint" {
		t.Fatalf("latest configuration=%+v exists=%v err=%v", latest, exists, err)
	}
}

func TestCompletionRejectsResultsThatDoNotMatchTheDeclaredChecks(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	now := time.Unix(1_800_000_000, 0)
	if err := store.AddRepository(ctx, Repository{ID: "project", Name: "Project", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	task, err := store.CreateTask(ctx, "project", "Mismatch", now)
	if err != nil {
		t.Fatal(err)
	}
	attempt := attemptFor(task, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", now, AttemptFailed)
	if _, _, err := store.RegisterCheckAttempt(ctx, attempt); err != nil {
		t.Fatal(err)
	}
	results := append([]CheckResult(nil), attempt.Results...)
	results[0].Name = "other"
	if _, _, err := store.CompleteCheckAttempt(ctx, CheckCompletion{
		AttemptID: attempt.ID, RepositoryID: "project", TaskID: task.ID, Results: results,
		FinishedAt: attempt.FinishedAt, WorktreeState: attempt.WorktreeState,
	}, now); err == nil {
		t.Fatal("a result for an undeclared check was accepted")
	}
}

func TestHelperCredentialsAreScopedHashedAndRevocable(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	now := time.Unix(1_800_000_000, 0)
	if err := store.AddRepository(ctx, Repository{ID: "project", Name: "Project", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	token := "synthetic-helper-token"
	hash := sha256.Sum256([]byte(token))
	credential, created, err := store.CreateHelperCredential(ctx, "project", "laptop", cycleIDFor(7), hash[:], now)
	if err != nil || !created {
		t.Fatalf("create credential=%+v created=%v err=%v", credential, created, err)
	}
	// A retransmit with the same creation identity returns the same credential
	// instead of creating duplicate authority.
	again, created, err := store.CreateHelperCredential(ctx, "project", "laptop", cycleIDFor(7), hash[:], now.Add(time.Second))
	if err != nil || created || again.ID != credential.ID {
		t.Fatalf("retransmitted creation=%+v created=%v err=%v", again, created, err)
	}
	found, ok, err := store.HelperCredentialByToken(ctx, hash[:], now.Add(time.Minute))
	if err != nil || !ok || found.ID != credential.ID || found.RepositoryID != "project" {
		t.Fatalf("credential lookup=%+v ok=%v err=%v", found, ok, err)
	}
	if found.LastUsedAt == nil {
		t.Fatal("credential use was not recorded")
	}
	// A scoped compensating revoke is idempotent.
	if err := store.RevokeHelperCredentialByCreation(ctx, "project", cycleIDFor(7), now.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := store.RevokeHelperCredentialByCreation(ctx, "project", cycleIDFor(7), now.Add(3*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := store.HelperCredentialByToken(ctx, hash[:], now.Add(4*time.Minute)); err != nil || ok {
		t.Fatalf("revoked credential accepted: ok=%v err=%v", ok, err)
	}
	credentials, err := store.HelperCredentials(ctx, "project")
	if err != nil || len(credentials) != 1 || credentials[0].RevokedAt == nil {
		t.Fatalf("credential list=%+v err=%v", credentials, err)
	}
}

func TestCheckLogsAreDisposableAndPruned(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	now := time.Unix(1_800_000_000, 0)
	if err := store.AddRepository(ctx, Repository{ID: "project", Name: "Project", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	task, err := store.CreateTask(ctx, "project", "Disposable log", now)
	if err != nil {
		t.Fatal(err)
	}
	attempt := attemptFor(task, strings.Repeat("a", 40), now, AttemptFailed)
	_, stored := recordAttemptWithLog(t, store, attempt, "raw output")
	if stored.LogID != attempt.ID || stored.LogExpiresAt == nil || stored.LogTruncated {
		t.Fatalf("stored log metadata=%+v", stored)
	}
	content, logState, err := store.ReadCheckLog(stored.LogID, stored.LogExpiresAt, now)
	if err != nil || logState != CheckLogFound || string(content) != "raw output" {
		t.Fatalf("log content=%q state=%q err=%v", content, logState, err)
	}
	if _, logState, err := store.ReadCheckLog(stored.LogID, stored.LogExpiresAt, stored.LogExpiresAt.Add(time.Second)); err != nil || logState != CheckLogExpired {
		t.Fatalf("expired read state=%q err=%v", logState, err)
	}
	removed, err := store.PruneCheckLogs(ctx, stored.LogExpiresAt.Add(time.Second))
	if err != nil || removed != 1 {
		t.Fatalf("pruned=%d err=%v", removed, err)
	}
	if _, logState, _ := store.ReadCheckLog(stored.LogID, stored.LogExpiresAt, now); logState != CheckLogMissing {
		t.Fatalf("pruned log state=%q", logState)
	}
	reloaded, exists, err := store.CheckAttemptByID(ctx, "project", attempt.ID)
	if err != nil || !exists || reloaded.Status != AttemptFailed || reloaded.LogID != stored.LogID {
		t.Fatalf("durable attempt=%+v exists=%v err=%v", reloaded, exists, err)
	}
}

func TestPruneDrainsDueRowsAndReportsCommittedProgress(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	now := time.Unix(1_800_000_000, 0)
	if err := store.AddRepository(ctx, Repository{ID: "project", Name: "Project", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	task, err := store.CreateTask(ctx, "project", "Prune batches", now)
	if err != nil {
		t.Fatal(err)
	}
	const logs = checkLogPruneBatch + 4
	for index := 0; index < logs; index++ {
		attempt := attemptFor(task, fmt.Sprintf("%040x", index+1), now.Add(time.Duration(index)*time.Second), AttemptFailed)
		_, stored := recordAttemptWithLog(t, store, attempt, "raw output")
		if stored.LogExpiresAt == nil {
			t.Fatal("raw log has no expiry")
		}
	}
	futureAttempt := attemptFor(task, fmt.Sprintf("%040x", 1000), now.Add(100*24*time.Hour), AttemptFailed)
	_, futureStored := recordAttemptWithLog(t, store, futureAttempt, "future raw output")
	cutoff := now.Add(retention(DefaultCheckLogRetentionDays) + time.Hour)
	var failID string
	if err := store.db.QueryRowContext(ctx, `SELECT attempt_id FROM check_raw_logs WHERE expires_at<=? ORDER BY expires_at,attempt_id LIMIT 1 OFFSET ?`, cutoff.Unix(), checkLogPruneBatch).Scan(&failID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, fmt.Sprintf(`CREATE TRIGGER fail_second_prune_batch BEFORE DELETE ON check_raw_logs WHEN OLD.attempt_id='%s' BEGIN SELECT RAISE(ABORT,'injected prune failure'); END`, failID)); err != nil {
		t.Fatal(err)
	}
	removed, err := store.PruneCheckLogs(ctx, cutoff)
	if err == nil || !strings.Contains(err.Error(), "injected prune failure") {
		t.Fatalf("prune error=%v", err)
	}
	if removed != checkLogPruneBatch {
		t.Fatalf("committed prune progress=%d want %d", removed, checkLogPruneBatch)
	}
	if _, err := store.db.ExecContext(ctx, `DROP TRIGGER fail_second_prune_batch`); err != nil {
		t.Fatal(err)
	}
	removed, err = store.PruneCheckLogs(ctx, cutoff)
	if err != nil || removed != logs-checkLogPruneBatch {
		t.Fatalf("remaining prune progress=%d err=%v", removed, err)
	}
	var remaining int
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM check_raw_logs WHERE expires_at<=?`, cutoff.Unix()).Scan(&remaining); err != nil || remaining != 0 {
		t.Fatalf("due raw logs=%d err=%v", remaining, err)
	}
	if count := checkRawLogCount(t, store, futureStored.ID); count != 1 {
		t.Fatalf("future raw log rows=%d", count)
	}
}

func TestRecoveryExcludesRawLogsAndRestoreDoesNotReviveThem(t *testing.T) {
	ctx := context.Background()
	now := time.Unix(1_800_000_000, 0)
	store := openTestStore(t)
	if err := store.AddRepository(ctx, Repository{ID: "project", Name: "Project", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	task, err := store.CreateTask(ctx, "project", "Exclude raw logs from recovery", now)
	if err != nil {
		t.Fatal(err)
	}
	attempt := attemptFor(task, strings.Repeat("b", 40), now, AttemptFailed)
	const rawMarker = "unique-raw-log-marker-not-for-recovery"
	_, stored := recordAttemptWithLog(t, store, attempt, rawMarker)
	completeTestSetup(t, store)
	snapshot, err := store.RecoverySnapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(encoded, []byte(rawMarker)) {
		t.Fatal("recovery snapshot contains raw log bytes")
	}

	restored := openTestStore(t)
	if _, err := restored.db.ExecContext(ctx, `PRAGMA foreign_keys=OFF`); err != nil {
		t.Fatal(err)
	}
	if _, err := restored.db.ExecContext(ctx, `INSERT INTO check_raw_logs(attempt_id,content,expires_at) VALUES(?,?,?)`, "orphaned-before-restore", []byte("preexisting raw bytes"), stored.LogExpiresAt.Unix()); err != nil {
		t.Fatal(err)
	}
	if _, err := restored.db.ExecContext(ctx, `PRAGMA foreign_keys=ON`); err != nil {
		t.Fatal(err)
	}
	if count := checkRawLogCount(t, restored, "orphaned-before-restore"); count != 1 {
		t.Fatalf("pre-restore raw log rows=%d", count)
	}
	if err := restored.RestoreRecoveryState(ctx, t.TempDir(), snapshot); err != nil {
		t.Fatal(err)
	}
	var rawRows int
	if err := restored.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM check_raw_logs`).Scan(&rawRows); err != nil || rawRows != 0 {
		t.Fatalf("raw log rows after restore=%d err=%v", rawRows, err)
	}
	reloaded, exists, err := restored.CheckAttemptByID(ctx, "project", stored.ID)
	if err != nil || !exists || reloaded.LogID != stored.LogID || reloaded.LogExpiresAt == nil {
		t.Fatalf("restored metadata=%+v exists=%v err=%v", reloaded, exists, err)
	}
	if state := restored.CheckLogState(reloaded.LogID, reloaded.LogExpiresAt, now); state != CheckLogMissing {
		t.Fatalf("restored raw log before expiry=%q", state)
	}
	if state := restored.CheckLogState(reloaded.LogID, reloaded.LogExpiresAt, reloaded.LogExpiresAt.Add(time.Second)); state != CheckLogExpired {
		t.Fatalf("restored raw log after expiry=%q", state)
	}
}

func TestCheckLogReportsSubmittedTruncation(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	now := time.Unix(1_800_000_000, 0)
	if err := store.AddRepository(ctx, Repository{ID: "project", Name: "Project", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	task, err := store.CreateTask(ctx, "project", "Truncated log", now)
	if err != nil {
		t.Fatal(err)
	}
	attempt := attemptFor(task, strings.Repeat("b", 40), now, AttemptFailed)
	_, registered, err := store.RegisterCheckAttempt(ctx, attempt)
	if err != nil {
		t.Fatal(err)
	}
	completion := CheckCompletion{
		AttemptID: registered.ID, RepositoryID: registered.RepositoryID, TaskID: registered.TaskID,
		Results: attempt.Results, FinishedAt: attempt.FinishedAt, WorktreeState: attempt.WorktreeState,
		Log: strings.Repeat("x", MaximumCheckLogBytes), LogTruncated: true,
	}
	_, stored, err := store.CompleteCheckAttempt(ctx, completion, now)
	if err != nil || !stored.LogTruncated || !stored.SubmittedTruncated {
		t.Fatalf("stored truncation=%+v err=%v", stored, err)
	}
}

func checkRawLogCount(t *testing.T, store *Store, attemptID string) int {
	t.Helper()
	var count int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM check_raw_logs WHERE attempt_id=?`, attemptID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

// assertAcceptedLog checks that the accepted bytes and expiry survived a
// conflicting retransmit.
func assertAcceptedLog(t *testing.T, store *Store, attempt CheckAttempt, now time.Time, want string) {
	t.Helper()
	content, logState, err := store.ReadCheckLog(attempt.LogID, attempt.LogExpiresAt, now)
	if err != nil || logState != CheckLogFound || string(content) != want {
		t.Fatalf("accepted log=%q state=%q err=%v want %q", content, logState, err, want)
	}
}

func attemptFor(task Task, revision string, now time.Time, status string) CheckAttempt {
	exit := 1
	if status == AttemptPassed {
		exit = 0
	}
	return CheckAttempt{
		ID: attemptID(revision, now), TaskID: task.ID, RepositoryID: task.RepositoryID, RevisionOID: revision,
		WorktreeState: WorktreeClean, StartedAt: now, FinishedAt: now.Add(time.Second), CreatedAt: now,
		Protection: ProtectionUnknown, ExecutionScope: ExecutionScopeInherited,
		Checks:  []CheckDefinition{{Name: "unit", Command: "go test ./..."}},
		Results: []CheckResult{{Name: "unit", Command: "go test ./...", Status: status, ExitCode: &exit, DurationMS: 1000, OutputExcerpt: "out"}},
	}
}

// attemptID derives a stable 32 character identity from the tested revision and
// start time, so a retransmit of the same attempt reuses it.
func attemptID(revision string, now time.Time) string {
	hash := sha256.Sum256([]byte(revision + now.UTC().Format(time.RFC3339Nano)))
	return hex.EncodeToString(hash[:16])
}

func cycleIDFor(sequence int) string {
	hash := sha256.Sum256([]byte("cycle:" + string(rune('a'+sequence))))
	return hex.EncodeToString(hash[:16])
}

// TestChangedReplayAfterLogRemovalKeepsAcceptedBytes covers a retransmit whose
// disposable raw row was removed. The accepted completion owns the bytes, so a
// changed replay must not recreate it with different content.
func TestChangedReplayAfterLogRemovalKeepsAcceptedBytes(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	now := time.Unix(1_800_000_000, 0)
	if err := store.AddRepository(ctx, Repository{ID: "project", Name: "Project", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	task, err := store.CreateTask(ctx, "project", "Fix the build", now)
	if err != nil {
		t.Fatal(err)
	}
	attempt := attemptFor(task, "1111111111111111111111111111111111111111", now, AttemptFailed)
	_, stored := recordAttemptWithLog(t, store, attempt, "accepted log\n")
	if stored.LogID == "" {
		t.Fatalf("no log was accepted: %+v", stored)
	}
	if _, err := store.db.ExecContext(ctx, `DELETE FROM check_raw_logs WHERE attempt_id=?`, stored.LogID); err != nil {
		t.Fatal(err)
	}
	exact := CheckCompletion{
		AttemptID: stored.ID, RepositoryID: stored.RepositoryID, TaskID: stored.TaskID, Results: attempt.Results,
		FinishedAt: attempt.FinishedAt, WorktreeState: attempt.WorktreeState, Log: "accepted log\n",
	}
	_, replayed, err := store.CompleteCheckAttempt(ctx, exact, now)
	if err != nil {
		t.Fatalf("exact replay error=%v", err)
	}
	if replayed.CompletionDigest != stored.CompletionDigest || replayed.LogID != stored.LogID {
		t.Fatalf("exact replay changed the accepted record: %+v", replayed)
	}
	if count := checkRawLogCount(t, store, stored.LogID); count != 0 {
		t.Fatalf("the exact replay recreated %d raw log rows", count)
	}
	changed := exact
	changed.Log = "changed log\n"
	if _, _, err := store.CompleteCheckAttempt(ctx, changed, now); !errors.Is(err, ErrAttemptConflict) {
		t.Fatalf("changed replay error=%v", err)
	}
	// An accepted completion owns its log, so no retry recreates it.
	if count := checkRawLogCount(t, store, stored.LogID); count != 0 {
		t.Fatalf("the changed replay recreated %d raw log rows", count)
	}
}

// TestEmptyReplayAfterLogRemovalKeepsAcceptedBytes covers the same removal with
// an empty replay, which must not recreate an empty row for the accepted
// completion.
func TestEmptyReplayAfterLogRemovalKeepsAcceptedBytes(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	now := time.Unix(1_800_000_000, 0)
	if err := store.AddRepository(ctx, Repository{ID: "project", Name: "Project", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	task, err := store.CreateTask(ctx, "project", "Fix the build", now)
	if err != nil {
		t.Fatal(err)
	}
	attempt := attemptFor(task, "1111111111111111111111111111111111111111", now, AttemptFailed)
	_, stored := recordAttemptWithLog(t, store, attempt, "accepted log\n")
	if _, err := store.db.ExecContext(ctx, `DELETE FROM check_raw_logs WHERE attempt_id=?`, stored.LogID); err != nil {
		t.Fatal(err)
	}
	completion := CheckCompletion{
		AttemptID: stored.ID, RepositoryID: stored.RepositoryID, TaskID: stored.TaskID, Results: attempt.Results,
		FinishedAt: attempt.FinishedAt, WorktreeState: attempt.WorktreeState,
	}
	if _, _, err := store.CompleteCheckAttempt(ctx, completion, now); !errors.Is(err, ErrAttemptConflict) {
		t.Fatalf("empty replay error=%v", err)
	}
	if count := checkRawLogCount(t, store, stored.LogID); count != 0 {
		t.Fatalf("the accepted log was recreated in %d rows", count)
	}
}

// TestCompletingAnOlderAttemptWhileANewerIsPendingKeepsRecoveryValid covers the
// case where the applied attempt exhausts the reserved budget while a newer
// registration is still in flight. The runtime state must survive the portable
// validation that a backup performs.
func TestCompletingAnOlderAttemptWhileANewerIsPendingKeepsRecoveryValid(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	now := time.Unix(1_800_000_000, 0)
	if err := store.AddRepository(ctx, Repository{ID: "project", Name: "Project", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	task, err := store.CreateTask(ctx, "project", "Fix the build", now)
	if err != nil {
		t.Fatal(err)
	}
	for index := 1; index <= CorrectionCycleLimit; index++ {
		if task, _, err = store.ReserveCorrectionCycle(ctx, "project", task.ID, cycleIDFor(index), now); err != nil {
			t.Fatal(err)
		}
	}
	older := attemptFor(task, "1111111111111111111111111111111111111111", now, AttemptFailed)
	newer := attemptFor(task, "2222222222222222222222222222222222222222", now.Add(time.Minute), AttemptFailed)
	if _, _, err := store.RegisterCheckAttempt(ctx, older); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.RegisterCheckAttempt(ctx, newer); err != nil {
		t.Fatal(err)
	}
	completion := CheckCompletion{
		AttemptID: older.ID, RepositoryID: "project", TaskID: older.TaskID, Results: older.Results,
		FinishedAt: older.FinishedAt, WorktreeState: older.WorktreeState,
	}
	task, _, err = store.CompleteCheckAttempt(ctx, completion, now)
	if err != nil {
		t.Fatal(err)
	}
	// The newer registration is still in flight, so the task stays active even
	// though the applied attempt exhausted the reserved budget.
	if task.Status != TaskActive || task.PendingAttemptID != newer.ID {
		t.Fatalf("task after the older completion = %+v", task)
	}
	completeTestSetup(t, store)
	if _, err := store.RecoverySnapshot(ctx); err != nil {
		t.Fatalf("portable state rejected the runtime state: %v", err)
	}
}

// TestCompletingTheNewestPendingAttemptDoesNotPointAtAnOlderOne covers the
// reverse order. The older unresolved registration is historical, so the task
// must not advertise it as the current pending attempt.
func TestCompletingTheNewestPendingAttemptDoesNotPointAtAnOlderOne(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	now := time.Unix(1_800_000_000, 0)
	if err := store.AddRepository(ctx, Repository{ID: "project", Name: "Project", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	task, err := store.CreateTask(ctx, "project", "Fix the build", now)
	if err != nil {
		t.Fatal(err)
	}
	older := attemptFor(task, "1111111111111111111111111111111111111111", now, AttemptFailed)
	newer := attemptFor(task, "2222222222222222222222222222222222222222", now.Add(time.Minute), AttemptFailed)
	if _, _, err := store.RegisterCheckAttempt(ctx, older); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.RegisterCheckAttempt(ctx, newer); err != nil {
		t.Fatal(err)
	}
	completion := CheckCompletion{
		AttemptID: newer.ID, RepositoryID: "project", TaskID: newer.TaskID, Results: newer.Results,
		FinishedAt: newer.FinishedAt, WorktreeState: newer.WorktreeState,
	}
	task, _, err = store.CompleteCheckAttempt(ctx, completion, now)
	if err != nil {
		t.Fatal(err)
	}
	if task.PendingAttemptID != "" {
		t.Fatalf("pending attempt=%q want none after the newest completion", task.PendingAttemptID)
	}
	completeTestSetup(t, store)
	if _, err := store.RecoverySnapshot(ctx); err != nil {
		t.Fatalf("portable state rejected the runtime state: %v", err)
	}
}

func TestNewestPendingAttemptIsStableAcrossRecoveryValidation(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	now := time.Unix(1_800_000_000, 0)
	if err := store.AddRepository(ctx, Repository{ID: "project", Name: "Project", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	task, err := store.CreateTask(ctx, "project", "Pending order", now)
	if err != nil {
		t.Fatal(err)
	}
	task, _ = recordAttempt(t, store, attemptFor(task, "1111111111111111111111111111111111111111", now, AttemptFailed))
	older := attemptFor(task, "2222222222222222222222222222222222222222", now.Add(time.Minute), AttemptFailed)
	newer := attemptFor(task, "3333333333333333333333333333333333333333", now.Add(2*time.Minute), AttemptFailed)
	if _, _, err := store.RegisterCheckAttempt(ctx, older); err != nil {
		t.Fatal(err)
	}
	task, newest, err := store.RegisterCheckAttempt(ctx, newer)
	if err != nil {
		t.Fatal(err)
	}
	if task.PendingAttemptID != newest.ID {
		t.Fatalf("pending attempt=%q want highest-sequence %q", task.PendingAttemptID, newest.ID)
	}
	completeTestSetup(t, store)
	var snapshot RecoveryState
	for iteration := 0; iteration < 64; iteration++ {
		snapshot, err = store.RecoverySnapshot(ctx)
		if err != nil {
			t.Fatalf("recovery validation changed pending selection on iteration %d: %v", iteration, err)
		}
	}
	restored := openTestStore(t)
	if err := restored.RestoreRecoveryState(ctx, t.TempDir(), snapshot); err != nil {
		t.Fatalf("restore multiple pending attempts: %v", err)
	}
	restoredTask, exists, err := restored.Task(ctx, "project", task.ID)
	if err != nil || !exists || restoredTask.PendingAttemptID != newest.ID || restoredTask.LastAppliedSequence != 1 || restoredTask.LastRegisteredSequence != 3 {
		t.Fatalf("restored pending projection=%+v exists=%v err=%v", restoredTask, exists, err)
	}
}

func TestMultipleAttemptsCanUseOneReservedCycle(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	now := time.Unix(1_800_000_000, 0)
	if err := store.AddRepository(ctx, Repository{ID: "project", Name: "Project", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	task, err := store.CreateTask(ctx, "project", "Retry one correction", now)
	if err != nil {
		t.Fatal(err)
	}
	task, _ = recordAttempt(t, store, attemptFor(task, "1111111111111111111111111111111111111111", now, AttemptFailed))
	cycleID := cycleIDFor(1)
	task, _, err = store.ReserveCorrectionCycle(ctx, "project", task.ID, cycleID, now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	first := attemptFor(task, "2222222222222222222222222222222222222222", now.Add(2*time.Minute), AttemptFailed)
	first.CycleID = cycleID
	_, firstStored := recordAttempt(t, store, first)
	second := attemptFor(task, "3333333333333333333333333333333333333333", now.Add(3*time.Minute), AttemptPassed)
	second.CycleID = cycleID
	task, secondStored := recordAttempt(t, store, second)
	if task.Status != TaskResolved || task.CorrectionCyclesUsed != 1 || task.LastAppliedAttemptID != secondStored.ID {
		t.Fatalf("second execution did not decide the reserved round: task=%+v", task)
	}
	replay := first
	replay.CreatedAt = now.Add(4 * time.Minute)
	task, replayed, err := store.RegisterCheckAttempt(ctx, replay)
	if err != nil || replayed.ID != firstStored.ID || replayed.Sequence != firstStored.Sequence {
		t.Fatalf("exact replay in a reused cycle=%+v err=%v", replayed, err)
	}
	if task.LastAppliedAttemptID != secondStored.ID || task.Status != TaskResolved {
		t.Fatalf("older replay changed the task projection: %+v", task)
	}
	cycles, err := store.CheckCycles(ctx, "project", task.ID)
	if err != nil || len(cycles) != 1 || cycles[0].AttemptID != secondStored.ID {
		t.Fatalf("derived cycle attempt=%+v err=%v", cycles, err)
	}
	if firstStored.CycleID != cycleID || secondStored.CycleID != cycleID {
		t.Fatalf("attempts lost cycle binding: first=%+v second=%+v", firstStored, secondStored)
	}
	completeTestSetup(t, store)
	snapshot, err := store.RecoverySnapshot(ctx)
	if err != nil {
		t.Fatalf("multiple attempts in one reserved cycle were rejected: %v", err)
	}
	restored := openTestStore(t)
	if err := restored.RestoreRecoveryState(ctx, t.TempDir(), snapshot); err != nil {
		t.Fatalf("restore authoritative check facts: %v", err)
	}
	restoredTask, exists, err := restored.Task(ctx, "project", task.ID)
	if err != nil || !exists || restoredTask.Status != TaskResolved || restoredTask.CorrectionCyclesUsed != 1 || restoredTask.LastAppliedAttemptID != secondStored.ID {
		t.Fatalf("restored task projection=%+v exists=%v err=%v", restoredTask, exists, err)
	}
	restoredCycles, err := restored.CheckCycles(ctx, "project", task.ID)
	if err != nil || len(restoredCycles) != 1 || restoredCycles[0].AttemptID != secondStored.ID {
		t.Fatalf("restored cycle projection=%+v err=%v", restoredCycles, err)
	}
	rebacked, err := restored.RecoverySnapshot(ctx)
	if err != nil {
		t.Fatalf("snapshot restored facts: %v", err)
	}
	if !reflect.DeepEqual(rebacked, snapshot) {
		t.Fatalf("restore and re-snapshot changed authoritative facts:\nfirst=%+v\nagain=%+v", snapshot, rebacked)
	}
}

// TestLatestAttemptForARevisionUsesRepositoryOrderNotTaskSequence covers two
// tasks for one revision whose task-local sequences differ. The most recently
// registered attempt is the newest evidence, not the highest task-local
// sequence.
func TestLatestAttemptForARevisionUsesRepositoryOrderNotTaskSequence(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	now := time.Unix(1_800_000_000, 0)
	if err := store.AddRepository(ctx, Repository{ID: "project", Name: "Project", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	revision := "1111111111111111111111111111111111111111"
	historical, err := store.CreateTask(ctx, "project", "Old task", now)
	if err != nil {
		t.Fatal(err)
	}
	var last CheckAttempt
	for index := 0; index < 10; index++ {
		attempt := attemptFor(historical, revision, now.Add(time.Duration(index)*time.Second), AttemptFailed)
		if _, last, err = store.RegisterCheckAttempt(ctx, attempt); err != nil {
			t.Fatal(err)
		}
	}
	if last.Sequence != 10 {
		t.Fatalf("historical sequence=%d want 10", last.Sequence)
	}
	recent, err := store.CreateTask(ctx, "project", "New task", now.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	attempt := attemptFor(recent, revision, now.Add(time.Hour), AttemptFailed)
	_, newest, err := store.RegisterCheckAttempt(ctx, attempt)
	if err != nil {
		t.Fatal(err)
	}
	// The sequence is repository-wide, so the newer task continues the counter
	// instead of restarting at one.
	if newest.Sequence != 11 {
		t.Fatalf("new task sequence=%d want 11", newest.Sequence)
	}
	latest, found, err := store.LatestCheckAttemptForRevision(ctx, "project", revision)
	if err != nil || !found {
		t.Fatalf("latest attempt found=%v err=%v", found, err)
	}
	if latest.ID != newest.ID {
		t.Fatalf("latest attempt=%s want the most recently registered %s", latest.ID, newest.ID)
	}
}

// TestConcurrentCreationIdentityIsIdempotent covers two requests that share one
// operation identity. They must not create duplicate authority or leak a
// uniqueness error.
func TestConcurrentCreationIdentityIsIdempotent(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	now := time.Unix(1_800_000_000, 0)
	if err := store.AddRepository(ctx, Repository{ID: "project", Name: "Project", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	creationID := "0123456789abcdef0123456789abcdef"
	hash := sha256.Sum256([]byte("token"))
	type outcome struct {
		credential HelperCredential
		created    bool
		err        error
	}
	results := make([]outcome, 2)
	start := make(chan struct{})
	var wait sync.WaitGroup
	for index := range results {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			<-start
			credential, created, err := store.CreateHelperCredential(ctx, "project", "laptop", creationID, hash[:], now)
			results[index] = outcome{credential, created, err}
		}(index)
	}
	close(start)
	wait.Wait()
	for index, result := range results {
		if result.err != nil {
			t.Fatalf("concurrent create %d error=%v", index, result.err)
		}
	}
	if results[0].credential.ID != results[1].credential.ID {
		t.Fatalf("duplicate authority: %s and %s", results[0].credential.ID, results[1].credential.ID)
	}
	if results[0].created == results[1].created {
		t.Fatalf("created flags=%v and %v", results[0].created, results[1].created)
	}
}

// TestCleanRegistrationWithDirtyCompletionIsNotCertifiedClean covers a check
// that dirties the tree. The registration observation and the submitted
// observation are stored separately, and the effective state is the worse one.
func TestCleanRegistrationWithDirtyCompletionIsNotCertifiedClean(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	now := time.Unix(1_800_000_000, 0)
	if err := store.AddRepository(ctx, Repository{ID: "project", Name: "Project", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	task, err := store.CreateTask(ctx, "project", "Fix the build", now)
	if err != nil {
		t.Fatal(err)
	}
	attempt := attemptFor(task, "1111111111111111111111111111111111111111", now, AttemptPassed)
	attempt.WorktreeState = WorktreeClean
	_, registered, err := store.RegisterCheckAttempt(ctx, attempt)
	if err != nil {
		t.Fatal(err)
	}
	completion := CheckCompletion{
		AttemptID: registered.ID, RepositoryID: "project", TaskID: task.ID, Results: attempt.Results,
		FinishedAt: attempt.FinishedAt, WorktreeState: WorktreeDirty,
	}
	_, stored, err := store.CompleteCheckAttempt(ctx, completion, now)
	if err != nil {
		t.Fatal(err)
	}
	if stored.WorktreeState != WorktreeClean || stored.SubmittedWorktreeState != WorktreeDirty {
		t.Fatalf("stored observations = %q and %q", stored.WorktreeState, stored.SubmittedWorktreeState)
	}
	if stored.EffectiveWorktreeState() != WorktreeDirty {
		t.Fatalf("effective worktree state = %q want dirty", stored.EffectiveWorktreeState())
	}
	// The portable state must survive the same validation.
	completeTestSetup(t, store)
	if _, err := store.RecoverySnapshot(ctx); err != nil {
		t.Fatalf("portable state rejected the observations: %v", err)
	}
}

type injectedSQLiteError struct {
	code int
}

func (err injectedSQLiteError) Error() string {
	return fmt.Sprintf("injected SQLite code %d", err.code)
}
func (err injectedSQLiteError) Code() int { return err.code }

func TestRawLogStorageFailureUsesOneFreshMetadataTransaction(t *testing.T) {
	for _, test := range []struct {
		name string
		code int
	}{
		{name: "full", code: 13},
		{name: "extended IOERR", code: 10 | 3<<8},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := openTestStore(t)
			ctx := context.Background()
			now := time.Unix(1_800_000_000, 0)
			if err := store.AddRepository(ctx, Repository{ID: "project", Name: "Project", CreatedAt: now}); err != nil {
				t.Fatal(err)
			}
			task, err := store.CreateTask(ctx, "project", "Store metadata after a log failure", now)
			if err != nil {
				t.Fatal(err)
			}
			attempt := attemptFor(task, strings.Repeat("1", 40), now, AttemptFailed)
			_, registered, err := store.RegisterCheckAttempt(ctx, attempt)
			if err != nil {
				t.Fatal(err)
			}
			completion := CheckCompletion{
				AttemptID: registered.ID, RepositoryID: registered.RepositoryID, TaskID: registered.TaskID,
				Results: attempt.Results, FinishedAt: attempt.FinishedAt, WorktreeState: attempt.WorktreeState, Log: "raw log",
			}
			ops := defaultCheckCompletionOps()
			insertions := 0
			ops.insertRawLog = func(context.Context, *sql.Tx, string, []byte, int64) error {
				insertions++
				return injectedSQLiteError{code: test.code}
			}
			_, stored, err := store.completeCheckAttempt(ctx, completion, nil, now, ops)
			if err != nil {
				t.Fatal(err)
			}
			if insertions != 1 {
				t.Fatalf("raw insertions=%d want 1", insertions)
			}
			if stored.Status != AttemptFailed || stored.LogID != "" || stored.LogDigest != "" || stored.LogExpiresAt != nil || stored.LogError == "" {
				t.Fatalf("metadata-only outcome=%+v", stored)
			}
			if stored.SubmittedLogDigest != digestFields(completion.Log) || stored.CompletionDigest == "" || len(stored.Results) != len(completion.Results) {
				t.Fatalf("metadata-only completion lost durable facts: %+v", stored)
			}
			if count := checkRawLogCount(t, store, registered.ID); count != 0 {
				t.Fatalf("failed BLOB insert left %d rows", count)
			}

			_, replayed, err := store.CompleteCheckAttempt(ctx, completion, now.Add(time.Hour))
			if err != nil {
				t.Fatalf("exact replay error=%v", err)
			}
			if replayed.CompletionDigest != stored.CompletionDigest || replayed.LogError != stored.LogError || checkRawLogCount(t, store, registered.ID) != 0 {
				t.Fatalf("exact replay changed the metadata-only outcome: %+v", replayed)
			}
			changed := completion
			changed.Log = "changed raw log"
			if _, _, err := store.CompleteCheckAttempt(ctx, changed, now.Add(2*time.Hour)); !errors.Is(err, ErrAttemptConflict) {
				t.Fatalf("changed replay error=%v", err)
			}
			unchanged, exists, err := store.CheckAttemptByID(ctx, "project", registered.ID)
			if err != nil || !exists || unchanged.LogError != stored.LogError || unchanged.CompletionDigest != stored.CompletionDigest {
				t.Fatalf("changed replay altered fallback metadata=%+v exists=%v err=%v", unchanged, exists, err)
			}
		})
	}
}

func TestMetadataFallbackFailureIsBounded(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	now := time.Unix(1_800_000_000, 0)
	if err := store.AddRepository(ctx, Repository{ID: "project", Name: "Project", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	task, err := store.CreateTask(ctx, "project", "Bound a failed fallback", now)
	if err != nil {
		t.Fatal(err)
	}
	attempt := attemptFor(task, strings.Repeat("8", 40), now, AttemptFailed)
	_, registered, err := store.RegisterCheckAttempt(ctx, attempt)
	if err != nil {
		t.Fatal(err)
	}
	completion := CheckCompletion{
		AttemptID: registered.ID, RepositoryID: registered.RepositoryID, TaskID: registered.TaskID,
		Results: attempt.Results, FinishedAt: attempt.FinishedAt, WorktreeState: attempt.WorktreeState, Log: "raw log",
	}
	fallbackErr := errors.New("injected metadata commit failure")
	ops := defaultCheckCompletionOps()
	insertions, commits := 0, 0
	ops.insertRawLog = func(context.Context, *sql.Tx, string, []byte, int64) error {
		insertions++
		return injectedSQLiteError{code: 13}
	}
	ops.commit = func(tx *sql.Tx) error {
		commits++
		if err := tx.Rollback(); err != nil {
			return err
		}
		return fallbackErr
	}
	if _, _, err := store.completeCheckAttempt(ctx, completion, nil, now, ops); !errors.Is(err, fallbackErr) {
		t.Fatalf("bounded fallback error=%v", err)
	}
	if insertions != 1 || commits != 1 {
		t.Fatalf("raw insertions=%d commits=%d", insertions, commits)
	}
	reloaded, exists, err := store.CheckAttemptByID(ctx, "project", registered.ID)
	if err != nil || !exists || reloaded.Status != AttemptPending || checkRawLogCount(t, store, registered.ID) != 0 {
		t.Fatalf("attempt after failed fallback=%+v exists=%v err=%v", reloaded, exists, err)
	}
}

// TestEmptyConfiguredSetIsUnavailableNotPassed covers a helper that declared no
// checks. An empty set is honest unavailable evidence.
func TestEmptyConfiguredSetIsUnavailableNotPassed(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	now := time.Unix(1_800_000_000, 0)
	if err := store.AddRepository(ctx, Repository{ID: "project", Name: "Project", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	task, err := store.CreateTask(ctx, "project", "Fix the build", now)
	if err != nil {
		t.Fatal(err)
	}
	attempt := attemptFor(task, "1111111111111111111111111111111111111111", now, AttemptPassed)
	attempt.Checks = nil
	attempt.Results = nil
	_, stored := recordAttempt(t, store, attempt)
	if stored.Status != AttemptUnavailable {
		t.Fatalf("empty set status=%q want unavailable", stored.Status)
	}
	if stored.Summary != "0 checks" {
		t.Fatalf("empty set summary=%q", stored.Summary)
	}
	// An empty cancelled execution stays cancellation.
	cancelled := attemptFor(task, "2222222222222222222222222222222222222222", now.Add(time.Minute), AttemptCancelled)
	cancelled.Status = AttemptCancelled
	cancelled.Checks = nil
	cancelled.Results = nil
	_, storedCancelled := recordAttempt(t, store, cancelled)
	if storedCancelled.Status != AttemptCancelled {
		t.Fatalf("empty cancelled status=%q", storedCancelled.Status)
	}
}

// TestPreReservationAttemptCannotCertifyTheReservedRound covers the immutable
// reservation boundary. An attempt registered before the reservation cannot
// certify the round even when it finishes later.
func TestPreReservationAttemptCannotCertifyTheReservedRound(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	now := time.Unix(1_800_000_000, 0)
	if err := store.AddRepository(ctx, Repository{ID: "project", Name: "Project", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	task, err := store.CreateTask(ctx, "project", "Fix the build", now)
	if err != nil {
		t.Fatal(err)
	}
	// The initial check passes, so the task is resolved.
	task, _ = recordAttempt(t, store, attemptFor(task, "1111111111111111111111111111111111111111", now, AttemptPassed))
	if task.Status != TaskResolved {
		t.Fatalf("task after the initial pass = %+v", task)
	}
	// A pre-reservation attempt is registered, then the round is reserved.
	pre := attemptFor(task, "2222222222222222222222222222222222222222", now.Add(time.Minute), AttemptFailed)
	if _, _, err := store.RegisterCheckAttempt(ctx, pre); err != nil {
		t.Fatal(err)
	}
	task, cycle, err := store.ReserveCorrectionCycle(ctx, "project", task.ID, cycleIDFor(1), now.Add(2*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	// The initial check and the pre-reservation attempt both precede the
	// reservation, so the boundary is the counter observed at reservation.
	if cycle.ReservedAfterSequence != 2 {
		t.Fatalf("reservation boundary=%d want 2", cycle.ReservedAfterSequence)
	}
	// The pre-reservation attempt finishes later and fails.
	completion := CheckCompletion{
		AttemptID: pre.ID, RepositoryID: "project", TaskID: task.ID, Results: pre.Results,
		FinishedAt: now.Add(3 * time.Minute), WorktreeState: pre.WorktreeState,
	}
	task, _, err = store.CompleteCheckAttempt(ctx, completion, now.Add(3*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	// The round is still uncertified, so the task stays active.
	if task.Status != TaskActive {
		t.Fatalf("task after the pre-reservation failure = %+v", task)
	}
	// A manual completion after the boundary resolves the task without
	// refunding the reserved budget.
	manual := attemptFor(task, "3333333333333333333333333333333333333333", now.Add(4*time.Minute), AttemptPassed)
	task, _ = recordAttempt(t, store, manual)
	if task.Status != TaskResolved || task.CorrectionCyclesUsed != 1 {
		t.Fatalf("task after the manual pass = %+v", task)
	}
	completeTestSetup(t, store)
	if _, err := store.RecoverySnapshot(ctx); err != nil {
		t.Fatalf("portable state rejected the boundary: %v", err)
	}
}

func TestRawLogAndCompletionMetadataRollBackTogether(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	now := time.Unix(1_800_000_000, 0)
	if err := store.AddRepository(ctx, Repository{ID: "project", Name: "Project", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	task, err := store.CreateTask(ctx, "project", "Roll back an incomplete completion", now)
	if err != nil {
		t.Fatal(err)
	}
	attempt := attemptFor(task, strings.Repeat("1", 40), now, AttemptFailed)
	_, registered, err := store.RegisterCheckAttempt(ctx, attempt)
	if err != nil {
		t.Fatal(err)
	}
	completion := CheckCompletion{
		AttemptID: registered.ID, RepositoryID: registered.RepositoryID, TaskID: registered.TaskID,
		Results: attempt.Results, FinishedAt: attempt.FinishedAt, WorktreeState: attempt.WorktreeState, Log: "submitted bytes",
	}
	injected := errors.New("injected failure after raw insert")
	ops := defaultCheckCompletionOps()
	insertRawLog := ops.insertRawLog
	ops.insertRawLog = func(ctx context.Context, tx *sql.Tx, attemptID string, content []byte, expiresAt int64) error {
		if err := insertRawLog(ctx, tx, attemptID, content, expiresAt); err != nil {
			return err
		}
		return injected
	}
	if _, _, err := store.completeCheckAttempt(ctx, completion, nil, now, ops); !errors.Is(err, injected) {
		t.Fatalf("completion error=%v", err)
	}
	if count := checkRawLogCount(t, store, registered.ID); count != 0 {
		t.Fatalf("rolled-back raw log rows=%d", count)
	}
	reloaded, exists, err := store.CheckAttemptByID(ctx, "project", registered.ID)
	if err != nil || !exists || reloaded.Status != AttemptPending || reloaded.CompletionDigest != "" || len(reloaded.Results) != 0 {
		t.Fatalf("rolled-back attempt=%+v exists=%v err=%v", reloaded, exists, err)
	}
}

func TestCompletionReconcilesCommitAmbiguity(t *testing.T) {
	for _, test := range []struct {
		name             string
		commitThenError  bool
		storageError     bool
		wantSuccess      bool
		wantRaw          int
		wantCommitCalls  int
		wantMetadataOnly bool
	}{
		{name: "commit succeeded before error", commitThenError: true, wantSuccess: true, wantRaw: 1, wantCommitCalls: 1},
		{name: "commit rolled back", wantCommitCalls: 1},
		{name: "FULL rolled back then falls back", storageError: true, wantSuccess: true, wantCommitCalls: 2, wantMetadataOnly: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := openTestStore(t)
			ctx := context.Background()
			now := time.Unix(1_800_000_000, 0)
			if err := store.AddRepository(ctx, Repository{ID: "project", Name: "Project", CreatedAt: now}); err != nil {
				t.Fatal(err)
			}
			task, err := store.CreateTask(ctx, "project", "Resolve an ambiguous commit", now)
			if err != nil {
				t.Fatal(err)
			}
			attempt := attemptFor(task, strings.Repeat("6", 40), now, AttemptFailed)
			_, registered, err := store.RegisterCheckAttempt(ctx, attempt)
			if err != nil {
				t.Fatal(err)
			}
			completion := CheckCompletion{
				AttemptID: registered.ID, RepositoryID: registered.RepositoryID, TaskID: registered.TaskID,
				Results: attempt.Results, FinishedAt: attempt.FinishedAt, WorktreeState: attempt.WorktreeState, Log: "raw bytes",
			}
			commitErr := errors.New("injected ambiguous commit")
			ops := defaultCheckCompletionOps()
			commitCalls := 0
			ops.commit = func(tx *sql.Tx) error {
				commitCalls++
				if test.storageError && commitCalls == 2 {
					return tx.Commit()
				}
				if test.commitThenError {
					if err := tx.Commit(); err != nil {
						return err
					}
					return commitErr
				}
				if err := tx.Rollback(); err != nil {
					return err
				}
				if test.storageError {
					return injectedSQLiteError{code: 13}
				}
				return commitErr
			}
			_, stored, err := store.completeCheckAttempt(ctx, completion, nil, now, ops)
			if test.wantSuccess {
				if err != nil {
					t.Fatal(err)
				}
			} else if !errors.Is(err, commitErr) {
				t.Fatalf("completion error=%v", err)
			}
			if commitCalls != test.wantCommitCalls {
				t.Fatalf("commit calls=%d want %d", commitCalls, test.wantCommitCalls)
			}
			if count := checkRawLogCount(t, store, registered.ID); count != test.wantRaw {
				t.Fatalf("raw log rows=%d want %d", count, test.wantRaw)
			}
			if test.wantMetadataOnly && (stored.Status != AttemptFailed || stored.LogID != "" || stored.LogError == "") {
				t.Fatalf("fallback outcome=%+v", stored)
			}
			if !test.wantSuccess {
				reloaded, exists, loadErr := store.CheckAttemptByID(ctx, "project", registered.ID)
				if loadErr != nil || !exists || reloaded.Status != AttemptPending {
					t.Fatalf("pending after rollback=%+v exists=%v err=%v", reloaded, exists, loadErr)
				}
			}
		})
	}
}

func TestRealSQLiteFullUsesMetadataOnlyFallback(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	now := time.Unix(1_800_000_000, 0)
	if err := store.AddRepository(ctx, Repository{ID: "project", Name: "Project", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	task, err := store.CreateTask(ctx, "project", "Survive a full database", now)
	if err != nil {
		t.Fatal(err)
	}
	attempt := attemptFor(task, strings.Repeat("2", 40), now, AttemptFailed)
	_, registered, err := store.RegisterCheckAttempt(ctx, attempt)
	if err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		`CREATE TABLE full_reserve(content BLOB)`,
		`INSERT INTO full_reserve(content) VALUES(zeroblob(131072))`,
		`DROP TABLE full_reserve`,
		`PRAGMA wal_checkpoint(TRUNCATE)`,
	} {
		if _, err := store.db.ExecContext(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
	var pageCount int
	if err := store.db.QueryRowContext(ctx, `PRAGMA page_count`).Scan(&pageCount); err != nil {
		t.Fatal(err)
	}
	var acceptedLimit int
	if err := store.db.QueryRowContext(ctx, fmt.Sprintf(`PRAGMA max_page_count=%d`, pageCount)).Scan(&acceptedLimit); err != nil || acceptedLimit != pageCount {
		t.Fatalf("max page count=%d want %d err=%v", acceptedLimit, pageCount, err)
	}
	completion := CheckCompletion{
		AttemptID: registered.ID, RepositoryID: registered.RepositoryID, TaskID: registered.TaskID,
		Results: attempt.Results, FinishedAt: attempt.FinishedAt, WorktreeState: attempt.WorktreeState,
		Log: strings.Repeat("x", MaximumCheckLogBytes),
	}
	ops := defaultCheckCompletionOps()
	insertRawLog := ops.insertRawLog
	insertions, commits := 0, 0
	ops.insertRawLog = func(ctx context.Context, tx *sql.Tx, attemptID string, content []byte, expiresAt int64) error {
		insertions++
		return insertRawLog(ctx, tx, attemptID, content, expiresAt)
	}
	ops.commit = func(tx *sql.Tx) error {
		commits++
		return tx.Commit()
	}
	_, stored, err := store.completeCheckAttempt(ctx, completion, nil, now, ops)
	if err != nil {
		t.Fatal(err)
	}
	if insertions != 1 || commits != 1 {
		t.Fatalf("raw insertions=%d commits=%d, want one attempted insert and one fallback commit", insertions, commits)
	}
	if stored.LogID != "" || stored.LogDigest != "" || stored.LogExpiresAt != nil || stored.LogError == "" {
		t.Fatalf("FULL outcome=%+v", stored)
	}
	if stored.Status != AttemptFailed || stored.SubmittedLogDigest != digestFields(completion.Log) || stored.CompletionDigest == "" {
		t.Fatalf("FULL fallback lost completion facts: %+v", stored)
	}
	if count := checkRawLogCount(t, store, registered.ID); count != 0 {
		t.Fatalf("FULL left %d raw log rows", count)
	}
}

func TestLegacyLogDirectoryAndStagingFilesRemainUntouched(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	now := time.Unix(1_800_000_000, 0)
	legacyDirectory := filepath.Join(store.dir, "logs")
	if err := os.Mkdir(legacyDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	legacyFiles := map[string][]byte{
		"accepted.log":       []byte("legacy accepted bytes"),
		".pending.staging-1": []byte("legacy staging bytes"),
	}
	for name, content := range legacyFiles {
		if err := os.WriteFile(filepath.Join(legacyDirectory, name), content, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.AddRepository(ctx, Repository{ID: "project", Name: "Project", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	task, err := store.CreateTask(ctx, "project", "Ignore legacy filesystem logs", now)
	if err != nil {
		t.Fatal(err)
	}
	attempt := attemptFor(task, strings.Repeat("3", 40), now, AttemptFailed)
	_, stored := recordAttemptWithLog(t, store, attempt, "database bytes")
	if stored.LogID != attempt.ID || stored.LogError != "" {
		t.Fatalf("database raw log outcome=%+v", stored)
	}
	if removed, err := store.PruneCheckLogs(ctx, now.Add(100*365*24*time.Hour)); err != nil || removed != 1 {
		t.Fatalf("database prune removed=%d err=%v", removed, err)
	}
	for name, want := range legacyFiles {
		got, err := os.ReadFile(filepath.Join(legacyDirectory, name))
		if err != nil || !bytes.Equal(got, want) {
			t.Fatalf("legacy file %q=%q err=%v", name, got, err)
		}
	}
}

func TestLegacyLogDirectorySymlinkRemainsUntouched(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	now := time.Unix(1_800_000_000, 0)
	target := t.TempDir()
	sentinel := filepath.Join(target, "sentinel")
	if err := os.WriteFile(sentinel, []byte("unchanged"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(store.dir, "logs")); err != nil {
		t.Skipf("create directory symlink: %v", err)
	}
	if err := store.AddRepository(ctx, Repository{ID: "project", Name: "Project", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	task, err := store.CreateTask(ctx, "project", "Ignore a legacy log symlink", now)
	if err != nil {
		t.Fatal(err)
	}
	attempt := attemptFor(task, strings.Repeat("4", 40), now, AttemptFailed)
	_, stored := recordAttemptWithLog(t, store, attempt, "database bytes")
	if stored.LogID != attempt.ID || stored.LogError != "" {
		t.Fatalf("database raw log outcome=%+v", stored)
	}
	if removed, err := store.PruneCheckLogs(ctx, now.Add(100*365*24*time.Hour)); err != nil || removed != 1 {
		t.Fatalf("database prune removed=%d err=%v", removed, err)
	}
	content, err := os.ReadFile(sentinel)
	if err != nil || string(content) != "unchanged" {
		t.Fatalf("symlink target sentinel=%q err=%v", content, err)
	}
	info, err := os.Lstat(filepath.Join(store.dir, "logs"))
	if err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("legacy symlink mode=%v err=%v", infoMode(info), err)
	}
}

func TestRawLogDigestMismatchIsMissingAndReplayDoesNotRepairIt(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	now := time.Unix(1_800_000_000, 0)
	if err := store.AddRepository(ctx, Repository{ID: "project", Name: "Project", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	task, err := store.CreateTask(ctx, "project", "Detect a damaged raw log", now)
	if err != nil {
		t.Fatal(err)
	}
	attempt := attemptFor(task, strings.Repeat("5", 40), now, AttemptFailed)
	_, stored := recordAttemptWithLog(t, store, attempt, "accepted bytes")
	if _, err := store.db.ExecContext(ctx, `UPDATE check_raw_logs SET content=? WHERE attempt_id=?`, []byte("damaged bytes"), stored.ID); err != nil {
		t.Fatal(err)
	}
	if state := store.CheckLogState(stored.LogID, stored.LogExpiresAt, now); state != CheckLogMissing {
		t.Fatalf("damaged raw log state=%q", state)
	}
	if content, state, err := store.ReadCheckLog(stored.LogID, stored.LogExpiresAt, now); err == nil || state != CheckLogMissing || content != nil {
		t.Fatalf("damaged raw log content=%q state=%q err=%v", content, state, err)
	}
	completion := CheckCompletion{
		AttemptID: stored.ID, RepositoryID: stored.RepositoryID, TaskID: stored.TaskID,
		Results: attempt.Results, FinishedAt: attempt.FinishedAt, WorktreeState: attempt.WorktreeState, Log: "accepted bytes",
	}
	if _, replayed, err := store.CompleteCheckAttempt(ctx, completion, now); err != nil || replayed.CompletionDigest != stored.CompletionDigest {
		t.Fatalf("exact replay=%+v err=%v", replayed, err)
	}
	var damaged []byte
	if err := store.db.QueryRowContext(ctx, `SELECT content FROM check_raw_logs WHERE attempt_id=?`, stored.ID).Scan(&damaged); err != nil || string(damaged) != "damaged bytes" {
		t.Fatalf("replay repaired damaged content=%q err=%v", damaged, err)
	}
}

// TestConcurrentDifferentCompletionsKeepOneHonestOutcome covers two completions
// with different payloads. Exactly one is accepted, and the stored log digest
// describes the raw row that actually exists.
func TestConcurrentDifferentCompletionsKeepOneHonestOutcome(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	now := time.Unix(1_800_000_000, 0)
	if err := store.AddRepository(ctx, Repository{ID: "project", Name: "Project", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	task, err := store.CreateTask(ctx, "project", "Fix the build", now)
	if err != nil {
		t.Fatal(err)
	}
	attempt := attemptFor(task, "1111111111111111111111111111111111111111", now, AttemptFailed)
	if _, _, err := store.RegisterCheckAttempt(ctx, attempt); err != nil {
		t.Fatal(err)
	}
	completions := []CheckCompletion{
		{AttemptID: attempt.ID, RepositoryID: "project", TaskID: task.ID, Results: attempt.Results, FinishedAt: attempt.FinishedAt, WorktreeState: attempt.WorktreeState, Log: "first payload"},
		{AttemptID: attempt.ID, RepositoryID: "project", TaskID: task.ID, Results: attempt.Results, FinishedAt: attempt.FinishedAt, WorktreeState: attempt.WorktreeState, Log: "second payload"},
	}
	errs := make([]error, len(completions))
	start := make(chan struct{})
	var wait sync.WaitGroup
	for index := range completions {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			<-start
			_, _, errs[index] = store.CompleteCheckAttempt(ctx, completions[index], now)
		}(index)
	}
	close(start)
	wait.Wait()
	conflicts := 0
	for _, err := range errs {
		if errors.Is(err, ErrAttemptConflict) {
			conflicts++
		} else if err != nil {
			t.Fatalf("completion error=%v", err)
		}
	}
	if conflicts != 1 {
		t.Fatalf("conflicts=%d want exactly one", conflicts)
	}
	stored, found, err := store.CheckAttemptByID(ctx, "project", attempt.ID)
	if err != nil || !found {
		t.Fatalf("stored attempt found=%v err=%v", found, err)
	}
	content, state, err := store.ReadCheckLog(stored.LogID, stored.LogExpiresAt, now)
	if err != nil || state != CheckLogFound {
		t.Fatalf("accepted raw log state=%q err=%v", state, err)
	}
	if stored.LogDigest != digestFields(string(content)) {
		t.Fatalf("stored log digest does not describe the accepted row")
	}
	if stored.SubmittedLogDigest != digestFields(string(content)) {
		t.Fatalf("submitted log digest does not describe the accepted row")
	}
}
