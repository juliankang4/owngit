package containerjob

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"owngit/internal/state"
)

// ContainerJob uses synthetic state and the same opt-in cached image as the
// configured-check integration tests. It never downloads an image.
type ContainerJob struct {
	Store     *state.Store
	Job       state.CheckJob
	Workspace string
	Docker    string
	Image     string
}

func NewContainerJob(t testing.TB) *ContainerJob {
	t.Helper()
	if testing.Short() {
		t.Skip("starts and inspects real containers")
	}
	if os.Getenv("OWNGIT_REAL_DOCKER_TEST") != "1" {
		t.Skip("set OWNGIT_REAL_DOCKER_TEST=1 and OWNGIT_DOCKER_IMAGE for real container integration")
	}
	image := os.Getenv("OWNGIT_DOCKER_IMAGE")
	if os.Geteuid() == 0 || !strings.HasPrefix(image, "sha256:") || !state.ImmutableContainerImage(image) || !filepath.IsAbs(os.Getenv("TMPDIR")) {
		t.Fatal("a nonroot controller, cached sha256 image ID and shared absolute TMPDIR are required")
	}
	docker, err := exec.LookPath("docker")
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	ctx := context.Background()
	store, err := state.Open(ctx, filepath.Join(root, "state"))
	if err != nil {
		t.Fatal(err)
	}
	fixture := &ContainerJob{Store: store, Docker: docker, Image: image, Workspace: filepath.Join(root, "job's folder", "source")}
	t.Cleanup(func() {
		if fixture.Job.ID != "" {
			output := fixture.DockerCommand(t, "ps", "--all", "--quiet", "--filter", "label=com.owngit.check-job="+fixture.Job.ID)
			for _, id := range strings.Fields(output) {
				label := fixture.DockerCommand(t, "inspect", "--format", `{{index .Config.Labels "com.owngit.check-job"}}`, id)
				if strings.TrimSpace(label) != fixture.Job.ID {
					t.Error("container cleanup ownership does not match")
					continue
				}
				t.Error("a task-owned container was left after execution")
				fixture.DockerCommand(t, "rm", "--force", id)
			}
		}
		if err := store.Close(); err != nil {
			t.Error(err)
		}
	})
	now := time.Now().UTC()
	execution := state.CheckExecutionSettings{ContainerRuntime: "docker-local", ContainerImage: image, ContainerNetwork: "none",
		ContainerCPUMillis: 1000, ContainerMemoryBytes: 128 << 20, ContainerPIDs: 64, ContainerScratchBytes: 16 << 20}
	for _, err := range []error{
		os.MkdirAll(filepath.Join(fixture.Workspace, "sub"), 0o700),
		store.CompleteSetup(ctx, filepath.Join(root, "repositories"), "open", "", "test-admin-hash", true),
		store.AddRepository(ctx, state.Repository{ID: "containers", Name: "Containers", CreatedAt: now}),
	} {
		if err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.SetCheckPolicy(ctx, state.CheckPolicyInput{RepositoryID: "containers", Executor: state.CheckExecutorContainer,
		AllowedEvents: []string{"push"}, MaxTimeoutMS: 60000, MaxOutputLimitBytes: 64 << 10,
		QueueLimit: 4, MaxActiveJobs: 1, MaxLeaseMS: 60000, Execution: execution}, now); err != nil {
		t.Fatal(err)
	}
	if _, err := store.GrantCheckConsent(ctx, "containers", now); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.AdmitCheckJob(ctx, state.CheckJobRequest{RepositoryID: "containers", Trigger: "push", TriggerRef: "main",
		SourceOID: strings.Repeat("a", 40), EventKey: "refs/heads/main@" + strings.Repeat("a", 40), WorkflowDigest: strings.Repeat("b", 64),
		Checks: []state.CheckDefinition{{Name: "workflow", Command: "run workflow"}}}, now); err != nil {
		t.Fatal(err)
	}
	job, found, err := store.ClaimLocalCheckJob(ctx, "containers", now)
	if err != nil || !found {
		t.Fatalf("claim container job: found=%v err=%v", found, err)
	}
	fixture.Job = job
	return fixture
}

func (fixture *ContainerJob) Start(t testing.TB) {
	t.Helper()
	id, err := state.RandomID()
	if err != nil {
		t.Fatal(err)
	}
	job := fixture.Job
	_, _, err = fixture.Store.StartCheckJob(context.Background(), state.CheckJobStart{RepositoryID: job.RepositoryID, JobID: job.ID, LeaseID: job.LeaseID,
		CredentialID: job.CredentialID, CredentialGeneration: job.CredentialGeneration, Protection: state.ProtectionContainer, AttemptID: id}, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
}

func (fixture *ContainerJob) DockerCommand(t testing.TB, arguments ...string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, fixture.Docker, arguments...).CombinedOutput()
	if err != nil {
		t.Fatal(fmt.Errorf("Docker command: %w: %s", err, strings.TrimSpace(string(output))))
	}
	return string(output)
}
