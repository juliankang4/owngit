package checkrun

import (
	"context"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"owngit/internal/checksource"
	"owngit/internal/checkworkflow"
	"owngit/internal/state"
	"owngit/internal/testfixture"
)

func TestHostJobEnvironment(t *testing.T) {
	fixture := newPushFixture(t, 4)
	now := time.Now().UTC()
	_, err := fixture.store.SetCheckPolicy(fixture.ctx, state.CheckPolicyInput{
		RepositoryID: fixture.repositoryID, Executor: state.CheckExecutorHost,
		AllowedEvents: []string{checkworkflow.EventPush}, MaxTimeoutMS: 60_000, MaxOutputLimitBytes: 64 << 10,
		QueueLimit: 4, MaxActiveJobs: 1, MaxLeaseMS: 60_000,
	}, now)
	noErr(t, err)
	_, err = fixture.store.GrantCheckConsent(fixture.ctx, fixture.repositoryID, now.Add(time.Second))
	noErr(t, err)
	environment := testfixture.NewCheckEnvironment(t)
	fixture.pushWorkflow("main", strings.Replace(validWorkflow, `"exit 0"`, strconv.Quote(environment.Command), 1))
	noErr(t, fixture.coordinator.reconcile(fixture.ctx))
	root := filepath.Join(t.TempDir(), "jobs")
	workspace, err := checksource.AcquireWorkspaceRoot(root)
	noErr(t, err)
	t.Cleanup(workspace.Close)
	fixture.coordinator.workspace = workspace
	noErr(t, fixture.coordinator.runOneLocal(fixture.ctx))
	jobs, err := fixture.store.LatestCheckJobs(fixture.ctx, fixture.repositoryID, 10)
	noErr(t, err)
	if len(jobs) != 1 {
		t.Fatalf("host jobs=%+v", jobs)
	}
	job := jobs[0]
	if job.Status != state.CheckJobFailed {
		t.Fatalf("host job=%+v", job)
	}
	attempt, exists, err := fixture.store.CheckAttemptByID(fixture.ctx, fixture.repositoryID, job.AttemptID)
	noErr(t, err)
	if !exists || len(attempt.Results) != 1 || attempt.EffectiveWorktreeState() != state.WorktreeClean {
		t.Fatalf("host attempt=%+v exists=%v", attempt, exists)
	}
	environment.Assert(t, attempt.Results[0].OutputExcerpt, filepath.Join(root, job.ID))
}

func TestDockerControlEnvironment(t *testing.T) {
	t.Setenv("DOCKER_CONFIG", "control-config")
	t.Setenv("CHECK_LOCAL_SETTING", "control-setting")
	command, arguments := "env", []string(nil)
	if runtime.GOOS == "windows" {
		command, arguments = "cmd", []string{"/d", "/c", "set"}
	}
	executable, err := exec.LookPath(command)
	noErr(t, err)
	output, err := runDockerControl(context.Background(), executable, "", arguments...)
	noErr(t, err)
	for _, want := range []string{"DOCKER_CONFIG=control-config", "CHECK_LOCAL_SETTING=control-setting"} {
		t.Run(want, func(t *testing.T) {
			if !strings.Contains(output, want) || strings.Contains(output, "did not pass") {
				t.Fatalf("control environment is missing %q", want)
			}
		})
	}
}
