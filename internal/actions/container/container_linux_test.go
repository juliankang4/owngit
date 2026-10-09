package container

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"owngit/internal/actions"
	"owngit/internal/checkrun"
	"owngit/internal/testfixture/containerjob"
)

func TestContainerRunJob(t *testing.T) {
	const secret = "synthetic-container-secret"
	stateCode := `set -e
for directory in "$HOME" "$XDG_CACHE_HOME" "$GOCACHE" "$GOTMPDIR" "$TMPDIR" "$TEMP" "$TMP" "$RUNNER_TEMP"; do
  case "$directory" in /owngit/work/*) test -d "$directory";; *) exit 9;; esac
done
test "$GITHUB_WORKSPACE" = /workspace
test "$GITHUB_EVENT_PATH" = /owngit/scripts/event.json
test -r "$GITHUB_EVENT_PATH"
test "$GITHUB_ENV" = /owngit/files/GITHUB_ENV
test "$RUNNER_OS" = Linux
printf 'STATE_OK\n'`
	installCode := `set -e
mkdir -p "$HOME/bin"
printf '#!/bin/sh\nprintf "JOB_TOOL_OK\\n"\n' > "$HOME/bin/job-tool"
chmod +x "$HOME/bin/job-tool"
printf '%s\n' "$HOME/bin" >> "$GITHUB_PATH"
printf 'VALUE<<END\nfirst line\nsecond line\nEND\n' >> "$GITHUB_ENV"
printf 'ready=yes\n' >> "$GITHUB_OUTPUT"
printf 'ignored summary\n' >> "$GITHUB_STEP_SUMMARY"
printf 'old step\n' > /owngit/files/previous-step
printf 'source state\n' > /workspace/source-state`
	tests := []struct {
		name, shell, code, working, want, output        string
		steps                                           []actions.Step
		env                                             map[string]string
		statuses                                        []string
		transport, cancel, invalidPayload, lookupMounts bool
		limit                                           int64
		timeout                                         time.Duration
		lookupChecks, exitCode                          int
	}{
		{name: "default shell", code: "false | true\nprintf 'DEFAULT_OK:%s\\n' \"${BASH_VERSION:-sh}\"", want: actions.StatusPassed, output: "DEFAULT_OK"},
		{name: "explicit sh and job state", shell: "sh", code: stateCode, want: actions.StatusPassed, output: "STATE_OK"},
		{name: "shell template", shell: "sh -e \"{0}\"", code: "printf 'CUSTOM_OK\\n'", want: actions.StatusPassed, output: "CUSTOM_OK"},
		{name: "working directory", working: "sub", code: "test \"$PWD\" = /workspace/sub; printf 'DIRECTORY_OK\\n'", want: actions.StatusPassed, output: "DIRECTORY_OK"},
		{name: "private working directory", working: "${{ secrets.TOKEN }}", transport: true, env: map[string]string{"TOKEN": "${{ secrets.TOKEN }}"}, code: "test \"$PWD\" = \"/workspace/$TOKEN\"; printf '%s\\nDIRECTORY_OK\\n' \"$PWD\"", want: actions.StatusPassed, output: "DIRECTORY_OK"},
		{name: "private relative interpreter", working: "${{ secrets.TOKEN }}", shell: "./" + secret + " {0}", transport: true, lookupChecks: 1, env: map[string]string{"TOKEN": "${{ secrets.TOKEN }}"}, code: "printf '%s\\nLOOKUP_OK\\n' \"$TOKEN\"", want: actions.StatusPassed, output: "LOOKUP_OK"},
		{name: "private missing interpreter", working: "${{ secrets.TOKEN }}", shell: secret + "-missing {0}", transport: true, lookupChecks: 1, exitCode: 127, code: "touch /workspace/should-not-run", want: actions.StatusUnavailable, output: "workflow.shell_unavailable"},
		{name: "cached interpreter", transport: true, lookupChecks: 1, env: map[string]string{"TOKEN": "${{ secrets.TOKEN }}"}, want: actions.StatusPassed, output: "CACHE_OK", steps: []actions.Step{
			{Shell: "sh", Run: "true"},
			{Shell: "sh", Run: "printf '%s\\nCACHE_OK\\n' \"$TOKEN\""},
		}},
		{name: "missing interpreter installed later", transport: true, lookupChecks: 3, env: map[string]string{"TOKEN": "${{ secrets.TOKEN }}"}, want: actions.StatusUnavailable, output: "INSTALLED_INTERPRETER_OK", statuses: []string{actions.StatusUnavailable, actions.StatusPassed, actions.StatusPassed}, steps: []actions.Step{
			{Shell: "cache-tool {0}", Run: "touch /workspace/should-not-run"},
			{If: "always()", Shell: "sh", Run: `set -e
mkdir -p "$HOME/.local/bin"
printf '#!/bin/sh\nexec /bin/sh -e "$@"\n' > "$HOME/.local/bin/cache-tool"
chmod +x "$HOME/.local/bin/cache-tool"
command -v cache-tool
printf 'INSTALLED_OK\n'`},
			{If: "always()", Shell: "cache-tool {0}", Run: "printf '%s\\nINSTALLED_INTERPRETER_OK\\n' \"$TOKEN\""},
		}},
		{name: "lookup mount restrictions", shell: "sh", transport: true, lookupMounts: true, lookupChecks: 1, env: map[string]string{"TOKEN": "${{ secrets.TOKEN }}"}, code: "set -e; test ! -e /owngit/scripts/step-1.lookup; printf '%s\\nLOOKUP_MOUNTS_OK\\n' \"$TOKEN\"", want: actions.StatusPassed, output: "LOOKUP_MOUNTS_OK"},
		{name: "interpreter directory changes", transport: true, lookupChecks: 2, env: map[string]string{"TOKEN": "${{ secrets.TOKEN }}"}, want: actions.StatusPassed, output: "CACHE_OK", steps: []actions.Step{
			{Shell: "sh", Run: "true"},
			{Shell: "sh", WorkingDirectory: "sub", Run: "test \"$PWD\" = /workspace/sub; printf '%s\\nCACHE_OK\\n' \"$TOKEN\""},
		}},
		{name: "interpreter PATH changes", transport: true, lookupChecks: 2, env: map[string]string{"TOKEN": "${{ secrets.TOKEN }}"}, want: actions.StatusPassed, output: "CACHE_OK", steps: []actions.Step{
			{Shell: "sh", Run: "true"},
			{Shell: "sh", Env: map[string]string{"PATH": "/workspace/" + secret + "/bin:/bin"}, Run: "true"},
			{Shell: "sh", Env: map[string]string{"PATH": "/workspace/" + secret + "/bin:/bin"}, Run: "printf '%s\\nCACHE_OK\\n' \"$TOKEN\""},
		}},
		{name: "working directory escape", working: "../outside", code: "exit 0", want: actions.StatusError},
		{name: "before grant refused", code: "printf 'SHOULD_NOT_RUN\\n'", want: actions.StatusError, output: "start grant"},
		{name: "home override", env: map[string]string{"HOME": "/owngit/work/temp"}, code: "test \"$HOME\" = /owngit/work/temp; printf 'HOME_OK\\n'", want: actions.StatusPassed, output: "HOME_OK"},
		{name: "mount restrictions", code: `set -e
test "$(stat -c %a /owngit/scripts/step-1.env)" = 600
test ! -e /owngit/scripts/step-1.lookup
if touch /owngit/scripts/forbidden 2>/dev/null; then exit 9; fi
if touch /readonly-root-probe 2>/dev/null; then exit 9; fi
test ! -e /owngit/envelope-owner
test ! -e /workspace/../envelope-owner
printf 'MOUNTS_OK\n'`, want: actions.StatusPassed, output: "MOUNTS_OK"},
		{name: "command files and persistent tool", want: actions.StatusPassed, output: "JOB_TOOL_OK", steps: []actions.Step{
			{ID: "install", Run: installCode},
			{Run: "set -e\ntest \"$VALUE\" = 'first line\nsecond line'\ntest '${{ steps.install.outputs.ready }}' = yes\ntest ! -e /owngit/files/previous-step\ntest -f /workspace/source-state\njob-tool"},
		}},
		{name: "user installed tool persists", want: actions.StatusPassed, output: "USER_TOOL_OK", steps: []actions.Step{
			{Run: `set -e
mkdir -p "$HOME/.local/bin"
printf '#!/bin/sh\nprintf "USER_TOOL_OK\\n"\n' > "$HOME/.local/bin/user-tool"
chmod +x "$HOME/.local/bin/user-tool"`},
			{Run: "user-tool"},
		}},
		{name: "private multiline transport", transport: true, shell: "sh -e {0} '" + secret + "'", env: map[string]string{
			"PAYLOAD_MARKER": secret, "TOKEN": "${{ secrets.TOKEN }}", "QUOTED": "one'\"$(touch /owngit/work/injected)\nsecond line",
			"DOCKER_HOST": "tcp://untrusted.invalid:1", "DOCKER_CONFIG": "/owngit/work/untrusted", "DOCKER_AUTH_CONFIG": secret,
		}, code: "set -e\ntest \"$1\" = \"$TOKEN\"\ntest \"$QUOTED\" = 'one'\\''\"$(touch /owngit/work/injected)\nsecond line'\ntest ! -e /owngit/work/injected\nprintf '%s\\n' \"$TOKEN\"\nprintf 'TRANSPORT_OK\\n'", want: actions.StatusPassed, output: "TRANSPORT_OK"},
		{name: "container expression paths", code: "test '${{ github.workspace }}' = /workspace\ntest '${{ runner.temp }}' = /owngit/work/temp\ntest '${{ env.GITHUB_ENV }}' = /owngit/files/GITHUB_ENV\nprintf 'CONTEXT_OK\\n'", want: actions.StatusPassed, output: "CONTEXT_OK"},
		{name: "linked command file", code: "mv \"$GITHUB_ENV\" \"$GITHUB_ENV.saved\"; ln -s /workspace/poison \"$GITHUB_ENV\"", want: actions.StatusError},
		{name: "fifo command file", code: "mv \"$GITHUB_ENV\" \"$GITHUB_ENV.saved\"; mkfifo \"$GITHUB_ENV\"", want: actions.StatusError},
		{name: "oversized command file", code: "dd if=/dev/zero of=\"$GITHUB_ENV\" bs=65537 count=1 2>/dev/null", want: actions.StatusError},
		{name: "missing shell", shell: "owngit-no-such-shell {0}", code: "exit 0", want: actions.StatusUnavailable, exitCode: 127, output: "workflow.shell_unavailable"},
		{name: "cmd unavailable", shell: "cmd", code: "exit 0", want: actions.StatusUnavailable},
		{name: "invalid template", shell: "sh -e", code: "exit 0", want: actions.StatusError},
		{name: "invalid quoting", shell: "sh -e 'broken {0}", code: "exit 0", want: actions.StatusError},
		{name: "invalid adapter environment", invalidPayload: true, code: "exit 0", want: actions.StatusError},
		{name: "script exit 126", code: "exit 126", want: actions.StatusFailed, exitCode: 126},
		{name: "script exit 127", code: "exit 127", want: actions.StatusFailed, exitCode: 127},
		{name: "script prints unavailable prefix", code: "printf 'workflow.shell_unavailable: synthetic\\n'; exit 127", want: actions.StatusFailed, exitCode: 127, output: "workflow.shell_unavailable: synthetic"},
		{name: "output limit", code: "printf 'abcdefghijklmnopqrstuvwxyz\\n'", limit: 16, want: actions.StatusIncomplete},
		{name: "policy timeout", code: "exec tail -f /dev/null", timeout: 200 * time.Millisecond, want: actions.StatusIncomplete},
		{name: "cancel running container", cancel: true, code: "printf 'CANCEL_READY\\n'; exec tail -f /dev/null", want: actions.StatusCancelled, output: "CANCEL_READY"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := containerjob.NewContainerJob(t)
			if test.working == "${{ secrets.TOKEN }}" {
				if err := os.Mkdir(filepath.Join(fixture.Workspace, secret), 0o700); err != nil {
					t.Fatal(err)
				}
			}
			if test.name == "private relative interpreter" {
				if err := os.WriteFile(filepath.Join(fixture.Workspace, secret, secret), []byte("#!/bin/sh\nexec /bin/sh -e \"$@\"\n"), 0o700); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.WriteFile(filepath.Join(fixture.Workspace, "poison"), []byte("INJECTED=yes\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(filepath.Dir(fixture.Workspace), "envelope-owner"), []byte("not a mount\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			coordinator := &checkrun.Coordinator{Store: fixture.Store, DockerPath: fixture.Docker}
			trace := filepath.Join(t.TempDir(), "control")
			if test.transport {
				wrapper := filepath.Join(t.TempDir(), "docker-control")
				code := "#!/bin/sh\n[ -z \"${PAYLOAD_MARKER-}\" ] || exit 88\n[ -z \"${TOKEN-}\" ] || exit 88\ncase \"$PATH\" in *synthetic-container-secret*) exit 88;; esac\nprintf '%s\\n' \"$@\" >> " + shellQuote(trace+".args") + "\ncreating=0\nfor value do [ \"$value\" != create ] || creating=1; done\n"
				code += "if [ \"$creating\" = 1 ]; then\nlookup_source=\nfor file in " + shellQuote(filepath.Join(filepath.Dir(fixture.Workspace), "actions", "scripts")) + "/*/*.lookup.env; do\n[ -f \"$file\" ] || continue\nlookup_source=\"${file%/*}\"\n[ \"$(stat -c %a \"$file\")\" = 600 ] || exit 88\n[ \"$(stat -c %a \"$lookup_source\")\" = 700 ] || exit 88\n[ \"$(grep -c '^export ' \"$file\")\" = 1 ] || exit 88\ngrep -q '^export PATH=' \"$file\" || exit 88\nprintf 'lookup\\n' >> " + shellQuote(trace+".lookups") + "\ndone\n"
				code += "id=$(" + shellQuote(fixture.Docker) + " \"$@\") || exit $?\n"
				if test.lookupMounts {
					code += "if [ -n \"$lookup_source\" ]; then\n" + shellQuote(fixture.Docker) + " inspect --format '{{json .Mounts}}' \"$id\" >> " + shellQuote(trace+".mounts") + " || exit $?\nfi\n"
				}
				code += shellQuote(fixture.Docker) + " inspect --format '{{.Config.WorkingDir}}' \"$id\" >> " + shellQuote(trace+".directories") + " || exit $?\nprintf '%s\\n' \"$id\"\nelse exec " + shellQuote(fixture.Docker) + " \"$@\"; fi\n"
				if err := os.WriteFile(wrapper, []byte(code), 0o700); err != nil {
					t.Fatal(err)
				}
				coordinator.DockerPath = wrapper
			}
			ctx, stop := context.WithTimeout(context.Background(), time.Minute)
			defer stop()
			executor, err := coordinator.PrepareActionsContainer(ctx, fixture.Job, fixture.Workspace)
			if err != nil {
				t.Fatal(err)
			}
			if test.name != "before grant refused" {
				fixture.Start(t)
			}
			steps := test.steps
			if steps == nil {
				steps = []actions.Step{{ID: "step", Shell: test.shell, Run: test.code, WorkingDirectory: test.working}}
			}
			plan := actions.JobPlan{JobKey: "build", WorkflowEnv: test.env, Steps: steps, SecretNames: []string{"TOKEN"}, Context: actions.PlanContext{GitHub: actions.GitHubContext{Repository: "synthetic", EventName: "push", Ref: "refs/heads/main"}}}
			options := actions.RunOptions{Workspace: fixture.Workspace, Identity: actions.RunIdentity{ID: "run-17", Number: 17, Attempt: 1}, MaxTimeout: time.Minute, Secrets: map[string]string{"TOKEN": secret}, Evaluator: containerTestEval}
			if test.limit != 0 {
				options.OutputLimit = test.limit
			}
			if test.timeout != 0 {
				options.MaxTimeout = test.timeout
			}
			if test.cancel || test.invalidPayload {
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				defer cancel()
				options.RunScript = func(ctx context.Context, script actions.Script) actions.ScriptResult {
					if test.cancel {
						script.Output = &cancelOnOutput{Writer: script.Output, cancel: cancel}
					} else {
						script.Environment = append(script.Environment, "INVALID=payload\x00value")
					}
					return Runner(executor)(ctx, script)
				}
			}
			result := RunJob(ctx, plan, options, executor)
			if result.Status != test.want || len(result.Steps) != len(steps) {
				t.Fatalf("want %s, got %+v", test.want, result)
			}
			last := result.Steps[len(result.Steps)-1]
			if test.name == "default shell" {
				t.Log(strings.TrimSpace(last.Output))
			}
			wantLast := test.want
			for index, status := range test.statuses {
				if result.Steps[index].Status != status {
					t.Fatalf("step %d=%+v want=%s", index+1, result.Steps[index], status)
				}
				wantLast = status
			}
			if last.Status != wantLast || !strings.Contains(last.Output, test.output) {
				t.Fatalf("step=%+v want=%s output=%q", last, wantLast, test.output)
			}
			if test.exitCode != 0 && (last.ExitCode == nil || *last.ExitCode != test.exitCode) {
				t.Fatalf("exit code=%v want=%d", last.ExitCode, test.exitCode)
			}
			if test.transport {
				encoded, err := json.Marshal(result)
				if err != nil || strings.Contains(string(encoded), secret) || !strings.Contains(last.Output, "[redacted]") {
					t.Fatalf("transport masking failed: err=%v", err)
				}
				arguments, err := os.ReadFile(trace + ".args")
				if err != nil || strings.Contains(string(arguments), secret) || strings.Contains(string(arguments), "untrusted.invalid") || strings.Contains(string(arguments), "/owngit/work/untrusted") {
					t.Fatalf("Docker control arguments contain workflow payload or are unreadable: %v", err)
				}
				directories, err := os.ReadFile(trace + ".directories")
				if err != nil || len(strings.Fields(string(directories))) == 0 {
					t.Fatalf("Docker working directory metadata is absent: %v", err)
				}
				for _, directory := range strings.Fields(string(directories)) {
					if directory != "/workspace" {
						t.Fatal("Docker working directory is not fixed")
					}
				}
				if test.lookupChecks != 0 {
					lookups, err := os.ReadFile(trace + ".lookups")
					if err != nil || len(strings.Fields(string(lookups))) != test.lookupChecks {
						t.Fatalf("interpreter checks=%q want=%d err=%v", lookups, test.lookupChecks, err)
					}
				}
			}
			if test.lookupMounts {
				var mounts []struct {
					Source, Destination string
					RW                  bool
				}
				data, err := os.ReadFile(trace + ".mounts")
				if err != nil {
					t.Fatal(err)
				}
				if err := json.Unmarshal(data, &mounts); err != nil {
					t.Fatal(err)
				}
				found := false
				for _, mount := range mounts {
					if mount.Destination == "/owngit/files" {
						t.Fatal("interpreter lookup mounted command files")
					}
					if mount.Destination == "/owngit/scripts" {
						found = true
						if mount.RW || mount.Source != filepath.Join(filepath.Dir(fixture.Workspace), "actions", "scripts", "step-1.lookup") {
							t.Fatal("interpreter lookup did not mount only its read-only folder")
						}
					}
				}
				if !found {
					t.Fatal("interpreter lookup folder was not mounted")
				}
			}
			if _, err := os.Stat(filepath.Join(fixture.Workspace, "should-not-run")); !os.IsNotExist(err) {
				t.Fatalf("missing interpreter ran the script: %v", err)
			}
			if test.timeout != 0 && (len(last.Notes) != 1 || last.Notes[0].Code != "workflow.step_timeout") {
				t.Fatalf("timeout notes=%+v", last.Notes)
			}
			if test.limit != 0 && !last.Truncated {
				t.Fatal("output limit was not reported")
			}
			for _, pattern := range []string{"*.env", "*.lookup"} {
				files, err := filepath.Glob(filepath.Join(filepath.Dir(fixture.Workspace), "actions", "scripts", pattern))
				if err != nil || len(files) != 0 {
					t.Fatalf("private step payloads remain: %v err=%v", files, err)
				}
			}
			owned, err := fixture.Store.ActiveCheckContainers(context.Background(), 100)
			if err != nil || len(owned) != 0 {
				t.Fatalf("containers remain: %+v err=%v", owned, err)
			}
		})
	}
}

func containerTestEval(_ string, text string, contexts map[string]any) (any, error) {
	for source, value := range map[string]string{
		"${{ secrets.TOKEN }}":    contexts["secrets"].(map[string]any)["TOKEN"].(string),
		"${{ github.workspace }}": contexts["github"].(map[string]any)["workspace"].(string),
		"${{ runner.temp }}":      contexts["runner"].(map[string]any)["temp"].(string),
	} {
		text = strings.ReplaceAll(text, source, value)
	}
	if values, ok := contexts["env"].(map[string]string); ok {
		text = strings.ReplaceAll(text, "${{ env.GITHUB_ENV }}", values["GITHUB_ENV"])
	}
	if step, ok := contexts["steps"].(map[string]any)["install"].(map[string]any); ok {
		text = strings.ReplaceAll(text, "${{ steps.install.outputs.ready }}", step["outputs"].(map[string]string)["ready"])
	}
	return text, nil
}

type cancelOnOutput struct {
	io.Writer
	cancel context.CancelFunc
	seen   strings.Builder
}

func (writer *cancelOnOutput) Write(value []byte) (int, error) {
	count, err := writer.Writer.Write(value)
	writer.seen.Write(value)
	if strings.Contains(writer.seen.String(), "CANCEL_READY") {
		writer.cancel()
	}
	return count, err
}
