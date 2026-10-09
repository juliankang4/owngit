package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"owngit/internal/apiclient"
	"owngit/internal/auth"
	"owngit/internal/checkapi"
	"owngit/internal/state"
	"owngit/internal/testfixture"
)

type workflowCLIResponse struct {
	Run  state.ActionsRunEvidence `json:"run"`
	Jobs []struct {
		Job checkapi.Job `json:"job"`
	} `json:"jobs"`
}

func workflowCLIFixture(t *testing.T) (*configuredCheckCLIFixture, []string) {
	t.Helper()
	fixture := newConfiguredCheckCLIFixture(t)
	work := filepath.Join(fixture.root, "source")
	testfixture.WriteWorkflow(t, work, "ci.yml", testfixture.SurfaceWorkflow)
	runPRGit(t, work, "add", ".")
	runPRGit(t, work, "commit", "-m", "synthetic workflow")
	runPRGit(t, work, "push", filepath.Join(fixture.root, "repositories", fixture.repository.ID+".git"), "HEAD:refs/heads/main")
	fixture.sourceOID = prGitOutput(t, work, "rev-parse", "HEAD")
	input := checkapi.PolicyInput{Executor: state.CheckExecutorExternalRunner, AllowedEvents: []string{state.ActionsEventDispatch}, MaxTimeoutMS: 600000, MaxOutputLimitBytes: 65536, QueueLimit: 16, MaxActiveJobs: 1, MaxLeaseMS: 60000, RunWorkflows: new(true)}
	content, err := json.Marshal(input)
	noErr(t, err)
	path := filepath.Join(fixture.root, "policy.json")
	noErr(t, os.WriteFile(path, content, 0o600))
	fixture.runPolicy(t, "set", "--policy-file", path, "--enable")
	return fixture, []string{"--server", fixture.httpServer.URL, "--repository", fixture.repository.ID, "--accept-insecure-http", "--json"}
}

func TestWorkflowCLI(t *testing.T) {
	fixture, remote := workflowCLIFixture(t)
	dispatch := func() string {
		return cliOutput(t, workflowCommand, append([]string{"dispatch", "--path", ".github/workflows/ci.yml", "--expected-oid", fixture.sourceOID, "--input", "enabled=false", "--input", "count=3", "--input", "choice=second"}, remote...)...)
	}
	var admitted workflowCLIResponse
	noErr(t, json.Unmarshal([]byte(dispatch()), &admitted))
	if len(admitted.Jobs) != 2 || admitted.Run.Conclusion != "queued" || admitted.Run.SourceOID != fixture.sourceOID || admitted.Run.InputsJSON != `{"choice":"second","count":3,"enabled":false}` {
		t.Fatalf("dispatch=%+v", admitted)
	}
	value := filepath.Join(fixture.root, "value")
	noErr(t, os.WriteFile(value, []byte("synthetic-private-value\nsecond line"), 0o600))
	noErr(t, state.ProtectPrivatePath(value, false))
	rows := []struct {
		name           string
		command        func([]string) error
		args           []string
		admin          bool
		code, contains string
	}{
		{name: "workflow list", command: workflowCommand, args: []string{"list", "--ref", "main"}, contains: "workflow_dispatch"},
		{name: "workflow show", command: workflowCommand, args: []string{"show", "--path", ".github/workflows/ci.yml"}, contains: fixture.sourceOID},
		{name: "run list", command: workflowRunCommand, args: []string{"list"}, contains: "queued"},
		{name: "run show", command: workflowRunCommand, args: []string{"show", "--run", admitted.Run.ID}, contains: "waiting"},
		{name: "job show", command: workflowRunCommand, args: []string{"show", "--run", admitted.Run.ID, "--job", admitted.Jobs[0].Job.ID}, contains: "echo synthetic"},
		{name: "missing raw log", command: workflowRunCommand, args: []string{"log", "--run", admitted.Run.ID, "--job", admitted.Jobs[0].Job.ID}, code: "check_log_missing"},
		{name: "unknown run", command: workflowRunCommand, args: []string{"show", "--run", strings.Repeat("0", 32)}, code: "workflow.run_not_found"},
		{name: "moved dispatch", command: workflowCommand, args: []string{"dispatch", "--path", ".github/workflows/ci.yml", "--expected-oid", strings.Repeat("a", 40)}, code: "workflow.moved"},
		{name: "unknown input", command: workflowCommand, args: []string{"dispatch", "--path", ".github/workflows/ci.yml", "--input", "unknown=value"}, code: "workflow.dispatch_input"},
		{name: "duplicate input", command: workflowCommand, args: []string{"dispatch", "--path", ".github/workflows/ci.yml", "--input", "enabled=true", "--input", "enabled=false"}, code: "invalid_arguments"},
		{name: "cancel", command: workflowRunCommand, args: []string{"cancel", "--run", admitted.Run.ID}, contains: "cancelled"},
		{name: "rerun", command: workflowRunCommand, args: []string{"rerun", "--run", admitted.Run.ID}, contains: "rerun_generation"},
		{name: "rerun deduplicates", command: workflowRunCommand, args: []string{"rerun", "--run", admitted.Run.ID}, contains: "deduplicated"},
		{name: "secret needs admin file", command: workflowSecretCommand, args: []string{"list"}, code: "invalid_arguments"},
		{name: "secret set file", command: workflowSecretCommand, args: []string{"set", "--name", "TOKEN", "--value-file", value}, admin: true, contains: "updated_at"},
		{name: "secret list", command: workflowSecretCommand, args: []string{"list"}, admin: true, contains: "TOKEN"},
		{name: "secret value is not an argument", command: workflowSecretCommand, args: []string{"set", "--name", "TOKEN", "--value", "synthetic-private-value"}, admin: true, code: "invalid_arguments"},
		{name: "two secret sources refused", command: workflowSecretCommand, args: []string{"set", "--name", "TOKEN", "--value-file", value, "--value-stdin"}, admin: true, code: "invalid_arguments"},
		{name: "secret remove", command: workflowSecretCommand, args: []string{"remove", "--name", "TOKEN"}, admin: true, contains: "ok"},
	}
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			args := append([]string{}, row.args...)
			if row.admin {
				args = fixture.adminArguments(args...)
				args = append(args, "--json")
			} else {
				args = append(args, remote...)
			}
			if row.code != "" {
				err := row.command(args)
				var problem *apiclient.Error
				if !errors.As(err, &problem) || problem.Code != row.code {
					t.Fatalf("error=%v want=%s", err, row.code)
				}
				return
			}
			content := cliOutput(t, row.command, args...)
			if !strings.Contains(content, row.contains) || strings.Contains(content, "synthetic-private-value") || strings.Contains(content, `"value"`) {
				t.Fatalf("output=%s", content)
			}
		})
	}
	t.Run("secret stdin preserves exact value without returning it", func(t *testing.T) {
		input, err := os.Open(value)
		noErr(t, err)
		defer input.Close()
		previous := os.Stdin
		os.Stdin = input
		defer func() { os.Stdin = previous }()
		content := cliOutput(t, workflowSecretCommand, fixture.adminArguments("set", "--name", "TOKEN", "--value-stdin")...)
		if strings.Contains(content, "synthetic-private-value") {
			t.Fatal("secret value returned")
		}
		values, err := fixture.store.ReadWorkflowSecrets(context.Background(), fixture.repository.ID, []string{"TOKEN"})
		noErr(t, err)
		if values["TOKEN"] != "synthetic-private-value\nsecond line" {
			t.Fatal("the exact stdin value was not preserved")
		}
	})
	hash, err := auth.HashPassword("synthetic-shared")
	noErr(t, err)
	noErr(t, fixture.store.SetAccessPassword(context.Background(), hash))
	for _, row := range rows[:6] {
		err := row.command(append(append([]string{}, row.args...), remote...))
		var problem *apiclient.Error
		if !errors.As(err, &problem) || problem.Code != "authentication_required" {
			t.Fatalf("anonymous %s: %v", row.name, err)
		}
	}
}

func TestWorkflowMCP(t *testing.T) {
	fixture, remote := workflowCLIFixture(t)
	session := startMCPSession(t, mcpOptions{server: fixture.httpServer.URL, repository: fixture.repository.ID, acceptInsecureHTTP: true, noRunCheck: true})
	names, schemas := session.toolNames()
	for _, name := range []string{"workflow_list", "workflow_show", "workflow_dispatch", "workflow_run_list", "workflow_run_show", "workflow_job_log", "workflow_run_cancel", "workflow_run_rerun"} {
		if _, found := schemas[name]; !found {
			t.Fatalf("missing tool %s in %v", name, names)
		}
	}
	for _, name := range names {
		if strings.Contains(name, "workflow_secret") || name == "workflow_enable" {
			t.Fatalf("administrator tool offered: %s", name)
		}
	}
	text, isError := session.call("workflow_dispatch", map[string]any{"path": ".github/workflows/ci.yml", "expected_oid": fixture.sourceOID})
	var admitted workflowCLIResponse
	noErr(t, json.Unmarshal([]byte(text), &admitted))
	if isError || admitted.Run.ID == "" {
		t.Fatalf("dispatch=%s error=%v", text, isError)
	}
	rows := []struct {
		tool           string
		arguments      map[string]any
		command        func([]string) error
		args           []string
		code, contains string
	}{
		{tool: "workflow_list", command: workflowCommand, args: []string{"list"}},
		{tool: "workflow_show", arguments: map[string]any{"path": ".github/workflows/ci.yml"}, command: workflowCommand, args: []string{"show", "--path", ".github/workflows/ci.yml"}},
		{tool: "workflow_run_list", command: workflowRunCommand, args: []string{"list"}},
		{tool: "workflow_run_show", arguments: map[string]any{"run": admitted.Run.ID}, command: workflowRunCommand, args: []string{"show", "--run", admitted.Run.ID}},
		{tool: "workflow_run_show", arguments: map[string]any{"run": admitted.Run.ID, "job": admitted.Jobs[0].Job.ID}, command: workflowRunCommand, args: []string{"show", "--run", admitted.Run.ID, "--job", admitted.Jobs[0].Job.ID}},
		{tool: "workflow_job_log", arguments: map[string]any{"run": admitted.Run.ID, "job": admitted.Jobs[0].Job.ID}, code: "check_log_missing"},
		{tool: "workflow_run_show", arguments: map[string]any{"run": strings.Repeat("0", 32)}, code: "workflow.run_not_found"},
		{tool: "workflow_dispatch", arguments: map[string]any{"path": ".github/workflows/ci.yml", "expected_oid": strings.Repeat("a", 40)}, code: "workflow.moved"},
		{tool: "workflow_dispatch", arguments: map[string]any{"path": ".github/workflows/ci.yml", "inputs": map[string]any{"unknown": "value"}}, code: "workflow.dispatch_input"},
		{tool: "workflow_list", arguments: map[string]any{"run": admitted.Run.ID}, code: "invalid_arguments"},
		{tool: "workflow_run_cancel", arguments: map[string]any{"run": admitted.Run.ID}, contains: "cancelled"},
		{tool: "workflow_run_rerun", arguments: map[string]any{"run": admitted.Run.ID}, contains: "rerun_generation"},
		{tool: "workflow_run_rerun", arguments: map[string]any{"run": admitted.Run.ID}, contains: "deduplicated"},
	}
	for _, row := range rows {
		t.Run(row.tool+row.code+row.contains, func(t *testing.T) {
			text, isError := session.call(row.tool, row.arguments)
			if row.code != "" {
				var answer struct {
					Error struct {
						Code string `json:"code"`
					} `json:"error"`
				}
				noErr(t, json.Unmarshal([]byte(text), &answer))
				if !isError || answer.Error.Code != row.code {
					t.Fatalf("result=%s isError=%v", text, isError)
				}
				return
			}
			if isError {
				t.Fatalf("result=%s", text)
			}
			if row.command != nil {
				want := cliOutput(t, row.command, append(append([]string{}, row.args...), remote...)...)
				if text != want {
					t.Fatalf("MCP=%s CLI=%s", text, want)
				}
			} else if !strings.Contains(text, row.contains) {
				t.Fatalf("result=%s", text)
			}
			if strings.Contains(text, `"value"`) {
				t.Fatalf("secret value returned: %s", text)
			}
		})
	}
	hash, err := auth.HashPassword("synthetic-shared")
	noErr(t, err)
	noErr(t, fixture.store.SetAccessPassword(context.Background(), hash))
	for _, row := range rows[:5] {
		if code := session.callError(row.tool, row.arguments); code != "authentication_required" {
			t.Fatalf("anonymous %s=%s", row.tool, code)
		}
	}
	noErr(t, fixture.store.SavePolicies(context.Background(), state.PolicyChange{LoginLimits: &state.LoginLimits{Attempts: 100, Window: time.Minute, Pause: time.Minute}}))
}
