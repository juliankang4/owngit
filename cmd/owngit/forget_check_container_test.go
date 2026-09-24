package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"owngit/internal/checkrun"
	"owngit/internal/state"
)

// forgetFixture is a state directory with two finished (interrupted) check
// jobs, each holding a container cleanup record made on the daemon
// "old-daemon".
type forgetFixture struct {
	stateDir string
	jobs     [2]string
}

func newForgetFixture(t *testing.T) forgetFixture {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC()
	stateDir := filepath.Join(t.TempDir(), "state")
	store, err := state.Open(ctx, stateDir)
	noErr(t, err)
	defer store.Close()
	noErr(t, store.AddRepository(ctx, state.Repository{ID: "sample", Name: "sample", CreatedAt: now}))
	_, err = store.SetCheckPolicy(ctx, state.CheckPolicyInput{
		RepositoryID: "sample", Executor: state.CheckExecutorExternalRunner, AllowedEvents: []string{"push"},
		MaxTimeoutMS: 60000, MaxOutputLimitBytes: 65536, QueueLimit: 4, MaxActiveJobs: 1, MaxLeaseMS: 60000,
	}, now)
	noErr(t, err)
	_, err = store.GrantCheckConsent(ctx, "sample", now)
	noErr(t, err)
	fixture := forgetFixture{stateDir: stateDir}
	for index, oid := range []string{strings.Repeat("b", 40), strings.Repeat("c", 40)} {
		job, _, err := store.AdmitCheckJob(ctx, state.CheckJobRequest{
			RepositoryID: "sample", Trigger: "push", EventKey: "refs/heads/main@" + oid,
			SourceOID: oid, TriggerRef: "main", WorkflowDigest: strings.Repeat("d", 64),
			Checks: []state.CheckDefinition{{Name: "unit", Command: "true"}},
		}, now)
		noErr(t, err)
		fixture.jobs[index] = job.ID
		noErr(t, store.Exec(ctx, `UPDATE check_jobs SET status=? WHERE id=?`, state.CheckJobInterrupted, job.ID))
		noErr(t, store.Exec(ctx, `INSERT INTO check_job_runtime_ownership(job_id,repository_id,container_name,container_id,daemon_id,created_at) VALUES(?,?,?,?,?,1)`,
			job.ID, "sample", "owngit-check-"+job.ID, strings.Repeat(string(rune('e'+index)), 64), "old-daemon"))
	}
	return fixture
}

func (fixture forgetFixture) recorded(t *testing.T) map[string]bool {
	t.Helper()
	store, err := state.Open(context.Background(), fixture.stateDir)
	noErr(t, err)
	defer store.Close()
	records := map[string]bool{}
	for _, job := range fixture.jobs {
		_, exists, err := store.CheckContainerOwnershipForJob(context.Background(), job)
		noErr(t, err)
		records[job] = exists
	}
	return records
}

// fakeDocker puts a docker executable on PATH that reports daemonID, or fails
// like an unreachable daemon when daemonID is empty.
func fakeDocker(t *testing.T, daemonID string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the fake Docker fixture is a POSIX shell script")
	}
	for _, name := range []string{"DOCKER_HOST", "DOCKER_CONTEXT", "DOCKER_TLS_VERIFY", "DOCKER_CERT_PATH"} {
		t.Setenv(name, "")
	}
	script := "#!/bin/sh\necho 'Cannot connect to the Docker daemon' >&2\nexit 1\n"
	if daemonID != "" {
		script = `#!/bin/sh
case "$*" in
  "context inspect"*) echo unix:///var/run/docker.sock ;;
  *" version "*) echo linux ;;
  *" info "*) echo ` + daemonID + ` ;;
  *) echo "unexpected docker call: $*" >&2; exit 1 ;;
esac
`
	}
	directory := t.TempDir()
	noErr(t, os.WriteFile(filepath.Join(directory, "docker"), []byte(script), 0o700))
	t.Setenv("PATH", directory)
}

func TestForgetCheckContainerRequiresConfirmation(t *testing.T) {
	fixture := newForgetFixture(t)
	fakeDocker(t, "new-daemon")
	err := forgetCheckContainer([]string{"--state-dir", fixture.stateDir, "--job", fixture.jobs[0]})
	if err == nil || !strings.Contains(err.Error(), "--confirm-container-removed is required") {
		t.Fatalf("missing confirmation error=%v", err)
	}
	if records := fixture.recorded(t); !records[fixture.jobs[0]] || !records[fixture.jobs[1]] {
		t.Fatalf("an unconfirmed request changed records: %v", records)
	}
}

func TestForgetCheckContainerWithUnreachableDockerNeedsConfirmation(t *testing.T) {
	fixture := newForgetFixture(t)
	fakeDocker(t, "")
	if err := forgetCheckContainer([]string{"--state-dir", fixture.stateDir, "--job", fixture.jobs[0]}); err == nil {
		t.Fatal("unreachable Docker without confirmation was accepted")
	}
	if records := fixture.recorded(t); !records[fixture.jobs[0]] {
		t.Fatal("an unconfirmed request removed the record")
	}
	output, err := captureStdout(func() error {
		return forgetCheckContainer([]string{"--state-dir", fixture.stateDir, "--job", fixture.jobs[0], "--confirm-container-removed"})
	})
	noErr(t, err)
	if !strings.Contains(output, "Docker could not be checked") {
		t.Fatalf("output did not say Docker was unavailable: %q", output)
	}
	if records := fixture.recorded(t); records[fixture.jobs[0]] || !records[fixture.jobs[1]] {
		t.Fatalf("records after confirmed release: %v", records)
	}
}

// A job that is still unfinished may belong to a running server, which cleans
// up its own container. Its record is kept even when Docker cannot be checked
// from the invoking shell and the owner confirmed removal.
func TestForgetCheckContainerRefusesAnUnfinishedJob(t *testing.T) {
	for _, status := range []string{state.CheckJobPending, state.CheckJobClaimed, state.CheckJobStarted} {
		t.Run(status, func(t *testing.T) {
			fixture := newForgetFixture(t)
			fixture.setStatus(t, fixture.jobs[0], status)
			fakeDocker(t, "")
			err := forgetCheckContainer([]string{"--state-dir", fixture.stateDir, "--job", fixture.jobs[0], "--confirm-container-removed"})
			if !errors.Is(err, state.ErrCheckContainerJobActive) {
				t.Fatalf("unfinished job error=%v", err)
			}
			if records := fixture.recorded(t); !records[fixture.jobs[0]] || !records[fixture.jobs[1]] {
				t.Fatalf("an unfinished job lost its record: %v", records)
			}
		})
	}
}

func (fixture forgetFixture) setStatus(t *testing.T, job, status string) {
	t.Helper()
	store, err := state.Open(context.Background(), fixture.stateDir)
	noErr(t, err)
	defer store.Close()
	noErr(t, store.Exec(context.Background(), `UPDATE check_jobs SET status=? WHERE id=?`, status, job))
}

func TestForgetCheckContainerRefusesTheCurrentDaemon(t *testing.T) {
	fixture := newForgetFixture(t)
	fakeDocker(t, "old-daemon")
	err := forgetCheckContainer([]string{"--state-dir", fixture.stateDir, "--job", fixture.jobs[0], "--confirm-container-removed"})
	if !errors.Is(err, checkrun.ErrContainerOnCurrentDaemon) {
		t.Fatalf("current daemon error=%v", err)
	}
	if records := fixture.recorded(t); !records[fixture.jobs[0]] || !records[fixture.jobs[1]] {
		t.Fatalf("a refused request changed records: %v", records)
	}
}

func TestForgetCheckContainerReportsAJobWithoutARecord(t *testing.T) {
	fixture := newForgetFixture(t)
	fakeDocker(t, "new-daemon")
	err := forgetCheckContainer([]string{"--state-dir", fixture.stateDir, "--job", strings.Repeat("0", 32), "--confirm-container-removed"})
	if !errors.Is(err, checkrun.ErrNoContainerRecord) {
		t.Fatalf("missing record error=%v", err)
	}
	if records := fixture.recorded(t); !records[fixture.jobs[0]] || !records[fixture.jobs[1]] {
		t.Fatalf("a request for another job changed records: %v", records)
	}
}

func TestForgetCheckContainerRemovesOnlyThatRecord(t *testing.T) {
	fixture := newForgetFixture(t)
	fakeDocker(t, "new-daemon")
	output, err := captureStdout(func() error {
		return forgetCheckContainer([]string{"--state-dir", fixture.stateDir, "--job", fixture.jobs[0], "--confirm-container-removed"})
	})
	noErr(t, err)
	for _, want := range []string{
		"Container name: owngit-check-" + fixture.jobs[0],
		"Container ID: " + strings.Repeat("e", 64),
		"Docker daemon: old-daemon",
		"Label: com.owngit.check-job=" + fixture.jobs[0],
		"OwnGit removed no container",
	} {
		if !strings.Contains(output, want) {
			t.Errorf("output lacks %q: %q", want, output)
		}
	}
	if strings.Contains(output, "Docker could not be checked") {
		t.Errorf("reachable Docker was reported unavailable: %q", output)
	}
	if records := fixture.recorded(t); records[fixture.jobs[0]] || !records[fixture.jobs[1]] {
		t.Fatalf("records after release: %v", records)
	}
}
