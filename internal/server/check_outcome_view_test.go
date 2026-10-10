package server

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"owngit/internal/pullrequest"
	"owngit/internal/state"
	"owngit/internal/webui"
)

func TestCheckOutcomeAdapters(t *testing.T) {
	fixture := newRunnerReadFixture(t)
	job := fixture.claimAndStart(t)
	completion := passedCompletion(job.LeaseID)
	completion.Completion.Results[0].Status = state.AttemptIncomplete
	completion.Completion.Results[0].Truncated = true
	completion.Completion.Results[0].OutputLimitExceededBytes = 1024
	_, err := fixture.post("jobs/"+job.ID+"/complete", job.LeaseID, completion)
	noErr(t, err)
	job = fixture.read(t)
	attempt, found, err := fixture.store.CheckAttemptByID(fixture.ctx, "project", job.AttemptID)
	if err != nil || !found {
		t.Fatalf("attempt found=%v err=%v", found, err)
	}
	request := httptest.NewRequest("GET", "/", nil)
	detail := fixture.app.browserCheckJobDetail(request, state.Repository{ID: "project", Address: "project"}, job.ID, "/checks")
	if detail.Job.Outcome == nil || detail.Attempt.Outcome == nil || detail.Attempt.Results[0].OutputLimitExceededBytes != 1024 {
		t.Fatalf("missing detail facts: %+v", detail)
	}
	record, err := fixture.app.workflowJobRecord(request, job)
	noErr(t, err)
	row := browserWorkflowJob("project", "run", record, job)
	if row.Outcome == nil || row.Steps[0].OutputLimitExceededBytes != 1024 {
		t.Fatalf("missing workflow facts: %+v", row)
	}
	for _, view := range []workflowJobView{record, workflowJobSummary(record)} {
		encoded, err := json.Marshal(view)
		noErr(t, err)
		if strings.Contains(string(encoded), "Outcome") || !strings.Contains(string(encoded), attempt.Summary) {
			t.Fatalf("changed JSON presentation: %s", encoded)
		}
	}
	facts, ok := attempt.Outcome()
	if !ok {
		t.Fatal("missing verified facts")
	}
	checks := pullrequest.Checks{Outcome: &facts, Summary: attempt.Summary, LogExpiresAt: &attempt.FinishedAt}
	pr := browserCheckEvidence("project", checks, nil)
	if pr.Outcome == nil {
		t.Fatal("missing PR outcome")
	}
	encoded, err := json.Marshal(checks)
	noErr(t, err)
	if strings.Contains(string(encoded), "Outcome") {
		t.Fatal("PR display facts leaked into JSON")
	}
	for _, mutate := range []func(*state.CheckAttempt){
		func(a *state.CheckAttempt) { a.Status = state.AttemptPending },
		func(a *state.CheckAttempt) { a.Summary = "custom <summary>" },
		func(a *state.CheckAttempt) { a.Status = state.AttemptPassed },
	} {
		changed := attempt
		mutate(&changed)
		if browserAttemptOutcome(changed) != nil {
			t.Fatal("unverified outcome was localized")
		}
	}
	cleanup := attempt
	cleanup.Results = []state.CheckResult{{Status: state.AttemptPassed, CleanupError: "recorded cleanup failure"}}
	cleanup.Status = state.AttemptError
	cleanup.Summary = state.AttemptSummary(cleanup.Results, cleanup.EffectiveWorktreeState(), cleanup.Status)
	if outcome := browserAttemptOutcome(cleanup); outcome == nil || outcome.Error != 1 || outcome.Passed != 0 {
		t.Fatal("cleanup error was presented as a pass")
	}
	if browserJobOutcome("other", attempt.Summary, attempt) != nil || browserJobOutcome(attempt.ID, "job state", attempt) != nil {
		t.Fatal("job binding guard missing")
	}

	attempt.FinishedAt = attempt.FinishedAt.In(elsewhereZone())
	retention, err := fixture.store.CheckLogRetention(fixture.ctx)
	noErr(t, err)
	expires := retention.LogExpiry(attempt)
	if expires == nil {
		t.Fatal("fixture has no expiry")
	}
	checks.LogExpiresAt = expires
	for name, got := range map[string]time.Time{
		"attempt": fixture.app.browserAttemptRecord(fixture.ctx, attempt).LogExpiresAt,
		"job log": fixture.app.browserCheckJobLog(fixture.ctx, attempt).ExpiresAt,
		"PR":      browserCheckEvidence("project", checks, nil).LogExpiresAt,
	} {
		if !got.Equal(*expires) || got.Location() != time.Local {
			t.Fatalf("%s expiry=%v location=%v", name, got, got.Location())
		}
	}

	feedRecord := state.FeedRecord{ID: job.ID, RepositoryID: "project", AttemptID: attempt.ID, Message: attempt.Summary}
	for lang, want := range map[webui.Lang]string{webui.LangEN: "1 check: 1 incomplete", webui.LangKO: "체크 1개: 완료되지 않음 1개"} {
		got := fixture.app.recordNotifications(fixture.ctx, trayFeedRequest{lang: lang}, state.NotifyCheckFailed, []state.FeedRecord{feedRecord}, 1)
		if len(got) != 1 || got[0].Body != want {
			t.Fatalf("%s tray=%+v", lang, got)
		}
	}
	for _, kind := range []string{state.NotifyPullRequest, state.NotifyImportFailed, state.NotifyBackupFailed} {
		raw := feedRecord
		raw.Message, raw.Title = "recorded %s <body>", "recorded %s <title>"
		got := fixture.app.recordNotifications(fixture.ctx, trayFeedRequest{lang: webui.LangKO}, kind, []state.FeedRecord{raw}, 1)[0].Body
		want := raw.Message
		if kind == state.NotifyPullRequest {
			want = raw.Title
		}
		if got != want {
			t.Fatalf("%s body=%q", kind, got)
		}
	}
	for _, changed := range []state.FeedRecord{
		{RepositoryID: "project", AttemptID: attempt.ID, Message: "job-specific message"},
		{RepositoryID: "project", AttemptID: strings.Repeat("f", 32), Message: "missing attempt"},
	} {
		got := fixture.app.recordNotifications(fixture.ctx, trayFeedRequest{}, state.NotifyCheckFailed, []state.FeedRecord{changed}, 1)
		if got[0].Body != changed.Message {
			t.Fatal("tray fallback lost")
		}
	}
	cancelled, cancel := context.WithCancel(fixture.ctx)
	cancel()
	got := fixture.app.recordNotifications(cancelled, trayFeedRequest{}, state.NotifyCheckFailed, []state.FeedRecord{feedRecord}, 1)
	if got[0].Body != feedRecord.Message {
		t.Fatal("cancelled read did not retain stored text")
	}
	aggregate := fixture.app.recordNotifications(cancelled, trayFeedRequest{}, state.NotifyCheckFailed, []state.FeedRecord{feedRecord}, 4)
	if len(aggregate) != 1 || aggregate[0].Body != "" {
		t.Fatal("aggregate fetched or exposed an individual body")
	}
}
