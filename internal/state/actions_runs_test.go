package state

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"owngit/internal/actions"
	"owngit/internal/testfixture"
)

func workflowRunRequest(t *testing.T) ActionsRunRequest {
	t.Helper()
	plan, digest := testfixture.ActionsPlan(t, "test", 0)
	return ActionsRunRequest{
		Run:  ActionsRun{RepositoryID: "project", WorkflowPath: ".github/workflows/ci.yml", Event: "push", EventKey: "push/main/" + strings.Repeat("a", 40), SourceOID: strings.Repeat("a", 40), TriggerRef: "main", Facts: actions.RunFacts{WorkflowDigest: strings.Repeat("b", 64)}},
		Jobs: []ActionsJobRequest{{CheckJobRequest: CheckJobRequest{JobKey: "test", PlanDigest: digest, WorkflowDigest: strings.Repeat("b", 64), Checks: []CheckDefinition{{Name: "test", Command: "echo synthetic"}}}, Plan: plan}},
	}
}

func workflowStore(t *testing.T) *checkJobFixture {
	t.Helper()
	fixture := newCheckJobFixture(t)
	fixture.setPolicy(t, func(input *CheckPolicyInput) {
		input.AllowedEvents = []string{"push", "pull_request", ActionsEventDispatch, ActionsEventSchedule}
	})
	fixture.grantConsent(t)
	return fixture
}

func TestWorkflowPolicyDefaults(t *testing.T) {
	for _, test := range []struct {
		name      string
		stored    *bool
		requested *bool
		want      bool
	}{
		{name: "new policy defaults on", want: true},
		{name: "existing off stays off", stored: new(false), want: false},
		{name: "existing on stays on", stored: new(true), want: true},
		{name: "explicit turn on", stored: new(false), requested: new(true), want: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newCheckJobFixture(t)
			input := defaultPolicyInput()
			if test.stored != nil {
				input.RunWorkflows = test.stored
				_, err := fixture.store.SetCheckPolicy(context.Background(), input, fixture.now)
				noErr(t, err)
				fixture.grantConsent(t)
			}
			input.RunWorkflows = test.requested
			policy, err := fixture.store.SetCheckPolicy(context.Background(), input, fixture.now)
			noErr(t, err)
			stored, exists, err := fixture.store.CheckPolicy(context.Background(), "project")
			if err != nil || !exists || stored.RunWorkflows != test.want || policy.Digest != stored.Digest {
				t.Fatalf("policy=%+v exists=%v err=%v", stored, exists, err)
			}
			if test.stored != nil && test.requested == nil && !stored.ConsentActive {
				t.Fatal("unchanged policy lost consent")
			}
			if test.requested != nil && stored.ConsentActive {
				t.Fatal("changed policy kept consent")
			}
		})
	}
}

func TestAdmitActionsRun(t *testing.T) {
	for _, test := range []struct {
		name      string
		change    func(*testing.T, *checkJobFixture, *ActionsRunRequest)
		wantErr   error
		errorText string
		outcome   string
		jobs      int
	}{
		{name: "push", jobs: 1},
		{name: "pull request", jobs: 1, change: func(_ *testing.T, _ *checkJobFixture, r *ActionsRunRequest) {
			r.Run.Event = "pull_request"
			r.Run.PullRequestNumber = 1
			r.Run.BaseOID = strings.Repeat("c", 40)
			r.Run.Facts.PullRequestAction, r.Run.Facts.PullRequestHeadRef = "opened", "feature"
		}},
		{name: "push has pull request action", wantErr: ErrInvalidActionsRun, change: func(_ *testing.T, _ *checkJobFixture, r *ActionsRunRequest) {
			r.Run.Facts.PullRequestAction = "opened"
		}},
		{name: "push has pull request head", wantErr: ErrInvalidActionsRun, change: func(_ *testing.T, _ *checkJobFixture, r *ActionsRunRequest) {
			r.Run.Facts.PullRequestHeadRef = "feature"
		}},
		{name: "dispatch inputs", jobs: 1, change: func(_ *testing.T, _ *checkJobFixture, r *ActionsRunRequest) {
			r.Run.Event = ActionsEventDispatch
			r.Run.InputsJSON = "\n" + `{"target":"test","dry_run":true}` + " \n"
		}},
		{name: "schedule", jobs: 1, change: func(_ *testing.T, f *checkJobFixture, r *ActionsRunRequest) {
			r.Run.Event = ActionsEventSchedule
			r.Run.ScheduledFor = &f.now
		}},
		{name: "waiting job", jobs: 2, change: func(t *testing.T, _ *checkJobFixture, r *ActionsRunRequest) {
			*r = graphRunRequest(t, actions.JobPlan{JobKey: "test", Needs: []string{"build"}}, actions.JobPlan{JobKey: "build"})
			r.Jobs[0].Waiting = true
		}},
		{name: "refused file", outcome: "refused", change: func(_ *testing.T, _ *checkJobFixture, r *ActionsRunRequest) {
			r.Run.Outcome, r.Run.Reason, r.Jobs = "refused", "Unsupported event", nil
		}},
		{name: "whole run never fits queue", outcome: "refused", change: func(t *testing.T, f *checkJobFixture, r *ActionsRunRequest) {
			f.setPolicy(t, func(i *CheckPolicyInput) { i.QueueLimit = 1 })
			f.grantConsent(t)
			second := r.Jobs[0]
			second.MatrixIndex = 1
			second.Plan, second.PlanDigest = testfixture.ActionsPlan(t, "test", 1)
			r.Jobs = append(r.Jobs, second)
		}},
		{name: "full queue records not run", outcome: "not_run", change: func(t *testing.T, f *checkJobFixture, _ *ActionsRunRequest) {
			f.setPolicy(t, func(i *CheckPolicyInput) { i.QueueLimit = 1 })
			f.grantConsent(t)
			f.admit(t, pushJobRequest())
		}},
		{name: "workflows off", wantErr: ErrActionsWorkflowsOff, change: func(t *testing.T, f *checkJobFixture, _ *ActionsRunRequest) {
			f.setPolicy(t, func(i *CheckPolicyInput) { i.RunWorkflows = new(false) })
			f.grantConsent(t)
		}},
		{name: "no consent", wantErr: ErrCheckConsentRequired, change: func(t *testing.T, f *checkJobFixture, _ *ActionsRunRequest) {
			_, err := f.store.RevokeCheckConsent(context.Background(), "project", f.now)
			noErr(t, err)
		}},
		{name: "invalid path", wantErr: ErrInvalidActionsRun, change: func(_ *testing.T, _ *checkJobFixture, r *ActionsRunRequest) {
			r.Run.WorkflowPath = ".github/workflows/sub/ci.yml"
		}},
		{name: "unknown event", wantErr: ErrCheckEventNotAllowed, change: func(_ *testing.T, _ *checkJobFixture, r *ActionsRunRequest) { r.Run.Event = "merge" }},
		{name: "invalid plan digest", wantErr: ErrInvalidCheckJob, change: func(_ *testing.T, _ *checkJobFixture, r *ActionsRunRequest) {
			r.Jobs[0].PlanDigest = strings.Repeat("c", 64)
		}},
		{name: "job file differs from run", wantErr: ErrInvalidCheckJob, change: func(_ *testing.T, _ *checkJobFixture, r *ActionsRunRequest) {
			r.Jobs[0].WorkflowDigest = strings.Repeat("c", 64)
		}},
		{name: "plan write failure rolls back the whole run", errorText: "synthetic plan write failure", change: func(t *testing.T, f *checkJobFixture, r *ActionsRunRequest) {
			second := r.Jobs[0]
			second.JobKey = "second"
			second.Plan, second.PlanDigest = testfixture.ActionsPlan(t, "second", 0)
			r.Jobs = append(r.Jobs, second)
			noErr(t, f.store.Exec(context.Background(), `CREATE TRIGGER fail_second_plan BEFORE INSERT ON actions_job_plans WHEN json_extract(NEW.plan_json,'$.job_key')='second' BEGIN SELECT RAISE(ABORT,'synthetic plan write failure'); END`))
		}},
		{name: "per-run identity is not stored in a plan", wantErr: ErrInvalidCheckJob, change: func(_ *testing.T, _ *checkJobFixture, r *ActionsRunRequest) {
			r.Jobs[0].Plan = json.RawMessage(strings.Replace(string(r.Jobs[0].Plan), `"run_id":""`, `"run_id":"synthetic-run"`, 1))
			r.Jobs[0].PlanDigest = fmt.Sprintf("%x", sha256.Sum256(r.Jobs[0].Plan))
		}},
		{name: "fail-fast differs from plan", wantErr: ErrInvalidActionsRun, change: func(_ *testing.T, _ *checkJobFixture, r *ActionsRunRequest) {
			r.Run.Facts.FailFast = map[string]bool{"test": true}
		}},
		{name: "graph differs from plan", wantErr: ErrInvalidActionsRun, change: func(_ *testing.T, _ *checkJobFixture, r *ActionsRunRequest) {
			r.Run.Facts.Needs = map[string][]string{"test": {"test"}}
		}},
		{name: "duplicate job", wantErr: ErrInvalidActionsRun, change: func(_ *testing.T, _ *checkJobFixture, r *ActionsRunRequest) { r.Jobs = append(r.Jobs, r.Jobs[0]) }},
		{name: "too many jobs", wantErr: ErrInvalidActionsRun, change: func(_ *testing.T, _ *checkJobFixture, r *ActionsRunRequest) { r.Jobs = make([]ActionsJobRequest, 17) }},
		{name: "empty run", wantErr: ErrInvalidActionsRun, change: func(_ *testing.T, _ *checkJobFixture, r *ActionsRunRequest) { r.Jobs = nil }},
		{name: "inputs are not an object", wantErr: ErrInvalidActionsRun, change: func(_ *testing.T, _ *checkJobFixture, r *ActionsRunRequest) { r.Run.InputsJSON = `[]` }},
		{name: "oversized inputs", wantErr: ErrInvalidActionsRun, change: func(_ *testing.T, _ *checkJobFixture, r *ActionsRunRequest) {
			r.Run.InputsJSON = `{"text":"` + strings.Repeat("x", MaximumActionsJSONBytes) + `"}`
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			fixture := workflowStore(t)
			request := workflowRunRequest(t)
			if test.change != nil {
				test.change(t, fixture, &request)
			}
			run, deduped, err := fixture.store.AdmitActionsRun(ctx, request, fixture.now)
			if test.errorText != "" {
				if err == nil || !strings.Contains(err.Error(), test.errorText) {
					t.Fatalf("write fault=%v", err)
				}
			} else if !errors.Is(err, test.wantErr) {
				t.Fatalf("run=%+v err=%v, want %v", run, err, test.wantErr)
			}
			runs, readErr := fixture.store.ActionsRuns(ctx, "project", 0)
			noErr(t, readErr)
			if test.wantErr != nil || test.errorText != "" {
				if len(runs) != 0 {
					t.Fatalf("refusal wrote runs: %+v", runs)
				}
				for _, table := range []string{"check_jobs", "actions_job_plans"} {
					count, err := fixture.store.TableRowCount(ctx, table)
					if err != nil || count != 0 {
						t.Fatalf("%s rows=%d err=%v", table, count, err)
					}
				}
				return
			}
			if deduped || run.Outcome != test.outcome || len(runs) != 1 || run.Number != 1 {
				t.Fatalf("run=%+v deduped=%v runs=%d", run, deduped, len(runs))
			}
			jobs, err := fixture.store.ActionsRunJobs(ctx, "project", run.ID)
			if err != nil || len(jobs) != test.jobs {
				t.Fatalf("jobs=%+v err=%v", jobs, err)
			}
			for _, job := range jobs {
				if request.Jobs[0].Waiting && job.JobKey == request.Jobs[0].JobKey && job.Status != CheckJobWaiting {
					t.Fatalf("waiting status=%s", job.Status)
				}
			}
			again, deduped, err := fixture.store.AdmitActionsRun(ctx, request, fixture.now)
			if err != nil || !deduped || again.ID != run.ID {
				t.Fatalf("replay=%+v deduped=%v err=%v", again, deduped, err)
			}
			events, err := fixture.store.CheckEventsWithJobs(ctx, "project", run.Event, []string{run.EventKey})
			if err != nil || !events[run.EventKey] {
				t.Fatalf("event missing: %v err=%v", events, err)
			}
		})
	}
}

func TestActionsRunReads(t *testing.T) {
	ctx := context.Background()
	fixture := workflowStore(t)
	run, _, err := fixture.store.AdmitActionsRun(ctx, workflowRunRequest(t), fixture.now)
	noErr(t, err)
	for _, test := range []struct {
		name  string
		query func() (int, error)
		want  int
	}{
		{"one run", func() (int, error) {
			_, found, err := fixture.store.ActionsRun(ctx, "project", run.ID)
			if found {
				return 1, err
			}
			return 0, err
		}, 1},
		{"cross repository", func() (int, error) {
			_, found, err := fixture.store.ActionsRun(ctx, "other", run.ID)
			if found {
				return 1, err
			}
			return 0, err
		}, 0},
		{"run list", func() (int, error) { runs, err := fixture.store.ActionsRuns(ctx, "project", 1); return len(runs), err }, 1},
		{"revision list", func() (int, error) {
			runs, err := fixture.store.ActionsRunsForRevision(ctx, "project", run.SourceOID)
			return len(runs), err
		}, 1},
		{"unknown revision", func() (int, error) {
			runs, err := fixture.store.ActionsRunsForRevision(ctx, "project", strings.Repeat("c", 40))
			return len(runs), err
		}, 0},
		{"job list", func() (int, error) {
			jobs, err := fixture.store.ActionsRunJobs(ctx, "project", run.ID)
			return len(jobs), err
		}, 1},
		{"cross repository jobs", func() (int, error) {
			jobs, err := fixture.store.ActionsRunJobs(ctx, "other", run.ID)
			return len(jobs), err
		}, 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			count, err := test.query()
			if err != nil || count != test.want {
				t.Fatalf("count=%d err=%v", count, err)
			}
		})
	}
}

func TestActionsJobPlan(t *testing.T) {
	for _, test := range []struct {
		name       string
		repository string
		sql        string
		found      bool
		invalid    bool
	}{
		{name: "stored", repository: "project", found: true},
		{name: "cross repository", repository: "other"},
		{name: "pruned", repository: "project", sql: `DELETE FROM actions_job_plans`},
		{name: "plan bytes differ from digest", repository: "project", sql: `UPDATE actions_job_plans SET plan_json=plan_json||' '`, invalid: true},
		{name: "changed job digest", repository: "project", sql: `UPDATE check_jobs SET plan_digest='cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc'`, invalid: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			fixture := workflowStore(t)
			request := workflowRunRequest(t)
			run, _, err := fixture.store.AdmitActionsRun(ctx, request, fixture.now)
			noErr(t, err)
			jobs, err := fixture.store.ActionsRunJobs(ctx, "project", run.ID)
			noErr(t, err)
			if test.sql != "" {
				noErr(t, fixture.store.Exec(ctx, test.sql))
			}
			plan, found, err := fixture.store.ActionsJobPlan(ctx, test.repository, jobs[0].ID)
			if (err != nil) != test.invalid || found != test.found || found && string(plan) != string(request.Jobs[0].Plan) {
				t.Fatalf("plan=%s found=%v err=%v", plan, found, err)
			}
		})
	}
}

func TestCompleteActionsJobAttempt(t *testing.T) {
	for _, test := range []struct {
		name, status, role, cleanup, want string
		invalid                           bool
	}{
		{name: "run passed", status: "passed", role: "run", want: "passed"},
		{name: "run failed", status: "failed", role: "run", want: "failed"},
		{name: "tolerated failure", status: "failed", role: "tolerated", want: "passed"},
		{name: "builtin only", status: "passed", role: "builtin", want: "skipped"},
		{name: "not run builtin", status: "not_run", role: "builtin", want: "skipped"},
		{name: "skipped run", status: "skipped", role: "run", want: "skipped"},
		{name: "cleanup cannot be tolerated", status: "failed", role: "tolerated", cleanup: "Process cleanup was not confirmed.", want: "error"},
		{name: "missing role", status: "passed", invalid: true},
		{name: "unknown role", status: "passed", role: "unknown", invalid: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			fixture := workflowStore(t)
			run, _, err := fixture.store.AdmitActionsRun(ctx, workflowRunRequest(t), fixture.now)
			noErr(t, err)
			jobs, err := fixture.store.ActionsRunJobs(ctx, "project", run.ID)
			noErr(t, err)
			runner := mustRunner(t, fixture)
			claimed, attempt := fixture.claimAndStart(t, jobs[0], runner, ProtectionRunnerReported)
			completion := passedCompletion(attempt, fixture.now.Add(time.Second))
			completion.Results[0].Name, completion.Results[0].Command = attempt.Checks[0].Name, attempt.Checks[0].Command
			completion.Results[0].Status, completion.Results[0].Role, completion.Results[0].CleanupError = test.status, test.role, test.cleanup
			authority := authorityFor(jobs[0].ID, claimed.LeaseID, runner)
			_, stored, err := fixture.store.CompleteCheckJobAttempt(ctx, completion, authority, fixture.now.Add(time.Second))
			if test.invalid {
				if err == nil {
					t.Fatal("invalid role was accepted")
				}
				plan, found, err := fixture.store.ActionsJobPlan(ctx, "project", jobs[0].ID)
				if err != nil || !found || len(plan) == 0 {
					t.Fatalf("refusal changed plan: found=%v err=%v", found, err)
				}
				return
			}
			if err != nil || stored.Status != test.want || stored.Results[0].Role != test.role {
				t.Fatalf("attempt=%+v err=%v", stored, err)
			}
			job, _, err := fixture.store.CheckJob(ctx, "project", jobs[0].ID)
			if err != nil || job.Status != test.want {
				t.Fatalf("job=%+v err=%v", job, err)
			}
			if _, found, err := fixture.store.ActionsJobPlan(ctx, "project", job.ID); err != nil || found {
				t.Fatalf("terminal plan found=%v err=%v", found, err)
			}
			_, replay, err := fixture.store.CompleteCheckJobAttempt(ctx, completion, authority, fixture.now.Add(2*time.Second))
			if err != nil || replay.CompletionDigest != stored.CompletionDigest {
				t.Fatalf("completion replay=%+v err=%v", replay, err)
			}
			snapshot, err := fixture.store.RecoverySnapshot(ctx)
			noErr(t, err)
			restored := openTestStore(t)
			noErr(t, restored.RestoreRecoveryState(ctx, "/synthetic/repositories", snapshot))
			recovered, found, err := restored.CheckAttemptByID(ctx, "project", stored.ID)
			if err != nil || !found || recovered.Status != test.want || !reflect.DeepEqual(recovered.Results, stored.Results) || recovered.CompletionDigest != stored.CompletionDigest {
				t.Fatalf("recovered=%+v found=%v err=%v", recovered, found, err)
			}
		})
	}
}

func TestValidateActionsRecovery(t *testing.T) {
	fixture := workflowStore(t)
	ctx := context.Background()
	request := workflowRunRequest(t)
	request.Run.Event, request.Run.BaseOID, request.Run.PullRequestNumber = "pull_request", strings.Repeat("c", 40), 1
	request.Run.Facts.PullRequestAction, request.Run.Facts.PullRequestHeadRef = "opened", "feature"
	root, _, err := fixture.store.AdmitActionsRun(ctx, request, fixture.now)
	noErr(t, err)
	request.Run.RerunRoot, request.Run.RerunGeneration = root.ID, 1
	_, _, err = fixture.store.AdmitActionsRun(ctx, request, fixture.now.Add(time.Second))
	noErr(t, err)
	baseline, err := fixture.store.RecoverySnapshot(ctx)
	noErr(t, err)
	for _, test := range []struct {
		name    string
		mutate  func(*RecoveryState)
		invalid bool
	}{
		{name: "rerun root is a run"},
		{name: "job root is not a run", invalid: true, mutate: func(s *RecoveryState) { s.CheckJobs[1].RerunRoot = s.CheckJobs[0].ID }},
		{name: "unknown run", invalid: true, mutate: func(s *RecoveryState) { s.CheckJobs[0].RunID = strings.Repeat("c", 32) }},
		{name: "wrong run generation", invalid: true, mutate: func(s *RecoveryState) { s.CheckJobs[1].RerunGeneration++ }},
		{name: "root is a rerun", invalid: true, mutate: func(s *RecoveryState) { s.ActionsRuns[1].RerunRoot = s.ActionsRuns[1].ID }},
		{name: "duplicate number", invalid: true, mutate: func(s *RecoveryState) { s.ActionsRuns[1].Number = s.ActionsRuns[0].Number }},
		{name: "changed inputs", invalid: true, mutate: func(s *RecoveryState) { s.ActionsRuns[1].InputsJSON = `{"changed":true}` }},
		{name: "missing pull request action", invalid: true, mutate: func(s *RecoveryState) { s.ActionsRuns[0].Facts.PullRequestAction = "" }},
		{name: "unknown pull request action", invalid: true, mutate: func(s *RecoveryState) { s.ActionsRuns[0].Facts.PullRequestAction = "unknown" }},
		{name: "changed rerun action", invalid: true, mutate: func(s *RecoveryState) { s.ActionsRuns[1].Facts.PullRequestAction = "synchronize" }},
		{name: "missing pull request head", invalid: true, mutate: func(s *RecoveryState) { s.ActionsRuns[0].Facts.PullRequestHeadRef = "" }},
		{name: "invalid pull request head", invalid: true, mutate: func(s *RecoveryState) { s.ActionsRuns[0].Facts.PullRequestHeadRef = "feature..invalid" }},
		{name: "oversized pull request head", invalid: true, mutate: func(s *RecoveryState) {
			s.ActionsRuns[0].Facts.PullRequestHeadRef = strings.Repeat("a", MaximumPullRequestBranchBytes+1)
		}},
		{name: "changed rerun head", invalid: true, mutate: func(s *RecoveryState) { s.ActionsRuns[1].Facts.PullRequestHeadRef = "other" }},
		{name: "missing fail-fast", invalid: true, mutate: func(s *RecoveryState) { s.ActionsRuns[0].Facts.FailFast = nil }},
		{name: "unknown fail-fast key", invalid: true, mutate: func(s *RecoveryState) { s.ActionsRuns[0].Facts.FailFast = map[string]bool{"unknown": true} }},
		{name: "missing graph", invalid: true, mutate: func(s *RecoveryState) { s.ActionsRuns[0].Facts.Needs = nil }},
		{name: "unknown graph key", invalid: true, mutate: func(s *RecoveryState) {
			s.ActionsRuns[0].Facts.Needs = map[string][]string{"test": nil, "unknown": nil}
		}},
		{name: "unknown graph target", invalid: true, mutate: func(s *RecoveryState) { s.ActionsRuns[0].Facts.Needs = map[string][]string{"test": {"unknown"}} }},
		{name: "graph cycle", invalid: true, mutate: func(s *RecoveryState) { s.ActionsRuns[0].Facts.Needs = map[string][]string{"test": {"test"}} }},
		{name: "changed job key", invalid: true, mutate: func(s *RecoveryState) { s.CheckJobs[0].JobKey = "changed" }},
		{name: "JSON job with workflow facts", invalid: true, mutate: func(s *RecoveryState) { s.CheckJobs[0].RunID = "" }},
		{name: "waiting job has execution", invalid: true, mutate: func(s *RecoveryState) {
			s.CheckJobs[0].Status = CheckJobWaiting
			s.CheckJobs[0].StartedAt = &fixture.now
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			snapshot := cloneJobRecoveryState(baseline)
			snapshot.ActionsRuns = append([]ActionsRun(nil), baseline.ActionsRuns...)
			if test.mutate != nil {
				test.mutate(&snapshot)
			}
			err := ValidateCheckRecovery(snapshot)
			if (err != nil) != test.invalid {
				t.Fatalf("validation=%v", err)
			}
		})
	}
}
