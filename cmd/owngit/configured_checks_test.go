package main

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
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

func TestConfiguredCheckCLIEndToEnd(t *testing.T) {
	fixture := newConfiguredCheckCLIFixture(t)
	policyFile := filepath.Join(fixture.root, "policy.json")
	policyInput := checkapi.PolicyInput{
		Executor: state.CheckExecutorExternalRunner, AllowedEvents: []string{checkworkflow.EventPush},
		MaxTimeoutMS: 5_000, MaxOutputLimitBytes: 64 << 10,
		QueueLimit: 4, MaxActiveJobs: 1, MaxLeaseMS: 10_000,
	}
	policyJSON, err := json.Marshal(policyInput)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(policyFile, policyJSON, 0o600); err != nil {
		t.Fatal(err)
	}

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
	var issued checkapi.RunnerCredentialResponse
	if err := json.Unmarshal([]byte(issueOutput), &issued); err != nil || issued.Credential == nil {
		t.Fatal("runner-credential issue did not return a credential")
	}
	token, err := os.ReadFile(tokenFile)
	if err != nil || strings.TrimSpace(string(token)) == "" {
		t.Fatalf("runner token file available=%v err=%v", len(token) != 0, err)
	}
	trimmedToken := strings.TrimSpace(string(token))
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
	if err := os.Mkdir(repositoryRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	store, err := state.Open(ctx, stateRoot)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Error(err)
		}
	})
	const adminPassword = "synthetic-admin-password"
	adminHash, err := auth.HashPassword(adminPassword)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.CompleteSetup(ctx, repositoryRoot, "open", "", adminHash, true); err != nil {
		t.Fatal(err)
	}
	git, err := gitexec.New("", filepath.Join(stateRoot, "runtime"))
	if err != nil {
		t.Fatal(err)
	}
	manager := &repository.Manager{Store: store, Git: git, Locks: gitexec.NewLocks(), Root: repositoryRoot}
	stored, err := manager.Create(ctx, "configured-check-cli", "")
	if err != nil {
		t.Fatal(err)
	}
	repositoryPath, err := manager.Path(stored.ID)
	if err != nil {
		t.Fatal(err)
	}
	work := filepath.Join(root, "source")
	runPRGit(t, "", "init", "--initial-branch=main", work)
	runPRGit(t, work, "config", "user.name", "Configured Check CLI Test")
	runPRGit(t, work, "config", "user.email", "configured-check@example.invalid")
	if err := os.WriteFile(filepath.Join(work, "source.txt"), []byte("exact CLI runner source\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runPRGit(t, work, "add", "source.txt")
	runPRGit(t, work, "commit", "-m", "configured check CLI source")
	runPRGit(t, work, "push", repositoryPath, "HEAD:refs/heads/main")
	sourceOID := prGitOutput(t, work, "rev-parse", "HEAD")

	gitHandler, err := githttp.New(git, manager, "", 2)
	if err != nil {
		t.Fatal(err)
	}
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
	if err := os.WriteFile(adminPasswordFile, []byte(adminPassword+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := state.ProtectPrivatePath(adminPasswordFile, false); err != nil {
		t.Fatal(err)
	}
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
	if err != nil {
		t.Fatal(err)
	}
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
