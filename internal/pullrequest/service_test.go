package pullrequest

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"owngit/internal/gitexec"
	"owngit/internal/repository"
	"owngit/internal/state"
)

func TestReviewBoundMergeCreatesExactMergeCommitAndIsIdempotent(t *testing.T) {
	fixture := newServiceFixture(t)
	base := fixture.commitFile("shared.txt", "base\n", "base")
	fixture.push("HEAD:refs/heads/main")
	fixture.git("checkout", "-b", "feature")
	sourceOID := fixture.commitFile("feature.txt", "feature\n", "feature")
	fixture.push("HEAD:refs/heads/feature")
	fixture.git("checkout", "main")
	targetOID := fixture.commitFile("main.txt", "main\n", "main")
	fixture.push("HEAD:refs/heads/main")
	if base == sourceOID || base == targetOID || sourceOID == targetOID {
		t.Fatal("fixture did not create divergent commits")
	}

	created, err := fixture.service.Create(fixture.ctx, CreateInput{
		Repository: fixture.repositoryID, Title: "Merge the feature", SourceBranch: "feature", TargetBranch: "main", ReviewChoice: "request",
	})
	if err != nil {
		t.Fatal(err)
	}
	if created.Review.Status != state.ReviewPending || !created.MergeEligibility.Eligible || created.Checks.Status != "absent" || created.Checks.Configured || created.Checks.Passed || created.Checks.Blocking || !created.Checks.Advisory {
		t.Fatalf("unexpected created pull request: %+v", created)
	}
	if got := len(created.MergeEligibility.Blockers); got != 0 {
		t.Fatalf("pending review produced %d blockers, want none", got)
	}
	approved, err := fixture.service.SubmitReview(fixture.ctx, fixture.repositoryID, created.Number, ReviewSubmitInput{
		SourceOID: sourceOID, TargetOID: targetOID, Decision: state.ReviewApproved, ReviewerLabel: "existing-tool: synthetic-reviewer",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !approved.MergeEligibility.Eligible || approved.Review.Provenance != state.ReviewProvenanceExternalTool || approved.Review.Independent || approved.Review.ExecutedChecks {
		t.Fatalf("submitted review was represented incorrectly: %+v", approved)
	}

	merged, err := fixture.service.Merge(fixture.ctx, fixture.repositoryID, created.Number, RevisionInput{SourceOID: sourceOID, TargetOID: targetOID})
	if err != nil {
		t.Fatal(err)
	}
	if merged.State != state.PullRequestMerged || merged.Merge == nil || merged.Merge.Mode != "merge_commit" {
		t.Fatalf("unexpected merge result: %+v", merged)
	}
	if got := fixture.ref("refs/heads/main"); got != merged.Merge.OID {
		t.Fatalf("target ref=%s, want merge result %s", got, merged.Merge.OID)
	}
	if got := fixture.ref("refs/heads/feature"); got != sourceOID {
		t.Fatalf("source branch changed: %s", got)
	}
	if got := fixture.ref(MergeReceiptRef(created.Number)); got != merged.Merge.OID {
		t.Fatalf("receipt=%s, want %s", got, merged.Merge.OID)
	}
	commit := fixture.gitOutput("--git-dir", fixture.remote, "cat-file", "-p", merged.Merge.OID)
	var tree string
	var parents []string
	for _, line := range strings.Split(commit, "\n") {
		if strings.HasPrefix(line, "tree ") {
			tree = strings.TrimPrefix(line, "tree ")
		}
		if strings.HasPrefix(line, "parent ") {
			parents = append(parents, strings.TrimPrefix(line, "parent "))
		}
	}
	if len(parents) != 2 || parents[0] != targetOID || parents[1] != sourceOID {
		t.Fatalf("merge parents=%v, want [%s %s]", parents, targetOID, sourceOID)
	}
	expectedTree := strings.SplitN(fixture.gitOutput("--git-dir", fixture.remote, "merge-tree", "--write-tree", targetOID, sourceOID), "\n", 2)[0]
	if tree != expectedTree {
		t.Fatalf("merge tree=%s, want %s", tree, expectedTree)
	}
	fixture.git("--git-dir", fixture.remote, "merge-base", "--is-ancestor", targetOID, merged.Merge.OID)

	repeated, err := fixture.service.Merge(fixture.ctx, fixture.repositoryID, created.Number, RevisionInput{SourceOID: sourceOID, TargetOID: targetOID})
	if err != nil {
		t.Fatal(err)
	}
	if repeated.Merge == nil || repeated.Merge.OID != merged.Merge.OID || fixture.ref("refs/heads/main") != merged.Merge.OID {
		t.Fatalf("repeated merge changed its result: first=%+v repeated=%+v", merged.Merge, repeated.Merge)
	}
}

func TestObserveCurrentRevisionsBindsSourcePushWithoutViewRead(t *testing.T) {
	fixture := newServiceFixture(t)
	fixture.commitFile("base.txt", "base\n", "base")
	fixture.push("HEAD:refs/heads/main")
	fixture.git("checkout", "-b", "feature")
	fixture.commitFile("feature.txt", "first\n", "feature one")
	fixture.push("HEAD:refs/heads/feature")
	created, err := fixture.service.Create(fixture.ctx, CreateInput{
		Repository: fixture.repositoryID, Title: "Observe source pushes", SourceBranch: "feature", TargetBranch: "main", ReviewChoice: "skip",
	})
	if err != nil {
		t.Fatal(err)
	}
	newSource := fixture.commitFile("feature.txt", "second\n", "feature two")
	fixture.push("HEAD:refs/heads/feature")

	observed, more, err := fixture.service.ObserveCurrentRevisions(fixture.ctx, fixture.repositoryID, 64)
	if err != nil || more || observed != 1 {
		t.Fatalf("observed=%d more=%v err=%v", observed, more, err)
	}
	revisions, err := fixture.store.LatestPullRequestRevisions(fixture.ctx, fixture.repositoryID, 64)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, revision := range revisions {
		if revision.PullRequestNumber == created.Number && revision.SourceOID == newSource {
			found = true
		}
	}
	if !found {
		t.Fatalf("new source revision %s was not durably observed: %+v", newSource, revisions)
	}
	if observed, _, err := fixture.service.ObserveCurrentRevisions(fixture.ctx, fixture.repositoryID, 64); err != nil || observed != 0 {
		t.Fatalf("repeated observation count=%d err=%v", observed, err)
	}
}

func TestFreshNonFastForwardMergeCalculatesTreeTwice(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the command-counting wrapper is a Unix test fixture")
	}
	fixture := newServiceFixture(t)
	fixture.commitFile("base.txt", "base\n", "base")
	fixture.push("HEAD:refs/heads/main")
	fixture.git("checkout", "-b", "feature")
	sourceOID := fixture.commitFile("feature.txt", "feature\n", "feature")
	fixture.push("HEAD:refs/heads/feature")
	fixture.git("checkout", "main")
	targetOID := fixture.commitFile("target.txt", "target\n", "target")
	fixture.push("HEAD:refs/heads/main")
	created, err := fixture.service.Create(fixture.ctx, CreateInput{
		Repository: fixture.repositoryID, Title: "Count merge trees", SourceBranch: "feature", TargetBranch: "main", ReviewChoice: "skip",
	})
	if err != nil {
		t.Fatal(err)
	}

	gitPath, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	tracePath := filepath.Join(t.TempDir(), "git-commands")
	wrapperPath := filepath.Join(t.TempDir(), "git-wrapper")
	wrapper := "#!/bin/sh\nprintf '%s\\0' \"$@\" >> " + shellQuote(tracePath) + "\nprintf '\\n' >> " + shellQuote(tracePath) + "\nexec " + shellQuote(gitPath) + " \"$@\"\n"
	if err := os.WriteFile(wrapperPath, []byte(wrapper), 0o700); err != nil {
		t.Fatal(err)
	}
	traced, err := gitexec.New(wrapperPath, filepath.Join(t.TempDir(), "runtime"))
	if err != nil {
		t.Fatal(err)
	}
	fixture.manager.Git = traced
	if err := os.WriteFile(tracePath, nil, 0o600); err != nil {
		t.Fatal(err)
	}

	merged, err := fixture.service.Merge(fixture.ctx, fixture.repositoryID, created.Number, RevisionInput{SourceOID: sourceOID, TargetOID: targetOID})
	if err != nil {
		t.Fatal(err)
	}
	if merged.Merge == nil || merged.Merge.Mode != "merge_commit" {
		t.Fatalf("unexpected merge result: %+v", merged)
	}
	trace, err := os.ReadFile(tracePath)
	if err != nil {
		t.Fatal(err)
	}
	if count := bytes.Count(trace, []byte("\x00merge-tree\x00")); count != 2 {
		t.Fatalf("fresh non-fast-forward merge used %d merge-tree processes, want 2", count)
	}
}

func TestPassivePullRequestReadsDoNotPersistRevisionState(t *testing.T) {
	fixture, number, newSource, targetOID := newMovedHeadFixture(t)
	refsBefore := fixture.gitOutput("--git-dir", fixture.remote, "for-each-ref", "--format=%(refname) %(objectname)", "refs/owngit/pull-requests")
	rowsBefore, err := fixture.store.PullRequestRevisions(fixture.ctx, fixture.repositoryID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.service.Show(fixture.ctx, fixture.repositoryID, number); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.service.List(fixture.ctx, fixture.repositoryID); err != nil {
		t.Fatal(err)
	}
	refsAfter := fixture.gitOutput("--git-dir", fixture.remote, "for-each-ref", "--format=%(refname) %(objectname)", "refs/owngit/pull-requests")
	rowsAfter, err := fixture.store.PullRequestRevisions(fixture.ctx, fixture.repositoryID)
	if err != nil {
		t.Fatal(err)
	}
	if refsAfter != refsBefore {
		t.Fatalf("passive reads changed protected refs:\nbefore:\n%s\nafter:\n%s", refsBefore, refsAfter)
	}
	if len(rowsAfter) != len(rowsBefore) {
		t.Fatalf("passive reads added revision rows: before=%d after=%d", len(rowsBefore), len(rowsAfter))
	}
	// A decision still retains the newly observed pair.
	if _, err := fixture.service.SkipReview(fixture.ctx, fixture.repositoryID, number, RevisionInput{SourceOID: newSource, TargetOID: targetOID}); err != nil {
		t.Fatal(err)
	}
	newSourceRef, newTargetRef := RevisionRefNames(number, newSource, targetOID)
	if fixture.ref(newSourceRef) != newSource || fixture.ref(newTargetRef) != targetOID {
		t.Fatal("a review decision did not retain the current pair")
	}
	rowsFinal, err := fixture.store.PullRequestRevisions(fixture.ctx, fixture.repositoryID)
	if err != nil {
		t.Fatal(err)
	}
	if len(rowsFinal) != len(rowsBefore)+1 {
		t.Fatalf("review decision revision rows=%d, want %d", len(rowsFinal), len(rowsBefore)+1)
	}
}

func TestPassivePullRequestReadsInvokeNoUpdateRef(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the command-counting wrapper is a Unix test fixture")
	}
	fixture, number, _, _ := newMovedHeadFixture(t)
	gitPath, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	tracePath := filepath.Join(t.TempDir(), "git-commands")
	wrapperPath := filepath.Join(t.TempDir(), "git-wrapper")
	wrapper := "#!/bin/sh\nprintf '%s\\0' \"$@\" >> " + shellQuote(tracePath) + "\nprintf '\\n' >> " + shellQuote(tracePath) + "\nexec " + shellQuote(gitPath) + " \"$@\"\n"
	if err := os.WriteFile(wrapperPath, []byte(wrapper), 0o700); err != nil {
		t.Fatal(err)
	}
	traced, err := gitexec.New(wrapperPath, filepath.Join(t.TempDir(), "runtime"))
	if err != nil {
		t.Fatal(err)
	}
	fixture.manager.Git = traced
	if err := os.WriteFile(tracePath, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.service.Show(fixture.ctx, fixture.repositoryID, number); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.service.List(fixture.ctx, fixture.repositoryID); err != nil {
		t.Fatal(err)
	}
	trace, err := os.ReadFile(tracePath)
	if err != nil {
		t.Fatal(err)
	}
	if count := bytes.Count(trace, []byte("\x00update-ref\x00")); count != 0 {
		t.Fatalf("passive reads invoked update-ref %d times, want 0", count)
	}
}

// TestPassivePullRequestReadsHoldTheSharedLock blocks a read inside Git and
// inspects the repository lock while the read is in flight. A shared holder
// lets another reader in and keeps writers out; an exclusive holder would
// reject TryRLock.
func TestPassivePullRequestReadsHoldTheSharedLock(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the barrier wrapper is a Unix test fixture")
	}
	fixture, number, _, _ := newMovedHeadFixture(t)
	gitPath, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	entered := filepath.Join(dir, "entered")
	release := filepath.Join(dir, "release")
	armed := filepath.Join(dir, "armed")
	for _, fifo := range []string{entered, release} {
		if output, err := exec.Command("mkfifo", fifo).CombinedOutput(); err != nil {
			t.Fatalf("mkfifo %s: %v\n%s", fifo, err, output)
		}
	}
	// The wrapper blocks on the first Git call, so the test observes the lock
	// while the read is inside Git rather than after it returned.
	wrapperPath := filepath.Join(dir, "git-wrapper")
	wrapper := "#!/bin/sh\n" +
		"if [ -f " + shellQuote(armed) + " ]; then\n" +
		"  rm -f " + shellQuote(armed) + "\n" +
		"  printf 'entered\\n' > " + shellQuote(entered) + "\n" +
		"  read line < " + shellQuote(release) + "\n" +
		"fi\n" +
		"exec " + shellQuote(gitPath) + " \"$@\"\n"
	if err := os.WriteFile(wrapperPath, []byte(wrapper), 0o700); err != nil {
		t.Fatal(err)
	}
	barrier, err := gitexec.New(wrapperPath, filepath.Join(dir, "runtime"))
	if err != nil {
		t.Fatal(err)
	}
	fixture.manager.Git = barrier
	lock := fixture.manager.Locks.For(fixture.repositoryID)

	reads := []struct {
		name string
		call func(context.Context) error
	}{
		{"Show", func(ctx context.Context) error {
			_, err := fixture.service.Show(ctx, fixture.repositoryID, number)
			return err
		}},
		{"List", func(ctx context.Context) error { _, err := fixture.service.List(ctx, fixture.repositoryID); return err }},
	}
	for _, read := range reads {
		func() {
			if err := os.WriteFile(armed, nil, 0o600); err != nil {
				t.Fatal(err)
			}
			// Cancel on assertion failure so the wrapper blocked on the
			// release FIFO is killed instead of waiting for the runner
			// timeout.
			ctx, cancel := context.WithCancel(fixture.ctx)
			defer cancel()
			done := make(chan error, 1)
			go func() { done <- read.call(ctx) }()
			waitForFIFO(t, entered)
			if !lock.TryRLock() {
				t.Fatalf("%s held the exclusive lock while reading Git", read.name)
			}
			lock.RUnlock()
			if lock.TryLock() {
				lock.Unlock()
				t.Fatalf("%s held no lock while reading Git", read.name)
			}
			if err := os.WriteFile(release, []byte("go\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := <-done; err != nil {
				t.Fatalf("%s: %v", read.name, err)
			}
		}()
	}
}

// waitForFIFO blocks until the wrapper opens the other end of the barrier.
func waitForFIFO(t *testing.T, path string) {
	t.Helper()
	done := make(chan error, 1)
	go func() {
		_, err := os.ReadFile(path)
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatalf("timed out waiting for %s", path)
	}
}

func newMovedHeadFixture(t *testing.T) (*serviceFixture, int64, string, string) {
	t.Helper()
	fixture := newServiceFixture(t)
	fixture.commitFile("file.txt", "base\n", "base")
	fixture.push("HEAD:refs/heads/main")
	fixture.git("checkout", "-b", "feature")
	fixture.commitFile("file.txt", "feature one\n", "feature one")
	fixture.push("HEAD:refs/heads/feature")
	targetOID := fixture.ref("refs/heads/main")
	created, err := fixture.service.Create(fixture.ctx, CreateInput{
		Repository: fixture.repositoryID, Title: "Passive reads", SourceBranch: "feature", TargetBranch: "main", ReviewChoice: "skip",
	})
	if err != nil {
		t.Fatal(err)
	}
	newSource := fixture.commitFile("second.txt", "second\n", "feature two")
	fixture.push("HEAD:refs/heads/feature")
	return fixture, created.Number, newSource, targetOID
}

func TestHeadMovementInvalidatesSkipAndAdvisoryReviewDoesNotBlock(t *testing.T) {
	fixture := newServiceFixture(t)
	fixture.commitFile("file.txt", "base\n", "base")
	fixture.push("HEAD:refs/heads/main")
	fixture.git("checkout", "-b", "feature")
	oldSource := fixture.commitFile("file.txt", "feature one\n", "feature one")
	fixture.push("HEAD:refs/heads/feature")
	targetOID := fixture.ref("refs/heads/main")

	created, err := fixture.service.Create(fixture.ctx, CreateInput{
		Repository: fixture.repositoryID, Title: "Fast forward feature", SourceBranch: "feature", TargetBranch: "main", ReviewChoice: "skip",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !created.MergeEligibility.Eligible || created.Review.Status != state.ReviewSkipped {
		t.Fatalf("initial skip was not current: %+v", created)
	}
	newSource := fixture.commitFile("second.txt", "second\n", "feature two")
	fixture.push("HEAD:refs/heads/feature")
	moved, err := fixture.service.Show(fixture.ctx, fixture.repositoryID, created.Number)
	if err != nil {
		t.Fatal(err)
	}
	if moved.Source.OID != newSource || moved.Review.Status != "decision_required" || len(moved.MergeEligibility.Blockers) != 0 {
		t.Fatalf("head movement did not invalidate skip: %+v", moved)
	}
	oldSourceRef, oldTargetRef := RevisionRefNames(created.Number, oldSource, targetOID)
	if fixture.ref(oldSourceRef) != oldSource || fixture.ref(oldTargetRef) != targetOID {
		t.Fatal("the original pull request revision refs were not retained")
	}
	if _, err := fixture.service.SkipReview(fixture.ctx, fixture.repositoryID, created.Number, RevisionInput{SourceOID: oldSource, TargetOID: targetOID}); problemCode(err) != "stale_revision" {
		t.Fatalf("stale skip error=%v code=%q", err, problemCode(err))
	}
	changed, err := fixture.service.SubmitReview(fixture.ctx, fixture.repositoryID, created.Number, ReviewSubmitInput{
		SourceOID: newSource, TargetOID: targetOID, Decision: state.ReviewChangesRequested, ReviewerLabel: "existing-tool: synthetic-reviewer",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !changed.MergeEligibility.Eligible || len(changed.MergeEligibility.Blockers) != 0 {
		t.Fatalf("changes-requested review blocked merge: %+v", changed)
	}
	merged, err := fixture.service.Merge(fixture.ctx, fixture.repositoryID, created.Number, RevisionInput{SourceOID: newSource, TargetOID: targetOID})
	if err != nil {
		t.Fatalf("advisory changes-requested review blocked merge: %v", err)
	}
	if merged.Merge == nil || merged.Merge.Mode != "fast_forward" || merged.Merge.OID != newSource || fixture.ref("refs/heads/main") != newSource {
		t.Fatalf("fast-forward merge result=%+v", merged)
	}
	fixture.git("push", "origin", ":refs/heads/feature")
	newSourceRef, newTargetRef := RevisionRefNames(created.Number, newSource, targetOID)
	if fixture.ref(oldSourceRef) != oldSource || fixture.ref(oldTargetRef) != targetOID || fixture.ref(newSourceRef) != newSource || fixture.ref(newTargetRef) != targetOID {
		t.Fatal("protected pull request revision refs changed after source branch deletion")
	}
	if shown, err := fixture.service.Show(fixture.ctx, fixture.repositoryID, created.Number); err != nil || shown.Merge == nil || shown.Merge.OID != newSource {
		t.Fatalf("merged pull request became unreadable after source deletion: view=%+v err=%v", shown, err)
	}
	fixture.git("--git-dir", fixture.remote, "update-ref", "-d", oldSourceRef)
	if fixture.refExists(oldSourceRef) {
		t.Fatal("synthetic protected-ref interruption did not remove the fixture ref")
	}
	if err := fixture.service.ReconcileAll(fixture.ctx); err != nil {
		t.Fatal(err)
	}
	if fixture.ref(oldSourceRef) != oldSource {
		t.Fatal("startup reconciliation did not repair a recorded revision ref")
	}
}

func TestMergeConflictLeavesTargetAndReceiptUnchanged(t *testing.T) {
	fixture := newServiceFixture(t)
	fixture.commitFile("conflict.txt", "base\n", "base")
	fixture.push("HEAD:refs/heads/main")
	fixture.git("checkout", "-b", "feature")
	sourceOID := fixture.commitFile("conflict.txt", "feature\n", "feature")
	fixture.push("HEAD:refs/heads/feature")
	fixture.git("checkout", "main")
	targetOID := fixture.commitFile("conflict.txt", "main\n", "main")
	fixture.push("HEAD:refs/heads/main")
	created, err := fixture.service.Create(fixture.ctx, CreateInput{
		Repository: fixture.repositoryID, Title: "Conflicting feature", SourceBranch: "feature", TargetBranch: "main", ReviewChoice: "skip",
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = fixture.service.Merge(fixture.ctx, fixture.repositoryID, created.Number, RevisionInput{SourceOID: sourceOID, TargetOID: targetOID})
	if problemCode(err) != "merge_conflict" {
		t.Fatalf("merge conflict error=%v code=%q", err, problemCode(err))
	}
	if got := fixture.ref("refs/heads/main"); got != targetOID {
		t.Fatalf("conflicted merge changed target to %s", got)
	}
	if fixture.refExists(MergeReceiptRef(created.Number)) {
		t.Fatal("conflicted merge created a receipt")
	}
}

func TestMergeRejectsUnrelatedHistoriesWithoutChangingTarget(t *testing.T) {
	fixture := newServiceFixture(t)
	fixture.commitFile("main.txt", "main\n", "main root")
	fixture.push("HEAD:refs/heads/main")
	targetOID := fixture.ref("refs/heads/main")
	fixture.git("checkout", "--orphan", "isolated")
	fixture.git("rm", "-rf", ".")
	sourceOID := fixture.commitFile("isolated.txt", "isolated\n", "isolated root")
	fixture.push("HEAD:refs/heads/isolated")
	created, err := fixture.service.Create(fixture.ctx, CreateInput{
		Repository: fixture.repositoryID, Title: "Unrelated history", SourceBranch: "isolated", TargetBranch: "main", ReviewChoice: "skip",
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = fixture.service.Merge(fixture.ctx, fixture.repositoryID, created.Number, RevisionInput{SourceOID: sourceOID, TargetOID: targetOID})
	if problemCode(err) != "merge_conflict" {
		t.Fatalf("unrelated merge error=%v code=%q", err, problemCode(err))
	}
	if fixture.ref("refs/heads/main") != targetOID || fixture.refExists(MergeReceiptRef(created.Number)) {
		t.Fatal("unrelated merge changed the target or created a receipt")
	}
}

func TestGitSuccessStateFailureReconcilesWithoutDuplicateCommit(t *testing.T) {
	fixture := newServiceFixture(t)
	fixture.commitFile("file.txt", "base\n", "base")
	fixture.push("HEAD:refs/heads/main")
	fixture.git("checkout", "-b", "feature")
	sourceOID := fixture.commitFile("file.txt", "feature\n", "feature")
	fixture.push("HEAD:refs/heads/feature")
	targetOID := fixture.ref("refs/heads/main")
	created, err := fixture.service.Create(fixture.ctx, CreateInput{
		Repository: fixture.repositoryID, Title: "Recover merge state", SourceBranch: "feature", TargetBranch: "main", ReviewChoice: "skip",
	})
	if err != nil {
		t.Fatal(err)
	}
	fixture.service.CompleteMerge = func(context.Context, state.PullRequestMergeIntent, time.Time) error {
		return errors.New("injected state failure")
	}
	_, err = fixture.service.Merge(fixture.ctx, fixture.repositoryID, created.Number, RevisionInput{SourceOID: sourceOID, TargetOID: targetOID})
	if problemCode(err) != "merge_reconciliation_pending" {
		t.Fatalf("state failure error=%v code=%q", err, problemCode(err))
	}
	if fixture.ref("refs/heads/main") != sourceOID || fixture.ref(MergeReceiptRef(created.Number)) != sourceOID {
		t.Fatal("Git transaction did not publish before the injected state failure")
	}
	fixture.service.CompleteMerge = nil
	if err := fixture.service.ReconcileAll(fixture.ctx); err != nil {
		t.Fatal(err)
	}
	shown, err := fixture.service.Show(fixture.ctx, fixture.repositoryID, created.Number)
	if err != nil {
		t.Fatal(err)
	}
	if shown.State != state.PullRequestMerged || shown.Merge == nil || shown.Merge.OID != sourceOID || shown.Merge.Mode != "fast_forward" {
		t.Fatalf("reconciled result=%+v", shown)
	}
	if _, err := fixture.service.Merge(fixture.ctx, fixture.repositoryID, created.Number, RevisionInput{SourceOID: sourceOID, TargetOID: targetOID}); err != nil {
		t.Fatal(err)
	}
	if fixture.ref("refs/heads/main") != sourceOID {
		t.Fatal("idempotent merge created a second result")
	}
}

func TestMergeCapabilityRequiresGit238OrNewer(t *testing.T) {
	tests := map[string]bool{
		"git version 2.37.6":                 false,
		"git version 2.38.0":                 true,
		"git version 2.54.0 (Apple Git-157)": true,
		"git version 2.55.1.windows.1":       true,
		"git version malformed":              false,
		"":                                   false,
	}
	for version, want := range tests {
		if got := supportsMergeVersion(version); got != want {
			t.Errorf("supportsMergeVersion(%q)=%v, want %v", version, got, want)
		}
	}
}

func TestOldGitReconciliationPreservesPendingNonFastForwardMerge(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("synthetic Git wrapper uses a POSIX shell")
	}
	for _, intentStatus := range []string{state.MergeIntentPlanned, state.MergeIntentReady} {
		t.Run(intentStatus, func(t *testing.T) {
			fixture := newServiceFixture(t)
			fixture.commitFile("base.txt", "base\n", "base")
			fixture.push("HEAD:refs/heads/main")
			fixture.git("checkout", "-b", "feature")
			sourceOID := fixture.commitFile("feature.txt", "feature\n", "feature")
			fixture.push("HEAD:refs/heads/feature")
			fixture.git("checkout", "main")
			targetOID := fixture.commitFile("target.txt", "target\n", "target")
			fixture.push("HEAD:refs/heads/main")
			created, err := fixture.service.Create(fixture.ctx, CreateInput{
				Repository: fixture.repositoryID, Title: "Pending on old Git", SourceBranch: "feature", TargetBranch: "main", ReviewChoice: "skip",
			})
			if err != nil {
				t.Fatal(err)
			}
			record, ok, err := fixture.store.PullRequest(fixture.ctx, fixture.repositoryID, created.Number)
			if err != nil || !ok {
				t.Fatalf("read pull request: ok=%v err=%v", ok, err)
			}
			intent, err := fixture.store.BeginPullRequestMerge(fixture.ctx, state.PullRequestMergeIntent{
				RepositoryID: fixture.repositoryID, PullRequestNumber: created.Number, SourceOID: sourceOID, TargetOID: targetOID,
				ReceiptRef: MergeReceiptRef(created.Number), CreatedAt: time.Now(),
			})
			if err != nil {
				t.Fatal(err)
			}
			intent, err = fixture.service.planMerge(fixture.ctx, fixture.remote, intent)
			if err != nil {
				t.Fatal(err)
			}
			if intent.Mode != "merge_commit" {
				t.Fatalf("intent mode=%q, want merge_commit", intent.Mode)
			}
			if err := fixture.service.ensurePlannedMergeTree(fixture.ctx, fixture.remote, record, intent, true); err != nil {
				t.Fatal(err)
			}
			intent, err = fixture.store.UpdatePullRequestMergeIntent(fixture.ctx, intent)
			if err != nil {
				t.Fatal(err)
			}
			if intentStatus == state.MergeIntentReady {
				intent.ResultOID, err = fixture.service.ensureMergeResult(fixture.ctx, fixture.remote, record, intent, true)
				if err != nil {
					t.Fatal(err)
				}
				intent.Status = state.MergeIntentReady
				intent.UpdatedAt = time.Now()
				intent, err = fixture.store.UpdatePullRequestMergeIntent(fixture.ctx, intent)
				if err != nil {
					t.Fatal(err)
				}
			}

			oldRunner, mergeTreeMarker := newOldGitRunner(t, filepath.Join(fixture.store.Dir(), "runtime-old-git"))
			fixture.manager.Git = oldRunner
			fixture.service.Repositories = fixture.manager
			if err := fixture.service.ReconcileAll(fixture.ctx); err != nil {
				t.Fatalf("reconcile pending merge on old Git: %v", err)
			}
			if _, err := os.Stat(mergeTreeMarker); !os.IsNotExist(err) {
				t.Fatalf("general reconciliation invoked merge-tree: %v", err)
			}
			shown, err := fixture.service.Show(fixture.ctx, fixture.repositoryID, created.Number)
			if err != nil || shown.State != state.PullRequestOpen {
				t.Fatalf("show pending pull request on old Git: view=%+v err=%v", shown, err)
			}
			ordinary, err := oldRunner.Run(fixture.ctx, "", nil, "--git-dir", fixture.remote, "rev-parse", "--verify", "refs/heads/main")
			if err != nil || strings.TrimSpace(string(ordinary.Stdout)) != targetOID {
				t.Fatalf("ordinary repository read on old Git: oid=%q err=%v", ordinary.Stdout, err)
			}
			_, err = fixture.service.Merge(fixture.ctx, fixture.repositoryID, created.Number, RevisionInput{SourceOID: sourceOID, TargetOID: targetOID})
			if problemCode(err) != "unsupported_git" {
				t.Fatalf("merge error=%v code=%q, want unsupported_git", err, problemCode(err))
			}
			if fixture.ref("refs/heads/main") != targetOID || fixture.refExists(MergeReceiptRef(created.Number)) {
				t.Fatal("unsupported merge changed the target or created a receipt")
			}
			if _, err := os.Stat(mergeTreeMarker); !os.IsNotExist(err) {
				t.Fatalf("unsupported merge invoked merge-tree: %v", err)
			}
			stored, ok, err := fixture.store.PullRequestMergeIntent(fixture.ctx, fixture.repositoryID, created.Number, sourceOID, targetOID)
			if err != nil || !ok || stored.Status != intentStatus || stored.TreeOID != intent.TreeOID || stored.ResultOID != intent.ResultOID {
				t.Fatalf("old Git changed pending intent: stored=%+v ok=%v err=%v", stored, ok, err)
			}
		})
	}
}

func TestReconcileRejectsMismatchedDurableMergeObjects(t *testing.T) {
	for _, intentStatus := range []string{state.MergeIntentPlanned, state.MergeIntentReady} {
		t.Run(intentStatus, func(t *testing.T) {
			fixture := newServiceFixture(t)
			fixture.commitFile("base.txt", "base\n", "base")
			fixture.push("HEAD:refs/heads/main")
			fixture.git("checkout", "-b", "feature")
			sourceOID := fixture.commitFile("feature.txt", "feature\n", "feature")
			fixture.push("HEAD:refs/heads/feature")
			fixture.git("checkout", "main")
			targetOID := fixture.commitFile("target.txt", "target\n", "target")
			fixture.push("HEAD:refs/heads/main")
			created, err := fixture.service.Create(fixture.ctx, CreateInput{
				Repository: fixture.repositoryID, Title: "Integrity", SourceBranch: "feature", TargetBranch: "main", ReviewChoice: "skip",
			})
			if err != nil {
				t.Fatal(err)
			}
			intent, err := fixture.store.BeginPullRequestMerge(fixture.ctx, state.PullRequestMergeIntent{
				RepositoryID: fixture.repositoryID, PullRequestNumber: created.Number, SourceOID: sourceOID, TargetOID: targetOID,
				ReceiptRef: MergeReceiptRef(created.Number), CreatedAt: time.Now(),
			})
			if err != nil {
				t.Fatal(err)
			}
			calculatedTree, err := fixture.service.calculateMergeTree(fixture.ctx, fixture.remote, targetOID, sourceOID)
			if err != nil {
				t.Fatal(err)
			}
			intent.Mode = "merge_commit"
			intent.TreeOID = calculatedTree
			intent.Status = intentStatus
			intent.UpdatedAt = intent.CreatedAt
			treeRef := MergeTreeRef(created.Number, sourceOID, targetOID)
			protectedTree := calculatedTree
			if intentStatus == state.MergeIntentPlanned {
				protectedTree = fixture.gitOutput("rev-parse", sourceOID+"^{tree}")
				if protectedTree == calculatedTree {
					t.Fatal("integrity fixture unexpectedly produced the real merge tree")
				}
			} else {
				record, ok, err := fixture.store.PullRequest(fixture.ctx, fixture.repositoryID, created.Number)
				if err != nil || !ok {
					t.Fatalf("read pull request for integrity fixture: ok=%v err=%v", ok, err)
				}
				intent.ResultOID, err = fixture.service.createMergeCommit(fixture.ctx, fixture.remote, record, intent)
				if err != nil {
					t.Fatal(err)
				}
			}
			fixture.git("--git-dir", fixture.remote, "update-ref", treeRef, protectedTree)
			resultRef := MergeResultRef(created.Number, sourceOID, targetOID)
			if intentStatus == state.MergeIntentReady {
				fixture.git("--git-dir", fixture.remote, "update-ref", resultRef, sourceOID)
			}
			stored, err := fixture.store.UpdatePullRequestMergeIntent(fixture.ctx, intent)
			if err != nil {
				t.Fatal(err)
			}
			if err := fixture.service.ReconcileAll(fixture.ctx); problemCode(err) != "repository_integrity_error" {
				t.Fatalf("reconcile error=%v code=%q", err, problemCode(err))
			}
			after, ok, err := fixture.store.PullRequestMergeIntent(fixture.ctx, fixture.repositoryID, created.Number, sourceOID, targetOID)
			if err != nil || !ok {
				t.Fatalf("read intent after failed reconciliation: ok=%v err=%v", ok, err)
			}
			if after.Status != stored.Status || after.TreeOID != stored.TreeOID || after.ResultOID != stored.ResultOID {
				t.Fatalf("failed reconciliation overwrote durable intent: before=%+v after=%+v", stored, after)
			}
			if fixture.ref("refs/heads/main") != targetOID || fixture.refExists(MergeReceiptRef(created.Number)) {
				t.Fatal("failed reconciliation changed the target or fabricated a receipt")
			}
			if fixture.ref(treeRef) != protectedTree {
				t.Fatal("failed reconciliation overwrote the mismatched protected tree ref")
			}
			if intentStatus == state.MergeIntentReady {
				if fixture.ref(resultRef) != sourceOID {
					t.Fatal("failed reconciliation overwrote the mismatched protected result ref")
				}
			} else if fixture.refExists(resultRef) {
				t.Fatal("failed planned reconciliation fabricated a result ref")
			}
		})
	}
}

func TestFailedCreateRefPreservationDoesNotExposePRAndRetryCreatesOne(t *testing.T) {
	fixture := newServiceFixture(t)
	fixture.commitFile("base.txt", "base\n", "base")
	fixture.push("HEAD:refs/heads/main")
	fixture.git("checkout", "-b", "feature")
	sourceOID := fixture.commitFile("feature.txt", "feature\n", "feature")
	fixture.push("HEAD:refs/heads/feature")
	targetOID := fixture.ref("refs/heads/main")
	unrelatedRef := "refs/owngit/retained/heads/" + targetOID
	fixture.git("--git-dir", fixture.remote, "update-ref", unrelatedRef, targetOID)

	sourceRef, _ := RevisionRefNames(1, sourceOID, targetOID)
	blockingPath := filepath.Join(fixture.remote, filepath.FromSlash(filepath.Dir(sourceRef)))
	if err := os.MkdirAll(filepath.Dir(blockingPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(blockingPath, []byte("synthetic ref obstruction\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	input := CreateInput{Repository: fixture.repositoryID, Title: "Retryable creation", SourceBranch: "feature", TargetBranch: "main", ReviewChoice: "skip"}
	if _, err := fixture.service.Create(fixture.ctx, input); err == nil {
		t.Fatal("ref obstruction did not fail pull request creation")
	}
	records, err := fixture.store.PullRequests(fixture.ctx, fixture.repositoryID)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 0 {
		t.Fatalf("failed creation exposed pull requests: %+v", records)
	}
	provisional, err := fixture.store.ProvisionalPullRequests(fixture.ctx, fixture.repositoryID)
	if err != nil {
		t.Fatal(err)
	}
	revisions, err := fixture.store.PullRequestRevisionsFor(fixture.ctx, fixture.repositoryID, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(provisional) != 0 || len(revisions) != 0 {
		t.Fatalf("failed creation was not fully compensated: provisional=%+v revisions=%+v", provisional, revisions)
	}
	if fixture.ref(unrelatedRef) != targetOID {
		t.Fatal("failed creation changed an unrelated protected ref")
	}
	if err := os.Remove(blockingPath); err != nil {
		t.Fatal(err)
	}
	created, err := fixture.service.Create(fixture.ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	if created.Number != 1 {
		t.Fatalf("retry allocated pull request #%d, want the never-visible number 1", created.Number)
	}
	records, err = fixture.store.PullRequests(fixture.ctx, fixture.repositoryID)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 || records[0].Number != created.Number {
		t.Fatalf("retry exposed %d pull requests: %+v", len(records), records)
	}
	if fixture.ref(unrelatedRef) != targetOID {
		t.Fatal("successful retry changed an unrelated protected ref")
	}
}

func TestBranchMovementDuringCreateLeavesNoVisiblePullRequestAndRetryCreatesOne(t *testing.T) {
	fixture := newServiceFixture(t)
	fixture.commitFile("base.txt", "base\n", "base")
	fixture.push("HEAD:refs/heads/main")
	fixture.git("checkout", "-b", "feature")
	oldSourceOID := fixture.commitFile("feature.txt", "one\n", "feature one")
	fixture.push("HEAD:refs/heads/feature")
	targetOID := fixture.ref("refs/heads/main")
	sourceHead, err := fixture.service.resolveBranch(fixture.ctx, fixture.remote, "feature")
	if err != nil {
		t.Fatal(err)
	}
	targetHead, err := fixture.service.resolveBranch(fixture.ctx, fixture.remote, "main")
	if err != nil {
		t.Fatal(err)
	}
	newSourceOID := fixture.commitFile("feature.txt", "two\n", "feature two")
	fixture.push("HEAD:refs/heads/feature")

	_, err = fixture.service.createForHeadsLocked(fixture.ctx, fixture.repositoryID, "Moved during creation", "feature", "main", state.ReviewSkipped, fixture.remote, sourceHead, targetHead)
	if problemCode(err) != "stale_revision" {
		t.Fatalf("create error=%v, want stale_revision", err)
	}
	records, err := fixture.store.PullRequests(fixture.ctx, fixture.repositoryID)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 0 {
		t.Fatalf("branch movement exposed pull requests: %+v", records)
	}
	provisional, err := fixture.store.ProvisionalPullRequests(fixture.ctx, fixture.repositoryID)
	if err != nil {
		t.Fatal(err)
	}
	if len(provisional) != 0 {
		t.Fatalf("branch movement left provisional records: %+v", provisional)
	}
	revisions, err := fixture.store.PullRequestRevisionsFor(fixture.ctx, fixture.repositoryID, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(revisions) != 0 {
		t.Fatalf("branch movement left revision metadata: %+v", revisions)
	}
	oldSourceRef, oldTargetRef := RevisionRefNames(1, oldSourceOID, targetOID)
	if fixture.refExists(oldSourceRef) || fixture.refExists(oldTargetRef) {
		t.Fatal("failed branch verification retained a partial revision ref")
	}

	created, err := fixture.service.Create(fixture.ctx, CreateInput{
		Repository: fixture.repositoryID, Title: "Moved during creation", SourceBranch: "feature", TargetBranch: "main", ReviewChoice: "skip",
	})
	if err != nil {
		t.Fatal(err)
	}
	if created.Number != 1 {
		t.Fatalf("retry allocated pull request #%d, want the never-visible number 1", created.Number)
	}
	if created.Source.OID != newSourceOID || created.Target.OID != targetOID {
		t.Fatalf("retry revision=%s/%s, want %s/%s", created.Source.OID, created.Target.OID, newSourceOID, targetOID)
	}
	records, err = fixture.store.PullRequests(fixture.ctx, fixture.repositoryID)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 || records[0].Number != created.Number {
		t.Fatalf("retry exposed %d pull requests: %+v", len(records), records)
	}
}

func TestReconcileAllResolvesProvisionalCreateCrashWindows(t *testing.T) {
	for _, retained := range []bool{false, true} {
		name := "before_revision_refs"
		if retained {
			name = "after_revision_refs"
		}
		t.Run(name, func(t *testing.T) {
			fixture := newServiceFixture(t)
			fixture.commitFile("base.txt", "base\n", "base")
			fixture.push("HEAD:refs/heads/main")
			fixture.git("checkout", "-b", "feature")
			sourceOID := fixture.commitFile("feature.txt", "feature\n", "feature")
			fixture.push("HEAD:refs/heads/feature")
			targetOID := fixture.ref("refs/heads/main")
			record, err := fixture.store.BeginPullRequestCreation(fixture.ctx, fixture.repositoryID, "Interrupted creation", "feature", "main", sourceOID, targetOID, state.ReviewSkipped, time.Now())
			if err != nil {
				t.Fatal(err)
			}
			if retained {
				if err := fixture.service.ensureRevisionRefs(fixture.ctx, fixture.remote, record, sourceOID, targetOID); err != nil {
					t.Fatal(err)
				}
			}
			if err := fixture.service.ReconcileAll(fixture.ctx); err != nil {
				t.Fatal(err)
			}
			provisional, err := fixture.store.ProvisionalPullRequests(fixture.ctx, fixture.repositoryID)
			if err != nil {
				t.Fatal(err)
			}
			if len(provisional) != 0 {
				t.Fatalf("reconciliation left provisional records: %+v", provisional)
			}
			records, err := fixture.store.PullRequests(fixture.ctx, fixture.repositoryID)
			if err != nil {
				t.Fatal(err)
			}
			if retained {
				if len(records) != 1 || records[0].Number != record.Number || records[0].Status != state.PullRequestOpen {
					t.Fatalf("retained creation was not activated: %+v", records)
				}
			} else if len(records) != 0 {
				t.Fatalf("unretained creation became visible: %+v", records)
			}
		})
	}
}

func TestCreateRejectsReservedMissingAndNonCommitRefs(t *testing.T) {
	t.Setenv("LC_ALL", "C")
	t.Setenv("LANG", "C")
	fixture := newServiceFixture(t)
	fixture.commitFile("file.txt", "base\n", "base")
	fixture.push("HEAD:refs/heads/main")
	if _, err := fixture.service.Create(fixture.ctx, CreateInput{
		Repository: fixture.repositoryID, Title: "Reserved", SourceBranch: "refs/owngit/private", TargetBranch: "main", ReviewChoice: "skip",
	}); problemCode(err) != "reserved_ref" {
		t.Fatalf("reserved ref error=%v code=%q", err, problemCode(err))
	}
	if _, err := fixture.service.Create(fixture.ctx, CreateInput{
		Repository: fixture.repositoryID, Title: "Missing", SourceBranch: "missing", TargetBranch: "main", ReviewChoice: "skip",
	}); problemCode(err) != "source_branch_missing" {
		t.Fatalf("missing ref error=%v code=%q", err, problemCode(err))
	}
	hashResult := fixture.gitResult("-c", "core.autocrlf=true", "-c", "core.safecrlf=warn", "--git-dir", fixture.remote, "hash-object", "-w", filepath.Join(fixture.work, "file.txt"))
	blob := hashResult.Stdout
	if len(blob) != 40 || !validOID(blob) {
		t.Fatalf("hash-object returned invalid SHA-1 object ID %q", blob)
	}
	if !strings.Contains(hashResult.Stderr, "warning:") || !strings.Contains(hashResult.Stderr, "LF") || !strings.Contains(hashResult.Stderr, "CRLF") {
		t.Fatalf("hash-object stderr did not contain the expected LF/CRLF conversion warning: %q", hashResult.Stderr)
	}
	blobRef := filepath.Join(fixture.remote, "refs", "heads", "blob")
	if err := os.MkdirAll(filepath.Dir(blobRef), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(blobRef, []byte(blob+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.service.Create(fixture.ctx, CreateInput{
		Repository: fixture.repositoryID, Title: "Blob", SourceBranch: "blob", TargetBranch: "main", ReviewChoice: "skip",
	}); problemCode(err) != "source_not_commit" {
		t.Fatalf("noncommit ref error=%v code=%q", err, problemCode(err))
	}
}

type serviceFixture struct {
	t            *testing.T
	ctx          context.Context
	store        *state.Store
	manager      *repository.Manager
	service      *Service
	repositoryID string
	remote       string
	work         string
}

func newServiceFixture(t *testing.T) *serviceFixture {
	t.Helper()
	ctx := context.Background()
	root := t.TempDir()
	stateRoot := filepath.Join(root, "state")
	repositoryRoot := filepath.Join(root, "repositories")
	if err := os.Mkdir(repositoryRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	store, err := state.Open(ctx, stateRoot)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := store.CompleteSetup(ctx, repositoryRoot, "open", "", "synthetic-admin-hash", true); err != nil {
		t.Fatal(err)
	}
	runner, err := gitexec.New("", filepath.Join(stateRoot, "runtime"))
	if err != nil {
		t.Fatal(err)
	}
	manager := &repository.Manager{Store: store, Git: runner, Locks: gitexec.NewLocks(), Root: repositoryRoot}
	stored, err := manager.Create(ctx, "project", "pull request fixture")
	if err != nil {
		t.Fatal(err)
	}
	remote, err := manager.Path(stored.ID)
	if err != nil {
		t.Fatal(err)
	}
	work := filepath.Join(root, "work")
	runFixtureGit(t, "", "init", "--initial-branch=main", work)
	runFixtureGit(t, work, "config", "user.name", "PR Test")
	runFixtureGit(t, work, "config", "user.email", "pr-test@example.invalid")
	runFixtureGit(t, work, "remote", "add", "origin", remote)
	return &serviceFixture{
		t: t, ctx: ctx, store: store, manager: manager,
		service: &Service{Store: store, Repositories: manager}, repositoryID: stored.ID, remote: remote, work: work,
	}
}

func (fixture *serviceFixture) commitFile(name, content, message string) string {
	fixture.t.Helper()
	path := filepath.Join(fixture.work, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		fixture.t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		fixture.t.Fatal(err)
	}
	fixture.git("add", "--", name)
	fixture.git("commit", "-m", message)
	return fixture.gitOutput("rev-parse", "HEAD")
}

func (fixture *serviceFixture) push(spec string) {
	fixture.t.Helper()
	fixture.git("push", "origin", spec)
}

func (fixture *serviceFixture) ref(name string) string {
	fixture.t.Helper()
	return fixture.gitOutput("--git-dir", fixture.remote, "rev-parse", "--verify", name)
}

func (fixture *serviceFixture) refExists(name string) bool {
	fixture.t.Helper()
	command := exec.Command("git", "--git-dir", fixture.remote, "show-ref", "--verify", "--quiet", name)
	command.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	return command.Run() == nil
}

func (fixture *serviceFixture) git(arguments ...string) {
	fixture.t.Helper()
	runFixtureGit(fixture.t, fixture.work, arguments...)
}

func (fixture *serviceFixture) gitOutput(arguments ...string) string {
	fixture.t.Helper()
	return fixture.gitResult(arguments...).Stdout
}

func (fixture *serviceFixture) gitResult(arguments ...string) fixtureGitResult {
	fixture.t.Helper()
	return fixtureGitResultOutput(fixture.t, fixture.work, arguments...)
}

func runFixtureGit(t *testing.T, directory string, arguments ...string) {
	t.Helper()
	command := exec.Command("git", arguments...)
	command.Dir = directory
	command.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(arguments, " "), err, output)
	}
}

type fixtureGitResult struct {
	Stdout string
	Stderr string
}

func fixtureGitResultOutput(t *testing.T, directory string, arguments ...string) fixtureGitResult {
	t.Helper()
	command := exec.Command("git", arguments...)
	command.Dir = directory
	command.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	var stderr bytes.Buffer
	command.Stderr = &stderr
	output, err := command.Output()
	if err != nil {
		t.Fatalf("git %s: %v\nstdout:\n%s\nstderr:\n%s", strings.Join(arguments, " "), err, output, stderr.Bytes())
	}
	return fixtureGitResult{Stdout: strings.TrimSpace(string(output)), Stderr: strings.TrimSpace(stderr.String())}
}

func newOldGitRunner(t *testing.T, runtimeDirectory string) (*gitexec.Runner, string) {
	t.Helper()
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	marker := filepath.Join(root, "merge-tree-invoked")
	wrapper := filepath.Join(root, "git-old")
	script := "#!/bin/sh\n" +
		"if test \"$1\" = --version; then echo 'git version 2.37.6'; exit 0; fi\n" +
		"for arg in \"$@\"; do\n" +
		"  if test \"$arg\" = merge-tree; then printf invoked > " + shellQuote(marker) + "; exit 97; fi\n" +
		"done\n" +
		"exec " + shellQuote(realGit) + " \"$@\"\n"
	if err := os.WriteFile(wrapper, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	runner, err := gitexec.New(wrapper, runtimeDirectory)
	if err != nil {
		t.Fatal(err)
	}
	return runner, marker
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}

func blockerCode(view *View) string {
	if view == nil || len(view.MergeEligibility.Blockers) == 0 {
		return ""
	}
	return view.MergeEligibility.Blockers[0].Code
}

func problemCode(err error) string {
	var problem *Problem
	if errors.As(err, &problem) {
		return problem.Code
	}
	return ""
}
