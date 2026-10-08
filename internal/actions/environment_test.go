package actions

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEnvironmentLayers(t *testing.T) {
	tests := []struct {
		name       string
		commandEnv string
		configure  func(*JobPlan, *RunOptions)
		wantError  bool
		check      func(*testing.T, JobResult, []map[string]string, []string)
	}{
		{name: "precedence and own command files", configure: func(plan *JobPlan, options *RunOptions) {
			options.BaseEnvironment = []string{"VALUE=base", "PATH=/base"}
			plan.WorkflowEnv, plan.Env = map[string]string{"VALUE": "workflow"}, map[string]string{"VALUE": "job", "OTHER": "${{ env.VALUE }}"}
			plan.Steps = []Step{{Run: "one", Env: map[string]string{"VALUE": "step"}}, {Run: "two"}, {Run: "three", Env: map[string]string{"VALUE": "last-step"}}}
		}, check: func(t *testing.T, job JobResult, envs []map[string]string, _ []string) {
			if envs[0]["VALUE"] != "step" || envs[1]["VALUE"] != "command" || envs[2]["VALUE"] != "last-step" || envs[0]["OTHER"] != "workflow" {
				t.Fatal(envs)
			}
			if envs[0]["GITHUB_ENV"] == envs[1]["GITHUB_ENV"] || !strings.Contains(envs[1]["PATH"], strings.Join([]string{"/new", "/old", "/base"}, string(filepath.ListSeparator))) {
				t.Fatal(envs)
			}
			if job.Steps[0].Notes[0].Code != "note.ignored_env" || envs[1]["GITHUB_RUN_ID"] != "run-17" {
				t.Fatal(job, envs)
			}
		}},
		{name: "runtime identity and private outputs", configure: func(plan *JobPlan, _ *RunOptions) {
			plan.Steps = []Step{{ID: "first", Run: "${{ github.run_id }}"}, {Run: "${{ steps.first.outputs.token }}"}}
		}, check: func(t *testing.T, job JobResult, envs []map[string]string, codes []string) {
			if codes[0] != "run-17" || codes[1] != testSecret || envs[0]["GITHUB_RUN_NUMBER"] != "17" || envs[0]["GITHUB_RUN_ATTEMPT"] != "2" || envs[0]["GITHUB_ACTIONS"] != "true" || job.Steps[0].Outputs["token"] != "[redacted]" {
				t.Fatal(job, envs, codes)
			}
		}},
		{name: "windows comparison and separator", configure: func(plan *JobPlan, options *RunOptions) {
			options.RunnerOS = "Windows"
			options.BaseEnvironment = []string{"value=base", "Path=/base", "ProgramFiles(x86)=/programs"}
			plan.WorkflowEnv, plan.Env = map[string]string{"VALUE": "workflow"}, map[string]string{"Value": "job"}
			plan.Steps = []Step{{Run: "one", Env: map[string]string{"vAlUe": "step"}}, {Run: "two"}}
		}, check: func(t *testing.T, _ JobResult, envs []map[string]string, _ []string) {
			if environmentValue(envs[0], "VALUE", true) != "step" || environmentValue(envs[1], "VALUE", true) != "command" || environmentValue(envs[1], "PATH", true) != "/new;/old;/base" || envs[0]["ProgramFiles(x86)"] != "/programs" {
				t.Fatal(envs)
			}
			count := 0
			for name := range envs[0] {
				if strings.EqualFold(name, "VALUE") {
					count++
				}
			}
			if count != 1 {
				t.Fatal(envs)
			}
		}},
		{name: "windows last command environment assignment reaches next step", commandEnv: "VALUE=first\nValue=second\nvalue=last\n", configure: func(plan *JobPlan, options *RunOptions) {
			options.RunnerOS = "Windows"
			plan.Steps = []Step{{Run: "one"}, {Run: "two"}}
		}, check: func(t *testing.T, _ JobResult, envs []map[string]string, _ []string) {
			if environmentValue(envs[1], "VALUE", true) != "last" {
				t.Fatal(envs[1])
			}
		}},
		{name: "unix command environment names remain distinct", commandEnv: "VALUE=first\nValue=second\nvalue=last\n", configure: func(plan *JobPlan, options *RunOptions) {
			options.RunnerOS = "Linux"
			plan.Steps = []Step{{Run: "one"}, {Run: "two"}}
		}, check: func(t *testing.T, _ JobResult, envs []map[string]string, _ []string) {
			if envs[1]["VALUE"] != "first" || envs[1]["Value"] != "second" || envs[1]["value"] != "last" {
				t.Fatal(envs[1])
			}
		}},
		{name: "step PATH overrides earlier paths", configure: func(plan *JobPlan, options *RunOptions) {
			options.BaseEnvironment = []string{"PATH=/base"}
			plan.Steps = []Step{{Run: "one"}, {Run: "two", Env: map[string]string{"PATH": "/step"}}}
		}, check: func(t *testing.T, _ JobResult, envs []map[string]string, _ []string) {
			if envs[1]["PATH"] != "/step" {
				t.Fatal(envs)
			}
		}},
		{name: "too many names", wantError: true, configure: func(plan *JobPlan, _ *RunOptions) {
			plan.Env = map[string]string{}
			for index := range 201 {
				plan.Env[fmt.Sprintf("V%d", index)] = "x"
			}
		}},
		{name: "value bound", wantError: true, configure: func(plan *JobPlan, _ *RunOptions) {
			plan.Env = map[string]string{"VALUE": strings.Repeat("x", maxEnvironmentValue+1)}
		}},
		{name: "total bound", wantError: true, configure: func(plan *JobPlan, _ *RunOptions) {
			plan.Env = map[string]string{}
			for index := range 6 {
				plan.Env[fmt.Sprintf("V%d", index)] = strings.Repeat("x", maxEnvironmentValue)
			}
		}},
		{name: "windows character bound", wantError: true, configure: func(plan *JobPlan, options *RunOptions) {
			options.RunnerOS = "Windows"
			plan.Env = map[string]string{"VALUE": strings.Repeat("x", 32768)}
		}},
		{name: "NUL refused", wantError: true, configure: func(plan *JobPlan, _ *RunOptions) { plan.Env = map[string]string{"VALUE": "bad\x00value"} }},
		{name: "invalid UTF-8 refused", wantError: true, configure: func(plan *JobPlan, _ *RunOptions) { plan.Env = map[string]string{"VALUE": "bad\xffvalue"} }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			plan, options := jobFixture(t)
			plan.Steps = []Step{{Run: "one"}}
			test.configure(&plan, &options)
			var envs []map[string]string
			var codes []string
			options.RunScript = func(_ context.Context, script Script) ScriptResult {
				values := environmentMap(script.Environment, options.RunnerOS == "Windows")
				envs = append(envs, values)
				code, err := os.ReadFile(script.Path)
				if err != nil {
					t.Fatal(err)
				}
				codes = append(codes, string(code))
				if len(envs) == 1 {
					commandEnv := test.commandEnv
					if commandEnv == "" {
						commandEnv = "VALUE=command\nGITHUB_RUN_ID=forbidden\n"
					}
					for name, value := range map[string]string{"GITHUB_ENV": commandEnv, "GITHUB_PATH": "/old\n/new\n", "GITHUB_OUTPUT": "token=" + testSecret + "\n"} {
						if err := os.WriteFile(values[name], []byte(value), 0o600); err != nil {
							t.Fatal(err)
						}
					}
				}
				event, err := os.ReadFile(values["GITHUB_EVENT_PATH"])
				if err != nil || string(event) != "{}" {
					t.Fatal(string(event), err)
				}
				if filepath.Dir(script.Path) != script.ScriptsDirectory || filepath.Dir(values["GITHUB_ENV"]) != script.FilesDirectory {
					t.Fatal(script)
				}
				return ScriptResult{Status: StatusPassed}
			}
			job := RunJob(context.Background(), plan, options)
			want := StatusPassed
			if test.wantError {
				want = StatusError
			}
			if job.Status != want || test.wantError && len(envs) != 0 {
				t.Fatal(job, envs)
			}
			if test.check != nil {
				test.check(t, job, envs, codes)
			}
		})
	}
}
