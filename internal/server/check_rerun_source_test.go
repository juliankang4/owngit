package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"owngit/internal/checkworkflow"
	"owngit/internal/state"
	"owngit/internal/webui"
)

// finishCheckJob ends the oldest pending job before it starts.
func finishCheckJob(t *testing.T, fixture apiFixture) {
	t.Helper()
	claimed, found, err := fixture.store.ClaimLocalCheckJob(context.Background(), "project", fixture.app.now())
	if err != nil || !found {
		t.Fatalf("claim: err=%v found=%v", err, found)
	}
	if _, err := fixture.store.FailCheckJobBeforeStart(context.Background(), state.CheckJobCompletionAuthority{
		JobID: claimed.ID, LeaseID: claimed.LeaseID,
		CredentialID: claimed.CredentialID, CredentialGeneration: claimed.CredentialGeneration,
	}, state.CheckJobError, "The run ended before execution began.", fixture.app.now()); err != nil {
		t.Fatal(err)
	}
}

func unfinishedCheckJobs(t *testing.T, fixture apiFixture) int {
	t.Helper()
	jobs, err := fixture.store.LatestCheckJobs(context.Background(), "project", 100)
	noErr(t, err)
	count := 0
	for _, job := range jobs {
		if job.FinishedAt == nil {
			count++
		}
	}
	return count
}

// A rerun records its job only under the repository read lock after finding
// the job's commit, so it cannot slip in while unused object cleanup holds
// the write lock after finding no unfinished job. A commit that is no longer
// in the repository refuses the rerun in the API and in the browser.
func TestCheckRerunWaitsForTheRepositoryAndNeedsItsCommit(t *testing.T) {
	fixture := newAPIFixture(t, false)
	server, client, jar := openBrowser(t, fixture)
	_, present := admitEnabledJob(t, fixture, server.URL, client, jar, "cc-rerun-present",
		jobFacts{ref: "feature", sourceOID: fixture.sourceOID})
	finishCheckJob(t, fixture)
	base := server.URL + "/api/v1/repositories/project/check-jobs/"

	lock := fixture.app.Repositories.Locks.For("project")
	lock.Lock()
	answered := make(chan int, 1)
	go func() {
		status, _ := checkStatus(t, adminAPIRequest(t, http.MethodPost, base+present.ID+"/rerun", map[string]any{}, "admin-password"))
		answered <- status
	}()
	time.Sleep(300 * time.Millisecond)
	waiting := unfinishedCheckJobs(t, fixture)
	lock.UnlockWithoutRefChanges()
	if waiting != 0 {
		t.Fatalf("a rerun was recorded while the repository write lock was held: %d unfinished", waiting)
	}
	if status := <-answered; status != http.StatusOK || unfinishedCheckJobs(t, fixture) != 1 {
		t.Fatalf("rerun after the lock: status=%d unfinished=%d", status, unfinishedCheckJobs(t, fixture))
	}
	finishCheckJob(t, fixture)

	// A commit that cleanup removed, or that never was in the repository.
	csrf, missing := admitEnabledJob(t, fixture, server.URL, client, jar, "cc-rerun-missing",
		jobFacts{ref: "main", sourceOID: strings.Repeat("c", 40)})
	finishCheckJob(t, fixture)
	if status, code := checkStatus(t, adminAPIRequest(t, http.MethodPost, base+missing.ID+"/rerun", map[string]any{}, "admin-password")); status != http.StatusConflict || code != "check_source_missing" {
		t.Fatalf("API rerun of a missing commit: status=%d code=%q", status, code)
	}
	result := browserForm(t, client, server.URL+configuredChecksURL("project"), url.Values{
		"csrf": {csrf}, "action": {webui.ActionRerunCheckJob},
		"admin_password": {"admin-password"}, "job_id": {missing.ID},
	}, server.URL)
	if result.status != http.StatusConflict || !strings.Contains(noticeRegion(t, result.body), browserText(webui.MsgCCJobSourceMissing)) {
		t.Fatalf("browser rerun of a missing commit: status=%d", result.status)
	}
	if unfinished := unfinishedCheckJobs(t, fixture); unfinished != 0 {
		t.Fatalf("a refused rerun queued %d jobs", unfinished)
	}
}

// A rerun starts from what the job's workflow asked for, not from the limits
// the first run ended with, so raising the policy's cap lets the rerun run
// longer. A workflow file that no longer matches the recorded one refuses it.
func TestCheckRerunAppliesTheCurrentCapsToTheWorkflowRequest(t *testing.T) {
	fixture := newAPIFixture(t, false)
	ctx := context.Background()
	workflow := []byte(`{"version":1,"events":{"push":{}},"checks":[{"name":"unit","command":"exit 0"}],"limits":{"timeout_ms":120000}}`)
	noErr(t, os.MkdirAll(filepath.Join(fixture.work, ".owngit"), 0o700))
	noErr(t, os.WriteFile(filepath.Join(fixture.work, checkworkflow.Path), workflow, 0o600))
	apiRunGit(t, fixture.work, "add", ".")
	apiRunGit(t, fixture.work, "commit", "-m", "workflow")
	apiRunGit(t, fixture.work, "push", "origin", "HEAD:refs/heads/ci")
	oid := apiGitOutput(t, fixture.work, "rev-parse", "HEAD")
	blobOID := apiGitOutput(t, fixture.work, "rev-parse", "HEAD:"+checkworkflow.Path)
	digest := sha256.Sum256(workflow)

	now := fixture.app.now()
	setCap := func(timeoutMS int64) {
		_, err := fixture.store.SetCheckPolicy(ctx, state.CheckPolicyInput{
			RepositoryID: "project", Executor: state.CheckExecutorExternalRunner, AllowedEvents: []string{"push"},
			MaxTimeoutMS: timeoutMS, MaxOutputLimitBytes: 65536, QueueLimit: 4, MaxActiveJobs: 1, MaxLeaseMS: 60000,
		}, now)
		noErr(t, err)
		_, err = fixture.store.GrantCheckConsent(ctx, "project", now)
		noErr(t, err)
	}
	admit := func(recordedDigest string) state.CheckJob {
		job, _, err := fixture.store.AdmitCheckJob(ctx, state.CheckJobRequest{
			RepositoryID: "project", Trigger: "push", EventKey: "refs/heads/ci@" + oid + recordedDigest[:4], SourceOID: oid, TriggerRef: "ci",
			WorkflowPath: checkworkflow.Path, WorkflowOID: blobOID, WorkflowDigest: recordedDigest, TimeoutMS: 120000,
			Checks: []state.CheckDefinition{{Name: "unit", Command: "exit 0"}},
		}, now)
		noErr(t, err)
		_, err = fixture.store.CancelCheckJob(ctx, "project", job.ID, now)
		noErr(t, err)
		return job
	}
	setCap(30000)
	job := admit(hex.EncodeToString(digest[:]))
	if job.Limits.TimeoutMS != 30000 {
		t.Fatalf("limits=%+v", job.Limits)
	}
	setCap(90000)
	rerun, _, err := fixture.app.rerunCheckJob(ctx, "project", job.ID)
	if err != nil || rerun.Limits.TimeoutMS != 90000 {
		t.Fatalf("rerun after raising the cap: limits=%+v err=%v", rerun.Limits, err)
	}
	other := admit(strings.Repeat("d", 64))
	if _, _, err := fixture.app.rerunCheckJob(ctx, "project", other.ID); !errors.Is(err, errRerunWorkflowChanged) {
		t.Fatalf("rerun of a job whose workflow differs: err=%v", err)
	}
}
