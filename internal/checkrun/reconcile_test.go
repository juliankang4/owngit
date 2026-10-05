package checkrun

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"owngit/internal/checksource"
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
	// A push event that arrives while the queue is full is refused the same
	// way and named in the log: nothing is queued for it, and the branch head
	// waits for a later pass exactly as the head pass does.
	fixture.coordinator.NotePush(fixture.repositoryID, []PushUpdate{{Ref: "refs/heads/b-second", New: secondOID}})
	fixture.coordinator.admitPendingPushes(fixture.ctx, state.DefaultCheckCeilings)
	if later, err := fixture.store.LatestCheckJobs(fixture.ctx, fixture.repositoryID, 10); err != nil || len(later) != 1 {
		t.Fatalf("a full queue admitted jobs=%v err=%v", later, err)
	}
	refusals := 0
	for _, line := range fixture.logs {
		if strings.Contains(line, "was not admitted") {
			refusals++
		}
	}
	if refusals != 1 {
		t.Fatalf("the refused push event was named %d times, want once: %v", refusals, fixture.logs)
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

// A push event that is off observes nothing and admits nothing from the push
// facts it kept, so turning it on later admits the heads that arrived
// meanwhile, and a head that already has a job stays unqueued.
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
	oid := fixture.pushWorkflow("main", `{"version":1,"events":{"push":{},"pull_request":{}},"checks":[{"name":"n","command":"exit 0"}]}`)
	fixture.coordinator.NotePush(fixture.repositoryID, []PushUpdate{{Ref: "refs/heads/main", New: oid}})
	for pass := 0; pass < 2; pass++ {
		noErr(t, fixture.coordinator.reconcile(fixture.ctx))
	}
	if observed := fixture.observed(); len(observed) != 0 {
		t.Fatalf("observations while push was off: %v", observed)
	}
	if refs := fixture.jobRefs(); len(refs) != 0 {
		t.Fatalf("the push facts of a disabled event admitted %v", refs)
	}
	policy(checkworkflow.EventPush, checkworkflow.EventPullRequest)
	for pass := 0; pass < 2; pass++ {
		noErr(t, fixture.coordinator.reconcile(fixture.ctx))
	}
	if refs := fixture.jobRefs(); len(refs) != 1 || refs[0] != "main" {
		t.Fatalf("jobs after enabling push=%v, want one for main", refs)
	}
}

// The pass a push wakes runs while the push still holds the repository write
// lock. It waits for the writer and sees the pushed head in that pass.
func TestPushWakePassWaitsForTheWritingPush(t *testing.T) {
	fixture := newPushFixture(t, 4)
	fixture.pushWorkflow("main", validWorkflow)
	noErr(t, fixture.coordinator.reconcile(fixture.ctx))
	lock := fixture.coordinator.Repositories.Locks.For(fixture.repositoryID)
	lock.Lock()
	fixture.pushWorkflow("feature", validWorkflow)
	done := make(chan error, 1)
	go func() { done <- fixture.coordinator.reconcile(fixture.ctx) }()
	// The pass is blocked on the lock once it counts as waiting. A pass that
	// finished first did not wait for the writer.
	for finished := false; !lock.Waiting() && !finished; {
		select {
		case err := <-done:
			done <- err
			finished = true
		default:
			runtime.Gosched()
		}
	}
	lock.Unlock()
	noErr(t, <-done)
	if refs := fixture.jobRefs(); len(refs) != 2 {
		t.Fatalf("jobs after the pass woken by the push=%v, want main and feature", refs)
	}
}

// A branch written straight into the repository folder, not through OwnGit,
// is seen by the next pass.
func TestBranchWrittenOutsideOwnGitIsSeenByTheNextPass(t *testing.T) {
	fixture := newPushFixture(t, 4)
	fixture.pushWorkflow("main", validWorkflow)
	noErr(t, fixture.coordinator.reconcile(fixture.ctx))
	fixture.git("-C", fixture.work, "update-ref", "refs/heads/outside", "HEAD")
	fixture.git("-C", fixture.work, "push", fixture.repoPath, "refs/heads/outside")
	noErr(t, fixture.coordinator.reconcile(fixture.ctx))
	if refs := fixture.jobRefs(); len(refs) != 2 {
		t.Fatalf("jobs=%v, want main and outside", refs)
	}
}

// Observations left while push was off, with consent inactive so no pass
// cleared them, must not hide the head once push is turned on again. A head
// that already had a job is still not queued twice.
func TestEnablingPushIgnoresObservationsLeftWhilePushWasOff(t *testing.T) {
	fixture := newPushFixture(t, 4)
	oid := fixture.pushWorkflow("main", validWorkflow)
	now := time.Now().UTC()
	savePolicy := func(consent bool, events ...string) {
		now = now.Add(2 * time.Second)
		_, err := fixture.store.SetCheckPolicy(fixture.ctx, state.CheckPolicyInput{
			RepositoryID: fixture.repositoryID, Executor: state.CheckExecutorExternalRunner, AllowedEvents: events,
			MaxTimeoutMS: 60_000, MaxOutputLimitBytes: 64 << 10, QueueLimit: 4, MaxActiveJobs: 1, MaxLeaseMS: 60_000,
		}, now)
		noErr(t, err)
		if consent {
			_, err = fixture.store.GrantCheckConsent(fixture.ctx, fixture.repositoryID, now.Add(time.Second))
			noErr(t, err)
		}
	}
	savePolicy(false, checkworkflow.EventPullRequest)
	noErr(t, fixture.store.RecordCheckObservation(fixture.ctx, fixture.repositoryID, "refs/heads/main", oid, now))
	savePolicy(true, checkworkflow.EventPush, checkworkflow.EventPullRequest)
	noErr(t, fixture.coordinator.reconcile(fixture.ctx))
	if refs := fixture.jobRefs(); len(refs) != 1 {
		t.Fatalf("jobs after enabling push=%v, want one for main", refs)
	}
	savePolicy(true, checkworkflow.EventPullRequest)
	noErr(t, fixture.store.RecordCheckObservation(fixture.ctx, fixture.repositoryID, "refs/heads/main", strings.Repeat("1", 40), now))
	savePolicy(true, checkworkflow.EventPush, checkworkflow.EventPullRequest)
	noErr(t, fixture.coordinator.reconcile(fixture.ctx))
	if refs := fixture.jobRefs(); len(refs) != 1 {
		t.Fatalf("jobs after enabling push again=%v, want still one", refs)
	}
}

// An observation that equals the current head but has no job, such as one
// left by an earlier version, is checked against the job history by the first
// pass after a start and queues the head once. A head that already has a job
// is not queued again, and later passes do not look again.
func TestFirstPassAfterStartAdmitsAnObservedHeadWithoutAJob(t *testing.T) {
	fixture := newPushFixture(t, 4)
	oid := fixture.pushWorkflow("main", validWorkflow)
	noErr(t, fixture.store.RecordCheckObservation(fixture.ctx, fixture.repositoryID, "refs/heads/main", oid, time.Now().UTC()))
	noErr(t, fixture.coordinator.reconcile(fixture.ctx))
	if refs := fixture.jobRefs(); len(refs) != 1 {
		t.Fatalf("jobs after the first pass=%v, want one for main", refs)
	}
	// A restarted coordinator finds the job and queues nothing more.
	restarted := &Coordinator{Store: fixture.store, Repositories: fixture.coordinator.Repositories, PullRequests: fixture.coordinator.PullRequests}
	noErr(t, restarted.reconcile(fixture.ctx))
	if refs := fixture.jobRefs(); len(refs) != 1 {
		t.Fatalf("jobs after a restart=%v, want still one", refs)
	}
	// Later passes trust observations again.
	feature := fixture.pushWorkflow("feature", validWorkflow)
	noErr(t, fixture.store.RecordCheckObservation(fixture.ctx, fixture.repositoryID, "refs/heads/feature", feature, time.Now().UTC()))
	noErr(t, restarted.reconcile(fixture.ctx))
	if refs := fixture.jobRefs(); len(refs) != 1 {
		t.Fatalf("jobs after a later pass=%v, want still one", refs)
	}
}

// With more branches than one page holds, the first-pass check pages toward
// the last branch without wrapping, so it ends after two pages and evaluates
// each unchanged head once.
func TestFirstPassCheckSweepsToTheLastBranchWithoutWrapping(t *testing.T) {
	fixture := newPushFixture(t, 4)
	fixture.pushWorkflow("main", validWorkflow)
	broken := fixture.pushWorkflow("a-broken", `{`)
	for index := 0; index < maximumObservedRefs-1; index++ {
		fixture.git("-C", fixture.work, "push", "-q", fixture.repoPath, fmt.Sprintf("HEAD:refs/heads/z-%02d", index))
	}
	// 65 branches: a-broken, main and 63 more; the observation set is bounded,
	// so the rejected head is evaluated whenever the check runs.
	noErr(t, fixture.store.RecordCheckObservation(fixture.ctx, fixture.repositoryID, "refs/heads/a-broken", broken, time.Now().UTC()))
	for pass := 0; pass < 2; pass++ {
		noErr(t, fixture.coordinator.reconcile(fixture.ctx))
	}
	if !fixture.coordinator.pushChecked[fixture.repositoryID] {
		t.Fatal("the first-pass check was not complete after two pages")
	}
	rejections := 0
	for _, line := range fixture.logs {
		if strings.Contains(line, "was not admitted") {
			rejections++
		}
	}
	if rejections > 65 {
		t.Fatalf("rejected heads were evaluated %d times in 2 passes, want at most one per head (65)", rejections)
	}
}

// pushJobOIDs are the revisions the repository's jobs were admitted for.
func pushJobOIDs(t *testing.T, fixture *pushFixture) map[string]bool {
	t.Helper()
	jobs, err := fixture.store.LatestCheckJobs(fixture.ctx, fixture.repositoryID, 20)
	noErr(t, err)
	oids := make(map[string]bool, len(jobs))
	for _, job := range jobs {
		oids[job.SourceOID] = true
	}
	return oids
}

// A push that moves a branch while a local job runs keeps its own check: two
// fast-forward pushes made during the run each get a job for their own
// revision, not only the head they left.
func TestPushesWhileALocalJobRunsEachGetAJob(t *testing.T) {
	fixture := newPushFixture(t, 8)
	now := time.Now().UTC()
	if _, err := fixture.store.SetCheckPolicy(fixture.ctx, state.CheckPolicyInput{
		RepositoryID: fixture.repositoryID, Executor: state.CheckExecutorHost,
		AllowedEvents: []string{checkworkflow.EventPush}, MaxTimeoutMS: 60_000, MaxOutputLimitBytes: 64 << 10,
		QueueLimit: 8, MaxActiveJobs: 1, MaxLeaseMS: 60_000,
	}, now); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.store.GrantCheckConsent(fixture.ctx, fixture.repositoryID, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	workspace, err := checksource.AcquireWorkspaceRoot(filepath.Join(t.TempDir(), "check-jobs"))
	noErr(t, err)
	t.Cleanup(workspace.Close)
	coordinator := fixture.coordinator
	coordinator.workspace = workspace
	first := fixture.pushWorkflow("main", validWorkflow)
	noErr(t, coordinator.reconcile(fixture.ctx))

	// The job waits for the repository write lock this test holds, exactly
	// where a concurrent push holds it. The two pushes below move the branch
	// while the coordinator is inside the job.
	lock := coordinator.Repositories.Locks.For(fixture.repositoryID)
	lock.Lock()
	jobDone := make(chan error, 1)
	go func() { jobDone <- coordinator.runOneLocal(fixture.ctx) }()
	deadline := time.Now().Add(20 * time.Second)
	for {
		jobs, err := fixture.store.LatestCheckJobs(fixture.ctx, fixture.repositoryID, 10)
		noErr(t, err)
		if len(jobs) == 1 && jobs[0].Status == state.CheckJobClaimed {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the local job was not claimed: %+v", jobs)
		}
		time.Sleep(5 * time.Millisecond)
	}
	second := fixture.pushWorkflow("main", validWorkflow)
	third := fixture.pushWorkflow("main", validWorkflow)
	coordinator.NotePush(fixture.repositoryID, []PushUpdate{{Ref: "refs/heads/main", New: second}})
	coordinator.NotePush(fixture.repositoryID, []PushUpdate{{Ref: "refs/heads/main", New: third}})
	lock.Unlock()
	noErr(t, <-jobDone)

	noErr(t, coordinator.reconcile(fixture.ctx))
	oids := pushJobOIDs(t, fixture)
	for _, oid := range []string{first, second, third} {
		if !oids[oid] {
			t.Fatalf("the pushed revision %s has no job of its own: %v", oid, oids)
		}
	}
	if len(oids) != 3 {
		t.Fatalf("jobs for %v, want one per pushed revision", oids)
	}
}

// The Git handler hands one push's refs to admission and has no result to
// return. A deletion, a ref outside refs/heads, a revision whose workflow is
// refused and an event that already has a job are all decided without
// touching the push, and the events behind them still run.
func TestPushUpdatesThatCannotBeAdmittedDoNotStopThePush(t *testing.T) {
	fixture := newPushFixture(t, 4)
	broken := fixture.pushWorkflow("main", `{`)
	valid := fixture.pushWorkflow("main", validWorkflow)
	coordinator := fixture.coordinator
	coordinator.NotePush(fixture.repositoryID, []PushUpdate{
		{Ref: "refs/heads/gone", New: ""},
		{Ref: "refs/tags/v1", New: valid},
		{Ref: "refs/heads/main", New: broken},
	})
	coordinator.NotePush(fixture.repositoryID, []PushUpdate{{Ref: "refs/heads/main", New: valid}})
	noErr(t, coordinator.reconcile(fixture.ctx))
	if refs := fixture.jobRefs(); len(refs) != 1 || refs[0] != "main" {
		t.Fatalf("jobs=%v, want only main", refs)
	}
	if oids := pushJobOIDs(t, fixture); !oids[valid] {
		t.Fatalf("the push behind the refused revision has no job: %v", oids)
	}
	refusals := 0
	for _, line := range fixture.logs {
		if strings.Contains(line, "was not admitted") {
			refusals++
		}
	}
	if refusals != 1 {
		t.Fatalf("the refused revision was named %d times, want once: %v", refusals, fixture.logs)
	}
	// The same event twice queues nothing twice.
	coordinator.NotePush(fixture.repositoryID, []PushUpdate{{Ref: "refs/heads/main", New: valid}})
	noErr(t, coordinator.reconcile(fixture.ctx))
	if oids := pushJobOIDs(t, fixture); len(oids) != 1 {
		t.Fatalf("a repeated push event queued another job: %v", oids)
	}
}

// A repository keeps a bounded set of accepted pushes waiting for admission.
// An update the bound drops is named once, so a push that gets no job of its
// own is never silent.
func TestPushRetentionIsBoundedAndReportsWhatItDrops(t *testing.T) {
	fixture := newPushFixture(t, 4)
	for index := 0; index <= maximumPendingPushes; index++ {
		fixture.coordinator.NotePush(fixture.repositoryID, []PushUpdate{{
			Ref: "refs/heads/main", New: fmt.Sprintf("%040x", index),
		}})
	}
	fixture.coordinator.mu.Lock()
	kept := append([]pushUpdate(nil), fixture.coordinator.pendingPushes[fixture.repositoryID]...)
	fixture.coordinator.mu.Unlock()
	if len(kept) != maximumPendingPushes {
		t.Fatalf("retained %d pushes, want %d", len(kept), maximumPendingPushes)
	}
	if kept[0].oid != fmt.Sprintf("%040x", 1) || kept[len(kept)-1].oid != fmt.Sprintf("%040x", maximumPendingPushes) {
		t.Fatalf("retained the wrong pushes: first %s last %s", kept[0].oid, kept[len(kept)-1].oid)
	}
	dropped := 0
	for _, line := range fixture.logs {
		if strings.Contains(line, "was not queued") {
			dropped++
		}
	}
	if dropped != 1 {
		t.Fatalf("the dropped push was named %d times, want once: %v", dropped, fixture.logs)
	}
}
