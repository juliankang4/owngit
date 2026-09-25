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
