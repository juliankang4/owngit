package state

import (
	"context"
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
