package actions

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"math"
	"reflect"
	"strings"
	"testing"
)

func admissionContext() PlanContext {
	return PlanContext{GitHub: GitHubContext{SHA: strings.Repeat("a", 40), Ref: "refs/heads/main", RefName: "main", RefType: "branch", Repository: "example", EventName: "push", Actor: "owngit", TriggeringActor: "owngit", RunID: "synthetic-run", RunNumber: 1, RunAttempt: 1}}
}

func cloneTestJSON(v any) ([]byte, error) { return json.Marshal(v) }

func TestPlan(t *testing.T) {
	cases := []struct {
		name, source, fixture, code, refusal string
		jobs                                 int
		check                                func(*testing.T, PlannedWorkflow)
	}{
		{name: "single job", source: minimalWorkflow, jobs: 1},
		{name: "extra schedule does not refuse push", source: strings.Replace(minimalWorkflow, "on: push", "on:\n  push:\n  schedule:\n"+strings.Repeat("    - cron: '0 0 * * *'\n", 11), 1), jobs: 1, check: func(t *testing.T, w PlannedWorkflow) {
			if len(w.Facts.Notes) != 1 || w.Facts.Notes[0].Code != "workflow.limit" || len(w.Facts.RefusedJobs) != 0 {
				t.Fatal("extra schedule refusal must stay visible without refusing jobs")
			}
		}},
		{name: "matrix needs and builtins", fixture: "ci.yml", jobs: 3, check: func(t *testing.T, w PlannedWorkflow) {
			if w.RunName != "Check main" || w.Concurrency.Group != "ci-refs/heads/main" || !w.Concurrency.CancelInProgress || w.Jobs[2].Status != StatusWaiting {
				t.Fatalf("admission facts missing: %+v", w)
			}
			if w.Jobs[0].Tolerated || !w.Jobs[1].Tolerated || w.Jobs[0].Plan.Context.Strategy.MaxParallel != 2 || w.Jobs[0].Plan.Context.Strategy.FailFast {
				t.Fatal("matrix strategy or tolerance not resolved")
			}
			if len(w.Jobs[0].Notes) != 2 || !strings.Contains(w.Jobs[0].Notes[1].Detail, "1.20") || len(w.Jobs[0].Plan.SecretNames) != 1 || w.Jobs[0].Plan.SecretNames[0] != "TEST_TOKEN" {
				t.Fatal("builtin notes or named secrets lost")
			}
		}},
		{name: "refusal only its job", fixture: "refused.yml", jobs: 1, refusal: "workflow.environment"},
		{name: "unknown action", source: strings.Replace(minimalWorkflow, "run: echo ok", "uses: vendor/unknown@v1", 1), refusal: "workflow.action"},
		{name: "local action", source: strings.Replace(minimalWorkflow, "run: echo ok", "uses: ./local", 1), refusal: "workflow.local_action"},
		{name: "docker action", source: strings.Replace(minimalWorkflow, "run: echo ok", "uses: docker://example/tool", 1), refusal: "workflow.docker_action"},
		{name: "artifact download", source: strings.Replace(minimalWorkflow, "run: echo ok", "uses: actions/download-artifact@v4", 1), refusal: "workflow.artifact_download"},
		{name: "checkout another repository", source: strings.Replace(minimalWorkflow, "run: echo ok", "uses: actions/checkout@v6\n        with: {repository: other}", 1), refusal: "workflow.checkout_input"},
		{name: "checkout submodules", source: strings.Replace(minimalWorkflow, "run: echo ok", "uses: actions/checkout@any\n        with: {submodules: true}", 1), refusal: "workflow.checkout_input"},
		{name: "checkout unknown input", source: strings.Replace(minimalWorkflow, "run: echo ok", "uses: actions/checkout@abc\n        with: {typo: value}", 1), refusal: "workflow.unknown_key"},
		{name: "checkout inert token", source: strings.Replace(minimalWorkflow, "run: echo ok", "uses: actions/checkout@main\n        with: {token: '${{ secrets.GITHUB_TOKEN }}', fetch-depth: 0}", 1), jobs: 1},
		{name: "setup token inert version evaluated", source: strings.Replace(minimalWorkflow, "run: echo ok", "uses: actions/setup-node@v6\n        with: {node-version: '${{ inputs.version }}', token: '${{ github.token }}'}", 1), jobs: 1},
		{name: "cache hashFiles not evaluated", source: strings.Replace(minimalWorkflow, "run: echo ok", "uses: actions/cache@v4\n        with: {key: \"${{ hashFiles('**/go.sum') }}\", path: cache}", 1), jobs: 1},
		{name: "hashFiles in run refused", source: strings.Replace(minimalWorkflow, "echo ok", "echo ${{ hashFiles('x') }}", 1), refusal: "workflow.function"},
		{name: "unsupported github token", source: strings.Replace(minimalWorkflow, "echo ok", "echo ${{ github.token }}", 1), refusal: "workflow.token"},
		{name: "unsupported guard alias", source: strings.Replace(minimalWorkflow, "runs-on: linux", "runs-on: linux\n    if: (github || inputs).token == ''", 1), refusal: "workflow.token"},
		{name: "if cannot access matrix", source: strings.Replace(minimalWorkflow, "runs-on: linux", "runs-on: linux\n    if: matrix.experimental", 1), refusal: "workflow.context"},
		{name: "step env names only", source: strings.Replace(minimalWorkflow, "run: echo ok", "run: echo ok\n        env: {TOKEN: '${{ secrets.token }}'}", 1), jobs: 1, check: func(t *testing.T, w PlannedWorkflow) {
			if !reflect.DeepEqual(w.Jobs[0].Plan.SecretNames, []string{"TOKEN"}) || !strings.Contains(string(w.Jobs[0].Encoded), "secrets.token") {
				t.Fatal("plan must contain raw expression and name")
			}
		}},
		{name: "file env context refused", source: "env: {BAD: '${{ matrix.value }}'}\n" + minimalWorkflow, code: "workflow.context"},
		{name: "workflow secrets reach each job", source: "env: {TOKEN: '${{ secrets.SHARED }}'}\n" + minimalWorkflow + "  second:\n    runs-on: linux\n    env: {TOKEN: '${{ secrets.shared }}', EXTRA: '${{ secrets.LOCAL }}'}\n    steps:\n      - run: echo ok\n", jobs: 2, check: func(t *testing.T, w PlannedWorkflow) {
			if !reflect.DeepEqual(w.Jobs[0].Plan.SecretNames, []string{"SHARED"}) || !reflect.DeepEqual(w.Jobs[1].Plan.SecretNames, []string{"LOCAL", "SHARED"}) {
				t.Fatal("workflow and job secrets must be merged per job")
			}
		}},
		{name: "false job skipped", source: strings.Replace(minimalWorkflow, "runs-on: linux", "runs-on: linux\n    if: false", 1), jobs: 1, check: func(t *testing.T, w PlannedWorkflow) {
			if w.Jobs[0].Status != StatusSkipped {
				t.Fatal("false if must skip")
			}
		}},
		{name: "matrix from JSON", source: strings.Replace(minimalWorkflow, "runs-on: linux", "runs-on: linux\n    strategy:\n      matrix: ${{ fromJSON('{\"n\":[1,2]}') }}", 1), jobs: 2},
		{name: "JSON matrix declared axis order", source: strings.Replace(minimalWorkflow, "runs-on: linux", "runs-on: linux\n    strategy:\n      matrix: ${{ fromJSON('{\"version\":[1,2],\"os\":[\"linux\",\"macos\"]}') }}", 1), jobs: 4, check: func(t *testing.T, w PlannedWorkflow) {
			for i, job := range w.Jobs {
				if job.Plan.Context.Strategy.JobIndex != i || job.Plan.Context.Matrix["version"] != float64(i/2+1) || job.Plan.Context.Matrix["os"] != []string{"linux", "macos"}[i%2] {
					t.Fatalf("combination %d has a different job-index order: %+v", i, job.Plan.Context)
				}
			}
		}},
		{name: "matrix needs refused at admission", source: strings.Replace(minimalWorkflow, "runs-on: linux", "runs-on: linux\n    strategy:\n      matrix: ${{ fromJSON(needs.build.outputs.matrix) }}", 1), refusal: "workflow.context"},
		{name: "matrix JSON error is not empty", source: strings.Replace(minimalWorkflow, "runs-on: linux", "runs-on: linux\n    strategy:\n      matrix: ${{ fromJSON('bad') }}", 1), refusal: "workflow.wrong_type"},
		{name: "sixteen per workflow", source: strings.Replace(minimalWorkflow, "runs-on: linux", "runs-on: linux\n    strategy:\n      matrix: {n: [0,1,2,3,4,5,6,7,8,9,10,11,12,13,14,15]}", 1), jobs: 16},
		{name: "seventeen refuses whole run", source: strings.Replace(minimalWorkflow, "runs-on: linux", "runs-on: linux\n    strategy:\n      matrix: {n: [0,1,2,3,4,5,6,7,8,9,10,11,12,13,14,15,16]}", 1), code: "workflow.too_many_jobs"},
		{name: "max parallel invalid", source: strings.Replace(minimalWorkflow, "runs-on: linux", "runs-on: linux\n    strategy: {max-parallel: 0}", 1), refusal: "workflow.wrong_type"},
		{name: "concurrency group bound", source: "concurrency: '" + strings.Repeat("x", 201) + "'\n" + minimalWorkflow, code: "workflow.limit"},
		{name: "job timeout", source: strings.Replace(minimalWorkflow, "runs-on: linux", "runs-on: linux\n    timeout-minutes: 2.5", 1), jobs: 1, check: func(t *testing.T, w PlannedWorkflow) {
			if w.Jobs[0].TimeoutMinutes != 2.5 {
				t.Fatal("job timeout missing")
			}
		}},
		{name: "boolean string expression refused", source: strings.Replace(minimalWorkflow, "runs-on: linux", "runs-on: linux\n    continue-on-error: ${{ 'false' }}", 1), refusal: "workflow.wrong_type"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			source := tc.source
			if tc.fixture != "" {
				source = readFixture(t, tc.fixture)
			}
			workflow, err := Parse(".github/workflows/ci.yml", []byte(source))
			if err != nil {
				t.Fatal(err)
			}
			before, _ := cloneTestJSON(workflow)
			context := admissionContext()
			context.Inputs = map[string]any{"version": float64(20)}
			got, err := Plan(workflow, context)
			if errorCode(err) != tc.code {
				t.Fatalf("error = %v, want %s", err, tc.code)
			}
			if err != nil {
				return
			}
			if len(got.Jobs) != tc.jobs {
				t.Fatalf("%d jobs, want %d; refused: %+v", len(got.Jobs), tc.jobs, got.Facts.RefusedJobs)
			}
			if tc.refusal != "" && (len(got.Facts.RefusedJobs) != 1 || got.Facts.RefusedJobs[0].Reason.Code != tc.refusal || got.Facts.RefusedJobs[0].Reason.Detail == "") {
				t.Fatalf("refused jobs = %+v, want %s with remedy", got.Facts.RefusedJobs, tc.refusal)
			}
			for _, job := range got.Jobs {
				decoded, err := DecodePlan(job.Encoded, job.Digest)
				if err != nil || !reflect.DeepEqual(decoded, job.Plan) {
					t.Fatalf("plan round trip failed: %v", err)
				}
				if job.TimeoutMinutes <= 0 || job.Digest == "" {
					t.Fatal("resolved execution facts missing")
				}
			}
			after, _ := cloneTestJSON(workflow)
			if string(before) != string(after) {
				t.Fatal("planner mutated workflow")
			}
			if tc.check != nil {
				tc.check(t, got)
			}
		})
	}
}

func TestResolveJob(t *testing.T) {
	plan := JobPlan{JobKey: "deploy", Needs: []string{"build"}, If: "", Name: "Deploy ${{ needs.build.result }}", Context: admissionContext(), Steps: []Step{{Run: "echo deploy"}}}
	cases := []struct {
		name, condition, status, result string
		dependencies                    []Dependency
	}{
		{"waiting missing", "", StatusWaiting, "", nil},
		{"waiting one matrix member", "always()", StatusWaiting, "", []Dependency{{Status: StatusPassed}, {Status: StatusStarted}}},
		{"all members successful", "", StatusPending, ResultSuccess, []Dependency{{Status: StatusPassed}, {Status: StatusPassed}}},
		{"tolerated failure is success", "", StatusPending, ResultSuccess, []Dependency{{Status: StatusFailed, Tolerated: true}}},
		{"failed skips default", "", StatusSkipped, ResultFailure, []Dependency{{Status: StatusPassed}, {Status: StatusFailed}}},
		{"failure dependent", "failure()", StatusPending, ResultFailure, []Dependency{{Status: StatusFailed}}},
		{"refused dependency", "always()", StatusPending, ResultFailure, []Dependency{{Status: StatusRefused}}},
		{"cancelled default", "", StatusSkipped, ResultCancelled, []Dependency{{Status: StatusCancelled}}},
		{"cancelled condition", "cancelled()", StatusPending, ResultCancelled, []Dependency{{Status: StatusCancelled}}},
		{"skipped default", "", StatusSkipped, ResultSkipped, []Dependency{{Status: StatusSkipped}}},
		{"ancestor failure", "failure()", StatusPending, ResultSkipped, []Dependency{{Status: StatusSkipped, AncestorFailed: true}}},
		{"uncertain always skipped", "always()", StatusSkipped, ResultFailure, []Dependency{{Status: StatusAmbiguous}}},
		{"unavailable not tolerated", "failure()", StatusPending, ResultFailure, []Dependency{{Status: StatusUnavailable, Tolerated: true}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			input := plan
			input.If = tc.condition
			got, err := ResolveJob(input, map[string][]Dependency{"build": tc.dependencies})
			if err != nil || got.Status != tc.status || got.Plan.Context.Needs["build"] != tc.result {
				t.Fatalf("status %s, needs %+v, error %v; want %s, %s", got.Status, got.Plan.Context.Needs, err, tc.status, tc.result)
			}
			if got.Status == StatusPending && got.Name != "Deploy "+tc.result {
				t.Fatal("job name not evaluated when released")
			}
			if plan.Context.Needs != nil || plan.Name != "Deploy ${{ needs.build.result }}" {
				t.Fatal("resolver mutated input")
			}
		})
	}
}

func TestPlanEncoding(t *testing.T) {
	base := JobPlan{JobKey: "build", Context: admissionContext(), Steps: []Step{{Run: "echo ok"}}, Env: map[string]string{"B": "two", "A": "one"}, SecretNames: []string{"TOKEN"}}
	cases := []struct {
		name          string
		edit          func(*JobPlan, *[]byte, *string)
		code          string
		compareDigest bool
		changedDigest bool
	}{
		{name: "canonical round trip"},
		{name: "same job in two runs", edit: func(plan *JobPlan, _ *[]byte, _ *string) {
			plan.Context.GitHub.RunID = "different-run"
			plan.Context.GitHub.RunNumber = 99
			plan.Context.GitHub.RunAttempt = 2
		}, compareDigest: true},
		{name: "command changes digest", edit: func(plan *JobPlan, _ *[]byte, _ *string) { plan.Steps = []Step{{Run: "echo changed"}} }, compareDigest: true, changedDigest: true},
		{name: "env changes digest", edit: func(plan *JobPlan, _ *[]byte, _ *string) { plan.Env = map[string]string{"A": "changed"} }, compareDigest: true, changedDigest: true},
		{name: "matrix changes digest", edit: func(plan *JobPlan, _ *[]byte, _ *string) { plan.Context.Matrix = map[string]any{"n": float64(2)} }, compareDigest: true, changedDigest: true},
		{name: "commit changes digest", edit: func(plan *JobPlan, _ *[]byte, _ *string) { plan.Context.GitHub.SHA = strings.Repeat("b", 40) }, compareDigest: true, changedDigest: true},
		{name: "changed bytes", edit: func(_ *JobPlan, data *[]byte, _ *string) { (*data)[0] = '[' }, code: "workflow.wrong_type"},
		{name: "wrong digest", edit: func(_ *JobPlan, _ *[]byte, digest *string) { *digest = strings.Repeat("0", 64) }, code: "workflow.wrong_type"},
		{name: "encoded size bound", edit: func(_ *JobPlan, data *[]byte, _ *string) { *data = make([]byte, MaxPlanBytes+1) }, code: "workflow.limit"},
		{name: "product size bound", edit: func(plan *JobPlan, _ *[]byte, _ *string) {
			plan.Steps = []Step{{Run: strings.Repeat("x", MaxPlanBytes)}}
		}, code: "workflow.limit"},
		{name: "invalid JSON values", edit: func(plan *JobPlan, _ *[]byte, _ *string) { plan.Context.Matrix = map[string]any{"n": math.NaN()} }, code: "workflow.wrong_type"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			plan := base
			encoded, digest, err := EncodePlan(plan)
			if err != nil {
				t.Fatal(err)
			}
			original := string(encoded)
			if tc.edit != nil {
				tc.edit(&plan, &encoded, &digest)
			}
			if tc.compareDigest {
				other, changed, err := EncodePlan(plan)
				if err != nil || (changed != digest) != tc.changedDigest {
					t.Fatalf("digest changed = %t, want %t: %v", changed != digest, tc.changedDigest, err)
				}
				hash := sha256.Sum256(other)
				if changed != hex.EncodeToString(hash[:]) {
					t.Fatal("digest must hash the exact stored bytes")
				}
				decoded, err := DecodePlan(other, changed)
				if err != nil || decoded.Context.GitHub.RunID != "" || decoded.Context.GitHub.RunNumber != 0 || decoded.Context.GitHub.RunAttempt != 0 {
					t.Fatal("encoded plan retained per-run identity")
				}
				return
			}
			if tc.name == "product size bound" || tc.name == "invalid JSON values" {
				_, _, err = EncodePlan(plan)
			} else {
				_, err = DecodePlan(encoded, digest)
			}
			if errorCode(err) != tc.code {
				t.Fatalf("got %v, want %s", err, tc.code)
			}
			if tc.name == "canonical round trip" {
				reversed := base
				reversed.Env = map[string]string{"A": "one", "B": "two"}
				encoded, other, err := EncodePlan(reversed)
				if err != nil || string(encoded) != original || other != digest || len(digest) != 64 {
					t.Fatal("map insertion order affected encoding")
				}
				reversed.Steps = []Step{{Run: "echo changed"}}
				_, changed, _ := EncodePlan(reversed)
				if changed == digest {
					t.Fatal("changed execution plan retained digest")
				}
			}
		})
	}
}
