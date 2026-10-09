package checkrun

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"owngit/internal/checksource"
	"owngit/internal/state"
)

func TestCoordinatorRecoveryPrivatePayloads(t *testing.T) {
	fixture := actionsFixture(t, state.CheckExecutorHost)
	image := "sha256:" + strings.Repeat("a", 64)
	_, err := fixture.store.SetCheckPolicy(fixture.ctx, state.CheckPolicyInput{
		RepositoryID: fixture.repositoryID,
		Executor:     state.CheckExecutorContainer,
		AllowedEvents: []string{
			"push",
		},
		MaxTimeoutMS:        60_000,
		MaxOutputLimitBytes: 64 << 10,
		QueueLimit:          16,
		MaxActiveJobs:       4,
		MaxLeaseMS:          60_000,
		Execution: state.CheckExecutionSettings{
			ContainerRuntime:      "docker-local",
			ContainerImage:        image,
			ContainerNetwork:      "none",
			ContainerCPUMillis:    1000,
			ContainerMemoryBytes:  128 << 20,
			ContainerPIDs:         64,
			ContainerScratchBytes: 16 << 20,
		},
	}, time.Now().UTC())
	noErr(t, err)
	_, err = fixture.store.GrantCheckConsent(fixture.ctx, fixture.repositoryID, time.Now().UTC())
	noErr(t, err)

	workflow := "on: push\njobs:\n  test:\n    runs-on: windows-latest\n    steps:\n      - env:\n          TOKEN: ${{ secrets.TOKEN }}\n        run: echo ok\n"
	oid := pushActionsFiles(fixture, map[string]string{".github/workflows/ci.yml": workflow})
	_, err = fixture.coordinator.AdmitEvent(fixture.ctx, EventRequest{RepositoryID: fixture.repositoryID, Event: "push", SourceOID: oid, TriggerRef: "main"})
	noErr(t, err)
	job, claimed, err := fixture.store.ClaimLocalCheckJob(fixture.ctx, fixture.repositoryID, time.Now().UTC())
	if err != nil || !claimed {
		t.Fatalf("claim=%v err=%v", claimed, err)
	}
	if job.Executor != state.CheckExecutorContainer || job.Execution.ContainerImage != image {
		t.Fatalf("workflow labels changed policy execution: executor=%q image=%q", job.Executor, job.Execution.ContainerImage)
	}

	workspacePath := filepath.Join(t.TempDir(), "check-jobs")
	workspace, err := checksource.AcquireWorkspaceRoot(workspacePath)
	noErr(t, err)
	envelope, _, err := workspace.PrepareJob(job.ID)
	noErr(t, err)
	payloadPath := filepath.Join(envelope, "actions", "scripts", "step-1.env")
	noErr(t, os.MkdirAll(filepath.Dir(payloadPath), 0o700))
	const secret = "synthetic-secret"
	noErr(t, os.WriteFile(payloadPath, []byte("export TOKEN='"+secret+"'\n"), 0o600))

	for _, name := range []string{"scripts/step-1.script", "scripts/step-1.script.ps1", "scripts/step-1.script.cmd", "scripts/step-1.lookup/step-1.lookup.env", "files/step-1/GITHUB_ENV"} {
		path := filepath.Join(envelope, "actions", filepath.FromSlash(name))
		noErr(t, os.MkdirAll(filepath.Dir(path), 0o700))
		noErr(t, os.WriteFile(path, []byte(secret), 0o600))
	}
	marker := filepath.Join(envelope, "source", "keep-workspace")
	noErr(t, os.Mkdir(filepath.Dir(marker), 0o700))
	noErr(t, os.WriteFile(marker, []byte("non-secret workspace"), 0o600))
	jsonScript := filepath.Join(envelope, "json-check-script")
	noErr(t, os.WriteFile(jsonScript, []byte("non-secret JSON check"), 0o600))

	attemptID, err := state.RandomID()
	noErr(t, err)
	_, _, err = fixture.store.StartCheckJob(fixture.ctx, state.CheckJobStart{
		RepositoryID: job.RepositoryID, JobID: job.ID, LeaseID: job.LeaseID,
		CredentialID: job.CredentialID, CredentialGeneration: job.CredentialGeneration,
		Protection: state.ProtectionContainer, AttemptID: attemptID,
	}, time.Now().UTC())
	noErr(t, err)
	authority := state.CheckJobCompletionAuthority{JobID: job.ID, LeaseID: job.LeaseID, CredentialID: job.CredentialID, CredentialGeneration: job.CredentialGeneration}
	noErr(t, fixture.store.PlanCheckContainer(fixture.ctx, authority, "owngit-check-"+job.ID+"-step-1", "synthetic-daemon", time.Now().UTC()))
	if _, exists, err := fixture.store.ActionsJobPlan(fixture.ctx, fixture.repositoryID, job.ID); err != nil || !exists {
		t.Fatalf("plan before recovery exists=%v err=%v", exists, err)
	}
	workspace.Close()

	restarted := &Coordinator{
		Store:         fixture.store,
		Repositories:  fixture.coordinator.Repositories,
		PullRequests:  fixture.coordinator.PullRequests,
		WorkspaceRoot: workspacePath,
		DockerPath:    "/owngit-u6c-no-such-docker",
		Logf:          fixture.recordLog,
	}
	startContext, cancelStart := context.WithCancel(context.Background())
	defer cancelStart()
	noErr(t, restarted.Start(startContext))
	stopContext, cancelStop := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancelStop()
	defer func() { noErr(t, restarted.Stop(stopContext)) }()

	for _, name := range []string{"scripts", "files"} {
		if _, err := os.Stat(filepath.Join(envelope, "actions", name)); !os.IsNotExist(err) {
			t.Fatalf("private step material remains after restart: %s: %v", name, err)
		}
	}
	for _, path := range []string{marker, jsonScript} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("non-secret envelope material was lost: %v", err)
		}
	}

	if _, exists, err := fixture.store.ActionsJobPlan(fixture.ctx, fixture.repositoryID, job.ID); err != nil || exists {
		t.Fatalf("terminal plan after restart exists=%v err=%v", exists, err)
	}
	stored, found, err := fixture.store.CheckJob(fixture.ctx, fixture.repositoryID, job.ID)
	if err != nil || !found || stored.Status != state.CheckJobAmbiguous {
		t.Fatalf("recovered job=%+v found=%v err=%v", stored, found, err)
	}
	ownership, found, err := fixture.store.CheckContainerOwnershipForJob(fixture.ctx, job.ID)
	if err != nil || !found {
		t.Fatalf("retained ownership=%+v found=%v err=%v", ownership, found, err)
	}
	if len(fixture.logLines("startup container cleanup")) == 0 {
		t.Fatal("unavailable Docker cleanup was not reported")
	}

	noErr(t, restarted.Stop(stopContext))
	noErr(t, fixture.store.ForgetCheckContainer(fixture.ctx, ownership))
	cleanup, err := checksource.AcquireWorkspaceRoot(workspacePath)
	noErr(t, err)
	defer cleanup.Close()
	noErr(t, cleanup.RemoveJob(job.ID))
}
