package actions

import (
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"testing"

	"go.yaml.in/yaml/v3"
)

func TestStepNoteArgsMasked(t *testing.T) {
	mask := newMasker(map[string]string{"TEST": "synthetic-step-secret"})
	step := StepResult{Notes: []Message{{Code: "note.ignored_env", Detail: "Ignored synthetic-step-secret", Args: map[string]string{"name": "synthetic-step-secret"}}}}
	step.maskMetadata(mask)
	if step.Notes[0].Detail != "Ignored [redacted]" || step.Notes[0].Args["name"] != "[redacted]" {
		t.Fatalf("note metadata was not masked: %+v", step.Notes[0])
	}
}

func TestUnlocatedMessageArgsFallBack(t *testing.T) {
	cases := []struct {
		name    string
		message func() Message
		path    string
		detail  string
	}{
		{"missing on", func() Message {
			_, err := Parse(".github/workflows/test.yml", []byte("jobs:\n  build:\n    runs-on: linux\n    steps:\n      - run: echo ok\n"))
			return refusalMessageForTest(t, err)
		}, "on", "on (line 0) must be an event name, list or mapping."},
		{"missing jobs", func() Message {
			_, err := Parse(".github/workflows/test.yml", []byte("on: push\n"))
			return refusalMessageForTest(t, err)
		}, "jobs", "jobs (line 0) must be a mapping."},
		{"missing runs-on", func() Message {
			_, err := Parse(".github/workflows/test.yml", []byte("on: push\njobs:\n  build:\n    steps:\n      - run: echo ok\n"))
			return refusalMessageForTest(t, err)
		}, "jobs.build.runs-on", "jobs.build.runs-on (line 0) must be a runner label or list of labels."},
		{"missing steps", func() Message {
			_, err := Parse(".github/workflows/test.yml", []byte("on: push\njobs:\n  build:\n    runs-on: linux\n"))
			return refusalMessageForTest(t, err)
		}, "jobs.build.steps", "jobs.build.steps (line 0) must be a nonempty list of steps."},
		{"expression", func() Message { return refusalMessageForTest(t, expressionError("job.if", "a boolean")) }, "", ""},
		{"unknown builtin input", func() Message {
			_, _, err := EvaluateBuiltin(Step{Uses: "actions/checkout@v4", With: map[string]string{"unknown": "value"}}, EvalContext{})
			return refusalMessageForTest(t, err)
		}, "", ""},
		{"job planning", func() Message { return refusedJob(JobPlan{}, errors.New("synthetic failure")).Reason }, "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.message()
			if got.Args != nil || got.Detail == "" {
				t.Fatalf("unlocated message has invented args or no detail: %+v", got)
			}
			if tc.path != "" && (got.Code != "workflow.wrong_type" || got.Path != tc.path || got.Line != 0 || got.Detail != tc.detail) {
				t.Fatalf("missing field refusal = %+v, want path %q and detail %q", got, tc.path, tc.detail)
			}
		})
	}
}

func refusalMessageForTest(t *testing.T, err error) Message {
	t.Helper()
	var refusal *Refusal
	if !errors.As(err, &refusal) {
		t.Fatalf("not a workflow refusal: %v", err)
	}
	return refusal.Message
}

func TestMessageArgsCatalog(t *testing.T) {
	message := func(err error) Message {
		var refusal *Refusal
		if !errors.As(err, &refusal) {
			t.Fatalf("not a workflow refusal: %v", err)
		}
		return refusal.Message
	}
	cases := []struct {
		code   string
		keys   []string
		sample func() Message
	}{
		{"workflow.yaml", []string{"line", "detail"}, func() Message { return message(invalidYAML("file", errors.New("yaml: line 7: bad"))) }},
		{"workflow.yaml_feature", []string{"line", "feature"}, func() Message { return message(yamlFeature(&yaml.Node{Line: 2}, "jobs", "aliases")) }},
		{"workflow.duplicate_key", []string{"key", "path", "a", "b"}, func() Message {
			return message(duplicateKey(&yaml.Node{Line: 1}, &yaml.Node{Line: 2}, "run", "jobs.test"))
		}},
		{"workflow.unknown_key", []string{"path", "line"}, func() Message { return message(unknownKey(&yaml.Node{Line: 3}, "jobs.test.unknown")) }},
		{"workflow.wrong_type", []string{"path", "line", "expected"}, func() Message { return message(wrongType(&yaml.Node{Line: 3}, "job", "a string")) }},
		{"workflow.limit", []string{"what", "limit"}, func() Message { return message(limitError("jobs", 16)) }},
		{"workflow.action", []string{"action"}, func() Message { _, err := LookupBuiltin("other/action@v1"); return message(err) }},
		{"workflow.local_action", []string{"action"}, func() Message { _, err := LookupBuiltin("./tool@v1"); return message(err) }},
		{"workflow.docker_action", []string{"action"}, func() Message { _, err := LookupBuiltin("docker://image@v1"); return message(err) }},
		{"workflow.reusable", []string{"workflow"}, func() Message { return refusedFeature("uses", "other.yml") }},
		{"workflow.container", nil, func() Message { return refusedFeature("container", "") }},
		{"workflow.services", nil, func() Message { return refusedFeature("services", "") }},
		{"workflow.environment", nil, func() Message { return refusedFeature("environment", "") }},
		{"workflow.snapshot", nil, func() Message { return refusedFeature("snapshot", "") }},
		{"workflow.background", []string{"key"}, func() Message { return refusedFeature("parallel", "") }},
		{"workflow.secret_ref", nil, func() Message { return message(secretReferenceError("if")) }},
		{"workflow.token", []string{"expr"}, func() Message { return message(tokenError("github.token", "if")) }},
		{"workflow.context", []string{"context", "key", "hint"}, func() Message { return message(contextError("needs", "job.if", "use env")) }},
		{"workflow.function", []string{"function", "hint"}, func() Message { _, err := ValidateTemplate("${{ hashFiles('x') }}", "step.if"); return message(err) }},
		{"workflow.event", []string{"event"}, func() Message { return eventMessage("release") }},
		{"workflow.event_pr_target", nil, func() Message { return eventMessage("pull_request_target") }},
		{"workflow.tags", nil, nil},
		{"workflow.timezone", nil, nil},
		{"workflow.cron_never", []string{"cron"}, func() Message { _, err := ParseCron("0 0 31 2 *"); return message(err) }},
		{"workflow.too_many_jobs", []string{"count"}, func() Message { return message(tooManyJobs(17)) }},
		{"workflow.never_fits", []string{"count", "limit"}, nil},
		{"workflow.env_name", []string{"name"}, func() Message {
			_, err := readEnv(&yaml.Node{Kind: yaml.MappingNode, Content: []*yaml.Node{{Kind: yaml.ScalarNode, Value: "bad-name", Line: 2}, {Kind: yaml.ScalarNode, Value: "x"}}}, "env")
			return message(err)
		}},
		{"workflow.env_too_long", []string{"name", "limit"}, nil},
		{"workflow.shell", []string{"shell"}, nil},
		{"workflow.shell_unavailable", []string{"shell", "os"}, nil},
		{"workflow.step_timeout", []string{"limit"}, nil},
		{"workflow.checkout_input", []string{"input", "sha"}, nil},
		{"workflow.artifact_download", nil, func() Message { _, err := LookupBuiltin("actions/download-artifact@v4"); return message(err) }},
		{"workflow.run_too_long", nil, nil},
		{"workflow.not_run_queue", []string{"waiting", "limit", "needed"}, nil},
		{"workflow.off", nil, nil},
		{"workflow.event_off", []string{"event"}, nil},
		{"workflow.dispatch_input", []string{"input", "reason"}, func() Message { return message(dispatchError("target", "missing")) }},
		{"workflow.moved", []string{"branch", "oid"}, nil},
		{"workflow.secrets_unreadable", nil, nil},
		{"note.checkout", []string{"sha"}, func() Message {
			_, notes, err := EvaluateBuiltin(Step{Uses: "actions/checkout@v4"}, EvalContext{Values: map[string]any{"github": map[string]any{"sha": "abc"}}})
			if err != nil {
				t.Fatal(err)
			}
			return notes[0]
		}},
		{"note.setup", []string{"tool", "version"}, func() Message {
			_, notes, err := EvaluateBuiltin(Step{Uses: "actions/setup-go@v5"}, EvalContext{})
			if err != nil {
				t.Fatal(err)
			}
			return notes[0]
		}},
		{"note.cache", nil, nil},
		{"note.artifact", nil, nil},
		{"note.runs_on", []string{"label", "where"}, nil},
		{"note.not_applied", []string{"key", "effect"}, func() Message { return notApplied("permissions") }},
		{"note.nothing_ran", nil, nil},
		{"note.tolerated", nil, nil},
		{"note.paths_unknown", []string{"reason"}, nil},
		{"note.concurrency_wait", []string{"n", "group"}, nil},
		{"note.concurrency_cancel", []string{"n", "group"}, nil},
		{"note.max_parallel", []string{"n"}, nil},
		{"note.fail_fast", []string{"job"}, nil},
		{"note.uncertain", []string{"job"}, nil},
		{"note.schedule_often", nil, nil},
		{"note.missed", []string{"slot", "time"}, nil},
		{"note.schedule_paused", nil, nil},
		{"note.runner_old", nil, nil},
		{"note.secret_missing", []string{"name"}, nil},
		{"note.plain_http", nil, nil},
		{"note.open_dispatch", nil, nil},
		{"note.ignored_env", []string{"name"}, nil},
		{"note.mask_limit", nil, nil},
		{"note.stopped", nil, nil},
		{"note.summary", nil, nil},
	}
	for _, tc := range cases {
		t.Run(tc.code, func(t *testing.T) {
			if tc.sample == nil {
				return
			}
			got := tc.sample()
			var keys []string
			for key := range got.Args {
				keys = append(keys, key)
			}
			slices.Sort(keys)
			want := slices.Clone(tc.keys)
			slices.Sort(want)
			if got.Code != tc.code || !reflect.DeepEqual(keys, want) {
				t.Fatalf("code=%s args=%v, want %s %v", got.Code, keys, tc.code, want)
			}
		})
	}
	for _, raw := range []string{`{"code":"workflow.limit","detail":"old record"}`, `{"code":"workflow.limit","detail":"new record","args":{"what":"jobs","limit":"16"}}`} {
		var decoded Message
		if err := json.Unmarshal([]byte(raw), &decoded); err != nil || decoded.Code != "workflow.limit" {
			t.Fatalf("message compatibility: %+v %v", decoded, err)
		}
	}
}
