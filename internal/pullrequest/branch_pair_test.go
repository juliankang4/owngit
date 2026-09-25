package pullrequest

import (
	"errors"
	"testing"
	"time"

	"owngit/internal/state"
)

// QA-008: one open pull request per source and target branch pair, and a
// merge of a source the target already contains writes no commit.

func TestSecondOpenPullRequestForABranchPairIsRefused(t *testing.T) {
	fixture := newServiceFixture(t)
	fixture.commitFile("base.txt", "base\n", "base")
	fixture.push("HEAD:refs/heads/main")
	fixture.push("HEAD:refs/heads/release")
	fixture.git("checkout", "-b", "feature")
	sourceOID := fixture.commitFile("feature.txt", "feature\n", "feature")
	fixture.push("HEAD:refs/heads/feature")

	first, err := fixture.service.Create(fixture.ctx, CreateInput{Repository: fixture.repositoryID, Title: "First", SourceBranch: "feature", TargetBranch: "main"})
	noErr(t, err)
	_, err = fixture.service.Create(fixture.ctx, CreateInput{Repository: fixture.repositoryID, Title: "Second", SourceBranch: "refs/heads/feature", TargetBranch: "main"})
	var problem *Problem
	if !errors.As(err, &problem) || problem.Code != "pull_request_exists" {
		t.Fatalf("second create err=%v, want pull_request_exists", err)
	}
	if existing, ok := problem.Details.(ExistingPullRequest); !ok || existing.Number != first.Number {
		t.Fatalf("details=%#v, want the open pull request #%d", problem.Details, first.Number)
	}
	records, err := fixture.store.PullRequests(fixture.ctx, fixture.repositoryID)
	noErr(t, err)
	if len(records) != 1 {
		t.Fatalf("the refused create left %d pull requests", len(records))
	}

	// Another target is another pair.
	_, err = fixture.service.Create(fixture.ctx, CreateInput{Repository: fixture.repositoryID, Title: "Release", SourceBranch: "feature", TargetBranch: "release"})
	noErr(t, err)

	// Once the open one is merged, the pair is free again.
	targetOID := fixture.ref("refs/heads/main")
	_, err = fixture.service.Merge(fixture.ctx, fixture.repositoryID, first.Number, RevisionInput{SourceOID: sourceOID, TargetOID: targetOID})
	noErr(t, err)
	fixture.commitFile("feature.txt", "more\n", "more feature")
	fixture.push("HEAD:refs/heads/feature")
	_, err = fixture.service.Create(fixture.ctx, CreateInput{Repository: fixture.repositoryID, Title: "Follow-up", SourceBranch: "feature", TargetBranch: "main"})
	noErr(t, err)
}

func TestMergeOfAContainedSourceWritesNoCommit(t *testing.T) {
	fixture := newServiceFixture(t)
	fixture.commitFile("base.txt", "base\n", "base")
	fixture.push("HEAD:refs/heads/main")
	fixture.git("checkout", "-b", "feature")
	sourceOID := fixture.commitFile("feature.txt", "feature\n", "feature")
	fixture.push("HEAD:refs/heads/feature")
	created, err := fixture.service.Create(fixture.ctx, CreateInput{Repository: fixture.repositoryID, Title: "Already merged", SourceBranch: "feature", TargetBranch: "main"})
	noErr(t, err)

	// The feature reaches main some other way, and main moves on.
	fixture.git("checkout", "main")
	fixture.git("merge", "--no-ff", "-m", "merged outside OwnGit", "feature")
	targetOID := fixture.commitFile("main.txt", "main\n", "main moves on")
	fixture.push("HEAD:refs/heads/main")

	merged, err := fixture.service.Merge(fixture.ctx, fixture.repositoryID, created.Number, RevisionInput{SourceOID: sourceOID, TargetOID: targetOID})
	noErr(t, err)
	if merged.State != state.PullRequestMerged || merged.Merge == nil || merged.Merge.Mode != "up_to_date" || merged.Merge.OID != targetOID {
		t.Fatalf("merge result=%+v, want up_to_date at the unchanged target %s", merged.Merge, targetOID)
	}
	if got := fixture.ref("refs/heads/main"); got != targetOID {
		t.Fatalf("target moved to %s, want it unchanged at %s", got, targetOID)
	}
	if got := fixture.ref(MergeReceiptRef(created.Number)); got != targetOID {
		t.Fatalf("receipt=%s, want %s", got, targetOID)
	}

	// A retry returns the same result, and reads and recovery accept it.
	repeated, err := fixture.service.Merge(fixture.ctx, fixture.repositoryID, created.Number, RevisionInput{SourceOID: sourceOID, TargetOID: targetOID})
	noErr(t, err)
	if repeated.Merge == nil || repeated.Merge.OID != targetOID || fixture.ref("refs/heads/main") != targetOID {
		t.Fatalf("repeated merge=%+v", repeated.Merge)
	}
	shown, err := fixture.service.Show(fixture.ctx, fixture.repositoryID, created.Number)
	noErr(t, err)
	if shown.Merge == nil || shown.Merge.Mode != "up_to_date" {
		t.Fatalf("show=%+v", shown.Merge)
	}
	noErr(t, fixture.service.ReconcileAll(fixture.ctx))
}

func TestMergeOfIdenticalBranchesWritesNoCommit(t *testing.T) {
	fixture := newServiceFixture(t)
	oid := fixture.commitFile("base.txt", "base\n", "base")
	fixture.push("HEAD:refs/heads/main")
	fixture.push("HEAD:refs/heads/feature")
	created, err := fixture.service.Create(fixture.ctx, CreateInput{Repository: fixture.repositoryID, Title: "Nothing to merge", SourceBranch: "feature", TargetBranch: "main"})
	noErr(t, err)
	merged, err := fixture.service.Merge(fixture.ctx, fixture.repositoryID, created.Number, RevisionInput{SourceOID: oid, TargetOID: oid})
	noErr(t, err)
	if merged.Merge == nil || merged.Merge.Mode != "up_to_date" || fixture.ref("refs/heads/main") != oid {
		t.Fatalf("merge result=%+v", merged.Merge)
	}
}

// Stores from before the rule can hold two open pull requests for one pair.
// Both keep working: the first merges, the second is then already up to date.
func TestStoredDuplicatePullRequestsStillMerge(t *testing.T) {
	fixture := newServiceFixture(t)
	fixture.commitFile("base.txt", "base\n", "base")
	fixture.push("HEAD:refs/heads/main")
	fixture.git("checkout", "-b", "feature")
	fixture.commitFile("feature.txt", "one\n", "feature one")
	fixture.git("checkout", "main")
	fixture.commitFile("main.txt", "main\n", "main")
	fixture.push("HEAD:refs/heads/main")
	fixture.git("checkout", "feature")
	sourceOID := fixture.commitFile("feature.txt", "two\n", "feature two")
	fixture.push("HEAD:refs/heads/feature")
	targetOID := fixture.ref("refs/heads/main")

	first, err := fixture.service.Create(fixture.ctx, CreateInput{Repository: fixture.repositoryID, Title: "First", SourceBranch: "feature", TargetBranch: "main"})
	noErr(t, err)
	legacy, err := fixture.store.CreatePullRequest(fixture.ctx, fixture.repositoryID, "Duplicate", "feature", "main", sourceOID, targetOID, state.ReviewNotRequested, time.Now())
	noErr(t, err)
	// Startup preparation repairs the protected refs of recorded revisions.
	noErr(t, fixture.service.ReconcileAll(fixture.ctx))

	merged, err := fixture.service.Merge(fixture.ctx, fixture.repositoryID, first.Number, RevisionInput{SourceOID: sourceOID, TargetOID: targetOID})
	noErr(t, err)
	if merged.Merge == nil || merged.Merge.Mode != "merge_commit" {
		t.Fatalf("first merge=%+v", merged.Merge)
	}
	mergeOID := merged.Merge.OID
	second, err := fixture.service.Merge(fixture.ctx, fixture.repositoryID, legacy.Number, RevisionInput{SourceOID: sourceOID, TargetOID: mergeOID})
	noErr(t, err)
	if second.Merge == nil || second.Merge.Mode != "up_to_date" || second.Merge.OID != mergeOID || fixture.ref("refs/heads/main") != mergeOID {
		t.Fatalf("second merge=%+v, main=%s, want no new commit after %s", second.Merge, fixture.ref("refs/heads/main"), mergeOID)
	}
	views, err := fixture.service.List(fixture.ctx, fixture.repositoryID)
	noErr(t, err)
	if len(views) != 2 || views[0].State != state.PullRequestMerged || views[1].State != state.PullRequestMerged {
		t.Fatalf("list=%d views", len(views))
	}
}
