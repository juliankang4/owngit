package main

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"owngit/internal/pullrequest"
)

// runDiffCommand returns what owngit pr diff printed on standard output and
// standard error.
func runDiffCommand(t *testing.T, arguments ...string) (stdout, stderr string, err error) {
	t.Helper()
	stderr, _ = captureStderr(func() error {
		stdout, err = captureStdout(func() error { return prCommand(append([]string{"diff"}, arguments...)) })
		return nil
	})
	return stdout, stderr, err
}

func TestPRDiffPrintsTheServerDiffAndCompactForms(t *testing.T) {
	serverURL, _ := startRepositoryCLIServer(t, "")
	remote := []string{"--server", serverURL, "--accept-insecure-http", "--repository", "project"}
	var created struct {
		OK bool `json:"ok"`
	}
	runRepoCommandJSON(t, []string{"create", "--name", "project", "--server", serverURL, "--accept-insecure-http"}, &created)

	work := filepath.Join(t.TempDir(), "work")
	runPRGit(t, "", "init", "--initial-branch=main", work)
	runPRGit(t, work, "config", "user.name", "CLI Test")
	runPRGit(t, work, "config", "user.email", "cli-test@example.invalid")
	commit := func(name, content string) string {
		noErr(t, os.WriteFile(filepath.Join(work, name), []byte(content), 0o600))
		runPRGit(t, work, "add", ".")
		runPRGit(t, work, "commit", "-q", "-m", name)
		return prGitOutput(t, work, "rev-parse", "HEAD")
	}
	targetOID := commit("base.txt", "base\n")
	runPRGit(t, work, "remote", "add", "origin", serverURL+"/git/project.git")
	runPRGit(t, work, "push", "-q", "origin", "HEAD:refs/heads/main")
	runPRGit(t, work, "checkout", "-q", "-b", "feature")
	sourceOID := commit("feature.txt", "<feature>\n")
	runPRGit(t, work, "push", "-q", "origin", "HEAD:refs/heads/feature")
	pr := runPRCommandJSON(t, append([]string{"create", "--title", "Feature", "--source", "feature", "--target", "main"}, remote...))
	number := strconv.FormatInt(pr.PullRequest.Number, 10)

	// The default output is the server's JSON, byte for byte.
	stdout, _, err := runDiffCommand(t, append([]string{"--number", number}, remote...)...)
	noErr(t, err)
	response, err := http.Get(serverURL + "/api/v1/repositories/project/pull-requests/" + number + "/diff")
	noErr(t, err)
	body, err := io.ReadAll(response.Body)
	response.Body.Close()
	noErr(t, err)
	if stdout != string(body) {
		t.Fatalf("pr diff printed\n%s\nthe API returned\n%s", stdout, body)
	}
	var diff pullrequest.Diff
	noErr(t, json.Unmarshal([]byte(stdout), &diff))
	if diff.Source.OID != sourceOID || diff.Target.OID != targetOID || diff.MergeBase != targetOID || len(diff.Files) != 1 || !strings.Contains(diff.Patch, "+<feature>\n") {
		t.Fatalf("diff=%+v", diff)
	}

	// --stat is the same object without the patch.
	stat, _, err := runDiffCommand(t, append([]string{"--number", number, "--stat"}, remote...)...)
	noErr(t, err)
	var full, compact map[string]any
	noErr(t, json.Unmarshal([]byte(stdout), &full))
	noErr(t, json.Unmarshal([]byte(stat), &compact))
	if _, ok := compact["patch"]; ok || !strings.HasSuffix(stat, "}\n") {
		t.Fatalf("--stat kept the patch: %s", stat)
	}
	delete(full, "patch")
	if !reflect.DeepEqual(full, compact) {
		t.Fatalf("--stat changed the result:\n%s\n%s", stdout, stat)
	}

	// --patch prints the bare patch and notes the revisions on stderr.
	patch, notes, err := runDiffCommand(t, append([]string{"--number", number, "--patch"}, remote...)...)
	noErr(t, err)
	if patch != diff.Patch {
		t.Fatalf("--patch printed %q, want %q", patch, diff.Patch)
	}
	if notes != "owngit: source "+sourceOID+", target "+targetOID+", merge base "+targetOID+"\n" {
		t.Fatalf("--patch notes=%q", notes)
	}

	// After the source moves, a pinned recorded pair is still diffed and
	// the move is noted.
	movedOID := commit("second.txt", "second\n")
	runPRGit(t, work, "push", "-q", "origin", "HEAD:refs/heads/feature")
	patch, notes, err = runDiffCommand(t, append([]string{"--number", number, "--patch", "--source-oid", sourceOID, "--target-oid", targetOID}, remote...)...)
	noErr(t, err)
	if patch != diff.Patch || !strings.Contains(notes, "owngit: the branches moved; the current source is "+movedOID+" and the current target is "+targetOID+"\n") {
		t.Fatalf("pinned --patch printed %q with notes %q", patch, notes)
	}

	for _, refused := range []struct {
		arguments []string
		code      string
	}{
		{[]string{"--number", number, "--source-oid", sourceOID}, "invalid_arguments"},
		{[]string{"--number", number, "--target-oid", targetOID}, "invalid_arguments"},
		{[]string{"--number", number, "--stat", "--patch"}, "invalid_arguments"},
		{[]string{"--number", "0"}, "invalid_arguments"},
		{[]string{"--number", number, "--source-oid", targetOID, "--target-oid", targetOID}, "revision_not_recorded"},
		{[]string{"--number", "99"}, "pull_request_not_found"},
	} {
		stdout, _, err := runDiffCommand(t, append(refused.arguments, remote...)...)
		if got := commandErrorCode(err); got != refused.code || stdout != "" {
			t.Errorf("%v: code=%q stdout=%q, want %s", refused.arguments, got, stdout, refused.code)
		}
	}
}
