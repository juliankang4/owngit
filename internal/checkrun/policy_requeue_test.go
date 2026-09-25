package checkrun

import (
	"fmt"
	"testing"
	"time"

	"owngit/internal/checkworkflow"
	"owngit/internal/pullrequest"
	"owngit/internal/state"
)

// setPolicyVersion saves a new policy version and consents to it, as saving
// and enabling a policy does.
func (fixture *pushFixture) setPolicyVersion(timeoutMS int64) {
	fixture.t.Helper()
	now := time.Now().UTC()
	if _, err := fixture.store.SetCheckPolicy(fixture.ctx, state.CheckPolicyInput{
		RepositoryID: fixture.repositoryID, Executor: state.CheckExecutorExternalRunner,
		AllowedEvents: []string{checkworkflow.EventPush, checkworkflow.EventPullRequest}, MaxTimeoutMS: timeoutMS, MaxOutputLimitBytes: 64 << 10,
		QueueLimit: 1000, MaxActiveJobs: 1, MaxLeaseMS: 60_000,
	}, now); err != nil {
		fixture.t.Fatal(err)
	}
	if _, err := fixture.store.GrantCheckConsent(fixture.ctx, fixture.repositoryID, now.Add(time.Second)); err != nil {
		fixture.t.Fatal(err)
	}
}

func (fixture *pushFixture) settle() int {
	fixture.t.Helper()
	for pass := 0; pass < 4; pass++ {
		noErr(fixture.t, fixture.coordinator.reconcile(fixture.ctx))
	}
	jobs, err := fixture.store.CheckJobs(fixture.ctx, fixture.repositoryID)
	noErr(fixture.t, err)
	return len(jobs)
}

// With more branches than the bounded observation set, heads lost their
// observation without moving. Each saved and enabled policy version then
// queued them again, and every open pull request revision too, which filled
// the queue ahead of new pushes. A head or revision is now queued once.
func TestPolicyChangeDoesNotRequeueHeadsThatAlreadyHadAJob(t *testing.T) {
	fixture := newPushFixture(t, 1000)
	fixture.coordinator.PullRequests = &pullrequest.Service{Store: fixture.store, Repositories: fixture.coordinator.Repositories}
	fixture.setPolicyVersion(60_000)
	fixture.pushWorkflow("main", `{"version":1,"events":{"push":{},"pull_request":{}},"checks":[{"name":"n","command":"true"}]}`)
	branches := maximumObservedRefs + 6
	for index := 0; index < branches; index++ {
		fixture.git("-C", fixture.work, "push", "-q", fixture.repoPath, fmt.Sprintf("HEAD:refs/heads/ci-%02d", index))
	}
	fixture.git("-C", fixture.work, "commit", "--allow-empty", "-m", "feature")
	fixture.git("-C", fixture.work, "push", "-q", fixture.repoPath, "HEAD:refs/heads/feature")
	if _, err := fixture.coordinator.PullRequests.Create(fixture.ctx, pullrequest.CreateInput{
		Repository: fixture.repositoryID, Title: "Feature", SourceBranch: "feature", TargetBranch: "main",
	}); err != nil {
		t.Fatal(err)
	}
	initial := fixture.settle()
	if want := branches + 2 + 1; initial != want {
		t.Fatalf("initial jobs=%d, want one per branch head and one for the pull request (%d)", initial, want)
	}
	for version := int64(1); version <= 2; version++ {
		fixture.setPolicyVersion(60_000 - version)
		if jobs := fixture.settle(); jobs != initial {
			t.Fatalf("policy version change %d queued %d more jobs", version, jobs-initial)
		}
	}
	// A branch that moves is a new event and is still queued.
	fixture.git("-C", fixture.work, "commit", "--allow-empty", "-m", "moved")
	fixture.git("-C", fixture.work, "push", "-q", fixture.repoPath, "HEAD:refs/heads/ci-03")
	if jobs := fixture.settle(); jobs != initial+1 {
		t.Fatalf("a moved branch queued %d jobs, want 1", jobs-initial)
	}
}
