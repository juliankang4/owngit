package recovery

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"owngit/internal/actions"
	"owngit/internal/gitexec"
	"owngit/internal/repository"
	"owngit/internal/state"
	"owngit/internal/testfixture"
)

func TestFormat12Content(t *testing.T) {
	for _, test := range []struct {
		name    string
		edit    func(*Manifest)
		version int
	}{
		{name: "JSON only", version: 10},
		{name: "workflow run", version: 12, edit: func(m *Manifest) { m.ActionsRuns = []state.ActionsRun{{}} }},
		{name: "policy opt-in", version: 12, edit: func(m *Manifest) { m.CheckPolicies = []CheckPolicyManifest{{RunWorkflows: true}} }},
		{name: "dispatch allowed", version: 12, edit: func(m *Manifest) {
			m.CheckPolicies = []CheckPolicyManifest{{AllowedEvents: []string{state.ActionsEventDispatch}}}
		}},
		{name: "schedule allowed", version: 12, edit: func(m *Manifest) {
			m.CheckPolicies = []CheckPolicyManifest{{AllowedEvents: []string{state.ActionsEventSchedule}}}
		}},
		{name: "run ID", version: 12, edit: func(m *Manifest) { m.CheckJobs = []CheckJobManifest{{RunID: "run"}} }},
		{name: "job key", version: 12, edit: func(m *Manifest) { m.CheckJobs = []CheckJobManifest{{JobKey: "test"}} }},
		{name: "matrix index", version: 12, edit: func(m *Manifest) { m.CheckJobs = []CheckJobManifest{{MatrixIndex: 1}} }},
		{name: "plan digest", version: 12, edit: func(m *Manifest) { m.CheckJobs = []CheckJobManifest{{PlanDigest: "digest"}} }},
		{name: "tolerated job", version: 12, edit: func(m *Manifest) { m.CheckJobs = []CheckJobManifest{{Tolerated: true}} }},
		{name: "concurrency group", version: 12, edit: func(m *Manifest) { m.CheckJobs = []CheckJobManifest{{ConcurrencyGroup: "test"}} }},
		{name: "max parallel", version: 12, edit: func(m *Manifest) { m.CheckJobs = []CheckJobManifest{{MaxParallel: 2}} }},
		{name: "waiting job", version: 12, edit: func(m *Manifest) { m.CheckJobs = []CheckJobManifest{{Status: "waiting"}} }},
		{name: "skipped job", version: 12, edit: func(m *Manifest) { m.CheckJobs = []CheckJobManifest{{Status: "skipped"}} }},
		{name: "dispatch job", version: 12, edit: func(m *Manifest) { m.CheckJobs = []CheckJobManifest{{Trigger: state.ActionsEventDispatch}} }},
		{name: "schedule job", version: 12, edit: func(m *Manifest) { m.CheckJobs = []CheckJobManifest{{Trigger: state.ActionsEventSchedule}} }},
		{name: "skipped attempt", version: 12, edit: func(m *Manifest) { m.CheckAttempts = []CheckAttemptManifest{{Status: "skipped"}} }},
		{name: "result role", version: 12, edit: func(m *Manifest) { m.CheckResults = []CheckResultManifest{{Role: "run"}} }},
		{name: "skipped result", version: 12, edit: func(m *Manifest) { m.CheckResults = []CheckResultManifest{{Status: "skipped"}} }},
		{name: "not-run result", version: 12, edit: func(m *Manifest) { m.CheckResults = []CheckResultManifest{{Status: "not_run"}} }},
	} {
		t.Run(test.name, func(t *testing.T) {
			manifest := Manifest{Format: backupFormat}
			if test.edit != nil {
				test.edit(&manifest)
			}
			version, err := backupManifestVersion(manifest, format10ManifestLimit, manifestLimit)
			if err != nil || version != test.version {
				t.Fatalf("version=%d err=%v", version, err)
			}

		})
	}
}

func TestWorkflowBackupFormats(t *testing.T) {
	for _, test := range []struct {
		name    string
		enabled *bool
		version int
	}{
		{name: "explicitly off", enabled: new(false), version: 10},
		{name: "new policy defaults on", version: 12},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			root := t.TempDir()
			store, manager := newBackupStore(t, root)
			policy, err := store.SetCheckPolicy(ctx, state.CheckPolicyInput{RepositoryID: "project", Executor: state.CheckExecutorExternalRunner, AllowedEvents: []string{"push"}, RunWorkflows: test.enabled, MaxTimeoutMS: 60000, MaxOutputLimitBytes: 65536, QueueLimit: 4, MaxActiveJobs: 1, MaxLeaseMS: 60000}, time.Unix(1800000000, 0))
			noErr(t, err)
			backup := filepath.Join(root, "backup")
			_, err = CreateWithReport(ctx, store, manager, backup)
			noErr(t, err)
			manifest, err := readManifest(filepath.Join(backup, manifestName))
			noErr(t, err)
			if manifest.Version != test.version || manifest.CheckPolicies[0].RunWorkflows != policy.RunWorkflows {
				t.Fatalf("manifest=%+v", manifest.CheckPolicies)
			}
			restoredState := canonicalTestTarget(t, filepath.Join(root, "restored-state"))
			_, err = RestoreWithReport(ctx, backup, restoredState, canonicalTestTarget(t, filepath.Join(root, "restored-repositories")), "")
			noErr(t, err)
			restored, err := state.Open(ctx, restoredState)
			noErr(t, err)
			defer restored.Close()
			got, exists, err := restored.CheckPolicy(ctx, "project")
			if err != nil || !exists || got.RunWorkflows != policy.RunWorkflows || got.Digest != policy.Digest || got.ConsentActive {
				t.Fatalf("restored policy=%+v exists=%v err=%v", got, exists, err)
			}
		})
	}
	t.Run("migrated off policy keeps format 10", func(t *testing.T) {
		directory := filepath.Join(t.TempDir(), "released-state")
		testfixture.LoadReleasedState(t, directory, "schema16-1.1.6-populated.sql")
		store, err := state.Open(context.Background(), directory)
		noErr(t, err)
		defer store.Close()
		snapshot, err := store.RecoverySnapshot(context.Background())
		noErr(t, err)
		manifest := Manifest{Format: backupFormat}
		addCheckState(&manifest, snapshot)
		for _, policy := range manifest.CheckPolicies {
			if policy.RunWorkflows {
				t.Fatal("migration enabled workflows")
			}
		}
		version, err := backupManifestVersion(manifest, format10ManifestLimit, manifestLimit)
		noErr(t, err)
		manifest.Version = version
		var content bytes.Buffer
		noErr(t, writeManifest(&content, manifest))
		if version != 10 || bytes.Contains(content.Bytes(), []byte(`"run_workflows"`)) {
			t.Fatalf("migrated policy format=%d", version)
		}
	})
}

func TestFormat12RoundTrip(t *testing.T) {
	for _, test := range []struct {
		name, event, status, role, want string
		waiting                         bool
	}{
		{name: "tolerated failure without a plan", event: state.ActionsEventDispatch, status: "failed", role: "tolerated", want: "passed"},
		{name: "builtin-only attempt", event: "push", status: "not_run", role: "builtin", want: "skipped"},
		{name: "waiting schedule job", event: state.ActionsEventSchedule, waiting: true, want: "interrupted"},
		{name: "pull request event and same-second revisions", event: "pull_request", status: "passed", role: "run", want: "passed"},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			root := t.TempDir()
			store, manager := newBackupStore(t, root)
			now := time.Unix(1800000000, 0)
			_, err := store.SaveCheckPolicyAndGrantConsent(ctx, state.CheckPolicyInput{RepositoryID: "project", Executor: state.CheckExecutorExternalRunner, AllowedEvents: []string{"push", "pull_request", state.ActionsEventDispatch, state.ActionsEventSchedule}, MaxTimeoutMS: 60000, MaxOutputLimitBytes: 65536, QueueLimit: 4, MaxActiveJobs: 1, MaxLeaseMS: 60000}, nil, now)
			noErr(t, err)
			original, originalDigest := testfixture.ActionsPlan(t, "test", 0)
			plan, err := actions.DecodePlan(original, originalDigest)
			noErr(t, err)
			plan.ContinueOnError = "true"
			plan.Concurrency = actions.Concurrency{Group: "synthetic-group"}
			plan.Context.Strategy.MaxParallel = 2
			if test.waiting {
				plan.Needs = []string{"build"}
			}
			encoded, digest, err := actions.EncodePlan(plan)
			noErr(t, err)
			request := state.ActionsRunRequest{Run: state.ActionsRun{RepositoryID: "project", WorkflowPath: ".github/workflows/ci.yml", Event: test.event, EventKey: "synthetic-event", SourceOID: strings.Repeat("a", 40), TriggerRef: "main", InputsJSON: "\n" + `{"dry_run":true,"target":"synthetic","count":1.5}` + " \n", Facts: actions.RunFacts{WorkflowDigest: strings.Repeat("b", 64), SecretNames: []string{"SYNTHETIC_TOKEN"}}, Actor: state.Actor{Kind: state.ActorAccess}}, Jobs: []state.ActionsJobRequest{{CheckJobRequest: state.CheckJobRequest{JobKey: "test", PlanDigest: digest, WorkflowDigest: strings.Repeat("b", 64), Waiting: test.waiting, Tolerated: true, ConcurrencyGroup: "synthetic-group", MaxParallel: 2, Checks: []state.CheckDefinition{{Name: "test", Command: "echo synthetic"}}}, Plan: encoded}}}
			if test.waiting {
				build, buildDigest := testfixture.ActionsPlan(t, "build", 0)
				request.Jobs = append(request.Jobs, state.ActionsJobRequest{CheckJobRequest: state.CheckJobRequest{JobKey: "build", PlanDigest: buildDigest, WorkflowDigest: strings.Repeat("b", 64), Checks: []state.CheckDefinition{{Name: "test", Command: "echo synthetic"}}}, Plan: build})
			}
			if test.event == state.ActionsEventSchedule {
				request.Run.ScheduledFor = &now
			}
			var updatedOID string
			if test.event == "pull_request" {
				work := filepath.Join(root, "backup-work")
				request.Run.BaseOID = gitOutput(t, work, "rev-parse", "HEAD")
				runGit(t, work, "commit", "--allow-empty", "-m", "Synthetic source one")
				first := gitOutput(t, work, "rev-parse", "HEAD")
				runGit(t, work, "commit", "--allow-empty", "-m", "Synthetic source two")
				second := gitOutput(t, work, "rev-parse", "HEAD")
				if first < second {
					first, second = second, first
				}
				remote, err := manager.Path("project")
				noErr(t, err)
				runGit(t, work, "push", remote, first+":refs/heads/feature")
				runGit(t, work, "push", "--force", remote, second+":refs/heads/feature")
				request.Run.SourceOID, updatedOID = first, second
				pr, err := store.CreatePullRequest(ctx, "project", "Synthetic change", "feature", "main", request.Run.SourceOID, request.Run.BaseOID, state.ReviewNotRequested, now)
				noErr(t, err)
				noErr(t, store.RecordPullRequestRevision(ctx, state.PullRequestRevision{RepositoryID: "project", PullRequestNumber: pr.Number, SourceOID: updatedOID, TargetOID: request.Run.BaseOID, RecordedAt: now.Add(10 * time.Millisecond)}))
				request.Run.PullRequestNumber = pr.Number
				request.Run.Facts.PullRequestAction, request.Run.Facts.PullRequestHeadRef = "opened", "feature"
				noErr(t, store.RecordAcceptedActionsPushes(ctx, "project", []state.AcceptedActionsPush{{Ref: "refs/heads/feature", NewOID: request.Run.SourceOID}}, now))
			}
			run, _, err := store.AdmitActionsRun(ctx, request, now)
			noErr(t, err)
			jobs, err := store.ActionsRunJobs(ctx, "project", run.ID)
			noErr(t, err)
			var job state.CheckJob
			for _, candidate := range jobs {
				if candidate.JobKey == "test" {
					job = candidate
				}
			}
			if job.ID == "" || test.waiting && job.Status != state.CheckJobWaiting {
				t.Fatalf("target job=%+v", job)
			}
			if !test.waiting {
				runner, _, _, err := store.IssueCheckRunnerToken(ctx, "project", "synthetic-runner", "", now)
				noErr(t, err)
				claimed, found, err := store.ClaimCheckJob(ctx, "project", runner.ID, now, state.RunnerFeatureWorkflowsV1)
				if err != nil || !found {
					t.Fatalf("claim found=%v err=%v", found, err)
				}
				_, attempt, err := store.StartCheckJob(ctx, state.CheckJobStart{RepositoryID: "project", JobID: job.ID, LeaseID: claimed.LeaseID, CredentialID: runner.ID, CredentialGeneration: runner.Generation, AttemptID: strings.Repeat("1", 32), Protection: state.ProtectionRunnerReported}, now)
				noErr(t, err)
				_, _, err = store.CompleteCheckJobAttempt(ctx, state.CheckCompletion{AttemptID: attempt.ID, RepositoryID: "project", TaskID: job.TaskID, WorktreeState: state.WorktreeClean, FinishedAt: now.Add(time.Second), Results: []state.CheckResult{{Name: "test", Command: "echo synthetic", Status: test.status, Role: test.role}}, Log: "synthetic masked log"}, state.CheckJobCompletionAuthority{JobID: job.ID, LeaseID: claimed.LeaseID, CredentialID: runner.ID, CredentialGeneration: runner.Generation}, now.Add(time.Second))
				noErr(t, err)
				if _, found, err := store.ActionsJobPlan(ctx, "project", job.ID); err != nil || found {
					t.Fatalf("terminal plan found=%v err=%v", found, err)
				}
			}
			noErr(t, store.Exec(ctx, `INSERT INTO actions_schedules(repository_id,workflow_path,cron,source_oid,next_due_at,updated_at,last_run_id) VALUES('project','.github/workflows/ci.yml','*/5 * * * *',?,?,?,?)`, run.SourceOID, now.UnixNano(), now.UnixNano(), run.ID))
			secretDir := filepath.Join(store.Dir(), "workflow-secrets")
			noErr(t, os.Mkdir(secretDir, 0o700))
			noErr(t, os.WriteFile(filepath.Join(secretDir, "project.json"), []byte(`{"SYNTHETIC_TOKEN":"synthetic-secret-not-for-backup"}`), 0o600))
			backup := filepath.Join(root, "backup")
			_, err = CreateWithReport(ctx, store, manager, backup)
			noErr(t, err)
			manifest, err := readManifest(filepath.Join(backup, manifestName))
			noErr(t, err)
			var manifestJob CheckJobManifest
			for _, candidate := range manifest.CheckJobs {
				if candidate.ID == job.ID {
					manifestJob = candidate
				}
			}
			if manifest.Version != 12 || len(manifest.ActionsRuns) != 1 || manifestJob.PlanDigest != digest || !manifestJob.Tolerated {
				t.Fatalf("manifest version=%d runs=%d jobs=%+v", manifest.Version, len(manifest.ActionsRuns), manifest.CheckJobs)
			}
			if test.event == "pull_request" {
				for _, field := range []string{"action", "head"} {
					invalid := manifest
					invalid.ActionsRuns = append([]state.ActionsRun(nil), manifest.ActionsRuns...)
					if field == "action" {
						invalid.ActionsRuns[0].Facts.PullRequestAction = ""
					} else {
						invalid.ActionsRuns[0].Facts.PullRequestHeadRef = ""
					}
					if err := validateManifest(invalid); err == nil {
						t.Fatalf("backup without pull request %s was accepted", field)
					}
				}
			}
			for _, version := range []int{10, 11} {
				older := manifest
				older.Version = version
				if err := validateManifest(older); err == nil || !strings.Contains(err.Error(), "only version 12 holds") {
					t.Fatalf("downgraded manifest=%v", err)
				}
			}
			content, err := os.ReadFile(filepath.Join(backup, manifestName))
			noErr(t, err)
			for _, omitted := range []string{"synthetic-secret-not-for-backup", "actions_job_plans", "actions_schedules", "actions_accepted_pushes", `"plan_json"`} {
				if strings.Contains(string(content), omitted) {
					t.Fatalf("backup contains %s", omitted)
				}
			}
			restoredState := canonicalTestTarget(t, filepath.Join(root, "restored-state"))
			restoredRepos := canonicalTestTarget(t, filepath.Join(root, "restored-repositories"))
			_, err = RestoreWithReport(ctx, backup, restoredState, restoredRepos, "")
			noErr(t, err)
			restored, err := state.Open(ctx, restoredState)
			noErr(t, err)
			defer restored.Close()
			got, found, err := restored.ActionsRun(ctx, "project", run.ID)
			if err != nil || !found || !reflect.DeepEqual(got, run) {
				t.Fatalf("restored run=%+v found=%v err=%v", got, found, err)
			}
			gotJob, found, err := restored.CheckJob(ctx, "project", job.ID)
			if err != nil || !found || gotJob.Status != test.want || gotJob.PlanDigest != digest || gotJob.ConcurrencyGroup != job.ConcurrencyGroup || gotJob.MaxParallel != job.MaxParallel {
				t.Fatalf("restored job=%+v found=%v err=%v", gotJob, found, err)
			}
			if test.event == "pull_request" {
				opened, err := restored.ActionsPullRequestAction(ctx, "project", run.PullRequestNumber, run.SourceOID, run.BaseOID)
				noErr(t, err)
				updated, err := restored.ActionsPullRequestAction(ctx, "project", run.PullRequestNumber, updatedOID, run.BaseOID)
				noErr(t, err)
				if opened != "opened" || updated != "synchronize" {
					t.Fatalf("restored revision order: initial=%s updated=%s", opened, updated)
				}
				note, err := restored.ActionsPullRequestAdmissionNote(ctx, "project", run.SourceOID)
				if err != nil || note == nil || note.Code != "note.push_required" {
					t.Fatalf("restored push authority note=%+v err=%v", note, err)
				}
			}
			for _, table := range []string{"actions_job_plans", "actions_schedules", "actions_accepted_pushes", "check_job_runtime_ownership", "check_runner_credentials"} {
				count, err := restored.TableRowCount(ctx, table)
				if err != nil || count != 0 {
					t.Fatalf("restored %s rows=%d err=%v", table, count, err)
				}
			}
			if _, err := os.Stat(filepath.Join(restored.Dir(), "workflow-secrets", "project.json")); !os.IsNotExist(err) {
				t.Fatalf("secrets restored: %v", err)
			}
			if !test.waiting {
				attempt, found, err := restored.CheckAttemptByID(ctx, "project", manifest.CheckAttempts[0].ID)
				if err != nil || !found || attempt.Status != test.want || attempt.Results[0].Role != test.role || attempt.CompletionDigest != manifest.CheckAttempts[0].CompletionDigest {
					t.Fatalf("portable attempt=%+v err=%v", attempt, err)
				}
			}
			policy, _, err := restored.CheckPolicy(ctx, "project")
			if err != nil || !policy.RunWorkflows || policy.ConsentActive {
				t.Fatalf("restored policy=%+v err=%v", policy, err)
			}
			rebackup := filepath.Join(root, "rebackup")
			restoredManager := &repository.Manager{Store: restored, Root: restoredRepos, Git: restoredRunner(t, restoredState), Locks: gitexec.NewLocks()}
			_, err = CreateWithReport(ctx, restored, restoredManager, rebackup)
			noErr(t, err)
			again, err := readManifest(filepath.Join(rebackup, manifestName))
			noErr(t, err)
			if again.Version != 12 || !reflect.DeepEqual(again.ActionsRuns, manifest.ActionsRuns) {
				t.Fatalf("rebackup version=%d runs=%+v", again.Version, again.ActionsRuns)
			}
		})
	}
}
