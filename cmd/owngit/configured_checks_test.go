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
	"sync/atomic"
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
	// Save and enable turns checks on for exactly the policy in the file.
	policyInput.QueueLimit = 5
	policyJSON, err = json.Marshal(policyInput)
	noErr(t, err)
	noErr(t, os.WriteFile(policyFile, policyJSON, 0o600))
	savedEnabled := fixture.runPolicy(t, "set", "--policy-file", policyFile, "--enable")
	if savedEnabled.Policy == nil || !savedEnabled.Policy.ConsentActive || savedEnabled.Policy.QueueLimit != 5 || savedEnabled.Policy.Digest == set.Policy.Digest {
		t.Fatalf("check-policy set --enable stored %+v", savedEnabled.Policy)
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
	// creationRevokes counts revoke-by-creation requests the server received.
	creationRevokes atomic.Int64
	// beforeIssue, when set, runs as the server receives a credential POST.
	beforeIssue func()
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

	gitHandler, err := githttp.New(git, manager, "")
	noErr(t, err)
	hosts := server.NewHostPolicy()
	application := &server.App{
		Store: store, Auth: &auth.Manager{Store: store}, Repositories: manager,
		PullRequests: &pullrequest.Service{Store: store, Repositories: manager},
		GitHTTP:      gitHandler, Hosts: hosts,
		Network: server.NewLiveNetwork(server.LiveNetworkConfig{Hosts: hosts}),
	}
	gitHandler.Authorize = application.AuthorizeGit
	adminPasswordFile := filepath.Join(root, "admin-password")
	noErr(t, os.WriteFile(adminPasswordFile, []byte(adminPassword+"\n"), 0o600))
	noErr(t, state.ProtectPrivatePath(adminPasswordFile, false))
	fixture := &configuredCheckCLIFixture{
		ctx: ctx, root: root, store: store, repository: stored, sourceOID: sourceOID, adminPassword: adminPasswordFile,
	}
	handler := application.Handler()
	fixture.httpServer = httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method == http.MethodDelete && strings.Contains(request.URL.Path, "/by-creation/") {
			fixture.creationRevokes.Add(1)
		}
		if request.Method == http.MethodPost && strings.HasSuffix(request.URL.Path, "-credentials") && fixture.beforeIssue != nil {
			fixture.beforeIssue()
		}
		handler.ServeHTTP(writer, request)
	}))
	t.Cleanup(fixture.httpServer.Close)
	return fixture
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
// credential issuance. Only an attempt that may have created a credential is
// revoked by its creation identity: an OwnGit refusal and a replay without a
// token created nothing. The final CLI error states why issuance failed and
// what the compensating revoke achieved, names the creation identity when the
// owner must act, and never carries the token or the password file's content.
func TestCredentialIssuanceReportsItsCompensation(t *testing.T) {
	const token = "synthetic-issued-token"
	const password = "synthetic-admin-password"
	const credentialID = "ffffffffffffffffffffffffffffffff"
	lostResponse := func(writer http.ResponseWriter, _, _ string) {
		connection, _, err := writer.(http.Hijacker).Hijack()
		if err != nil {
			t.Errorf("lose the issuance response: %v", err)
			return
		}
		noErr(t, connection.Close())
	}
	malformed := func(writer http.ResponseWriter, _, _ string) {
		_, _ = io.WriteString(writer, `{"ok":true}`)
	}
	replay := func(writer http.ResponseWriter, creationID, _ string) {
		_, _ = fmt.Fprintf(writer, `{"ok":true,"credential":{"id":%q,"repository_id":"project","creation_id":%q}}`, credentialID, creationID)
	}
	replacedOutput := func(writer http.ResponseWriter, creationID, output string) {
		if err := os.Rename(output, output+".moved"); err != nil {
			t.Errorf("replace the reserved path: %v", err)
		}
		if err := os.WriteFile(output, []byte("replacement\n"), 0o600); err != nil {
			t.Errorf("write the replacement: %v", err)
		}
		_, _ = fmt.Fprintf(writer, `{"ok":true,"token":%q,"credential":{"id":%q,"repository_id":"project","creation_id":%q}}`, token, credentialID, creationID)
	}
	refusal := func(status int, code, message string) func(http.ResponseWriter, string, string) {
		return func(writer http.ResponseWriter, _, _ string) {
			writer.WriteHeader(status)
			_, _ = fmt.Fprintf(writer, `{"ok":false,"error":{"code":%q,"message":%q}}`, code, message)
		}
	}
	unavailableWithDetails := func(details any) func(http.ResponseWriter, string, string) {
		encoded, err := json.Marshal(details)
		noErr(t, err)
		return func(writer http.ResponseWriter, _, _ string) {
			writer.WriteHeader(http.StatusServiceUnavailable)
			_, _ = fmt.Fprintf(writer, `{"ok":false,"error":{"code":"state_unavailable","message":"Authentication is unavailable.","details":%s}}`, encoded)
		}
	}
	notStarted := pullrequest.OperationErrorDetails{OperationStarted: new(bool)}
	started := true
	invalidMarker, err := json.Marshal(notStarted)
	noErr(t, err)
	invalidMarker = bytes.Replace(invalidMarker, []byte("false"), []byte(`"false"`), 1)
	// A proxy page is not an OwnGit answer, so it settles nothing.
	proxyPage := func(writer http.ResponseWriter, _, _ string) {
		writer.Header().Set("Content-Type", "text/html")
		writer.WriteHeader(http.StatusForbidden)
		_, _ = io.WriteString(writer, "<html>forbidden by proxy</html>")
	}
	const unavailable = "The state store is unavailable."
	for _, command := range []struct {
		name       string
		collection string
		command    string // lists and revokes the credentials
		idFlag     string
		run        func(output string, flags []string) error
	}{
		{"helper", "helper-credentials", "owngit helper-credential", "--id", func(output string, flags []string) error {
			return helperCredentialCommand(append([]string{"create", "--label", "laptop", "--output", output}, flags...))
		}},
		{"runner", "runner-credentials", "owngit runner-credential", "--credential", func(output string, flags []string) error {
			return runnerCredentialCommand(append([]string{"issue", "--label", "runner", "--token-file", output}, flags...))
		}},
	} {
		for _, test := range []struct {
			name        string
			answer      func(writer http.ResponseWriter, creationID, output string)
			revokeFails bool
			code        string
			reason      string
			revokes     int  // compensating DELETE requests
			identity    bool // the message names the creation identity
			retry       bool // the message suggests a retry
		}{
			{"unauthorized", refusal(http.StatusUnauthorized, "unauthorized", "The administrator password is incorrect."), true,
				"unauthorized", "password is incorrect", 0, false, false},
			{"forbidden", refusal(http.StatusForbidden, "forbidden", "Credentials are disabled for this repository."), true,
				"forbidden", "disabled", 0, false, false},
			{"not found", refusal(http.StatusNotFound, "check_policy_not_found", "The repository has no configured-check policy."), true,
				"check_policy_not_found", "no configured-check policy", 0, false, false},
			{"creation conflict", refusal(http.StatusConflict, "creation_conflict", "The creation identity has different content."), true,
				"creation_conflict", "different content", 0, false, false},
			{"invalid input", refusal(http.StatusUnprocessableEntity, "invalid_credential", "The label is invalid."), true,
				"invalid_credential", "label is invalid", 0, false, false},
			{"replay without a token", replay, true,
				"token_unavailable", command.command + " revoke " + command.idFlag + " " + credentialID, 0, true, false},
			{"proxy page and confirmed revoke", proxyPage, false,
				"invalid_response", "non-JSON", 1, false, true},
			{"authentication refused before issuance", unavailableWithDetails(notStarted), true,
				"state_unavailable", "Authentication is unavailable.", 0, false, false},
			{"operation started and failed revoke", unavailableWithDetails(pullrequest.OperationErrorDetails{OperationStarted: &started}), true,
				"credential_creation_unconfirmed", "Authentication is unavailable.", 1, true, true},
			{"missing operation marker and failed revoke", unavailableWithDetails(struct{}{}), true,
				"credential_creation_unconfirmed", "Authentication is unavailable.", 1, true, true},
			{"null operation marker and failed revoke", unavailableWithDetails(pullrequest.OperationErrorDetails{}), true,
				"credential_creation_unconfirmed", "Authentication is unavailable.", 1, true, true},
			{"invalid operation marker and failed revoke", unavailableWithDetails(json.RawMessage(invalidMarker)), true,
				"credential_creation_unconfirmed", "Authentication is unavailable.", 1, true, true},
			{"server failure and failed revoke", refusal(http.StatusServiceUnavailable, "state_unavailable", unavailable), true,
				"credential_creation_unconfirmed", unavailable, 1, true, true},
			{"lost response and confirmed revoke", lostResponse, false,
				"connection_failed", "request failed", 1, false, true},
			{"malformed response and confirmed revoke", malformed, false,
				"invalid_response", "did not return", 1, false, true},
			{"malformed response and failed revoke", malformed, true,
				"credential_creation_unconfirmed", "did not return", 1, true, true},
			{"replaced output and failed revoke", replacedOutput, true,
				"credential_creation_unconfirmed", "output path was replaced", 1, true, true},
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
				revoked := test.revokes == 1 && !test.revokeFails
				// No command revokes by creation identity, so the owner is sent
				// to the list, which shows each credential's creation_id.
				find := "The creation outcome is unconfirmed and the compensating revoke failed. Run " + command.command +
					" list, and if a credential with creation_id " + creationID + " is listed without revoked_at, revoke it with " +
					command.command + " revoke " + command.idFlag + " <id>. Retry the command."
				if envelope.Error.Code != test.code || !strings.Contains(message, test.reason) || !strings.Contains(message, "preserved") ||
					strings.Contains(message, creationID) != test.identity || strings.Contains(message, "was revoked") != revoked ||
					strings.Contains(message, "Retry the command") != test.retry ||
					strings.Contains(message, find) != (test.code == "credential_creation_unconfirmed") {
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

// TestRunnerCredentialReuseKeepsTheEarlierCredential reuses a creation
// identity against a real server. Neither a refused request nor a replay
// created anything, so nothing is revoked by that identity. An active
// credential stays active and the replay names it. A revoked credential is
// reported as revoked, with a new --creation-id as the next step.
func TestRunnerCredentialReuseKeepsTheEarlierCredential(t *testing.T) {
	fixture := newRunnerIssuanceFixture(t)
	activeID := strings.Repeat("c", 32)
	active, failure := fixture.issue(t, activeID, "build-host", "active-token")
	if failure != "" {
		t.Fatal(failure)
	}
	for _, refusal := range []struct {
		name, label, code string
		unavailable       bool
	}{
		{"invalid label", "bad\nlabel", "invalid_runner_credential", false},
		{"unfinished authentication", "build-host", "state_unavailable", true},
	} {
		t.Run(refusal.name, func(t *testing.T) {
			if refusal.unavailable {
				noErr(t, fixture.store.RecordFailedAttempt(fixture.ctx, "admin", "127.0.0.1", time.Now()))
				noErr(t, fixture.store.Exec(fixture.ctx, `CREATE TRIGGER refuse_clearing BEFORE DELETE ON login_attempts BEGIN SELECT RAISE(ABORT, 'injected failure'); END`))
				t.Cleanup(func() { noErr(t, fixture.store.Exec(fixture.ctx, `DROP TRIGGER refuse_clearing`)) })
			}
			_, failure := fixture.issue(t, activeID, refusal.label, refusal.code+"-token")
			if !strings.HasPrefix(failure, refusal.code+": ") || strings.Contains(failure, "was revoked") {
				t.Fatalf("refused reuse: %s", failure)
			}
			if revokes := fixture.creationRevokes.Load(); revokes != 0 {
				t.Fatalf("a pre-issuance refusal sent %d revokes", revokes)
			}
		})
	}
	// The same content replays the earlier creation without its token.
	_, failure = fixture.issue(t, activeID, "build-host", "replayed-token")
	if !strings.HasPrefix(failure, "token_unavailable: ") || !strings.Contains(failure, activeID) ||
		!strings.Contains(failure, "owngit runner-credential revoke --credential "+active.Credential.ID) {
		t.Fatalf("replayed reuse: %s", failure)
	}
	// The list shows the creation identity, which unconfirmed compensation
	// guidance relies on.
	listed := fixture.runCredentialList(t)
	if len(listed.Credentials) != 1 || listed.Credentials[0].ID != active.Credential.ID || listed.Credentials[0].RevokedAt != nil ||
		listed.Credentials[0].CreationID != activeID {
		t.Fatalf("earlier credential count=%d still active=%v", len(listed.Credentials),
			len(listed.Credentials) == 1 && listed.Credentials[0].ID == active.Credential.ID && listed.Credentials[0].RevokedAt == nil)
	}

	token, err := readTokenFile(filepath.Join(fixture.root, "active-token"))
	noErr(t, err)
	credential, accepted, err := fixture.store.RunnerCredentialByToken(fixture.ctx, fixture.repository.ID, token.secret, time.Now())
	noErr(t, err)
	if !accepted || credential.ID != active.Credential.ID {
		t.Fatal("the earlier runner token is no longer active")
	}
	content, err := os.ReadFile(filepath.Join(fixture.root, "replayed-token"))
	noErr(t, err)
	if len(content) != 0 {
		t.Fatal("the replay disclosed a token")
	}

	// A replay of a credential the owner revoked names it as revoked and asks
	// for a new creation identity instead of an impossible revoke.
	revokedID := strings.Repeat("d", 32)
	revoked, failure := fixture.issue(t, revokedID, "revoked-host", "revoked-token")
	if failure != "" {
		t.Fatal(failure)
	}
	configuredCheckCLIOutput(t, func() error {
		return runnerCredentialCommand(fixture.adminArguments("revoke", "--credential", revoked.Credential.ID))
	})
	_, failure = fixture.issue(t, revokedID, "revoked-host", "after-revoke-token")
	if !strings.HasPrefix(failure, "token_unavailable: ") || !strings.Contains(failure, revoked.Credential.ID+", which is revoked") ||
		!strings.Contains(failure, newCreationIdentity) || strings.Contains(failure, "revoke the credential with") {
		t.Fatalf("replay of a revoked credential: %s", failure)
	}
	if revokes := fixture.creationRevokes.Load(); revokes != 0 {
		t.Fatalf("a refusal or replay sent %d revokes by creation identity", revokes)
	}
}

// TestCompensatedRunnerIssuanceAsksForANewCreationIdentity covers a confirmed
// compensation against a real server when the owner chose the creation
// identity. That identity now names the revoked credential, so the guidance
// asks for a new identity, and following it issues a working credential.
func TestCompensatedRunnerIssuanceAsksForANewCreationIdentity(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows denies path replacement while the private handle is open")
	}
	fixture := newRunnerIssuanceFixture(t)
	output := filepath.Join(fixture.root, "compensated-token")
	fixture.beforeIssue = func() {
		if err := os.Rename(output, output+".moved"); err != nil {
			t.Errorf("replace the reserved path: %v", err)
		}
	}
	_, failure := fixture.issue(t, strings.Repeat("e", 32), "compensated-host", "compensated-token")
	fixture.beforeIssue = nil
	if !strings.HasPrefix(failure, "output_replaced: ") || !strings.Contains(failure, "was revoked. "+newCreationIdentity) {
		t.Fatalf("compensated issuance: %s", failure)
	}
	if revokes := fixture.creationRevokes.Load(); revokes != 1 {
		t.Fatalf("revokes by creation identity=%d, want the compensation only", revokes)
	}
	listed := fixture.runCredentialList(t)
	if len(listed.Credentials) != 1 || listed.Credentials[0].CreationID != strings.Repeat("e", 32) || listed.Credentials[0].RevokedAt == nil {
		t.Fatalf("compensation did not revoke the issued credential: %+v", listed.Credentials)
	}
	if _, failure := fixture.issue(t, "", "compensated-host", "fresh-token"); failure != "" {
		t.Fatalf("issuance without a chosen identity: %s", failure)
	}
}

const newCreationIdentity = "Run the command again with a new --creation-id, or without it."

type runnerIssuanceFixture struct {
	*configuredCheckCLIFixture
}

// newRunnerIssuanceFixture starts a real server whose repository has an
// external-runner policy, so runner credentials can be issued.
func newRunnerIssuanceFixture(t *testing.T) runnerIssuanceFixture {
	fixture := newConfiguredCheckCLIFixture(t)
	policyFile := filepath.Join(fixture.root, "policy.json")
	policyJSON, err := json.Marshal(checkapi.PolicyInput{
		Executor: state.CheckExecutorExternalRunner, AllowedEvents: []string{checkworkflow.EventPush},
		MaxTimeoutMS: 5_000, MaxOutputLimitBytes: 64 << 10, QueueLimit: 4, MaxActiveJobs: 1, MaxLeaseMS: 10_000,
	})
	noErr(t, err)
	noErr(t, os.WriteFile(policyFile, policyJSON, 0o600))
	fixture.runPolicy(t, "set", "--policy-file", policyFile)
	return runnerIssuanceFixture{fixture}
}

// issue runs "runner-credential issue" with an optional chosen creation
// identity. It returns the credential, or the failure as "code: message".
func (fixture runnerIssuanceFixture) issue(t *testing.T, creationID, label, tokenFile string) (checkapi.RunnerCredentialResponse, string) {
	t.Helper()
	arguments := []string{"issue", "--label", label, "--token-file", filepath.Join(fixture.root, tokenFile)}
	if creationID != "" {
		arguments = append(arguments, "--creation-id", creationID)
	}
	stdout, err := captureStdout(func() error { return runnerCredentialCommand(fixture.adminArguments(arguments...)) })
	var issued checkapi.RunnerCredentialResponse
	if err != nil {
		return issued, commandErrorCode(err) + ": " + err.Error()
	}
	if err := json.Unmarshal([]byte(stdout), &issued); err != nil || issued.Credential == nil {
		t.Fatal("issuance did not return a credential")
	}
	return issued, ""
}
