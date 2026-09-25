package checkrun

import (
	"path/filepath"
	"testing"
	"time"

	"owngit/internal/checksource"
	"owngit/internal/checkworkflow"
	"owngit/internal/state"
)

// Another push holds the repository's write lock when a job starts copying its
// source. The job used to end unavailable without running any command; it now
// waits for the push to finish and then runs.
func TestJobWaitsForAPushThatHoldsTheRepository(t *testing.T) {
	fixture := newPushFixture(t, 4)
	now := time.Now().UTC()
	if _, err := fixture.store.SetCheckPolicy(fixture.ctx, state.CheckPolicyInput{
		RepositoryID: fixture.repositoryID, Executor: state.CheckExecutorHost,
		AllowedEvents: []string{checkworkflow.EventPush}, MaxTimeoutMS: 60_000, MaxOutputLimitBytes: 64 << 10,
		QueueLimit: 4, MaxActiveJobs: 1, MaxLeaseMS: 60_000,
	}, now); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.store.GrantCheckConsent(fixture.ctx, fixture.repositoryID, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	fixture.pushWorkflow("main", validWorkflow)
	noErr(t, fixture.coordinator.reconcile(fixture.ctx))
	workspace, err := checksource.AcquireWorkspaceRoot(filepath.Join(t.TempDir(), "check-jobs"))
	noErr(t, err)
	t.Cleanup(workspace.Close)
	fixture.coordinator.workspace = workspace

	lock := fixture.coordinator.Repositories.Locks.For(fixture.repositoryID)
	lock.Lock()
	released := make(chan struct{})
	go func() {
		defer close(released)
		time.Sleep(300 * time.Millisecond)
		lock.Unlock()
	}()
	noErr(t, fixture.coordinator.runOneLocal(fixture.ctx))
	<-released

	jobs, err := fixture.store.LatestCheckJobs(fixture.ctx, fixture.repositoryID, 10)
	noErr(t, err)
	if len(jobs) != 1 || jobs[0].Status != state.CheckJobPassed {
		t.Fatalf("job after a concurrent push: %+v", jobs)
	}
}
