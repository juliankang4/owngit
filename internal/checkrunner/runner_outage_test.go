package checkrunner_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"owngit/internal/apiclient"
	"owngit/internal/state"
)

type runnerLog struct {
	mu    sync.Mutex
	lines []string
}

func (log *runnerLog) logf(format string, arguments ...any) {
	log.mu.Lock()
	defer log.mu.Unlock()
	log.lines = append(log.lines, fmt.Sprintf(format, arguments...))
}

func (log *runnerLog) snapshot() []string {
	log.mu.Lock()
	defer log.mu.Unlock()
	return append([]string(nil), log.lines...)
}

func (log *runnerLog) count(fragment string) int {
	log.mu.Lock()
	defer log.mu.Unlock()
	count := 0
	for _, line := range log.lines {
		if strings.Contains(line, fragment) {
			count++
		}
	}
	return count
}

// While OwnGit restarts, claims get no answer or a 503. The runner used to
// exit on the first failed claim; it now retries, reports the outage and the
// recovery once each, and runs the job once OwnGit answers again.
func TestRunnerKeepsRunningAcrossAnOutage(t *testing.T) {
	fixture := newRunnerIntegrationFixture(t, "echo runner-ok")
	var claims atomic.Int32
	httpServer, origin := fixture.startHTTPServer(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			if strings.HasSuffix(request.URL.Path, "/runner/claim") {
				switch claims.Add(1) {
				case 1, 2:
					// No answer at all, as while the server is down.
					connection, _, err := http.NewResponseController(writer).Hijack()
					if err == nil {
						connection.Close()
					}
					return
				case 3:
					writer.Header().Set("Content-Type", "application/json")
					writer.Header().Set("Retry-After", "0")
					writer.WriteHeader(http.StatusServiceUnavailable)
					_, _ = writer.Write([]byte(`{"ok":false,"error":{"code":"state_unavailable","message":"starting"}}`))
					return
				}
			}
			next.ServeHTTP(writer, request)
		})
	})
	defer httpServer.Close()

	log := &runnerLog{}
	runner := fixture.runner(fixture.client(origin))
	runner.Once = false
	runner.PollInterval = 20 * time.Millisecond
	runner.RetryInitial, runner.RetryMax = 10*time.Millisecond, 40*time.Millisecond
	runner.Logf = log.logf
	ctx, cancel := context.WithCancel(fixture.ctx)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- runner.Run(ctx) }()

	deadline := time.Now().Add(30 * time.Second)
	for fixture.readJob().Status != state.CheckJobPassed {
		select {
		case err := <-done:
			t.Fatalf("the runner stopped during the outage: %v", err)
		default:
		}
		if time.Now().After(deadline) {
			t.Fatalf("job after the outage: %+v", fixture.readJob())
		}
		time.Sleep(20 * time.Millisecond)
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("runner stop err=%v", err)
	}
	if outages, recoveries := log.count("OwnGit is unreachable"), log.count("OwnGit answered again"); outages != 1 || recoveries != 1 {
		t.Fatalf("outage logged %d times and recovery %d times: %v", outages, recoveries, log.lines)
	}
}

// A revoked token cannot start working again, so the runner stops at once and
// says what to do instead of retrying forever.
func TestRunnerStopsWhenItsTokenIsRevoked(t *testing.T) {
	fixture := newRunnerIntegrationFixture(t, "echo runner-ok")
	httpServer, origin := fixture.startHTTPServer(nil)
	defer httpServer.Close()
	noErr(t, fixture.store.RevokeCheckRunnerToken(fixture.ctx, fixture.repository.ID, fixture.credential.ID, time.Now().UTC()))
	runner := fixture.runner(fixture.client(origin))
	runner.Once = false
	runner.RetryInitial, runner.RetryMax = 10*time.Millisecond, 40*time.Millisecond
	ctx, cancel := context.WithTimeout(fixture.ctx, 20*time.Second)
	defer cancel()
	err := runner.Run(ctx)
	var problem *apiclient.Error
	if !errors.As(err, &problem) || problem.Code != "invalid_runner_credential" || !strings.Contains(problem.Message, "runner-credential issue") {
		t.Fatalf("revoked runner err=%v", err)
	}
	if job := fixture.readJob(); job.Status != state.CheckJobPending {
		t.Fatalf("a revoked runner changed the job: %+v", job)
	}
}

// When the network drops after a job ran, the completion never arrives and
// OwnGit marks the job ambiguous once its lease expires. The runner used to
// log only the outage, so nothing named the job; it now logs one line with the
// job ID and the reason.
func TestRunnerLogsAJobThatEndedWithoutAConfirmedResult(t *testing.T) {
	fixture := newRunnerIntegrationFixture(t, "echo runner-ok")
	httpServer, origin := fixture.startHTTPServer(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			if strings.HasSuffix(request.URL.Path, "/renew") || strings.HasSuffix(request.URL.Path, "/complete") {
				// No answer, as when the link drops.
				connection, _, err := http.NewResponseController(writer).Hijack()
				if err == nil {
					connection.Close()
				}
				return
			}
			next.ServeHTTP(writer, request)
		})
	})
	defer httpServer.Close()

	log := &runnerLog{}
	runner := fixture.runner(fixture.client(origin))
	runner.Once = false
	runner.PollInterval = 20 * time.Millisecond
	runner.RetryInitial, runner.RetryMax = 10*time.Millisecond, 40*time.Millisecond
	runner.Logf = log.logf
	ctx, cancel := context.WithCancel(fixture.ctx)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- runner.Run(ctx) }()
	deadline := time.Now().Add(30 * time.Second)
	for log.count("OwnGit answered again") == 0 {
		select {
		case err := <-done:
			t.Fatalf("the runner stopped: %v", err)
		default:
		}
		if time.Now().After(deadline) {
			t.Fatalf("runner log: %v", log.snapshot())
		}
		time.Sleep(20 * time.Millisecond)
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("runner stop err=%v", err)
	}
	if job := fixture.readJob(); job.Status != state.CheckJobStarted {
		t.Fatalf("job without a completion: %+v", job)
	}
	named := 0
	for _, line := range log.snapshot() {
		if strings.Contains(line, fixture.job.ID) && strings.Contains(line, "without a confirmed result") && strings.Contains(line, "ambiguous") {
			named++
			if strings.Contains(line, fixture.token) || strings.Contains(line, fixture.readJob().LeaseID) {
				t.Fatalf("job line exposes a credential: %q", line)
			}
		}
	}
	if named != 1 {
		t.Fatalf("lines naming job %s as ambiguous: %d in %v", fixture.job.ID, named, log.snapshot())
	}
}

// A valid token presented for another repository, or for one that does not
// exist, is refused with its own code and a message that names no repository.
// An unknown or revoked token keeps the unknown-or-revoked answer, also for
// another repository.
func TestRunnerStopsWhenItsTokenBelongsToAnotherRepository(t *testing.T) {
	fixture := newRunnerIntegrationFixture(t, "echo runner-ok")
	other, err := fixture.manager.Create(fixture.ctx, "other-project", "")
	noErr(t, err)
	if _, err := fixture.store.SetCheckPolicy(fixture.ctx, state.CheckPolicyInput{
		RepositoryID: other.ID, Executor: state.CheckExecutorExternalRunner,
		AllowedEvents: []string{"push"}, MaxTimeoutMS: 60_000, MaxOutputLimitBytes: 64 << 10,
		QueueLimit: 4, MaxActiveJobs: 1, MaxLeaseMS: 60_000,
	}, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	httpServer, origin := fixture.startHTTPServer(nil)
	defer httpServer.Close()
	run := func(client *apiclient.Client, repositoryID string) *apiclient.Error {
		t.Helper()
		runner := fixture.runner(client)
		runner.RepositoryID = repositoryID
		runner.Once = false
		runner.RetryInitial, runner.RetryMax = 10*time.Millisecond, 40*time.Millisecond
		ctx, cancel := context.WithTimeout(fixture.ctx, 20*time.Second)
		defer cancel()
		var problem *apiclient.Error
		if err := runner.Run(ctx); !errors.As(err, &problem) {
			t.Fatalf("runner for %s err=%v", repositoryID, err)
		}
		return problem
	}
	for _, repositoryID := range []string{other.ID, "nosuchrepo"} {
		problem := run(fixture.client(origin), repositoryID)
		if problem.Code != "runner_credential_repository_mismatch" || problem.ResponseStatus != http.StatusForbidden ||
			!strings.Contains(problem.Message, "belongs to another repository") || strings.Contains(problem.Message, "unknown or revoked") {
			t.Fatalf("token for another repository: code=%s status=%d message=%q", problem.Code, problem.ResponseStatus, problem.Message)
		}
		for _, detail := range []string{fixture.repository.ID, fixture.repository.Name, fixture.credential.ID} {
			if strings.Contains(problem.Error(), detail) || strings.Contains(string(problem.Details), detail) {
				t.Fatalf("refusal names %q: %v %s", detail, problem, problem.Details)
			}
		}
	}

	unknown := fixture.token[:len(fixture.token)-1] + "0"
	if unknown == fixture.token {
		unknown = fixture.token[:len(fixture.token)-1] + "1"
	}
	unknownClient := apiclient.NewBearer(origin, unknown)
	for _, repositoryID := range []string{fixture.repository.ID, other.ID} {
		if problem := run(unknownClient, repositoryID); problem.Code != "invalid_runner_credential" || !strings.Contains(problem.Message, "unknown or revoked") {
			t.Fatalf("unknown token for %s: code=%s message=%q", repositoryID, problem.Code, problem.Message)
		}
	}
	noErr(t, fixture.store.RevokeCheckRunnerToken(fixture.ctx, fixture.repository.ID, fixture.credential.ID, time.Now().UTC()))
	for _, repositoryID := range []string{fixture.repository.ID, other.ID} {
		if problem := run(fixture.client(origin), repositoryID); problem.Code != "invalid_runner_credential" || !strings.Contains(problem.Message, "unknown or revoked") {
			t.Fatalf("revoked token for %s: code=%s message=%q", repositoryID, problem.Code, problem.Message)
		}
	}
	if job := fixture.readJob(); job.Status != state.CheckJobPending {
		t.Fatalf("a refused runner changed the job: %+v", job)
	}
}
