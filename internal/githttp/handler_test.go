package githttp

import (
	"bytes"
	"context"
	"crypto/rand"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"owngit/internal/gitexec"
	"owngit/internal/repository"
	"owngit/internal/state"
)

func TestSmartHTTPNormalAndChunkedPushCloneFetch(t *testing.T) {
	manager, runner := newHTTPTestRepository(t)
	handler, err := New(runner, manager, "", 2)
	noErr(t, err)
	handler.Authorize = func(*http.Request) bool { return true }
	var sawChunked atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method == http.MethodPost && len(request.TransferEncoding) == 1 && request.TransferEncoding[0] == "chunked" {
			sawChunked.Store(true)
		}
		handler.ServeHTTP(writer, request)
	}))
	defer server.Close()

	work := filepath.Join(t.TempDir(), "work")
	runHTTPGit(t, "", "init", "--initial-branch=main", work)
	runHTTPGit(t, work, "config", "user.name", "HTTP Test")
	runHTTPGit(t, work, "config", "user.email", "http@example.invalid")
	noErr(t, os.WriteFile(filepath.Join(work, "README.md"), []byte("normal push\n"), 0o600))
	runHTTPGit(t, work, "add", "README.md")
	runHTTPGit(t, work, "commit", "-m", "normal")
	remoteURL := server.URL + "/git/sample.git"
	runHTTPGit(t, work, "remote", "add", "origin", remoteURL)
	runHTTPGit(t, work, "push", "origin", "HEAD:refs/heads/main")

	large := make([]byte, 12<<20)
	if _, err := rand.Read(large); err != nil {
		t.Fatal(err)
	}
	noErr(t, os.WriteFile(filepath.Join(work, "large.bin"), large, 0o600))
	runHTTPGit(t, work, "add", "large.bin")
	runHTTPGit(t, work, "commit", "-m", "chunked")
	runHTTPGit(t, work, "-c", "http.postBuffer=1", "push", "origin", "HEAD:refs/heads/main")
	if !sawChunked.Load() {
		t.Fatal("real Git push did not reach the backend with chunked transfer encoding")
	}

	clone := filepath.Join(t.TempDir(), "clone")
	runHTTPGit(t, "", "clone", remoteURL, clone)
	content, err := os.ReadFile(filepath.Join(clone, "large.bin"))
	noErr(t, err)
	if !bytes.Equal(content, large) {
		t.Fatal("clone did not contain the exact noncompressible push content")
	}

	fetchedPayload := make([]byte, 12<<20)
	if _, err := rand.Read(fetchedPayload); err != nil {
		t.Fatal(err)
	}
	noErr(t, os.WriteFile(filepath.Join(work, "large.bin"), fetchedPayload, 0o600))
	noErr(t, os.WriteFile(filepath.Join(work, "README.md"), []byte("fetched\n"), 0o600))
	runHTTPGit(t, work, "add", "README.md", "large.bin")
	runHTTPGit(t, work, "commit", "-m", "fetch")
	runHTTPGit(t, work, "push", "origin", "HEAD:refs/heads/main")
	runHTTPGit(t, clone, "fetch", "origin")
	remoteHead := httpGitOutput(t, clone, "rev-parse", "origin/main")
	workHead := httpGitOutput(t, work, "rev-parse", "HEAD")
	if remoteHead != workHead {
		t.Fatalf("fetched head %s, want %s", remoteHead, workHead)
	}
	fetchedContent, err := httpGitBytes(clone, "show", "origin/main:large.bin")
	noErr(t, err)
	if !bytes.Equal(fetchedContent, fetchedPayload) {
		t.Fatal("fetch did not contain the exact noncompressible update")
	}

	oldMain := workHead
	runHTTPGit(t, work, "checkout", "--orphan", "replacement")
	runHTTPGit(t, work, "rm", "-rf", ".")
	noErr(t, os.WriteFile(filepath.Join(work, "replacement.txt"), []byte("replacement\n"), 0o600))
	runHTTPGit(t, work, "add", ".")
	runHTTPGit(t, work, "commit", "-m", "replacement root")
	replacement := httpGitOutput(t, work, "rev-parse", "HEAD")
	runHTTPGit(t, work, "-c", "http.postBuffer=1", "push", "--force", "origin", "HEAD:refs/heads/main")
	runHTTPGit(t, work, "push", "origin", "HEAD:refs/heads/delete-me")
	runHTTPGit(t, work, "push", "origin", ":refs/heads/delete-me")
	runHTTPGit(t, work, "tag", "-a", "release", "-m", "release", replacement)
	oldTag := httpGitOutput(t, work, "rev-parse", "refs/tags/release")
	runHTTPGit(t, work, "push", "origin", "refs/tags/release")
	runHTTPGit(t, work, "tag", "-f", "-a", "release", "-m", "replaced release", oldMain)
	runHTTPGit(t, work, "push", "--force", "origin", "refs/tags/release")
	newTag := httpGitOutput(t, work, "rev-parse", "refs/tags/release")
	runHTTPGit(t, work, "push", "origin", ":refs/tags/release")

	retained, err := manager.RetainedRefs(context.Background(), "sample")
	noErr(t, err)
	retainedOIDs := make(map[string]string)
	for _, ref := range retained {
		retainedOIDs[ref.OID] = ref.Kind
	}
	for oid, kind := range map[string]string{oldMain: "branch", replacement: "branch", oldTag: "tag", newTag: "tag"} {
		if retainedOIDs[oid] != kind {
			t.Errorf("retained %s kind=%q, want %q; all=%+v", oid, retainedOIDs[oid], kind, retained)
		}
	}
	remotePath, err := manager.Path("sample")
	noErr(t, err)
	runHTTPGit(t, "", "--git-dir", remotePath, "reflog", "expire", "--expire=now", "--all")
	runHTTPGit(t, "", "--git-dir", remotePath, "gc", "--prune=now")
	for _, oid := range []string{oldMain, replacement, oldTag, newTag} {
		runHTTPGit(t, "", "--git-dir", remotePath, "cat-file", "-e", oid+"^{object}")
	}
}

func TestSmartHTTPGatesEveryEndpointAndRejectsDumbPaths(t *testing.T) {
	manager, runner := newHTTPTestRepository(t)
	handler, err := New(runner, manager, "", 1)
	noErr(t, err)
	handler.Authorize = func(*http.Request) bool { return false }
	server := httptest.NewServer(handler)
	defer server.Close()

	for _, target := range []string{
		"/git/sample.git/info/refs?service=git-upload-pack",
		"/git/sample.git/info/refs?service=git-receive-pack",
		"/git/sample.git/git-upload-pack",
		"/git/sample.git/git-receive-pack",
	} {
		method := http.MethodGet
		var request *http.Request
		if strings.HasSuffix(target, "-pack") {
			method = http.MethodPost
		}
		request, _ = http.NewRequest(method, server.URL+target, strings.NewReader("0000"))
		if method == http.MethodPost {
			service := target[strings.LastIndex(target, "/")+1:]
			request.Header.Set("Content-Type", "application/x-"+service+"-request")
		}
		response, err := http.DefaultClient.Do(request)
		noErr(t, err)
		response.Body.Close()
		if response.StatusCode != http.StatusUnauthorized {
			t.Errorf("%s %s status=%d, want 401", method, target, response.StatusCode)
		}
	}

	handler.Authorize = func(*http.Request) bool { return true }
	for _, target := range []string{
		"/git/sample.git/HEAD",
		"/git/sample.git/objects/info/packs",
		"/git/sample.git/info/refs",
		"/git/sample.git/info/refs?service=git-upload-pack&service=git-receive-pack",
	} {
		response, err := http.Get(server.URL + target)
		noErr(t, err)
		response.Body.Close()
		if response.StatusCode != http.StatusNotFound {
			t.Errorf("GET %s status=%d, want 404", target, response.StatusCode)
		}
	}
}

func newHTTPTestRepository(t *testing.T) (*repository.Manager, *gitexec.Runner) {
	t.Helper()
	root := t.TempDir()
	store, err := state.Open(context.Background(), filepath.Join(root, "state"))
	noErr(t, err)
	t.Cleanup(func() { _ = store.Close() })
	runner, err := gitexec.New("", filepath.Join(root, "runtime"))
	noErr(t, err)
	repositoryRoot := filepath.Join(root, "repositories")
	noErr(t, os.Mkdir(repositoryRoot, 0o700))
	manager := &repository.Manager{Store: store, Git: runner, Locks: gitexec.NewLocks(), Root: repositoryRoot}
	if _, err := manager.Create(context.Background(), "sample", ""); err != nil {
		t.Fatal(err)
	}
	return manager, runner
}

func runHTTPGit(t *testing.T, directory string, arguments ...string) {
	t.Helper()
	if output, err := httpGitCombined(directory, arguments...); err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(arguments, " "), err, output)
	}
}

func httpGitOutput(t *testing.T, directory string, arguments ...string) string {
	t.Helper()
	output, err := httpGitCombined(directory, arguments...)
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(arguments, " "), err, output)
	}
	return strings.TrimSpace(output)
}

func httpGitCombined(directory string, arguments ...string) (string, error) {
	output, err := httpGitBytes(directory, arguments...)
	return string(output), err
}

func httpGitBytes(directory string, arguments ...string) ([]byte, error) {
	command := exec.Command("git", arguments...)
	command.Dir = directory
	command.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	return command.CombinedOutput()
}
