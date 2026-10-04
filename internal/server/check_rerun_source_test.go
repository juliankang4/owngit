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

// pushWorkflowCommit commits workflow on a new branch of the fixture and
// returns the commit, the workflow blob and the workflow's SHA-256 digest.
func pushWorkflowCommit(t *testing.T, fixture apiFixture, workflow []byte) (string, string, string) {
	t.Helper()
	noErr(t, os.MkdirAll(filepath.Join(fixture.work, ".owngit"), 0o700))
	noErr(t, os.WriteFile(filepath.Join(fixture.work, checkworkflow.Path), workflow, 0o600))
	apiRunGit(t, fixture.work, "add", ".")
	apiRunGit(t, fixture.work, "commit", "-m", "workflow")
	apiRunGit(t, fixture.work, "push", "origin", "HEAD:refs/heads/ci")
	digest := sha256.Sum256(workflow)
	return apiGitOutput(t, fixture.work, "rev-parse", "HEAD"), apiGitOutput(t, fixture.work, "rev-parse", "HEAD:"+checkworkflow.Path), hex.EncodeToString(digest[:])
}

const limitedWorkflow = `{"version":1,"events":{"push":{},"pull_request":{}},"checks":[{"name":"unit","command":"exit 0"}],"limits":{"timeout_ms":120000}}`

// A rerun starts from what the job's workflow asked for, not from the limits
// the first run ended with, so raising the policy's cap lets the rerun run
// longer. A pull request job's workflow is the one at its source commit, which
// differs from the base's, and its rerun reads that same file. A workflow file
// that no longer matches the recorded one refuses the rerun.
func TestCheckRerunAppliesTheCurrentCapsToTheWorkflowRequest(t *testing.T) {
	fixture := newAPIFixture(t, false)
	ctx := context.Background()
	oid, blobOID, digest := pushWorkflowCommit(t, fixture, []byte(limitedWorkflow))

	now := fixture.app.now()
	setCap := func(timeoutMS int64) {
		_, err := fixture.store.SetCheckPolicy(ctx, state.CheckPolicyInput{
			RepositoryID: "project", Executor: state.CheckExecutorExternalRunner, AllowedEvents: []string{"push", "pull_request"},
			MaxTimeoutMS: timeoutMS, MaxOutputLimitBytes: 65536, QueueLimit: 4, MaxActiveJobs: 1, MaxLeaseMS: 60000,
		}, now)
		noErr(t, err)
		_, err = fixture.store.GrantCheckConsent(ctx, "project", now)
		noErr(t, err)
	}
	admit := func(request state.CheckJobRequest, recordedDigest string) state.CheckJob {
		request.RepositoryID, request.SourceOID, request.WorkflowPath = "project", oid, checkworkflow.Path
		request.WorkflowOID, request.WorkflowDigest, request.TimeoutMS = blobOID, recordedDigest, 120000
		request.Checks = []state.CheckDefinition{{Name: "unit", Command: "exit 0"}}
		job, _, err := fixture.store.AdmitCheckJob(ctx, request, now)
		noErr(t, err)
		_, err = fixture.store.CancelCheckJob(ctx, "project", job.ID, now)
		noErr(t, err)
		return job
	}
	push := state.CheckJobRequest{Trigger: "push", EventKey: "refs/heads/ci@" + oid, TriggerRef: "ci"}
	setCap(30000)
	job := admit(push, digest)
	if job.Limits.TimeoutMS != 30000 {
		t.Fatalf("limits=%+v", job.Limits)
	}
	setCap(90000)
	rerun, _, err := fixture.app.rerunCheckJob(ctx, "project", job.ID)
	if err != nil || rerun.Limits.TimeoutMS != 90000 {
		t.Fatalf("rerun after raising the cap: limits=%+v err=%v", rerun.Limits, err)
	}
	// The base commit has no workflow file; the head's workflow is the one read.
	pullRequest := admit(state.CheckJobRequest{
		Trigger: "pull_request", EventKey: "pr/1/" + oid + "/" + fixture.targetOID, BaseOID: fixture.targetOID,
		PullRequestNumber: 1, TriggerRef: "main",
	}, digest)
	if rerun, _, err := fixture.app.rerunCheckJob(ctx, "project", pullRequest.ID); err != nil || rerun.Limits.TimeoutMS != 90000 {
		t.Fatalf("pull request rerun: limits=%+v err=%v", rerun.Limits, err)
	}
	other := admit(state.CheckJobRequest{Trigger: "push", EventKey: "refs/heads/ci@" + oid + "#2", TriggerRef: "ci"}, strings.Repeat("d", 64))
	if _, _, err := fixture.app.rerunCheckJob(ctx, "project", other.ID); !errors.Is(err, errRerunWorkflowChanged) {
		t.Fatalf("rerun of a job whose workflow differs: err=%v", err)
	}
}

// A workflow that differs from the recorded one is a refusal the owner can
// read, not the server being unavailable.
func TestBrowserRerunOfAChangedWorkflowIsRefused(t *testing.T) {
	fixture := newAPIFixture(t, false)
	server, client, jar := openBrowser(t, fixture)
	oid, _, _ := pushWorkflowCommit(t, fixture, []byte(limitedWorkflow))
	// admitEnabledJob records a placeholder workflow digest, which differs from the file.
	csrf, job := admitEnabledJob(t, fixture, server.URL, client, jar, "cc-rerun-changed", jobFacts{ref: "ci", sourceOID: oid})
	finishCheckJob(t, fixture)
	result := browserForm(t, client, server.URL+configuredChecksURL("project"), url.Values{
		"csrf": {csrf}, "action": {webui.ActionRerunCheckJob},
		"admin_password": {"admin-password"}, "job_id": {job.ID},
	}, server.URL)
	if result.status != http.StatusConflict || !strings.Contains(noticeRegion(t, result.body), browserText(webui.MsgCCJobWorkflowChanged)) {
		t.Fatalf("browser rerun of a changed workflow: status=%d", result.status)
	}
}

// A job migrated from an earlier version records no source limits. Its rerun
// still reads the workflow, with the default bound, and applies the caps.
func TestCheckRerunOfAMigratedJobReadsTheWorkflow(t *testing.T) {
	fixture := newAPIFixture(t, false)
	ctx := context.Background()
	oid, blobOID, digest := pushWorkflowCommit(t, fixture, []byte(limitedWorkflow))
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
	setCap(30000)
	job, _, err := fixture.store.AdmitCheckJob(ctx, state.CheckJobRequest{
		RepositoryID: "project", Trigger: "push", EventKey: "refs/heads/ci@" + oid, SourceOID: oid, TriggerRef: "ci",
		WorkflowPath: checkworkflow.Path, WorkflowOID: blobOID, WorkflowDigest: digest, TimeoutMS: 120000,
		Checks: []state.CheckDefinition{{Name: "unit", Command: "exit 0"}},
	}, now)
	noErr(t, err)
	_, err = fixture.store.CancelCheckJob(ctx, "project", job.ID, now)
	noErr(t, err)
	noErr(t, fixture.store.Exec(ctx, `UPDATE check_jobs SET execution_json='{"legacy":true}' WHERE id=?`, job.ID))
	// Only the workflow itself says it asked for 120000, so a rerun under the
	// raised cap reaching 90000 shows the workflow was read.
	setCap(90000)
	rerun, _, err := fixture.app.rerunCheckJob(ctx, "project", job.ID)
	if err != nil || rerun.Limits.TimeoutMS != 90000 {
		t.Fatalf("rerun of a migrated job: limits=%+v err=%v", rerun.Limits, err)
	}
}

// The API refuses the rerun of a job whose workflow differs from its record
// with a conflict that names the reason.
func TestAPIRerunOfAChangedWorkflowIsAConflict(t *testing.T) {
	fixture := newAPIFixture(t, false)
	server, client, jar := openBrowser(t, fixture)
	oid, _, _ := pushWorkflowCommit(t, fixture, []byte(limitedWorkflow))
	_, job := admitEnabledJob(t, fixture, server.URL, client, jar, "cc-api-changed", jobFacts{ref: "ci", sourceOID: oid})
	finishCheckJob(t, fixture)
	base := server.URL + "/api/v1/repositories/project/check-jobs/"
	if status, code := checkStatus(t, adminAPIRequest(t, http.MethodPost, base+job.ID+"/rerun", map[string]any{}, "admin-password")); status != http.StatusConflict || code != "check_workflow_changed" {
		t.Fatalf("API rerun of a changed workflow: status=%d code=%q", status, code)
	}
}
