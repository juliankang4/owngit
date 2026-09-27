package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"owngit/internal/auth"
	"owngit/internal/checkapi"
	"owngit/internal/checkworkflow"
	"owngit/internal/gitexec"
	"owngit/internal/githttp"
	"owngit/internal/pullrequest"
	"owngit/internal/repository"
	"owngit/internal/server"
	"owngit/internal/state"
)

func TestRunnerLoopbackHost(t *testing.T) {
	for _, host := range []string{"localhost", "LOCALHOST.", "127.0.0.1", "::1"} {
		if !runnerLoopbackHost(host) {
			t.Fatalf("expected loopback host %q", host)
		}
	}
	for _, host := range []string{"example.test", "192.0.2.1", "localhost.example.test", ""} {
		if runnerLoopbackHost(host) {
			t.Fatalf("unexpected loopback host %q", host)
		}
	}
}

func TestDefaultRunnerWorkspaceUsesCacheAndKeepsEarlierOwnedWorkspace(t *testing.T) {
	scratch := t.TempDir()
	temporary := filepath.Join(scratch, "temp")
	home := filepath.Join(scratch, "home")
	for _, directory := range []string{temporary, home} {
		if err := os.Mkdir(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"TMPDIR", "TMP", "TEMP"} {
		t.Setenv(name, temporary)
	}
	t.Setenv("HOME", home)
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	t.Setenv("LocalAppData", filepath.Join(home, "AppData", "Local"))
	cache, err := os.UserCacheDir()
	if err != nil {
		t.Fatal(err)
	}

	chosen, err := defaultRunnerWorkspace("https://git.example.test", "project")
	if err != nil {
		t.Fatal(err)
	}
	name := filepath.Base(chosen)
	if !strings.HasPrefix(name, "owngit-runner-") || chosen != filepath.Join(cache, "owngit", name) {
		t.Fatalf("default runner workspace %s, want a folder in %s", chosen, filepath.Join(cache, "owngit"))
	}
	if other, _ := defaultRunnerWorkspace("https://git.example.test", "other"); other == chosen {
		t.Fatal("two repositories share one default runner workspace")
	}

	// A workspace that an earlier release made in the temporary folder, and
	// that this account owns, stays in use.
	earlier := filepath.Join(os.TempDir(), name)
	if err := os.Mkdir(earlier, 0o700); err != nil {
		t.Fatal(err)
	}
	if kept, err := defaultRunnerWorkspace("https://git.example.test", "project"); err != nil || kept != earlier {
		t.Fatalf("default runner workspace %s err=%v, want the earlier workspace %s", kept, err, earlier)
	}
}

func TestConfiguredCheckCLIEndToEnd(t *testing.T) {
	fixture := newConfiguredCheckCLIFixture(t)
	policyFile := filepath.Join(fixture.root, "policy.json")
	policyInput := checkapi.PolicyInput{
		Executor: state.CheckExecutorExternalRunner, AllowedEvents: []string{checkworkflow.EventPush},
		MaxTimeoutMS: 5_000, MaxOutputLimitBytes: 64 << 10,
		QueueLimit: 4, MaxActiveJobs: 1, MaxLeaseMS: 10_000,
	}
	policyJSON, err := json.Marshal(policyInput)
	noErr(t, err)
	noErr(t, os.WriteFile(policyFile, policyJSON, 0o600))

	set := fixture.runPolicy(t, "set", "--policy-file", policyFile)
	assertConfiguredCheckRuntime(t, set)
	if set.Policy == nil || set.Policy.Executor != state.CheckExecutorExternalRunner || set.Policy.ConsentActive {
		t.Fatalf("set policy executor=%v consent_active=%v", policyExecutor(set.Policy), policyConsent(set.Policy))
	}
	shown := fixture.runPolicy(t, "show")
	assertConfiguredCheckRuntime(t, shown)
	if shown.Policy == nil || shown.Policy.Digest != set.Policy.Digest {
		t.Fatal("check-policy show did not return the stored policy")
	}
	enabled := fixture.runPolicy(t, "enable")
	assertConfiguredCheckRuntime(t, enabled)
	if enabled.Policy == nil || !enabled.Policy.ConsentActive {
		t.Fatal("check-policy enable did not activate consent")
	}
	disabled := fixture.runPolicy(t, "disable")
	assertConfiguredCheckRuntime(t, disabled)
	if disabled.Policy == nil || disabled.Policy.ConsentActive {
		t.Fatal("check-policy disable did not revoke consent")
	}
	fixture.runPolicy(t, "enable")

	tokenFile := filepath.Join(fixture.root, "runner-token")
	issueOutput := configuredCheckCLIOutput(t, func() error {
		return runnerCredentialCommand(fixture.adminArguments("issue",
			"--label", "CLI runner", "--creation-id", strings.Repeat("a", 32), "--token-file", tokenFile))
	})
	var issued struct {
		checkapi.RunnerCredentialResponse
		TokenFileServer string `json:"token_file_server"`
	}
	if err := json.Unmarshal([]byte(issueOutput), &issued); err != nil || issued.Credential == nil {
		t.Fatal("runner-credential issue did not return a credential")
	}
	token, err := os.ReadFile(tokenFile)
	if err != nil || strings.TrimSpace(string(token)) == "" {
		t.Fatalf("runner token file available=%v err=%v", len(token) != 0, err)
	}
	// Like a helper credential, the token file names the server it belongs to.
	serverLine, trimmedToken, found := strings.Cut(strings.TrimSuffix(string(token), "\n"), "\n")
	if !found || serverLine != "owngit-server: "+fixture.httpServer.URL || issued.TokenFileServer != fixture.httpServer.URL ||
		trimmedToken == "" || strings.Contains(trimmedToken, "\n") {
		t.Fatalf("runner token file server line=%q token_file_server=%q token lines ok=%v", serverLine, issued.TokenFileServer, found && !strings.Contains(trimmedToken, "\n"))
	}
	if issued.Token != "" || strings.Contains(issueOutput, trimmedToken) {
		t.Fatal("runner-credential issue exposed its bearer token on stdout")
	}
	listed := fixture.runCredentialList(t)
	if len(listed.Credentials) != 1 || listed.Credentials[0].ID != issued.Credential.ID || listed.Credentials[0].RevokedAt != nil {
		t.Fatalf("active runner credential count=%d id_matches=%v revoked=%v", len(listed.Credentials), len(listed.Credentials) == 1 && listed.Credentials[0].ID == issued.Credential.ID, len(listed.Credentials) == 1 && listed.Credentials[0].RevokedAt != nil)
	}

	job := fixture.admitExternalJob(t, "echo cli-runner-ok")
	workspaceRoot := filepath.Join(fixture.root, "runner-work")
	if err := runnerCommand([]string{
		"--server", fixture.httpServer.URL, "--repository", fixture.repository.ID,
		"--token-file", tokenFile, "--workspace-root", workspaceRoot, "--once",
	}); err == nil {
		t.Fatal("runner accepted loopback HTTP without --accept-insecure-http")
	}
	if stored := fixture.readJob(t, job.ID); stored.Status != state.CheckJobPending {
		t.Fatalf("refused insecure runner changed job status to %s", stored.Status)
	}
	// The same server under another name is another origin: the token file
	// is refused before anything is sent.
	otherName := strings.Replace(fixture.httpServer.URL, "127.0.0.1", "localhost", 1)
	if err := runnerCommand([]string{
		"--server", otherName, "--accept-insecure-http", "--repository", fixture.repository.ID,
		"--token-file", tokenFile, "--workspace-root", workspaceRoot, "--poll", "10ms", "--once",
	}); commandErrorCode(err) != "credential_origin_mismatch" {
		t.Fatalf("runner with a token file for another server: %v", err)
	}
	if stored := fixture.readJob(t, job.ID); stored.Status != state.CheckJobPending {
		t.Fatalf("refused runner changed job status to %s", stored.Status)
	}
	if runtime.GOOS != "windows" {
		// Another account could replace a workspace inside a folder that all
		// users can write, so the runner refuses it before claiming a job.
		shared := filepath.Join(fixture.root, "shared")
		if err := os.Mkdir(shared, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(shared, 0o777); err != nil {
			t.Fatal(err)
		}
		err := runnerCommand([]string{
			"--server", fixture.httpServer.URL, "--accept-insecure-http", "--repository", fixture.repository.ID,
			"--token-file", tokenFile, "--workspace-root", filepath.Join(shared, "work"), "--poll", "10ms", "--once",
		})
		if commandErrorCode(err) != "unsafe_workspace_root" || !strings.Contains(err.Error(), "--workspace-root") || strings.Contains(err.Error(), "\n") {
			t.Fatalf("runner with a replaceable workspace parent: %v", err)
		}
		if _, statErr := os.Lstat(filepath.Join(shared, "work")); !os.IsNotExist(statErr) {
			t.Fatalf("refused runner created its workspace: %v", statErr)
		}
		if stored := fixture.readJob(t, job.ID); stored.Status != state.CheckJobPending {
			t.Fatalf("refused runner changed job status to %s", stored.Status)
		}
	}
	if err := runnerCommand([]string{
		"--server", fixture.httpServer.URL, "--accept-insecure-http", "--repository", fixture.repository.ID,
		"--token-file", tokenFile, "--workspace-root", workspaceRoot, "--poll", "10ms", "--once",
	}); err != nil {
		t.Fatal(err)
	}
	completed := fixture.readJob(t, job.ID)
	if completed.Status != state.CheckJobPassed || completed.AttemptID == "" {
		t.Fatalf("runner result status=%s attempt_registered=%v", completed.Status, completed.AttemptID != "")
	}
	attempt, exists, err := fixture.store.CheckAttemptByID(fixture.ctx, fixture.repository.ID, completed.AttemptID)
	if err != nil || !exists {
		t.Fatalf("runner attempt exists=%v err=%v", exists, err)
	}
	if attempt.RevisionOID != fixture.sourceOID || attempt.Status != state.AttemptPassed || len(attempt.Results) != 1 || !strings.Contains(attempt.Results[0].OutputExcerpt, "cli-runner-ok") {
		t.Fatalf("runner exact result revision_matches=%v status=%s result_count=%d output_matches=%v", attempt.RevisionOID == fixture.sourceOID, attempt.Status, len(attempt.Results), len(attempt.Results) == 1 && strings.Contains(attempt.Results[0].OutputExcerpt, "cli-runner-ok"))
	}

	revokeOutput := configuredCheckCLIOutput(t, func() error {
		return runnerCredentialCommand(fixture.adminArguments("revoke", "--credential", issued.Credential.ID))
	})
	var revoked checkapi.OKResponse
	if err := json.Unmarshal([]byte(revokeOutput), &revoked); err != nil || !revoked.OK {
		t.Fatal("runner-credential revoke did not return success JSON")
	}
	listed = fixture.runCredentialList(t)
	if len(listed.Credentials) != 1 || listed.Credentials[0].RevokedAt == nil {
		t.Fatalf("revoked runner credential list count=%d revoked=%v", len(listed.Credentials), len(listed.Credentials) == 1 && listed.Credentials[0].RevokedAt != nil)
	}
	if err := runnerCommand([]string{
		"--server", fixture.httpServer.URL, "--accept-insecure-http", "--repository", fixture.repository.ID,
		"--token-file", tokenFile, "--workspace-root", filepath.Join(fixture.root, "revoked-runner-work"), "--once",
	}); err == nil {
		t.Fatal("runner accepted a revoked credential")
	}
}

type configuredCheckCLIFixture struct {
	ctx           context.Context
	root          string
	store         *state.Store
	repository    state.Repository
	sourceOID     string
	adminPassword string
	httpServer    *httptest.Server
}

func newConfiguredCheckCLIFixture(t *testing.T) *configuredCheckCLIFixture {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("Git is unavailable")
	}
	ctx := context.Background()
	root := t.TempDir()
	stateRoot := filepath.Join(root, "state")
	repositoryRoot := filepath.Join(root, "repositories")
	noErr(t, os.Mkdir(repositoryRoot, 0o700))
	store, err := state.Open(ctx, stateRoot)
	noErr(t, err)
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Error(err)
		}
	})
	const adminPassword = "synthetic-admin-password"
	adminHash, err := auth.HashPassword(adminPassword)
	noErr(t, err)
	noErr(t, store.CompleteSetup(ctx, repositoryRoot, "open", "", adminHash, true))
	git, err := gitexec.New("", filepath.Join(stateRoot, "runtime"))
	noErr(t, err)
	manager := &repository.Manager{Store: store, Git: git, Locks: gitexec.NewLocks(), Root: repositoryRoot}
	stored, err := manager.Create(ctx, "configured-check-cli", "")
	noErr(t, err)
	repositoryPath, err := manager.Path(stored.ID)
	noErr(t, err)
	work := filepath.Join(root, "source")
	runPRGit(t, "", "init", "--initial-branch=main", work)
	runPRGit(t, work, "config", "user.name", "Configured Check CLI Test")
	runPRGit(t, work, "config", "user.email", "configured-check@example.invalid")
	noErr(t, os.WriteFile(filepath.Join(work, "source.txt"), []byte("exact CLI runner source\n"), 0o600))
	runPRGit(t, work, "add", "source.txt")
	runPRGit(t, work, "commit", "-m", "configured check CLI source")
	runPRGit(t, work, "push", repositoryPath, "HEAD:refs/heads/main")
	sourceOID := prGitOutput(t, work, "rev-parse", "HEAD")

	gitHandler, err := githttp.New(git, manager, "", 2)
	noErr(t, err)
	hosts := server.NewHostPolicy()
	application := &server.App{
		Store: store, Auth: &auth.Manager{Store: store, SessionLife: time.Hour}, Repositories: manager,
		PullRequests: &pullrequest.Service{Store: store, Repositories: manager},
		GitHTTP:      gitHandler, Hosts: hosts,
	}
	gitHandler.Authorize = application.AuthorizeGit
	httpServer := httptest.NewServer(application.Handler())
	t.Cleanup(httpServer.Close)
	adminPasswordFile := filepath.Join(root, "admin-password")
	noErr(t, os.WriteFile(adminPasswordFile, []byte(adminPassword+"\n"), 0o600))
	noErr(t, state.ProtectPrivatePath(adminPasswordFile, false))
	return &configuredCheckCLIFixture{
		ctx: ctx, root: root, store: store, repository: stored, sourceOID: sourceOID,
		adminPassword: adminPasswordFile, httpServer: httpServer,
	}
}

func (fixture *configuredCheckCLIFixture) adminArguments(arguments ...string) []string {
	return append(arguments,
		"--server", fixture.httpServer.URL, "--accept-insecure-http", "--repository", fixture.repository.ID,
		"--password-file", fixture.adminPassword)
}

func (fixture *configuredCheckCLIFixture) runPolicy(t *testing.T, arguments ...string) checkapi.PolicyResponse {
	t.Helper()
	output := configuredCheckCLIOutput(t, func() error { return checkPolicyCommand(fixture.adminArguments(arguments...)) })
	var response checkapi.PolicyResponse
	if err := json.Unmarshal([]byte(output), &response); err != nil || !response.OK {
		t.Fatal("check-policy did not return valid success JSON")
	}
	return response
}

func (fixture *configuredCheckCLIFixture) runCredentialList(t *testing.T) checkapi.RunnerCredentialListResponse {
	t.Helper()
	output := configuredCheckCLIOutput(t, func() error { return runnerCredentialCommand(fixture.adminArguments("list")) })
	var response checkapi.RunnerCredentialListResponse
	if err := json.Unmarshal([]byte(output), &response); err != nil || !response.OK {
		t.Fatal("runner-credential list did not return valid success JSON")
	}
	return response
}

func (fixture *configuredCheckCLIFixture) admitExternalJob(t *testing.T, command string) state.CheckJob {
	t.Helper()
	job, deduped, err := fixture.store.AdmitCheckJob(fixture.ctx, state.CheckJobRequest{
		RepositoryID: fixture.repository.ID, Trigger: checkworkflow.EventPush,
		EventKey: "refs/heads/main@" + fixture.sourceOID, SourceOID: fixture.sourceOID, TriggerRef: "main",
		WorkflowPath: checkworkflow.Path, WorkflowDigest: strings.Repeat("b", 64),
		Checks: []state.CheckDefinition{{Name: "CLI runner", Command: command}},
	}, time.Now().UTC())
	if err != nil || deduped {
		t.Fatalf("admit CLI runner job deduped=%v err=%v", deduped, err)
	}
	return job
}

func (fixture *configuredCheckCLIFixture) readJob(t *testing.T, jobID string) state.CheckJob {
	t.Helper()
	job, exists, err := fixture.store.CheckJob(fixture.ctx, fixture.repository.ID, jobID)
	if err != nil || !exists {
		t.Fatalf("configured check job exists=%v err=%v", exists, err)
	}
	return job
}

func configuredCheckCLIOutput(t *testing.T, command func() error) string {
	t.Helper()
	output, err := captureStdout(command)
	noErr(t, err)
	return output
}

func assertConfiguredCheckRuntime(t *testing.T, response checkapi.PolicyResponse) {
	t.Helper()
	if !response.Runtime.Available || response.Runtime.UnavailableCode != "" || response.Runtime.UnavailableReason != "" {
		t.Fatalf("configured-check runtime available=%v code_present=%v reason_present=%v", response.Runtime.Available, response.Runtime.UnavailableCode != "", response.Runtime.UnavailableReason != "")
	}
}

func policyExecutor(policy *checkapi.Policy) string {
	if policy == nil {
		return ""
	}
	return policy.Executor
}

func policyConsent(policy *checkapi.Policy) bool {
	return policy != nil && policy.ConsentActive
}

// TestCredentialIssuanceReportsItsCompensation covers failed helper and runner
// credential issuance. The final CLI error states why issuance failed and what
// the compensating revoke achieved. It suggests a retry only when a retry can
// help, names the creation identity when the revoke is unconfirmed, and never
// carries the token or the password file's content.
func TestCredentialIssuanceReportsItsCompensation(t *testing.T) {
	const token = "synthetic-issued-token"
	const password = "synthetic-admin-password"
	malformed := func(writer http.ResponseWriter, _, _ string) {
		_, _ = io.WriteString(writer, `{"ok":true}`)
	}
	replacedOutput := func(writer http.ResponseWriter, creationID, output string) {
		if err := os.Rename(output, output+".moved"); err != nil {
			t.Errorf("replace the reserved path: %v", err)
		}
		if err := os.WriteFile(output, []byte("replacement\n"), 0o600); err != nil {
			t.Errorf("write the replacement: %v", err)
		}
		_, _ = fmt.Fprintf(writer, `{"ok":true,"token":%q,"credential":{"id":"%s","repository_id":"project","creation_id":%q}}`, token, strings.Repeat("f", 32), creationID)
	}
	refusal := func(status int, code, message string) func(http.ResponseWriter, string, string) {
		return func(writer http.ResponseWriter, _, _ string) {
			writer.WriteHeader(status)
			_, _ = fmt.Fprintf(writer, `{"ok":false,"error":{"code":%q,"message":%q}}`, code, message)
		}
	}
	const disabled = "Credentials are disabled for this repository."
	const unavailable = "The state store is unavailable."
	for _, command := range []struct {
		name       string
		collection string
		run        func(output string, flags []string) error
	}{
		{"helper", "helper-credentials", func(output string, flags []string) error {
			return helperCredentialCommand(append([]string{"create", "--label", "laptop", "--output", output}, flags...))
		}},
		{"runner", "runner-credentials", func(output string, flags []string) error {
			return runnerCredentialCommand(append([]string{"issue", "--label", "runner", "--token-file", output}, flags...))
		}},
	} {
		for _, test := range []struct {
			name        string
			answer      func(writer http.ResponseWriter, creationID, output string)
			revokeFails bool
			code        string
			reason      string
			outcome     string
			revokes     int
		}{
			{"refusal and confirmed revoke", refusal(http.StatusForbidden, "forbidden", disabled), false,
				"forbidden", disabled, "Any credential this attempt created was revoked. The reserved", 1},
			{"refusal and failed revoke", refusal(http.StatusForbidden, "forbidden", disabled), true,
				"credential_creation_unconfirmed", disabled, "unconfirmed", 1},
			{"server failure and failed revoke", refusal(http.StatusServiceUnavailable, "state_unavailable", unavailable), true,
				"credential_creation_unconfirmed", unavailable, "unconfirmed", 1},
			{"malformed response and confirmed revoke", malformed, false,
				"invalid_response", "did not return", "Any credential this attempt created was revoked. Retry the command.", 1},
			{"malformed response and failed revoke", malformed, true,
				"credential_creation_unconfirmed", "did not return", "unconfirmed", 1},
			{"replaced output and failed revoke", replacedOutput, true,
				"credential_creation_unconfirmed", "output path was replaced", "unconfirmed", 1},
			{"creation conflict", refusal(http.StatusConflict, "creation_conflict", "The creation identity has different content."), false,
				"creation_conflict", "different content", "preserved", 0},
		} {
			t.Run(command.name+" "+test.name, func(t *testing.T) {
				if runtime.GOOS == "windows" && strings.HasPrefix(test.name, "replaced output") {
					t.Skip("Windows denies path replacement while the private handle is open")
				}
				root := t.TempDir()
				passwordFile := filepath.Join(root, "password")
				noErr(t, os.WriteFile(passwordFile, []byte(password+"\n"), 0o600))
				noErr(t, state.ProtectPrivatePath(passwordFile, false))
				output := filepath.Join(root, "token")
				var creationID string
				var revokes []string
				server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
					writer.Header().Set("Content-Type", "application/json")
					if request.Method == http.MethodDelete {
						revokes = append(revokes, request.URL.Path)
						if test.revokeFails {
							writer.WriteHeader(http.StatusServiceUnavailable)
							_, _ = io.WriteString(writer, `{"ok":false,"error":{"code":"state_unavailable","message":"The revoke failed."}}`)
							return
						}
						_, _ = io.WriteString(writer, `{"ok":true}`)
						return
					}
					var input checkapi.CreateCredentialInput
					_ = json.NewDecoder(request.Body).Decode(&input)
					creationID = input.CreationID
					test.answer(writer, creationID, output)
				}))
				defer server.Close()
				stdout, err := captureStdout(func() error {
					return command.run(output, []string{"--server", server.URL, "--accept-insecure-http", "--repository", "project", "--password-file", passwordFile})
				})
				var final bytes.Buffer
				if stdout != "" || !writeStructuredCommandError(&final, err) {
					t.Fatalf("issue stdout=%q err=%v", stdout, err)
				}
				var envelope pullrequest.ErrorEnvelope
				noErr(t, json.Unmarshal(final.Bytes(), &envelope))
				message := envelope.Error.Message
				unconfirmed := test.code == "credential_creation_unconfirmed"
				if envelope.Error.Code != test.code || !strings.Contains(message, test.reason) || !strings.Contains(message, test.outcome) ||
					!strings.Contains(message, "preserved") || strings.Contains(message, creationID) != unconfirmed ||
					strings.Contains(message, "Retry the command") != (test.name == "malformed response and confirmed revoke") {
					t.Fatalf("final error=%s", final.Bytes())
				}
				if strings.Contains(final.String(), token) || strings.Contains(final.String(), password) {
					t.Fatalf("final error exposed a secret: %s", final.Bytes())
				}
				revoke := "/api/v1/repositories/project/" + command.collection + "/by-creation/" + creationID
				if len(revokes) != test.revokes || (test.revokes == 1 && revokes[0] != revoke) {
					t.Fatalf("compensating revokes=%v", revokes)
				}
			})
		}
	}
}
