package pullrequest

import (
	"context"
	"errors"
	"testing"
	"time"

	"owngit/internal/state"
)

// A closed pull request stays in the history, frees its branch pair, cannot
// be merged or reviewed, and reopens only while its pair is free.
func TestCloseAndReopenPullRequest(t *testing.T) {
	fixture := newServiceFixture(t)
	fixture.commitFile("base.txt", "base\n", "base")
	fixture.push("HEAD:refs/heads/main")
	targetOID := fixture.ref("refs/heads/main")
	fixture.git("checkout", "-b", "feature")
	sourceOID := fixture.commitFile("feature.txt", "feature\n", "feature")
	fixture.push("HEAD:refs/heads/feature")
	first, err := fixture.service.Create(fixture.ctx, CreateInput{Repository: fixture.repositoryID, Title: "First", SourceBranch: "feature", TargetBranch: "main"})
	noErr(t, err)

	closed, err := fixture.service.Close(fixture.ctx, fixture.repositoryID, first.Number)
	noErr(t, err)
	if closed.State != state.PullRequestClosed || closed.MergeEligibility.Eligible || blockerCode(closed) != "pull_request_not_open" {
		t.Fatalf("closed view=%+v", closed)
	}
	again, err := fixture.service.Close(fixture.ctx, fixture.repositoryID, first.Number)
	if err != nil || again.State != state.PullRequestClosed {
		t.Fatalf("repeated close=%+v err=%v", again, err)
	}
	revision := RevisionInput{SourceOID: sourceOID, TargetOID: targetOID}
	if _, err := fixture.service.Merge(fixture.ctx, fixture.repositoryID, first.Number, revision); problemCode(err) != "pull_request_not_open" {
		t.Fatalf("merge of a closed pull request err=%v", err)
	}
	if _, err := fixture.service.RequestReview(fixture.ctx, fixture.repositoryID, first.Number, revision); problemCode(err) != "pull_request_not_open" {
		t.Fatalf("review of a closed pull request err=%v", err)
	}
	if got := fixture.ref("refs/heads/main"); got != targetOID {
		t.Fatalf("a closed pull request changed the target: %s", got)
	}
	views, err := fixture.service.List(fixture.ctx, fixture.repositoryID)
	noErr(t, err)
	if len(views) != 1 || views[0].State != state.PullRequestClosed {
		t.Fatalf("the closed pull request left the history: %d", len(views))
	}

	// The pair is free, so a new pull request can be opened; the old one then
	// cannot be reopened beside it.
	second, err := fixture.service.Create(fixture.ctx, CreateInput{Repository: fixture.repositoryID, Title: "Second", SourceBranch: "feature", TargetBranch: "main"})
	noErr(t, err)
	_, err = fixture.service.Reopen(fixture.ctx, fixture.repositoryID, first.Number)
	var problem *Problem
	if !errors.As(err, &problem) || problem.Code != "pull_request_exists" || problem.Details != (ExistingPullRequest{Number: second.Number}) {
		t.Fatalf("reopen beside an open pull request err=%v", err)
	}
	_, err = fixture.service.Close(fixture.ctx, fixture.repositoryID, second.Number)
	noErr(t, err)
	reopened, err := fixture.service.Reopen(fixture.ctx, fixture.repositoryID, first.Number)
	if err != nil || reopened.State != state.PullRequestOpen || !reopened.MergeEligibility.Eligible {
		t.Fatalf("reopen=%+v err=%v", reopened, err)
	}
	if again, err := fixture.service.Reopen(fixture.ctx, fixture.repositoryID, first.Number); err != nil || again.State != state.PullRequestOpen {
		t.Fatalf("repeated reopen=%+v err=%v", again, err)
	}

	merged, err := fixture.service.Merge(fixture.ctx, fixture.repositoryID, first.Number, revision)
	noErr(t, err)
	if merged.State != state.PullRequestMerged {
		t.Fatalf("merge after reopen=%+v", merged)
	}
	for name, change := range map[string]func(context.Context, string, int64) (*View, error){"close": fixture.service.Close, "reopen": fixture.service.Reopen} {
		if _, err := change(fixture.ctx, fixture.repositoryID, first.Number); problemCode(err) != "pull_request_merged" {
			t.Fatalf("%s of a merged pull request err=%v", name, err)
		}
	}
	if _, err := fixture.service.Close(fixture.ctx, fixture.repositoryID, 99); problemCode(err) != "pull_request_not_found" {
		t.Fatalf("close of a missing pull request err=%v", err)
	}
	noErr(t, fixture.service.ReconcileAll(fixture.ctx))
}

// A merge that Git published but OwnGit had not yet recorded is completed,
// not closed, so the history never says closed about merged work.
func TestClosingAPublishedButUnrecordedMergeRecordsTheMerge(t *testing.T) {
	fixture := newServiceFixture(t)
	fixture.commitFile("file.txt", "base\n", "base")
	fixture.push("HEAD:refs/heads/main")
	targetOID := fixture.ref("refs/heads/main")
	fixture.git("checkout", "-b", "feature")
	sourceOID := fixture.commitFile("file.txt", "feature\n", "feature")
	fixture.push("HEAD:refs/heads/feature")
	created, err := fixture.service.Create(fixture.ctx, CreateInput{Repository: fixture.repositoryID, Title: "Published", SourceBranch: "feature", TargetBranch: "main"})
	noErr(t, err)
	fixture.service.CompleteMerge = func(context.Context, state.PullRequestMergeIntent, time.Time) error {
		return errors.New("injected state failure")
	}
	if _, err := fixture.service.Merge(fixture.ctx, fixture.repositoryID, created.Number, RevisionInput{SourceOID: sourceOID, TargetOID: targetOID}); problemCode(err) != "merge_reconciliation_pending" {
		t.Fatalf("merge err=%v", err)
	}
	fixture.service.CompleteMerge = nil
	if _, err := fixture.service.Close(fixture.ctx, fixture.repositoryID, created.Number); problemCode(err) != "pull_request_merged" {
		t.Fatalf("close after a published merge err=%v", err)
	}
	shown, err := fixture.service.Show(fixture.ctx, fixture.repositoryID, created.Number)
	noErr(t, err)
	if shown.State != state.PullRequestMerged || shown.Merge == nil || shown.Merge.OID != sourceOID {
		t.Fatalf("shown=%+v", shown)
	}
	noErr(t, fixture.service.ReconcileAll(fixture.ctx))
}
