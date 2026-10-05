//go:build !windows

package checkrun

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"owngit/internal/checkworkflow"
	"owngit/internal/state"
)

// This file covers admission while a local job runs. It holds the job with a
// FIFO, which sh and the file system give every supported Unix; Windows would
// need a different hold and skips the coverage.

// TestPushesMadeWhileALocalJobRunsGetJobsDuringTheRun checks the point of the
// push event path: two pushes made while a local job is still running each get
// their own job during that run, not only after it ends.
func TestPushesMadeWhileALocalJobRunsGetJobsDuringTheRun(t *testing.T) {
	fixture := newPushFixture(t, 8)
	fixture.useHostExecutor(t, 8)
	release, holding := holdCheck(t)
	first := fixture.pushWorkflow("main", holding)
	fixture.startCoordinator(t)

	// The first job runs the holding check, so the branch moves away from the
	// revision it is running while it is still started.
	waitForJobStatus(t, fixture, first, state.CheckJobStarted)
	second := fixture.pushWorkflow("main", validWorkflow)
	third := fixture.pushWorkflow("main", validWorkflow)
	fixture.coordinator.NotePush(fixture.repositoryID, []PushUpdate{{Ref: "refs/heads/main", New: second}})
	fixture.coordinator.NotePush(fixture.repositoryID, []PushUpdate{{Ref: "refs/heads/main", New: third}})

	// Both jobs must exist while the running job is still running.
	deadline := time.Now().Add(20 * time.Second)
	for {
		jobs := jobsByOID(t, fixture)
		if len(jobs) == 3 {
			if jobs[first].Status != state.CheckJobStarted {
				t.Fatalf("the first job was %s when the pushes made during it got their jobs", jobs[first].Status)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("pushes made during a running job got no job of their own: %+v", jobs)
		}
		time.Sleep(5 * time.Millisecond)
	}
	release()
	// The held job ends and the later pushes run too, oldest first.
	deadline = time.Now().Add(60 * time.Second)
	for {
		jobs := jobsByOID(t, fixture)
		finished := 0
		for _, job := range jobs {
			switch job.Status {
			case state.CheckJobPassed, state.CheckJobFailed, state.CheckJobError:
				finished++
			}
		}
		if finished == 3 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("not every job finished: %+v", jobs)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// useHostExecutor saves and consents to a policy whose jobs this computer runs,
// so the coordinator loop executes them.
func (fixture *pushFixture) useHostExecutor(t *testing.T, queueLimit int) {
	t.Helper()
	now := time.Now().UTC()
	if _, err := fixture.store.SetCheckPolicy(fixture.ctx, state.CheckPolicyInput{
		RepositoryID: fixture.repositoryID, Executor: state.CheckExecutorHost,
		AllowedEvents: []string{checkworkflow.EventPush}, MaxTimeoutMS: 60_000, MaxOutputLimitBytes: 64 << 10,
		QueueLimit: queueLimit, MaxActiveJobs: 1, MaxLeaseMS: 60_000,
	}, now); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.store.GrantCheckConsent(fixture.ctx, fixture.repositoryID, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
}

// startCoordinator starts the coordinator's loops and stops them before the
// test ends.
func (fixture *pushFixture) startCoordinator(t *testing.T) {
	t.Helper()
	ctx, cancel := context.WithCancel(fixture.ctx)
	started := make(chan error, 1)
	go func() { started <- fixture.coordinator.Start(ctx) }()
	select {
	case err := <-started:
		noErr(t, err)
	case <-time.After(30 * time.Second):
		t.Fatal("the coordinator did not start")
	}
	t.Cleanup(func() {
		stopContext, stopCancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer stopCancel()
		if err := fixture.coordinator.Stop(stopContext); err != nil {
			t.Error(err)
		}
		cancel()
	})
}

// holdCheck gives a check that runs until the returned release is called. The
// command blocks in cat on a FIFO, and release opens it for writing.
func holdCheck(t *testing.T) (release func(), workflow string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "hold")
	if err := unix.Mkfifo(path, 0o600); err != nil {
		t.Fatalf("create the holding FIFO: %v", err)
	}
	release = func() {
		deadline := time.Now().Add(30 * time.Second)
		for {
			writer, err := os.OpenFile(path, os.O_WRONLY|unix.O_NONBLOCK, 0)
			if err == nil {
				writer.Close()
				return
			}
			if time.Now().After(deadline) {
				t.Fatalf("the running job did not open the holding FIFO: %v", err)
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	workflow = fmt.Sprintf(`{"version":1,"events":{"push":{}},"checks":[{"name":"hold","command":"cat '%s'"}]}`, path)
	return release, workflow
}

// jobsByOID are the repository's jobs, keyed by the revision they run on.
func jobsByOID(t *testing.T, fixture *pushFixture) map[string]state.CheckJob {
	t.Helper()
	jobs, err := fixture.store.LatestCheckJobs(fixture.ctx, fixture.repositoryID, 20)
	noErr(t, err)
	result := make(map[string]state.CheckJob, len(jobs))
	for _, job := range jobs {
		result[job.SourceOID] = job
	}
	return result
}

// waitForJobStatus waits until one revision's job reaches the status.
func waitForJobStatus(t *testing.T, fixture *pushFixture, oid, status string) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		jobs := jobsByOID(t, fixture)
		if job, exists := jobs[oid]; exists && job.Status == status {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("the job of %s never reached %s: %+v", oid, status, jobs)
		}
		time.Sleep(5 * time.Millisecond)
	}
}
