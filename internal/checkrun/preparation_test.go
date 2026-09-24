package checkrun

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"owngit/internal/checkworkflow"
	"owngit/internal/state"
)

// While a repository is being prepared after startup, the coordinator neither
// reads its refs nor claims its queued local job, and records no failure. Once
// the repository is ready, both resume.
func TestCoordinatorSkipsAPreparingRepository(t *testing.T) {
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
	if refs := fixture.jobRefs(); len(refs) != 1 {
		t.Fatalf("fixture admitted jobs for %v, want main", refs)
	}

	manager := fixture.coordinator.Repositories
	manager.PreparationRetry = 10 * time.Millisecond
	var failing atomic.Bool
	failing.Store(true)
	preparation, cancel := context.WithCancel(fixture.ctx)
	t.Cleanup(func() {
		cancel()
		if err := manager.StopPreparation(context.Background()); err != nil {
			t.Error(err)
		}
	})
	noErr(t, manager.StartPreparation(preparation, func(context.Context, string, string) error {
		if failing.Load() {
			return errors.New("synthetic preparation failure")
		}
		return nil
	}, 5*time.Second, nil))
	if !manager.Preparing(fixture.repositoryID) {
		t.Fatal("fixture repository is not preparing")
	}

	fixture.pushWorkflow("second", validWorkflow)
	noErr(t, fixture.coordinator.reconcile(fixture.ctx))
	noErr(t, fixture.coordinator.runOneLocal(fixture.ctx))
	if refs := fixture.jobRefs(); len(refs) != 1 {
		t.Fatalf("a preparing repository admitted jobs: %v", refs)
	}
	if observed := fixture.observed(); observed["refs/heads/second"] != "" {
		t.Fatalf("a preparing repository was observed: %v", observed)
	}
	jobs, err := fixture.store.LatestCheckJobs(fixture.ctx, fixture.repositoryID, 10)
	noErr(t, err)
	if len(jobs) != 1 || jobs[0].Status != state.CheckJobPending {
		t.Fatalf("the queued job of a preparing repository changed: %+v", jobs)
	}
	for _, line := range fixture.logs {
		if strings.Contains(line, "reconcile") || strings.Contains(line, "execution") {
			t.Fatalf("the coordinator reported a preparing repository: %v", fixture.logs)
		}
	}

	failing.Store(false)
	deadline := time.Now().Add(10 * time.Second)
	for manager.Preparing(fixture.repositoryID) {
		if time.Now().After(deadline) {
			t.Fatal("the repository did not become ready")
		}
		time.Sleep(5 * time.Millisecond)
	}
	noErr(t, fixture.coordinator.reconcile(fixture.ctx))
	if refs := fixture.jobRefs(); len(refs) != 2 {
		t.Fatalf("jobs after preparation=%v, want main and second", refs)
	}
	// runOneLocal would execute the job, which this fixture cannot do; the
	// claim it makes is checked directly.
	if job, claimed, err := fixture.store.ClaimLocalCheckJob(fixture.ctx, fixture.repositoryID, time.Now().UTC()); err != nil || !claimed || job.ID != jobs[0].ID {
		t.Fatalf("the skipped job is not claimable after preparation: claimed=%v job=%s err=%v", claimed, job.ID, err)
	}
}
