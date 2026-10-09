package checkrun

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"owngit/internal/actions"
	"owngit/internal/checksource"
	"owngit/internal/repository"
	"owngit/internal/state"
	"owngit/internal/statepath"
)

const eventWorkflow = "name: CI\non:\n  push:\n  pull_request:\n  workflow_dispatch:\n    inputs:\n      dry_run:\n        type: boolean\n        default: false\n  schedule:\n    - cron: '0 0 * * *'\njobs:\n  build:\n    runs-on: ubuntu-latest\n    steps:\n      - run: echo build\n  after:\n    needs: build\n    runs-on: ubuntu-latest\n    steps:\n      - run: echo after\n"
const simpleWorkflow = "on: [push, pull_request, workflow_dispatch, schedule]\njobs:\n  test:\n    runs-on: ubuntu-latest\n    steps:\n      - run: echo extra\n"

func actionsFixture(t *testing.T, executor string) *pushFixture {
	t.Helper()
	fixture := newPushFixture(t, 16)
	_, err := fixture.store.SetCheckPolicy(fixture.ctx, state.CheckPolicyInput{RepositoryID: fixture.repositoryID, Executor: executor, AllowedEvents: []string{"push", "pull_request", state.ActionsEventDispatch, state.ActionsEventSchedule}, MaxTimeoutMS: 60_000, MaxOutputLimitBytes: 64 << 10, QueueLimit: 16, MaxActiveJobs: 4, MaxLeaseMS: 60_000}, time.Now().UTC())
	noErr(t, err)
	_, err = fixture.store.GrantCheckConsent(fixture.ctx, fixture.repositoryID, time.Now().UTC())
	noErr(t, err)
	return fixture
}

func pushActionsFiles(fixture *pushFixture, files map[string]string, accepted ...bool) string {
	fixture.t.Helper()
	for path, text := range files {
		full := filepath.Join(fixture.work, filepath.FromSlash(path))
		noErr(fixture.t, os.MkdirAll(filepath.Dir(full), 0o700))
		noErr(fixture.t, os.WriteFile(full, []byte(text), 0o600))
	}
	fixture.git("-C", fixture.work, "add", ".")
	fixture.git("-C", fixture.work, "commit", "--allow-empty", "-m", "Synthetic workflow revision")
	fixture.git("-C", fixture.work, "push", "--force", fixture.repoPath, "HEAD:refs/heads/main")
	fixture.noteOwnGitWrite()
	oid := fixture.git("-C", fixture.work, "rev-parse", "HEAD")
	if len(accepted) == 0 || accepted[0] {
		parents := strings.Fields(fixture.git("-C", fixture.work, "show", "-s", "--format=%P", "HEAD"))
		old := ""
		if len(parents) != 0 {
			old = parents[0]
		}
		noErr(fixture.t, fixture.store.RecordAcceptedActionsPushes(fixture.ctx, fixture.repositoryID, []state.AcceptedActionsPush{{Ref: "refs/heads/main", OldOID: old, NewOID: oid}}, time.Now().UTC()))
	}
	return oid
}

func workflowFixtureLog(t *testing.T, fixture *pushFixture, attemptID string) string {
	t.Helper()
	attempt, exists, err := fixture.store.CheckAttemptByID(fixture.ctx, fixture.repositoryID, attemptID)
	if err != nil || !exists {
		t.Fatalf("attempt=%v err=%v", exists, err)
	}
	content, status, err := fixture.store.ReadCheckLog(attempt, state.KeepCheckLogs, time.Now().UTC())
	if err != nil || status != state.CheckLogFound {
		t.Fatalf("log=%s err=%v", status, err)
	}
	return string(content)
}

func TestCoordinatorAdmitEvent(t *testing.T) {
	for _, test := range []struct {
		name, event, path    string
		off, noConsent, json bool
		want                 int
		wantErr              error
		filename             string
	}{
		{"push files", "push", "", false, false, true, 3, nil, ""},
		{"pull request files", "pull_request", "", false, false, false, 3, nil, ""},
		{"dispatch one file", state.ActionsEventDispatch, ".github/workflows/a-ci.yml", false, false, false, 1, nil, ""},
		{"dispatch imported head", state.ActionsEventDispatch, ".github/workflows/a-ci.yml", false, false, false, 0, nil, ""},
		{"dispatch branch moved after selection", state.ActionsEventDispatch, ".github/workflows/a-ci.yml", false, false, false, 0, nil, ""},
		{"dispatch branch deleted after selection", state.ActionsEventDispatch, ".github/workflows/a-ci.yml", false, false, false, 0, nil, ""},
		{"scheduled file", state.ActionsEventSchedule, ".github/workflows/a-ci.yml", false, false, false, 1, nil, ""},
		{"workflows off keeps JSON", "push", "", true, false, true, 0, nil, ""},
		{"workflows off rejects dispatch", state.ActionsEventDispatch, ".github/workflows/a-ci.yml", true, false, false, 0, state.ErrActionsWorkflowsOff, ""},
		{"consent required", "push", "", false, true, true, 0, state.ErrCheckConsentRequired, ""},
		{"line break filename", "push", "", false, false, true, 3, nil, "c-line\nbreak.yml"},
		{"backslash filename", "push", "", false, false, true, 3, nil, "c-back\\slash.yml"},
		{"ordinary filename", "push", "", false, false, true, 3, nil, "c-control.yml"},
		{"invalid actor keeps JSON", "push", "", false, false, true, 0, nil, ""},
		{"tab filename", "push", "", false, false, true, 3, nil, "c-tab\t.yml"},
		{"escape filename", "push", "", false, false, true, 3, nil, "c-esc\x1b.yml"},
		{"delete filename", "push", "", false, false, true, 3, nil, "c-del\x7f.yml"},
		{"other ASCII control filename", "push", "", false, false, true, 3, nil, "c-control\x01.yml"},
		{"Unicode control filename", "push", "", false, false, true, 3, nil, "c-control\u0085.yml"},
		{"invalid UTF-8 filename", "push", "", false, false, true, 3, nil, "c-invalid\xff.yml"},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := actionsFixture(t, state.CheckExecutorExternalRunner)
			if os.PathSeparator == '\\' && test.filename != "" && !state.ValidActionsWorkflowPath(".github/workflows/"+test.filename) {
				t.Skip("Unsupported Git filenames are exercised on Unix filesystems")
			}
			if runtime.GOOS == "darwin" && !utf8.ValidString(test.filename) {
				t.Skip("macOS file systems refuse invalid UTF-8 names; Linux covers this row")
			}
			files := map[string]string{".github/workflows/a-ci.yml": eventWorkflow, ".github/workflows/b-test.yaml": simpleWorkflow, ".github/workflows/c-bad.yml": "on: push\nunknown: true\njobs: {}\n", ".github/workflows/sub/ignored.yml": simpleWorkflow}
			if test.filename != "" {
				delete(files, ".github/workflows/c-bad.yml")
				files[".github/workflows/"+test.filename] = simpleWorkflow
			}
			if test.json {
				files[".owngit/checks.json"] = `{"version":1,"events":{"push":{}},"checks":[{"name":"unit","command":"echo JSON"}]}`
			}
			oid := pushActionsFiles(fixture, files, test.name != "dispatch imported head")
			if test.off {
				_, err := fixture.store.SetCheckPolicy(fixture.ctx, state.CheckPolicyInput{RepositoryID: fixture.repositoryID, Executor: state.CheckExecutorExternalRunner, RunWorkflows: new(false), AllowedEvents: []string{"push", "pull_request", state.ActionsEventDispatch, state.ActionsEventSchedule}, MaxTimeoutMS: 60_000, MaxOutputLimitBytes: 64 << 10, QueueLimit: 16, MaxActiveJobs: 4, MaxLeaseMS: 60_000}, time.Now().UTC())
				noErr(t, err)
				_, err = fixture.store.GrantCheckConsent(fixture.ctx, fixture.repositoryID, time.Now().UTC())
				noErr(t, err)
			}
			if test.noConsent {
				_, err := fixture.store.RevokeCheckConsent(fixture.ctx, fixture.repositoryID, time.Now().UTC())
				noErr(t, err)
			}
			event := EventRequest{RepositoryID: fixture.repositoryID, Event: test.event, EventKey: "synthetic-event", SourceOID: oid, TriggerRef: "main", WorkflowPath: test.path, Actor: state.Actor{Kind: state.ActorAccess}}
			if test.name == "invalid actor keeps JSON" {
				event.Actor.Kind = "unknown"
			}
			if test.event == "pull_request" {
				event.BaseOID, event.PullRequestNumber, event.HeadRef, event.Action = oid, 1, "feature", "opened"
			}
			if test.event == state.ActionsEventDispatch {
				event.Inputs = map[string]any{"dry_run": true}
				switch test.name {
				case "dispatch branch moved after selection":
					fixture.git("-C", fixture.work, "commit", "--allow-empty", "-m", "Synthetic newer head")
					fixture.git("-C", fixture.work, "push", fixture.repoPath, "HEAD:refs/heads/main")
					fixture.noteOwnGitWrite()
				case "dispatch branch deleted after selection":
					fixture.git("-C", fixture.repoPath, "update-ref", "-d", "refs/heads/main")
					fixture.noteOwnGitWrite()
				}
				if strings.HasPrefix(test.name, "dispatch branch ") {
					pinned, err := fixture.coordinator.Repositories.PinRepository(fixture.ctx, fixture.repositoryID, oid, oid)
					noErr(t, err)
					called := false
					err = pinned.WhileRefPresent(fixture.ctx, "refs/heads/main", oid, func() error { called = true; return nil })
					if !errors.Is(err, repository.ErrPinnedRefMoved) || called {
						t.Fatalf("final ref guard called=%v err=%v", called, err)
					}
				}
			}
			if test.event == state.ActionsEventSchedule {
				when := time.Now().UTC()
				event.ScheduledFor = &when
			}
			result, err := fixture.coordinator.AdmitEvent(fixture.ctx, event)
			if test.name == "dispatch imported head" || strings.HasPrefix(test.name, "dispatch branch ") {
				var refusal *actions.Refusal
				code := "note.push_required"
				if test.name != "dispatch imported head" {
					code = "workflow.moved"
				}
				if !errors.As(err, &refusal) || refusal.Code != code || len(result.Runs) != 0 {
					t.Fatalf("dispatch admission=%+v err=%v, want %s", result, err, code)
				}
				if refusal.Args != nil && test.name != "dispatch branch moved after selection" {
					t.Fatalf("dispatch refusal without a current commit has args: %+v", refusal.Message)
				}
				if test.name == "dispatch branch moved after selection" {
					_, currentOID, resolveErr := fixture.coordinator.Repositories.ResolveRef(fixture.ctx, fixture.repositoryID, "refs/heads/main")
					noErr(t, resolveErr)
					if len(refusal.Args) != 2 || refusal.Args["branch"] != "main" || refusal.Args["oid"] != currentOID {
						t.Fatalf("moved branch args=%v, want branch main and oid %s", refusal.Args, currentOID)
					}
				}
				return
			}
			if !errors.Is(err, test.wantErr) {
				t.Fatalf("admission=%+v err=%v, want %v", result, err, test.wantErr)
			}
			if err != nil {
				return
			}
			if len(result.Runs) != test.want || test.json && result.Job == nil {
				t.Fatalf("result=%+v", result)
			}
			if test.name == "invalid actor keeps JSON" {
				if len(result.Refusals) != 3 || len(fixture.logLines("workflow.invalid")) != 3 {
					t.Fatalf("missing refusal notes or logs: %+v logs=%v", result, fixture.logs)
				}
			}
			for _, run := range result.Runs {
				if test.filename != "" && test.filename != "c-control.yml" && run.WorkflowPath == ".github/workflows/"+test.filename {
					t.Fatalf("unsupported filename admitted: %+v", run)
				}
				if strings.HasPrefix(run.WorkflowPath, state.ActionsRefusedWorkflowPrefix) {
					if run.Outcome != "refused" || len(run.Facts.Notes) != 1 || run.Facts.Notes[0].Code != "workflow.limit" || run.Facts.Notes[0].Path != strconv.QuoteToASCII(".github/workflows/"+test.filename) {
						t.Fatalf("invalid filename refusal=%+v", run)
					}
					continue
				}
				if strings.HasSuffix(run.WorkflowPath, "c-bad.yml") {
					if run.Outcome != "refused" {
						t.Fatalf("bad file=%+v", run)
					}
					continue
				}
				jobs, err := fixture.store.ActionsRunJobs(fixture.ctx, fixture.repositoryID, run.ID)
				noErr(t, err)
				for _, job := range jobs {
					encoded, found, err := fixture.store.ActionsJobPlan(fixture.ctx, fixture.repositoryID, job.ID)
					if err != nil || !found {
						t.Fatalf("plan=%v err=%v", found, err)
					}
					plan, err := actions.DecodePlan(encoded, job.PlanDigest)
					noErr(t, err)
					if test.event == state.ActionsEventDispatch && plan.Context.Inputs["dry_run"] != true {
						t.Fatalf("typed inputs=%v", plan.Context.Inputs)
					}
					if test.event == "pull_request" && (plan.Context.GitHub.Event.Action != "opened" || plan.Context.GitHub.HeadRef != "feature" || plan.Context.GitHub.Ref != "refs/pull/1/head" || plan.Context.GitHub.RefName != "1/head" || plan.Context.GitHub.BaseRef != "main") {
						t.Fatalf("PR context=%+v", plan.Context.GitHub)
					}
					if test.event != "pull_request" && (plan.Context.GitHub.Ref != "refs/heads/main" || plan.Context.GitHub.RefName != "main") {
						t.Fatalf("branch event context=%+v", plan.Context.GitHub)
					}
					if job.JobKey == "after" && job.Status != "waiting" {
						t.Fatalf("dependent=%+v", job)
					}
				}
			}
			again, err := fixture.coordinator.AdmitEvent(fixture.ctx, event)
			if err != nil || again.Admitted {
				t.Fatalf("duplicate admission=%+v err=%v", again, err)
			}
		})
	}
}

func TestPullRequestLongHeadAdmission(t *testing.T) {
	for _, branch := range []string{"feature", strings.Repeat("a", 201), strings.Repeat("a", 100) + "/" + strings.Repeat("b", 154)} {
		t.Run(fmt.Sprint(len(branch)), func(t *testing.T) {
			fixture := actionsFixture(t, state.CheckExecutorExternalRunner)
			oid := pushActionsFiles(fixture, map[string]string{".github/workflows/ci.yml": simpleWorkflow, ".owngit/checks.json": `{"version":1,"events":{"pull_request":{}},"checks":[{"name":"unit","command":"echo JSON"}]}`})
			fixture.git("-C", fixture.work, "push", fixture.repoPath, "HEAD:refs/heads/"+branch)
			fixture.noteOwnGitWrite()
			pr, err := fixture.store.CreatePullRequest(fixture.ctx, fixture.repositoryID, "Synthetic source branch", branch, "main", oid, oid, state.ReviewNotRequested, time.Now().UTC())
			noErr(t, err)
			policy, _, err := fixture.store.CheckPolicy(fixture.ctx, fixture.repositoryID)
			noErr(t, err)
			admissionErr := fixture.coordinator.reconcilePullRequests(fixture.ctx, fixture.repositoryID, policy)
			jobs, err := fixture.store.CheckJobs(fixture.ctx, fixture.repositoryID)
			noErr(t, err)
			jsonJob, workflowJob := false, false
			for _, job := range jobs {
				if job.Trigger == "pull_request" && job.PullRequestNumber == pr.Number {
					jsonJob = jsonJob || job.RunID == ""
					workflowJob = workflowJob || job.RunID != ""
				}
			}
			if admissionErr != nil || !jsonJob || !workflowJob {
				t.Fatalf("admission error=%v JSON job=%v workflow job=%v", admissionErr, jsonJob, workflowJob)
			}
		})
	}
}

func TestCoordinatorRerunActionsRun(t *testing.T) {
	for _, test := range []struct {
		name, action, types string
	}{
		{"opened filter", "opened", "[opened]"},
		{"opened context", "opened", "[opened, synchronize]"},
		{"synchronize", "synchronize", "[synchronize]"},
		{"reopened", "reopened", "[reopened]"},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := actionsFixture(t, state.CheckExecutorExternalRunner)
			workflow := "on:\n  pull_request:\n    types: " + test.types + "\njobs:\n  test:\n    runs-on: ubuntu-latest\n    steps:\n      - run: echo ${{ github.event.action }}\n"
			oid := pushActionsFiles(fixture, map[string]string{".github/workflows/ci.yml": workflow})
			pr, err := fixture.store.CreatePullRequest(fixture.ctx, fixture.repositoryID, "Synthetic change", "feature", "main", oid, oid, state.ReviewNotRequested, time.Now().UTC())
			noErr(t, err)
			event := EventRequest{RepositoryID: fixture.repositoryID, Event: "pull_request", EventKey: fmt.Sprintf("pr/%d/%s/%s", pr.Number, oid, oid), SourceOID: oid, BaseOID: oid, PullRequestNumber: pr.Number, TriggerRef: "main", HeadRef: "feature", Action: test.action}
			result, err := fixture.coordinator.AdmitEvent(fixture.ctx, event)
			noErr(t, err)
			if len(result.Runs) != 1 {
				t.Fatalf("initial runs=%d", len(result.Runs))
			}
			_, err = fixture.store.CancelActionsRun(fixture.ctx, fixture.repositoryID, result.Runs[0].ID, time.Now().UTC())
			noErr(t, err)
			rerun, deduped, err := fixture.coordinator.RerunActionsRun(fixture.ctx, fixture.repositoryID, result.Runs[0].ID)
			if err != nil || deduped || rerun.RerunGeneration != 1 {
				t.Fatalf("rerun generation=%d dedup=%v err=%v", rerun.RerunGeneration, deduped, err)
			}
			jobs, err := fixture.store.ActionsRunJobs(fixture.ctx, fixture.repositoryID, rerun.ID)
			noErr(t, err)
			if len(jobs) != 1 {
				t.Fatalf("rerun jobs=%d", len(jobs))
			}
			encoded, exists, err := fixture.store.ActionsJobPlan(fixture.ctx, fixture.repositoryID, jobs[0].ID)
			if err != nil || !exists {
				t.Fatalf("plan=%v err=%v", exists, err)
			}
			plan, err := actions.DecodePlan(encoded, jobs[0].PlanDigest)
			noErr(t, err)
			if plan.Context.GitHub.Event.Action != test.action || plan.Context.GitHub.HeadRef != event.HeadRef || plan.Context.GitHub.Ref != fmt.Sprintf("refs/pull/%d/head", pr.Number) || plan.Context.GitHub.RefName != fmt.Sprintf("%d/head", pr.Number) {
				t.Fatalf("rerun PR context=%+v, want action=%s head=%s", plan.Context.GitHub, test.action, event.HeadRef)
			}
		})
	}
}

func TestCoordinatorPathFilters(t *testing.T) {
	for _, test := range []struct {
		name, filters, path, previous, want string
		many, rerun                         bool
	}{
		{"match", "paths: ['src/**']", "src/changed.txt", "base", "run", false, false},
		{"not matched", "paths: ['src/**']", "docs/readme.txt", "base", "none", false, false},
		{"ignored", "paths-ignore: ['docs/**']", "docs/readme.txt", "base", "none", false, false},
		{"unknown base", "paths: ['src/**']", "src/changed.txt", "missing", "not_run", false, false},
		{"cut list decides", "paths: ['docs/**']", "docs/", "base", "run", true, false},
		{"cut list undecided", "paths-ignore: ['docs/**']", "docs/", "base", "not_run", true, false},
		{"explicit rerun bypasses paths", "paths: ['src/**']", "docs/readme.txt", "missing", "not_run", false, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := actionsFixture(t, state.CheckExecutorExternalRunner)
			workflow := "on:\n  push:\n    " + test.filters + "\njobs:\n  test:\n    runs-on: ubuntu-latest\n    steps:\n      - run: echo paths\n"
			base := pushActionsFiles(fixture, map[string]string{".github/workflows/ci.yml": workflow, "base.txt": "base"})
			files := map[string]string{test.path: "changed"}
			if test.many {
				files = map[string]string{}
				for index := range 3001 {
					files[fmt.Sprintf("%s%04d.txt", test.path, index)] = "synthetic"
				}
			}
			head := pushActionsFiles(fixture, files)
			previous := base
			if test.previous == "missing" {
				previous = strings.Repeat("a", 40)
				noErr(t, fixture.store.Exec(fixture.ctx, `UPDATE actions_accepted_pushes SET old_oid=? WHERE new_oid=?`, previous, head))
			}
			result, err := fixture.coordinator.AdmitEvent(fixture.ctx, EventRequest{RepositoryID: fixture.repositoryID, Event: "push", SourceOID: head, PreviousOID: previous, TriggerRef: "main"})
			noErr(t, err)
			if test.want == "none" {
				if len(result.Runs) != 0 {
					t.Fatalf("unmatched runs=%+v", result.Runs)
				}
				return
			}
			if len(result.Runs) != 1 || test.want == "not_run" && result.Runs[0].Outcome != "not_run" || test.want == "run" && result.Runs[0].Outcome != "" {
				t.Fatalf("runs=%+v", result.Runs)
			}
			if test.rerun {
				run, deduped, err := fixture.coordinator.RerunActionsRun(fixture.ctx, fixture.repositoryID, result.Runs[0].ID)
				if err != nil || deduped || run.Outcome != "" || run.RerunGeneration != 1 {
					t.Fatalf("rerun=%+v dedup=%v err=%v", run, deduped, err)
				}
			}
		})
	}
}

func TestCoordinatorRefusedNames(t *testing.T) {
	for _, test := range []struct {
		name, filename, branch string
		refused                bool
		entry, limit           string
	}{
		{"long workflow name", strings.Repeat("a", 97) + ".yml", "main", true, "", "100 bytes"},
		{"control character workflow name", "bad\tname.yml", "main", true, "", ""},
		{"near-limit workflow name", strings.Repeat("a", 96) + ".yml", "main", false, "", ""},
		{"long branch", "ci.yml", strings.Repeat("b", 201), true, "", "200 bytes"},
		{"near-limit branch", "ci.yml", strings.Repeat("b", 200), false, "", ""},
		{"push branch within observation bound", "ci.yml", strings.Repeat("a", 201), true, "push", "200 bytes"},
		{"push branch above observation bound", "ci.yml", strings.Repeat("a", 100) + "/" + strings.Repeat("b", 100) + "/" + strings.Repeat("c", 100), true, "push", "200 bytes"},
		{"head branch within observation bound", "ci.yml", strings.Repeat("a", 201), true, "head", "200 bytes"},
		{"head branch above observation bound", "ci.yml", strings.Repeat("a", 100) + "/" + strings.Repeat("b", 100) + "/" + strings.Repeat("c", 100), true, "head", "200 bytes"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if runtime.GOOS == "windows" && strings.ContainsRune(test.filename, '\t') {
				t.Skip("Windows cannot create a filename containing a tab")
			}
			fixture := actionsFixture(t, state.CheckExecutorExternalRunner)
			path := ".github/workflows/" + test.filename
			oid := pushActionsFiles(fixture, map[string]string{path: simpleWorkflow})
			var result state.CheckEventAdmission
			if test.entry == "" {
				if test.branch != "main" {
					noErr(t, fixture.store.RecordAcceptedActionsPushes(fixture.ctx, fixture.repositoryID, []state.AcceptedActionsPush{{Ref: "refs/heads/" + test.branch, NewOID: oid}}, time.Now().UTC()))
				}
				var err error
				result, err = fixture.coordinator.AdmitEvent(fixture.ctx, EventRequest{RepositoryID: fixture.repositoryID, Event: "push", SourceOID: oid, TriggerRef: test.branch})
				noErr(t, err)
			} else {
				ref := "refs/heads/" + test.branch
				fixture.git("-C", fixture.work, "push", fixture.repoPath, "HEAD:"+ref)
				fixture.noteOwnGitWrite()
				noErr(t, fixture.store.RecordAcceptedActionsPushes(fixture.ctx, fixture.repositoryID, []state.AcceptedActionsPush{{Ref: ref, NewOID: oid}}, time.Now().UTC()))
				if test.entry == "push" {
					fixture.coordinator.NotePush(fixture.repositoryID, []PushUpdate{{Ref: ref, New: oid}})
					fixture.coordinator.admitPendingPushes(fixture.ctx)
				} else {
					policy, _, err := fixture.store.CheckPolicy(fixture.ctx, fixture.repositoryID)
					noErr(t, err)
					noErr(t, fixture.coordinator.reconcilePushes(fixture.ctx, fixture.repositoryID, policy))
				}
				runs, err := fixture.store.ActionsRunsForRevision(fixture.ctx, fixture.repositoryID, oid)
				noErr(t, err)
				for _, run := range runs {
					if run.TriggerRef != "main" {
						result.Runs = append(result.Runs, run)
					}
				}
			}
			if len(result.Runs) != 1 {
				t.Fatalf("runs=%+v", result.Runs)
			}
			run := result.Runs[0]
			if test.refused {
				if run.Outcome != "refused" || len(run.Facts.Notes) == 0 || run.Facts.Notes[0].Code != "workflow.limit" || run.WorkflowPath == path && run.TriggerRef == test.branch {
					t.Fatalf("refusal=%+v", run)
				}
				if test.limit == "" {
					if run.Facts.Notes[0].Args != nil || run.Reason != "Workflow filename cannot be stored as a run identity." {
						t.Fatalf("unsupported name must use detail: %+v", run)
					}
				} else {
					what := test.branch
					if len(test.filename) > 100 {
						what = path
					}
					if run.Facts.Notes[0].Args["what"] != what || run.Facts.Notes[0].Args["limit"] != test.limit || len(run.Facts.Notes[0].Args) != 2 {
						t.Fatalf("limit args=%v, want %q of %s", run.Facts.Notes[0].Args, what, test.limit)
					}
				}
			} else if run.Outcome != "" || run.WorkflowPath != path || run.TriggerRef != test.branch {
				t.Fatalf("near-limit name changed: %+v", run)
			}
			snapshot, err := fixture.store.RecoverySnapshot(fixture.ctx)
			noErr(t, err)
			noErr(t, state.ValidateCheckRecovery(snapshot))
			if test.refused {
				for index := range snapshot.ActionsRuns {
					if snapshot.ActionsRuns[index].ID == run.ID {
						snapshot.ActionsRuns[index].Outcome = "not_run"
					}
				}
				if err := state.ValidateCheckRecovery(snapshot); err == nil {
					t.Fatal("surrogate accepted on a non-refused run")
				}
			}
		})
	}
}

func TestCoordinatorWorkflowAuthority(t *testing.T) {
	for _, test := range []struct {
		name, event, workflow string
		accepted, off         bool
		want                  int
	}{
		{"unaccepted push head", "push", simpleWorkflow, false, false, 0},
		{"unaccepted pull request source", "pull_request", simpleWorkflow, false, false, 0},
		{"accepted push after restart", "push", simpleWorkflow, true, false, 1},
		{"accepted pull request source", "pull_request", simpleWorkflow, true, false, 1},
		{"workflows off consumes push", "push", simpleWorkflow, true, true, 0},
		{"nonmatching workflow consumes push", "push", "on: pull_request\njobs:\n  test:\n    runs-on: ubuntu-latest\n    steps:\n      - run: echo extra\n", true, false, 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := actionsFixture(t, state.CheckExecutorExternalRunner)
			files := map[string]string{".github/workflows/ci.yml": test.workflow, ".owngit/checks.json": `{"version":1,"events":{"push":{},"pull_request":{}},"checks":[{"name":"unit","command":"echo JSON"}]}`}
			base := pushActionsFiles(fixture, files)
			if test.event == "pull_request" {
				fixture.git("-C", fixture.work, "push", fixture.repoPath, "HEAD:refs/heads/feature")
				_, err := fixture.store.CreatePullRequest(fixture.ctx, fixture.repositoryID, "Synthetic change", "feature", "main", base, base, state.ReviewNotRequested, time.Now().UTC())
				noErr(t, err)
			}
			head := pushActionsFiles(fixture, map[string]string{"revision.txt": "Synthetic ref update"}, test.accepted)
			if test.off {
				_, err := fixture.store.SetCheckPolicy(fixture.ctx, state.CheckPolicyInput{RepositoryID: fixture.repositoryID, Executor: state.CheckExecutorExternalRunner, RunWorkflows: new(false), AllowedEvents: []string{"push", "pull_request"}, MaxTimeoutMS: 60_000, MaxOutputLimitBytes: 64 << 10, QueueLimit: 16, MaxActiveJobs: 4, MaxLeaseMS: 60_000}, time.Now().UTC())
				noErr(t, err)
				_, err = fixture.store.GrantCheckConsent(fixture.ctx, fixture.repositoryID, time.Now().UTC())
				noErr(t, err)
			}
			policy, _, err := fixture.store.CheckPolicy(fixture.ctx, fixture.repositoryID)
			noErr(t, err)
			if test.event == "pull_request" {
				fixture.git("-C", fixture.work, "push", fixture.repoPath, "HEAD:refs/heads/feature")
				fixture.noteOwnGitWrite()
				noErr(t, fixture.coordinator.reconcilePullRequests(fixture.ctx, fixture.repositoryID, policy))
				note, err := fixture.store.ActionsPullRequestAdmissionNote(fixture.ctx, fixture.repositoryID, head)
				noErr(t, err)
				if (note == nil) != test.accepted || note != nil && note.Code != "note.push_required" {
					t.Fatalf("source authority note=%+v", note)
				}
			} else {
				fixture.coordinator.pendingPushes = nil
				noErr(t, fixture.coordinator.reconcilePushes(fixture.ctx, fixture.repositoryID, policy))
				pending, err := fixture.store.PendingAcceptedActionsPushes(fixture.ctx, fixture.repositoryID, 16)
				noErr(t, err)
				if len(pending) != 0 {
					t.Fatalf("accepted pushes not consumed: %+v", pending)
				}
			}
			runs, err := fixture.store.ActionsRunsForRevision(fixture.ctx, fixture.repositoryID, head)
			noErr(t, err)
			if len(runs) != test.want {
				t.Fatalf("workflow runs=%d, want %d", len(runs), test.want)
			}
			jobs, err := fixture.store.LatestCheckJobs(fixture.ctx, fixture.repositoryID, 16)
			noErr(t, err)
			foundJSON := false
			for _, job := range jobs {
				foundJSON = foundJSON || job.SourceOID == head && job.RunID == ""
			}
			if !foundJSON {
				t.Fatal("ref reconciliation lost the JSON check")
			}
		})
	}
}

func TestCoordinatorHostActions(t *testing.T) {
	for _, test := range []struct {
		name, workflow, status, output string
		count                          int
		json, container                bool
		sourcePath                     string
	}{
		{"two files and needs", eventWorkflow, "passed", "after", 3, false, false, ""},
		{"multi-line display", "on: push\njobs:\n  test:\n    runs-on: ubuntu-latest\n    steps:\n      - run: |\n          echo first\n          echo second\n", "passed", "second", 1, false, false, ""},
		{"step outputs", "on: push\njobs:\n  test:\n    runs-on: ubuntu-latest\n    steps:\n      - id: source\n        run: echo 'answer=ready' >> \"$GITHUB_OUTPUT\"\n      - env:\n          ANSWER: ${{ steps.source.outputs.answer }}\n        run: test \"$ANSWER\" = ready && echo OUTPUT_READY\n", "passed", "OUTPUT_READY", 1, false, false, ""},
		{"tracked source change before builtin", "on: push\njobs:\n  test:\n    runs-on: ubuntu-latest\n    steps:\n      - run: |\n          echo changed > .github/workflows/ci.yml\n          echo TRACKED_CHANGED\n      - uses: actions/checkout@v4\n", "incomplete", "TRACKED_CHANGED", 1, false, false, ""},
		{"all skipped", "on: push\njobs:\n  test:\n    runs-on: ubuntu-latest\n    steps:\n      - if: false\n        run: echo MUST_NOT_RUN\n", "skipped", "", 1, false, false, ""},
		{"pull request ref and guarded step", "on: pull_request\njobs:\n  test:\n    runs-on: ubuntu-latest\n    steps:\n      - if: github.ref == 'refs/heads/main'\n        run: echo MUST_NOT_RUN\n      - run: printenv GITHUB_REF\n", "passed", "refs/pull/7/head", 1, false, false, ""},
		{"builtin only", "on: push\njobs:\n  test:\n    runs-on: ubuntu-latest\n    steps:\n      - uses: actions/checkout@v4\n", "skipped", "", 1, false, false, ""},
		{"JSON keeps later command", "", "failed", "JSON_CONTINUED", 1, true, false, ""},
		{"control character source", simpleWorkflow, "unavailable", "", 2, false, false, "unsafe/bell\a.txt"},
		{"invalid UTF-8 source", simpleWorkflow, "unavailable", "", 2, false, false, "unsafe/invalid\xff.txt"},
		{"container passes", "on: push\njobs:\n  test:\n    runs-on: windows-latest\n    steps:\n      - run: test \"$GITHUB_WORKSPACE\" = /workspace; test \"$RUNNER_OS\" = Linux; echo state > \"$HOME/job-state\"\n      - run: test -f \"$HOME/job-state\"; echo CONTAINER_PASSED\n", "passed", "CONTAINER_PASSED", 1, false, true, ""},
		{"container step failure", "on: push\njobs:\n  test:\n    runs-on: windows-latest\n    steps:\n      - shell: sh\n        run: echo STEP_FAILED; exit 7\n      - run: echo MUST_NOT_RUN\n", "failed", "STEP_FAILED", 1, false, true, ""},
		{"container masks secret", "on: push\njobs:\n  test:\n    runs-on: ubuntu-latest\n    steps:\n      - env:\n          TOKEN: ${{ secrets.TOKEN }}\n        run: test -n \"$TOKEN\" && test -z \"$OTHER\" && printf 'token:%s:end\\n' \"$TOKEN\"\n", "passed", "[redacted]", 1, false, true, ""},
		{"container tracked source", "on: push\njobs:\n  test:\n    runs-on: ubuntu-latest\n    steps:\n      - run: echo changed > .github/workflows/ci.yml; echo TRACKED_CHANGED\n      - uses: actions/checkout@v4\n", "incomplete", "TRACKED_CHANGED", 1, false, true, ""},
		{"container early timeout", "on: push\njobs:\n  test:\n    runs-on: ubuntu-latest\n    timeout-minutes: 0.000000001\n    steps:\n      - run: echo MUST_NOT_RUN\n", "incomplete", "note.stopped", 1, false, true, ""},
		{"container engine unavailable", simpleWorkflow, "unavailable", "", 1, false, true, ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			if test.sourcePath != "" && os.PathSeparator == '\\' {
				t.Skip("Unsupported Git filenames are exercised on Unix filesystems")
			}
			if !utf8.ValidString(test.sourcePath) && runtime.GOOS != "linux" {
				t.Skip("Linux covers Git paths that contain invalid UTF-8")
			}
			executor := state.CheckExecutorHost
			if test.container {
				if runtime.GOOS != "linux" || testing.Short() || os.Getenv("OWNGIT_REAL_DOCKER_TEST") != "1" {
					t.Skip("requires opt-in real Docker on Linux")
				}
				if os.Geteuid() == 0 || !state.ImmutableContainerImage(os.Getenv("OWNGIT_DOCKER_IMAGE")) || !filepath.IsAbs(os.Getenv("TMPDIR")) {
					t.Fatal("requires a nonroot controller, cached immutable image and shared absolute TMPDIR")
				}
				executor = state.CheckExecutorContainer
			}
			fixture := actionsFixture(t, state.CheckExecutorHost)
			if test.container {
				_, err := fixture.store.SetCheckPolicy(fixture.ctx, state.CheckPolicyInput{RepositoryID: fixture.repositoryID, Executor: executor,
					AllowedEvents: []string{"push"}, MaxTimeoutMS: 60_000, MaxOutputLimitBytes: 64 << 10, QueueLimit: 16, MaxActiveJobs: 4, MaxLeaseMS: 60_000,
					Execution: state.CheckExecutionSettings{ContainerRuntime: "docker-local", ContainerImage: os.Getenv("OWNGIT_DOCKER_IMAGE"), ContainerNetwork: "none",
						ContainerCPUMillis: 1000, ContainerMemoryBytes: 128 << 20, ContainerPIDs: 64, ContainerScratchBytes: 16 << 20}}, time.Now().UTC())
				noErr(t, err)
				_, err = fixture.store.GrantCheckConsent(fixture.ctx, fixture.repositoryID, time.Now().UTC())
				noErr(t, err)
			}
			if test.name == "container engine unavailable" {
				fixture.coordinator.DockerPath = "/owngit-no-such-docker"
			}
			const secret = "synthetic-selected-secret"
			if test.name == "container masks secret" {
				for name, value := range map[string]string{"TOKEN": secret, "OTHER": "synthetic-not-delivered"} {
					_, err := fixture.store.SetWorkflowSecret(fixture.ctx, fixture.repositoryID, name, value, state.Actor{Kind: state.ActorAdministrator}, time.Now().UTC())
					noErr(t, err)
				}
			}
			files := map[string]string{}
			if test.sourcePath != "" {
				files[".owngit/checks.json"] = `{"version":1,"events":{"push":{}},"checks":[{"name":"json","command":"echo MUST_NOT_RUN"}]}`
				files[".github/workflows/ci.yml"] = test.workflow
				files[test.sourcePath] = "unsupported source name"
			} else if test.json {
				files[".owngit/checks.json"] = `{"version":1,"events":{"push":{}},"checks":[{"name":"fail","command":"exit 1"},{"name":"later","command":"echo JSON_CONTINUED"}]}`
			} else {
				files[".github/workflows/ci.yml"] = test.workflow
			}
			if test.count == 3 {
				files[".github/workflows/extra.yml"] = simpleWorkflow
			}
			oid := pushActionsFiles(fixture, files)
			event := EventRequest{RepositoryID: fixture.repositoryID, Event: "push", SourceOID: oid, TriggerRef: "main"}
			if test.name == "pull request ref and guarded step" {
				event.Event, event.BaseOID, event.PullRequestNumber, event.HeadRef, event.Action = "pull_request", oid, 7, "feature", "opened"
				event.EventKey = fmt.Sprintf("pr/7/%s/%s", oid, oid)
			}
			result, err := fixture.coordinator.AdmitEvent(fixture.ctx, event)
			noErr(t, err)
			workspace, err := checksource.AcquireWorkspaceRoot(filepath.Join(t.TempDir(), "jobs"))
			noErr(t, err)
			t.Cleanup(workspace.Close)
			fixture.coordinator.workspace = workspace
			for range test.count {
				noErr(t, fixture.coordinator.runOneLocal(fixture.ctx))
			}
			jobs, err := fixture.store.CheckJobs(fixture.ctx, fixture.repositoryID)
			noErr(t, err)
			if len(jobs) != test.count {
				t.Fatalf("jobs=%+v", jobs)
			}
			seenOutput := test.output == ""
			sourceSummary := ""
			for _, job := range jobs {
				if job.Status != test.status || (job.StartedAt == nil) != (test.status == "unavailable") {
					for _, item := range jobs {
						if item.AttemptID != "" {
							t.Logf("job %s log=%q", item.JobKey, workflowFixtureLog(t, fixture, item.AttemptID))
						}
					}
					t.Fatalf("job=%+v", job)
				}
				log := ""
				if job.AttemptID != "" {
					log = workflowFixtureLog(t, fixture, job.AttemptID)
					attempt, found, err := fixture.store.CheckAttemptByID(fixture.ctx, fixture.repositoryID, job.AttemptID)
					noErr(t, err)
					protection := state.ProtectionHost
					if test.container {
						protection = state.ProtectionContainer
					}
					if !found || attempt.Protection != protection {
						t.Fatalf("attempt=%+v", attempt)
					}
					for _, step := range attempt.Results {
						if strings.Contains(step.OutputExcerpt, "MUST_NOT_RUN") {
							t.Fatalf("skipped script executed: %+v", step)
						}
					}
				}
				if strings.Contains(log, secret) || strings.Contains(log, "synthetic-not-delivered") {
					t.Fatalf("unexpected output: %q", log)
				}
				if test.sourcePath != "" {
					if !strings.Contains(job.Summary, strconv.Quote(test.sourcePath)) || len(job.Summary) > 500 {
						t.Fatalf("source refusal did not name the bounded escaped path: %q", job.Summary)
					}
					if sourceSummary == "" {
						sourceSummary = job.Summary
					} else if job.Summary != sourceSummary {
						t.Fatalf("source refusals differ: %q != %q", job.Summary, sourceSummary)
					}
				}
				seenOutput = seenOutput || strings.Contains(log, test.output)
				if !test.json {
					if _, exists, err := fixture.store.ActionsJobPlan(fixture.ctx, fixture.repositoryID, job.ID); err != nil || exists {
						t.Fatalf("finished plan=%v err=%v", exists, err)
					}
				}
			}
			if !seenOutput {
				t.Fatalf("expected output %q absent", test.output)
			}
			if test.count == 3 && len(result.Runs) != 2 {
				t.Fatalf("per-file runs=%+v", result.Runs)
			}
			if test.sourcePath != "" && len(result.Runs) != 1 {
				t.Fatalf("source refusal runs=%+v", result.Runs)
			}
			snapshot, err := fixture.store.RecoverySnapshot(fixture.ctx)
			noErr(t, err)
			noErr(t, state.ValidateCheckRecovery(snapshot))
			encoded, err := json.Marshal(snapshot)
			noErr(t, err)
			if strings.Contains(string(encoded), secret) {
				t.Fatal("secret entered portable state")
			}
			owned, err := fixture.store.ActiveCheckContainers(fixture.ctx, 100)
			noErr(t, err)
			if len(owned) != 0 {
				t.Fatalf("containers left: %+v", owned)
			}
		})
	}
}

func TestActionsStateResults(t *testing.T) {
	attempt := state.CheckAttempt{Checks: []state.CheckDefinition{{Name: "Run echo test", Command: "echo test"}}}
	for _, test := range []struct {
		name, jobStatus, stepStatus, role, want string
		cancelled                               bool
	}{
		{"timeout before run", "incomplete", "skipped", "run", "incomplete", false},
		{"timeout before builtin", "incomplete", "skipped", "builtin", "incomplete", false},
		{"ordinary skipped", "skipped", "skipped", "run", "skipped", false},
		{"builtins only", "skipped", "passed", "builtin", "skipped", false},
		{"counted failure preserved", "incomplete", "failed", "run", "failed", false},
		{"cancellation preserved", "incomplete", "skipped", "run", "cancelled", true},
	} {
		t.Run(test.name, func(t *testing.T) {
			job := actions.JobResult{Status: test.jobStatus, Cancelled: test.cancelled, Steps: []actions.StepResult{{ScriptResult: actions.ScriptResult{Status: test.stepStatus}, Role: test.role}}}
			results := actionsStateResults(attempt, job)
			if got := state.AggregateActionsAttemptStatus(results, job.Cancelled); got != test.want {
				t.Fatalf("stored status=%s, want %s", got, test.want)
			}
			if job.Steps[0].Status != test.stepStatus || job.Steps[0].Role != test.role {
				t.Fatal("raw engine results changed")
			}
		})
	}
}

func TestCoordinatorSecretsBeforeStart(t *testing.T) {
	for _, kind := range []string{"selected secret", "missing secret", "unreadable secrets", "no names still checks file", "corrupt plan"} {
		t.Run(kind, func(t *testing.T) {
			fixture := actionsFixture(t, state.CheckExecutorHost)
			workflow := "on: push\njobs:\n  test:\n    runs-on: ubuntu-latest\n    steps:\n      - env:\n          TOKEN: ${{ secrets.TOKEN }}\n        run: printf 'token:%s:end\\n' \"$TOKEN\"\n"
			if kind == "selected secret" {
				workflow = strings.Replace(workflow, "run: printf", "run: test -n \"$TOKEN\" && test -z \"$OTHER\" && printf", 1)
			}
			if kind == "no names still checks file" {
				workflow = simpleWorkflow
			}
			oid := pushActionsFiles(fixture, map[string]string{".github/workflows/ci.yml": workflow})
			_, err := fixture.coordinator.AdmitEvent(fixture.ctx, EventRequest{RepositoryID: fixture.repositoryID, Event: "push", SourceOID: oid, TriggerRef: "main"})
			noErr(t, err)
			secret := "synthetic-selected-secret"
			if kind != "missing secret" {
				_, err := fixture.store.SetWorkflowSecret(fixture.ctx, fixture.repositoryID, "TOKEN", secret, state.Actor{Kind: state.ActorAdministrator}, time.Now().UTC())
				noErr(t, err)
				_, err = fixture.store.SetWorkflowSecret(fixture.ctx, fixture.repositoryID, "OTHER", "synthetic-not-delivered", state.Actor{Kind: state.ActorAdministrator}, time.Now().UTC())
				noErr(t, err)
			}
			if kind == "unreadable secrets" || kind == "no names still checks file" {
				noErr(t, os.WriteFile(filepath.Join(fixture.store.Dir(), statepath.WorkflowSecrets, fixture.repositoryID+".json"), []byte("{invalid synthetic secret file"), 0o600))
			}
			if kind == "corrupt plan" {
				noErr(t, fixture.store.Exec(fixture.ctx, `UPDATE actions_job_plans SET plan_json=plan_json||' '`))
			}
			workspace, err := checksource.AcquireWorkspaceRoot(filepath.Join(t.TempDir(), "jobs"))
			noErr(t, err)
			t.Cleanup(workspace.Close)
			fixture.coordinator.workspace = workspace
			noErr(t, fixture.coordinator.runOneLocal(fixture.ctx))
			jobs, err := fixture.store.CheckJobs(fixture.ctx, fixture.repositoryID)
			noErr(t, err)
			if len(jobs) != 1 {
				t.Fatalf("jobs=%+v", jobs)
			}
			job := jobs[0]
			if kind == "unreadable secrets" || kind == "no names still checks file" || kind == "corrupt plan" {
				code := "workflow.secrets_unreadable"
				if kind == "corrupt plan" {
					code = "workflow.plan"
				}
				if job.Status != "error" || job.StartedAt != nil || job.AttemptID != "" || !strings.Contains(job.Summary, code) {
					t.Fatalf("unstarted failure=%+v", job)
				}
				return
			}
			if job.Status != "passed" || job.StartedAt == nil {
				t.Fatalf("job=%+v log=%q", job, workflowFixtureLog(t, fixture, job.AttemptID))
			}
			log := workflowFixtureLog(t, fixture, job.AttemptID)
			if strings.Contains(log, secret) || strings.Contains(log, "synthetic-not-delivered") || kind == "selected secret" && !strings.Contains(log, "[redacted]") {
				t.Fatalf("masking log=%q", log)
			}
			snapshot, err := fixture.store.RecoverySnapshot(fixture.ctx)
			noErr(t, err)
			encoded, err := json.Marshal(snapshot)
			noErr(t, err)
			if strings.Contains(string(encoded), secret) {
				t.Fatal("secret entered portable state")
			}
		})
	}
}
