package server

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"owngit/internal/pullrequest"
	"owngit/internal/state"
)

// A pull request without evidence for its head shows an earlier result only
// when that result is for a source revision recorded for this pull request.
// Newer evidence for another branch is never presented as this change's result.
func TestPullRequestChecksFallBackOnlyToItsOwnRevisions(t *testing.T) {
	fixture := newAPIFixture(t, false)
	ctx := context.Background()
	task, err := fixture.store.CreateTask(ctx, "project", "Evidence scope", time.Now().UTC().Add(-time.Minute))
	noErr(t, err)
	recordBrowserAttempt(t, fixture.store, task.ID, fixture.targetOID, "44444444444444444444444444444444", state.WorktreeClean, "", true)
	created, err := fixture.app.PullRequests.Create(ctx, pullrequest.CreateInput{
		Repository: "project", Title: "Scoped evidence", SourceBranch: "feature", TargetBranch: "main",
		SourceOID: fixture.sourceOID, TargetOID: fixture.targetOID,
	})
	noErr(t, err)
	show := func() pullrequest.Checks {
		t.Helper()
		view, err := fixture.app.PullRequests.Show(ctx, "project", created.Number)
		noErr(t, err)
		return view.Checks
	}
	if checks := show(); checks.Status != "absent" || checks.Stale || checks.RevisionOID != "" {
		t.Fatalf("another branch's evidence was shown for this pull request: %+v", checks)
	}

	recordBrowserAttempt(t, fixture.store, task.ID, fixture.sourceOID, "55555555555555555555555555555555", state.WorktreeClean, "", true)
	noErr(t, os.WriteFile(filepath.Join(fixture.work, "later.txt"), []byte("later\n"), 0o600))
	apiRunGit(t, fixture.work, "add", ".")
	apiRunGit(t, fixture.work, "commit", "-m", "later")
	apiRunGit(t, fixture.work, "push", "origin", "HEAD:refs/heads/feature")
	// Newer evidence on the target branch must not replace this pull request's
	// own earlier result.
	recordBrowserAttempt(t, fixture.store, task.ID, fixture.targetOID, "66666666666666666666666666666666", state.WorktreeClean, "", true)
	if checks := show(); checks.Status != "stale" || !checks.Stale || checks.RevisionOID != fixture.sourceOID || checks.AttemptID != "55555555555555555555555555555555" {
		t.Fatalf("earlier revision evidence=%+v", checks)
	}
}
