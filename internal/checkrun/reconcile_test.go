package checkrun

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"owngit/internal/checkworkflow"
	"owngit/internal/gitexec"
	"owngit/internal/pullrequest"
	"owngit/internal/repository"
	"owngit/internal/state"
	"owngit/internal/testfixture"
)

type pushFixture struct {
	t            *testing.T
	ctx          context.Context
	store        *state.Store
	repositoryID string
	repoPath     string
	work         string
	coordinator  *Coordinator
	logs         []string
}

func newPushFixture(t *testing.T, queueLimit int) *pushFixture {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("Git is unavailable")
	}
	ctx := context.Background()
	root := t.TempDir()
	store, err := state.Open(ctx, filepath.Join(root, "state"))
	noErr(t, err)
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Error(err)
		}
	})
	git, err := gitexec.New("", filepath.Join(root, "runtime"))
	noErr(t, err)
	repositoryRoot := filepath.Join(root, "repositories")
	noErr(t, os.Mkdir(repositoryRoot, 0o700))
	noErr(t, store.CompleteSetup(ctx, repositoryRoot, "open", "", "test-admin-hash", true))
	manager := &repository.Manager{Store: store, Git: git, Locks: gitexec.NewLocks(), Root: repositoryRoot}
	stored, err := manager.Create(ctx, "workflows", "")
	noErr(t, err)
	repoPath, err := manager.Path(stored.ID)
	noErr(t, err)
	fixture := &pushFixture{t: t, ctx: ctx, store: store, repositoryID: stored.ID, repoPath: repoPath, work: filepath.Join(root, "work")}
	fixture.git("init", "--initial-branch=main", fixture.work)
	fixture.git("-C", fixture.work, "config", "user.name", "OwnGit Test")
	fixture.git("-C", fixture.work, "config", "user.email", "test@example.invalid")
	noErr(t, os.MkdirAll(filepath.Join(fixture.work, ".owngit"), 0o700))

	now := time.Now().UTC().Add(-time.Minute)
	if _, err := store.SetCheckPolicy(ctx, state.CheckPolicyInput{
		RepositoryID: stored.ID, Executor: state.CheckExecutorExternalRunner,
		AllowedEvents: []string{checkworkflow.EventPush}, MaxTimeoutMS: 60_000, MaxOutputLimitBytes: 64 << 10,
		QueueLimit: queueLimit, MaxActiveJobs: 1, MaxLeaseMS: 60_000,
	}, now); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := store.IssueCheckRunnerToken(ctx, stored.ID, "test runner", "", now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.GrantCheckConsent(ctx, stored.ID, now.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	fixture.coordinator = &Coordinator{
		Store: store, Repositories: manager, PullRequests: &pullrequest.Service{Store: store, Repositories: manager},
		Logf: func(format string, arguments ...any) { fixture.logs = append(fixture.logs, format) },
	}
	return fixture
}

func (fixture *pushFixture) git(arguments ...string) string {
	fixture.t.Helper()
	command := exec.Command("git", arguments...)
	command.Env = testfixture.GitEnvironment(os.Environ())
	output, err := command.CombinedOutput()
	if err != nil {
		fixture.t.Fatalf("git %v: %v\n%s", arguments, err, output)
	}
	for _, argument := range arguments {
		if argument == "push" {
			// A push reaches the repository through OwnGit, which releases the
			// repository's write lock; the cached refs are then read again.
			lock := fixture.coordinator.Repositories.Locks.For(fixture.repositoryID)
			lock.Lock()
			lock.Unlock()
		}
	}
	return strings.TrimSpace(string(output))
}

// pushWorkflow commits workflow as the checks file and pushes it to branch.
func (fixture *pushFixture) pushWorkflow(branch, workflow string) string {
	fixture.t.Helper()
	if err := os.WriteFile(filepath.Join(fixture.work, checkworkflow.Path), []byte(workflow), 0o600); err != nil {
		fixture.t.Fatal(err)
	}
	fixture.git("-C", fixture.work, "add", ".")
	fixture.git("-C", fixture.work, "commit", "--allow-empty", "-m", branch)
	fixture.git("-C", fixture.work, "push", fixture.repoPath, "HEAD:refs/heads/"+branch)
	return fixture.git("-C", fixture.work, "rev-parse", "HEAD")
}

func (fixture *pushFixture) observed() map[string]string {
	fixture.t.Helper()
	observations, err := fixture.store.CheckObservations(fixture.ctx, fixture.repositoryID)
	if err != nil {
		fixture.t.Fatal(err)
	}
	result := make(map[string]string, len(observations))
	for _, observation := range observations {
		result[observation.RefName] = observation.OID
	}
	return result
}

func (fixture *pushFixture) jobRefs() []string {
	fixture.t.Helper()
	jobs, err := fixture.store.LatestCheckJobs(fixture.ctx, fixture.repositoryID, 10)
	if err != nil {
		fixture.t.Fatal(err)
	}
	refs := make([]string, 0, len(jobs))
	for _, job := range jobs {
		refs = append(refs, job.TriggerRef)
	}
	return refs
}

// validWorkflow's check succeeds in every check shell without looking up a
// program: exit is built into both sh and cmd.exe.
const validWorkflow = `{"version":1,"events":{"push":{}},"checks":[{"name":"n","command":"exit 0"}]}`

// A branch whose workflow cannot be parsed sorts before main. Its failure used
// to abort the pass before the cursor moved, so main was never admitted.
func TestInvalidWorkflowOnOneBranchDoesNotBlockLaterBranches(t *testing.T) {
	fixture := newPushFixture(t, 4)
	mainOID := fixture.pushWorkflow("main", validWorkflow)
	brokenOID := fixture.pushWorkflow("a-broken", `{`)
	for pass := 0; pass < 2; pass++ {
		noErr(t, fixture.coordinator.reconcile(fixture.ctx))
	}
	if refs := fixture.jobRefs(); len(refs) != 1 || refs[0] != "main" {
		t.Fatalf("admitted jobs for %v, want only main", refs)
	}
	observed := fixture.observed()
	if observed["refs/heads/a-broken"] != brokenOID || observed["refs/heads/main"] != mainOID {
		t.Fatalf("observations=%v", observed)
	}
	rejections := 0
	for _, line := range fixture.logs {
		if strings.Contains(line, "was not admitted") {
			rejections++
		}
	}
	if rejections != 1 {
		t.Fatalf("the rejected revision was reported %d times, want once: %v", rejections, fixture.logs)
	}
	if fixture.coordinator.pushCursor[fixture.repositoryID] != "main" {
		t.Fatalf("cursor=%q", fixture.coordinator.pushCursor[fixture.repositoryID])
	}

	// Fixing the branch moves it to a new revision, which is admitted.
	fixture.pushWorkflow("a-broken", validWorkflow)
	noErr(t, fixture.coordinator.reconcile(fixture.ctx))
	if refs := fixture.jobRefs(); len(refs) != 2 {
		t.Fatalf("admitted jobs after the fix=%v", refs)
	}
}

// A full queue is transient. The branch stays unobserved and the cursor stays
// before it, so a later pass admits it once the queue drains.
func TestFullQueueKeepsTheBranchForALaterPass(t *testing.T) {
	fixture := newPushFixture(t, 1)
	fixture.pushWorkflow("a-first", validWorkflow)
	secondOID := fixture.pushWorkflow("b-second", validWorkflow)
	policy, _, err := fixture.store.CheckPolicy(fixture.ctx, fixture.repositoryID)
	noErr(t, err)
	if err := fixture.coordinator.reconcilePushes(fixture.ctx, fixture.repositoryID, policy); !errors.Is(err, state.ErrCheckQueueFull) {
		t.Fatalf("reconcile err=%v, want a full queue", err)
	}
	if observed := fixture.observed(); observed["refs/heads/b-second"] != "" {
		t.Fatalf("the refused branch was observed: %v", observed)
	}
	if cursor := fixture.coordinator.pushCursor[fixture.repositoryID]; cursor != "a-first" {
		t.Fatalf("cursor=%q", cursor)
	}
	jobs, err := fixture.store.LatestCheckJobs(fixture.ctx, fixture.repositoryID, 10)
	if err != nil || len(jobs) != 1 {
		t.Fatalf("jobs=%v err=%v", jobs, err)
	}
	if _, err := fixture.store.CancelCheckJob(fixture.ctx, fixture.repositoryID, jobs[0].ID, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	noErr(t, fixture.coordinator.reconcilePushes(fixture.ctx, fixture.repositoryID, policy))
	if observed := fixture.observed(); observed["refs/heads/b-second"] != secondOID {
		t.Fatalf("the branch was not admitted after the queue drained: %v", observed)
	}
}

// A branch name the observation cannot hold used to fail the pass before its
// cursor moved, so every later branch of the repository was never checked.
func TestUnrecordableBranchNameDoesNotBlockLaterBranches(t *testing.T) {
	fixture := newPushFixture(t, 4)
	component := strings.Repeat("a", 100)
	long := component + "/" + component + "/" + component
	fixture.pushWorkflow(long, validWorkflow)
	mainOID := fixture.pushWorkflow("main", validWorkflow)
	for pass := 0; pass < 3; pass++ {
		noErr(t, fixture.coordinator.reconcile(fixture.ctx))
	}
	if refs := fixture.jobRefs(); len(refs) != 1 || refs[0] != "main" {
		t.Fatalf("admitted jobs for %v, want only main", refs)
	}
	if observed := fixture.observed(); observed["refs/heads/main"] != mainOID || len(observed) != 1 {
		t.Fatalf("observations=%v", observed)
	}
	skips := 0
	for _, line := range fixture.logs {
		if strings.Contains(line, "is skipped") {
			skips++
		}
	}
	if skips != 1 {
		t.Fatalf("the skipped branch was reported %d times, want once: %v", skips, fixture.logs)
	}
}

// A push event that is off observes nothing, so turning it on later admits the
// heads that arrived meanwhile, and a head that already has a job stays unqueued.
func TestDisabledPushEventObservesNothing(t *testing.T) {
	fixture := newPushFixture(t, 4)
	now := time.Now().UTC()
	policy := func(events ...string) {
		_, err := fixture.store.SetCheckPolicy(fixture.ctx, state.CheckPolicyInput{
			RepositoryID: fixture.repositoryID, Executor: state.CheckExecutorExternalRunner, AllowedEvents: events,
			MaxTimeoutMS: 60_000, MaxOutputLimitBytes: 64 << 10, QueueLimit: 4, MaxActiveJobs: 1, MaxLeaseMS: 60_000,
		}, now)
		noErr(t, err)
		_, err = fixture.store.GrantCheckConsent(fixture.ctx, fixture.repositoryID, now.Add(time.Second))
		noErr(t, err)
		now = now.Add(2 * time.Second)
	}
	policy(checkworkflow.EventPullRequest)
	fixture.pushWorkflow("main", `{"version":1,"events":{"push":{},"pull_request":{}},"checks":[{"name":"n","command":"exit 0"}]}`)
	for pass := 0; pass < 2; pass++ {
		noErr(t, fixture.coordinator.reconcile(fixture.ctx))
	}
	if observed := fixture.observed(); len(observed) != 0 {
		t.Fatalf("observations while push was off: %v", observed)
	}
	policy(checkworkflow.EventPush, checkworkflow.EventPullRequest)
	for pass := 0; pass < 2; pass++ {
		noErr(t, fixture.coordinator.reconcile(fixture.ctx))
	}
	if refs := fixture.jobRefs(); len(refs) != 1 || refs[0] != "main" {
		t.Fatalf("jobs after enabling push=%v, want one for main", refs)
	}
}
