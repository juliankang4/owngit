package checkrun

import (
	"path/filepath"
	"testing"
	"time"

	"owngit/internal/checksource"
	"owngit/internal/checkworkflow"
	"owngit/internal/state"
)

// A repository whose name sorts late but holds the oldest pending job runs
// first, ahead of younger jobs of an earlier-named repository. The commands
// are unreadable, so the claimed job ends as not run without a process.
func TestLocalClaimTakesTheOldestJobAcrossRepositories(t *testing.T) {
	fixture := newPushFixture(t, 4)
	early, err := fixture.coordinator.Repositories.Create(fixture.ctx, "aaa", "")
	noErr(t, err)
	now := time.Now().UTC()
	for _, id := range []string{fixture.repositoryID, early.ID} {
		_, err := fixture.store.SetCheckPolicy(fixture.ctx, state.CheckPolicyInput{
			RepositoryID: id, Executor: state.CheckExecutorHost,
			AllowedEvents: []string{checkworkflow.EventPush}, MaxTimeoutMS: 60_000, MaxOutputLimitBytes: 64 << 10,
			QueueLimit: 4, MaxActiveJobs: 1, MaxLeaseMS: 60_000,
		}, now)
		noErr(t, err)
		_, err = fixture.store.GrantCheckConsent(fixture.ctx, id, now.Add(time.Second))
		noErr(t, err)
	}
	oid := fixture.pushWorkflow("main", validWorkflow)
	admit := func(id, ref string, at time.Time) state.CheckJob {
		job, _, err := fixture.store.AdmitCheckJob(fixture.ctx, state.CheckJobRequest{
			RepositoryID: id, Trigger: checkworkflow.EventPush, EventKey: "refs/heads/" + ref + "@" + oid, SourceOID: oid, TriggerRef: ref,
			WorkflowPath: checkworkflow.Path, WorkflowDigest: "0000000000000000000000000000000000000000000000000000000000000000",
			Checks: []state.CheckDefinition{{Name: "n", Command: "exit 0"}},
		}, at)
		noErr(t, err)
		return job
	}
	late := admit(fixture.repositoryID, "main", now.Add(2*time.Second))
	admit(early.ID, "main", now.Add(3*time.Second))
	admit(early.ID, "dev", now.Add(4*time.Second))
	workspace, err := checksource.AcquireWorkspaceRoot(filepath.Join(t.TempDir(), "check-jobs"))
	noErr(t, err)
	t.Cleanup(workspace.Close)
	fixture.coordinator.workspace = workspace
	noErr(t, fixture.store.Exec(fixture.ctx, `UPDATE check_configurations SET checks_json='{'`))
	noErr(t, fixture.coordinator.runOneLocal(fixture.ctx))
	stored, _, err := fixture.store.CheckJob(fixture.ctx, fixture.repositoryID, late.ID)
	noErr(t, err)
	if stored.Status == state.CheckJobPending {
		t.Fatal("the older job of the later-named repository is still pending after one claim")
	}
}
