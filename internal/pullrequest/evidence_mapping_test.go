package pullrequest

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"owngit/internal/state"
)

func TestEvidenceQueryFailuresRemainStructuredAndAdvisory(t *testing.T) {
	t.Run("configuration presence is unknown", func(t *testing.T) {
		fixture, pullRequest, _ := newEvidenceReadFixture(t)
		if err := fixture.store.Exec(fixture.ctx, `DROP TABLE check_configurations`); err != nil {
			t.Fatal(err)
		}
		view, err := fixture.service.Show(fixture.ctx, fixture.repositoryID, pullRequest.Number)
		if err != nil {
			t.Fatal(err)
		}
		if view.Checks.ReadFailure == nil || view.Checks.ReadFailure.Code != ReadFailureCheckConfiguration || view.Checks.Configured || view.Checks.Status != "" {
			t.Fatalf("configuration read failure=%+v", view.Checks)
		}
		assertAdvisoryEvidenceDidNotBlockMerge(t, view)
	})

	t.Run("known configuration survives evidence read failure", func(t *testing.T) {
		fixture, pullRequest, sourceOID := newEvidenceReadFixture(t)
		now := time.Date(2026, 9, 19, 13, 0, 0, 0, time.UTC)
		task, err := fixture.store.CreateTask(fixture.ctx, fixture.repositoryID, "Read check evidence", now)
		if err != nil {
			t.Fatal(err)
		}
		_, _, err = fixture.store.RegisterCheckAttempt(fixture.ctx, state.CheckAttempt{
			ID: "11111111111111111111111111111111", TaskID: task.ID, RepositoryID: fixture.repositoryID,
			RevisionOID: sourceOID, WorktreeState: state.WorktreeClean,
			StartedAt: now, CreatedAt: now, Protection: state.ProtectionUnknown, ExecutionScope: state.ExecutionScopeInherited,
			Checks: []state.CheckDefinition{{Name: "unit", Command: "go test ./..."}},
		})
		if err != nil {
			t.Fatal(err)
		}
		// The attempt query itself succeeds, then its real result query fails.
		if err := fixture.store.Exec(fixture.ctx, `DROP TABLE check_results`); err != nil {
			t.Fatal(err)
		}
		view, err := fixture.service.Show(fixture.ctx, fixture.repositoryID, pullRequest.Number)
		if err != nil {
			t.Fatal(err)
		}
		if view.Checks.ReadFailure == nil || view.Checks.ReadFailure.Code != ReadFailureCheckEvidence || !view.Checks.Configured || view.Checks.Status != "" {
			t.Fatalf("check evidence read failure=%+v", view.Checks)
		}
		assertAdvisoryEvidenceDidNotBlockMerge(t, view)
	})

	t.Run("review read failure is not execution unavailable", func(t *testing.T) {
		fixture, pullRequest, _ := newEvidenceReadFixture(t)
		if err := fixture.store.Exec(fixture.ctx, `DROP TABLE pull_request_reviews`); err != nil {
			t.Fatal(err)
		}
		view, err := fixture.service.Show(fixture.ctx, fixture.repositoryID, pullRequest.Number)
		if err != nil {
			t.Fatal(err)
		}
		if view.Review.ReadFailure == nil || view.Review.ReadFailure.Code != ReadFailureReviewEvidence || view.Review.Status != "" {
			t.Fatalf("review read failure=%+v", view.Review)
		}
		assertAdvisoryEvidenceDidNotBlockMerge(t, view)
	})
}

func newEvidenceReadFixture(t *testing.T) (*serviceFixture, *View, string) {
	t.Helper()
	fixture := newServiceFixture(t)
	fixture.commitFile("base.txt", "base\n", "base")
	fixture.push("HEAD:refs/heads/main")
	fixture.git("checkout", "-b", "feature")
	sourceOID := fixture.commitFile("feature.txt", "feature\n", "feature")
	fixture.push("HEAD:refs/heads/feature")
	fixture.git("checkout", "main")
	fixture.commitFile("main.txt", "main\n", "main")
	fixture.push("HEAD:refs/heads/main")
	created, err := fixture.service.Create(fixture.ctx, CreateInput{
		Repository: fixture.repositoryID, Title: "Read advisory evidence",
		SourceBranch: "feature", TargetBranch: "main", ReviewChoice: "request",
	})
	if err != nil {
		t.Fatal(err)
	}
	return fixture, created, sourceOID
}

func assertAdvisoryEvidenceDidNotBlockMerge(t *testing.T, view *View) {
	t.Helper()
	if !view.Checks.Advisory || !view.MergeEligibility.Eligible || len(view.MergeEligibility.Blockers) != 0 {
		t.Fatalf("advisory read failure changed merge eligibility: checks=%+v eligibility=%+v", view.Checks, view.MergeEligibility)
	}
}

func TestChecksFromAttemptPreservesCleanupAndPendingEvidence(t *testing.T) {
	store, err := state.Open(context.Background(), filepath.Join(t.TempDir(), "state"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)
	service := Service{Store: store, Now: func() time.Time { return now }}
	exitCode := 0
	attempt := state.CheckAttempt{
		ID: "11111111111111111111111111111111", TaskID: "task-one", RepositoryID: "project",
		RevisionOID: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", WorktreeState: state.WorktreeClean,
		SubmittedWorktreeState: state.WorktreeDirty, ConfigurationVersion: 7, Status: state.AttemptPassed,
		StartedAt: now.Add(-time.Minute), FinishedAt: now, CreatedAt: now.Add(-time.Minute),
		Summary: "command exited successfully", Protection: state.ProtectionUnknown, ExecutionScope: state.ExecutionScopeInherited,
		CredentialID: "helper-one", LogError: "raw log unavailable",
		Results: []state.CheckResult{{Name: "test", Command: "go test ./...", Status: state.AttemptPassed, ExitCode: &exitCode, CleanupError: "cleanup not confirmed"}},
	}

	checks := service.checksFromAttempt(attempt, attempt.RevisionOID)
	if !checks.CleanupFailed || checks.TestedCommit || checks.WorktreeState != state.WorktreeDirty {
		t.Fatalf("cleanup mapping=%+v", checks)
	}
	if checks.TaskID != attempt.TaskID || checks.RegisteredAt == nil || !checks.RegisteredAt.Equal(attempt.CreatedAt) || checks.FinishedAt == nil || !checks.FinishedAt.Equal(attempt.FinishedAt) {
		t.Fatalf("identity/time mapping=%+v", checks)
	}
	if checks.LogError != attempt.LogError || checks.CredentialID != attempt.CredentialID || checks.ExecutionScope != state.ExecutionScopeInherited {
		t.Fatalf("metadata mapping=%+v", checks)
	}

	attempt.Status = state.AttemptPending
	attempt.FinishedAt = time.Time{}
	attempt.Results = nil
	attempt.SubmittedWorktreeState = ""
	attempt.LogError = ""
	pending := service.checksFromAttempt(attempt, attempt.RevisionOID)
	if pending.FinishedAt != nil || pending.LogStatus != "" || pending.TestedCommit || pending.RegisteredAt == nil {
		t.Fatalf("pending mapping=%+v", pending)
	}
}

// TestChecksCarryTheRecordedJobLinkWithoutWideningTheAPI covers the projection
// a reader needs to tell the server's own automatic run from a person's manual
// one.
//
// The job link is a recorded fact on the attempt. Deriving that distinction
// from the protection and the execution scope instead is a guess, because an
// admitted job may still carry no established protection. The link is carried
// for the browser only: this release does not extend the pull request API
// response, so it must not appear in the JSON.
func TestChecksCarryTheRecordedJobLinkWithoutWideningTheAPI(t *testing.T) {
	store, err := state.Open(context.Background(), filepath.Join(t.TempDir(), "state"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Date(2026, 9, 21, 9, 0, 0, 0, time.UTC)
	service := Service{Store: store, Now: func() time.Time { return now }}

	// One attempt, projected twice, differing only in the job link. Every
	// other recorded fact is identical, so a mapping that ignores the link
	// cannot distinguish these two and this test fails.
	base := state.CheckAttempt{
		ID: "11111111111111111111111111111111", TaskID: "task-one", RepositoryID: "project",
		RevisionOID: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", WorktreeState: state.WorktreeClean,
		ConfigurationVersion: 7, Status: state.AttemptPassed,
		StartedAt: now.Add(-time.Minute), FinishedAt: now, CreatedAt: now.Add(-time.Minute),
		Summary:    "command exited successfully",
		Protection: state.ProtectionUnknown, ExecutionScope: state.ExecutionScopeInherited,
		CredentialID: "credential-one",
	}

	manual := service.checksFromAttempt(base, base.RevisionOID)
	if manual.JobID != "" {
		t.Errorf("an unlinked attempt projected the job %q", manual.JobID)
	}

	linked := base
	linked.JobID = strings.Repeat("e", 32)
	automatic := service.checksFromAttempt(linked, linked.RevisionOID)
	if automatic.JobID != linked.JobID {
		t.Errorf("the projected job link = %q, want %q", automatic.JobID, linked.JobID)
	}

	// Only the link may differ. Anything else changing here would mean the
	// projection rewrote evidence rather than carrying one more recorded fact.
	// The comparison runs over the JSON because the timestamps are fresh
	// pointers on every call and their values are what matter; the link itself
	// is excluded from that encoding, which the assertions below rely on.
	sameFacts, err := json.Marshal(manual)
	if err != nil {
		t.Fatal(err)
	}
	linkedFacts, err := json.Marshal(automatic)
	if err != nil {
		t.Fatal(err)
	}
	if string(sameFacts) != string(linkedFacts) {
		t.Errorf("the job link changed other evidence:\nunlinked=%s\nlinked=%s", sameFacts, linkedFacts)
	}

	// The pull request API response is unchanged in this release.
	if strings.Contains(string(linkedFacts), linked.JobID) || strings.Contains(string(linkedFacts), "job_id") {
		t.Errorf("the job link reached the pull request API response: %s", linkedFacts)
	}
	var decoded map[string]any
	if err := json.Unmarshal(linkedFacts, &decoded); err != nil {
		t.Fatal(err)
	}
	if _, present := decoded["job_id"]; present {
		t.Error("the API response gained a job_id field")
	}
	// The evidence a reader already relied on is still published.
	for _, field := range []string{"status", "advisory", "attempt_id", "execution_scope"} {
		if _, present := decoded[field]; !present {
			t.Errorf("the API response lost the %q field", field)
		}
	}
}
