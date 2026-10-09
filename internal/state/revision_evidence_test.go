package state

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"

	"owngit/internal/actions"
)

func TestRevisionEvidence(t *testing.T) {
	for _, test := range []struct {
		name                                                                                      string
		statuses                                                                                  []string
		refused, tolerated, neutral, jsonFailed, newRun, laterAttempt, summaryNotes, manyRefusals bool
		want                                                                                      string
	}{
		{name: "failed sibling before passing sibling", statuses: []string{"failed", "passed"}, want: "failed"},
		{name: "partly refused workflow", statuses: []string{"passed"}, refused: true, want: "partial"},
		{name: "unsupported file stays neutral", statuses: []string{"passed"}, neutral: true, want: "passed"},
		{name: "nothing ran", statuses: []string{"skipped", "skipped"}, want: "skipped"},
		{name: "tolerated failure", statuses: []string{"failed"}, tolerated: true, want: "passed"},
		{name: "queued before an attempt", want: "queued"},
		{name: "JSON failure survives passing workflow", statuses: []string{"passed"}, jsonFailed: true, want: "failed"},
		{name: "latest event at the same revision", statuses: []string{"failed"}, newRun: true, want: "queued"},
		{name: "later task attempt selects its newer revision", statuses: []string{"passed"}, laterAttempt: true, want: "passed"},
		{name: "summary notes are explicitly bounded", statuses: []string{"passed"}, summaryNotes: true, want: "passed"},
		{name: "revision summary caps refused runs", statuses: []string{"failed"}, manyRefusals: true, want: "failed"},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			fixture := workflowStore(t)
			count := max(1, len(test.statuses))
			plans := make([]actions.JobPlan, count)
			for i := range plans {
				plans[i] = actions.JobPlan{JobKey: fmt.Sprintf("job%d", i), ContinueOnError: fmt.Sprint(test.tolerated)}
			}
			request := graphRunRequest(t, plans...)
			if test.manyRefusals {
				request.Run.WorkflowPath = ".github/workflows/zzzz.yml"
			}
			if test.tolerated {
				request.Jobs[0].Tolerated = true
			}
			if test.refused {
				request.Run.Facts.RefusedJobs = []actions.RefusedJob{{JobKey: "unsupported", Reason: actions.Message{Code: "workflow.action", Detail: "Replace the action."}}}
			}
			if test.summaryNotes {
				for i := range MaximumActionsRunSummaryNoteKeys + 1 {
					request.Run.Facts.Notes = append(request.Run.Facts.Notes, actions.Message{Code: fmt.Sprintf("note.code_%02d", i)})
				}
			}
			run := admitGraph(t, fixture, request)
			runner, _ := fixture.issueRunner(t)
			jobs, err := fixture.store.ActionsRunJobs(ctx, "project", run.ID)
			noErr(t, err)
			sort.Slice(jobs, func(i, j int) bool { return jobs[i].ID < jobs[j].ID })
			for i, status := range test.statuses {
				completeGraphJob(t, fixture, jobs[i], runner, status)
			}
			if test.neutral {
				refused := workflowRunRequest(t)
				refused.Run.WorkflowPath = ".github/workflows/unsupported.yml"
				refused.Run.Outcome = "refused"
				refused.Run.Reason = "Unsupported event."
				refused.Jobs = nil
				admitGraph(t, fixture, refused)
			}
			if test.jsonFailed {
				job := fixture.admit(t, pushJobRequest())
				claimed, attempt := fixture.claimAndStart(t, job, runner, ProtectionRunnerReported)
				completion := passedCompletion(attempt, fixture.now.Add(time.Second))
				completion.Results[0].Status = AttemptFailed
				_, _, err := fixture.store.CompleteCheckJobAttempt(ctx, completion, authorityFor(job.ID, claimed.LeaseID, runner), fixture.now.Add(time.Second))
				noErr(t, err)
			}
			if test.newRun {
				newer := request
				newer.Run.EventKey += "/manual"
				admitGraph(t, fixture, newer)
			}
			if test.manyRefusals {
				for i := range 40 {
					refused := request
					refused.Run.WorkflowPath = fmt.Sprintf(".github/workflows/%02d.yml", i)
					refused.Run.Outcome = actions.StatusRefused
					refused.Run.Facts.Notes = []actions.Message{{Code: "workflow.limit", Detail: "Workflow files per commit is over OwnGit's limit of 32."}}
					refused.Jobs = nil
					admitGraph(t, fixture, refused)
				}
			}
			evidence, err := fixture.store.RevisionEvidence(ctx, "project", run.SourceOID)
			noErr(t, err)
			if evidence.Conclusion != test.want {
				t.Fatalf("conclusion=%s want=%s evidence=%+v", evidence.Conclusion, test.want, evidence)
			}
			if (evidence.JSONAttempt != nil) != test.jsonFailed {
				t.Fatalf("Actions leaked into the JSON lane: %+v", evidence.JSONAttempt)
			}
			if test.summaryNotes && (len(evidence.Workflows) != 1 || len(evidence.Workflows[0].NoteKeys) != MaximumActionsRunSummaryNoteKeys || !evidence.Workflows[0].NotesTruncated || evidence.Workflows[0].Counts.Notes != MaximumActionsRunSummaryNoteKeys+1) {
				t.Fatalf("bounded summary notes=%+v", evidence.Workflows)
			}
			if test.manyRefusals {
				if len(evidence.Workflows) != MaximumRevisionWorkflowSummaries || evidence.WorkflowsTotal != 41 || !evidence.WorkflowsTruncated {
					t.Fatalf("bounded workflow summaries=%+v", evidence)
				}
				for _, summary := range evidence.Workflows {
					if summary.WorkflowPath == request.Run.WorkflowPath {
						t.Fatalf("hidden failure appeared in summaries: %+v", evidence.Workflows)
					}
				}
			}
			wantTaskRevision, wantTaskConclusion := run.SourceOID, test.want
			if test.laterAttempt {
				storedTask, found, err := fixture.store.Task(ctx, "project", jobs[0].TaskID)
				if err != nil || !found {
					t.Fatalf("task found=%v error=%v", found, err)
				}
				wantTaskRevision, wantTaskConclusion = strings.Repeat("e", 40), actions.StatusRunning
				_, _, err = fixture.store.RegisterCheckAttempt(ctx, attemptFor(storedTask, wantTaskRevision, fixture.now.Add(time.Hour), AttemptPassed))
				noErr(t, err)
			}
			task, err := fixture.store.TaskRevisionEvidence(ctx, "project", jobs[0].TaskID)
			noErr(t, err)
			if task.Conclusion != wantTaskConclusion || task.RevisionOID != wantTaskRevision {
				t.Fatalf("task=%+v want revision=%s conclusion=%s", task, wantTaskRevision, wantTaskConclusion)
			}
			unknown, err := fixture.store.RevisionEvidence(ctx, "other", run.SourceOID)
			noErr(t, err)
			if unknown.Conclusion != "" || len(unknown.Workflows) != 0 {
				t.Fatalf("cross-repository evidence=%+v", unknown)
			}
		})
	}
}

func TestTaskRevisionEvidenceOrdersRefusalAndJobInTheSameSecond(t *testing.T) {
	for _, test := range []struct {
		name       string
		refuseLast bool
	}{
		{name: "queue refusal follows queued job", refuseLast: true},
		{name: "queued job follows invalid file refusal"},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			fixture := newCheckJobFixture(t)
			fixture.setPolicy(t, func(input *CheckPolicyInput) { input.QueueLimit = 1 })
			policy := fixture.grantConsent(t)
			first := pushJobRequest()
			second := pushJobRequest()
			second.SourceOID = strings.Repeat("c", 40)
			second.EventKey = "refs/heads/main@" + second.SourceOID
			earlier, later := fixture.now.Add(100*time.Millisecond), fixture.now.Add(200*time.Millisecond)
			expected := ExpectedCheckPolicy{Version: policy.Version, Digest: policy.Digest}
			var job CheckJob
			var result CheckEventAdmission
			var err error
			if test.refuseLast {
				job, _, err = fixture.store.AdmitCheckJob(ctx, first, earlier)
				noErr(t, err)
				result, err = fixture.store.AdmitCheckEventWithJSONRefusal(ctx, "project", expected, &second, nil, nil, later)
				noErr(t, err)
				if !result.JSONQueueFull {
					t.Fatalf("expected a full JSON queue: %+v", result)
				}
			} else {
				result, err = fixture.store.AdmitCheckEventWithJSONRefusal(ctx, "project", expected, nil,
					&JSONAdmissionRefusal{Request: first, Reason: "Invalid check file"}, nil, earlier)
				noErr(t, err)
				job, _, err = fixture.store.AdmitCheckJob(ctx, second, later)
				noErr(t, err)
			}
			evidence, err := fixture.store.TaskRevisionEvidence(ctx, "project", job.TaskID)
			noErr(t, err)
			want := second.SourceOID
			if !test.refuseLast {
				want = job.SourceOID
			}
			if evidence.RevisionOID != want {
				t.Fatalf("evidence revision=%q want=%q", evidence.RevisionOID, want)
			}
		})
	}
}

func TestJSONAdmissionRefusalKeepsConfigurationAndRecovery(t *testing.T) {
	ctx := context.Background()
	fixture := newCheckJobFixture(t)
	fixture.setPolicy(t, nil)
	policy := fixture.grantConsent(t)
	first := pushJobRequest()
	_, _, err := fixture.store.AdmitCheckJob(ctx, first, fixture.now)
	noErr(t, err)
	refused := pushJobRequest()
	refused.SourceOID = strings.Repeat("c", 40)
	refused.EventKey = "refs/heads/main@" + refused.SourceOID
	_, err = fixture.store.AdmitCheckEventWithJSONRefusal(ctx, "project", ExpectedCheckPolicy{Version: policy.Version, Digest: policy.Digest}, nil,
		&JSONAdmissionRefusal{Request: refused, Reason: "Invalid check file"}, nil, fixture.now.Add(time.Second))
	noErr(t, err)
	third := pushJobRequest()
	third.SourceOID = strings.Repeat("d", 40)
	third.EventKey = "refs/heads/main@" + third.SourceOID
	_, _, err = fixture.store.AdmitCheckJob(ctx, third, fixture.now.Add(2*time.Second))
	noErr(t, err)
	latest, found, err := fixture.store.LatestCheckConfiguration(ctx, "project")
	noErr(t, err)
	if !found || len(latest.Checks) != 1 || latest.Checks[0].Name != "unit" {
		t.Fatalf("latest configuration after refusal: %+v", latest)
	}
	snapshot, err := fixture.store.RecoverySnapshot(ctx)
	noErr(t, err)
	noErr(t, ValidateCheckRecovery(snapshot))
	restored := openTestStore(t)
	noErr(t, restored.RestoreRecoveryState(ctx, t.TempDir(), snapshot))
	backed, err := restored.RecoverySnapshot(ctx)
	noErr(t, err)
	if len(backed.CheckAttempts) != 1 || backed.CheckAttempts[0].Summary != snapshot.CheckAttempts[0].Summary {
		t.Fatalf("restored refusal=%+v", backed.CheckAttempts)
	}
	plan := stateQueryPlan(t, restored.db, jsonAdmissionRefusalLookup, []any{"project", backed.CheckAttempts[0].TaskID, backed.CheckAttempts[0].LogError})
	if !strings.Contains(plan, "check_attempts_refusal_event (repository_id=? AND task_id=? AND log_error=?)") {
		t.Fatalf("restored refusal lookup does not seek the event: %s", plan)
	}
	_, err = restored.SetCheckPolicy(ctx, defaultPolicyInput(), fixture.now)
	noErr(t, err)
	consent, err := restored.GrantCheckConsent(ctx, "project", fixture.now)
	noErr(t, err)
	expected := ExpectedCheckPolicy{Version: consent.Version, Digest: consent.Digest}
	_, err = restored.AdmitCheckEventWithJSONRefusal(ctx, "project", expected, nil,
		&JSONAdmissionRefusal{Request: refused, Reason: "Replay after restore"}, nil, fixture.now.Add(3*time.Second))
	noErr(t, err)
	otherEvent := refused
	otherEvent.EventKey += "-distinct"
	_, err = restored.AdmitCheckEventWithJSONRefusal(ctx, "project", expected, nil,
		&JSONAdmissionRefusal{Request: otherEvent, Reason: "Different event"}, nil, fixture.now.Add(4*time.Second))
	noErr(t, err)
	after, err := restored.CheckAttempts(ctx, "project")
	noErr(t, err)
	if len(after) != 2 || after[0].ID != backed.CheckAttempts[0].ID || after[1].ID == after[0].ID {
		t.Fatalf("restored refusal replay and distinct event: %+v", after)
	}
}

func TestInvalidJSONRefusalDoesNotUndoJobAdmission(t *testing.T) {
	for _, test := range []struct {
		name    string
		invalid func(*CheckJobRequest)
	}{
		{"missing event key", func(request *CheckJobRequest) { request.EventKey = "" }},
		{"repository mismatch", func(request *CheckJobRequest) { request.RepositoryID = "other" }},
		{"unselected event", func(request *CheckJobRequest) { request.Trigger = "schedule" }},
		{"oversized event key", func(request *CheckJobRequest) { request.EventKey = strings.Repeat("a", MaximumCheckEventKeyBytes+1) }},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			fixture := newCheckJobFixture(t)
			fixture.setPolicy(t, nil)
			policy := fixture.grantConsent(t)
			bad := pushJobRequest()
			test.invalid(&bad)
			jobRequest := pushJobRequest()
			result, err := fixture.store.AdmitCheckEventWithJSONRefusal(ctx, "project", ExpectedCheckPolicy{Version: policy.Version, Digest: policy.Digest},
				&jobRequest, &JSONAdmissionRefusal{Request: bad, Reason: "Invalid check file"}, nil, fixture.now)
			noErr(t, err)
			if result.Job == nil {
				t.Fatal("valid check job was not admitted")
			}
			attempts, err := fixture.store.CheckAttempts(ctx, "project")
			noErr(t, err)
			if len(attempts) != 0 {
				t.Fatalf("invalid refusal made attempts: %+v", attempts)
			}
		})
	}
}

func TestHelperAttemptCannotImitateAdmissionRefusal(t *testing.T) {
	ctx := context.Background()
	fixture := newCheckJobFixture(t)
	fixture.setPolicy(t, nil)
	policy := fixture.grantConsent(t)
	job, _, err := fixture.store.AdmitCheckJob(ctx, pushJobRequest(), fixture.now)
	noErr(t, err)
	refused := pushJobRequest()
	refused.SourceOID = strings.Repeat("c", 40)
	refused.EventKey = "refs/heads/main@" + refused.SourceOID
	preclaimedID := "adf1a15e89732ad09c6319778ae83aa3"
	for _, test := range []struct {
		name, attemptID string
	}{
		{name: "ordinary helper identity", attemptID: strings.Repeat("f", 32)},
		{name: "previous refusal-form identity", attemptID: preclaimedID},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, _, err := fixture.store.RegisterCheckAttempt(ctx, CheckAttempt{
				ID: test.attemptID, TaskID: job.TaskID, RepositoryID: "project", RevisionOID: strings.Repeat("9", 40),
				WorktreeState: WorktreeUnknown, StartedAt: fixture.now.AddDate(50, 0, 0), CreatedAt: fixture.now.Add(time.Second),
				CredentialID: "helper", Checks: []CheckDefinition{{Name: "Admission", Command: "Not run"}},
			})
			noErr(t, err)
			_, _, err = fixture.store.CompleteCheckAttempt(ctx, CheckCompletion{
				AttemptID: test.attemptID, RepositoryID: "project", TaskID: job.TaskID,
				Results:    []CheckResult{{Name: "Admission", Command: "Not run", Status: AttemptUnavailable}},
				FinishedAt: fixture.now.Add(time.Second), WorktreeState: WorktreeUnknown,
			}, fixture.now.Add(time.Second))
			noErr(t, err)
		})
	}
	_, err = fixture.store.AdmitCheckEventWithJSONRefusal(ctx, "project", ExpectedCheckPolicy{Version: policy.Version, Digest: policy.Digest}, nil,
		&JSONAdmissionRefusal{Request: refused, Reason: "Invalid check file"}, nil, fixture.now.Add(2*time.Second))
	noErr(t, err)
	attempts, err := fixture.store.CheckAttempts(ctx, "project")
	noErr(t, err)
	if len(attempts) != 3 {
		t.Fatalf("helper attempt suppressed refusal: %+v", attempts)
	}
	refusal := attempts[2]
	if refusal.ID == preclaimedID || refusal.CredentialID != jsonAdmissionRefusalCredentialID || refusal.RevisionOID != refused.SourceOID || refusal.Status != AttemptUnavailable || !strings.Contains(refusal.Summary, "Invalid check file") {
		t.Fatalf("refusal not recorded: %+v", refusal)
	}
	_, err = fixture.store.AdmitCheckEventWithJSONRefusal(ctx, "project", ExpectedCheckPolicy{Version: policy.Version, Digest: policy.Digest}, nil,
		&JSONAdmissionRefusal{Request: refused, Reason: "Repeated event"}, nil, fixture.now.Add(3*time.Second))
	noErr(t, err)
	replayed := refused
	replayed.SourceOID = strings.Repeat("b", 40)
	_, err = fixture.store.AdmitCheckEventWithJSONRefusal(ctx, "project", ExpectedCheckPolicy{Version: policy.Version, Digest: policy.Digest}, nil,
		&JSONAdmissionRefusal{Request: replayed, Reason: "Repeated event"}, nil, fixture.now.Add(3*time.Second))
	noErr(t, err)
	attempts, err = fixture.store.CheckAttempts(ctx, "project")
	noErr(t, err)
	if len(attempts) != 3 || attempts[2].ID != refusal.ID {
		t.Fatalf("duplicate refusal for the same event: %+v", attempts)
	}
	otherEvent := refused
	otherEvent.EventKey = "refs/heads/other@" + refused.SourceOID
	_, err = fixture.store.AdmitCheckEventWithJSONRefusal(ctx, "project", ExpectedCheckPolicy{Version: policy.Version, Digest: policy.Digest}, nil,
		&JSONAdmissionRefusal{Request: otherEvent, Reason: "Other event"}, nil, fixture.now.Add(4*time.Second))
	noErr(t, err)
	attempts, err = fixture.store.CheckAttempts(ctx, "project")
	noErr(t, err)
	if len(attempts) != 4 || attempts[3].ID == refusal.ID || attempts[3].CredentialID != jsonAdmissionRefusalCredentialID {
		t.Fatalf("distinct event did not get its own refusal: %+v", attempts)
	}
	_, _, err = fixture.store.RegisterCheckAttempt(ctx, CheckAttempt{
		ID: strings.Repeat("d", 32), TaskID: job.TaskID, RepositoryID: "project", RevisionOID: strings.Repeat("8", 40),
		WorktreeState: WorktreeUnknown, StartedAt: fixture.now.AddDate(50, 0, 0), CreatedAt: fixture.now.Add(time.Second),
		CredentialID: jsonAdmissionRefusalCredentialID, Checks: []CheckDefinition{{Name: "Admission", Command: "Not run"}},
	})
	if !errors.Is(err, ErrInvalidCheckJob) {
		t.Fatalf("reserved refusal marker accepted from helper: %v", err)
	}
	later := pushJobRequest()
	later.SourceOID = strings.Repeat("e", 40)
	later.EventKey = "refs/heads/main@" + later.SourceOID
	_, _, err = fixture.store.AdmitCheckJob(ctx, later, fixture.now.Add(time.Hour))
	noErr(t, err)
	evidence, err := fixture.store.TaskRevisionEvidence(ctx, "project", job.TaskID)
	noErr(t, err)
	if evidence.RevisionOID != later.SourceOID {
		t.Fatalf("helper attempt hides newer job: got %s want %s", evidence.RevisionOID, later.SourceOID)
	}
	t.Run("runner attempt with chosen identity", func(t *testing.T) {
		fixture := newCheckJobFixture(t)
		fixture.setPolicy(t, nil)
		policy := fixture.grantConsent(t)
		fixture.admit(t, pushJobRequest())
		runner, _ := fixture.issueRunner(t)
		claimed, found, err := fixture.store.ClaimCheckJob(ctx, "project", runner.ID, fixture.now)
		noErr(t, err)
		if !found {
			t.Fatal("job was not claimed")
		}
		start := fixture.startFor(claimed, claimed.LeaseID, runner)
		start.AttemptID = preclaimedID
		_, started, err := fixture.store.StartCheckJob(ctx, start, fixture.now)
		noErr(t, err)
		if started.ID != preclaimedID {
			t.Fatalf("runner identity changed: %s", started.ID)
		}
		_, err = fixture.store.AdmitCheckEventWithJSONRefusal(ctx, "project", ExpectedCheckPolicy{Version: policy.Version, Digest: policy.Digest}, nil,
			&JSONAdmissionRefusal{Request: refused, Reason: "Invalid check file"}, nil, fixture.now.Add(time.Second))
		noErr(t, err)
		attempts, err := fixture.store.CheckAttempts(ctx, "project")
		noErr(t, err)
		if len(attempts) != 2 || attempts[1].ID == preclaimedID || !isJSONAdmissionRefusal(attempts[1].CredentialID) {
			t.Fatalf("runner attempt suppressed refusal: %+v", attempts)
		}
	})
}

func TestPullRequestRevisionEvidence(t *testing.T) {
	for _, test := range []struct {
		name                           string
		workflowRevision, jsonRevision string
		wantConclusion                 string
		wantJSONStale, wantRunStale    bool
	}{
		{name: "stale workflows", workflowRevision: "old", wantRunStale: true},
		{name: "stale JSON", jsonRevision: "old", wantJSONStale: true},
		{name: "both lanes stale", workflowRevision: "old", jsonRevision: "old", wantJSONStale: true, wantRunStale: true},
		{name: "unrelated evidence", workflowRevision: "unrelated", jsonRevision: "unrelated"},
		{name: "current workflows with stale JSON", workflowRevision: "current", jsonRevision: "old", wantConclusion: actions.StatusIncomplete, wantJSONStale: true},
		{name: "stale workflows with current JSON", workflowRevision: "old", jsonRevision: "current", wantConclusion: actions.StatusIncomplete, wantRunStale: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			fixture := workflowStore(t)
			old, current, target, unrelated := strings.Repeat("a", 40), strings.Repeat("c", 40), strings.Repeat("d", 40), strings.Repeat("e", 40)
			revision := func(name string) string {
				switch name {
				case "old":
					return old
				case "current":
					return current
				case "unrelated":
					return unrelated
				default:
					return ""
				}
			}
			pr, err := fixture.store.CreatePullRequest(ctx, "project", "Synthetic change", "feature", "main", old, target, ReviewNotRequested, fixture.now)
			noErr(t, err)
			noErr(t, fixture.store.RecordPullRequestRevision(ctx, PullRequestRevision{RepositoryID: "project", PullRequestNumber: pr.Number, SourceOID: current, TargetOID: target, RecordedAt: fixture.now}))
			if oid := revision(test.workflowRevision); oid != "" {
				request := workflowRunRequest(t)
				request.Run.SourceOID, request.Run.EventKey = oid, "push/main/"+oid
				run := admitGraph(t, fixture, request)
				jobs, err := fixture.store.ActionsRunJobs(ctx, "project", run.ID)
				noErr(t, err)
				runner, _ := fixture.issueRunner(t)
				completeGraphJob(t, fixture, jobs[0], runner, "passed")
			}
			if oid := revision(test.jsonRevision); oid != "" {
				request := pushJobRequest()
				request.SourceOID, request.EventKey = oid, "refs/heads/main@"+oid
				job := fixture.admit(t, request)
				runner, _ := fixture.issueRunner(t)
				claimed, attempt := fixture.claimAndStart(t, job, runner, ProtectionRunnerReported)
				_, _, err := fixture.store.CompleteCheckJobAttempt(ctx, passedCompletion(attempt, fixture.now.Add(time.Second)), authorityFor(job.ID, claimed.LeaseID, runner), fixture.now.Add(time.Second))
				noErr(t, err)
			}
			evidence, err := fixture.store.PullRequestRevisionEvidence(ctx, "project", pr.Number, current)
			noErr(t, err)
			wantWorkflows := 0
			if test.workflowRevision == "old" || test.workflowRevision == "current" {
				wantWorkflows = 1
			}
			if evidence.Conclusion != test.wantConclusion || evidence.JSONStale != test.wantJSONStale || evidence.WorkflowsTotal != wantWorkflows || evidence.WorkflowsTruncated || (len(evidence.Workflows) > 0 && evidence.Workflows[0].Stale) != test.wantRunStale {
				t.Fatalf("evidence=%+v want conclusion=%q JSON stale=%v run stale=%v workflow total=%d", evidence, test.wantConclusion, test.wantJSONStale, test.wantRunStale, wantWorkflows)
			}
			if len(evidence.Workflows) > 0 && evidence.Workflows[0].WorkflowPath == "" {
				t.Fatalf("workflow summary=%+v", evidence.Workflows[0])
			}
		})
	}
}
