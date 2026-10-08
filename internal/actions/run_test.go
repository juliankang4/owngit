package actions

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

const testSecret = "synthetic-secret-value"

func tinyEvaluator(key, text string, contexts map[string]any) (any, error) {
	switch text {
	case "success()", "failure()", "cancelled()":
		return contexts[strings.TrimSuffix(text, "()")], nil
	case "always()":
		return true, nil
	case "github.ref == 'refs/heads/main'":
		return contexts["github"].(map[string]any)["ref"] == "refs/heads/main", nil
	case "${{ secrets.TEST }}":
		return contexts["secrets"].(map[string]any)["TEST"], nil
	case "${{ env.VALUE }}":
		return contexts["env"].(map[string]string)["VALUE"], nil
	case "${{ matrix.version }}":
		return contexts["matrix"].(map[string]any)["version"], nil
	case "${{ github.run_id }}":
		return contexts["github"].(map[string]any)["run_id"], nil
	case "${{ steps.first.outputs.token }}":
		return contexts["steps"].(map[string]any)["first"].(map[string]any)["outputs"].(map[string]string)["token"], nil
	case "EXPRESSION_ERROR":
		return nil, fmt.Errorf("cannot convert %s", testSecret)
	}
	if key == "step.if" {
		switch text {
		case "true":
			return true, nil
		case "false":
			return false, nil
		}
		return nil, fmt.Errorf("unknown test condition")
	}
	if strings.Contains(text, "${{") {
		return nil, fmt.Errorf("unknown test template")
	}
	return text, nil
}

func jobFixture(t *testing.T) (JobPlan, RunOptions) {
	t.Helper()
	workspace := filepath.Join(t.TempDir(), "source")
	if err := os.Mkdir(workspace, 0o700); err != nil {
		t.Fatal(err)
	}
	plan := JobPlan{JobKey: "build", SecretNames: []string{"TEST"}, Context: PlanContext{
		GitHub: GitHubContext{SHA: "synthetic-commit", Ref: "refs/heads/main", RefName: "main", RefType: "branch", EventName: "push", Repository: "sample", Workflow: "CI", RunID: "stale-plan-id", RunNumber: 999, RunAttempt: 999},
		Matrix: map[string]any{"version": "1.27"},
	}}
	options := RunOptions{Workspace: workspace, Identity: RunIdentity{ID: "run-17", Number: 17, Attempt: 2}, Secrets: map[string]string{"TEST": testSecret},
		Evaluator: tinyEvaluator, BaseEnvironment: []string{}, MaxTimeout: time.Second, OutputLimit: 64 << 10}
	return plan, options
}

func TestRunJob(t *testing.T) {
	tests := []struct {
		name        string
		steps       []Step
		statuses    []string
		want        []string
		jobStatus   string
		cancelStart bool
		cancelStep  bool
		configure   func(*JobPlan, *RunOptions)
		check       func(*testing.T, JobResult, []Script)
	}{
		{name: "cancel before start", steps: []Step{{Run: "one"}, {Run: "two", If: "always()"}}, want: []string{StatusSkipped, StatusSkipped}, jobStatus: StatusCancelled, cancelStart: true},
		{name: "cancel during step", steps: []Step{{Run: "one"}, {Run: "two", If: "always()"}}, want: []string{StatusCancelled, StatusSkipped}, jobStatus: StatusCancelled, cancelStep: true},
		{name: "missing identity", jobStatus: StatusError, configure: func(_ *JobPlan, options *RunOptions) { options.Identity = RunIdentity{} }},
		{name: "invalid job timeout", jobStatus: StatusError, configure: func(plan *JobPlan, _ *RunOptions) { plan.TimeoutMinutes = "0" }},
		{name: "missing workspace", jobStatus: StatusError, configure: func(_ *JobPlan, options *RunOptions) { options.Workspace = filepath.Join(options.Workspace, "missing") }},
		{name: "command file failure", steps: []Step{{Run: "one"}, {Run: "two"}}, want: []string{StatusError, StatusSkipped}, jobStatus: StatusError,
			configure: func(_ *JobPlan, options *RunOptions) {
				options.RunScript = func(_ context.Context, script Script) ScriptResult {
					path := environmentMap(script.Environment, false)["GITHUB_OUTPUT"]
					if err := os.WriteFile(path, []byte(strings.Repeat("x", maxCommandFileBytes+1)), 0o600); err != nil {
						return ScriptResult{Status: StatusError, Output: err.Error()}
					}
					return ScriptResult{Status: StatusPassed}
				}
			}},
		{name: "ordered", steps: []Step{{Run: "one"}, {Run: "two"}}, want: []string{StatusPassed, StatusPassed}, jobStatus: StatusPassed},
		{name: "implicit success after failure", steps: []Step{{Run: "fail"}, {Run: "deploy", If: "github.ref == 'refs/heads/main'"}, {Run: "report", If: "failure()"}, {Run: "always", If: "always()"}}, statuses: []string{StatusFailed, StatusPassed, StatusPassed}, want: []string{StatusFailed, StatusSkipped, StatusPassed, StatusPassed}, jobStatus: StatusFailed},
		{name: "tolerated", steps: []Step{{ID: "first", Run: "fail", ContinueOnError: "true"}, {Run: "next"}}, statuses: []string{StatusFailed, StatusPassed}, want: []string{StatusFailed, StatusPassed}, jobStatus: StatusPassed,
			check: func(t *testing.T, job JobResult, _ []Script) {
				if job.Steps[0].Role != RoleTolerated || StepConclusion(job.Steps[0].Evidence()) != ResultSuccess {
					t.Fatal(job)
				}
			}},
		{name: "error blocks ordinary step", steps: []Step{{Run: "one"}, {Run: "two"}}, statuses: []string{StatusError}, want: []string{StatusError, StatusSkipped}, jobStatus: StatusError},
		{name: "incomplete blocks ordinary step", steps: []Step{{Run: "one"}, {Run: "two"}}, statuses: []string{StatusIncomplete}, want: []string{StatusIncomplete, StatusSkipped}, jobStatus: StatusIncomplete},
		{name: "unavailable blocks ordinary step", steps: []Step{{Run: "one"}, {Run: "two"}}, statuses: []string{StatusUnavailable}, want: []string{StatusUnavailable, StatusSkipped}, jobStatus: StatusUnavailable},
		{name: "tolerated unavailable", steps: []Step{{Run: "one", ContinueOnError: "true"}, {Run: "two"}}, statuses: []string{StatusUnavailable, StatusPassed}, want: []string{StatusUnavailable, StatusPassed}, jobStatus: StatusPassed},
		{name: "nothing ran", steps: []Step{{Run: "one", If: "false"}}, want: []string{StatusSkipped}, jobStatus: StatusSkipped},
		{name: "builtins only", steps: []Step{{Uses: "actions/checkout@v4"}, {Uses: "actions/setup-go@v5", With: map[string]string{"go-version": "${{ matrix.version }}", "cache": "${{ ignored }}"}}, {ID: "cache", Uses: "actions/cache@v4"}, {Uses: "actions/cache/restore@main"}, {Uses: "actions/cache/save@main"}, {Uses: "actions/setup-node@v4"}, {Uses: "actions/setup-python@v5"}, {Uses: "actions/setup-java@v4"}, {Uses: "actions/upload-artifact@v4"}},
			want: []string{StatusPassed, StatusNotRun, StatusNotRun, StatusNotRun, StatusNotRun, StatusNotRun, StatusNotRun, StatusNotRun, StatusNotRun}, jobStatus: StatusSkipped,
			check: func(t *testing.T, job JobResult, scripts []Script) {
				if len(scripts) != 0 || job.Steps[0].Role != RoleBuiltin || job.Steps[2].Outputs["cache-hit"] != "false" || !strings.Contains(job.Steps[1].Notes[0].Detail, "go-version=1.27") {
					t.Fatal(job)
				}
			}},
		{name: "unsupported action is an error", steps: []Step{{Uses: "actions/download-artifact@v4"}}, want: []string{StatusError}, jobStatus: StatusError},
		{name: "masked runtime name", steps: []Step{{Name: "${{ secrets.TEST }}", Run: "one"}}, want: []string{StatusPassed}, jobStatus: StatusPassed,
			check: func(t *testing.T, job JobResult, _ []Script) {
				if job.Steps[0].Name != "${{ secrets.TEST }}" || job.Steps[0].DisplayName != "[redacted]" || job.Steps[0].Command != "one" {
					t.Fatal(job)
				}
			}},
		{name: "runtime mask covers metadata", steps: []Step{{ID: "future-mask", Name: "future-mask", Run: "future-mask"}}, want: []string{StatusFailed}, jobStatus: StatusFailed,
			configure: func(_ *JobPlan, options *RunOptions) {
				options.RunScript = func(_ context.Context, script Script) ScriptResult {
					fmt.Fprintln(script.Output, "::add-mask::future-mask")
					return ScriptResult{Status: StatusFailed, Output: "diagnostic future-mask"}
				}
			}, check: func(t *testing.T, job JobResult, _ []Script) {
				step := job.Steps[0]
				if step.Name != "[redacted]" || step.DisplayName != "[redacted]" || step.Command != "[redacted]" || step.ID != "[redacted]" {
					t.Fatal(job)
				}
			}},
		{name: "masked expression error", steps: []Step{{Run: "EXPRESSION_ERROR"}}, want: []string{StatusError}, jobStatus: StatusError},
		{name: "environment error", steps: []Step{{Run: "one", Env: map[string]string{"BAD-NAME": "x"}}}, want: []string{StatusError}, jobStatus: StatusError},
		{name: "invalid step tolerance", steps: []Step{{Run: "one", ContinueOnError: "not-a-boolean"}}, want: []string{StatusError}, jobStatus: StatusError},
		{name: "invalid step timeout", steps: []Step{{Run: "one", TimeoutMinutes: "NaN"}}, want: []string{StatusError}, jobStatus: StatusError},
		{name: "timeout cannot exceed policy", steps: []Step{{Run: "one", TimeoutMinutes: "20"}}, statuses: []string{StatusIncomplete}, want: []string{StatusIncomplete}, jobStatus: StatusIncomplete,
			check: func(t *testing.T, job JobResult, scripts []Script) {
				if scripts[0].Timeout != time.Second || !strings.Contains(job.Steps[0].Notes[0].Detail, "max_timeout_ms (1000 ms)") {
					t.Fatal(job, scripts)
				}
			}},
		{name: "step timeout is lower", steps: []Step{{Run: "one", TimeoutMinutes: "0.001"}}, statuses: []string{StatusIncomplete}, want: []string{StatusIncomplete}, jobStatus: StatusIncomplete,
			check: func(t *testing.T, job JobResult, scripts []Script) {
				if scripts[0].Timeout != 60*time.Millisecond || strings.Contains(job.Steps[0].Notes[0].Detail, "Raise it under") {
					t.Fatal(job, scripts)
				}
			}},
		{name: "job tolerance preserves raw failure", steps: []Step{{Run: "one"}}, statuses: []string{StatusFailed}, want: []string{StatusFailed}, jobStatus: StatusFailed,
			configure: func(plan *JobPlan, _ *RunOptions) { plan.ContinueOnError = "true" }, check: func(t *testing.T, job JobResult, _ []Script) {
				if !job.Tolerated {
					t.Fatal(job)
				}
			}},
		{name: "cleanup error stops always", steps: []Step{{Run: "one", ContinueOnError: "true"}, {Run: "two", If: "always()"}}, want: []string{StatusError, StatusSkipped}, jobStatus: StatusError,
			configure: func(_ *JobPlan, options *RunOptions) {
				options.RunScript = func(context.Context, Script) ScriptResult {
					return ScriptResult{Status: StatusPassed, CleanupError: testSecret + " cleanup"}
				}
			}},
		{name: "job timeout stops always", steps: []Step{{Run: "one"}, {Run: "two", If: "always()"}}, want: []string{StatusIncomplete, StatusSkipped}, jobStatus: StatusIncomplete,
			configure: func(plan *JobPlan, options *RunOptions) {
				plan.TimeoutMinutes = "0.002"
				options.RunScript = func(ctx context.Context, _ Script) ScriptResult {
					<-ctx.Done()
					return ScriptResult{Status: StatusCancelled}
				}
			}},
		{name: "mask limit stops always", steps: []Step{{Run: "one", ContinueOnError: "true"}, {Run: "two", If: "always()"}}, want: []string{StatusError, StatusSkipped}, jobStatus: StatusError,
			configure: func(_ *JobPlan, options *RunOptions) {
				options.RunScript = func(_ context.Context, script Script) ScriptResult {
					fmt.Fprintln(script.Output, "::add-mask::"+strings.Repeat("x", maxMaskBytes+1))
					fmt.Fprintln(script.Output, "WITHHELD")
					return ScriptResult{Status: StatusPassed}
				}
			}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			plan, options := jobFixture(t)
			plan.Steps = test.steps
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if test.cancelStart {
				cancel()
			}
			var scripts []Script
			options.RunScript = func(_ context.Context, script Script) ScriptResult {
				status := StatusPassed
				if len(scripts) < len(test.statuses) {
					status = test.statuses[len(scripts)]
				}
				scripts = append(scripts, script)
				if test.cancelStep {
					cancel()
					status = StatusCancelled
				}
				fmt.Fprintln(script.Output, testSecret)
				return ScriptResult{Status: status}
			}
			if test.configure != nil {
				test.configure(&plan, &options)
			}
			job := RunJob(ctx, plan, options)
			var statuses []string
			for _, result := range job.Steps {
				statuses = append(statuses, result.Status)
				if len(result.Excerpt) > 8<<10 {
					t.Fatal("oversized excerpt")
				}
			}
			if !reflect.DeepEqual(statuses, test.want) || job.Status != test.jobStatus {
				t.Fatalf("got %v, job=%+v", statuses, job)
			}
			encoded, err := json.Marshal(job)
			if err != nil || strings.Contains(string(encoded), testSecret) || strings.Contains(string(encoded), "WITHHELD") {
				t.Fatalf("unmasked result: %s, %v", encoded, err)
			}
			if test.check != nil {
				test.check(t, job, scripts)
			}
		})
	}
}
