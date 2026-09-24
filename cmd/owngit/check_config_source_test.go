package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"owngit/internal/apiclient"
	"owngit/internal/auth"
	"owngit/internal/checkapi"
	"owngit/internal/gitexec"
	"owngit/internal/githttp"
	"owngit/internal/pullrequest"
	"owngit/internal/repository"
	"owngit/internal/server"
	"owngit/internal/state"
)

// startCheckCLIServer serves one repository with a helper credential and
// returns the helper flags, a task identifier, and a committed work tree.
func startCheckCLIServer(t *testing.T) (remoteFlags []string, taskID, work string) {
	t.Helper()
	ctx := context.Background()
	root := t.TempDir()
	stateRoot := filepath.Join(root, "state")
	repositoryRoot := filepath.Join(root, "repositories")
	noErr(t, os.Mkdir(repositoryRoot, 0o700))
	store, err := state.Open(ctx, stateRoot)
	noErr(t, err)
	t.Cleanup(func() { _ = store.Close() })
	adminHash, err := auth.HashPassword("admin-password")
	noErr(t, err)
	noErr(t, store.CompleteSetup(ctx, repositoryRoot, "open", "", adminHash, true))
	runner, err := gitexec.New("", filepath.Join(stateRoot, "runtime"))
	noErr(t, err)
	manager := &repository.Manager{Store: store, Git: runner, Locks: gitexec.NewLocks(), Root: repositoryRoot}
	if _, err := manager.Create(ctx, "project", "check configuration fixture"); err != nil {
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
	t.Cleanup(httpServer.Close)

	token := "synthetic-helper-token"
	hash := sha256.Sum256([]byte(token))
	if _, _, err := store.CreateHelperCredential(ctx, "project", "cli", "", hash[:], time.Now()); err != nil {
		t.Fatal(err)
	}
	credentialFile := filepath.Join(root, "helper-token")
	noErr(t, os.WriteFile(credentialFile, []byte(token+"\n"), 0o600))
	noErr(t, state.ProtectPrivatePath(credentialFile, false))
	remoteFlags = []string{"--server", httpServer.URL, "--accept-insecure-http", "--repository", "project", "--credential-file", credentialFile}

	taskOutput, err := captureStdout(func() error {
		return checkCommand(append([]string{"task", "new", "--title", "configuration source"}, remoteFlags...))
	})
	noErr(t, err)
	var taskResponse checkapi.TaskResponse
	if err := json.Unmarshal([]byte(taskOutput), &taskResponse); err != nil || taskResponse.Task == nil {
		t.Fatalf("task output=%q err=%v", taskOutput, err)
	}

	work = filepath.Join(root, "work")
	runPRGit(t, "", "init", "--initial-branch=main", work)
	runPRGit(t, work, "config", "user.name", "Check Test")
	runPRGit(t, work, "config", "user.email", "check-test@example.invalid")
	noErr(t, os.WriteFile(filepath.Join(work, "file.txt"), []byte("base\n"), 0o600))
	runPRGit(t, work, "add", ".")
	runPRGit(t, work, "commit", "-m", "base")
	return remoteFlags, taskResponse.Task.ID, work
}

func writeCommittedChecks(t *testing.T, work, content string) {
	t.Helper()
	noErr(t, os.MkdirAll(filepath.Join(work, ".owngit"), 0o700))
	noErr(t, os.WriteFile(filepath.Join(work, ".owngit", "checks.json"), []byte(content), 0o600))
	runPRGit(t, work, "add", ".owngit/checks.json")
	runPRGit(t, work, "commit", "-m", "checks")
}

func markerExists(t *testing.T, work, name string) bool {
	t.Helper()
	_, err := os.Stat(filepath.Join(work, name))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	return err == nil
}

// Without --check, check run runs only the checks committed in the revision it
// tests. A configuration recorded on the server from another revision, or an
// uncommitted edit, never selects the commands.
func TestCheckRunUsesOnlyTheTestedRevisionConfiguration(t *testing.T) {
	remoteFlags, taskID, work := startCheckCLIServer(t)

	// Another revision records a different configuration on the server.
	if output, err := captureStdout(func() error {
		return checkCommand(append([]string{"run", "--task", taskID, "--workdir", work, "--check", "other=echo other > other-marker"}, remoteFlags...))
	}); err != nil {
		t.Fatalf("recording run error=%v output=%s", err, output)
	}
	noErr(t, os.Remove(filepath.Join(work, "other-marker")))

	// A revision without a committed file refuses instead of reusing it.
	_, err := captureStdout(func() error {
		return checkCommand(append([]string{"run", "--task", taskID, "--workdir", work}, remoteFlags...))
	})
	var problem *apiclient.Error
	if !errors.As(err, &problem) || problem.Code != "checks_not_configured" {
		t.Fatalf("run without a committed configuration err=%v", err)
	}
	if markerExists(t, work, "other-marker") {
		t.Fatal("a configuration recorded for another revision ran")
	}

	writeCommittedChecks(t, work, `{"version":1,"events":{"push":{}},"checks":[{"name":"own","command":"echo own > own-marker"}]}`)
	// An uncommitted edit does not change what runs for the revision.
	noErr(t, os.WriteFile(filepath.Join(work, ".owngit", "checks.json"),
		[]byte(`{"version":1,"events":{"push":{}},"checks":[{"name":"edited","command":"echo edited > edited-marker"}]}`), 0o600))
	output, err := captureStdout(func() error {
		return checkCommand(append([]string{"run", "--task", taskID, "--workdir", work}, remoteFlags...))
	})
	if err != nil {
		t.Fatalf("committed configuration run error=%v output=%s", err, output)
	}
	var result checkRunOutput
	noErr(t, json.Unmarshal([]byte(output), &result))
	if !result.Uploaded || len(result.Results) != 1 || result.Results[0].Name != "own" {
		t.Fatalf("committed configuration result=%+v", result)
	}
	if !markerExists(t, work, "own-marker") || markerExists(t, work, "other-marker") || markerExists(t, work, "edited-marker") {
		t.Fatal("a command outside the committed configuration ran")
	}

	// A local-only run reads the same committed file and needs no server.
	noErr(t, os.Remove(filepath.Join(work, "own-marker")))
	if output, err := captureStdout(func() error {
		return checkCommand([]string{"run", "--task", "local", "--workdir", work, "--no-upload"})
	}); err != nil || !markerExists(t, work, "own-marker") {
		t.Fatalf("local run error=%v output=%s", err, output)
	}

	// An invalid committed file is refused before anything runs.
	noErr(t, os.Remove(filepath.Join(work, "own-marker")))
	writeCommittedChecks(t, work, `{"version":1,"checks":[{"name":"own","command":"echo own > own-marker"}]}`)
	_, err = captureStdout(func() error {
		return checkCommand(append([]string{"run", "--task", taskID, "--workdir", work}, remoteFlags...))
	})
	if !errors.As(err, &problem) || problem.Code != "invalid_check_configuration" || markerExists(t, work, "own-marker") {
		t.Fatalf("invalid committed configuration err=%v", err)
	}
}

// Invalid limits and a definite server refusal stop the run before any check
// executes, and neither is reported as an unconfirmed registration.
func TestCheckRunRefusesBeforeRunning(t *testing.T) {
	remoteFlags, taskID, work := startCheckCLIServer(t)
	check := "marker=echo ran > ran-marker"
	for _, limit := range [][]string{
		{"--timeout", "0"}, {"--timeout", "-1s"}, {"--output-limit", "0"}, {"--output-limit", "-5"},
	} {
		arguments := append([]string{"run", "--task", taskID, "--workdir", work, "--check", check}, limit...)
		_, err := captureStdout(func() error { return checkCommand(append(arguments, remoteFlags...)) })
		var problem *apiclient.Error
		if !errors.As(err, &problem) || problem.Code != "invalid_arguments" || markerExists(t, work, "ran-marker") {
			t.Fatalf("limit %v err=%v", limit, err)
		}
	}

	// An unreserved cycle is a definite refusal.
	_, err := captureStdout(func() error {
		return checkCommand(append([]string{"run", "--task", taskID, "--workdir", work, "--check", check, "--cycle", "0123456789abcdef0123456789abcdef"}, remoteFlags...))
	})
	var exit *checkExit
	var problem *apiclient.Error
	if errors.As(err, &exit) || !errors.As(err, &problem) || problem.Status < 400 || problem.Status >= 500 {
		t.Fatalf("unreserved cycle err=%v", err)
	}
	if markerExists(t, work, "ran-marker") {
		t.Fatal("the check ran after the server refused the registration")
	}
}
