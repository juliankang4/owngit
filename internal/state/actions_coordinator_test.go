package state

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"owngit/internal/actions"
)

func graphRunRequest(t *testing.T, plans ...actions.JobPlan) ActionsRunRequest {
	t.Helper()
	request := workflowRunRequest(t)
	request.Jobs = nil
	for _, plan := range plans {
		if len(plan.Steps) == 0 {
			plan.Steps = []actions.Step{{Name: plan.JobKey, Run: "echo synthetic"}}
		}
		encoded, digest, err := actions.EncodePlan(plan)
		noErr(t, err)
		checks := make([]CheckDefinition, len(plan.Steps))
		for index, step := range plan.Steps {
			checks[index].Name, checks[index].Command = actions.StepDisplay(step)
		}
		request.Jobs = append(request.Jobs, ActionsJobRequest{CheckJobRequest: CheckJobRequest{JobKey: plan.JobKey, MatrixIndex: plan.MatrixIndex, PlanDigest: digest, WorkflowDigest: request.Run.Facts.WorkflowDigest, Checks: checks}, Plan: encoded})
	}
	return request
}

func graphJobs(t *testing.T, fixture *checkJobFixture, run ActionsRun) map[string]CheckJob {
	t.Helper()
	jobs, err := fixture.store.ActionsRunJobs(context.Background(), "project", run.ID)
	noErr(t, err)
	out := map[string]CheckJob{}
	for _, job := range jobs {
		out[fmt.Sprintf("%s/%d", job.JobKey, job.MatrixIndex)] = job
	}
	return out
}

func admitGraph(t *testing.T, fixture *checkJobFixture, request ActionsRunRequest) ActionsRun {
	t.Helper()
	run, deduped, err := fixture.store.AdmitActionsRun(context.Background(), request, fixture.now)
	if err != nil || deduped {
		t.Fatalf("run=%+v dedup=%v error=%v", run, deduped, err)
	}
	return run
}

func completeGraphJob(t *testing.T, fixture *checkJobFixture, job CheckJob, runner RunnerCredential, status string) {
	t.Helper()
	claimed, attempt := fixture.claimAndStart(t, job, runner, ProtectionRunnerReported)
	completion := passedCompletion(attempt, fixture.now.Add(time.Second))
	completion.Results[0].Name, completion.Results[0].Command = attempt.Checks[0].Name, attempt.Checks[0].Command
	completion.Results[0].Status, completion.Results[0].Role = status, actions.RoleRun
	_, _, err := fixture.store.CompleteCheckJobAttempt(context.Background(), completion, authorityFor(job.ID, claimed.LeaseID, runner), fixture.now.Add(time.Second))
	noErr(t, err)
}

func TestActionsPullRequestAction(t *testing.T) {
	for _, gap := range []time.Duration{10 * time.Millisecond, time.Second} {
		t.Run(gap.String(), func(t *testing.T) {
			ctx := context.Background()
			fixture := workflowStore(t)
			initial, updated, target := strings.Repeat("b", 40), strings.Repeat("a", 40), strings.Repeat("c", 40)
			pr, err := fixture.store.CreatePullRequest(ctx, "project", "Synthetic change", "feature", "main", initial, target, ReviewNotRequested, fixture.now)
			noErr(t, err)
			noErr(t, fixture.store.RecordPullRequestRevision(ctx, PullRequestRevision{RepositoryID: "project", PullRequestNumber: pr.Number, SourceOID: updated, TargetOID: target, RecordedAt: fixture.now.Add(gap)}))
			opened, err := fixture.store.ActionsPullRequestAction(ctx, "project", pr.Number, initial, target)
			noErr(t, err)
			synchronized, err := fixture.store.ActionsPullRequestAction(ctx, "project", pr.Number, updated, target)
			noErr(t, err)
			if opened != "opened" || synchronized != "synchronize" {
				t.Fatalf("initial=%q updated=%q, want opened and synchronize", opened, synchronized)
			}
		})
	}
}

func TestAdmitCheckEvent(t *testing.T) {
	for _, test := range []struct {
		name, event  string
		queue        int
		json         bool
		stale, fault bool
		outcomes     []string
		mutate       func(*ActionsRunRequest)
		note         bool
		authority    bool
	}{
		{name: "push", event: "push", queue: 4, json: true, outcomes: []string{"", ""}},
		{name: "pull request", event: "pull_request", queue: 4, outcomes: []string{"", ""}},
		{name: "dispatch", event: ActionsEventDispatch, queue: 4, outcomes: []string{"", ""}},
		{name: "schedule", event: ActionsEventSchedule, queue: 4, outcomes: []string{"", ""}},
		{name: "JSON gets first queue slot", event: "push", queue: 2, json: true, outcomes: []string{"", "not_run"}},
		{name: "never fits", event: "push", queue: 1, outcomes: []string{"refused", ""}},
		{name: "policy changed", event: "push", queue: 4, stale: true},
		{name: "atomic rollback", event: "push", queue: 4, json: true, fault: true},
		{name: "invalid plan keeps other admissions", event: "push", queue: 4, json: true, outcomes: []string{"refused", ""}, mutate: func(r *ActionsRunRequest) { r.Jobs[0].PlanDigest = strings.Repeat("0", 64) }},
		{name: "invalid graph keeps other admissions", event: "push", queue: 4, json: true, outcomes: []string{"refused", ""}, mutate: func(r *ActionsRunRequest) { r.Run.Facts.Needs = map[string][]string{"build": {"absent"}} }},
		{name: "invalid inputs keep other admissions", event: "push", queue: 4, json: true, outcomes: []string{"refused", ""}, mutate: func(r *ActionsRunRequest) { r.Run.InputsJSON = `[]` }},
		{name: "invalid job keeps other admissions", event: "push", queue: 4, json: true, outcomes: []string{"refused", ""}, mutate: func(r *ActionsRunRequest) { r.Jobs[0].Checks = nil }},
		{name: "invalid concurrency keeps other admissions", event: "push", queue: 4, json: true, outcomes: []string{"refused", ""}, mutate: func(r *ActionsRunRequest) { r.Concurrency = &actions.Concurrency{Group: "${{ unknown.value }}"} }},
		{name: "unstorable identity keeps other admissions", event: "push", queue: 4, json: true, outcomes: []string{""}, note: true, mutate: func(r *ActionsRunRequest) {
			r.Run.WorkflowPath = ".github/workflows/" + strings.Repeat("a", 5000) + "\n\t\x1b.yml"
		}},
		{name: "repository mismatch rolls back event", event: "push", queue: 4, json: true, authority: true, mutate: func(r *ActionsRunRequest) { r.Run.RepositoryID = "other" }},
		{name: "invalid facts keep other admissions", event: "pull_request", queue: 4, json: true, outcomes: []string{""}, note: true, mutate: func(r *ActionsRunRequest) { r.Run.Facts.PullRequestAction = "unknown" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			fixture := workflowStore(t)
			fixture.setPolicy(t, func(input *CheckPolicyInput) {
				input.QueueLimit = test.queue
				input.AllowedEvents = []string{"push", "pull_request", ActionsEventDispatch, ActionsEventSchedule}
			})
			policy := fixture.grantConsent(t)
			first := graphRunRequest(t, actions.JobPlan{JobKey: "build"})
			if test.name == "never fits" {
				first = graphRunRequest(t, actions.JobPlan{JobKey: "build"}, actions.JobPlan{JobKey: "deploy", Needs: []string{"build"}})
			}
			second := graphRunRequest(t, actions.JobPlan{JobKey: "test"})
			second.Run.WorkflowPath = ".github/workflows/test.yml"
			for _, request := range []*ActionsRunRequest{&first, &second} {
				request.Run.Event = test.event
				if test.event == "pull_request" {
					request.Run.BaseOID, request.Run.PullRequestNumber = strings.Repeat("c", 40), 1
					request.Run.Facts.PullRequestAction, request.Run.Facts.PullRequestHeadRef = "opened", "feature"
				}
				if test.event == ActionsEventDispatch {
					request.Run.InputsJSON = `{"dry_run":true}`
				}
				if test.event == ActionsEventSchedule {
					request.Run.ScheduledFor = &fixture.now
				}
			}
			if test.mutate != nil {
				test.mutate(&first)
			}
			var jsonRequest *CheckJobRequest
			if test.json {
				request := pushJobRequest()
				jsonRequest = &request
			}
			expected := ExpectedCheckPolicy{Version: policy.Version, Digest: policy.Digest}
			if test.stale {
				expected.Version++
			}
			if test.fault {
				noErr(t, fixture.store.Exec(ctx, `CREATE TRIGGER refuse_plan BEFORE INSERT ON actions_job_plans BEGIN SELECT RAISE(ABORT,'synthetic plan failure'); END`))
			}
			noErr(t, fixture.store.RecordAcceptedActionsPushes(ctx, "project", []AcceptedActionsPush{{Ref: "refs/heads/main", NewOID: first.Run.SourceOID}}, fixture.now))
			pushes, err := fixture.store.PendingAcceptedActionsPushes(ctx, "project", 1)
			noErr(t, err)
			result, err := fixture.store.AdmitCheckEvent(ctx, "project", expected, jsonRequest, []ActionsRunRequest{first, second}, fixture.now, pushes[0].Sequence)
			if test.stale || test.fault || test.authority {
				if err == nil {
					t.Fatal("invalid event was accepted")
				}
				for _, table := range []string{"check_jobs", "actions_runs", "actions_job_plans"} {
					count, err := fixture.store.TableRowCount(ctx, table)
					if err != nil || count != 0 {
						t.Fatalf("%s count=%d err=%v", table, count, err)
					}
				}
				return
			}
			noErr(t, err)
			if len(result.Runs) != len(test.outcomes) || (result.Job != nil) != test.json || (len(result.Refusals) != 0) != test.note {
				t.Fatalf("event=%+v", result)
			}
			for _, refusal := range result.Refusals {
				if refusal.Code != "workflow.invalid" || len(refusal.Path) > 4096 || len(refusal.Detail) > 4096 || strings.ContainsAny(refusal.Path+refusal.Detail, "\r\n\t\x1b") {
					t.Fatalf("unsafe refusal: %+v", refusal)
				}
			}
			for index, run := range result.Runs {
				if run.Outcome != test.outcomes[index] {
					t.Fatalf("run %d=%+v", index, run)
				}
				if run.Outcome == "refused" && test.mutate != nil {
					jobs, err := fixture.store.ActionsRunJobs(ctx, "project", run.ID)
					noErr(t, err)
					if len(jobs) != 0 || len(run.Facts.Notes) != 1 || run.Facts.Notes[0].Code != "workflow.invalid" {
						t.Fatalf("invalid run was partly admitted: %+v jobs=%+v", run, jobs)
					}
				}
			}
			again, err := fixture.store.AdmitCheckEvent(ctx, "project", expected, jsonRequest, []ActionsRunRequest{first, second}, fixture.now)
			if err != nil || again.Admitted || again.Runs[0].ID != result.Runs[0].ID {
				t.Fatalf("replay=%+v err=%v", again, err)
			}
		})
	}
}

func TestActionsFinishingTransactions(t *testing.T) {
	for _, test := range []struct {
		name, parent, dependent string
		started                 bool
	}{
		{"completion", CheckJobPassed, CheckJobPending, true},
		{"failure", CheckJobFailed, CheckJobPending, true},
		{"before start failure", CheckJobUnavailable, CheckJobPending, false},
		{"job cancel", CheckJobCancelled, CheckJobPending, false},
		{"claimed restart", CheckJobInterrupted, CheckJobPending, false},
		{"started restart", CheckJobAmbiguous, CheckJobSkipped, true},
		{"lease expiry", CheckJobAmbiguous, CheckJobSkipped, false},
		{"renew expiry", CheckJobAmbiguous, CheckJobSkipped, false},
		{"start expiry", CheckJobAmbiguous, CheckJobSkipped, false},
		{"stale policy", CheckJobInterrupted, CheckJobInterrupted, false},
		{"credential revoke", CheckJobInterrupted, CheckJobPending, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			fixture := workflowStore(t)
			run := admitGraph(t, fixture, graphRunRequest(t, actions.JobPlan{JobKey: "build"}, actions.JobPlan{JobKey: "after", Needs: []string{"build"}, If: "always()"}))
			runner := mustRunner(t, fixture)
			parent, claimed, err := fixture.store.ClaimCheckJob(ctx, "project", runner.ID, fixture.now, RunnerFeatureWorkflowsV1)
			if err != nil || !claimed || parent.JobKey != "build" {
				t.Fatalf("claim=%+v err=%v", parent, err)
			}
			var attempt CheckAttempt
			if test.started {
				parent, attempt, err = fixture.store.StartCheckJob(ctx, fixture.startFor(parent, parent.LeaseID, runner), fixture.now)
				noErr(t, err)
			}
			authority := authorityFor(parent.ID, parent.LeaseID, runner)
			finish := fixture.now.Add(time.Second)
			switch test.name {
			case "completion", "failure":
				completion := passedCompletion(attempt, finish)
				completion.Results[0].Name, completion.Results[0].Command, completion.Results[0].Status, completion.Results[0].Role = attempt.Checks[0].Name, attempt.Checks[0].Command, test.parent, actions.RoleRun
				_, _, err = fixture.store.CompleteCheckJobAttempt(ctx, completion, authority, finish)
			case "before start failure":
				_, err = fixture.store.FailCheckJobBeforeStart(ctx, authority, CheckJobUnavailable, "Synthetic preflight failure.", finish)
			case "job cancel":
				_, err = fixture.store.CancelCheckJob(ctx, "project", parent.ID, finish)
			case "claimed restart", "started restart":
				_, err = fixture.store.ReconcileCheckJobRestart(ctx, finish)
			case "lease expiry":
				_, err = fixture.store.ExpireCheckJobLeases(ctx, *parent.LeaseExpiresAt)
			case "renew expiry":
				_, err = fixture.store.RenewCheckJobLease(ctx, authority, *parent.LeaseExpiresAt)
				if errors.Is(err, ErrCheckJobLease) {
					err = nil
				}
			case "start expiry":
				_, _, err = fixture.store.StartCheckJob(ctx, fixture.startFor(parent, parent.LeaseID, runner), *parent.LeaseExpiresAt)
				if errors.Is(err, ErrCheckJobLease) {
					err = nil
				}
			case "stale policy":
				_, err = fixture.store.RevokeCheckConsent(ctx, "project", finish)
			case "credential revoke":
				err = fixture.store.RevokeCheckRunnerToken(ctx, "project", runner.ID, finish)
			}
			noErr(t, err)
			jobs := graphJobs(t, fixture, run)
			if jobs["build/0"].Status != test.parent || jobs["after/0"].Status != test.dependent {
				t.Fatalf("jobs=%+v", jobs)
			}
			for _, job := range jobs {
				_, exists, err := fixture.store.ActionsJobPlan(ctx, "project", job.ID)
				if err != nil || exists == terminalCheckJob(job.Status) {
					t.Fatalf("%s status=%s plan=%v err=%v", job.JobKey, job.Status, exists, err)
				}
			}
			snapshot, err := fixture.store.RecoverySnapshot(ctx)
			noErr(t, err)
			noErr(t, ValidateCheckRecovery(snapshot))
		})
	}
}

func TestActionsDependencyRelease(t *testing.T) {
	for _, test := range []struct {
		name, status, condition, want, tolerate string
		refused                                 bool
	}{
		{"success", "passed", "", "pending", "", false},
		{"implicit success skips failure", "failed", "", "skipped", "", false},
		{"failure condition", "failed", "failure()", "pending", "", false},
		{"tolerated failure", "failed", "needs.build.result == 'success'", "pending", "true", false},
		{"skipped prerequisite", "skipped", "", "skipped", "", false},
		{"refused prerequisite", "refused", "failure()", "pending", "", true},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := workflowStore(t)
			build := actions.JobPlan{JobKey: "build", ContinueOnError: test.tolerate}
			if test.status == "skipped" {
				build.If = "false"
			}
			request := graphRunRequest(t, build, actions.JobPlan{JobKey: "after", Needs: []string{"build"}, If: test.condition})
			if test.refused {
				request.Jobs = request.Jobs[1:]
				request.Run.Facts.RefusedJobs = []actions.RefusedJob{{JobKey: "build", Reason: actions.Message{Code: "workflow.action", Detail: "Unsupported synthetic action."}}}
			}
			run := admitGraph(t, fixture, request)
			if test.status == "passed" || test.status == "failed" {
				completeGraphJob(t, fixture, graphJobs(t, fixture, run)["build/0"], mustRunner(t, fixture), test.status)
			}
			jobs := graphJobs(t, fixture, run)
			if jobs["after/0"].Status != test.want {
				t.Fatalf("after=%+v", jobs["after/0"])
			}
			if test.want == "pending" {
				encoded, exists, err := fixture.store.ActionsJobPlan(context.Background(), "project", jobs["after/0"].ID)
				if err != nil || !exists {
					t.Fatalf("plan=%v err=%v", exists, err)
				}
				plan, err := actions.DecodePlan(encoded, jobs["after/0"].PlanDigest)
				noErr(t, err)
				if plan.Context.Needs["build"] != actions.NeedsResult(test.status, test.tolerate == "true") {
					t.Fatalf("needs=%v", plan.Context.Needs)
				}
			}
		})
	}
	fixture := workflowStore(t)
	run := admitGraph(t, fixture, graphRunRequest(t, actions.JobPlan{JobKey: "build"}, actions.JobPlan{JobKey: "middle", Needs: []string{"build"}, If: "always()"}, actions.JobPlan{JobKey: "after", Needs: []string{"middle"}, If: "failure()"}))
	runner := mustRunner(t, fixture)
	completeGraphJob(t, fixture, graphJobs(t, fixture, run)["build/0"], runner, "failed")
	completeGraphJob(t, fixture, graphJobs(t, fixture, run)["middle/0"], runner, "passed")
	jobs := graphJobs(t, fixture, run)
	if jobs["after/0"].Status != CheckJobPending {
		t.Fatalf("indirect failure was lost: %+v", jobs)
	}
	if _, exists, err := fixture.store.ActionsJobPlan(context.Background(), "project", jobs["middle/0"].ID); err != nil || exists {
		t.Fatalf("middle plan remains=%v err=%v", exists, err)
	}
}

func TestActionsAmbiguousAncestors(t *testing.T) {
	for _, condition := range []string{"always()", "failure()"} {
		t.Run(condition, func(t *testing.T) {
			ctx := context.Background()
			fixture := workflowStore(t)
			fixture.setPolicy(t, func(input *CheckPolicyInput) { input.MaxActiveJobs = 2 })
			fixture.grantConsent(t)
			run := admitGraph(t, fixture, graphRunRequest(t, actions.JobPlan{JobKey: "build"}, actions.JobPlan{JobKey: "middle", Needs: []string{"build"}, If: "always()"}, actions.JobPlan{JobKey: "after", Needs: []string{"middle"}, If: condition}, actions.JobPlan{JobKey: "healthy"}))
			runner := mustRunner(t, fixture)
			var build, healthy CheckJob
			for range 2 {
				job, found, err := fixture.store.ClaimCheckJob(ctx, "project", runner.ID, fixture.now, RunnerFeatureWorkflowsV1)
				if err != nil || !found {
					t.Fatalf("claim=%v err=%v", found, err)
				}
				if job.JobKey == "build" {
					build = job
				} else {
					healthy = job
				}
			}
			_, err := fixture.store.RenewCheckJobLease(ctx, authorityFor(healthy.ID, healthy.LeaseID, runner), fixture.now.Add(time.Second))
			noErr(t, err)
			_, err = fixture.store.RenewCheckJobLease(ctx, authorityFor(build.ID, build.LeaseID, runner), *build.LeaseExpiresAt)
			if !errors.Is(err, ErrCheckJobLease) {
				t.Fatalf("expired lease=%v", err)
			}
			jobs := graphJobs(t, fixture, run)
			for _, key := range []string{"middle/0", "after/0"} {
				if jobs[key].Status != "skipped" || !strings.Contains(jobs[key].Summary, "build") {
					t.Fatalf("dependent=%+v", jobs[key])
				}
			}
			if _, exists, err := fixture.store.ActionsJobPlan(ctx, "project", jobs["middle/0"].ID); err != nil || exists {
				t.Fatalf("middle plan=%v err=%v", exists, err)
			}
			started, attempt, err := fixture.store.StartCheckJob(ctx, fixture.startFor(healthy, healthy.LeaseID, runner), *build.LeaseExpiresAt)
			noErr(t, err)
			completion := passedCompletion(attempt, build.LeaseExpiresAt.Add(time.Millisecond))
			completion.Results[0].Name, completion.Results[0].Command, completion.Results[0].Role = attempt.Checks[0].Name, attempt.Checks[0].Command, "run"
			_, _, err = fixture.store.CompleteCheckJobAttempt(ctx, completion, authorityFor(started.ID, started.LeaseID, runner), build.LeaseExpiresAt.Add(time.Millisecond))
			noErr(t, err)
			if graphJobs(t, fixture, run)["healthy/0"].Status != "passed" {
				t.Fatal("unrelated branch did not run")
			}
		})
	}
}

func TestCancelActionsRun(t *testing.T) {
	for _, phase := range []string{"pending", "claimed", "started", "ambiguous", "finished", "cross repository"} {
		t.Run(phase, func(t *testing.T) {
			ctx := context.Background()
			fixture := workflowStore(t)
			run := admitGraph(t, fixture, graphRunRequest(t, actions.JobPlan{JobKey: "build"}, actions.JobPlan{JobKey: "after", Needs: []string{"build"}, If: "always()"}))
			jobs := graphJobs(t, fixture, run)
			parent := jobs["build/0"]
			runner := mustRunner(t, fixture)
			if phase == "claimed" || phase == "started" || phase == "ambiguous" {
				parent, _, _ = fixture.store.ClaimCheckJob(ctx, "project", runner.ID, fixture.now, RunnerFeatureWorkflowsV1)
			}
			if phase == "started" || phase == "ambiguous" {
				var err error
				parent, _, err = fixture.store.StartCheckJob(ctx, fixture.startFor(parent, parent.LeaseID, runner), fixture.now)
				noErr(t, err)
			}
			if phase == "ambiguous" {
				_, err := fixture.store.ExpireCheckJobLeases(ctx, *parent.LeaseExpiresAt)
				noErr(t, err)
			}
			if phase == "finished" {
				completeGraphJob(t, fixture, parent, runner, "passed")
				completeGraphJob(t, fixture, graphJobs(t, fixture, run)["after/0"], runner, "passed")
			}
			repository := "project"
			if phase == "cross repository" {
				repository = "other"
			}
			cancelled, err := fixture.store.CancelActionsRun(ctx, repository, run.ID, fixture.now.Add(time.Second))
			if phase == "cross repository" {
				if err == nil {
					t.Fatal("cross repository cancel accepted")
				}
				return
			}
			noErr(t, err)
			jobs = graphJobs(t, fixture, run)
			if phase == "finished" {
				if cancelled.CancelRequestedAt != nil || jobs["build/0"].Status != "passed" {
					t.Fatal("finished run was changed")
				}
				return
			}
			if cancelled.CancelRequestedAt == nil || jobs["after/0"].Status != "cancelled" && jobs["after/0"].Status != "skipped" {
				t.Fatalf("cancel=%+v jobs=%+v", cancelled, jobs)
			}
			if phase == "started" || phase == "ambiguous" {
				if jobs["build/0"].Status != phase || jobs["build/0"].CancelRequestedAt == nil {
					t.Fatalf("started evidence changed: %+v", jobs)
				}
			} else if jobs["build/0"].Status != "cancelled" {
				t.Fatalf("unstarted job=%+v", jobs["build/0"])
			}
			_, _, err = fixture.store.StartCheckJob(ctx, fixture.startFor(parent, parent.LeaseID, runner), fixture.now.Add(2*time.Second))
			if err == nil {
				t.Fatal("cancelled run received a start grant")
			}
			again, err := fixture.store.CancelActionsRun(ctx, "project", run.ID, fixture.now.Add(3*time.Second))
			noErr(t, err)
			if !again.CancelRequestedAt.Equal(*cancelled.CancelRequestedAt) {
				t.Fatal("cancel replay changed intent time")
			}
		})
	}
}

func TestRerunActionsRun(t *testing.T) {
	for _, test := range []string{"root generations", "dispatch inputs", "new policy limits", "concurrent dedup"} {
		t.Run(test, func(t *testing.T) {
			ctx := context.Background()
			fixture := workflowStore(t)
			request := graphRunRequest(t, actions.JobPlan{JobKey: "build"})
			if test == "dispatch inputs" {
				request.Run.Event, request.Run.InputsJSON = ActionsEventDispatch, `{"enabled":true}`
			}
			root := admitGraph(t, fixture, request)
			_, err := fixture.store.CancelActionsRun(ctx, "project", root.ID, fixture.now)
			noErr(t, err)
			if test == "new policy limits" {
				fixture.setPolicy(t, func(input *CheckPolicyInput) {
					input.MaxTimeoutMS = 1000
					input.AllowedEvents = []string{"push", "pull_request", ActionsEventDispatch, ActionsEventSchedule}
				})
				fixture.grantConsent(t)
			}
			var first ActionsRun
			if test == "concurrent dedup" {
				var wait sync.WaitGroup
				results := make(chan ActionsRun, 4)
				for range 4 {
					wait.Add(1)
					go func() {
						defer wait.Done()
						run, _, err := fixture.store.RerunActionsRun(ctx, root.ID, request, fixture.now.Add(time.Second))
						if err != nil {
							t.Error(err)
						}
						results <- run
					}()
				}
				wait.Wait()
				close(results)
				for run := range results {
					if first.ID == "" {
						first = run
					}
					if run.ID != first.ID {
						t.Fatal("concurrent reruns have different identities")
					}
				}
			} else {
				first, _, err = fixture.store.RerunActionsRun(ctx, root.ID, request, fixture.now.Add(time.Second))
				noErr(t, err)
			}
			if first.RerunRoot != root.ID || first.RerunGeneration != 1 || first.InputsJSON != root.InputsJSON {
				t.Fatalf("rerun=%+v", first)
			}
			jobs := graphJobs(t, fixture, first)
			if test == "new policy limits" && jobs["build/0"].Limits.TimeoutMS != 1000 {
				t.Fatalf("limits=%+v", jobs)
			}
			again, deduped, err := fixture.store.RerunActionsRun(ctx, first.ID, request, fixture.now.Add(2*time.Second))
			if err != nil || !deduped || again.ID != first.ID {
				t.Fatalf("unfinished rerun=%+v dedup=%v err=%v", again, deduped, err)
			}
			_, err = fixture.store.CancelActionsRun(ctx, "project", first.ID, fixture.now.Add(3*time.Second))
			noErr(t, err)
			next, deduped, err := fixture.store.RerunActionsRun(ctx, first.ID, request, fixture.now.Add(4*time.Second))
			if err != nil || deduped || next.RerunRoot != root.ID || next.RerunGeneration != 2 || next.Number != 3 {
				t.Fatalf("next=%+v dedup=%v err=%v", next, deduped, err)
			}
			snapshot, err := fixture.store.RecoverySnapshot(ctx)
			noErr(t, err)
			noErr(t, ValidateCheckRecovery(snapshot))
		})
	}
}

func TestActionsFailFast(t *testing.T) {
	for _, test := range []struct {
		name               string
		enabled, tolerated bool
		sibling            string
		want               string
		late, damaged      bool
	}{
		{"pending sibling", true, false, "pending", "cancelled", false, false},
		{"claimed sibling", true, false, "claimed", "cancelled", false, false},
		{"started sibling", true, false, "started", "started", false, false},
		{"disabled", false, false, "pending", "pending", false, false},
		{"tolerated failure", true, true, "pending", "pending", false, false},
		{"late failure after pruning", true, false, "pending", "cancelled", true, false},
		{"damaged plan cannot block finishing", true, false, "pending", "cancelled", false, true},
		{"waiting matrix released", true, false, "waiting", "pending", false, false},
		{"damaged waiting plan", true, false, "waiting", "cancelled", false, true},
		{"damaged waiting plan with fail-fast off", false, false, "waiting", "pending", false, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			fixture := workflowStore(t)
			fixture.setPolicy(t, func(input *CheckPolicyInput) { input.MaxActiveJobs = 2 })
			fixture.grantConsent(t)
			strategy := actions.StrategyContext{JobTotal: 2, MaxParallel: 2, FailFast: test.enabled}
			first := actions.JobPlan{JobKey: "build", Context: actions.PlanContext{Strategy: strategy}}
			if test.sibling == "waiting" {
				first.Needs = []string{"prepare"}
			}
			if test.tolerated {
				first.ContinueOnError = "true"
			}
			second := first
			second.MatrixIndex = 1
			request := graphRunRequest(t, first, second)
			if test.sibling == "waiting" {
				request = graphRunRequest(t, actions.JobPlan{JobKey: "prepare"}, first, second)
			}
			run := admitGraph(t, fixture, request)
			runner := mustRunner(t, fixture)
			if test.sibling == "waiting" {
				jobs := graphJobs(t, fixture, run)
				parent, attempt := fixture.claimAndStart(t, jobs["prepare/0"], runner, ProtectionRunnerReported)
				if test.damaged {
					noErr(t, fixture.store.Exec(ctx, `UPDATE actions_job_plans SET plan_json=plan_json||' ' WHERE job_id=?`, jobs["build/0"].ID))
				}
				completion := passedCompletion(attempt, fixture.now.Add(time.Second))
				completion.Results[0].Name, completion.Results[0].Command, completion.Results[0].Role = attempt.Checks[0].Name, attempt.Checks[0].Command, actions.RoleRun
				_, _, err := fixture.store.CompleteCheckJobAttempt(ctx, completion, authorityFor(parent.ID, parent.LeaseID, runner), fixture.now.Add(time.Second))
				noErr(t, err)
				jobs = graphJobs(t, fixture, run)
				wantFirst := CheckJobPending
				if test.damaged {
					wantFirst = CheckJobError
				}
				if jobs["build/0"].Status != wantFirst || jobs["build/1"].Status != test.want {
					t.Fatalf("first=%s sibling=%s, want first=%s sibling=%s", jobs["build/0"].Status, jobs["build/1"].Status, wantFirst, test.want)
				}
				if test.want == CheckJobCancelled && (jobs["build/1"].CancelRequestedAt == nil || !strings.Contains(jobs["build/1"].Summary, "note.fail_fast")) {
					t.Fatalf("missing fail-fast intent: %+v", jobs["build/1"])
				}
				return
			}
			failed, _, err := fixture.store.ClaimCheckJob(ctx, "project", runner.ID, fixture.now, RunnerFeatureWorkflowsV1)
			noErr(t, err)
			failed, attempt, err := fixture.store.StartCheckJob(ctx, fixture.startFor(failed, failed.LeaseID, runner), fixture.now)
			noErr(t, err)
			var sibling CheckJob
			for _, job := range graphJobs(t, fixture, run) {
				if job.ID != failed.ID {
					sibling = job
				}
			}
			if test.sibling != "pending" {
				sibling, _, err = fixture.store.ClaimCheckJob(ctx, "project", runner.ID, fixture.now, RunnerFeatureWorkflowsV1)
				noErr(t, err)
			}
			if test.sibling == "started" {
				sibling, _, err = fixture.store.StartCheckJob(ctx, fixture.startFor(sibling, sibling.LeaseID, runner), fixture.now)
				noErr(t, err)
			}
			finish := fixture.now.Add(time.Second)
			if test.late {
				_, err := fixture.store.ExpireCheckJobLeases(ctx, *failed.LeaseExpiresAt)
				noErr(t, err)
				if _, exists, err := fixture.store.ActionsJobPlan(ctx, "project", failed.ID); err != nil || exists {
					t.Fatalf("ambiguous plan=%v err=%v", exists, err)
				}
				finish = failed.LeaseExpiresAt.Add(time.Second)
			}
			if test.damaged {
				noErr(t, fixture.store.Exec(ctx, `UPDATE actions_job_plans SET plan_json=plan_json||' ' WHERE job_id=?`, failed.ID))
			}
			completion := passedCompletion(attempt, finish)
			completion.Results[0].Name, completion.Results[0].Command, completion.Results[0].Status, completion.Results[0].Role = attempt.Checks[0].Name, attempt.Checks[0].Command, "failed", "run"
			_, _, err = fixture.store.CompleteCheckJobAttempt(ctx, completion, authorityFor(failed.ID, failed.LeaseID, runner), finish)
			noErr(t, err)
			stored, _, err := fixture.store.CheckJob(ctx, "project", sibling.ID)
			noErr(t, err)
			if stored.Status != test.want {
				t.Fatalf("sibling=%+v", stored)
			}
			if test.enabled && !test.tolerated && (stored.CancelRequestedAt == nil || !strings.Contains(stored.Summary, "note.fail_fast")) {
				t.Fatalf("missing fail-fast intent: %+v", stored)
			}
		})
	}
}

func TestActionsClaimGuards(t *testing.T) {
	for _, test := range []struct {
		name, firstRunGroup, firstJobGroup, nextRunGroup, nextJobGroup string
		release, matrix, cancelled, own, oldRunner                     bool
		wantWorkflow                                                   bool
	}{
		{name: "run cancel", cancelled: true},
		{name: "workflow concurrency", firstRunGroup: "deploy", nextRunGroup: "DEPLOY"},
		{name: "old runner retains concurrency note", firstRunGroup: "deploy", nextRunGroup: "DEPLOY", oldRunner: true},
		{name: "held run between jobs", firstRunGroup: "deploy", nextRunGroup: "deploy", release: true, wantWorkflow: true},
		{name: "job concurrency", firstJobGroup: "deploy", nextJobGroup: "DEPLOY"},
		{name: "workflow blocks job group", firstRunGroup: "deploy", nextJobGroup: "DEPLOY"},
		{name: "job group blocks workflow", firstJobGroup: "deploy", nextRunGroup: "DEPLOY"},
		{name: "run does not block own group", firstRunGroup: "deploy", firstJobGroup: "DEPLOY", release: true, own: true, wantWorkflow: true},
		{name: "max parallel", matrix: true},
		{name: "independent matrix", wantWorkflow: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			fixture := workflowStore(t)
			fixture.setPolicy(t, func(input *CheckPolicyInput) { input.MaxActiveJobs = 4; input.QueueLimit = 8 })
			fixture.grantConsent(t)
			runner := mustRunner(t, fixture)
			plan := actions.JobPlan{JobKey: "build", Context: actions.PlanContext{Strategy: actions.StrategyContext{JobTotal: 2, MaxParallel: 1}}, Concurrency: actions.Concurrency{Group: test.firstJobGroup, Queue: "max"}}
			plans := []actions.JobPlan{plan}
			if test.release {
				plans = append(plans, actions.JobPlan{JobKey: "after", Needs: []string{"build"}, Concurrency: plan.Concurrency})
			}
			if test.matrix {
				sibling := plan
				sibling.MatrixIndex = 1
				plans = append(plans, sibling)
			}
			request := graphRunRequest(t, plans...)
			request.Run.ConcurrencyGroup, request.Run.ConcurrencyQueue = test.firstRunGroup, "max"
			first := admitGraph(t, fixture, request)
			var parent CheckJob
			if !test.cancelled {
				var found bool
				var err error
				parent, found, err = fixture.store.ClaimCheckJob(ctx, "project", runner.ID, fixture.now, RunnerFeatureWorkflowsV1)
				if err != nil || !found {
					t.Fatalf("first claim=%v err=%v", found, err)
				}
			}
			if !test.cancelled && !test.matrix && !test.own {
				plan.Concurrency.Group = test.nextJobGroup
				plan.MatrixIndex = 1
				next := graphRunRequest(t, plan)
				next.Run.EventKey, next.Run.ConcurrencyGroup, next.Run.ConcurrencyQueue = "next-event", test.nextRunGroup, "max"
				admitGraph(t, fixture, next)
			}
			if test.cancelled {
				noErr(t, fixture.store.Exec(ctx, `UPDATE actions_runs SET cancel_requested_at=? WHERE id=?`, fixture.now.UnixNano(), first.ID))
			}
			if test.release {
				started, attempt, err := fixture.store.StartCheckJob(ctx, fixture.startFor(parent, parent.LeaseID, runner), fixture.now)
				noErr(t, err)
				completion := passedCompletion(attempt, fixture.now.Add(time.Second))
				completion.Results[0].Name, completion.Results[0].Command, completion.Results[0].Role = attempt.Checks[0].Name, attempt.Checks[0].Command, "run"
				_, _, err = fixture.store.CompleteCheckJobAttempt(ctx, completion, authorityFor(started.ID, started.LeaseID, runner), fixture.now.Add(time.Second))
				noErr(t, err)
			}
			fixture.now = fixture.now.Add(time.Second)
			jsonJob := fixture.admit(t, pushJobRequest())
			features := []string{RunnerFeatureWorkflowsV1}
			if test.oldRunner {
				features = nil
			}
			claimed, found, err := fixture.store.ClaimCheckJob(ctx, "project", runner.ID, fixture.now.Add(time.Second), features...)
			noErr(t, err)
			if !found || test.wantWorkflow && claimed.RunID == "" || !test.wantWorkflow && claimed.ID != jsonJob.ID {
				t.Fatalf("claim=%+v found=%v", claimed, found)
			}
			if test.oldRunner {
				for range 2 {
					jobs, err := fixture.store.CheckJobs(ctx, "project")
					noErr(t, err)
					var blocked CheckJob
					for _, job := range jobs {
						if job.RunID != "" && job.Status == CheckJobPending {
							blocked = job
						}
					}
					if !strings.Contains(blocked.Summary, "note.runner_old") || !strings.Contains(blocked.Summary, "note.concurrency_wait") {
						t.Fatalf("missing claim notes: %+v", blocked)
					}
					_, found, err := fixture.store.ClaimCheckJob(ctx, "project", runner.ID, fixture.now.Add(time.Second))
					noErr(t, err)
					if found {
						t.Fatal("old runner claimed blocked workflow")
					}
				}
			}
			if test.own && claimed.RunID != first.ID {
				t.Fatal("own workflow group was blocked")
			}
		})
	}
}

func TestActionsCrossScopeCancellation(t *testing.T) {
	for _, test := range []struct {
		name               string
		runFirst, cancel   bool
		phase, queue, want string
	}{
		{"run replaces pending job", false, false, "pending", "single", "cancelled"},
		{"job replaces pending run", true, false, "pending", "single", "cancelled"},
		{"run cancels started job", false, true, "started", "single", "started"},
		{"job cancels started run", true, true, "started", "single", "started"},
		{"mixed max queue preserves job", false, false, "pending", "max", "pending"},
		{"mixed max queue preserves run", true, false, "pending", "max", "pending"},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			fixture := workflowStore(t)
			plan := actions.JobPlan{JobKey: "build"}
			if !test.runFirst {
				plan.Concurrency = actions.Concurrency{Group: "DEPLOY", Queue: "max"}
			}
			request := graphRunRequest(t, plan)
			if test.runFirst {
				request.Run.ConcurrencyGroup, request.Run.ConcurrencyQueue = "DEPLOY", "max"
			}
			first := admitGraph(t, fixture, request)
			if test.phase == "started" {
				runner := mustRunner(t, fixture)
				job, found, err := fixture.store.ClaimCheckJob(ctx, "project", runner.ID, fixture.now, RunnerFeatureWorkflowsV1)
				if err != nil || !found {
					t.Fatalf("claim=%v err=%v", found, err)
				}
				_, _, err = fixture.store.StartCheckJob(ctx, fixture.startFor(job, job.LeaseID, runner), fixture.now)
				noErr(t, err)
			}
			plan.Concurrency = actions.Concurrency{}
			if test.runFirst {
				plan.Concurrency = actions.Concurrency{Group: "deploy", Queue: test.queue, CancelInProgress: fmt.Sprint(test.cancel)}
			}
			next := graphRunRequest(t, plan)
			next.Run.EventKey = "next-event"
			if !test.runFirst {
				next.Run.ConcurrencyGroup, next.Run.ConcurrencyQueue, next.Run.CancelInProgress = "deploy", test.queue, test.cancel
			}
			admitGraph(t, fixture, next)
			job := graphJobs(t, fixture, first)["build/0"]
			if job.Status != test.want || test.cancel && job.CancelRequestedAt == nil {
				t.Fatalf("cross-scope holder=%+v", job)
			}
			if job.Status == "cancelled" {
				if _, exists, err := fixture.store.ActionsJobPlan(ctx, "project", job.ID); err != nil || exists {
					t.Fatalf("cancelled plan=%v err=%v", exists, err)
				}
			}
		})
	}
}

func TestActionsConcurrencyQueueBound(t *testing.T) {
	for _, scope := range []string{"workflow", "job"} {
		t.Run(scope, func(t *testing.T) {
			fixture := workflowStore(t)
			fixture.setPolicy(t, func(input *CheckPolicyInput) { input.QueueLimit = 101 })
			fixture.grantConsent(t)
			plan := actions.JobPlan{JobKey: "build"}
			if scope == "job" {
				plan.Concurrency = actions.Concurrency{Group: "deploy", Queue: "max"}
			}
			request := graphRunRequest(t, plan)
			if scope == "workflow" {
				request.Run.ConcurrencyGroup, request.Run.ConcurrencyQueue = "deploy", "max"
			}
			for index := range 101 {
				request.Run.EventKey = fmt.Sprintf("event-%d", index)
				run := admitGraph(t, fixture, request)
				job := graphJobs(t, fixture, run)["build/0"]
				want := "pending"
				if index == 100 {
					want = "cancelled"
				}
				if job.Status != want {
					t.Fatalf("entry %d=%s, want %s", index, job.Status, want)
				}
			}
		})
	}
}

func TestActionsConcurrencyAdmission(t *testing.T) {
	for _, test := range []struct {
		name, queue, holder string
		cancel              bool
		want                string
	}{
		{"single replacement", "single", "pending", false, "cancelled"},
		{"max queue keeps pending", "max", "pending", false, "pending"},
		{"holder continues", "single", "claimed", false, "claimed"},
		{"cancel current holder", "single", "claimed", true, "cancelled"},
		{"cancel started holder", "single", "started", true, "started"},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			fixture := workflowStore(t)
			request := graphRunRequest(t, actions.JobPlan{JobKey: "build"})
			request.Run.ConcurrencyGroup, request.Run.ConcurrencyQueue = "DEPLOY", test.queue
			first := admitGraph(t, fixture, request)
			if test.holder != "pending" {
				runner := mustRunner(t, fixture)
				job, _, err := fixture.store.ClaimCheckJob(ctx, "project", runner.ID, fixture.now, RunnerFeatureWorkflowsV1)
				noErr(t, err)
				if test.holder == "started" {
					_, _, err := fixture.store.StartCheckJob(ctx, fixture.startFor(job, job.LeaseID, runner), fixture.now)
					noErr(t, err)
				}
			}
			request.Run.EventKey, request.Run.ConcurrencyGroup, request.Run.CancelInProgress = "later-event", "deploy", test.cancel
			admitGraph(t, fixture, request)
			job := graphJobs(t, fixture, first)["build/0"]
			if job.Status != test.want {
				t.Fatalf("holder=%+v", job)
			}
			if test.cancel && job.CancelRequestedAt == nil {
				t.Fatal("cancel-in-progress did not record intent")
			}
			if job.Status == "cancelled" {
				if _, exists, err := fixture.store.ActionsJobPlan(ctx, "project", job.ID); err != nil || exists {
					t.Fatalf("cancelled plan=%v err=%v", exists, err)
				}
			}
		})
	}
}
