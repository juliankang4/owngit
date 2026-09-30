package main

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"owngit/internal/auth"
	"owngit/internal/backups"
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
	gitHandler, err := githttp.New(runner, manager, "")
	noErr(t, err)
	hosts := server.NewHostPolicy()
	application := &server.App{
		Store: store, Auth: &auth.Manager{Store: store}, Repositories: manager,
		PullRequests: &pullrequest.Service{Store: store, Repositories: manager},
		GitHTTP:      gitHandler, Hosts: hosts,
		Network: server.NewLiveNetwork(server.LiveNetworkConfig{Hosts: hosts}),
	}
	gitHandler.Authorize = application.AuthorizeGit
	application.Backups = &backups.Service{Store: store, Repositories: manager}
	noErr(t, application.Backups.Start(ctx))
	t.Cleanup(func() { noErr(t, application.Backups.Stop(context.Background())) })
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

// repo rename renames with the administrator password and prints the
// repository; the old name then answers with where the repository went.
func TestRepoRenameMovesTheRepositoryAndTheOldNameSaysWhere(t *testing.T) {
	serverURL, passwordFile := startRepositoryCLIServer(t, "shared-password")
	remote := []string{"--server", serverURL, "--accept-insecure-http", "--password-file", passwordFile}
	adminFile := filepath.Join(t.TempDir(), "admin-password")
	noErr(t, os.WriteFile(adminFile, []byte("admin-password\n"), 0o600))
	noErr(t, state.ProtectPrivatePath(adminFile, false))
	admin := []string{"--server", serverURL, "--accept-insecure-http", "--password-file", adminFile}
	var created struct{}
	runRepoCommandJSON(t, append([]string{"create", "--name", "tools"}, remote...), &created)

	if got := commandErrorCode(repoCommand(append([]string{"rename", "tools", "Kit"}, remote...))); got != "invalid_admin_credentials" {
		t.Fatalf("rename with the shared password code=%q", got)
	}
	if got := commandErrorCode(repoCommand(append([]string{"rename", "tools"}, admin...))); got != "invalid_arguments" {
		t.Fatalf("rename without a new name code=%q", got)
	}
	var renamed struct {
		OK         bool `json:"ok"`
		Repository struct {
			repositoryCLIItem
			Address string `json:"address"`
		} `json:"repository"`
	}
	runRepoCommandJSON(t, append([]string{"rename", "tools", "Kit"}, admin...), &renamed)
	if !renamed.OK || renamed.Repository.ID != "tools" || renamed.Repository.Name != "Kit" || renamed.Repository.Address != "kit" ||
		renamed.Repository.CloneURL != serverURL+"/git/kit.git" {
		t.Fatalf("renamed=%+v", renamed)
	}
	err := repoCommand(append([]string{"show", "--repository", "tools"}, remote...))
	if got := commandErrorCode(err); got != "repository_moved" || !strings.Contains(err.Error(), "kit") {
		t.Fatalf("show at the old name error=%v code=%q", err, got)
	}
	var shown struct{}
	runRepoCommandJSON(t, append([]string{"show", "--repository", "kit"}, remote...), &shown)
}

// repo share creates a link and prints it once with its secret, lists it
// without the secret, and revokes it, all with the administrator password.
func TestRepoShareCreatesListsAndRevokesLinks(t *testing.T) {
	serverURL, passwordFile := startRepositoryCLIServer(t, "shared-password")
	remote := []string{"--server", serverURL, "--accept-insecure-http", "--password-file", passwordFile}
	root := t.TempDir()
	adminFile := filepath.Join(root, "admin-password")
	linkFile := filepath.Join(root, "link-password")
	for file, content := range map[string]string{adminFile: "admin-password\n", linkFile: "link-password\n"} {
		noErr(t, os.WriteFile(file, []byte(content), 0o600))
		noErr(t, state.ProtectPrivatePath(file, false))
	}
	admin := []string{"--server", serverURL, "--accept-insecure-http", "--repository", "tools", "--password-file", adminFile}
	var repository struct{}
	runRepoCommandJSON(t, append([]string{"create", "--name", "tools"}, remote...), &repository)

	if got := commandErrorCode(repoCommand(append([]string{"share", "create", "--label", "Reviewer"}, append(remote, "--repository", "tools")...))); got != "invalid_admin_credentials" {
		t.Fatalf("create with the shared password code=%q", got)
	}
	if got := commandErrorCode(repoCommand(append([]string{"share", "create", "--label", "Reviewer", "--days", "7", "--until-revoked"}, admin...))); got != "invalid_arguments" {
		t.Fatalf("create with two expiries code=%q", got)
	}
	var created struct {
		OK        bool `json:"ok"`
		ShareLink struct {
			ID          string     `json:"id"`
			Scope       string     `json:"scope"`
			HasPassword bool       `json:"has_password"`
			ExpiresAt   *time.Time `json:"expires_at"`
		} `json:"share_link"`
		URL      string   `json:"url"`
		CloneURL string   `json:"clone_url"`
		Warnings []string `json:"warnings"`
	}
	runRepoCommandJSON(t, append([]string{"share", "create", "--label", "Reviewer", "--scope", "clone", "--until-revoked", "--link-password-file", linkFile}, admin...), &created)
	if !created.OK || created.ShareLink.Scope != "clone" || !created.ShareLink.HasPassword || created.ShareLink.ExpiresAt != nil ||
		!strings.HasPrefix(created.URL, serverURL+"/share/") || created.CloneURL != serverURL+"/share/"+created.ShareLink.ID+".git" || len(created.Warnings) != 2 {
		t.Fatalf("created=%+v", created)
	}
	output, err := captureStdout(func() error { return repoCommand(append([]string{"share", "list"}, admin...)) })
	noErr(t, err)
	secret := strings.TrimPrefix(created.URL, serverURL+"/share/")
	if !strings.Contains(output, created.ShareLink.ID) || strings.Contains(output, secret) || strings.Contains(output, "link-password") {
		t.Fatalf("list output:\n%s", output)
	}
	var revoked struct {
		ShareLink struct {
			State string `json:"state"`
		} `json:"share_link"`
	}
	runRepoCommandJSON(t, append([]string{"share", "revoke", "--id", created.ShareLink.ID}, admin...), &revoked)
	if revoked.ShareLink.State != "revoked" {
		t.Fatalf("revoked=%+v", revoked)
	}
	if got := commandErrorCode(repoCommand(append([]string{"share", "revoke", "--id", created.ShareLink.ID}, admin...))); got != "share_link_not_active" {
		t.Fatalf("second revoke code=%q", got)
	}
}
