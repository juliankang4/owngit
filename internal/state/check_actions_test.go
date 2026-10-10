package state

import (
	"strings"
	"testing"
	"time"

	"owngit/internal/actions"
)

func TestAggregateActionsAttemptStatus(t *testing.T) {
	for _, test := range []struct {
		name      string
		results   []CheckResult
		cancelled bool
		want      string
	}{
		{"empty", nil, false, actions.StatusSkipped},
		{"built-ins only", []CheckResult{{Status: AttemptPassed, Role: actions.RoleBuiltin}}, false, actions.StatusSkipped},
		{"tolerated failure", []CheckResult{{Status: AttemptFailed, Role: actions.RoleTolerated}}, false, AttemptPassed},
		{"error before cancellation", []CheckResult{{Status: AttemptError, Role: actions.RoleRun}, {Status: AttemptCancelled, Role: actions.RoleRun}}, true, AttemptError},
		{"cleanup not tolerated", []CheckResult{{Status: AttemptPassed, Role: actions.RoleTolerated, CleanupError: "not released"}}, true, AttemptError},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := AggregateActionsAttemptStatus(test.results, test.cancelled); got != test.want {
				t.Fatalf("status=%q, want %q", got, test.want)
			}
		})
	}
}

func TestAggregateAttemptStatus(t *testing.T) {
	for _, test := range []struct {
		name      string
		results   []CheckResult
		cancelled bool
		want      string
	}{
		{"empty", nil, false, AttemptUnavailable},
		{"empty cancelled", nil, true, AttemptCancelled},
		{"passed", []CheckResult{{Status: AttemptPassed}}, false, AttemptPassed},
		{"incomplete", []CheckResult{{Status: AttemptPassed}, {Status: AttemptIncomplete}}, false, AttemptIncomplete},
		{"unavailable", []CheckResult{{Status: AttemptIncomplete}, {Status: AttemptUnavailable}}, false, AttemptUnavailable},
		{"failed", []CheckResult{{Status: AttemptUnavailable}, {Status: AttemptFailed}}, false, AttemptFailed},
		{"error", []CheckResult{{Status: AttemptFailed}, {Status: AttemptError}}, false, AttemptError},
		{"cancelled result before ordinary error", []CheckResult{{Status: AttemptError}, {Status: AttemptCancelled}}, true, AttemptCancelled},
		{"ordinary error before cancel flag", []CheckResult{{Status: AttemptError}}, true, AttemptError},
		{"cleanup before cancellation", []CheckResult{{Status: AttemptPassed, CleanupError: "not released"}, {Status: AttemptCancelled}}, true, AttemptError},
		{"JSON ignores tolerance", []CheckResult{{Status: AttemptFailed, Role: actions.RoleTolerated}}, false, AttemptFailed},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := AggregateAttemptStatus(test.results, test.cancelled); got != test.want {
				t.Fatalf("status=%q, want %q", got, test.want)
			}
		})
	}
}

func TestAttemptOutcomeUsesOnlyVerifiedFacts(t *testing.T) {
	for _, test := range []struct {
		name               string
		results            []CheckResult
		worktree           string
		credential         string
		cancelled          bool
		wantCancelled      bool
		wantError          int
		wantCancelledCount int
		wantTolerated      int
		wantRefusal        string
		wantNothing        bool
	}{
		{"JSON failure", []CheckResult{{Status: AttemptFailed}}, WorktreeClean, "", false, false, 0, 0, 0, "", false},
		{"refusal", []CheckResult{{Status: AttemptUnavailable, OutputExcerpt: "source missing"}}, WorktreeClean, jsonAdmissionRefusalCredentialID, false, false, 0, 0, 0, "source missing", false},
		{"workflow tolerated", []CheckResult{{Status: AttemptFailed, Role: actions.RoleTolerated}}, WorktreeClean, "", false, false, 0, 0, 1, "", false},
		{"workflow skipped", []CheckResult{{Status: actions.StatusSkipped, Role: actions.RoleRun}}, WorktreeClean, "", false, false, 0, 0, 0, "", true},
		{"dirty", []CheckResult{{Status: AttemptPassed}}, WorktreeDirty, "", false, false, 0, 0, 0, "", false},
		{"cancelled", []CheckResult{{Status: AttemptCancelled}}, WorktreeUnknown, "", true, true, 0, 1, 0, "", false},
		{"submitted cancellation with cleanup error", []CheckResult{{Status: AttemptCancelled, CleanupError: "cleanup failed"}}, WorktreeClean, "", true, false, 1, 0, 0, "", false},
		{"cancelled result without submitted cancellation", []CheckResult{{Status: AttemptCancelled}}, WorktreeClean, "", false, true, 0, 1, 0, "", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			workflow := len(test.results) > 0 && test.results[0].Role != ""
			status, summary := checkAttemptOutcome(test.results, test.cancelled, test.worktree, workflow, test.credential)
			attempt := CheckAttempt{Status: status, Summary: summary, Results: test.results, SubmittedCancelled: test.cancelled,
				WorktreeState: test.worktree, CredentialID: test.credential, FinishedAt: time.Now()}
			outcome, ok := attempt.Outcome()
			if !ok || outcome.Total != len(test.results) || outcome.Workflow != workflow || outcome.Worktree != test.worktree ||
				outcome.Tolerated != test.wantTolerated || outcome.Refusal != test.wantRefusal || outcome.NothingRan != test.wantNothing ||
				outcome.Cancelled != test.wantCancelled || outcome.Error != test.wantError || outcome.CancelledCount != test.wantCancelledCount {
				t.Fatalf("outcome=%+v ok=%v summary=%q", outcome, ok, summary)
			}
			attempt.Summary = "custom summary"
			if _, ok := attempt.Outcome(); ok {
				t.Fatal("custom text was claimed as typed")
			}
			attempt.Summary, attempt.Status = summary, AttemptPending
			if _, ok := attempt.Outcome(); ok {
				t.Fatal("pending attempt has an outcome")
			}
		})
	}
}

func TestResultLimitDigestKeepsAbsentFieldBytes(t *testing.T) {
	attempt := CheckAttempt{ID: "attempt", SubmittedWorktreeState: WorktreeClean, FinishedAt: time.Unix(0, 7), SubmittedLogDigest: "log"}
	result := CheckResult{Name: "unit", Command: "print", Status: AttemptIncomplete, DurationMS: 10, OutputExcerpt: "user", Truncated: true}
	old := digestFields("attempt", WorktreeClean, "false", "7", "false", "log", "unit", "print", AttemptIncomplete, "nil", "10", "user", "true", "")
	if got := completionDigest(attempt, []CheckResult{result}); got != old {
		t.Fatalf("historical digest changed: %s != %s", got, old)
	}
	result.OutputLimitExceededBytes = 4096
	if got := completionDigest(attempt, []CheckResult{result}); got == old || got != digestFields("attempt", WorktreeClean, "false", "7", "false", "log", "unit", "print", AttemptIncomplete, "nil", "10", "user", "true", "", "output_limit", "4096") {
		t.Fatalf("tagged digest=%s", got)
	}
}

func TestCheckJobDedupDigest(t *testing.T) {
	const (
		v1 = "e6b08c7e8a23f5cf7319754c4d0c1bbbbb727c75de2b3e920ff349838eebbcb1"
		v2 = "3f1d6ce3d1952c7cbec92a6b6c6f0bbe52e7c44dce3c1f49c589d8ae49450060"
		v3 = "29fbbf432f67c400690a68e2859b7328b93081cdbc09f7646ee7bc24de84bdea"
	)
	for _, test := range []struct {
		name    string
		version string
		mutate  func(*CheckJob)
		config  string
		want    string
		invalid bool
	}{
		{"JSON v1 unchanged", "v1", nil, "", v1, false},
		{"JSON v2 unchanged", "v2", nil, "", v2, false},
		{"Actions v3", "v3", nil, "", v3, false},
		{"run identity excluded", "v3", func(j *CheckJob) { j.RunID = "another-run" }, "", v3, false},
		{"row identity excluded", "v3", func(j *CheckJob) { j.ID = "another-row" }, "", v3, false},
		{"JSON v1 ignores Actions metadata", "v1", func(j *CheckJob) { j.JobKey, j.MatrixIndex, j.PlanDigest = "unit", 2, "plan" }, "", v1, false},
		{"JSON v2 ignores Actions metadata", "v2", func(j *CheckJob) { j.JobKey, j.MatrixIndex, j.PlanDigest = "unit", 2, "plan" }, "", v2, false},
		{"repository", "v3", func(j *CheckJob) { j.RepositoryID = "another" }, "", "", false},
		{"task", "v3", func(j *CheckJob) { j.TaskID = "another" }, "", "", false},
		{"trigger", "v3", func(j *CheckJob) { j.Trigger = "workflow_dispatch" }, "", "", false},
		{"event", "v3", func(j *CheckJob) { j.EventKey = "another" }, "", "", false},
		{"source", "v3", func(j *CheckJob) { j.SourceOID = "another" }, "", "", false},
		{"base", "v3", func(j *CheckJob) { j.BaseOID = "another" }, "", "", false},
		{"pull request", "v3", func(j *CheckJob) { j.PullRequestNumber++ }, "", "", false},
		{"ref", "v3", func(j *CheckJob) { j.TriggerRef = "another" }, "", "", false},
		{"workflow path", "v3", func(j *CheckJob) { j.WorkflowPath = ".github/workflows/other.yml" }, "", "", false},
		{"workflow blob", "v3", func(j *CheckJob) { j.WorkflowOID = "another" }, "", "", false},
		{"workflow digest", "v3", func(j *CheckJob) { j.WorkflowDigest = "another" }, "", "", false},
		{"configuration", "v3", nil, "another", "", false},
		{"executor", "v3", func(j *CheckJob) { j.Executor = CheckExecutorExternalRunner }, "", "", false},
		{"policy", "v3", func(j *CheckJob) { j.PolicyVersion++ }, "", "", false},
		{"consent", "v3", func(j *CheckJob) { j.ConsentVersion++ }, "", "", false},
		{"timeout", "v3", func(j *CheckJob) { j.Limits.TimeoutMS++ }, "", "", false},
		{"output limit", "v3", func(j *CheckJob) { j.Limits.OutputLimitBytes++ }, "", "", false},
		{"rerun root", "v3", func(j *CheckJob) { j.RerunRoot = "another" }, "", "", false},
		{"rerun generation", "v3", func(j *CheckJob) { j.RerunGeneration++ }, "", "", false},
		{"execution settings", "v3", func(j *CheckJob) { j.Execution.Source.MaxEntries++ }, "", "", false},
		{"legacy does not select v1 for Actions", "v3", func(j *CheckJob) { j.Execution.Legacy = true }, "", "", false},
		{"job key", "v3", func(j *CheckJob) { j.JobKey = "another" }, "", "", false},
		{"matrix index", "v3", func(j *CheckJob) { j.MatrixIndex++ }, "", "", false},
		{"plan digest", "v3", func(j *CheckJob) { j.PlanDigest = "another" }, "", "", false},
		{"oversized execution settings", "v3", func(j *CheckJob) { j.Execution.ContainerNetwork = strings.Repeat("x", 4096) }, "", "", true},
	} {
		t.Run(test.name, func(t *testing.T) {
			job := CheckJob{
				RepositoryID: "repo", TaskID: "task", Trigger: "push", EventKey: "event",
				SourceOID: "source", BaseOID: "base", PullRequestNumber: 2, TriggerRef: "main",
				WorkflowPath: ".owngit/checks.json", WorkflowOID: "blob", WorkflowDigest: "workflow",
				Executor: CheckExecutorHost, PolicyVersion: 3, ConsentVersion: 3,
				Limits: CheckJobLimits{TimeoutMS: 1000, OutputLimitBytes: 4096}, RerunRoot: "root", RerunGeneration: 1,
			}
			baseline := v2
			if test.version == "v1" {
				job.Execution.Legacy, baseline = true, v1
			} else if test.version == "v3" {
				job.RunID, job.JobKey, job.PlanDigest, baseline = "run", "unit", "plan", v3
				job.WorkflowPath = ".github/workflows/ci.yml"
			}
			if test.mutate != nil {
				test.mutate(&job)
			}
			config := test.config
			if config == "" {
				config = "config"
			}
			got := checkJobDedupDigest(job, config)
			switch {
			case test.invalid:
				if got != "" {
					t.Fatalf("invalid settings produced digest %q", got)
				}
			case test.want != "":
				if got != test.want {
					t.Fatalf("digest=%q, want %q", got, test.want)
				}
			default:
				if len(got) != 64 || got == baseline {
					t.Fatalf("changed execution facts produced digest %q (baseline %q)", got, baseline)
				}
			}
		})
	}
}
