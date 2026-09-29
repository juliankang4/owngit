package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"owngit/internal/state"
	"owngit/internal/testfixture"
)

// The command line and the MCP tools list kept history and preview and
// apply a restore with the same JSON. A restore onto the protected default
// branch adds a commit on its tip, and a preview is not consent to a later
// tip.
func TestRepoRestoreCommandsAndMCPTools(t *testing.T) {
	serverURL, _ := startRepositoryCLIServer(t, "")
	remote := []string{"--server", serverURL, "--accept-insecure-http", "--repository", "project"}
	cliOutput(t, repoCommand, "create", "--name", "project", "--server", serverURL, "--accept-insecure-http")
	work := filepath.Join(t.TempDir(), "work")
	runPRGit(t, "", "init", "--initial-branch=main", work)
	runPRGit(t, work, "config", "user.name", "Restore Test")
	runPRGit(t, work, "config", "user.email", "restore-test@example.invalid")
	runPRGit(t, work, "remote", "add", "origin", serverURL+"/git/project.git")
	commitFile(t, work, "file.txt", "first\n")
	first := prGitOutput(t, work, "rev-parse", "HEAD")
	commitFile(t, work, "file.txt", "kept\n")
	kept := prGitOutput(t, work, "rev-parse", "HEAD")
	runPRGit(t, work, "push", "-q", "origin", "HEAD:refs/heads/main")
	runPRGit(t, work, "reset", "-q", "--hard", first)
	commitFile(t, work, "other.txt", "other\n")
	current := prGitOutput(t, work, "rev-parse", "HEAD")
	runPRGit(t, work, "push", "-q", "--force", "origin", "HEAD:refs/heads/main")

	adminFile := filepath.Join(t.TempDir(), "admin-password")
	noErr(t, os.WriteFile(adminFile, []byte("admin-password\n"), 0o600))
	noErr(t, state.ProtectPrivatePath(adminFile, false))
	cliOutput(t, repoCommand, append([]string{"settings", "set", "--password-file", adminFile, "--protect-default-branch", "on"}, remote...)...)
	runPRGit(t, work, "commit", "-q", "--amend", "-m", "rewritten")
	refused := exec.Command("git", "push", "-q", "--force", "origin", "HEAD:refs/heads/main")
	refused.Dir = work
	refused.Env = testfixture.GitEnvironment(append(os.Environ(), "GIT_TERMINAL_PROMPT=0"))
	if output, err := refused.CombinedOutput(); err == nil || !strings.Contains(string(output), "protects the default branch") {
		t.Fatalf("force push to the protected default branch: %v\n%s", err, output)
	}

	session := startMCPSession(t, mcpOptions{server: serverURL, repository: "project", acceptInsecureHTTP: true})
	listed := cliOutput(t, repoCommand, append([]string{"kept-history"}, remote...)...)
	if text, isError := session.call("repository_kept_history", nil); isError || text != listed {
		t.Fatalf("repository_kept_history isError=%v\n got %s\nwant %s", isError, text, listed)
	}
	var history struct {
		KeptHistory []struct {
			SourceRef string `json:"source_ref"`
			CommitOID string `json:"commit_oid"`
		} `json:"kept_history"`
	}
	noErr(t, json.Unmarshal([]byte(listed), &history))
	if len(history.KeptHistory) != 1 || history.KeptHistory[0].CommitOID != kept || history.KeptHistory[0].SourceRef != "refs/heads/main" {
		t.Fatalf("kept history %s", listed)
	}

	previewed := cliOutput(t, repoCommand, append([]string{"restore", "preview", "--source", kept, "--target", "main", "--path", "file.txt"}, remote...)...)
	arguments := map[string]any{"source_oid": kept, "target_branch": "main", "paths": []string{"file.txt"}}
	if text, isError := session.call("repository_restore_preview", arguments); isError || text != previewed {
		t.Fatalf("repository_restore_preview isError=%v\n got %s\nwant %s", isError, text, previewed)
	}
	var preview struct {
		Preview struct {
			ExpectedHead string `json:"expected_head"`
			ResultTree   string `json:"result_tree"`
			Changes      []struct {
				Path   string `json:"path"`
				Status string `json:"status"`
			} `json:"changes"`
		} `json:"preview"`
	}
	noErr(t, json.Unmarshal([]byte(previewed), &preview))
	if preview.Preview.ExpectedHead != current || len(preview.Preview.Changes) != 1 || preview.Preview.Changes[0].Path != "file.txt" || preview.Preview.Changes[0].Status != "modified" {
		t.Fatalf("preview %s", previewed)
	}

	arguments["expected_head"] = first
	if code := session.callError("repository_restore_apply", arguments); code != "stale_revision" {
		t.Fatalf("apply at another tip: code %q", code)
	}
	err := repoCommand(append([]string{"restore", "apply", "--source", kept, "--target", "main", "--path", "file.txt"}, remote...))
	if got := commandErrorCode(err); got != "invalid_arguments" {
		t.Fatalf("apply without --expected-head: %v", err)
	}
	if code := session.callError("repository_restore_apply", map[string]any{"source_oid": kept, "target_branch": "main", "paths": []string{}, "expected_head": current}); code != "invalid_arguments" {
		t.Fatalf("apply with empty paths: code %q", code)
	}

	applied := cliOutput(t, repoCommand, append([]string{"restore", "apply", "--source", kept, "--target", "main", "--path", "file.txt", "--expected-head", current}, remote...)...)
	var result struct {
		OK      bool `json:"ok"`
		Restore struct {
			CommitOID string `json:"commit_oid"`
			Created   bool   `json:"created"`
		} `json:"restore"`
	}
	noErr(t, json.Unmarshal([]byte(applied), &result))
	runPRGit(t, work, "fetch", "-q", "origin", "main")
	restored := prGitOutput(t, work, "rev-parse", "FETCH_HEAD")
	if !result.OK || result.Restore.Created || result.Restore.CommitOID != restored ||
		prGitOutput(t, work, "rev-parse", restored+"^") != current || prGitOutput(t, work, "rev-parse", restored+"^{tree}") != preview.Preview.ResultTree {
		t.Fatalf("apply %s; main is %s", applied, restored)
	}
	arguments["expected_head"] = current
	if code := session.callError("repository_restore_apply", arguments); code != "stale_revision" {
		t.Fatalf("applying the same preview again: code %q", code)
	}
}
