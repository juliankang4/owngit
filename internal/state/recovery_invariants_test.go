package state

import (
	"context"
	"database/sql"
	"reflect"
	"strings"
	"testing"
	"time"
)

type recoveryInvariantFixture struct {
	snapshot            RecoveryState
	publishedID         string
	truncatedID         string
	fallbackID          string
	pendingID           string
	pendingRegistration CheckAttempt
	publishedCompletion CheckCompletion
}

func newRecoveryInvariantFixture(t *testing.T) recoveryInvariantFixture {
	t.Helper()
	store := openTestStore(t)
	ctx := context.Background()
	now := time.Unix(1_800_000_000, 0)
	for _, repository := range []Repository{
		{ID: "project", Name: "Project", CreatedAt: now},
		{ID: "secondary", Name: "Secondary", CreatedAt: now},
		{ID: "empty", Name: "Empty", CreatedAt: now},
	} {
		noErr(t, store.AddRepository(ctx, repository))
	}
	firstTask, err := store.CreateTask(ctx, "project", "First task", now)
	noErr(t, err)
	secondTask, err := store.CreateTask(ctx, "project", "Second task", now)
	noErr(t, err)
	secondaryTask, err := store.CreateTask(ctx, "secondary", "Secondary task", now)
	noErr(t, err)

	publishedAttempt := attemptFor(firstTask, strings.Repeat("1", 40), now, AttemptFailed)
	_, registered, err := store.RegisterCheckAttempt(ctx, publishedAttempt)
	noErr(t, err)
	publishedCompletion := CheckCompletion{
		AttemptID: registered.ID, RepositoryID: registered.RepositoryID, TaskID: registered.TaskID,
		Results: publishedAttempt.Results, FinishedAt: publishedAttempt.FinishedAt,
		WorktreeState: publishedAttempt.WorktreeState, Log: "published log",
	}
	if _, _, err := store.CompleteCheckAttempt(ctx, publishedCompletion, now); err != nil {
		t.Fatal(err)
	}

	truncatedAttempt := attemptFor(secondTask, strings.Repeat("2", 40), now.Add(time.Minute), AttemptFailed)
	_, registeredTruncated, err := store.RegisterCheckAttempt(ctx, truncatedAttempt)
	noErr(t, err)
	if _, _, err := store.CompleteCheckAttempt(ctx, CheckCompletion{
		AttemptID: registeredTruncated.ID, RepositoryID: registeredTruncated.RepositoryID, TaskID: registeredTruncated.TaskID,
		Results: truncatedAttempt.Results, FinishedAt: truncatedAttempt.FinishedAt,
		WorktreeState: truncatedAttempt.WorktreeState, Log: "truncated log", LogTruncated: true,
	}, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}

	fallbackAttempt := attemptFor(firstTask, strings.Repeat("3", 40), now.Add(2*time.Minute), AttemptFailed)
	_, registeredFallback, err := store.RegisterCheckAttempt(ctx, fallbackAttempt)
	noErr(t, err)
	fallbackCompletion := CheckCompletion{
		AttemptID: registeredFallback.ID, RepositoryID: registeredFallback.RepositoryID, TaskID: registeredFallback.TaskID,
		Results: fallbackAttempt.Results, FinishedAt: fallbackAttempt.FinishedAt,
		WorktreeState: fallbackAttempt.WorktreeState, Log: "unstored log", LogTruncated: true,
	}
	ops := defaultCheckCompletionOps()
	ops.insertRawLog = func(context.Context, *sql.Tx, string, []byte, int64) error {
		return injectedSQLiteError{code: 13}
	}
	if _, stored, err := store.completeCheckAttempt(ctx, fallbackCompletion, nil, now.Add(2*time.Minute), ops); err != nil {
		t.Fatal(err)
	} else if stored.LogError == "" || stored.LogID != "" || stored.LogDigest != "" || stored.LogExpiresAt != nil || stored.LogTruncated || !stored.SubmittedTruncated {
		t.Fatalf("metadata-only fallback=%+v", stored)
	}

	pendingAttempt := attemptFor(secondTask, strings.Repeat("4", 40), now.Add(3*time.Minute), AttemptFailed)
	_, registeredPending, err := store.RegisterCheckAttempt(ctx, pendingAttempt)
	noErr(t, err)
	if _, replayed, err := store.RegisterCheckAttempt(ctx, pendingAttempt); err != nil || replayed.Sequence != registeredPending.Sequence {
		t.Fatalf("registration replay=%+v err=%v", replayed, err)
	}

	secondaryAttempt := attemptFor(secondaryTask, strings.Repeat("5", 40), now.Add(4*time.Minute), AttemptPassed)
	_, secondaryStored := recordAttemptWithLog(t, store, secondaryAttempt, "secondary log")
	if secondaryStored.Sequence != 1 {
		t.Fatalf("secondary repository sequence=%d want 1", secondaryStored.Sequence)
	}

	completeTestSetup(t, store)
	snapshot, err := store.RecoverySnapshot(ctx)
	noErr(t, err)
	if err := ValidateCheckRecovery(snapshot); err != nil {
		t.Fatalf("generated recovery state is invalid: %v", err)
	}
	return recoveryInvariantFixture{
		snapshot: snapshot, publishedID: registered.ID, truncatedID: registeredTruncated.ID,
		fallbackID: registeredFallback.ID, pendingID: registeredPending.ID,
		pendingRegistration: pendingAttempt, publishedCompletion: publishedCompletion,
	}
}

func cloneRecoveryState(snapshot RecoveryState) RecoveryState {
	clone := snapshot
	clone.Repositories = append([]Repository(nil), snapshot.Repositories...)
	clone.CheckAttempts = append([]CheckAttempt(nil), snapshot.CheckAttempts...)
	for index := range clone.CheckAttempts {
		if clone.CheckAttempts[index].LogExpiresAt != nil {
			expiresAt := *clone.CheckAttempts[index].LogExpiresAt
			clone.CheckAttempts[index].LogExpiresAt = &expiresAt
		}
	}
	return clone
}

func recoveryAttemptIndex(t *testing.T, snapshot RecoveryState, attemptID string) int {
	t.Helper()
	for index := range snapshot.CheckAttempts {
		if snapshot.CheckAttempts[index].ID == attemptID {
			return index
		}
	}
	t.Fatalf("attempt %q is absent", attemptID)
	return -1
}

func recoveryRepositoryIndex(t *testing.T, snapshot RecoveryState, repositoryID string) int {
	t.Helper()
	for index := range snapshot.Repositories {
		if snapshot.Repositories[index].ID == repositoryID {
			return index
		}
	}
	t.Fatalf("repository %q is absent", repositoryID)
	return -1
}

func TestCheckRecoveryAcceptsProducerSequenceAndLogStates(t *testing.T) {
	fixture := newRecoveryInvariantFixture(t)
	ctx := context.Background()

	// Repository sequences are facts, so validation does not depend on the
	// order of records in the portable array.
	reordered := cloneRecoveryState(fixture.snapshot)
	for left, right := 0, len(reordered.CheckAttempts)-1; left < right; left, right = left+1, right-1 {
		reordered.CheckAttempts[left], reordered.CheckAttempts[right] = reordered.CheckAttempts[right], reordered.CheckAttempts[left]
	}
	if err := ValidateCheckRecovery(reordered); err != nil {
		t.Fatalf("reordered valid attempts were rejected: %v", err)
	}
	cleanupFailure := cloneRecoveryState(fixture.snapshot)
	fallback := recoveryAttemptIndex(t, cleanupFailure, fixture.fallbackID)
	cleanupFailure.CheckAttempts[fallback].LogError = "the raw log could not be stored; its staging file could not be removed"
	if err := ValidateCheckRecovery(cleanupFailure); err != nil {
		t.Fatalf("supported staging-cleanup failure was rejected: %v", err)
	}

	restored := openTestStore(t)
	if err := restored.RestoreRecoveryState(ctx, t.TempDir(), reordered); err != nil {
		t.Fatalf("restore valid producer state: %v", err)
	}
	rebacked, err := restored.RecoverySnapshot(ctx)
	noErr(t, err)
	if !reflect.DeepEqual(rebacked, fixture.snapshot) {
		t.Fatalf("restore changed portable facts:\nfirst=%+v\nagain=%+v", fixture.snapshot, rebacked)
	}

	// A replay keeps its issued sequence and never restores disposable bytes.
	if _, replayed, err := restored.RegisterCheckAttempt(ctx, fixture.pendingRegistration); err != nil || replayed.Sequence != 4 {
		t.Fatalf("restored registration replay=%+v err=%v", replayed, err)
	}
	if _, replayed, err := restored.CompleteCheckAttempt(ctx, fixture.publishedCompletion, time.Now()); err != nil || replayed.ID != fixture.publishedID {
		t.Fatalf("restored completion replay=%+v err=%v", replayed, err)
	}
	if count := checkRawLogCount(t, restored, fixture.publishedID); count != 0 {
		t.Fatalf("completion replay recreated %d raw log rows", count)
	}
	afterReplay, err := restored.RecoverySnapshot(ctx)
	noErr(t, err)
	project := recoveryRepositoryIndex(t, afterReplay, "project")
	if afterReplay.Repositories[project].AttemptSequence != 4 {
		t.Fatalf("registration replay advanced counter to %d", afterReplay.Repositories[project].AttemptSequence)
	}
}

func TestCheckRecoveryRejectsRepositorySequenceContradictions(t *testing.T) {
	fixture := newRecoveryInvariantFixture(t)
	project := recoveryRepositoryIndex(t, fixture.snapshot, "project")
	empty := recoveryRepositoryIndex(t, fixture.snapshot, "empty")
	first := recoveryAttemptIndex(t, fixture.snapshot, fixture.publishedID)
	second := recoveryAttemptIndex(t, fixture.snapshot, fixture.truncatedID)

	tests := []struct {
		name   string
		mutate func(*RecoveryState)
	}{
		{"negative counter", func(snapshot *RecoveryState) { snapshot.Repositories[project].AttemptSequence = -1 }},
		{"counter exceeds history", func(snapshot *RecoveryState) { snapshot.Repositories[project].AttemptSequence++ }},
		{"counter on repository without attempts", func(snapshot *RecoveryState) { snapshot.Repositories[empty].AttemptSequence = 1 }},
		{"missing initial sequence", func(snapshot *RecoveryState) {
			snapshot.Repositories[project].AttemptSequence = 5
			snapshot.CheckAttempts[first].Sequence = 5
		}},
		{"duplicate sequence across tasks", func(snapshot *RecoveryState) {
			snapshot.CheckAttempts[first].Sequence = snapshot.CheckAttempts[second].Sequence
		}},
		{"sequence above counter", func(snapshot *RecoveryState) { snapshot.CheckAttempts[first].Sequence = 5 }},
		{"cross repository task binding", func(snapshot *RecoveryState) { snapshot.CheckAttempts[first].RepositoryID = "secondary" }},
		{"duplicate repository counter", func(snapshot *RecoveryState) {
			snapshot.Repositories = append(snapshot.Repositories, snapshot.Repositories[project])
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			snapshot := cloneRecoveryState(fixture.snapshot)
			test.mutate(&snapshot)
			if err := ValidateCheckRecovery(snapshot); err == nil {
				t.Fatal("accepted contradictory repository sequence facts")
			}
		})
	}
}

func TestCheckRecoveryRejectsContradictoryLogMetadata(t *testing.T) {
	fixture := newRecoveryInvariantFixture(t)
	published := recoveryAttemptIndex(t, fixture.snapshot, fixture.publishedID)
	truncated := recoveryAttemptIndex(t, fixture.snapshot, fixture.truncatedID)
	fallback := recoveryAttemptIndex(t, fixture.snapshot, fixture.fallbackID)
	pending := recoveryAttemptIndex(t, fixture.snapshot, fixture.pendingID)
	validDigestValue := strings.Repeat("0", 64)
	zeroTime := time.Time{}

	tests := []struct {
		name   string
		mutate func(*RecoveryState)
	}{
		{"malformed published identity", func(snapshot *RecoveryState) { snapshot.CheckAttempts[published].LogID = "../outside" }},
		{"published identity owned by another attempt", func(snapshot *RecoveryState) { snapshot.CheckAttempts[published].LogID = fixture.truncatedID }},
		{"published log with failure", func(snapshot *RecoveryState) {
			snapshot.CheckAttempts[published].LogError = "the raw log could not be stored"
		}},
		{"published log without expiry", func(snapshot *RecoveryState) { snapshot.CheckAttempts[published].LogExpiresAt = nil }},
		{"published log with zero expiry", func(snapshot *RecoveryState) { snapshot.CheckAttempts[published].LogExpiresAt = &zeroTime }},
		{"published log without digest", func(snapshot *RecoveryState) { snapshot.CheckAttempts[published].LogDigest = "" }},
		{"published digest differs from submitted", func(snapshot *RecoveryState) { snapshot.CheckAttempts[published].LogDigest = validDigestValue }},
		{"published truncation differs from submitted", func(snapshot *RecoveryState) { snapshot.CheckAttempts[published].LogTruncated = true }},
		{"truncated publication loses accepted flag", func(snapshot *RecoveryState) { snapshot.CheckAttempts[truncated].LogTruncated = false }},
		{"fallback gains identity", func(snapshot *RecoveryState) { snapshot.CheckAttempts[fallback].LogID = fixture.fallbackID }},
		{"fallback gains digest", func(snapshot *RecoveryState) { snapshot.CheckAttempts[fallback].LogDigest = validDigestValue }},
		{"fallback gains expiry", func(snapshot *RecoveryState) { snapshot.CheckAttempts[fallback].LogExpiresAt = &zeroTime }},
		{"fallback gains accepted truncation", func(snapshot *RecoveryState) { snapshot.CheckAttempts[fallback].LogTruncated = true }},
		{"fallback loses failure", func(snapshot *RecoveryState) { snapshot.CheckAttempts[fallback].LogError = "" }},
		{"fallback has malformed failure", func(snapshot *RecoveryState) { snapshot.CheckAttempts[fallback].LogError = "\n" }},
		{"pending gains identity", func(snapshot *RecoveryState) { snapshot.CheckAttempts[pending].LogID = fixture.pendingID }},
		{"pending gains digest", func(snapshot *RecoveryState) { snapshot.CheckAttempts[pending].LogDigest = validDigestValue }},
		{"pending gains expiry", func(snapshot *RecoveryState) { snapshot.CheckAttempts[pending].LogExpiresAt = &zeroTime }},
		{"pending gains truncation", func(snapshot *RecoveryState) { snapshot.CheckAttempts[pending].LogTruncated = true }},
		{"pending gains failure", func(snapshot *RecoveryState) {
			snapshot.CheckAttempts[pending].LogError = "the raw log could not be stored"
		}},
		{"pending registration digest changes", func(snapshot *RecoveryState) { snapshot.CheckAttempts[pending].RegistrationDigest = validDigestValue }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			snapshot := cloneRecoveryState(fixture.snapshot)
			test.mutate(&snapshot)
			if err := ValidateCheckRecovery(snapshot); err == nil {
				t.Fatal("accepted contradictory log metadata")
			}
		})
	}
}

func TestRestoreRejectsInvalidCheckRecoveryBeforeDestinationMutation(t *testing.T) {
	fixture := newRecoveryInvariantFixture(t)
	invalid := cloneRecoveryState(fixture.snapshot)
	project := recoveryRepositoryIndex(t, invalid, "project")
	invalid.Repositories[project].AttemptSequence++

	destination := openTestStore(t)
	if err := destination.RestoreRecoveryState(context.Background(), t.TempDir(), invalid); err == nil {
		t.Fatal("restored invalid check recovery state")
	}
	settings, err := destination.Settings(context.Background())
	noErr(t, err)
	if settings.Initialized {
		t.Fatal("invalid recovery initialized the destination")
	}
	repositories, err := destination.Repositories(context.Background())
	noErr(t, err)
	if len(repositories) != 0 {
		t.Fatalf("invalid recovery wrote repositories: %+v", repositories)
	}
}
