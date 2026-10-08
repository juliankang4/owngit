package actions

import (
	"reflect"
	"strings"
	"testing"
)

func TestDispatchInputs(t *testing.T) {
	definitions := map[string]DispatchInput{
		"flag":   {Type: "boolean", Default: false},
		"count":  {Type: "number", Default: float64(2)},
		"target": {Type: "choice", Required: true, Options: []string{"test", "prod"}},
	}
	cases := []struct {
		name     string
		supplied map[string]any
		want     map[string]any
		code     string
	}{
		{"defaults typed", map[string]any{"target": "test"}, map[string]any{"flag": false, "count": float64(2), "target": "test"}, ""},
		{"string booleans and numbers", map[string]any{"flag": "true", "count": "3.5", "target": "prod"}, map[string]any{"flag": true, "count": 3.5, "target": "prod"}, ""},
		{"unknown input", map[string]any{"target": "test", "other": "x"}, nil, "workflow.dispatch_input"},
		{"required input missing", nil, nil, "workflow.dispatch_input"},
		{"choice outside list", map[string]any{"target": "other"}, nil, "workflow.dispatch_input"},
		{"boolean string yes", map[string]any{"flag": "yes", "target": "test"}, nil, "workflow.dispatch_input"},
		{"nonfinite number", map[string]any{"count": "NaN", "target": "test"}, nil, "workflow.dispatch_input"},
		{"value size", map[string]any{"target": strings.Repeat("x", 1025)}, nil, "workflow.dispatch_input"},
		{"NUL", map[string]any{"target": "test\x00"}, nil, "workflow.dispatch_input"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, event, err := DispatchInputs(definitions, tc.supplied)
			if errorCode(err) != tc.code || err == nil && !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("got %+v, %v; want %+v, %s", got, err, tc.want, tc.code)
			}
			if err == nil {
				for name, value := range got {
					text, _ := ScalarString(value)
					if event[name] != text {
						t.Fatal("event inputs must be strings while inputs remain typed")
					}
				}
			}
		})
	}
}

func TestEvaluateConcurrency(t *testing.T) {
	ctx, err := ContextFromPlan(admissionContext())
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name string
		raw  Concurrency
		want EvaluatedConcurrency
		code string
	}{
		{"unset", Concurrency{}, EvaluatedConcurrency{Queue: "single"}, ""},
		{"evaluated group", Concurrency{Group: "CI-${{ github.ref }}", CancelInProgress: "true"}, EvaluatedConcurrency{Group: "ci-refs/heads/main", CancelInProgress: true, Queue: "single"}, ""},
		{"max queue", Concurrency{Group: "ci", Queue: "max"}, EvaluatedConcurrency{Group: "ci", Queue: "max"}, ""},
		{"dynamic cancellation", Concurrency{Group: "ci", CancelInProgress: "${{ github.ref_name == 'main' }}"}, EvaluatedConcurrency{Group: "ci", CancelInProgress: true, Queue: "single"}, ""},
		{"max cancellation refused", Concurrency{Group: "ci", Queue: "max", CancelInProgress: "${{ true }}"}, EvaluatedConcurrency{}, "workflow.wrong_type"},
		{"empty evaluated group", Concurrency{Group: "${{ inputs.absent }}"}, EvaluatedConcurrency{}, "workflow.wrong_type"},
		{"group size", Concurrency{Group: strings.Repeat("x", 201)}, EvaluatedConcurrency{}, "workflow.limit"},
		{"normalized group size", Concurrency{Group: strings.Repeat("\u023a", 100)}, EvaluatedConcurrency{}, "workflow.limit"},
		{"raw group size before normalization", Concurrency{Group: strings.Repeat("\u0130", 150)}, EvaluatedConcurrency{}, "workflow.limit"},
		{"invalid queue", Concurrency{Group: "ci", Queue: "all"}, EvaluatedConcurrency{}, "workflow.wrong_type"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := EvaluateConcurrency(tc.raw, "workflow.concurrency", ctx)
			if errorCode(err) != tc.code || err == nil && got != tc.want {
				t.Fatalf("got %+v, %v; want %+v, %s", got, err, tc.want, tc.code)
			}
		})
	}
}

func TestBuiltins(t *testing.T) {
	ctx, err := ContextFromPlan(admissionContext())
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		uses, input, value, status, code, note string
	}{
		{"actions/checkout@v6", "ref", "${{ github.sha }}", StatusPassed, "", "note.checkout"},
		{"actions/checkout@main", "repository", "${{ github.repository }}", StatusPassed, "", "note.checkout"},
		{"actions/checkout@abc", "path", ".", StatusPassed, "", "note.checkout"},
		{"actions/checkout@v6", "lfs", "false", StatusPassed, "", "note.checkout"},
		{"actions/checkout@v6", "ref", "other", "", "workflow.checkout_input", ""},
		{"actions/checkout@v6", "path", "subfolder", "", "workflow.checkout_input", ""},
		{"actions/setup-go@v6", "go-version", "1.27", StatusNotRun, "", "note.setup"},
		{"actions/setup-node@v6", "node-version", "24", StatusNotRun, "", "note.setup"},
		{"actions/setup-python@v6", "python-version", "3.14", StatusNotRun, "", "note.setup"},
		{"actions/setup-java@v5", "java-version", "21", StatusNotRun, "", "note.setup"},
		{"actions/cache@v4", "key", "${{ hashFiles('x') }}", StatusNotRun, "", "note.cache"},
		{"actions/cache/restore@v4", "path", "cache", StatusNotRun, "", "note.cache"},
		{"actions/cache/save@v4", "path", "cache", StatusNotRun, "", "note.cache"},
		{"actions/upload-artifact@v4", "path", "out", StatusNotRun, "", "note.artifact"},
		{"actions/download-artifact@v4", "", "", "", "workflow.artifact_download", ""},
		{"vendor/tool@v1", "", "", "", "workflow.action", ""},
		{"./tool", "", "", "", "workflow.local_action", ""},
		{"docker://tool", "", "", "", "workflow.docker_action", ""},
		{"actions/checkout", "", "", "", "workflow.action", ""},
		{"actions/setup-go@v6", "typo", "x", "", "workflow.unknown_key", ""},
	}
	for _, tc := range cases {
		t.Run(tc.uses+"/"+tc.input+"/"+tc.value, func(t *testing.T) {
			step := Step{Uses: tc.uses, With: map[string]string{}}
			if tc.input != "" {
				step.With[tc.input] = tc.value
			}
			builtin, notes, err := EvaluateBuiltin(step, ctx)
			if errorCode(err) != tc.code {
				t.Fatalf("got %v, want %s", err, tc.code)
			}
			if err == nil && (builtin.Status != tc.status || len(notes) != 1 || notes[0].Code != tc.note || notes[0].Detail == "") {
				t.Fatalf("builtin %+v, notes %+v", builtin, notes)
			}
			if err == nil && tc.note == "note.cache" && builtin.Outputs["cache-hit"] != "false" {
				t.Fatal("cache no-op must report a miss")
			}
		})
	}
}
