package main

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"owngit/internal/auth"
	"owngit/internal/gitexec"
	"owngit/internal/githttp"
	"owngit/internal/pullrequest"
	"owngit/internal/repository"
	"owngit/internal/server"
	"owngit/internal/state"
)

// startRepositoryCLIServer starts a configured server with no repositories.
// With a shared password it returns a private file that holds it.
func startRepositoryCLIServer(t *testing.T, sharedPassword string) (serverURL, passwordFile string) {
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
	mode, accessHash := "open", ""
	if sharedPassword != "" {
		mode = "password"
		accessHash, err = auth.HashPassword(sharedPassword)
		noErr(t, err)
	}
	noErr(t, store.CompleteSetup(ctx, repositoryRoot, mode, accessHash, adminHash, true))
	runner, err := gitexec.New("", filepath.Join(stateRoot, "runtime"))
	noErr(t, err)
	manager := &repository.Manager{Store: store, Git: runner, Locks: gitexec.NewLocks(), Root: repositoryRoot}
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
	t.Cleanup(application.StopBackground)
	if sharedPassword != "" {
		passwordFile = filepath.Join(root, "shared-password")
		noErr(t, os.WriteFile(passwordFile, []byte(sharedPassword+"\n"), 0o600))
		noErr(t, state.ProtectPrivatePath(passwordFile, false))
	}
	return httpServer.URL, passwordFile
}

type repositoryCLIItem struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	Description   string `json:"description"`
	CloneURL      string `json:"clone_url"`
	DefaultBranch string `json:"default_branch"`
}

func runRepoCommandJSON(t *testing.T, arguments []string, result any) {
	t.Helper()
	output, err := captureStdout(func() error { return repoCommand(arguments) })
	noErr(t, err)
	if err := json.Unmarshal([]byte(output), result); err != nil {
		t.Fatalf("decode repo output: %v\n%s", err, output)
	}
}

func TestRepoCommandsCreateListAndShowRepositories(t *testing.T) {
	serverURL, passwordFile := startRepositoryCLIServer(t, "shared-password")
	remote := []string{"--server", serverURL, "--accept-insecure-http", "--password-file", passwordFile}

	var created struct {
		OK         bool              `json:"ok"`
		Repository repositoryCLIItem `json:"repository"`
	}
	runRepoCommandJSON(t, append([]string{"create", "--name", "Tools", "--description", "CLI fixture"}, remote...), &created)
	if !created.OK || created.Repository.ID != "tools" || created.Repository.Description != "CLI fixture" || created.Repository.CloneURL != serverURL+"/git/tools.git" {
		t.Fatalf("created=%+v", created)
	}
	var listed struct {
		OK           bool                `json:"ok"`
		Repositories []repositoryCLIItem `json:"repositories"`
		Truncated    bool                `json:"truncated"`
	}
	runRepoCommandJSON(t, append([]string{"list"}, remote...), &listed)
	if !listed.OK || listed.Truncated || len(listed.Repositories) != 1 || listed.Repositories[0].ID != "tools" {
		t.Fatalf("listed=%+v", listed)
	}
	var shown struct {
		OK         bool              `json:"ok"`
		Repository repositoryCLIItem `json:"repository"`
	}
	runRepoCommandJSON(t, append([]string{"show", "--repository", "tools"}, remote...), &shown)
	if !shown.OK || shown.Repository.Name != "Tools" {
		t.Fatalf("shown=%+v", shown)
	}

	err := repoCommand(append([]string{"create", "--name", "tools"}, remote...))
	if got := commandErrorCode(err); got != "repository_exists" {
		t.Fatalf("duplicate create error=%v code=%q", err, got)
	}
	err = repoCommand(append([]string{"show", "--repository", "missing"}, remote...))
	if got := commandErrorCode(err); got != "repository_not_found" {
		t.Fatalf("missing show error=%v code=%q", err, got)
	}
	err = repoCommand([]string{"list", "--server", serverURL, "--accept-insecure-http"})
	if got := commandErrorCode(err); got != "authentication_required" {
		t.Fatalf("list without password error=%v code=%q", err, got)
	}
	err = repoCommand([]string{"create", "--server", serverURL, "--accept-insecure-http"})
	if got := commandErrorCode(err); got != "invalid_arguments" {
		t.Fatalf("create without name error=%v code=%q", err, got)
	}
}
