package server

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

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
