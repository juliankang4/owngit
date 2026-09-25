//go:build !windows

package githttp

import (
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// A push that Git refuses inside the protocol, where git-http-backend still
// exits 0, logs one line with a fixed reason (QA-023).
func TestRefusedPushIsLoggedWithoutRequestContent(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("file permissions do not stop root")
	}
	manager, runner := newHTTPTestRepository(t)
	handler, err := New(runner, manager, "", 1)
	noErr(t, err)
	handler.Authorize = func(*http.Request) bool { return true }
	server := httptest.NewServer(handler)
	defer server.Close()
	logs := captureLog(t)
	repositoryPath, err := manager.Path("sample")
	noErr(t, err)
	work := filepath.Join(t.TempDir(), "work")
	runHTTPGit(t, "", "init", "--initial-branch=main", work)
	noErr(t, os.WriteFile(filepath.Join(work, "file"), []byte("content\n"), 0o600))
	runHTTPGit(t, work, "add", "file")
	runHTTPGit(t, work, "-c", "user.name=T", "-c", "user.email=t@example.invalid", "commit", "-m", "first")
	remote := server.URL + "/git/sample.git"
	const branch = "HEAD:refs/heads/request-detail-branch"
	push := func() string {
		t.Helper()
		before := len(logs.String())
		if output, err := httpGitCombined(work, "push", remote, branch); err == nil {
			t.Fatalf("push was accepted: %s", output)
		}
		return strings.TrimSpace(logs.String()[before:])
	}

	hook := filepath.Join(repositoryPath, "hooks", "pre-receive")
	noErr(t, os.WriteFile(hook, []byte("#!/bin/sh\necho 'hook output request-detail' >&2\nexit 1\n"), 0o700))
	if line := push(); line != `Git push request for repository "sample" failed: push refused: a server hook declined a ref update` {
		t.Fatalf("hook refusal log %q", line)
	}
	noErr(t, os.Remove(hook))

	objects := filepath.Join(repositoryPath, "objects")
	noErr(t, exec.Command("chmod", "-R", "a-w", objects).Run())
	t.Cleanup(func() { _ = exec.Command("chmod", "-R", "u+w", objects).Run() })
	if line := push(); line != `Git push request for repository "sample" failed: push refused: repository storage is not writable` {
		t.Fatalf("read-only refusal log %q", line)
	}
	noErr(t, exec.Command("chmod", "-R", "u+w", objects).Run())

	before := len(logs.String())
	runHTTPGit(t, work, "push", remote, branch)
	if line := strings.TrimSpace(logs.String()[before:]); line != "" {
		t.Fatalf("successful push logged %q", line)
	}
}
