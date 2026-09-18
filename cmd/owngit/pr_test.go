package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"owngit/internal/auth"
	"owngit/internal/gitexec"
	"owngit/internal/githttp"
	"owngit/internal/prclient"
	"owngit/internal/pullrequest"
	"owngit/internal/repository"
	"owngit/internal/server"
	"owngit/internal/state"
)

func TestPRCommandsUseRemoteJSONAPIAndPrivatePasswordFile(t *testing.T) {
	type observedRequest struct {
		method string
		path   string
		body   map[string]any
	}
	observed := make(chan observedRequest, 8)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		_, password, ok := request.BasicAuth()
		if !ok || password != "shared-password" {
			t.Error("CLI request omitted the shared password")
		}
		var body map[string]any
		if request.Body != nil {
			content, _ := io.ReadAll(request.Body)
			if len(content) != 0 {
				if err := json.Unmarshal(content, &body); err != nil {
					t.Errorf("decode request body: %v", err)
				}
			}
		}
		observed <- observedRequest{method: request.Method, path: request.URL.Path, body: body}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte("{\"ok\":true,\"pull_request\":{\"number\":1}}\n"))
	}))
	defer server.Close()
	passwordFile := filepath.Join(t.TempDir(), "password")
	if err := os.WriteFile(passwordFile, []byte("shared-password\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := state.ProtectPrivatePath(passwordFile, false); err != nil {
		t.Fatal(err)
	}
	remote := []string{"--server", server.URL, "--accept-insecure-http", "--repository", "project", "--password-file", passwordFile}
	tests := []struct {
		name   string
		args   []string
		method string
		path   string
		field  string
	}{
		{"create", append([]string{"create", "--title", "Feature", "--source", "feature", "--target", "main", "--review", "request"}, remote...), http.MethodPost, "/api/v1/repositories/project/pull-requests", "title"},
		{"list", append([]string{"list"}, remote...), http.MethodGet, "/api/v1/repositories/project/pull-requests", ""},
		{"show", append([]string{"show", "--number", "1"}, remote...), http.MethodGet, "/api/v1/repositories/project/pull-requests/1", ""},
		{"review request", append([]string{"review", "request", "--number", "1", "--source-oid", strings.Repeat("a", 40), "--target-oid", strings.Repeat("b", 40)}, remote...), http.MethodPost, "/api/v1/repositories/project/pull-requests/1/review/request", "source_oid"},
		{"review submit", append([]string{"review", "submit", "--number", "1", "--source-oid", strings.Repeat("a", 40), "--target-oid", strings.Repeat("b", 40), "--decision", "approved", "--reviewer", "existing-tool:test"}, remote...), http.MethodPost, "/api/v1/repositories/project/pull-requests/1/review/submit", "reviewer_label"},
		{"review skip", append([]string{"review", "skip", "--number", "1", "--source-oid", strings.Repeat("a", 40), "--target-oid", strings.Repeat("b", 40)}, remote...), http.MethodPost, "/api/v1/repositories/project/pull-requests/1/review/skip", "source_oid"},
		{"merge", append([]string{"merge", "--number", "1", "--source-oid", strings.Repeat("a", 40), "--target-oid", strings.Repeat("b", 40)}, remote...), http.MethodPost, "/api/v1/repositories/project/pull-requests/1/merge", "source_oid"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			output, err := captureStdout(func() error { return prCommand(test.args) })
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(output, `"ok":true`) {
				t.Fatalf("command output=%q", output)
			}
			request := <-observed
			if request.method != test.method || request.path != test.path {
				t.Fatalf("request=%s %s, want %s %s", request.method, request.path, test.method, test.path)
			}
			if test.field != "" {
				if _, exists := request.body[test.field]; !exists {
					t.Fatalf("request body lacks %q: %#v", test.field, request.body)
				}
			} else if len(request.body) != 0 {
				t.Fatalf("GET command sent a body: %#v", request.body)
			}
		})
	}
}

func TestPRCLIEndToEndKeepsPushIndependentAndMergesExactRevisions(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	stateRoot := filepath.Join(root, "state")
	repositoryRoot := filepath.Join(root, "repositories")
	if err := os.Mkdir(repositoryRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	store, err := state.Open(ctx, stateRoot)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	adminHash, err := auth.HashPassword("admin-password")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.CompleteSetup(ctx, repositoryRoot, "open", "", adminHash, true); err != nil {
		t.Fatal(err)
	}
	runner, err := gitexec.New("", filepath.Join(stateRoot, "runtime"))
	if err != nil {
		t.Fatal(err)
	}
	manager := &repository.Manager{Store: store, Git: runner, Locks: gitexec.NewLocks(), Root: repositoryRoot}
	if _, err := manager.Create(ctx, "project", "CLI integration fixture"); err != nil {
		t.Fatal(err)
	}
	gitHandler, err := githttp.New(runner, manager, "", 2)
	if err != nil {
		t.Fatal(err)
	}
	authentication := &auth.Manager{Store: store, SessionLife: time.Hour}
	application := &server.App{
		Store: store, Auth: authentication, Repositories: manager,
		PullRequests: &pullrequest.Service{Store: store, Repositories: manager},
		GitHTTP:      gitHandler, Hosts: server.NewHostPolicy(),
	}
	gitHandler.Authorize = application.AuthorizeGit
	httpServer := httptest.NewServer(application.Handler())
	defer httpServer.Close()

	work := filepath.Join(root, "work")
	runPRGit(t, "", "init", "--initial-branch=main", work)
	runPRGit(t, work, "config", "user.name", "CLI Test")
	runPRGit(t, work, "config", "user.email", "cli-test@example.invalid")
	if err := os.WriteFile(filepath.Join(work, "base.txt"), []byte("base\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runPRGit(t, work, "add", ".")
	runPRGit(t, work, "commit", "-m", "base")
	runPRGit(t, work, "remote", "add", "origin", httpServer.URL+"/git/project.git")
	runPRGit(t, work, "push", "origin", "HEAD:refs/heads/main")
	targetOID := prGitOutput(t, work, "rev-parse", "HEAD")
	runPRGit(t, work, "checkout", "-b", "feature")
	if err := os.WriteFile(filepath.Join(work, "feature.txt"), []byte("feature\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runPRGit(t, work, "add", ".")
	runPRGit(t, work, "commit", "-m", "feature")
	runPRGit(t, work, "push", "origin", "HEAD:refs/heads/feature")
	sourceOID := prGitOutput(t, work, "rev-parse", "HEAD")
	if records, err := store.PullRequests(ctx, "project"); err != nil || len(records) != 0 {
		t.Fatalf("ordinary push created pull requests: records=%v err=%v", records, err)
	}

	remoteFlags := []string{"--server", httpServer.URL, "--accept-insecure-http", "--repository", "project"}
	created := runPRCommandJSON(t, append([]string{
		"create", "--title", "CLI feature", "--source", "feature", "--target", "main", "--review", "request",
	}, remoteFlags...))
	if created.PullRequest == nil || created.PullRequest.Source.OID != sourceOID || created.PullRequest.Target.OID != targetOID {
		t.Fatalf("created pull request=%+v", created.PullRequest)
	}
	number := strconv.FormatInt(created.PullRequest.Number, 10)
	listed := runPRCommandJSON(t, append([]string{"list"}, remoteFlags...))
	if len(listed.Items) != 1 {
		t.Fatalf("CLI list returned %d pull requests", len(listed.Items))
	}
	shown := runPRCommandJSON(t, append([]string{"show", "--number", number}, remoteFlags...))
	if shown.PullRequest == nil || shown.PullRequest.Number != created.PullRequest.Number {
		t.Fatalf("CLI show result=%+v", shown.PullRequest)
	}
	runPRCommandJSON(t, append([]string{
		"review", "request", "--number", number, "--source-oid", sourceOID, "--target-oid", targetOID,
	}, remoteFlags...))
	changed := runPRCommandJSON(t, append([]string{
		"review", "submit", "--number", number, "--source-oid", sourceOID, "--target-oid", targetOID,
		"--decision", "changes_requested", "--reviewer", "existing-tool: cli-test",
	}, remoteFlags...))
	if changed.PullRequest == nil || changed.PullRequest.MergeEligibility.Eligible {
		t.Fatal("changes_requested did not block the CLI pull request")
	}
	runPRCommandJSON(t, append([]string{
		"review", "skip", "--number", number, "--source-oid", sourceOID, "--target-oid", targetOID,
	}, remoteFlags...))
	merged := runPRCommandJSON(t, append([]string{
		"merge", "--number", number, "--source-oid", sourceOID, "--target-oid", targetOID,
	}, remoteFlags...))
	if merged.PullRequest == nil || merged.PullRequest.Merge == nil || merged.PullRequest.Merge.OID != sourceOID {
		t.Fatalf("CLI merge result=%+v", merged.PullRequest)
	}
	repositoryPath, _ := manager.Path("project")
	if got := prGitOutput(t, "", "--git-dir", repositoryPath, "rev-parse", "--verify", "refs/heads/main"); got != sourceOID {
		t.Fatalf("CLI merge target=%s, want %s", got, sourceOID)
	}
}

func TestPRCommandValidatesServerBeforeReadingPasswordFile(t *testing.T) {
	missingPassword := filepath.Join(t.TempDir(), "missing-password")
	err := prCommand([]string{
		"list", "--server", "http://user:secret@example.test", "--accept-insecure-http",
		"--repository", "project", "--password-file", missingPassword,
	})
	if got := commandErrorCode(err); got != "invalid_server" {
		t.Fatalf("credential-bearing server error=%v code=%q, want invalid_server", err, got)
	}
	err = prCommand([]string{
		"list", "--server", "http://example.test", "--repository", "project", "--password-file", missingPassword,
	})
	if got := commandErrorCode(err); got != "insecure_http_confirmation_required" {
		t.Fatalf("unconfirmed HTTP error=%v code=%q, want insecure_http_confirmation_required", err, got)
	}
}

func runPRCommandJSON(t *testing.T, arguments []string) pullrequest.SuccessEnvelope {
	t.Helper()
	output, err := captureStdout(func() error { return prCommand(arguments) })
	if err != nil {
		t.Fatal(err)
	}
	var envelope pullrequest.SuccessEnvelope
	if err := json.Unmarshal([]byte(output), &envelope); err != nil {
		t.Fatalf("decode CLI JSON output: %v\n%s", err, output)
	}
	if !envelope.OK {
		t.Fatalf("CLI result reported ok=false: %s", output)
	}
	return envelope
}

func runPRGit(t *testing.T, directory string, arguments ...string) {
	t.Helper()
	command := exec.Command("git", arguments...)
	command.Dir = directory
	command.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(arguments, " "), err, output)
	}
}

func prGitOutput(t *testing.T, directory string, arguments ...string) string {
	t.Helper()
	command := exec.Command("git", arguments...)
	command.Dir = directory
	command.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	var stderr bytes.Buffer
	command.Stderr = &stderr
	output, err := command.Output()
	if err != nil {
		t.Fatalf("git %s: %v\nstdout:\n%s\nstderr:\n%s", strings.Join(arguments, " "), err, output, stderr.Bytes())
	}
	return strings.TrimSpace(string(output))
}

func commandErrorCode(err error) string {
	var problem *prclient.Error
	if errors.As(err, &problem) {
		return problem.Code
	}
	return ""
}

func TestStructuredPRFailureContainsStableCodeWithoutCause(t *testing.T) {
	problem := &prclient.Error{Code: "invalid_credentials", Message: "The password is invalid.", Cause: errors.New("synthetic secret detail")}
	var output bytes.Buffer
	if !writeStructuredCommandError(&output, problem) {
		t.Fatal("coded PR error was not handled")
	}
	if strings.Contains(output.String(), "synthetic secret detail") {
		t.Fatalf("structured error leaked its internal cause: %s", output.String())
	}
	var decoded struct {
		OK    bool `json:"ok"`
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(output.Bytes(), &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.OK || decoded.Error.Code != "invalid_credentials" {
		t.Fatalf("structured error=%+v", decoded)
	}
}
