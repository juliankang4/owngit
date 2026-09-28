package checkrunner_test

import (
	"errors"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"owngit/internal/apiclient"
	"owngit/internal/state"
)

// Between the claim and the start, the job's commands become unreadable or
// its attempt cannot be written. The server starts a job only together with
// its commands and its attempt, so the job stays claimed and the runner
// reports it as not run. It used to be started without an attempt and to end
// ambiguous.
func TestRunnerReportsAJobItCouldNotStartAsUnavailable(t *testing.T) {
	for _, test := range []struct {
		name, statement, summary string
	}{
		{"commands unreadable", `UPDATE check_configurations SET checks_json='{'`, "commands could not be read"},
		{"attempt not written", refuseAttempts, "job could not be started"},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newRunnerIntegrationFixture(t, "echo never-run")
			httpServer, origin := fixture.startHTTPServer(func(next http.Handler) http.Handler {
				return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
					if strings.HasSuffix(request.URL.Path, "/start") {
						noErr(t, fixture.store.Exec(fixture.ctx, test.statement))
					}
					next.ServeHTTP(writer, request)
				})
			})
			defer httpServer.Close()

			log := &runnerLog{}
			runner := fixture.runner(fixture.client(origin))
			runner.Logf = log.logf
			err := runner.Run(fixture.ctx)
			var problem *apiclient.Error
			if !errors.As(err, &problem) || problem.ResponseStatus != http.StatusServiceUnavailable {
				t.Fatalf("run err=%v", err)
			}
			if _, err := fixture.store.ExpireCheckJobLeases(fixture.ctx, time.Now().Add(time.Hour)); err != nil {
				t.Fatal(err)
			}
			job := fixture.readJob()
			if job.Status != state.CheckJobUnavailable || job.StartedAt != nil || job.AttemptID != "" || !strings.Contains(job.Summary, test.summary) {
				t.Fatalf("job after a refused start: status=%s started=%v attempt=%q summary=%q", job.Status, job.StartedAt, job.AttemptID, job.Summary)
			}
			if log.count("reported configured-check job "+fixture.job.ID+" unavailable") != 1 || log.count(ambiguousWarning) != 0 {
				t.Fatalf("runner log: %q", log.snapshot())
			}
		})
	}
}

// refuseAttempts makes every later attempt write fail, as a storage failure
// would.
const refuseAttempts = `CREATE TRIGGER refuse_attempts BEFORE INSERT ON check_attempts BEGIN SELECT RAISE(ABORT, 'synthetic attempt write failure'); END`

// A start error whose details name a different job settles nothing: the
// runner does not report that job, and the job it holds stays open under its
// own ID.
func TestRunnerIgnoresStartDetailsForAnotherJob(t *testing.T) {
	fixture := newRunnerIntegrationFixture(t, "echo never-run")
	other := strings.Repeat("d", 32)
	var reportedOther atomic.Bool
	httpServer, origin := fixture.startHTTPServer(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			switch {
			case strings.HasSuffix(request.URL.Path, "/start"):
				writer.Header().Set("Content-Type", "application/json")
				writer.WriteHeader(http.StatusServiceUnavailable)
				_, _ = writer.Write([]byte(`{"ok":false,"error":{"code":"state_unavailable","message":"The job's captured commands could not be read.","details":{"job_id":"` + other + `","lease_id":"` + other + `"}}}`))
				return
			case strings.Contains(request.URL.Path, "/jobs/"+other+"/"):
				reportedOther.Store(true)
			}
			next.ServeHTTP(writer, request)
		})
	})
	defer httpServer.Close()

	log := &runnerLog{}
	runner := fixture.runner(fixture.client(origin))
	runner.Logf = log.logf
	if err := runner.Run(fixture.ctx); err == nil {
		t.Fatal("the runner reported success")
	}
	if reportedOther.Load() || log.count("configured-check job "+fixture.job.ID+" "+ambiguousWarning) != 1 || log.count("reported configured-check job") != 0 {
		t.Fatalf("reported other=%v runner log: %q", reportedOther.Load(), log.snapshot())
	}
	if job := fixture.readJob(); job.Status != state.CheckJobClaimed {
		t.Fatalf("held job status=%s", job.Status)
	}
}
