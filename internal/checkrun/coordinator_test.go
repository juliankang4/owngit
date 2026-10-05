package checkrun

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"owngit/internal/checkworkflow"
	"owngit/internal/gitexec"
	"owngit/internal/pullrequest"
	"owngit/internal/repository"
	"owngit/internal/state"
)

func TestBoundedBranchBatchesMakeFairProgressPastFirstPage(t *testing.T) {
	branches := make([]repository.Ref, 130)
	for index := range branches {
		branches[index] = repository.Ref{Name: fmt.Sprintf("branch-%03d", index)}
	}
	seen := make(map[string]bool)
	after := ""
	for pass := 0; pass < 3; pass++ {
		batch := boundedBranchesAfter(branches, after, maximumObservedRefs)
		if len(batch) != maximumObservedRefs {
			t.Fatalf("pass %d batch size=%d", pass, len(batch))
		}
		for _, branch := range batch {
			seen[branch.Name] = true
		}
		after = batch[len(batch)-1].Name
	}
	if len(seen) != len(branches) {
		t.Fatalf("fair batches reached %d/%d branches", len(seen), len(branches))
	}
}

// The coordinator extends a lease that is about to end before it expires,
// with the same cadence the runner uses. Its own one-second wait used to
// arrive after a lease with little left had already ended.
func TestCoordinatorRenewsALeaseThatIsAboutToEnd(t *testing.T) {
	fixture := newPushFixture(t, 4)
	now := time.Now().UTC()
	if _, err := fixture.store.SetCheckPolicy(fixture.ctx, state.CheckPolicyInput{
		RepositoryID: fixture.repositoryID, Executor: state.CheckExecutorHost,
		AllowedEvents: []string{checkworkflow.EventPush}, MaxTimeoutMS: 60_000, MaxOutputLimitBytes: 64 << 10,
		QueueLimit: 4, MaxActiveJobs: 1, MaxLeaseMS: state.MinimumCheckLeaseMS,
	}, now); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.store.GrantCheckConsent(fixture.ctx, fixture.repositoryID, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	fixture.pushWorkflow("main", validWorkflow)
	noErr(t, fixture.coordinator.reconcile(fixture.ctx))
	job, claimed, err := fixture.store.ClaimLocalCheckJob(fixture.ctx, fixture.repositoryID, time.Now().UTC())
	if err != nil || !claimed {
		t.Fatalf("claim claimed=%v err=%v", claimed, err)
	}
	// Leave the claim a quarter of its lease, as a slow handover would.
	aboutToEnd := time.Now().UTC().Add(250 * time.Millisecond)
	job.LeaseExpiresAt = &aboutToEnd
	noErr(t, fixture.store.Exec(fixture.ctx, `UPDATE check_jobs SET lease_expires_at=? WHERE id=?`, aboutToEnd.UnixNano(), job.ID))
	authority := state.CheckJobCompletionAuthority{JobID: job.ID, LeaseID: job.LeaseID, CredentialID: job.CredentialID, CredentialGeneration: job.CredentialGeneration}
	watchContext, cancelWatch := context.WithCancel(fixture.ctx)
	watchDone := make(chan error, 1)
	go fixture.coordinator.watchLease(watchContext, cancelWatch, job, authority, watchDone)

	// A renewal moves the stored deadline past the shortened one. The bound
	// only keeps a coordinator that never renews from hanging the test.
	deadline := time.Now().Add(10 * time.Second)
	for {
		stored, exists, err := fixture.store.CheckJob(fixture.ctx, fixture.repositoryID, job.ID)
		noErr(t, err)
		if !exists {
			t.Fatal("the claimed job disappeared")
		}
		if stored.LeaseExpiresAt != nil && stored.LeaseExpiresAt.After(aboutToEnd) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the lease was not renewed before it ended: %+v", stored)
		}
		time.Sleep(5 * time.Millisecond)
	}
	cancelWatch()
	if err := <-watchDone; !errors.Is(err, context.Canceled) {
		t.Fatalf("watchLease returned %v after the test cancelled it", err)
	}
	stored, _, err := fixture.store.CheckJob(fixture.ctx, fixture.repositoryID, job.ID)
	if err != nil || stored.Status != state.CheckJobClaimed {
		t.Fatalf("job after a renewal: %+v err=%v", stored, err)
	}
}

func TestCoordinatorStartStopAndRestartReturn(t *testing.T) {
	root := t.TempDir()
	store, err := state.Open(context.Background(), filepath.Join(root, "state"))
	noErr(t, err)
	defer store.Close()
	git, err := gitexec.New("", filepath.Join(root, "runtime"))
	noErr(t, err)
	repositoryRoot := filepath.Join(root, "repositories")
	noErr(t, os.Mkdir(repositoryRoot, 0o700))
	manager := &repository.Manager{Store: store, Git: git, Locks: gitexec.NewLocks(), Root: repositoryRoot}
	coordinator := &Coordinator{
		Store: store, Repositories: manager, PullRequests: &pullrequest.Service{Store: store, Repositories: manager},
		WorkspaceRoot: filepath.Join(root, "workspaces"), Interval: time.Hour,
	}
	// Start and Stop must return instead of running for the scheduler's
	// lifetime. The bound only keeps a call that never returns from hanging
	// the test; a busy machine may make startup work slow.
	const bound = 30 * time.Second
	for cycle := 0; cycle < 2; cycle++ {
		ctx, cancel := context.WithCancel(context.Background())
		started := make(chan error, 1)
		go func() { started <- coordinator.Start(ctx) }()
		select {
		case err := <-started:
			if err != nil {
				cancel()
				t.Fatalf("cycle %d start: %v", cycle, err)
			}
		case <-time.After(bound):
			cancel()
			t.Fatalf("cycle %d start did not return within %s", cycle, bound)
		}
		// A retained push event must not keep Stop waiting for admission.
		coordinator.NotePush("missing-repository", []PushUpdate{{Ref: "refs/heads/main", New: "0000000000000000000000000000000000000000"}})
		stopContext, stopCancel := context.WithTimeout(context.Background(), bound)
		if err := coordinator.Stop(stopContext); err != nil {
			stopCancel()
			cancel()
			t.Fatalf("cycle %d stop: %v", cycle, err)
		}
		stopCancel()
		cancel()
	}
}
