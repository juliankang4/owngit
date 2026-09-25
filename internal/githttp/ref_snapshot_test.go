package githttp

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"owngit/internal/gitexec"
	"owngit/internal/repository"
)

// A push that changes no ref keeps the cached ref snapshot, so the next page
// runs no for-each-ref. Every push that changes a ref, including a partial
// push, a deletion, and a refused atomic push whose retention hook already
// preserved history, makes the next page read the refs again.
func TestPushesInvalidateTheRefSnapshotOnlyWhenRefsMayHaveChanged(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the Git process counter is a POSIX shell wrapper")
	}
	manager, runner := newHTTPTestRepository(t)
	handler, err := New(runner, manager, "", 2)
	noErr(t, err)
	handler.Authorize = func(*http.Request) bool { return true }
	server := httptest.NewServer(handler)
	defer server.Close()
	reads := countSnapshotReads(t, manager)
	ctx := context.Background()
	snapshot := func(wantReads int) repository.RefSnapshot {
		t.Helper()
		result, err := manager.RefSnapshot(ctx, "sample")
		noErr(t, err)
		if got := reads(); got != wantReads {
			t.Fatalf("snapshot for-each-ref runs=%d, want %d", got, wantReads)
		}
		return result
	}

	work := filepath.Join(t.TempDir(), "work")
	runHTTPGit(t, "", "init", "--initial-branch=main", work)
	runHTTPGit(t, work, "config", "user.name", "HTTP Test")
	runHTTPGit(t, work, "config", "user.email", "http@example.invalid")
	noErr(t, os.WriteFile(filepath.Join(work, "README.md"), []byte("one\n"), 0o600))
	runHTTPGit(t, work, "add", "README.md")
	runHTTPGit(t, work, "commit", "-m", "one")
	runHTTPGit(t, work, "remote", "add", "origin", server.URL+"/git/sample.git")
	runHTTPGit(t, work, "push", "origin", "HEAD:refs/heads/main")
	first := snapshot(1)
	snapshot(1)

	// Up to date: the client reads the ref advertisement and sends nothing.
	runHTTPGit(t, work, "push", "origin", "HEAD:refs/heads/main")
	snapshot(1)
	// Refused by the update hook before it writes anything.
	if output, err := httpGitCombined(work, "push", "origin", "HEAD:refs/notes/refused"); err == nil {
		t.Fatalf("push to an unsupported ref succeeded: %s", output)
	}
	snapshot(1)

	// Partial: one ref updated, one refused.
	if output, err := httpGitCombined(work, "push", "origin", "HEAD:refs/heads/feature", "HEAD:refs/notes/refused"); err == nil {
		t.Fatalf("partial push succeeded: %s", output)
	}
	if partial := snapshot(2); len(partial.Summary.Branches) != 2 {
		t.Fatalf("snapshot after a partial push=%+v", partial.Summary.Branches)
	}
	runHTTPGit(t, work, "push", "origin", ":refs/heads/feature")
	if deleted := snapshot(3); len(deleted.Summary.Branches) != 1 {
		t.Fatalf("snapshot after a deletion=%+v", deleted.Summary.Branches)
	}

	// Atomic: the hook for main preserves the replaced commit, then the
	// refused ref fails the whole push, so main keeps its commit but a
	// retained ref was written.
	runHTTPGit(t, work, "checkout", "--orphan", "replacement")
	noErr(t, os.WriteFile(filepath.Join(work, "README.md"), []byte("replacement\n"), 0o600))
	runHTTPGit(t, work, "add", "README.md")
	runHTTPGit(t, work, "commit", "-m", "replacement")
	if output, err := httpGitCombined(work, "push", "--atomic", "--force", "origin", "HEAD:refs/heads/main", "HEAD:refs/notes/refused"); err == nil {
		t.Fatalf("atomic push with a refused ref succeeded: %s", output)
	}
	remotePath, err := manager.Path("sample")
	noErr(t, err)
	retained := httpGitOutput(t, "", "--git-dir", remotePath, "for-each-ref", "--format=%(refname)", "refs/owngit/retained/heads")
	if !strings.Contains(retained, first.Summary.DefaultOID) {
		t.Fatalf("the fixture did not write a retained ref before the atomic failure: %q", retained)
	}
	after := snapshot(4)
	if after.Summary.DefaultOID != first.Summary.DefaultOID || after.ActivityKey == first.ActivityKey {
		t.Fatalf("snapshot after a refused atomic push: main=%s key changed=%v", after.Summary.DefaultOID, after.ActivityKey != first.ActivityKey)
	}
}

// countSnapshotReads makes the manager run Git through a wrapper that counts
// the for-each-ref runs of ref snapshot reads.
func countSnapshotReads(t *testing.T, manager *repository.Manager) func() int {
	t.Helper()
	gitPath, err := exec.LookPath("git")
	noErr(t, err)
	directory := t.TempDir()
	trace := filepath.Join(directory, "trace")
	wrapper := filepath.Join(directory, "git")
	script := "#!/bin/sh\ncase \"$*\" in *for-each-ref*refs/owngit/provenance/heads*) echo read >> '" + trace + "';; esac\nexec '" + gitPath + "' \"$@\"\n"
	noErr(t, os.WriteFile(wrapper, []byte(script), 0o700))
	counting, err := gitexec.New(wrapper, filepath.Join(directory, "runtime"))
	noErr(t, err)
	manager.Git = counting
	return func() int {
		t.Helper()
		content, err := os.ReadFile(trace)
		if os.IsNotExist(err) {
			return 0
		}
		noErr(t, err)
		return strings.Count(string(content), "\n")
	}
}
