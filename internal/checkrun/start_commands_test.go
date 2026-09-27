package checkrun

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"owngit/internal/checksource"
	"owngit/internal/checkworkflow"
	"owngit/internal/state"
)

// A local job whose commands cannot be read is not started. The coordinator
// records it as not run, as it does when the source cannot be prepared. It
// used to be started without an attempt and to end ambiguous.
func TestLocalJobWhoseCommandsCannotBeReadIsUnavailable(t *testing.T) {
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
	noErr(t, fixture.store.Exec(fixture.ctx, `UPDATE check_configurations SET checks_json='{' WHERE repository_id=?`, fixture.repositoryID))

	runErr := fixture.coordinator.runOneLocal(fixture.ctx)
	if _, err := fixture.store.ExpireCheckJobLeases(fixture.ctx, time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	jobs, err := fixture.store.LatestCheckJobs(fixture.ctx, fixture.repositoryID, 10)
	noErr(t, err)
	if len(jobs) != 1 || jobs[0].Status != state.CheckJobUnavailable || jobs[0].StartedAt != nil || jobs[0].AttemptID != "" || !strings.Contains(jobs[0].Summary, "commands could not be read") {
		t.Fatalf("job after a start without commands: %+v (run err=%v)", jobs, runErr)
	}
	noErr(t, runErr)
}

// A claimed job that ran nothing while the coordinator is stopping is recorded
// as interrupted, whichever step could not run it, since the stop may be the
// cause.
func TestNotRunJobIsInterruptedWhileStopping(t *testing.T) {
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
	job, claimed, err := fixture.store.ClaimLocalCheckJob(fixture.ctx, fixture.repositoryID, time.Now().UTC())
	if err != nil || !claimed {
		t.Fatalf("claim claimed=%v err=%v", claimed, err)
	}
	stopping, stop := context.WithCancel(fixture.ctx)
	stop()
	authority := state.CheckJobCompletionAuthority{JobID: job.ID, LeaseID: job.LeaseID, CredentialID: job.CredentialID, CredentialGeneration: job.CredentialGeneration}
	noErr(t, fixture.coordinator.recordNotRun(stopping, job, authority, "The job was not started: ", errors.New("the read was cancelled")))
	stored, _, err := fixture.store.CheckJob(fixture.ctx, fixture.repositoryID, job.ID)
	if err != nil || stored.Status != state.CheckJobInterrupted || !strings.Contains(stored.Summary, "the read was cancelled") {
		t.Fatalf("job after a stop: %+v err=%v", stored, err)
	}
}
