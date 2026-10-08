package host

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"owngit/internal/actions"
	"owngit/internal/checkexec"
)

func TestHostRunJob(t *testing.T) {
	windows := runtime.GOOS == "windows"
	defaultCode := "printf 'HOST_OK\\n'"
	localShell, localArguments := "sh", " -e {0}"
	if windows {
		defaultCode = "Write-Output 'HOST_OK'"
		localShell, localArguments = "powershell.exe", " -ExecutionPolicy Bypass -File {0}"
	}
	tests := []struct {
		name           string
		shell          string
		requires       string
		runsOn         []string
		code           string
		want           string
		output         string
		working        string
		localTool      bool
		relativePath   bool
		exitCode       int
		env            map[string]string
		replaceScripts bool
		check          func(*testing.T, actions.Script)
		configure      func(*actions.RunOptions)
	}{
		{name: "default", code: defaultCode, want: actions.StatusPassed, output: "HOST_OK"},
		{name: "job-scoped tool state", code: defaultCode, want: actions.StatusPassed, output: "HOST_OK", check: func(t *testing.T, script actions.Script) {
			values := environmentMap(script.Environment, windows)
			names := []string{"HOME", "XDG_CACHE_HOME", "GOCACHE", "GOTMPDIR", "TMPDIR", "TEMP", "TMP", "RUNNER_TEMP"}
			if windows {
				names = append(names, "USERPROFILE")
			}
			for _, name := range names {
				value := environmentValue(values, name, windows)
				resolved, err := filepath.EvalSymlinks(value)
				if err != nil {
					t.Fatalf("%s=%q is unavailable: %v", name, value, err)
				}
				relative, err := filepath.Rel(script.WorkDirectory, resolved)
				info, statErr := os.Stat(resolved)
				if err != nil || !filepath.IsLocal(relative) || statErr != nil || !info.IsDir() {
					t.Fatalf("%s=%q is not a directory inside %q", name, value, script.WorkDirectory)
				}
			}
		}},
		{name: "workflow home override", code: defaultCode, want: actions.StatusPassed, output: "HOST_OK", env: map[string]string{"HOME": "workflow-home"}, check: func(t *testing.T, script actions.Script) {
			if value := environmentValue(environmentMap(script.Environment, windows), "HOME", windows); value != "workflow-home" {
				t.Fatal(value)
			}
		}},
		{name: "non Windows label defaults to bash", runsOn: []string{"ubuntu-latest"}, code: "printf 'BASH_DEFAULT_OK\\n'", want: actions.StatusPassed, output: "BASH_DEFAULT_OK"},
		{name: "implicit bash has no pipefail", runsOn: []string{"ubuntu-latest"}, code: "false | true\nprintf 'PIPELINE_DEFAULT_OK\\n'", want: actions.StatusPassed, output: "PIPELINE_DEFAULT_OK"},
		{name: "explicit bash has pipefail", shell: "bash", code: "false | true\nprintf 'SHOULD_NOT_RUN\\n'", want: actions.StatusFailed},
		{name: "sh", shell: "sh", code: "printf 'SH_OK\\n'", want: actions.StatusPassed, output: "SH_OK"},
		{name: "custom shell with quoted placeholder", shell: "sh -e \"{0}\"", code: "printf 'CUSTOM_OK\\n'", want: actions.StatusPassed, output: "CUSTOM_OK"},
		{name: "relative custom executable", shell: "./tools/" + localShell + localArguments, localTool: true, code: defaultCode, want: actions.StatusPassed, output: "HOST_OK"},
		{name: "relative PATH custom executable", shell: localShell + localArguments, localTool: true, relativePath: true, code: defaultCode, want: actions.StatusPassed, output: "HOST_OK"},
		{name: "relative custom executable in step directory", shell: "./tools/" + localShell + localArguments, localTool: true, working: "sub", code: defaultCode, want: actions.StatusPassed, output: "HOST_OK"},
		{name: "relative PATH custom executable in step directory", shell: localShell + localArguments, localTool: true, relativePath: true, working: "sub", code: defaultCode, want: actions.StatusPassed, output: "HOST_OK"},
		{name: "custom powershell template", shell: "powershell -ExecutionPolicy Bypass -File {0}", requires: "powershell", code: "Write-Output 'CUSTOM_POWERSHELL_OK'", want: actions.StatusPassed, output: "CUSTOM_POWERSHELL_OK"},
		{name: "powershell unicode source", shell: "powershell", code: "$expected = [string][char]0xD55C + [char]0xAE00; if ('한글' -cne $expected) { throw 'UNICODE_SOURCE_FAILED' }; Write-Output 'UNICODE_SOURCE_OK'", want: actions.StatusPassed, output: "UNICODE_SOURCE_OK"},
		{name: "powershell", shell: "powershell", code: "Write-Output 'POWERSHELL_OK'", want: actions.StatusPassed, output: "POWERSHELL_OK"},
		{name: "powershell native exit", shell: "powershell", code: "& $env:ComSpec /D /C 'exit 7'", want: actions.StatusFailed, exitCode: 1},
		{name: "powershell errors stop", shell: "powershell", code: "Write-Error 'failed'; Write-Output 'SHOULD_NOT_RUN'", want: actions.StatusFailed},
		{name: "pwsh", shell: "pwsh", code: "Write-Output 'PWSH_OK'", want: actions.StatusPassed, output: "PWSH_OK"},
		{name: "cmd", shell: "cmd", code: "@echo off\r\necho CMD_OK\r\nexit /b 0\r\n", want: actions.StatusPassed, output: "CMD_OK"},
		{name: "cmd ignores workflow ComSpec", shell: "cmd", code: "@echo off\r\necho CMD_OK\r\necho %ComSpec%\r\nexit /b 0\r\n", env: map[string]string{"ComSpec": "owngit-no-such-cmd.exe", "SystemRoot": "owngit-no-such-system"}, want: actions.StatusPassed, output: "CMD_OK"},
		{name: "cmd uses base system directory", shell: "cmd", code: "@echo off\r\necho CMD_SYSTEM_OK\r\nexit /b 0\r\n", env: map[string]string{"PATH": "owngit-no-such-path", "SystemRoot": "owngit-no-such-system"}, want: actions.StatusPassed, output: "CMD_SYSTEM_OK", configure: func(options *actions.RunOptions) {
			options.Environment = func(temporary string) ([]string, string) {
				base, note := checkexec.HostEnvironment(temporary)
				filtered := base[:0]
				for _, entry := range base {
					name, _, _ := strings.Cut(entry, "=")
					if !strings.EqualFold(name, "ComSpec") {
						filtered = append(filtered, entry)
					}
				}
				return filtered, note
			}
		}},
		{name: "powershell scripts remain in job folder", shell: "powershell", code: "Write-Output 'SHOULD_NOT_RUN'", replaceScripts: true, want: actions.StatusError},
		{name: "cmd scripts remain in job folder", shell: "cmd", code: "@echo off\r\necho SHOULD_NOT_RUN\r\n", replaceScripts: true, want: actions.StatusError},
		{name: "cmd exit", shell: "cmd", code: "@echo off\r\nexit /b 3\r\n", want: actions.StatusFailed, exitCode: 3},
		{name: "python", shell: "python", code: "print('PYTHON_OK')\n", want: actions.StatusPassed, output: "PYTHON_OK"},
		{name: "missing shell", shell: "owngit-no-such-shell {0}", code: "unused", want: actions.StatusUnavailable},
		{name: "invalid custom shell", shell: "sh", code: "unused", want: actions.StatusError, configure: func(options *actions.RunOptions) {
			options.RunScript = func(ctx context.Context, script actions.Script) actions.ScriptResult {
				script.Shell = "sh -e"
				return Runner("")(ctx, script)
			}
		}},
		{name: "invalid custom quoting", shell: "sh -e 'broken {0}", code: "unused", want: actions.StatusError},
		{name: "working directory", code: defaultCode, working: "sub", want: actions.StatusPassed, output: "HOST_OK"},
		{name: "working directory escape", code: defaultCode, working: "../outside", want: actions.StatusError},
		{name: "output bound", code: "printf 'abcdefghijklmnopqrstuvwxyz\\n'", runsOn: []string{"ubuntu-latest"}, want: actions.StatusIncomplete, configure: func(options *actions.RunOptions) { options.OutputLimit = 16 }},
		{name: "policy timeout", code: "exec tail -f /dev/null", runsOn: []string{"ubuntu-latest"}, want: actions.StatusIncomplete, configure: func(options *actions.RunOptions) { options.MaxTimeout = 20 * time.Millisecond }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			workspace := filepath.Join(t.TempDir(), "job's & folder", "source")
			if err := os.MkdirAll(filepath.Join(workspace, "sub"), 0o700); err != nil {
				t.Fatal(err)
			}
			code := test.code
			if test.name == "working directory" {
				if windows {
					code += "; Set-Content -Path marker.txt -Value 'WORKING_OK'"
				} else {
					code += "; printf 'WORKING_OK' > marker.txt"
				}
			}
			plan := actions.JobPlan{JobKey: "build", RunsOn: test.runsOn, Steps: []actions.Step{{Name: "step", Run: code, Shell: test.shell, WorkingDirectory: test.working}}, Context: actions.PlanContext{GitHub: actions.GitHubContext{Repository: "sample", EventName: "push", Ref: "refs/heads/main"}}}
			plan.WorkflowEnv = test.env
			options := actions.RunOptions{Workspace: workspace, Identity: actions.RunIdentity{ID: "run-17", Number: 17, Attempt: 1}, MaxTimeout: 5 * time.Second}
			base, _ := checkexec.HostEnvironment(workspace)
			values := environmentMap(base, windows)
			stepDirectory := filepath.Join(workspace, test.working)
			if test.localTool {
				executable := findExecutable(localShell, stepDirectory, values)
				if executable == "" {
					t.Fatalf("fixture shell %s is unavailable", localShell)
				}
				tools := filepath.Join(stepDirectory, "tools")
				if err := os.Mkdir(tools, 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(executable, filepath.Join(tools, localShell)); err != nil {
					t.Fatal(err)
				}
				if test.relativePath {
					plan.Env = map[string]string{"PATH": "tools"}
				}
			}
			want := test.want
			if test.shell == "python" && !windows && findExecutable("python", stepDirectory, values) == "" {
				if python := findExecutable("python3", stepDirectory, values); python != "" {
					tools := filepath.Join(t.TempDir(), "tools")
					if err := os.Mkdir(tools, 0o700); err != nil {
						t.Fatal(err)
					}
					// Apple's python3 launcher selects its tool by executable name.
					wrapper := "#!/bin/sh\nexec '" + strings.ReplaceAll(python, "'", "'\\''") + "' \"$@\"\n"
					if err := os.WriteFile(filepath.Join(tools, "python"), []byte(wrapper), 0o700); err != nil {
						t.Fatal(err)
					}
					path := tools + string(filepath.ListSeparator) + values["PATH"]
					options.Environment = func(temporary string) ([]string, string) {
						base, note := checkexec.HostEnvironment(temporary)
						return append(base, "PATH="+path), note
					}
					values["PATH"] = path
				}
			}
			if !test.replaceScripts && (test.shell == "sh" && windows && test.configure == nil || test.shell == "cmd" && !windows || (test.shell == "python" || test.shell == "powershell" || test.shell == "pwsh") && findExecutable(test.shell, stepDirectory, values) == "") {
				want = actions.StatusUnavailable
			}
			if test.requires != "" && findExecutable(test.requires, stepDirectory, values) == "" {
				want = actions.StatusUnavailable
			}
			if test.configure != nil {
				test.configure(&options)
			}
			if test.check != nil || test.replaceScripts {
				options.RunScript = func(ctx context.Context, script actions.Script) actions.ScriptResult {
					if test.check != nil {
						test.check(t, script)
					}
					outside := ""
					if test.replaceScripts {
						outside = t.TempDir()
						code, err := os.ReadFile(script.Path)
						if err != nil {
							t.Fatal(err)
						}
						if err := os.WriteFile(filepath.Join(outside, filepath.Base(script.Path)), code, 0o600); err != nil {
							t.Fatal(err)
						}
						if err := os.Rename(script.ScriptsDirectory, script.ScriptsDirectory+"-kept"); err != nil {
							t.Fatal(err)
						}
						if err := os.Symlink(outside, script.ScriptsDirectory); err != nil {
							t.Fatal(err)
						}
					}
					result := Runner("")(ctx, script)
					if test.replaceScripts {
						for _, extension := range []string{".ps1", ".cmd"} {
							if _, err := os.Stat(filepath.Join(outside, filepath.Base(script.Path)+extension)); !os.IsNotExist(err) {
								t.Fatalf("script preparation wrote outside the job folder: %v", err)
							}
						}
						if result.Status != actions.StatusError || !strings.Contains(result.Output, "path escapes") {
							t.Fatal(result)
						}
					}
					return result
				}
			}
			job := RunJob(context.Background(), plan, options, "")
			if job.Status != want || len(job.Steps) != 1 {
				t.Fatalf("got %+v, want %s", job, want)
			}
			step := job.Steps[0]
			if want == actions.StatusPassed && !strings.Contains(step.Output, test.output) || strings.Contains(step.Output, "SHOULD_NOT_RUN") || step.CleanupError != "" {
				t.Fatal(job)
			}
			if test.exitCode != 0 && want != actions.StatusUnavailable && (step.ExitCode == nil || *step.ExitCode != test.exitCode) {
				t.Fatal(job)
			}
			if test.name == "output bound" && !step.Truncated {
				t.Fatal(job)
			}
			if test.name == "policy timeout" && (len(step.Notes) == 0 || !strings.Contains(step.Notes[0].Detail, "max_timeout_ms (20 ms)")) {
				t.Fatal(job)
			}
			if test.name == "working directory" {
				marker, err := os.ReadFile(filepath.Join(workspace, "sub", "marker.txt"))
				if err != nil || strings.TrimSpace(string(marker)) != "WORKING_OK" {
					t.Fatal(string(marker), err)
				}
			}
		})
	}
}

func TestHostWorkflowCommands(t *testing.T) {
	for _, test := range []struct {
		name      string
		runsOn    []string
		shell     string
		mixedCase bool
	}{
		{name: "native default"}, {name: "Git style bash", runsOn: []string{"ubuntu-latest"}, shell: "bash"},
		{name: "command environment assignment case", mixedCase: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			workspace := filepath.Join(t.TempDir(), "source")
			if err := os.Mkdir(workspace, 0o700); err != nil {
				t.Fatal(err)
			}
			first := "printf '%s\\n' \"$SECRET\"; printf '::add-mask::dynamic-value\\n'; printf 'dynamic-value\\n'; printf 'VALUE=from-command\\n' >> \"$GITHUB_ENV\"; printf 'token=%s\\n' \"$SECRET\" >> \"$GITHUB_OUTPUT\"; printf 'ignored summary\\n' >> \"$GITHUB_STEP_SUMMARY\""
			second := "test \"$VALUE\" = from-command; test \"$GITHUB_ACTIONS\" = true; test \"$GITHUB_RUN_ID\" = run-17; printf 'COMMANDS_OK\\n'"
			if runtime.GOOS == "windows" && test.shell == "" {
				first = "Write-Output $env:SECRET; Write-Output '::add-mask::dynamic-value'; Write-Output 'dynamic-value'; Add-Content -Path $env:GITHUB_ENV -Value 'VALUE=from-command'; Add-Content -Path $env:GITHUB_OUTPUT -Value ('token='+$env:SECRET); Add-Content -Path $env:GITHUB_STEP_SUMMARY -Value 'ignored summary'"
				second = "if ($env:VALUE -ne 'from-command' -or $env:GITHUB_ACTIONS -ne 'true' -or $env:GITHUB_RUN_ID -ne 'run-17') { throw 'environment failed' }; Write-Output 'COMMANDS_OK'"
			}
			if test.mixedCase {
				if runtime.GOOS == "windows" {
					first += "; Add-Content -Path $env:GITHUB_ENV -Value 'VALUE=first','Value=second','value=last'"
					second = "if ($env:VALUE -ne 'last') { throw 'last assignment lost' }; Write-Output 'COMMANDS_OK'"
				} else {
					first += "; printf 'VALUE=first\\nValue=second\\nvalue=last\\n' >> \"$GITHUB_ENV\""
					second = "test \"$VALUE\" = first; test \"$Value\" = second; test \"$value\" = last; printf 'COMMANDS_OK\\n'"
				}
			}
			plan := actions.JobPlan{JobKey: "build", RunsOn: test.runsOn, SecretNames: []string{"TEST"}, Env: map[string]string{"SECRET": "${{ secrets.TEST }}"}, Steps: []actions.Step{{ID: "first", Name: "${{ secrets.TEST }}", Run: first, Shell: test.shell}, {Run: second, Shell: test.shell}}}
			options := actions.RunOptions{Workspace: workspace, Identity: actions.RunIdentity{ID: "run-17", Number: 17, Attempt: 1}, Secrets: map[string]string{"TEST": "synthetic-secret-value"}, MaxTimeout: 5 * time.Second,
				Evaluator: func(_ string, text string, contexts map[string]any) (any, error) {
					if text == "${{ secrets.TEST }}" {
						return contexts["secrets"].(map[string]any)["TEST"], nil
					}
					return text, nil
				}}
			job := RunJob(context.Background(), plan, options, "")
			if job.Status != actions.StatusPassed || !strings.Contains(job.Steps[1].Output, "COMMANDS_OK") || job.Steps[0].Outputs["token"] != "[redacted]" || job.Steps[0].DisplayName != "[redacted]" {
				t.Fatal(job)
			}
			for _, step := range job.Steps {
				if strings.Contains(step.Output+step.Excerpt, "synthetic-secret-value") || strings.Contains(step.Output, "dynamic-value") {
					t.Fatal(job)
				}
			}
			if len(job.Steps[0].Notes) != 1 || job.Steps[0].Notes[0].Code != "note.summary" {
				t.Fatal(job)
			}
		})
	}
}
