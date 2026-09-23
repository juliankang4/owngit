package checkrunner_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"owngit/internal/checkrun"
	"owngit/internal/checkworkflow"
	"owngit/internal/gitexec"
	"owngit/internal/repository"
	"owngit/internal/state"
)

const (
	realDockerEnableEnv = "OWNGIT_REAL_DOCKER_TEST"
	realDockerImageEnv  = "OWNGIT_DOCKER_IMAGE"
	realDockerTimeout   = 75 * time.Second
	testContainerMemory = int64(128 << 20)
	testContainerCPU    = int64(500)
	testContainerPIDs   = int64(64)
	testContainerTmpfs  = int64(32 << 20)
)

var immutableDockerImage = regexp.MustCompile(`^(sha256:[0-9a-f]{64}|[^\x00\r\n\t ]+@sha256:[0-9a-f]{64})$`)

type realDockerConfig struct {
	docker     string
	dockerHost string
	image      string
}

type realDockerFixture struct {
	t           *testing.T
	config      realDockerConfig
	ctx         context.Context
	cancel      context.CancelFunc
	root        string
	store       *state.Store
	repository  state.Repository
	manager     *repository.Manager
	coordinator *checkrun.Coordinator
	stopped     bool
}

type dockerInspection struct {
	Config struct {
		User   string            `json:"User"`
		Labels map[string]string `json:"Labels"`
	} `json:"Config"`
	HostConfig struct {
		ReadonlyRootfs bool              `json:"ReadonlyRootfs"`
		CapDrop        []string          `json:"CapDrop"`
		SecurityOpt    []string          `json:"SecurityOpt"`
		NetworkMode    string            `json:"NetworkMode"`
		Memory         int64             `json:"Memory"`
		MemorySwap     int64             `json:"MemorySwap"`
		NanoCPUs       int64             `json:"NanoCpus"`
		PidsLimit      int64             `json:"PidsLimit"`
		Tmpfs          map[string]string `json:"Tmpfs"`
		LogConfig      struct {
			Type string `json:"Type"`
		} `json:"LogConfig"`
	} `json:"HostConfig"`
	Mounts []struct {
		Type        string `json:"Type"`
		Source      string `json:"Source"`
		Destination string `json:"Destination"`
		RW          bool   `json:"RW"`
	} `json:"Mounts"`
	State struct {
		Running bool `json:"Running"`
	} `json:"State"`
}

func TestRealDockerConfiguredChecks(t *testing.T) {
	config := requireRealDocker(t)
	for _, network := range []string{state.ContainerNetworkNone, state.ContainerNetworkBridge} {
		t.Run("successful_"+network, func(t *testing.T) {
			command := `set -eux
	test "$(id -u)" -ne 0
	grep -q '^exact source$' source.txt
	grep -Eq '^NoNewPrivs:[[:space:]]*1$' /proc/self/status
	grep -Eq '^CapEff:[[:space:]]*0+$' /proc/self/status
	grep -Eq '^CapPrm:[[:space:]]*0+$' /proc/self/status
	grep -Eq '^CapBnd:[[:space:]]*0+$' /proc/self/status
	root_readonly=
	while read -r device mountpoint filesystem options remainder; do
		if [ "$mountpoint" = / ]; then
			case ",$options," in *,ro,*) root_readonly=1 ;; esac
			break
		fi
	done < /proc/mounts
	test "$root_readonly" = 1
	if touch /owngit-rootfs-write-probe 2>/dev/null; then echo writable-root; exit 91; fi
	printf '#!/bin/sh\nprintf "tmp-executable\\n"\n' > /tmp/owngit-probe
	chmod 700 /tmp/owngit-probe
	printf 'ready\n' > .owngit-held
	while [ -f .owngit-held ]; do sleep 1; done
	test "$(/tmp/owngit-probe)" = tmp-executable
	printf 'exact-source container-result tmp-executable\n'`
			fixture := newRealDockerFixture(t, config, network, command)
			job, ownership := fixture.waitForRunningContainer()
			inspection := inspectOwnedContainer(t, config, ownership.ContainerID)
			assertContainerRestrictions(t, inspection, fixture.sourcePath(job.ID), job.ID, network)
			assertActualTmpfsMount(t, config, ownership.ContainerID)
			assertActualCgroupLimits(t, config, ownership.ContainerID)
			if err := os.Remove(filepath.Join(fixture.sourcePath(job.ID), ".owngit-held")); err != nil {
				t.Fatal(err)
			}
			completed := fixture.waitForTerminalJob(job.ID)
			if completed.Status != state.CheckJobPassed || completed.AttemptID == "" {
				t.Fatalf("container job status=%s attempt=%s summary=%s", completed.Status, completed.AttemptID, completed.Summary)
			}
			attempt, exists, err := fixture.store.CheckAttemptByID(fixture.ctx, fixture.repository.ID, completed.AttemptID)
			if err != nil || !exists {
				t.Fatalf("attempt exists=%v err=%v", exists, err)
			}
			if attempt.Status != state.AttemptPassed || attempt.Protection != state.ProtectionContainer || attempt.RevisionOID != completed.SourceOID || attempt.SubmittedWorktreeState != state.WorktreeClean || len(attempt.Results) != 1 || !strings.Contains(attempt.Results[0].OutputExcerpt, "exact-source container-result tmp-executable") {
				t.Fatalf("container attempt=%+v", attempt)
			}
			fixture.assertContainerRemoved(ownership.ContainerID)
		})
	}

	t.Run("cancellation_cleans_owned_container", func(t *testing.T) {
		command := `set -eu
	grep -q '^exact source$' source.txt
	printf 'ready\n' > .owngit-held
	while :; do sleep 1; done`
		fixture := newRealDockerFixture(t, config, state.ContainerNetworkNone, command)
		job, ownership := fixture.waitForRunningContainer()
		if _, err := fixture.store.CancelCheckJob(fixture.ctx, fixture.repository.ID, job.ID, time.Now().UTC()); err != nil {
			t.Fatal(err)
		}
		completed := fixture.waitForTerminalJob(job.ID)
		if completed.Status != state.CheckJobCancelled || completed.AttemptID == "" {
			t.Fatalf("cancelled job status=%s attempt=%s summary=%s", completed.Status, completed.AttemptID, completed.Summary)
		}
		attempt, exists, err := fixture.store.CheckAttemptByID(fixture.ctx, fixture.repository.ID, completed.AttemptID)
		if err != nil || !exists || attempt.Status != state.AttemptCancelled || attempt.Protection != state.ProtectionContainer {
			t.Fatalf("cancelled attempt=%+v exists=%v err=%v", attempt, exists, err)
		}
		fixture.assertContainerRemoved(ownership.ContainerID)
	})

	t.Run("missing_image_has_no_pull_or_fallback", func(t *testing.T) {
		missing := "sha256:" + strings.Repeat("0", 64)
		if _, err := dockerCommand(context.Background(), config, "image", "inspect", missing); err == nil {
			t.Fatalf("missing-image fixture unexpectedly exists: %s", missing)
		}
		root := t.TempDir()
		sentinel := filepath.Join(root, "host-fallback-ran")
		command := "printf fallback > " + shellQuoteDockerTest(sentinel)
		fixture := newRealDockerFixture(t, realDockerConfig{docker: config.docker, dockerHost: config.dockerHost, image: missing}, state.ContainerNetworkNone, command)
		job := fixture.waitForAnyJob()
		completed := fixture.waitForTerminalJob(job.ID)
		if completed.Status != state.CheckJobUnavailable || completed.AttemptID != "" {
			t.Fatalf("missing-image job status=%s attempt=%s summary=%s", completed.Status, completed.AttemptID, completed.Summary)
		}
		if _, err := os.Stat(sentinel); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("another executor ran the missing-image command: %v", err)
		}
		if _, err := dockerCommand(context.Background(), config, "image", "inspect", missing); err == nil {
			t.Fatal("missing immutable image appeared despite no-pull policy")
		}
		ownership, err := fixture.store.ActiveCheckContainers(fixture.ctx, 64)
		if err != nil || len(ownership) != 0 {
			t.Fatalf("missing-image ownership=%+v err=%v", ownership, err)
		}
	})
}

func requireRealDocker(t *testing.T) realDockerConfig {
	t.Helper()
	if os.Getenv(realDockerEnableEnv) != "1" {
		t.Skipf("set %s=1 with %s and a shared absolute TMPDIR to run real Docker integration", realDockerEnableEnv, realDockerImageEnv)
	}
	if os.Geteuid() == 0 {
		t.Fatal("real Docker integration requires a trusted nonroot controller; root fallback is forbidden")
	}
	for _, name := range []string{"DOCKER_HOST", "DOCKER_CONTEXT", "DOCKER_TLS_VERIFY", "DOCKER_CERT_PATH"} {
		if os.Getenv(name) != "" {
			t.Fatalf("%s must be unset; the product accepts only its local default Docker context", name)
		}
	}
	if value := os.Getenv("TMPDIR"); value == "" || !filepath.IsAbs(value) || filepath.Clean(value) != value {
		t.Fatal("TMPDIR must name the absolute host path shared unchanged with the local Docker daemon")
	} else if info, err := os.Stat(value); err != nil || !info.IsDir() {
		t.Fatalf("TMPDIR is unavailable: %v", err)
	}
	docker, err := exec.LookPath("docker")
	if err != nil {
		t.Fatal("docker CLI is required and must already be installed")
	}
	image := os.Getenv(realDockerImageEnv)
	if !immutableDockerImage.MatchString(image) {
		t.Fatalf("%s must be a cached immutable sha256 image ID or repository digest", realDockerImageEnv)
	}
	contextName, err := dockerCommandPath(context.Background(), docker, "", "context", "show")
	if err != nil || strings.TrimSpace(contextName) != "default" {
		t.Fatalf("Docker default context must be selected: context=%q err=%v", strings.TrimSpace(contextName), err)
	}
	dockerHost, err := dockerCommandPath(context.Background(), docker, "", "context", "inspect", "default", "--format", `{{(index .Endpoints "docker").Host}}`)
	if err != nil {
		t.Fatalf("inspect Docker default context: %v", err)
	}
	dockerHost = strings.TrimSpace(dockerHost)
	if !strings.HasPrefix(dockerHost, "unix://") {
		t.Fatalf("Docker default context is not a local Unix endpoint: %q", dockerHost)
	}
	config := realDockerConfig{docker: docker, dockerHost: dockerHost, image: image}
	serverOS, err := dockerCommand(context.Background(), config, "version", "--format", `{{.Server.Os}}`)
	if err != nil || strings.TrimSpace(serverOS) != "linux" {
		t.Fatalf("reachable local Linux Docker daemon required: os=%q err=%v", strings.TrimSpace(serverOS), err)
	}
	if _, err := dockerCommand(context.Background(), config, "image", "inspect", image); err != nil {
		t.Fatalf("approved immutable image must already be cached; this test never pulls: %v", err)
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Fatal("Git is required and must already be installed")
	}
	return config
}

func newRealDockerFixture(t *testing.T, config realDockerConfig, network, command string) *realDockerFixture {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), realDockerTimeout)
	fixture := &realDockerFixture{t: t, config: config, ctx: ctx, cancel: cancel, root: t.TempDir()}
	t.Cleanup(fixture.close)
	store, err := state.Open(ctx, filepath.Join(fixture.root, "state"))
	if err != nil {
		t.Fatal(err)
	}
	fixture.store = store
	git, err := gitexec.New("", filepath.Join(fixture.root, "runtime"))
	if err != nil {
		t.Fatal(err)
	}
	repositoryRoot := filepath.Join(fixture.root, "repositories")
	if err := os.Mkdir(repositoryRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := store.CompleteSetup(ctx, repositoryRoot, "open", "", "test-admin-hash", true); err != nil {
		t.Fatal(err)
	}
	fixture.manager = &repository.Manager{Store: store, Git: git, Locks: gitexec.NewLocks(), Root: repositoryRoot}
	stored, err := fixture.manager.Create(ctx, "docker-checks", "")
	if err != nil {
		t.Fatal(err)
	}
	fixture.repository = stored
	bare, err := fixture.manager.Path(stored.ID)
	if err != nil {
		t.Fatal(err)
	}
	work := filepath.Join(fixture.root, "source")
	runGit(t, "init", "--initial-branch=main", work)
	runGit(t, "-C", work, "config", "user.name", "OwnGit Docker Test")
	runGit(t, "-C", work, "config", "user.email", "docker-test@example.invalid")
	workflow, err := json.Marshal(map[string]any{
		"version": 1,
		"events":  map[string]any{"push": map[string]any{}},
		"checks":  []map[string]string{{"name": "real-docker", "command": "/bin/sh .owngit/docker-test.sh"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := checkworkflow.Parse(workflow); err != nil {
		t.Fatalf("parse generated Docker workflow: %v", err)
	}
	if err := os.Mkdir(filepath.Join(work, ".owngit"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(work, ".owngit", "docker-test.sh"), []byte("#!/bin/sh\n"+command+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(work, filepath.FromSlash(checkworkflow.Path)), workflow, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(work, "source.txt"), []byte("exact source\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, "-C", work, "add", ".")
	runGit(t, "-C", work, "commit", "-m", "real Docker integration fixture")
	runGit(t, "-C", work, "push", bare, "HEAD:refs/heads/main")
	if _, err := store.SetCheckPolicy(ctx, state.CheckPolicyInput{
		RepositoryID: stored.ID, Executor: state.CheckExecutorContainer,
		AllowedEvents: []string{checkworkflow.EventPush}, MaxTimeoutMS: 60_000, MaxOutputLimitBytes: 64 << 10,
		QueueLimit: 4, MaxActiveJobs: 1, MaxLeaseMS: 60_000,
		Execution: state.CheckExecutionSettings{
			ContainerImage: config.image, ContainerRuntime: "docker-local", ContainerNetwork: network,
			ContainerCPUMillis: testContainerCPU, ContainerMemoryBytes: testContainerMemory,
			ContainerPIDs: testContainerPIDs, ContainerScratchBytes: testContainerTmpfs,
		},
	}, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if _, err := store.GrantCheckConsent(ctx, stored.ID, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	fixture.coordinator = &checkrun.Coordinator{
		Store: store, Repositories: fixture.manager,
		WorkspaceRoot: filepath.Join(fixture.root, "check-workspaces"), DockerPath: config.docker,
		Interval: 20 * time.Millisecond,
	}
	if err := fixture.coordinator.Start(ctx); err != nil {
		t.Fatal(err)
	}
	return fixture
}

func (fixture *realDockerFixture) close() {
	if fixture == nil || fixture.stopped {
		return
	}
	fixture.stopped = true
	fixture.cancel()
	if fixture.coordinator != nil {
		stop, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		if err := fixture.coordinator.Stop(stop); err != nil {
			fixture.t.Errorf("stop Docker coordinator: %v", err)
		}
		cancel()
	}
	if fixture.store != nil {
		fixture.removeOnlyProvenContainers()
		if err := fixture.store.Close(); err != nil {
			fixture.t.Errorf("close Docker fixture state: %v", err)
		}
	}
}

func (fixture *realDockerFixture) removeOnlyProvenContainers() {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	ownership, err := fixture.store.ActiveCheckContainers(ctx, 100)
	if err != nil {
		fixture.t.Errorf("list task-owned containers: %v", err)
		return
	}
	for _, owned := range ownership {
		if owned.ContainerID == "" || owned.RepositoryID != fixture.repository.ID {
			continue
		}
		label, err := dockerCommand(ctx, fixture.config, "inspect", "--format", `{{index .Config.Labels "com.owngit.check-job"}}`, owned.ContainerID)
		if err != nil || strings.TrimSpace(label) != owned.JobID {
			fixture.t.Errorf("refuse cleanup without matching ownership for %s: label=%q err=%v", owned.ContainerID, strings.TrimSpace(label), err)
			continue
		}
		if _, err := dockerCommand(ctx, fixture.config, "rm", "--force", owned.ContainerID); err != nil {
			fixture.t.Errorf("remove proven task-owned container %s: %v", owned.ContainerID, err)
		}
	}
}

func (fixture *realDockerFixture) sourcePath(jobID string) string {
	return filepath.Join(fixture.coordinator.WorkspaceRoot, jobID, "source")
}

func (fixture *realDockerFixture) waitForAnyJob() state.CheckJob {
	fixture.t.Helper()
	for {
		jobs, err := fixture.store.CheckJobs(fixture.ctx, fixture.repository.ID)
		if err != nil {
			fixture.t.Fatal(err)
		}
		if len(jobs) > 1 {
			fixture.t.Fatalf("automatic admission created %d jobs", len(jobs))
		}
		if len(jobs) == 1 {
			return jobs[0]
		}
		fixture.waitTick("automatic Docker job was not admitted")
	}
}

func (fixture *realDockerFixture) waitForRunningContainer() (state.CheckJob, state.CheckContainerOwnership) {
	fixture.t.Helper()
	job := fixture.waitForAnyJob()
	for {
		stored, exists, err := fixture.store.CheckJob(fixture.ctx, fixture.repository.ID, job.ID)
		if err != nil || !exists {
			fixture.t.Fatalf("read running job exists=%v err=%v", exists, err)
		}
		if stored.Status != state.CheckJobPending && stored.Status != state.CheckJobClaimed && stored.Status != state.CheckJobStarted {
			fixture.t.Fatalf("job finished before held inspection: status=%s summary=%s diagnostic=%s", stored.Status, stored.Summary, fixture.attemptDiagnostic(stored))
		}
		if _, err := os.Stat(filepath.Join(fixture.sourcePath(job.ID), ".owngit-held")); err == nil {
			ownership, err := fixture.store.ActiveCheckContainers(fixture.ctx, 64)
			if err != nil {
				fixture.t.Fatal(err)
			}
			for _, owned := range ownership {
				if owned.JobID == job.ID && owned.RepositoryID == fixture.repository.ID && owned.ContainerID != "" {
					inspection := inspectOwnedContainer(fixture.t, fixture.config, owned.ContainerID)
					if inspection.State.Running {
						return stored, owned
					}
				}
			}
		}
		fixture.waitTick("owned Docker container did not reach the held running boundary")
	}
}

func (fixture *realDockerFixture) attemptDiagnostic(job state.CheckJob) string {
	if job.AttemptID == "" {
		return "attempt was not registered"
	}
	attempt, exists, err := fixture.store.CheckAttemptByID(fixture.ctx, fixture.repository.ID, job.AttemptID)
	if err != nil || !exists {
		return fmt.Sprintf("attempt %s exists=%v error=%v", job.AttemptID, exists, err)
	}
	parts := make([]string, 0, len(attempt.Results))
	for _, result := range attempt.Results {
		exitCode := "none"
		if result.ExitCode != nil {
			exitCode = strconv.Itoa(*result.ExitCode)
		}
		parts = append(parts, fmt.Sprintf("name=%q status=%s exit=%s output=%q cleanup=%q",
			result.Name, result.Status, exitCode, boundedDockerDiagnostic(result.OutputExcerpt), boundedDockerDiagnostic(result.CleanupError)))
	}
	return fmt.Sprintf("attempt_status=%s results=[%s]", attempt.Status, strings.Join(parts, "; "))
}

func boundedDockerDiagnostic(value string) string {
	const limit = 4096
	if len(value) <= limit {
		return value
	}
	return value[:limit] + "...[truncated]"
}

func (fixture *realDockerFixture) waitForTerminalJob(jobID string) state.CheckJob {
	fixture.t.Helper()
	for {
		job, exists, err := fixture.store.CheckJob(fixture.ctx, fixture.repository.ID, jobID)
		if err != nil || !exists {
			fixture.t.Fatalf("read terminal job exists=%v err=%v", exists, err)
		}
		switch job.Status {
		case state.CheckJobPending, state.CheckJobClaimed, state.CheckJobStarted:
			fixture.waitTick("Docker job did not finish")
		default:
			return job
		}
	}
}

func (fixture *realDockerFixture) waitTick(message string) {
	fixture.t.Helper()
	select {
	case <-fixture.ctx.Done():
		fixture.t.Fatal(message + ": " + fixture.ctx.Err().Error())
	case <-time.After(25 * time.Millisecond):
	}
}

func (fixture *realDockerFixture) assertContainerRemoved(containerID string) {
	fixture.t.Helper()
	for {
		if _, err := dockerCommand(fixture.ctx, fixture.config, "inspect", containerID); err != nil {
			ownership, listErr := fixture.store.ActiveCheckContainers(fixture.ctx, 64)
			if listErr != nil {
				fixture.t.Fatal(listErr)
			}
			for _, owned := range ownership {
				if owned.ContainerID == containerID {
					fixture.t.Fatalf("removed container still has ownership state: %+v", owned)
				}
			}
			return
		}
		fixture.waitTick("task-owned container was not removed")
	}
}

func inspectOwnedContainer(t *testing.T, config realDockerConfig, containerID string) dockerInspection {
	t.Helper()
	content, err := dockerCommand(context.Background(), config, "inspect", containerID)
	if err != nil {
		t.Fatal(err)
	}
	var records []dockerInspection
	if err := json.Unmarshal([]byte(content), &records); err != nil || len(records) != 1 {
		t.Fatalf("decode Docker inspection records=%d err=%v", len(records), err)
	}
	return records[0]
}

func assertContainerRestrictions(t *testing.T, inspection dockerInspection, workspace, jobID, network string) {
	t.Helper()
	expectedUser := fmt.Sprintf("%d:%d", os.Geteuid(), os.Getegid())
	if !inspection.State.Running || inspection.Config.User != expectedUser || inspection.Config.User == "0" || inspection.Config.User == "0:0" {
		t.Fatalf("container running=%v user=%q want=%q", inspection.State.Running, inspection.Config.User, expectedUser)
	}
	if inspection.Config.Labels["com.owngit.check-job"] != jobID {
		t.Fatalf("container ownership label=%q", inspection.Config.Labels["com.owngit.check-job"])
	}
	if !inspection.HostConfig.ReadonlyRootfs || inspection.HostConfig.LogConfig.Type != "none" || inspection.HostConfig.NetworkMode != network {
		t.Fatalf("root/log/network restrictions: readonly=%v log=%q network=%q", inspection.HostConfig.ReadonlyRootfs, inspection.HostConfig.LogConfig.Type, inspection.HostConfig.NetworkMode)
	}
	if len(inspection.HostConfig.CapDrop) != 1 || !strings.EqualFold(inspection.HostConfig.CapDrop[0], "ALL") {
		t.Fatalf("capability drop=%v", inspection.HostConfig.CapDrop)
	}
	foundNoNewPrivileges := false
	for _, option := range inspection.HostConfig.SecurityOpt {
		switch strings.ToLower(option) {
		case "no-new-privileges=true", "no-new-privileges:true":
			foundNoNewPrivileges = true
		}
	}
	if !foundNoNewPrivileges {
		t.Fatalf("security options do not explicitly enable no-new-privileges: %v", inspection.HostConfig.SecurityOpt)
	}
	if inspection.HostConfig.Memory != testContainerMemory || inspection.HostConfig.MemorySwap != testContainerMemory || inspection.HostConfig.NanoCPUs != testContainerCPU*1_000_000 || inspection.HostConfig.PidsLimit != testContainerPIDs {
		t.Fatalf("resource config memory=%d swap=%d nano_cpus=%d pids=%d", inspection.HostConfig.Memory, inspection.HostConfig.MemorySwap, inspection.HostConfig.NanoCPUs, inspection.HostConfig.PidsLimit)
	}
	tmpfs := inspection.HostConfig.Tmpfs["/tmp"]
	if !strings.Contains(tmpfs, "size="+strconv.FormatInt(testContainerTmpfs, 10)) || !hasMountOption(tmpfs, "exec") || hasMountOption(tmpfs, "noexec") {
		t.Fatalf("tmpfs /tmp=%q", tmpfs)
	}
	workspace, err := filepath.EvalSymlinks(workspace)
	if err != nil {
		t.Fatal(err)
	}
	binds, temporaryFilesystems := 0, 0
	for _, mount := range inspection.Mounts {
		switch mount.Type {
		case "bind":
			binds++
			source, err := filepath.EvalSymlinks(mount.Source)
			if err != nil {
				t.Fatal(err)
			}
			if source != workspace || mount.Destination != "/workspace" || !mount.RW {
				t.Fatalf("source mount=%+v want source=%s", mount, workspace)
			}
		case "tmpfs":
			temporaryFilesystems++
			if mount.Destination != "/tmp" || !mount.RW {
				t.Fatalf("unexpected tmpfs mount=%+v", mount)
			}
		default:
			t.Fatalf("unexpected mount type or image volume=%+v", mount)
		}
	}
	if binds != 1 || temporaryFilesystems > 1 {
		t.Fatalf("mount counts bind=%d tmpfs=%d mounts=%+v", binds, temporaryFilesystems, inspection.Mounts)
	}
}

func assertActualTmpfsMount(t *testing.T, config realDockerConfig, containerID string) {
	t.Helper()
	script := `set -eu
while read -r device mountpoint filesystem options remainder; do
 if [ "$mountpoint" = /tmp ]; then
  echo filesystem="$filesystem"
  echo options="$options"
  exit 0
 fi
done < /proc/mounts
exit 1`
	output, err := dockerCommand(context.Background(), config, "exec", containerID, "/bin/sh", "-c", script)
	if err != nil {
		t.Fatalf("inspect actual /tmp mount: %v", err)
	}
	values := make(map[string]string)
	for _, line := range strings.Split(strings.TrimSpace(output), "\n") {
		parts := strings.SplitN(line, "=", 2)
		if len(parts) == 2 {
			values[parts[0]] = parts[1]
		}
	}
	options := values["options"]
	if values["filesystem"] != "tmpfs" || !hasMountOption(options, "rw") || !hasMountOption(options, "nosuid") || !hasMountOption(options, "nodev") || hasMountOption(options, "noexec") {
		t.Fatalf("actual /tmp mount=%v", values)
	}
}

func hasMountOption(options, expected string) bool {
	for _, option := range strings.Split(options, ",") {
		if option == expected {
			return true
		}
	}
	return false
}

func assertActualCgroupLimits(t *testing.T, config realDockerConfig, containerID string) {
	t.Helper()
	script := `set -eu
if [ -f /sys/fs/cgroup/cgroup.controllers ]; then
 echo version=2
 echo memory=$(cat /sys/fs/cgroup/memory.max)
 echo swap=$(cat /sys/fs/cgroup/memory.swap.max)
 echo cpu=$(cat /sys/fs/cgroup/cpu.max)
 echo pids=$(cat /sys/fs/cgroup/pids.max)
else
 echo version=1
 for f in /sys/fs/cgroup/memory/memory.limit_in_bytes /sys/fs/cgroup/memory.limit_in_bytes; do [ ! -f "$f" ] || { echo memory=$(cat "$f"); break; }; done
 for f in /sys/fs/cgroup/memory/memory.memsw.limit_in_bytes /sys/fs/cgroup/memory.memsw.limit_in_bytes; do [ ! -f "$f" ] || { echo swap=$(cat "$f"); break; }; done
 for f in /sys/fs/cgroup/cpu/cpu.cfs_quota_us /sys/fs/cgroup/cpu.cfs_quota_us; do [ ! -f "$f" ] || { quota=$(cat "$f"); break; }; done
 for f in /sys/fs/cgroup/cpu/cpu.cfs_period_us /sys/fs/cgroup/cpu.cfs_period_us; do [ ! -f "$f" ] || { period=$(cat "$f"); break; }; done
 echo cpu="$quota $period"
 for f in /sys/fs/cgroup/pids/pids.max /sys/fs/cgroup/pids.max; do [ ! -f "$f" ] || { echo pids=$(cat "$f"); break; }; done
fi`
	output, err := dockerCommand(context.Background(), config, "exec", containerID, "/bin/sh", "-c", script)
	if err != nil {
		t.Fatalf("read actual container cgroups: %v", err)
	}
	values := make(map[string]string)
	for _, line := range strings.Split(strings.TrimSpace(output), "\n") {
		parts := strings.SplitN(strings.TrimSpace(line), "=", 2)
		if len(parts) == 2 {
			values[parts[0]] = parts[1]
		}
	}
	memory, memoryErr := strconv.ParseInt(values["memory"], 10, 64)
	pids, pidsErr := strconv.ParseInt(values["pids"], 10, 64)
	if memoryErr != nil || memory != testContainerMemory || pidsErr != nil || pids != testContainerPIDs {
		t.Fatalf("actual cgroups=%v memory_err=%v pids_err=%v", values, memoryErr, pidsErr)
	}
	cpu := strings.Fields(values["cpu"])
	if len(cpu) != 2 {
		t.Fatalf("actual CPU cgroup=%q", values["cpu"])
	}
	quota, quotaErr := strconv.ParseInt(cpu[0], 10, 64)
	period, periodErr := strconv.ParseInt(cpu[1], 10, 64)
	if quotaErr != nil || periodErr != nil || quota <= 0 || period <= 0 || quota*1000 != period*testContainerCPU {
		t.Fatalf("actual CPU cgroup=%q quota_err=%v period_err=%v", values["cpu"], quotaErr, periodErr)
	}
	switch values["version"] {
	case "2":
		if values["swap"] != "0" {
			t.Fatalf("cgroup v2 swap.max=%q, want 0", values["swap"])
		}
	case "1":
		swap, err := strconv.ParseInt(values["swap"], 10, 64)
		if err != nil || swap != testContainerMemory {
			t.Fatalf("cgroup v1 memory+swap=%q err=%v", values["swap"], err)
		}
	default:
		t.Fatalf("unknown cgroup result=%v", values)
	}
}

func dockerCommand(ctx context.Context, config realDockerConfig, arguments ...string) (string, error) {
	return dockerCommandPath(ctx, config.docker, config.dockerHost, arguments...)
}

func dockerCommandPath(ctx context.Context, docker, dockerHost string, arguments ...string) (string, error) {
	if _, bounded := ctx.Deadline(); !bounded {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, 15*time.Second)
		defer cancel()
	}
	if dockerHost != "" {
		arguments = append([]string{"--host", dockerHost}, arguments...)
	}
	command := exec.CommandContext(ctx, docker, arguments...)
	output, err := command.CombinedOutput()
	if err != nil {
		return string(output), fmt.Errorf("docker %s: %w: %s", strings.Join(arguments, " "), err, strings.TrimSpace(string(output)))
	}
	return string(output), nil
}

func shellQuoteDockerTest(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'"'"'`) + "'"
}
