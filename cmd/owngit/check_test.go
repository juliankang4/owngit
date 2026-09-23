package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"owngit/internal/apiclient"
	"owngit/internal/auth"
	"owngit/internal/checkapi"
	"owngit/internal/checkexec"
	"owngit/internal/gitexec"
	"owngit/internal/githttp"
	"owngit/internal/pullrequest"
	"owngit/internal/repository"
	"owngit/internal/server"
	"owngit/internal/state"
	"owngit/internal/version"
)

func TestVersionFlagWorksWithoutOpeningState(t *testing.T) {
	output, err := captureStdout(func() error { return run([]string{"--version"}) })
	noErr(t, err)
	// The CLI must propagate the single authoritative value, not a copy.
	if output != "owngit "+version.Version+"\n" {
		t.Fatalf("version output=%q", output)
	}
}

func TestCheckCLIEndToEndRecordsRevisionBoundEvidence(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	stateRoot := filepath.Join(root, "state")
	repositoryRoot := filepath.Join(root, "repositories")
	noErr(t, os.Mkdir(repositoryRoot, 0o700))
	store, err := state.Open(ctx, stateRoot)
	noErr(t, err)
	defer store.Close()
	adminHash, err := auth.HashPassword("admin-password")
	noErr(t, err)
	noErr(t, store.CompleteSetup(ctx, repositoryRoot, "open", "", adminHash, true))
	runner, err := gitexec.New("", filepath.Join(stateRoot, "runtime"))
	noErr(t, err)
	manager := &repository.Manager{Store: store, Git: runner, Locks: gitexec.NewLocks(), Root: repositoryRoot}
	if _, err := manager.Create(ctx, "project", "check CLI fixture"); err != nil {
		t.Fatal(err)
	}
	gitHandler, err := githttp.New(runner, manager, "", 2)
	noErr(t, err)
	application := &server.App{
		Store: store, Auth: &auth.Manager{Store: store, SessionLife: time.Hour}, Repositories: manager,
		PullRequests: &pullrequest.Service{Store: store, Repositories: manager},
		GitHTTP:      gitHandler, Hosts: server.NewHostPolicy(),
	}
	gitHandler.Authorize = application.AuthorizeGit
	httpServer := httptest.NewServer(application.Handler())
	defer httpServer.Close()

	token := "synthetic-helper-token"
	hash := sha256.Sum256([]byte(token))
	if _, _, err := store.CreateHelperCredential(ctx, "project", "cli", "", hash[:], time.Now()); err != nil {
		t.Fatal(err)
	}
	credentialFile := filepath.Join(root, "helper-token")
	noErr(t, os.WriteFile(credentialFile, []byte(token+"\n"), 0o600))
	noErr(t, state.ProtectPrivatePath(credentialFile, false))

	work := filepath.Join(root, "work")
	runPRGit(t, "", "init", "--initial-branch=main", work)
	runPRGit(t, work, "config", "user.name", "Check Test")
	runPRGit(t, work, "config", "user.email", "check-test@example.invalid")
	noErr(t, os.WriteFile(filepath.Join(work, "file.txt"), []byte("base\n"), 0o600))
	runPRGit(t, work, "add", ".")
	runPRGit(t, work, "commit", "-m", "base")
	revision := prGitOutput(t, work, "rev-parse", "HEAD")

	remoteFlags := []string{"--server", httpServer.URL, "--accept-insecure-http", "--repository", "project", "--credential-file", credentialFile}

	// Credential management uses the administrator password over Basic, and a
	// wrong password is rejected.
	adminPasswordFile := filepath.Join(root, "admin-password")
	noErr(t, os.WriteFile(adminPasswordFile, []byte("admin-password\n"), 0o600))
	noErr(t, state.ProtectPrivatePath(adminPasswordFile, false))
	adminFlags := []string{"--server", httpServer.URL, "--accept-insecure-http", "--repository", "project", "--password-file", adminPasswordFile}
	issuedTokenFile := filepath.Join(root, "issued-token")
	credentialOutput, err := captureStdout(func() error {
		return helperCredentialCommand(append([]string{"create", "--label", "cli", "--output", issuedTokenFile}, adminFlags...))
	})
	if err != nil {
		t.Fatalf("helper-credential create error=%v output=%s", err, credentialOutput)
	}
	var credentialResponse checkapi.CredentialResponse
	if err := json.Unmarshal([]byte(credentialOutput), &credentialResponse); err != nil || credentialResponse.Credential == nil {
		t.Fatalf("credential output=%q err=%v", credentialOutput, err)
	}
	// The token is delivered only through the protected file, so stdout never
	// carries the secret.
	if credentialResponse.Token != "" || strings.Contains(credentialOutput, "synthetic") {
		t.Fatalf("stdout carried the token: %q", credentialOutput)
	}
	issuedToken, err := os.ReadFile(issuedTokenFile)
	if err != nil || strings.TrimSpace(string(issuedToken)) == "" {
		t.Fatalf("issued token file=%q err=%v", issuedToken, err)
	}
	// An existing delivery file is reported before any remote creation.
	if _, err := captureStdout(func() error {
		return helperCredentialCommand(append([]string{"create", "--label", "cli", "--output", issuedTokenFile}, adminFlags...))
	}); err == nil {
		t.Fatal("an existing output file was overwritten")
	}
	// The direct fixture credential plus the one issued above. The rejected
	// create must not have reached the server.
	credentials, err := store.HelperCredentials(ctx, "project")
	if err != nil || len(credentials) != 2 {
		t.Fatalf("credentials after the rejected create=%d err=%v", len(credentials), err)
	}
	wrongPasswordFile := filepath.Join(root, "wrong-admin-password")
	noErr(t, os.WriteFile(wrongPasswordFile, []byte("wrong-password\n"), 0o600))
	noErr(t, state.ProtectPrivatePath(wrongPasswordFile, false))
	if _, err := captureStdout(func() error {
		return helperCredentialCommand([]string{"create", "--label", "cli", "--output", filepath.Join(root, "wrong-token"), "--server", httpServer.URL, "--accept-insecure-http", "--repository", "project", "--password-file", wrongPasswordFile})
	}); err == nil {
		t.Fatal("a wrong administrator password was accepted")
	}
	// A second credential can be revoked without affecting the first.
	revokeTokenFile := filepath.Join(root, "revoke-token")
	revokeOutput, err := captureStdout(func() error {
		return helperCredentialCommand(append([]string{"create", "--label", "revoke", "--output", revokeTokenFile}, adminFlags...))
	})
	if err != nil {
		t.Fatalf("second create error=%v output=%s", err, revokeOutput)
	}
	var revokeResponse checkapi.CredentialResponse
	if err := json.Unmarshal([]byte(revokeOutput), &revokeResponse); err != nil || revokeResponse.Credential == nil {
		t.Fatalf("second credential output=%q err=%v", revokeOutput, err)
	}
	if _, err := captureStdout(func() error {
		return helperCredentialCommand(append([]string{"revoke", "--id", revokeResponse.Credential.ID}, adminFlags...))
	}); err != nil {
		t.Fatalf("helper-credential revoke error=%v", err)
	}
	taskOutput, err := captureStdout(func() error {
		return checkCommand(append([]string{"task", "new", "--title", "CLI task"}, remoteFlags...))
	})
	noErr(t, err)
	var taskResponse checkapi.TaskResponse
	if err := json.Unmarshal([]byte(taskOutput), &taskResponse); err != nil || taskResponse.Task == nil {
		t.Fatalf("task output=%q err=%v", taskOutput, err)
	}
	taskID := taskResponse.Task.ID

	// The issued token authenticates helper traffic.
	issuedFlags := []string{"--server", httpServer.URL, "--accept-insecure-http", "--repository", "project", "--credential-file", issuedTokenFile}
	if _, err := captureStdout(func() error {
		return checkCommand(append([]string{"status", "--task", taskID}, issuedFlags...))
	}); err != nil {
		t.Fatalf("issued token was rejected: %v", err)
	}

	passOutput, err := captureStdout(func() error {
		return checkCommand(append([]string{"run", "--task", taskID, "--workdir", work, "--check", "pass=exit 0"}, remoteFlags...))
	})
	if err != nil {
		t.Fatalf("passing run error=%v output=%s", err, passOutput)
	}
	var passResult checkRunOutput
	noErr(t, json.Unmarshal([]byte(passOutput), &passResult))
	if !passResult.OK || !passResult.Uploaded || passResult.Attempt == nil || passResult.Attempt.Status != state.AttemptPassed {
		t.Fatalf("passing run result=%+v", passResult)
	}
	if passResult.Attempt.RevisionOID != revision || passResult.Attempt.WorktreeState != state.WorktreeClean {
		t.Fatalf("attempt binding=%+v", passResult.Attempt)
	}

	failOutput, err := captureStdout(func() error {
		return checkCommand(append([]string{"run", "--task", taskID, "--workdir", work, "--check", "fail=exit 4"}, remoteFlags...))
	})
	var exit *checkExit
	if !errors.As(err, &exit) || exit.code != 1 {
		t.Fatalf("failing run error=%v output=%s", err, failOutput)
	}
	var failResult checkRunOutput
	noErr(t, json.Unmarshal([]byte(failOutput), &failResult))
	if !failResult.OK || failResult.Attempt == nil || failResult.Attempt.Status != state.AttemptFailed {
		t.Fatalf("failing run result=%+v", failResult)
	}
	// A manual rerun consumes no correction round.
	if failResult.CorrectionCyclesRemaining != state.CorrectionCycleLimit {
		t.Fatalf("manual rerun consumed a round: remaining=%d", failResult.CorrectionCyclesRemaining)
	}

	// A round is reserved before the correction and counted once, whether the
	// following check passes or fails.
	cycleOutput, err := captureStdout(func() error {
		return checkCommand(append([]string{"cycle", "reserve", "--task", taskID}, remoteFlags...))
	})
	if err != nil {
		t.Fatalf("cycle reserve error=%v output=%s", err, cycleOutput)
	}
	var cycleResponse checkapi.CycleResponse
	if err := json.Unmarshal([]byte(cycleOutput), &cycleResponse); err != nil || cycleResponse.Cycle == nil {
		t.Fatalf("cycle output=%q err=%v", cycleOutput, err)
	}
	correctionOutput, err := captureStdout(func() error {
		return checkCommand(append([]string{"run", "--task", taskID, "--cycle", cycleResponse.Cycle.ID, "--workdir", work, "--check", "pass=exit 0"}, remoteFlags...))
	})
	if err != nil {
		t.Fatalf("correction run error=%v output=%s", err, correctionOutput)
	}
	var correctionResult checkRunOutput
	noErr(t, json.Unmarshal([]byte(correctionOutput), &correctionResult))
	if correctionResult.Attempt == nil || correctionResult.Attempt.CycleID != cycleResponse.Cycle.ID {
		t.Fatalf("correction attempt=%+v", correctionResult.Attempt)
	}
	if correctionResult.CorrectionCyclesRemaining != state.CorrectionCycleLimit-1 {
		t.Fatalf("successful correction did not count: remaining=%d", correctionResult.CorrectionCyclesRemaining)
	}

	attempts, err := store.CheckAttempts(ctx, "project")
	if err != nil || len(attempts) != 3 {
		t.Fatalf("stored attempts=%d err=%v", len(attempts), err)
	}
	// A check that dirties the tree while it runs must not be certified as the
	// revision observed before it ran.
	postDirtyOutput, err := captureStdout(func() error {
		return checkCommand(append([]string{"run", "--task", taskID, "--workdir", work, "--check", "touch=echo changed > generated.txt"}, remoteFlags...))
	})
	if err != nil {
		t.Fatalf("post-execution dirty run error=%v output=%s", err, postDirtyOutput)
	}
	var postDirtyResult checkRunOutput
	noErr(t, json.Unmarshal([]byte(postDirtyOutput), &postDirtyResult))
	if postDirtyResult.Attempt == nil || postDirtyResult.Attempt.WorktreeState != state.WorktreeDirty {
		t.Fatalf("post-execution dirty attempt=%+v", postDirtyResult.Attempt)
	}
	noErr(t, os.Remove(filepath.Join(work, "generated.txt")))

	// A dirty worktree must not be reported as a tested commit.
	noErr(t, os.WriteFile(filepath.Join(work, "dirty.txt"), []byte("dirty\n"), 0o600))
	dirtyOutput, err := captureStdout(func() error {
		return checkCommand(append([]string{"run", "--task", taskID, "--workdir", work, "--check", "pass=exit 0"}, remoteFlags...))
	})
	if err != nil {
		t.Fatalf("dirty run error=%v output=%s", err, dirtyOutput)
	}
	var dirtyResult checkRunOutput
	noErr(t, json.Unmarshal([]byte(dirtyOutput), &dirtyResult))
	if dirtyResult.Attempt == nil || dirtyResult.Attempt.WorktreeState != state.WorktreeDirty {
		t.Fatalf("dirty attempt=%+v", dirtyResult.Attempt)
	}

	// A large output must be truncated to the durable excerpt bound instead of
	// being rejected by the server. Read a fixture file so the check command
	// itself stays below the Windows command-line limit.
	largeFixture := filepath.Join(work, "large-output.txt")
	noErr(t, os.WriteFile(largeFixture, []byte(strings.Repeat("y", 20000)), 0o600))
	largeCommand := "cat large-output.txt"
	if runtime.GOOS == "windows" {
		largeCommand = "type large-output.txt"
	}
	largeOutput, err := captureStdout(func() error {
		return checkCommand(append([]string{"run", "--task", taskID, "--workdir", work, "--check", "big=" + largeCommand}, remoteFlags...))
	})
	if err != nil {
		t.Fatalf("large output run error=%v output=%s", err, largeOutput)
	}
	var largeResult checkRunOutput
	noErr(t, json.Unmarshal([]byte(largeOutput), &largeResult))
	if !largeResult.Uploaded || largeResult.Attempt == nil || len(largeResult.Attempt.Results) != 1 {
		t.Fatalf("large output result=%+v", largeResult)
	}
	if len(largeResult.Attempt.Results[0].OutputExcerpt) > state.MaximumCheckExcerptBytes || !largeResult.Attempt.Results[0].Truncated {
		t.Fatalf("large excerpt length=%d truncated=%v", len(largeResult.Attempt.Results[0].OutputExcerpt), largeResult.Attempt.Results[0].Truncated)
	}

	// A local-only run with explicit checks does not need a server.
	localOutput, err := captureStdout(func() error {
		return checkCommand([]string{"run", "--task", "local-task", "--workdir", work, "--check", "pass=exit 0", "--no-upload"})
	})
	if err != nil {
		t.Fatalf("local run error=%v output=%s", err, localOutput)
	}
	var localResult checkRunOutput
	noErr(t, json.Unmarshal([]byte(localOutput), &localResult))
	if !localResult.OK || localResult.Uploaded || localResult.Attempt == nil || localResult.Attempt.Status != state.AttemptPassed {
		t.Fatalf("local run result=%+v", localResult)
	}
	if localResult.AttemptID == "" || localResult.Attempt.ID != localResult.AttemptID {
		t.Fatalf("local run identity=%q attempt=%+v", localResult.AttemptID, localResult.Attempt)
	}
}

func TestCleanupFailureOutranksSeparateCancellationInCLI(t *testing.T) {
	zero := 0
	cleanup := checkexec.Result{
		Name: "cleanup", Command: "exit 0", Status: checkexec.StatusError,
		ExitCode: &zero, CleanupError: "owned process exit was not confirmed",
	}
	cancelled := checkexec.Result{Name: "remaining", Command: "exit 0", Status: checkexec.StatusCancelled}
	tests := []struct {
		name    string
		results []checkexec.Result
	}{
		{name: "cleanup first", results: []checkexec.Result{cleanup, cancelled}},
		{name: "cleanup last", results: []checkexec.Result{cancelled, cleanup}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			status := aggregateCheckStatus(test.results, true)
			if status != checkexec.StatusError {
				t.Fatalf("cleanup plus separate cancellation status=%q", status)
			}
			facts := checkResultsJSON(test.results)
			if len(facts) != 2 || facts[0].Status != test.results[0].Status || facts[1].Status != test.results[1].Status || facts[0].CleanupError != test.results[0].CleanupError || facts[1].CleanupError != test.results[1].CleanupError {
				t.Fatalf("CLI conversion changed submitted facts: got=%+v want=%+v", facts, test.results)
			}
			if summary := state.AttemptSummary(checkResults(test.results), state.WorktreeClean, status); summary != "2 checks: 1 error, 1 cancelled" {
				t.Fatalf("cleanup plus cancellation summary=%q", summary)
			}
			// A local no-upload run records its synthetic attempt before choosing
			// the exit code. Cleanup uncertainty is an execution failure, not the
			// cancellation exit reserved for cancellation-only evidence.
			var exit *checkExit
			if err := checkRunOutcomeError(true, status, true); !errors.As(err, &exit) || exit.code != 1 {
				t.Fatalf("local cleanup exit=%v", err)
			}
		})
	}

	ordinary := []checkexec.Result{
		{Name: "error", Command: "exit 1", Status: checkexec.StatusError},
		cancelled,
	}
	status := aggregateCheckStatus(ordinary, true)
	var exit *checkExit
	if err := checkRunOutcomeError(true, status, true); status != checkexec.StatusCancelled || !errors.As(err, &exit) || exit.code != 130 {
		t.Fatalf("ordinary cancellation precedence changed: status=%q err=%v", status, err)
	}
}

func TestReservedTokenFileKeepsTheHandleAndDetectsReplacement(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows denies path replacement while the private handle is open")
	}
	directory := t.TempDir()
	path := filepath.Join(directory, "token")
	reserved, err := reservePrivateTokenFile(path)
	noErr(t, err)
	// Replace the path with a different regular file while the handle stays
	// open, as a concurrent writer would.
	moved := path + ".moved"
	noErr(t, os.Rename(path, moved))
	noErr(t, os.WriteFile(path, []byte("replacement\n"), 0o600))
	if !reserved.replaced() {
		t.Fatal("path replacement was not detected")
	}
	noErr(t, reserved.write("token-value"))
	// The token went to the reserved file, and the replacement is untouched.
	content, err := os.ReadFile(moved)
	if err != nil || string(content) != "token-value\n" {
		t.Fatalf("reserved file=%q err=%v", content, err)
	}
	replacement, err := os.ReadFile(path)
	if err != nil || string(replacement) != "replacement\n" {
		t.Fatalf("replacement=%q err=%v", replacement, err)
	}
	noErr(t, reserved.preserve())
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("preserve removed the replacement target: %v", err)
	}
}

func TestReservedTokenFileDoesNotWriteThroughASymlinkReplacement(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows denies path replacement while the private handle is open")
	}
	directory := t.TempDir()
	path := filepath.Join(directory, "token")
	reserved, err := reservePrivateTokenFile(path)
	noErr(t, err)
	target := filepath.Join(directory, "target")
	noErr(t, os.WriteFile(target, []byte("target\n"), 0o600))
	noErr(t, os.Remove(path))
	noErr(t, os.Symlink(target, path))
	if !reserved.replaced() {
		t.Fatal("symlink replacement was not detected")
	}
	noErr(t, reserved.write("token-value"))
	// The symlink target is not written through.
	content, err := os.ReadFile(target)
	if err != nil || string(content) != "target\n" {
		t.Fatalf("symlink target=%q err=%v", content, err)
	}
	noErr(t, reserved.preserve())
	if _, err := os.Lstat(path); err != nil {
		t.Fatalf("preserve removed the symlink: %v", err)
	}
}

func TestCompensatingRevokeIsScopedAndIdempotent(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	repositoryRoot := filepath.Join(root, "repositories")
	noErr(t, os.Mkdir(repositoryRoot, 0o700))
	store, err := state.Open(ctx, filepath.Join(root, "state"))
	noErr(t, err)
	defer store.Close()
	adminHash, err := auth.HashPassword("admin-password")
	noErr(t, err)
	noErr(t, store.CompleteSetup(ctx, repositoryRoot, "open", "", adminHash, true))
	runner, err := gitexec.New("", filepath.Join(root, "runtime"))
	noErr(t, err)
	manager := &repository.Manager{Store: store, Git: runner, Locks: gitexec.NewLocks(), Root: repositoryRoot}
	if _, err := manager.Create(ctx, "project", "compensate"); err != nil {
		t.Fatal(err)
	}
	gitHandler, err := githttp.New(runner, manager, "", 2)
	noErr(t, err)
	application := &server.App{
		Store: store, Auth: &auth.Manager{Store: store, SessionLife: time.Hour}, Repositories: manager,
		PullRequests: &pullrequest.Service{Store: store, Repositories: manager},
		GitHTTP:      gitHandler, Hosts: server.NewHostPolicy(),
	}
	httpServer := httptest.NewServer(application.Handler())
	defer httpServer.Close()
	parsed, err := apiclient.ValidateServer(httpServer.URL, true)
	noErr(t, err)
	client := apiclient.NewAdmin(parsed, "admin-password")
	path := "/api/v1/repositories/project"
	creationID := "0123456789abcdef0123456789abcdef"
	// Create the credential directly, as a lost response would leave it.
	if _, err := client.Do(ctx, "POST", path+"/helper-credentials", checkapi.CreateCredentialInput{Label: "laptop", CreationID: creationID}); err != nil {
		t.Fatal(err)
	}
	// A lost response is compensated with a scoped revoke, and the caller is
	// told to retry.
	err = compensateCreation(client, path, creationID, "credential_creation_failed", errors.New("lost response"))
	var problem *apiclient.Error
	if !errors.As(err, &problem) || problem.Code != "credential_creation_failed" {
		t.Fatalf("compensation error=%v", err)
	}
	// The revoke is idempotent, so a second compensation is safe.
	if err := compensateCreation(client, path, creationID, "credential_creation_failed", errors.New("lost response")); err == nil {
		t.Fatal("second compensation reported success")
	}
	credentials, err := store.HelperCredentials(ctx, "project")
	if err != nil || len(credentials) != 1 || credentials[0].RevokedAt == nil {
		t.Fatalf("credentials=%+v err=%v", credentials, err)
	}
	// An unreachable server cannot confirm the revoke, so the creation
	// identity is reported instead of the token.
	dead := apiclient.NewAdmin(&url.URL{Scheme: "http", Host: "127.0.0.1:1"}, "admin-password")
	err = compensateCreation(dead, path, creationID, "credential_creation_failed", errors.New("lost response"))
	if !errors.As(err, &problem) || problem.Code != "credential_creation_unconfirmed" || !strings.Contains(problem.Message, creationID) {
		t.Fatalf("unconfirmed compensation error=%v", err)
	}
}

func TestCredentialCreateConflictPreservesExistingAuthority(t *testing.T) {
	create := runCredentialCreate(t, func(writer http.ResponseWriter, _ checkapi.CreateCredentialInput, _ string) {
		writer.WriteHeader(http.StatusConflict)
		_, _ = io.WriteString(writer, `{"ok":false,"error":{"code":"creation_conflict","message":"The creation identity has different content."}}`)
	})
	var problem *apiclient.Error
	if !errors.As(create.err, &problem) || problem.Code != "creation_conflict" || !strings.Contains(problem.Message, "preserved") {
		t.Fatalf("creation conflict error=%v", create.err)
	}
	if len(create.deleted) != 0 {
		t.Fatalf("creation conflict sent %d compensating revokes", len(create.deleted))
	}
	content, readErr := os.ReadFile(create.output)
	if readErr != nil || len(content) != 0 {
		t.Fatalf("reserved artifact=%q err=%v", content, readErr)
	}
}

func TestCredentialCreateCompensatesAMalformedResponse(t *testing.T) {
	// A success body without a token, as a lost token would look.
	create := runCredentialCreate(t, func(writer http.ResponseWriter, input checkapi.CreateCredentialInput, _ string) {
		_, _ = fmt.Fprintf(writer, `{"ok":true,"credential":{"id":"0123456789abcdef0123456789abcdef","repository_id":"project","creation_id":%q}}`, input.CreationID)
	})
	var problem *apiclient.Error
	if !errors.As(create.err, &problem) || problem.Code != "credential_creation_failed" {
		t.Fatalf("malformed response error=%v", create.err)
	}
	// The compensation is scoped by the creation identity, not by a credential
	// identifier the caller may not have.
	if len(create.deleted) != 1 || !strings.Contains(create.deleted[0], "/helper-credentials/by-creation/") {
		t.Fatalf("compensating revoke=%v", create.deleted)
	}
	// The reserved artifact remains because identity-checked pathname deletion
	// is not available here.
	content, readErr := os.ReadFile(create.output)
	if readErr != nil || len(content) != 0 || !strings.Contains(problem.Message, "preserved") {
		t.Fatalf("reserved artifact=%q readErr=%v problem=%v", content, readErr, problem)
	}
}

func TestReservedTokenFileWriteFailureIsReported(t *testing.T) {
	path := filepath.Join(t.TempDir(), "token")
	reserved, err := reservePrivateTokenFile(path)
	noErr(t, err)
	// Close the handle behind the helper, as a failed sync would leave it.
	noErr(t, reserved.file.Close())
	if err := reserved.write("token-value"); err == nil {
		t.Fatal("a write to a closed handle succeeded")
	}
}

func TestReservedTokenFilePreservesArtifactOnFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "token")
	reserved, err := reservePrivateTokenFile(path)
	noErr(t, err)
	noErr(t, reserved.preserve())
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("reserved artifact was removed: %v", err)
	}
}

func TestReservationProtectionFailurePreservesTheCreatedArtifact(t *testing.T) {
	path := filepath.Join(t.TempDir(), "token")
	reserved, err := reservePrivateTokenFileWithProtection(path, func(*os.File, bool) error {
		return errors.New("forced protection failure")
	})
	if reserved != nil {
		t.Fatal("protection failure returned a reservation")
	}
	var problem *apiclient.Error
	if !errors.As(err, &problem) || problem.Code != "output_failed" || !strings.Contains(problem.Message, "preserved") {
		t.Fatalf("protection error=%v", err)
	}
	content, readErr := os.ReadFile(path)
	if readErr != nil || len(content) != 0 {
		t.Fatalf("reserved artifact=%q err=%v", content, readErr)
	}
}

// TestCredentialCreateRejectsAMismatchedResponseIdentity covers a response that
// names a different repository and creation identity. The token must not be
// delivered as if the request had succeeded.
func TestCredentialCreateRejectsAMismatchedResponseIdentity(t *testing.T) {
	create := runCredentialCreate(t, func(writer http.ResponseWriter, _ checkapi.CreateCredentialInput, _ string) {
		_, _ = io.WriteString(writer, `{"ok":true,"token":"secret","credential":{"id":"0123456789abcdef0123456789abcdef","repository_id":"other","creation_id":"ffffffffffffffffffffffffffffffff"}}`)
	})
	var problem *apiclient.Error
	if !errors.As(create.err, &problem) || problem.Code != "credential_creation_failed" {
		t.Fatalf("mismatched response error=%v", create.err)
	}
	if len(create.deleted) != 1 || !strings.Contains(create.deleted[0], "/helper-credentials/by-creation/") {
		t.Fatalf("compensating revoke=%v", create.deleted)
	}
	content, readErr := os.ReadFile(create.output)
	if readErr != nil || len(content) != 0 || !strings.Contains(problem.Message, "preserved") {
		t.Fatalf("reserved artifact=%q readErr=%v problem=%v", content, readErr, problem)
	}
}

// TestCredentialCreateCompensatesAReplacedOutputPath covers a concurrent writer
// that replaces the reserved path while the request is in flight. The helper
// preserves both artifacts, withholds the token, and compensates the credential.
func TestCredentialCreateCompensatesAReplacedOutputPath(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows denies path replacement while the private handle is open")
	}
	create := runCredentialCreate(t, func(writer http.ResponseWriter, input checkapi.CreateCredentialInput, output string) {
		if err := os.Rename(output, output+".moved"); err != nil {
			t.Errorf("replace the reserved path: %v", err)
		}
		if err := os.WriteFile(output, []byte("replacement\n"), 0o600); err != nil {
			t.Errorf("write the replacement: %v", err)
		}
		_, _ = fmt.Fprintf(writer, `{"ok":true,"token":"secret","credential":{"id":"0123456789abcdef0123456789abcdef","repository_id":"project","creation_id":%q}}`, input.CreationID)
	})
	var problem *apiclient.Error
	if !errors.As(create.err, &problem) || problem.Code != "output_replaced" {
		t.Fatalf("replaced output error=%v", create.err)
	}
	replacement, err := os.ReadFile(create.output)
	if err != nil || string(replacement) != "replacement\n" {
		t.Fatalf("replacement=%q err=%v", replacement, err)
	}
	reserved, err := os.ReadFile(create.output + ".moved")
	if err != nil || len(reserved) != 0 {
		t.Fatalf("reserved artifact=%q err=%v", reserved, err)
	}
	if len(create.deleted) != 1 || !strings.Contains(create.deleted[0], "/helper-credentials/by-creation/") {
		t.Fatalf("compensating revoke=%v", create.deleted)
	}
}

// TestReservedTokenFileProtectsTheHeldHandleNotThePath covers POSIX delivery
// protection. The reserved file stays private after the path is replaced by a
// broader file, which a pathname chmod would not achieve.
func TestReservedTokenFileProtectsTheHeldHandleNotThePath(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows held-handle privacy has a native sharing and ACL test")
	}
	directory := t.TempDir()
	path := filepath.Join(directory, "token")
	reserved, err := reservePrivateTokenFile(path)
	noErr(t, err)
	defer reserved.preserve()
	noErrf(t, state.ValidatePrivateFile(path), "the reserved file is not private")
	moved := path + ".moved"
	noErr(t, os.Rename(path, moved))
	noErr(t, os.WriteFile(path, []byte("replacement\n"), 0o644))
	// The held file keeps its protection, and the replacement does not.
	noErrf(t, state.ValidatePrivateFile(moved), "the held file lost its protection")
	noErr(t, reserved.write("token-value"))
	content, err := os.ReadFile(moved)
	if err != nil || string(content) != "token-value\n" {
		t.Fatalf("held file=%q err=%v", content, err)
	}
}

// credentialCreate is the outcome of one "helper-credential create" run.
type credentialCreate struct {
	err     error
	deleted []string // paths of compensating DELETE requests
	output  string   // the --output path
}

// runCredentialCreate runs "helper-credential create" against a fake server.
// DELETE requests are recorded and answered with success; answer handles the
// creation request.
func runCredentialCreate(t *testing.T, answer func(http.ResponseWriter, checkapi.CreateCredentialInput, string)) credentialCreate {
	t.Helper()
	root := t.TempDir()
	passwordFile := filepath.Join(root, "password")
	noErr(t, os.WriteFile(passwordFile, []byte("admin-password\n"), 0o600))
	noErr(t, state.ProtectPrivatePath(passwordFile, false))
	result := credentialCreate{output: filepath.Join(root, "token")}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		if request.Method == http.MethodDelete {
			result.deleted = append(result.deleted, request.URL.Path)
			_, _ = io.WriteString(writer, `{"ok":true}`)
			return
		}
		body, _ := io.ReadAll(request.Body)
		var input checkapi.CreateCredentialInput
		_ = json.Unmarshal(body, &input)
		answer(writer, input, result.output)
	}))
	defer server.Close()
	_, result.err = captureStdout(func() error {
		return helperCredentialCommand([]string{
			"create", "--label", "laptop", "--output", result.output,
			"--server", server.URL, "--accept-insecure-http", "--repository", "project", "--password-file", passwordFile,
		})
	})
	return result
}
