package checkrunner_test

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"owngit/internal/apiclient"
	"owngit/internal/state"
)

const ambiguousWarning = "ended without a confirmed result"

// breakJobCommands makes the job's captured commands unreadable, so the server
// commits a claim it cannot hand over.
func breakJobCommands(t *testing.T, fixture *runnerIntegrationFixture) {
	t.Helper()
	noErr(t, fixture.store.Exec(fixture.ctx, `UPDATE check_configurations SET checks_json='{' WHERE repository_id=?`, fixture.repository.ID))
}

// A claim the server committed but could not hand over is reported by the
// runner as a job that ran nothing, so it is not left to expire as ambiguous.
// The server's 503 is still the answer: the runner returns it and does not
// warn that the job's outcome is open.
func TestRunnerReportsACommittedClaimItCouldNotRunAsUnavailable(t *testing.T) {
	fixture := newRunnerIntegrationFixture(t, "echo never-run")
	httpServer, origin := fixture.startHTTPServer(nil)
	defer httpServer.Close()
	breakJobCommands(t, fixture)

	log := &runnerLog{}
	runner := fixture.runner(fixture.client(origin))
	runner.Logf = log.logf
	err := runner.Run(fixture.ctx)
	var problem *apiclient.Error
	if !errors.As(err, &problem) || problem.ResponseStatus != http.StatusServiceUnavailable {
		t.Fatalf("run err=%v", err)
	}
	reported := fixture.readJob()
	if reported.Status != state.CheckJobUnavailable || reported.AttemptID != "" || !strings.Contains(reported.Summary, "commands could not be read") {
		t.Fatalf("job after a claim without commands: status=%s attempt=%q summary=%q", reported.Status, reported.AttemptID, reported.Summary)
	}
	if log.count("reported configured-check job "+fixture.job.ID+" unavailable") != 1 || log.count(ambiguousWarning) != 0 {
		t.Fatalf("runner log: %q", log.snapshot())
	}
	// The recorded outcome is final; lease expiry does not turn it into an
	// ambiguous one.
	if _, err := fixture.store.ExpireCheckJobLeases(fixture.ctx, time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if after := fixture.readJob(); after.Status != state.CheckJobUnavailable {
		t.Fatalf("job after lease expiry: status=%s", after.Status)
	}
}

// After reporting such a job, a continuously running runner waits as it does
// for any other unavailable answer before it claims again.
func TestRunnerWaitsAfterReportingAClaimItCouldNotRun(t *testing.T) {
	fixture := newRunnerIntegrationFixture(t, "echo never-run")
	var mu sync.Mutex
	var answered, next time.Time
	claims := 0
	httpServer, origin := fixture.startHTTPServer(func(handler http.Handler) http.Handler {
		return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			isClaim := strings.HasSuffix(request.URL.Path, "/runner/claim")
			if isClaim {
				mu.Lock()
				claims++
				if claims == 2 {
					next = time.Now()
				}
				mu.Unlock()
			}
			handler.ServeHTTP(writer, request)
			if isClaim {
				mu.Lock()
				if claims == 1 {
					answered = time.Now()
				}
				mu.Unlock()
			}
		})
	})
	defer httpServer.Close()
	breakJobCommands(t, fixture)

	const wait = 400 * time.Millisecond
	log := &runnerLog{}
	runner := fixture.runner(fixture.client(origin))
	runner.Once = false
	runner.PollInterval = time.Hour
	runner.RetryInitial, runner.RetryMax = wait, wait
	runner.Logf = log.logf
	ctx, cancel := context.WithCancel(fixture.ctx)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- runner.Run(ctx) }()

	secondClaimSeen := func() bool {
		mu.Lock()
		defer mu.Unlock()
		return claims >= 2
	}
	deadline := time.Now().Add(30 * time.Second)
	for !secondClaimSeen() || log.count("OwnGit answered again") == 0 {
		select {
		case err := <-done:
			t.Fatalf("the runner stopped: %v", err)
		default:
		}
		if secondClaimSeen() {
			mu.Lock()
			gap := next.Sub(answered)
			mu.Unlock()
			// The retry delay is jittered between half and all of the wait.
			if gap < wait/2 {
				t.Fatalf("the runner claimed again after %v, before the retry wait", gap)
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("the runner did not claim again after waiting: %q", log.snapshot())
		}
		time.Sleep(20 * time.Millisecond)
	}
	cancel()
	<-done
	if fixture.readJob().Status != state.CheckJobUnavailable || log.count("OwnGit is unreachable or unavailable") != 1 || log.count(ambiguousWarning) != 0 {
		t.Fatalf("job status=%s runner log: %q", fixture.readJob().Status, log.snapshot())
	}
}

// When the report itself fails, the job's outcome stays open: the runner
// returns the error and its warning names the job.
func TestRunnerNamesAClaimItCouldNotReport(t *testing.T) {
	fixture := newRunnerIntegrationFixture(t, "echo never-run")
	httpServer, origin := fixture.startHTTPServer(nil)
	defer httpServer.Close()
	breakJobCommands(t, fixture)
	noErr(t, fixture.store.Exec(fixture.ctx, `CREATE TRIGGER refuse_unavailable BEFORE UPDATE OF status ON check_jobs WHEN NEW.status='unavailable' BEGIN SELECT RAISE(ABORT, 'injected failure'); END`))

	log := &runnerLog{}
	runner := fixture.runner(fixture.client(origin))
	runner.Logf = log.logf
	if err := runner.Run(fixture.ctx); err == nil {
		t.Fatal("the runner reported success although the job outcome was not recorded")
	}
	if job := fixture.readJob(); job.Status != state.CheckJobClaimed {
		t.Fatalf("job after a failed report: status=%s", job.Status)
	}
	if log.count("configured-check job "+fixture.job.ID+" "+ambiguousWarning) != 1 {
		t.Fatalf("the runner did not name the job: %q", log.snapshot())
	}
}
