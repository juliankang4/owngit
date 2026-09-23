package checkrunner_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"owngit/internal/apiclient"
	"owngit/internal/checkrunner"
	"owngit/internal/checkworkflow"
	"owngit/internal/gitexec"
	"owngit/internal/repository"
	"owngit/internal/server"
	"owngit/internal/state"
)

type runnerIntegrationFixture struct {
	t          *testing.T
	ctx        context.Context
	root       string
	store      *state.Store
	manager    *repository.Manager
	repository state.Repository
	job        state.CheckJob
	credential state.RunnerCredential
	token      string
	hosts      *server.HostPolicy
	app        *server.App
}

func TestExternalRunnerClaimsExactSourceExecutesAndCompletes(t *testing.T) {
	fixture := newRunnerIntegrationFixture(t, "echo runner-ok")
	httpServer, origin := fixture.startHTTPServer(nil)
	defer httpServer.Close()
	if err := fixture.runner(fixture.client(origin)).Run(fixture.ctx); err != nil {
		t.Fatal(err)
	}
	completed := fixture.readJob()
	if completed.Status != state.CheckJobPassed || completed.AttemptID == "" {
		t.Fatalf("completed job=%+v", completed)
	}
	attempt, exists, err := fixture.store.CheckAttemptByID(fixture.ctx, fixture.repository.ID, completed.AttemptID)
	if err != nil || !exists || attempt.Status != state.AttemptPassed || attempt.RevisionOID != fixture.job.SourceOID {
		t.Fatalf("attempt=%+v exists=%v err=%v", attempt, exists, err)
	}
}

// A byte cut through Korean output, plus a non-UTF-8 byte, used to grow past
// the server's excerpt bound after JSON encoding. The server refused the
// completion and the runner stopped with the job left started.
func TestExternalRunnerCompletesNonASCIIOutputAcrossExcerptBound(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the output fixture uses POSIX shell tools")
	}
	fixture := newRunnerIntegrationFixture(t, `printf '\377xyz'; yes '가' | head -n 3000`)
	httpServer, origin := fixture.startHTTPServer(nil)
	defer httpServer.Close()
	if err := fixture.runner(fixture.client(origin)).Run(fixture.ctx); err != nil {
		t.Fatalf("runner stopped: %v (job %s)", err, fixture.readJob().Status)
	}
	completed := fixture.readJob()
	if completed.Status != state.CheckJobPassed {
		t.Fatalf("completed job=%+v", completed)
	}
	attempt, exists, err := fixture.store.CheckAttemptByID(fixture.ctx, fixture.repository.ID, completed.AttemptID)
	if err != nil || !exists || len(attempt.Results) != 1 {
		t.Fatalf("attempt=%+v exists=%v err=%v", attempt, exists, err)
	}
	excerpt := attempt.Results[0].OutputExcerpt
	if !attempt.Results[0].Truncated || len(excerpt) > state.MaximumCheckExcerptBytes || !utf8.ValidString(excerpt) || !strings.HasPrefix(excerpt, "\uFFFDxyz가\n") {
		t.Fatalf("stored excerpt len=%d valid=%v truncated=%v prefix=%q", len(excerpt), utf8.ValidString(excerpt), attempt.Results[0].Truncated, excerpt[:min(len(excerpt), 16)])
	}
}

func newRunnerIntegrationFixture(t *testing.T, command string) *runnerIntegrationFixture {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("Git is unavailable")
	}
	ctx := context.Background()
	root := t.TempDir()
	store, err := state.Open(ctx, filepath.Join(root, "state"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Error(err)
		}
	})
	git, err := gitexec.New("", filepath.Join(root, "runtime"))
	if err != nil {
		t.Fatal(err)
	}
	repositoryRoot := filepath.Join(root, "repositories")
	if err := os.Mkdir(repositoryRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := store.CompleteSetup(ctx, repositoryRoot, "open", "", "test-admin-hash", true); err != nil {
		t.Fatal(err)
	}
	manager := &repository.Manager{Store: store, Git: git, Locks: gitexec.NewLocks(), Root: repositoryRoot}
	stored, err := manager.Create(ctx, "runner-test", "")
	if err != nil {
		t.Fatal(err)
	}
	repositoryPath, err := manager.Path(stored.ID)
	if err != nil {
		t.Fatal(err)
	}
	work := filepath.Join(root, "source")
	runGit(t, "init", "--initial-branch=main", work)
	runGit(t, "-C", work, "config", "user.name", "OwnGit Test")
	runGit(t, "-C", work, "config", "user.email", "test@example.invalid")
	if err := os.WriteFile(filepath.Join(work, "source.txt"), []byte("exact source\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, "-C", work, "add", "source.txt")
	runGit(t, "-C", work, "commit", "-m", "source")
	runGit(t, "-C", work, "push", repositoryPath, "HEAD:refs/heads/main")
	sourceOID := strings.TrimSpace(runGit(t, "-C", work, "rev-parse", "HEAD"))

	now := time.Now().UTC().Add(-time.Minute)
	if _, err := store.SetCheckPolicy(ctx, state.CheckPolicyInput{
		RepositoryID: stored.ID, Executor: state.CheckExecutorExternalRunner,
		AllowedEvents: []string{checkworkflow.EventPush}, MaxTimeoutMS: 60_000, MaxOutputLimitBytes: 64 << 10,
		QueueLimit: 4, MaxActiveJobs: 1, MaxLeaseMS: 60_000,
	}, now); err != nil {
		t.Fatal(err)
	}
	credential, token, created, err := store.IssueCheckRunnerToken(ctx, stored.ID, "test runner", "", now.Add(time.Second))
	if err != nil || !created || token == "" {
		t.Fatalf("issue runner created=%v token=%v err=%v", created, token != "", err)
	}
	if _, err := store.GrantCheckConsent(ctx, stored.ID, now.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	job, deduped, err := store.AdmitCheckJob(ctx, state.CheckJobRequest{
		RepositoryID: stored.ID, Trigger: checkworkflow.EventPush, EventKey: "refs/heads/main@" + sourceOID,
		SourceOID: sourceOID, TriggerRef: "main", WorkflowPath: checkworkflow.Path,
		WorkflowDigest: strings.Repeat("a", 64), Checks: []state.CheckDefinition{{Name: "runner", Command: command}},
	}, now.Add(3*time.Second))
	if err != nil || deduped {
		t.Fatalf("admit job=%+v deduped=%v err=%v", job, deduped, err)
	}
	hosts := server.NewHostPolicy()
	fixture := &runnerIntegrationFixture{
		t: t, ctx: ctx, root: root, store: store, manager: manager, repository: stored,
		job: job, credential: credential, token: token, hosts: hosts,
	}
	fixture.app = &server.App{Store: store, Repositories: manager, Hosts: hosts}
	return fixture
}

func (fixture *runnerIntegrationFixture) startHTTPServer(wrap func(http.Handler) http.Handler) (*httptest.Server, *url.URL) {
	fixture.t.Helper()
	handler := fixture.app.Handler()
	if wrap != nil {
		handler = wrap(handler)
	}
	httpServer := httptest.NewServer(handler)
	origin := fixture.addHost(httpServer.URL)
	return httpServer, origin
}

func (fixture *runnerIntegrationFixture) addHost(rawURL string) *url.URL {
	fixture.t.Helper()
	origin, err := url.Parse(rawURL)
	if err != nil {
		fixture.t.Fatal(err)
	}
	if err := fixture.hosts.Add(origin.Host); err != nil {
		fixture.t.Fatal(err)
	}
	return origin
}

func (fixture *runnerIntegrationFixture) client(origin *url.URL) *apiclient.Client {
	client := apiclient.NewBearer(origin, fixture.token)
	client.MaximumResponse = 128 << 20
	return client
}

func (fixture *runnerIntegrationFixture) runner(client *apiclient.Client) *checkrunner.Runner {
	return &checkrunner.Runner{
		Client: client, RepositoryID: fixture.repository.ID,
		WorkspaceRoot: filepath.Join(fixture.root, "runner-work-"+stateIDForTest(fixture.t)), Once: true,
	}
}

func (fixture *runnerIntegrationFixture) readJob() state.CheckJob {
	fixture.t.Helper()
	job, exists, err := fixture.store.CheckJob(fixture.ctx, fixture.repository.ID, fixture.job.ID)
	if err != nil || !exists {
		fixture.t.Fatalf("job exists=%v err=%v", exists, err)
	}
	return job
}

func stateIDForTest(t *testing.T) string {
	t.Helper()
	id, err := state.RandomID()
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func runGit(t *testing.T, arguments ...string) string {
	t.Helper()
	command := exec.Command("git", arguments...)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", arguments, err, output)
	}
	return string(output)
}
