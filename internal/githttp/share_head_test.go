package githttp

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// A share address shows no kept history, and a detached HEAD can name a commit
// that only kept history reaches, so the link advertises and serves HEAD only
// when it names a tip that is in scope: a branch tip, a lightweight tag
// target, or the commit an annotated tag points at. HEAD on kept history stays
// hidden. The owner's address keeps answering HEAD as it is.
func TestShareScopeShowsHEADOnlyAsABranchOrTagTip(t *testing.T) {
	manager, runner := newHTTPTestRepository(t)
	handler, err := New(runner, manager, "")
	noErr(t, err)
	handler.Authorize = func(*http.Request) (bool, error) { return true, nil }
	main := httptest.NewServer(handler)
	defer main.Close()
	const sharePrefix = "/s/sample.git/"
	share := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handler.ServeRead(w, r, "sample", strings.TrimPrefix(r.URL.Path, sharePrefix))
	}))
	defer share.Close()

	remote, err := manager.Path("sample")
	noErr(t, err)
	work := t.TempDir()
	runHTTPGit(t, "", "init", "-q", "--initial-branch=main", work)
	commit := func(message string) string {
		noErr(t, os.WriteFile(filepath.Join(work, message+".txt"), []byte(message+"\n"), 0o600))
		runHTTPGit(t, work, "add", ".")
		runHTTPGit(t, work, "-c", "user.name=Fetch Bound", "-c", "user.email=bound@example.invalid", "commit", "-q", "-m", message)
		return httpGitOutput(t, work, "rev-parse", "HEAD")
	}
	runHTTPGit(t, work, "remote", "add", "origin", main.URL+"/git/sample.git")
	tip := commit("branch tip")
	runHTTPGit(t, work, "push", "-q", "origin", "main")
	// One tag reaches each of these commits: one lightweight, one annotated,
	// so the commit an annotated tag points at is not a ref target.
	lightTag := commit("light tag")
	runHTTPGit(t, work, "tag", "light", lightTag)
	runHTTPGit(t, work, "push", "-q", "origin", "refs/tags/light")
	annotatedTag := commit("annotated tag")
	runHTTPGit(t, work, "-c", "user.name=Fetch Bound", "-c", "user.email=bound@example.invalid",
		"tag", "-a", "annotated", "-m", "annotated tag", annotatedTag)
	runHTTPGit(t, work, "push", "-q", "origin", "refs/tags/annotated")
	// The branch is replaced: no branch or tag reaches the last commit, and the
	// repository's HEAD names it directly, as a repository whose branch was
	// deleted and kept can.
	kept := commit("kept history")
	runHTTPGit(t, work, "push", "-q", "origin", kept+":refs/heads/temporary")
	runHTTPGit(t, "", "--git-dir", remote, "update-ref", "refs/owngit/retained/heads/"+kept, kept)
	runHTTPGit(t, "", "--git-dir", remote, "update-ref", "-d", "refs/heads/temporary")
	if names := httpGitOutput(t, "", "--git-dir", remote, "rev-parse", "refs/tags/annotated"); names == annotatedTag {
		t.Fatalf("the fixture's annotated tag names the commit directly")
	}
	if reached := httpGitOutput(t, "", "--git-dir", remote, "rev-parse", "refs/tags/annotated^{}"); reached != annotatedTag {
		t.Fatalf("the fixture's annotated tag points at %s, want %s", reached, annotatedTag)
	}
	// Nothing else reaches the kept commit, and HEAD is the only thing that
	// names it.
	named := httpGitOutput(t, "", "--git-dir", remote, "for-each-ref", "--format=%(objectname) %(*objectname)", "refs/heads", "refs/tags")
	if strings.Contains(named, kept) {
		t.Fatalf("a branch or tag names the commit that only kept history reaches:\n%s", named)
	}

	advertisement := func(address string) string {
		output, err := httpGitCombined("", "ls-remote", address)
		noErr(t, err)
		return output
	}
	post := func(address, body string) (int, string) {
		request, err := http.NewRequest(http.MethodPost, address+"git-upload-pack", strings.NewReader(body))
		noErr(t, err)
		request.Header.Set("Content-Type", "application/x-git-upload-pack-request")
		request.Header.Set("Git-Protocol", "version=2")
		response, err := http.DefaultClient.Do(request)
		noErr(t, err)
		defer response.Body.Close()
		answer, err := io.ReadAll(response.Body)
		noErr(t, err)
		return response.StatusCode, string(answer)
	}
	ownerAddress, shareAddress := main.URL+"/git/sample.git/", share.URL+sharePrefix
	serves := func(address, object string) bool {
		status, answer := post(address, fetchRequestBody(true, object))
		if status != http.StatusOK {
			t.Fatalf("%s: status=%d answer=%q", address, status, answer)
		}
		return strings.Contains(answer, "PACK")
	}
	for _, state := range []struct {
		name  string
		head  string
		shown bool
	}{
		{name: "detached at the branch tip", head: tip, shown: true},
		{name: "detached at a lightweight tag target", head: lightTag, shown: true},
		{name: "detached at an annotated tag's commit", head: annotatedTag, shown: true},
		{name: "detached at kept history", head: kept, shown: false},
	} {
		t.Run(state.name, func(t *testing.T) {
			runHTTPGit(t, "", "--git-dir", remote, "update-ref", "--no-deref", "HEAD", state.head)
			if output := advertisement(shareAddress); strings.Contains(output, "HEAD") != state.shown {
				t.Errorf("the share address shows HEAD=%v, want %v:\n%s", !state.shown, state.shown, output)
			}
			if serves(shareAddress, state.head) != state.shown {
				status, answer := post(shareAddress, fetchRequestBody(true, state.head))
				t.Errorf("the share address serving %s disagrees with its advertisement: status=%d answer=%q", state.head, status, answer)
			}
			if output := advertisement(ownerAddress); !strings.Contains(output, "HEAD") || !serves(ownerAddress, state.head) {
				t.Errorf("the owner's address does not show and serve its own HEAD %s:\n%s", state.head, output)
			}
		})
	}
	// HEAD names a branch tip again, and the share address shows and serves it.
	runHTTPGit(t, "", "--git-dir", remote, "symbolic-ref", "HEAD", "refs/heads/main")
	if output := advertisement(shareAddress); !strings.Contains(output, "HEAD") {
		t.Fatalf("the share address does not show a HEAD on a branch:\n%s", output)
	}
	if !serves(shareAddress, tip) {
		t.Fatalf("the share address refused the branch tip its HEAD names")
	}

	// When the HEAD check itself fails, the address refuses the request before
	// the backend runs: it cannot tell whether HEAD is kept history, and a
	// guess would show or serve a commit the share must not reach.
	t.Run("HEAD check failure", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("the failing wrapper is a Unix test fixture")
		}
		logs := captureLog(t)
		realGit, err := exec.LookPath("git")
		noErr(t, err)
		directory := t.TempDir()
		wrapper := filepath.Join(directory, "git")
		script := "#!/bin/sh\nfor a in \"$@\"; do if test \"$a\" = show-ref; then echo 'fatal: simulated storage failure' >&2; exit 128; fi; done\nexec " + quoteShell(realGit) + " \"$@\"\n"
		noErr(t, os.WriteFile(wrapper, []byte(script), 0o700))
		previous := handler.Git.GitPath
		handler.Git.GitPath = wrapper
		t.Cleanup(func() { handler.Git.GitPath = previous })
		// Even a HEAD that the address would show is refused, because the check
		// did not say so.
		runHTTPGit(t, "", "--git-dir", remote, "update-ref", "--no-deref", "HEAD", tip)
		status, answer := post(shareAddress, fetchRequestBody(true, tip))
		if status != http.StatusServiceUnavailable || strings.Contains(answer, "PACK") {
			t.Errorf("status=%d answer=%q, want a refused request", status, answer)
		}
		if body := strings.TrimSpace(answer); !strings.Contains(body, http.StatusText(http.StatusServiceUnavailable)) {
			t.Errorf("body=%q, want the status text only", body)
		}
		if !strings.Contains(logs.String(), `Git fetch request for repository "sample" could not check its HEAD`) ||
			!strings.Contains(logs.String(), "simulated storage failure") {
			t.Errorf("the failure was not logged with its cause; log:\n%s", logs.String())
		}
		if output := advertisement(ownerAddress); !strings.Contains(output, "HEAD") {
			t.Errorf("the owner's address depends on the share check:\n%s", output)
		}
	})
}
