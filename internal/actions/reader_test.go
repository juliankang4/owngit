package actions

import (
	"errors"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"
)

const minimalWorkflow = "on: push\njobs:\n  build:\n    runs-on: linux\n    steps:\n      - run: echo ok\n"

func errorCode(err error) string {
	if err == nil {
		return ""
	}
	var refusal *Refusal
	if errors.As(err, &refusal) {
		return refusal.Code
	}
	return err.Error()
}

func readFixture(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestParse(t *testing.T) {
	cases := []struct {
		name, fixture, source, code, jobRefusal, detail string
		check                                           func(*testing.T, *Workflow)
	}{
		{name: "scalar event", source: minimalWorkflow},
		{name: "null event mapping", source: strings.Replace(minimalWorkflow, "on: push", "on:\n  push:\n  pull_request:", 1)},
		{name: "sequence and alias", fixture: "aliases.yaml", check: func(t *testing.T, w *Workflow) {
			if w.Env["MODE"] != "yes" || w.Jobs[0].Env["MODE"] != "yes" || len(w.Jobs[0].Steps) != 2 {
				t.Fatalf("aliases or YAML 1.2 strings changed: %+v", w)
			}
		}},
		{name: "complete keys", fixture: "ci.yml", check: func(t *testing.T, w *Workflow) {
			if len(w.Events) != 3 || len(w.Jobs) != 2 || !reflect.DeepEqual(w.Jobs[0].MatrixOrder, []string{"go", "os"}) || len(w.Digest) != 64 {
				t.Fatalf("workflow facts missing: %+v", w)
			}
		}},
		{name: "unsupported job keeps sibling", fixture: "refused.yml", jobRefusal: "workflow.environment"},
		{name: "aliased mapping key", source: strings.Replace("env:\n  &var MODE: one\n"+minimalWorkflow, "runs-on: linux", "runs-on: linux\n    env:\n      *var : two", 1), check: func(t *testing.T, w *Workflow) {
			if w.Env["MODE"] != "one" || w.Jobs[0].Env["MODE"] != "two" {
				t.Fatal("aliased key lost its value")
			}
		}},
		{name: "duplicate aliased key", source: "env:\n  &var MODE: one\n  *var : two\n" + minimalWorkflow, code: "workflow.duplicate_key", detail: "`MODE` appears twice in `.github/workflows/ci.yml.env` (lines 2 and 3). Keep one."},
		{name: "nonstring aliased key", source: "env:\n  NUMBER: &var 7\n  *var : two\n" + minimalWorkflow, code: "workflow.wrong_type"},
		{name: "duplicate lines", fixture: "duplicate.yml", code: "workflow.duplicate_key", detail: "`run` appears twice in `.github/workflows/ci.yml.jobs.build.steps` (lines 6 and 7). Keep one."},
		{name: "unknown file key", source: "typo: true\n" + minimalWorkflow, code: "workflow.unknown_key", detail: "`workflow.typo` (line 1) is not a workflow key OwnGit knows. Check the spelling against GitHub's workflow syntax, or remove it."},
		{name: "unknown nested step key", source: strings.Replace(minimalWorkflow, "run: echo ok", "run: echo ok\n        typo: true", 1), code: "workflow.unknown_key"},
		{name: "bad YAML", source: "on: [push}", code: "workflow.yaml", detail: "Line 1 is not valid YAML: yaml: did not find expected ',' or ']'"},
		{name: "bad second YAML", source: minimalWorkflow + "---\non: [push}", code: "workflow.yaml", detail: "Line 7 is not valid YAML: yaml: line 7: did not find expected ',' or ']'"},
		{name: "empty file", code: "workflow.yaml"},
		{name: "second document", source: minimalWorkflow + "---\non: push\n", code: "workflow.yaml_feature", detail: "Line 7 uses a second document, which OwnGit does not read in workflow files. Write the values out in full."},
		{name: "explicit tag", source: strings.Replace(minimalWorkflow, "on: push", "on: !!str push", 1), code: "workflow.yaml_feature", detail: "Line 1 uses a tag, which OwnGit does not read in workflow files. Write the values out in full."},
		{name: "merge key", source: strings.Replace(minimalWorkflow, "runs-on: linux", "<<: {runs-on: linux}", 1), code: "workflow.yaml_feature", detail: "Line 4 uses a merge key (<<), which OwnGit does not read in workflow files. Write the values out in full."},
		{name: "alias cycle", source: "on: push\njobs: &jobs\n  build: *jobs\n", code: "workflow.limit"},
		{name: "node expansion", source: aliasExpansion(), code: "workflow.limit"},
		{name: "UTF-8", source: string([]byte{0xff}), code: "workflow.yaml"},
		{name: "file size", source: strings.Repeat(" ", MaxWorkflowBytes+1), code: "workflow.limit"},
		{name: "unknown event", source: strings.Replace(minimalWorkflow, "on: push", "on: potato", 1), code: "workflow.unknown_key", detail: "`on.potato` (line 1) is not a workflow key OwnGit knows. Check the spelling against GitHub's workflow syntax, or remove it."},
		{name: "unknown sequence event", source: strings.Replace(minimalWorkflow, "on: push", "on:\n  - push\n  - potato", 1), code: "workflow.unknown_key", detail: "`on.potato` (line 3) is not a workflow key OwnGit knows. Check the spelling against GitHub's workflow syntax, or remove it."},
		{name: "duplicate sequence event", source: strings.Replace(minimalWorkflow, "on: push", "on:\n  - push\n  - push", 1), code: "workflow.duplicate_key", detail: "`push` appears twice in `on` (lines 2 and 3). Keep one."},
		{name: "empty branches", source: strings.Replace(minimalWorkflow, "on: push", "on:\n  push:\n    branches: []", 1), code: "workflow.wrong_type"},
		{name: "empty branches-ignore", source: strings.Replace(minimalWorkflow, "on: push", "on:\n  push:\n    branches-ignore: []", 1), code: "workflow.wrong_type"},
		{name: "empty paths", source: strings.Replace(minimalWorkflow, "on: push", "on:\n  push:\n    paths: []", 1), code: "workflow.wrong_type"},
		{name: "empty paths-ignore", source: strings.Replace(minimalWorkflow, "on: push", "on:\n  push:\n    paths-ignore: []", 1), code: "workflow.wrong_type"},
		{name: "empty tags", source: strings.Replace(minimalWorkflow, "on: push", "on:\n  push:\n    tags: []", 1), code: "workflow.wrong_type"},
		{name: "empty tags-ignore", source: strings.Replace(minimalWorkflow, "on: push", "on:\n  push:\n    tags-ignore: []", 1), code: "workflow.wrong_type"},
		{name: "empty PR types", source: strings.Replace(minimalWorkflow, "on: push", "on:\n  pull_request:\n    types: []", 1), code: "workflow.wrong_type"},
		{name: "empty PR branches", source: strings.Replace(minimalWorkflow, "on: push", "on:\n  pull_request:\n    branches: []", 1), code: "workflow.wrong_type"},
		{name: "empty PR paths-ignore", source: strings.Replace(minimalWorkflow, "on: push", "on:\n  pull_request:\n    paths-ignore: []", 1), code: "workflow.wrong_type"},
		{name: "empty workflow concurrency", source: "concurrency: ''\n" + minimalWorkflow, code: "workflow.wrong_type"},
		{name: "empty job concurrency", source: strings.Replace(minimalWorkflow, "runs-on: linux", "runs-on: linux\n    concurrency: ''", 1), code: "workflow.wrong_type"},
		{name: "conflicting branches", source: strings.Replace(minimalWorkflow, "on: push", "on:\n  push:\n    branches: [main]\n    branches-ignore: [docs]", 1), code: "workflow.wrong_type"},
		{name: "conflicting paths", source: strings.Replace(minimalWorkflow, "on: push", "on:\n  push:\n    paths: ['**']\n    paths-ignore: [docs]", 1), code: "workflow.wrong_type"},
		{name: "negative only", source: strings.Replace(minimalWorkflow, "on: push", "on:\n  push:\n    branches: ['!main']", 1), code: "workflow.wrong_type"},
		{name: "timezone", source: strings.Replace(minimalWorkflow, "on: push", "on:\n  schedule:\n    - cron: '0 0 * * *'\n      timezone: Asia/Seoul", 1), code: "workflow.timezone"},
		{name: "schedule entries", source: strings.Replace(minimalWorkflow, "on: push", "on:\n  push:\n  schedule:\n"+strings.Repeat("    - cron: '0 0 * * *'\n", 11), 1), check: func(t *testing.T, w *Workflow) {
			trigger := w.Events["schedule"]
			if len(trigger.Schedules) != 10 || len(trigger.RefusedSchedules) != 1 || trigger.RefusedSchedules[0].Code != "workflow.limit" || len(w.Jobs) != 1 {
				t.Fatal("extra schedule must refuse only that entry")
			}
			if _, ok := w.Events["push"]; !ok {
				t.Fatal("extra schedule removed the push trigger")
			}
		}},
		{name: "environment input", source: strings.Replace(minimalWorkflow, "on: push", "on:\n  workflow_dispatch:\n    inputs:\n      target:\n        type: environment", 1), code: "workflow.wrong_type"},
		{name: "numeric normalization", source: strings.Replace(minimalWorkflow, "runs-on: linux", "runs-on: linux\n    env: {VERSION: 1.20}", 1), check: func(t *testing.T, w *Workflow) {
			if w.Jobs[0].Env["VERSION"] != "1.2" {
				t.Fatal("number did not use GitHub conversion")
			}
		}},
		{name: "boolean yes is not true", source: strings.Replace(minimalWorkflow, "runs-on: linux", "runs-on: linux\n    continue-on-error: yes", 1), code: "workflow.wrong_type"},
		{name: "missing dependency", source: strings.Replace(minimalWorkflow, "runs-on: linux", "runs-on: linux\n    needs: absent", 1), code: "workflow.wrong_type"},
		{name: "dependency cycle", source: strings.Replace(minimalWorkflow, "runs-on: linux", "runs-on: linux\n    needs: build", 1), code: "workflow.wrong_type"},
		{name: "invalid identifier", source: strings.Replace(minimalWorkflow, "build:", "123:", 1), code: "workflow.wrong_type"},
		{name: "invalid env name", source: strings.Replace(minimalWorkflow, "runs-on: linux", "runs-on: linux\n    env: {BAD-NAME: value}", 1), code: "workflow.env_name"},
		{name: "env value bound", source: strings.Replace(minimalWorkflow, "runs-on: linux", "runs-on: linux\n    env: {LONG: '"+strings.Repeat("x", (48<<10)+1)+"'}", 1), code: "workflow.limit"},
		{name: "steps bound", source: strings.Replace(minimalWorkflow, "      - run: echo ok\n", strings.Repeat("      - run: echo ok\n", MaxSteps+1), 1), jobRefusal: "workflow.limit"},
		{name: "run bound", source: strings.Replace(minimalWorkflow, "echo ok", strings.Repeat("x", MaxRunBytes+1), 1), jobRefusal: "workflow.run_too_long"},
		{name: "missing runs-on", source: strings.Replace(minimalWorkflow, "    runs-on: linux\n", "", 1), code: "workflow.wrong_type"},
		{name: "run and uses", source: strings.Replace(minimalWorkflow, "run: echo ok", "run: echo ok\n        uses: actions/checkout@v6", 1), code: "workflow.wrong_type"},
		{name: "background", source: strings.Replace(minimalWorkflow, "run: echo ok", "run: echo ok\n        background: true", 1), jobRefusal: "workflow.background"},
		{name: "reusable", source: "on: push\njobs:\n  other:\n    uses: example/repo/.github/workflows/ci.yml@main\n", jobRefusal: "workflow.reusable"},
		{name: "queue max cancel", source: "concurrency: {group: ci, queue: max, cancel-in-progress: true}\n" + minimalWorkflow, code: "workflow.wrong_type"},
		{name: "noted current permissions", source: "permissions: {artifact-metadata: write, code-quality: read, vulnerability-alerts: read}\ncache-mode: write\n" + minimalWorkflow, check: func(t *testing.T, w *Workflow) {
			if len(w.Notes) != 2 {
				t.Fatal("noted keys lost")
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			source := tc.source
			if tc.fixture != "" {
				source = readFixture(t, tc.fixture)
			}
			w, err := Parse(".github/workflows/ci.yml", []byte(source))
			if errorCode(err) != tc.code {
				t.Fatalf("code = %q, want %q: %v", errorCode(err), tc.code, err)
			}
			if err != nil {
				if tc.detail != "" {
					var refusal *Refusal
					if !errors.As(err, &refusal) || refusal.Detail != tc.detail {
						t.Fatalf("detail = %v, want %q", err, tc.detail)
					}
				}
				return
			}
			if tc.jobRefusal != "" {
				found := false
				for _, job := range w.Jobs {
					found = found || job.Refusal != nil && job.Refusal.Code == tc.jobRefusal
				}
				if !found {
					t.Fatalf("missing refused job %s: %+v", tc.jobRefusal, w.Jobs)
				}
			}
			if tc.check != nil {
				tc.check(t, w)
			}
		})
	}
}

func aliasExpansion() string {
	source := "on: push\njobs:\n  build:\n    runs-on: linux\n    steps:\n      - run: echo ok\nenv:\n  A: &a [x, x, x, x, x, x, x, x, x, x]\n"
	previous := "a"
	for i := 0; i < 6; i++ {
		name := fmt.Sprintf("b%d", i)
		source += fmt.Sprintf("  V%d: &%s [%s]\n", i, name, strings.TrimSuffix(strings.Repeat("*"+previous+", ", 10), ", "))
		previous = name
	}
	return source
}

func TestParseFiles(t *testing.T) {
	regular := WorkflowFile{Path: ".github/workflows/ci.yml", Mode: "100644", Size: int64(len(minimalWorkflow)), Data: []byte(minimalWorkflow)}
	cases := []struct {
		name  string
		files []WorkflowFile
		count int
		code  string
	}{
		{"regular", []WorkflowFile{regular}, 1, ""},
		{"path order", []WorkflowFile{{Path: ".github/workflows/z.yml", Mode: regular.Mode, Size: regular.Size, Data: regular.Data}, regular}, 2, ""},
		{"subdirectory ignored", []WorkflowFile{{Path: ".github/workflows/sub/ci.yml"}}, 0, ""},
		{"symlink", []WorkflowFile{{Path: regular.Path, Mode: "120000", Size: regular.Size, Data: regular.Data}}, 1, "workflow.wrong_type"},
		{"size mismatch", []WorkflowFile{{Path: regular.Path, Mode: regular.Mode, Size: 1, Data: regular.Data}}, 1, "workflow.wrong_type"},
		{"filename bound", []WorkflowFile{{Path: ".github/workflows/" + strings.Repeat("x", 101) + ".yml", Mode: regular.Mode, Size: regular.Size, Data: regular.Data}}, 1, "workflow.limit"},
		{"file count", workflowFiles(33, []byte(minimalWorkflow)), 33, "workflow.limit"},
		{"total bytes", workflowFiles(9, []byte(minimalWorkflow+"#"+strings.Repeat("x", MaxWorkflowBytes-len(minimalWorkflow)-1))), 9, "workflow.limit"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			entries := ParseFiles(tc.files)
			if len(entries) != tc.count {
				t.Fatalf("read %d files, want %d", len(entries), tc.count)
			}
			if len(entries) > 0 {
				last := entries[len(entries)-1]
				code := ""
				if last.Refusal != nil {
					code = last.Refusal.Code
				}
				if code != tc.code {
					t.Fatalf("refusal = %+v, want %s", last.Refusal, tc.code)
				}
			}
			if tc.name == "path order" && entries[0].Path != regular.Path {
				t.Fatal("files not sorted")
			}
		})
	}
}

func workflowFiles(count int, data []byte) []WorkflowFile {
	files := make([]WorkflowFile, count)
	for i := range files {
		files[i] = WorkflowFile{Path: fmt.Sprintf(".github/workflows/%02d.yml", i), Mode: "100644", Size: int64(len(data)), Data: data}
	}
	return files
}
