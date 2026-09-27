package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"owngit/internal/apiclient"
	"owngit/internal/checkapi"
	"owngit/internal/state"
)

// runnerReadFixture is one repository with an external-runner policy, active
// consent, one runner token and one admitted job. The runner endpoints are
// served through a real HTTP client, so a test sees what a runner receives.
type runnerReadFixture struct {
	ctx    context.Context
	store  *state.Store
	app    *App
	job    state.CheckJob
	client *apiclient.Client
}

func newRunnerReadFixture(t *testing.T) *runnerReadFixture {
	t.Helper()
	ctx := context.Background()
	store, err := state.Open(ctx, filepath.Join(t.TempDir(), "state"))
	noErr(t, err)
	t.Cleanup(func() { store.Close() })
	now := time.Unix(1_900_000_000, 0).UTC()
	noErr(t, store.AddRepository(ctx, state.Repository{ID: "project", Name: "Project", CreatedAt: now}))
	_, err = store.SetCheckPolicy(ctx, state.CheckPolicyInput{
		RepositoryID: "project", Executor: state.CheckExecutorExternalRunner,
		AllowedEvents: []string{"push"}, MaxTimeoutMS: 60_000, MaxOutputLimitBytes: 64 << 10,
		QueueLimit: 4, MaxActiveJobs: 1, MaxLeaseMS: 60_000,
	}, now)
	noErr(t, err)
	_, err = store.GrantCheckConsent(ctx, "project", now)
	noErr(t, err)
	_, token, created, err := store.IssueCheckRunnerToken(ctx, "project", "runner", "", now)
	if err != nil || !created {
		t.Fatalf("issue runner created=%v err=%v", created, err)
	}
	job, deduped, err := store.AdmitCheckJob(ctx, state.CheckJobRequest{
		RepositoryID: "project", Trigger: "push", EventKey: "refs/heads/main@" + strings.Repeat("a", 40),
		SourceOID: strings.Repeat("a", 40), TriggerRef: "main", WorkflowDigest: strings.Repeat("b", 64),
		Checks: []state.CheckDefinition{{Name: "unit", Command: "go test ./..."}},
	}, now)
	if err != nil || deduped {
		t.Fatalf("admit job deduped=%v err=%v", deduped, err)
	}
	app := &App{Store: store, Repositories: newRepositoryManager(t, store, filepath.Join(t.TempDir(), "runtime")), Now: func() time.Time { return now.Add(time.Second) }}
	const prefix = "/api/v1/repositories/project/runner/"
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		app.handleRunnerAPI(writer, request, "project", strings.TrimPrefix(request.URL.Path, prefix))
	}))
	t.Cleanup(server.Close)
	serverURL, err := url.Parse(server.URL)
	noErr(t, err)
	return &runnerReadFixture{ctx: ctx, store: store, app: app, job: job, client: apiclient.NewBearer(serverURL, token)}
}

func (fixture *runnerReadFixture) post(path, leaseID string, input any) (checkapi.JobResponse, error) {
	content, err := fixture.client.DoWithHeaders(fixture.ctx, http.MethodPost, "/api/v1/repositories/project/runner/"+path, input, map[string]string{runnerLeaseHeader: leaseID})
	var response checkapi.JobResponse
	if err == nil {
		err = json.Unmarshal(content, &response)
	}
	return response, err
}

// claimAndStart takes the admitted job through the runner's claim and start.
func (fixture *runnerReadFixture) claimAndStart(t *testing.T) state.CheckJob {
	t.Helper()
	claimed, err := fixture.post("claim", "", nil)
	if err != nil || claimed.Job == nil || claimed.Job.ID != fixture.job.ID {
		t.Fatalf("claim job=%+v err=%v", claimed.Job, err)
	}
	leaseID := claimed.Job.LeaseID
	if _, err := fixture.post("jobs/"+fixture.job.ID+"/start", leaseID, checkapi.RunnerStartInput{LeaseID: leaseID, AttemptID: strings.Repeat("c", 32)}); err != nil {
		t.Fatalf("start: %v", err)
	}
	return fixture.read(t)
}

func (fixture *runnerReadFixture) read(t *testing.T) state.CheckJob {
	t.Helper()
	job, exists, err := fixture.store.CheckJob(fixture.ctx, "project", fixture.job.ID)
	if err != nil || !exists {
		t.Fatalf("read job exists=%v err=%v", exists, err)
	}
	return job
}

func (fixture *runnerReadFixture) exec(t *testing.T, statement string, arguments ...any) {
	t.Helper()
	noErr(t, fixture.store.Exec(fixture.ctx, statement, arguments...))
}

func passedCompletion(leaseID string) checkapi.RunnerCompletionInput {
	exit := 0
	return checkapi.RunnerCompletionInput{LeaseID: leaseID, Completion: checkapi.AttemptCompletion{
		Results:    []checkapi.Result{{Name: "unit", Command: "go test ./...", Status: state.AttemptPassed, ExitCode: &exit, DurationMS: 5}},
		FinishedAt: time.Unix(1_900_000_010, 0).UTC(), WorktreeState: state.WorktreeClean, Log: "ok",
	}}
}

// requireAPIError checks the error a runner client received, including the
// HTTP status it keeps for classifying the failure.
func requireAPIError(t *testing.T, name string, err error, status int, code string) *apiclient.Error {
	t.Helper()
	var problem *apiclient.Error
	if !errors.As(err, &problem) || problem.ResponseStatus != status || problem.Code != code {
		t.Fatalf("%s: want %d %s, got %v (%+v)", name, status, code, err, problem)
	}
	return problem
}

// A job that cannot be read is not a job that does not exist. The runner is
// told the server is unavailable, keeps that status for its retry decision,
// and nothing about the job changes; once the job is readable again the same
// lease completes it.
func TestRunnerCompletionOfAnUnreadableJobIsUnavailable(t *testing.T) {
	fixture := newRunnerReadFixture(t)
	started := fixture.claimAndStart(t)

	fixture.exec(t, `UPDATE check_jobs SET limits_json='{{' WHERE id=?`, started.ID)
	_, err := fixture.post("jobs/"+started.ID+"/complete", started.LeaseID, passedCompletion(started.LeaseID))
	requireAPIError(t, "unreadable job", err, http.StatusServiceUnavailable, "state_unavailable")

	fixture.exec(t, `UPDATE check_jobs SET limits_json=? WHERE id=?`, mustJSON(t, started.Limits), started.ID)
	if after := fixture.read(t); after.Status != state.CheckJobStarted || after.LeaseID != started.LeaseID || after.FinishedAt != nil {
		t.Fatalf("a failed read changed the job: status=%s lease_kept=%v finished=%v", after.Status, after.LeaseID == started.LeaseID, after.FinishedAt)
	}
	attempt, exists, err := fixture.store.CheckAttemptByID(fixture.ctx, "project", started.AttemptID)
	if err != nil || !exists || !attempt.FinishedAt.IsZero() {
		t.Fatalf("a failed read changed the attempt: exists=%v finished=%v err=%v", exists, attempt.FinishedAt, err)
	}

	// A job that does not exist keeps its established answer.
	missing := strings.Repeat("d", 32)
	_, err = fixture.post("jobs/"+missing+"/complete", started.LeaseID, passedCompletion(started.LeaseID))
	requireAPIError(t, "missing job", err, http.StatusNotFound, "check_job_not_found")

	if _, err := fixture.post("jobs/"+started.ID+"/complete", started.LeaseID, passedCompletion(started.LeaseID)); err != nil {
		t.Fatalf("completion after the read recovered: %v", err)
	}
	if after := fixture.read(t); after.Status != state.CheckJobPassed {
		t.Fatalf("completed job status=%s", after.Status)
	}
}

// A committed claim is answered with the job and its lease even when the
// captured commands cannot be read, but never as a job with no commands.
func TestRunnerClaimKeepsTheJobWhenItsCommandsCannotBeRead(t *testing.T) {
	for _, test := range []struct {
		name, statement, message string
	}{
		{"unreadable", `UPDATE check_configurations SET checks_json='{' WHERE repository_id='project'`, "The job's captured commands could not be read."},
		{"missing", `DELETE FROM check_configurations WHERE repository_id='project'`, "The job's captured commands are missing."},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newRunnerReadFixture(t)
			fixture.exec(t, test.statement)
			_, err := fixture.post("claim", "", nil)
			problem := requireAPIError(t, "claim", err, http.StatusServiceUnavailable, "state_unavailable")
			var details struct {
				JobID   string `json:"job_id"`
				LeaseID string `json:"lease_id"`
			}
			noErr(t, json.Unmarshal(problem.Details, &details))
			claimed := fixture.read(t)
			if problem.Message != test.message || claimed.Status != state.CheckJobClaimed || details.JobID != claimed.ID || details.LeaseID == "" || details.LeaseID != claimed.LeaseID {
				t.Fatalf("claim message=%q details=%+v job status=%s lease=%s", problem.Message, details, claimed.Status, claimed.LeaseID)
			}
		})
	}
}

// The owner's job detail reports a failed read of the captured commands or of
// the bound attempt instead of omitting them. A missing attempt that the job
// names is a damaged record, answered like a failed read by the detail and by
// the log endpoint.
func TestOwnerJobDetailReportsUnreadableCommandsAndAttempt(t *testing.T) {
	get := func(t *testing.T, fixture *runnerReadFixture, remainder string) (int, string) {
		t.Helper()
		request := httptest.NewRequest(http.MethodGet, "/api/v1/repositories/project/check-jobs/"+remainder, nil)
		response := httptest.NewRecorder()
		fixture.app.handleCheckJobs(response, request, "project", remainder)
		return response.Code, response.Body.String()
	}
	detail := func(t *testing.T, fixture *runnerReadFixture) (int, string) {
		t.Helper()
		return get(t, fixture, fixture.job.ID)
	}
	t.Run("commands", func(t *testing.T) {
		fixture := newRunnerReadFixture(t)
		fixture.exec(t, `UPDATE check_configurations SET checks_json='{' WHERE repository_id='project'`)
		if status, body := detail(t, fixture); status != http.StatusServiceUnavailable || !strings.Contains(body, "state_unavailable") {
			t.Fatalf("unreadable commands status=%d body=%s", status, body)
		}
	})
	t.Run("attempt", func(t *testing.T) {
		fixture := newRunnerReadFixture(t)
		started := fixture.claimAndStart(t)
		if status, body := detail(t, fixture); status != http.StatusOK || !strings.Contains(body, `"attempt":`) || !strings.Contains(body, `"checks":`) {
			t.Fatalf("readable detail status=%d body=%s", status, body)
		}
		fixture.exec(t, `UPDATE check_attempts SET started_at='unreadable' WHERE id=?`, started.AttemptID)
		if status, body := detail(t, fixture); status != http.StatusServiceUnavailable || !strings.Contains(body, "state_unavailable") {
			t.Fatalf("unreadable attempt status=%d body=%s", status, body)
		}
		fixture.exec(t, `UPDATE check_jobs SET attempt_id=? WHERE id=?`, strings.Repeat("e", 32), started.ID)
		for _, remainder := range []string{started.ID, started.ID + "/log"} {
			if status, body := get(t, fixture, remainder); status != http.StatusServiceUnavailable || !strings.Contains(body, "state_unavailable") || !strings.Contains(body, "The attempt this job names is missing.") {
				t.Fatalf("%s with a missing named attempt status=%d body=%s", remainder, status, body)
			}
		}
	})
}

func mustJSON(t *testing.T, value any) string {
	t.Helper()
	encoded, err := json.Marshal(value)
	noErr(t, err)
	return string(encoded)
}
