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
	"sync"
	"testing"
	"time"

	"owngit/internal/checkworkflow"
	"owngit/internal/gitexec"
	"owngit/internal/hostmem"
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
	// logs collects the coordinator's log lines. Logf is called from the
	// coordinator loops and from the test goroutine, so appends are serialized.
	logs   []string
	logsMu sync.Mutex
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
		Logf: fixture.recordLog,
	}
	// Tests drive the loops themselves, so mark the coordinator as started and
	// drain retained push events with admitPendingPushes. A coordinator that
	// never started keeps nothing, which TestPushRetentionIsBoundedAndReportsWhatItDrops
	// checks separately.
	fixture.coordinator.wake = make(chan struct{}, 1)
	return fixture
}

// recordLog collects one log line.
func (fixture *pushFixture) recordLog(format string, arguments ...any) {
	fixture.logsMu.Lock()
	defer fixture.logsMu.Unlock()
	fixture.logs = append(fixture.logs, fmt.Sprintf(format, arguments...))
}

// logLines returns the collected log lines that contain every phrase.
func (fixture *pushFixture) logLines(phrases ...string) []string {
	fixture.t.Helper()
	fixture.logsMu.Lock()
	defer fixture.logsMu.Unlock()
	var matching []string
	for _, line := range fixture.logs {
		found := true
		for _, phrase := range phrases {
			if !strings.Contains(line, phrase) {
				found = false
				break
			}
		}
		if found {
			matching = append(matching, line)
		}
	}
	return matching
}

// retained copies the push updates the repository keeps for a later attempt.
func (fixture *pushFixture) retained() []pushUpdate {
	fixture.t.Helper()
	fixture.coordinator.mu.Lock()
	defer fixture.coordinator.mu.Unlock()
	return append([]pushUpdate(nil), fixture.coordinator.pendingPushes[fixture.repositoryID]...)
}

// passes drains retained push events and runs reconciliation passes, as the
// running coordinator's admission and loop goroutines do.
func (fixture *pushFixture) passes(count int) {
	fixture.t.Helper()
	for pass := 0; pass < count; pass++ {
		fixture.coordinator.admitPendingPushes(fixture.ctx)
		noErr(fixture.t, fixture.coordinator.reconcile(fixture.ctx))
	}
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

// A checks file above the read bound is refused before Git reconstructs it,
// and the refusal names that reason where other workflow refusals appear.
func TestWorkflowAboveTheReadBoundIsRefusedWithItsReason(t *testing.T) {
	saved := hostmem.Ceiling
	hostmem.Ceiling = func() uint64 { return 512 << 20 }
	defer func() { hostmem.Ceiling = saved }()

	fixture := newPushFixture(t, 4)
	bound := fixture.coordinator.Repositories.Git.ReadBound()
	if bound == 0 {
		t.Fatal("the forced ceiling gave no read bound")
	}
	fixture.pushWorkflow("main", strings.Repeat("a", int(bound)+1))
	noErr(t, fixture.coordinator.reconcile(fixture.ctx))
	if refs := fixture.jobRefs(); len(refs) != 0 {
		t.Fatalf("admitted jobs for %v, want none", refs)
	}
	reported := false
	for _, line := range fixture.logs {
		if strings.Contains(line, "was not admitted") && strings.Contains(line, "above the server read bound") {
			reported = true
		}
	}
	if !reported {
		t.Fatalf("the refusal reason is not in the log: %v", fixture.logs)
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
	fixture.coordinator.admitPendingPushes(fixture.ctx)
	if later, err := fixture.store.LatestCheckJobs(fixture.ctx, fixture.repositoryID, 10); err != nil || len(later) != 1 {
		t.Fatalf("a full queue admitted jobs=%v err=%v", later, err)
	}
	refusals := len(fixture.logLines("was not admitted"))
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

// A push event that is off admits nothing and keeps nothing, so turning it on
// later admits the heads that arrived meanwhile, and a head that already has a
// job stays unqueued.
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
	fixture.passes(2)
	if observed := fixture.observed(); len(observed) != 0 {
		t.Fatalf("observations while push was off: %v", observed)
	}
	if refs := fixture.jobRefs(); len(refs) != 0 {
		t.Fatalf("the push facts of a disabled event admitted %v", refs)
	}
	policy(checkworkflow.EventPush, checkworkflow.EventPullRequest)
	fixture.passes(2)
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
	if testing.Short() {
		t.Skip("pushes 65 branches through real Git")
	}
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
	coordinator.admitPendingPushes(fixture.ctx)
	noErr(t, coordinator.reconcile(fixture.ctx))
	if refs := fixture.jobRefs(); len(refs) != 1 || refs[0] != "main" {
		t.Fatalf("jobs=%v, want only main", refs)
	}
	if oids := pushJobOIDs(t, fixture); !oids[valid] {
		t.Fatalf("the push behind the refused revision has no job: %v", oids)
	}
	refusals := len(fixture.logLines("was not admitted"))
	if refusals != 1 {
		t.Fatalf("the refused revision was named %d times, want once: %v", refusals, fixture.logs)
	}
	// The same event twice queues nothing twice.
	coordinator.NotePush(fixture.repositoryID, []PushUpdate{{Ref: "refs/heads/main", New: valid}})
	coordinator.admitPendingPushes(fixture.ctx)
	noErr(t, coordinator.reconcile(fixture.ctx))
	if oids := pushJobOIDs(t, fixture); len(oids) != 1 {
		t.Fatalf("a repeated push event queued another job: %v", oids)
	}
}

// A repository keeps a bounded set of accepted pushes waiting for admission.
// A push that updates many branches writes one summary line that names what it
// dropped, and a coordinator that has not started keeps and names nothing.
func TestPushRetentionIsBoundedAndReportsWhatItDrops(t *testing.T) {
	fixture := newPushFixture(t, 4)
	updates := make([]PushUpdate, 0, maximumPendingPushes+36)
	for index := 0; index < maximumPendingPushes+36; index++ {
		updates = append(updates, PushUpdate{
			Ref: fmt.Sprintf("refs/heads/b-%02d", index), New: fmt.Sprintf("%040x", index),
		})
	}
	fixture.coordinator.NotePush(fixture.repositoryID, updates)
	kept := fixture.retained()
	if len(kept) != maximumPendingPushes {
		t.Fatalf("retained %d pushes, want %d", len(kept), maximumPendingPushes)
	}
	if kept[0].oid != fmt.Sprintf("%040x", 36) || kept[len(kept)-1].oid != fmt.Sprintf("%040x", maximumPendingPushes+35) {
		t.Fatalf("retained the wrong pushes: first %s last %s", kept[0].oid, kept[len(kept)-1].oid)
	}
	summaries := fixture.logLines("dropped")
	if len(summaries) != 1 {
		t.Fatalf("a 100-branch push wrote %d summary lines, want one: %v", len(summaries), fixture.logs)
	}
	if !strings.Contains(summaries[0], "dropped 36 of 100 updates") || !strings.Contains(summaries[0], "b-00") || !strings.Contains(summaries[0], "b-35") {
		t.Fatalf("the summary does not name the count and the first and last dropped update: %s", summaries[0])
	}
	// A second push names its own drops once, whatever the number of branches.
	more := make([]PushUpdate, 0, 10)
	for index := 0; index < 10; index++ {
		more = append(more, PushUpdate{Ref: fmt.Sprintf("refs/heads/c-%02d", index), New: fmt.Sprintf("%040x", 200+index)})
	}
	fixture.coordinator.NotePush(fixture.repositoryID, more)
	if summaries := fixture.logLines("dropped"); len(summaries) != 2 {
		t.Fatalf("two pushes wrote %d summary lines, want one each: %v", len(summaries), fixture.logs)
	}
	// All repositories together keep a bounded set too: at the total, an
	// arrival that would grow the set is dropped and named once.
	crowded := &Coordinator{Store: fixture.store, Repositories: fixture.coordinator.Repositories, Logf: fixture.recordLog}
	crowded.wake = make(chan struct{}, 1)
	for index := 0; index < maximumPendingPushTotal; index++ {
		crowded.NotePush(fmt.Sprintf("repository-%04d", index/maximumPendingPushes), []PushUpdate{
			{Ref: "refs/heads/main", New: fmt.Sprintf("%040x", 10_000+index)},
		})
	}
	crowded.NotePush(fixture.repositoryID, more[:2])
	crowded.mu.Lock()
	total := 0
	for _, waiting := range crowded.pendingPushes {
		total += len(waiting)
	}
	crowded.mu.Unlock()
	if total != maximumPendingPushTotal {
		t.Fatalf("all repositories retained %d updates, want %d", total, maximumPendingPushTotal)
	}
	if summaries := fixture.logLines("dropped 2 of 2 updates", "across repositories", "c-00", "c-01"); len(summaries) != 1 {
		t.Fatalf("the arrival beyond the total was not named once: %v", fixture.logs)
	}
	// An update no observation or job can name is not retained. The head pass
	// names such a branch once, and a push to it names nothing.
	unrecordable := &Coordinator{Store: fixture.store, Repositories: fixture.coordinator.Repositories, Logf: fixture.recordLog}
	unrecordable.wake = make(chan struct{}, 1)
	unrecordable.NotePush(fixture.repositoryID, []PushUpdate{{Ref: "refs/heads/" + strings.Repeat("a", 100) + "/" + strings.Repeat("a", 100) + "/" + strings.Repeat("a", 100), New: fmt.Sprintf("%040x", 4242)}})
	unrecordable.mu.Lock()
	unrecorded := unrecordable.pendingPushes
	unrecordable.mu.Unlock()
	if len(unrecorded) != 0 {
		t.Fatalf("a branch name no job can carry was retained: %v", unrecorded)
	}
	// No loop would admit an update while the coordinator is not running, so a
	// coordinator that has not started, or has stopped, keeps and names nothing.
	stopped := &Coordinator{Store: fixture.store, Repositories: fixture.coordinator.Repositories, Logf: fixture.recordLog}
	before := len(fixture.logs)
	stopped.NotePush(fixture.repositoryID, updates)
	stopped.mu.Lock()
	pending := stopped.pendingPushes
	stopped.mu.Unlock()
	if len(pending) != 0 {
		t.Fatalf("a coordinator that has not started retained %d updates", len(pending))
	}
	if len(fixture.logs) != before {
		t.Fatalf("a coordinator that has not started logged %v", fixture.logs[before:])
	}
}

// A push still holds the repository write lock while its handler returns.
// Admission waits briefly for the writer instead of keeping the event, and the
// events become jobs in order once the repository is free. The wait never
// delays another repository: its event is admitted while the writer still
// holds the first one.
func TestPushEventWaitsForThePushThatHoldsTheRepository(t *testing.T) {
	fixture := newPushFixture(t, 4)
	older := fixture.pushWorkflow("main", validWorkflow)
	oid := fixture.pushWorkflow("main", validWorkflow)
	coordinator := fixture.coordinator
	other, err := coordinator.Repositories.Create(fixture.ctx, "other", "")
	noErr(t, err)
	otherPath, err := coordinator.Repositories.Path(other.ID)
	noErr(t, err)
	fixture.git("-C", fixture.work, "push", otherPath, "HEAD:refs/heads/main")
	now := time.Now().UTC()
	_, err = fixture.store.SetCheckPolicy(fixture.ctx, state.CheckPolicyInput{
		RepositoryID: other.ID, Executor: state.CheckExecutorHost,
		AllowedEvents: []string{checkworkflow.EventPush}, MaxTimeoutMS: 60_000, MaxOutputLimitBytes: 64 << 10,
		QueueLimit: 4, MaxActiveJobs: 1, MaxLeaseMS: 60_000,
	}, now)
	noErr(t, err)
	_, err = fixture.store.GrantCheckConsent(fixture.ctx, other.ID, now.Add(time.Second))
	noErr(t, err)
	// Hold the repository the drain reaches first, so a drain that waited for
	// it would delay the other one.
	held, free := fixture.repositoryID, other.ID
	if free < held {
		held, free = free, held
	}
	lock := coordinator.Repositories.Locks.For(held)
	lock.Lock()
	unlock := sync.OnceFunc(lock.Unlock)
	defer unlock()
	coordinator.NotePush(held, []PushUpdate{{Ref: "refs/heads/main", New: older}})
	coordinator.NotePush(held, []PushUpdate{{Ref: "refs/heads/main", New: oid}})
	coordinator.NotePush(free, []PushUpdate{{Ref: "refs/heads/main", New: oid}})
	admitted := make(chan bool, 1)
	go func() { admitted <- coordinator.admitPendingPushes(fixture.ctx) }()
	jobOIDs := func(repositoryID string) []string {
		jobs, err := fixture.store.CheckJobs(fixture.ctx, repositoryID)
		noErr(t, err)
		oids := make([]string, 0, len(jobs))
		for _, job := range jobs {
			oids = append(oids, job.SourceOID)
		}
		return oids
	}
	for deadline := time.Now().Add(10 * time.Second); len(jobOIDs(free)) == 0; {
		if time.Now().After(deadline) {
			t.Fatal("a repository a push held delayed the event of another repository")
		}
		time.Sleep(20 * time.Millisecond)
	}
	// A drain that returned here did not wait for the writer, whatever it did
	// with the held repository's events.
	select {
	case <-admitted:
		t.Fatal("admission returned while a push held the repository")
	default:
	}
	if oids := jobOIDs(held); len(oids) != 0 {
		t.Fatalf("a busy repository admitted %v", oids)
	}
	unlock()
	select {
	case retry := <-admitted:
		if retry {
			t.Fatal("the events were kept although the repository became free")
		}
	case <-time.After(30 * time.Second):
		t.Fatal("admission did not finish after the repository became free")
	}
	if oids := jobOIDs(held); len(oids) != 2 || oids[0] != older || oids[1] != oid {
		t.Fatalf("the held repository admitted %v, want %s then %s", oids, older, oid)
	}
}

// An event an attempt could not decide goes back in front of updates that
// arrived while it was decided, so admission stays oldest first, and a
// repository at its bound drops the newest arrival instead of the event it was
// retrying.
func TestKeptPushEventGoesInFrontOfNewerUpdates(t *testing.T) {
	fixture := newPushFixture(t, 8)
	oldest := fixture.pushWorkflow("main", validWorkflow)
	newer := fixture.pushWorkflow("main", validWorkflow)
	newest := fixture.pushWorkflow("main", validWorkflow)
	coordinator := fixture.coordinator
	coordinator.NotePush(fixture.repositoryID, []PushUpdate{{Ref: "refs/heads/main", New: newer}})
	coordinator.NotePush(fixture.repositoryID, []PushUpdate{{Ref: "refs/heads/main", New: newest}})
	coordinator.keepPushes(fixture.repositoryID, []pushUpdate{{ref: "refs/heads/main", oid: oldest}}, true)
	kept := fixture.retained()
	if len(kept) != 3 || kept[0].oid != oldest || kept[1].oid != newer || kept[2].oid != newest {
		t.Fatalf("retained %v, want the oldest update first", kept)
	}
	coordinator.admitPendingPushes(fixture.ctx)
	jobs, err := fixture.store.CheckJobs(fixture.ctx, fixture.repositoryID)
	noErr(t, err)
	order := make([]string, 0, len(jobs))
	for _, job := range jobs {
		order = append(order, job.SourceOID)
	}
	if len(order) != 3 || order[0] != oldest || order[1] != newer || order[2] != newest {
		t.Fatalf("jobs were admitted in order %v, want the oldest push first", order)
	}
	// The same rule holds at the bound: an older event goes in front, and the
	// newest arrival is the one the repository drops.
	filler := make([]PushUpdate, 0, maximumPendingPushes)
	for index := 0; index < maximumPendingPushes; index++ {
		filler = append(filler, PushUpdate{Ref: "refs/heads/main", New: fmt.Sprintf("%040x", 3000+index)})
	}
	coordinator.NotePush(fixture.repositoryID, filler)
	replayed := pushUpdate{ref: "refs/heads/main", oid: fmt.Sprintf("%040x", 2000)}
	coordinator.keepPushes(fixture.repositoryID, []pushUpdate{replayed}, true)
	kept = fixture.retained()
	if len(kept) != maximumPendingPushes || kept[0] != replayed {
		t.Fatalf("retained %d updates, want the older update in front: %v", len(kept), kept)
	}
	if kept[len(kept)-1].oid != fmt.Sprintf("%040x", 3000+maximumPendingPushes-2) {
		t.Fatalf("the repository dropped %s, want its newest arrival", kept[len(kept)-1].oid)
	}
	dropped := fixture.logLines("dropped")
	if len(dropped) != 1 || !strings.Contains(dropped[0], fmt.Sprintf("%040x", 3000+maximumPendingPushes-1)) {
		t.Fatalf("the newest arrival was not named once as dropped: %v", fixture.logs)
	}
}

// A Git or state failure is not a decision, so the event waits for a later
// attempt, like a head the head pass could not decide, and admission succeeds
// once the failure passes.
func TestPushEventKeepsATransientAdmissionFailure(t *testing.T) {
	fixture := newPushFixture(t, 8)
	oid := fixture.pushWorkflow("main", validWorkflow)
	noErr(t, fixture.store.Exec(fixture.ctx, `CREATE TRIGGER refuse_admission BEFORE INSERT ON check_jobs BEGIN SELECT RAISE(ABORT, 'synthetic job write failure'); END`))
	coordinator := fixture.coordinator
	coordinator.NotePush(fixture.repositoryID, []PushUpdate{{Ref: "refs/heads/main", New: oid}})
	if retry := coordinator.admitPendingPushes(fixture.ctx); !retry {
		t.Fatal("a transient failure did not keep the event for a later attempt")
	}
	if oids := pushJobOIDs(t, fixture); len(oids) != 0 {
		t.Fatalf("a failed admission queued %v", oids)
	}
	if kept := fixture.retained(); len(kept) != 1 || kept[0].oid != oid {
		t.Fatalf("retained %v, want the event a transient failure stopped", kept)
	}
	if kept := fixture.logLines("for a later attempt"); len(kept) != 1 {
		t.Fatalf("the kept event was named %d times, want once: %v", len(kept), fixture.logs)
	}
	// The failure passes, and the retained event is admitted then.
	noErr(t, fixture.store.Exec(fixture.ctx, `DROP TRIGGER refuse_admission`))
	if retry := coordinator.admitPendingPushes(fixture.ctx); retry {
		t.Fatal("the drain reported a waiting event after the failure passed")
	}
	if oids := pushJobOIDs(t, fixture); !oids[oid] {
		t.Fatalf("the kept push has no job after the failure passed: %v", oids)
	}
}
