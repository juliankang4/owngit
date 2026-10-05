package githttp

import (
	"bytes"
	"context"
	"crypto/rand"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"owngit/internal/gitexec"
	"owngit/internal/hostmem"
	"owngit/internal/repository"
	"owngit/internal/state"
	"owngit/internal/state/statetest"
	"owngit/internal/testfixture"
)

func TestSmartHTTPNormalAndChunkedPushCloneFetch(t *testing.T) {
	manager, runner := newHTTPTestRepository(t)
	handler, err := New(runner, manager, "")
	noErr(t, err)
	handler.Authorize = func(*http.Request) (bool, error) { return true, nil }
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
	handler, err := New(runner, manager, "")
	noErr(t, err)
	handler.Authorize = func(*http.Request) (bool, error) { return false, nil }
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

	handler.Authorize = func(*http.Request) (bool, error) { return true, nil }
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

// A push holds the repository write lock only while the handler runs, and
// the Git client finishes only after the handler returned, so the lock is
// free when the push returns and maintenance can start right after it. The
// pause after the handler would let a client that finishes early see the
// push end first.
func TestPushReleasesTheRepositoryBeforeTheClientFinishes(t *testing.T) {
	manager, runner := newHTTPTestRepository(t)
	handler, err := New(runner, manager, "")
	noErr(t, err)
	handler.Authorize = func(*http.Request) (bool, error) { return true, nil }
	var finished atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		handler.ServeHTTP(writer, request)
		if strings.HasSuffix(request.URL.Path, "/git-receive-pack") {
			time.Sleep(200 * time.Millisecond)
			finished.Store(true)
		}
	}))
	defer server.Close()

	work := filepath.Join(t.TempDir(), "work")
	runHTTPGit(t, "", "init", "--initial-branch=main", work)
	runHTTPGit(t, work, "config", "user.name", "HTTP Test")
	runHTTPGit(t, work, "config", "user.email", "http@example.invalid")
	noErr(t, os.WriteFile(filepath.Join(work, "README.md"), []byte("one\n"), 0o600))
	runHTTPGit(t, work, "add", "README.md")
	runHTTPGit(t, work, "commit", "-m", "one")
	runHTTPGit(t, work, "push", server.URL+"/git/sample.git", "HEAD:refs/heads/main")

	lock := manager.Locks.For("sample")
	if !finished.Load() {
		t.Fatal("the push returned before the handler finished")
	}
	if lock.Waiting() || !lock.TryLock() {
		t.Fatal("the repository is still locked after the push returned")
	}
	lock.Unlock()
}

// A request that waits for a repository held by another writer ends at its own
// bound instead of waiting for the writer, and gives its transfer slot and its
// memory gate slot back at once. A fetch holds the read lock, a push the write
// lock.
//
// The bound differs by request kind. A fetch advertisement is a GET with no
// body, so Go's HTTP server watches the connection and cancels the request as
// soon as the client closes it, and the operation limit is the only bound it
// needs. A POST is a fetch or push body that the handler has not read while it
// waits, and the server starts that watch only after the body is consumed, so
// such a request waits for the repository at most for the transfer queue wait
// and then gets the busy answer, instead of holding its slots for the whole
// operation limit after a peer disconnects. Once the lock is held, a transfer
// keeps the operation limit.
func TestServeLockWaitEndsAtTheOperationLimitAndOnClientLeave(t *testing.T) {
	for _, test := range []struct {
		name         string
		operation    time.Duration
		queueWait    time.Duration
		cancelClient bool
		// service is empty for the GET fetch advertisement and names the Smart
		// HTTP POST service otherwise.
		service string
		// wantBound is the bound the server log names, empty when the client left
		// and there is nothing to report.
		wantBound string
	}{
		{name: "operation limit", operation: 150 * time.Millisecond, wantBound: "operation limit"},
		{name: "client cancellation", operation: 30 * time.Second, cancelClient: true},
		{name: "operation limit on a push", operation: 150 * time.Millisecond, service: "git-receive-pack", wantBound: "operation limit"},
		{name: "queue wait on a push", operation: 30 * time.Second, queueWait: 500 * time.Millisecond, service: "git-receive-pack", wantBound: "transfer queue wait"},
		{name: "queue wait on a fetch POST", operation: 30 * time.Second, queueWait: 500 * time.Millisecond, service: "git-upload-pack", wantBound: "transfer queue wait"},
	} {
		t.Run(test.name, func(t *testing.T) {
			manager, runner := newHTTPTestRepository(t)
			handler, err := New(runner, manager, "")
			noErr(t, err)
			useLimits(t, handler, func(limits *Limits) {
				limits.PerRepository, limits.ExtraSlots = 1, 0
				limits.QueueWait = 5 * time.Second
				if test.queueWait > 0 {
					limits.QueueWait = test.queueWait
				}
				limits.Operation = test.operation
				// One memory slot, so a transfer that starts afterwards proves that
				// this request gave both of its slots back.
				limits.Memory = hostmem.NewGate(1)
			})
			server := httptest.NewServer(handler)
			defer server.Close()
			lock := manager.Locks.For("sample")
			lock.Lock()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			// A push or a fetch body is a POST to the service path with that
			// service's content type; without it the path is not a Git route and is
			// refused before admission, so the request never reaches the lock.
			var request *http.Request
			if test.service == "" {
				request, err = http.NewRequestWithContext(ctx, http.MethodGet, server.URL+"/git/sample.git/info/refs?service=git-upload-pack", nil)
				noErr(t, err)
			} else {
				request, err = http.NewRequestWithContext(ctx, http.MethodPost, server.URL+"/git/sample.git/"+test.service, strings.NewReader("0000"))
				noErr(t, err)
				request.Header.Set("Content-Type", "application/x-"+test.service+"-request")
			}
			var status int
			var retryAfter string
			var requestErr error
			// The log names the bound that ended the wait: the shorter of the
			// operation limit and the transfer queue wait that a POST also gets.
			logBuffer := &bytes.Buffer{}
			previousLog := log.Writer()
			log.SetOutput(logBuffer)
			defer log.SetOutput(previousLog)
			done := make(chan struct{})
			go func() {
				defer close(done)
				response, err := http.DefaultClient.Do(request)
				if err != nil {
					requestErr = err
					return
				}
				defer response.Body.Close()
				status, retryAfter = response.StatusCode, response.Header.Get("Retry-After")
			}()
			released := false
			release := func() {
				if released {
					return
				}
				released = true
				lock.Unlock()
				select {
				case <-done:
				case <-time.After(5 * time.Second):
				}
			}
			defer release()
			waitFor(t, 5*time.Second, "the request to wait for the repository writer", lock.Waiting)
			if test.cancelClient {
				cancel()
			}
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Fatal("the request still waits for the writer")
			}
			if test.cancelClient {
				if requestErr == nil {
					t.Fatalf("the request ended as %d after its client left", status)
				}
			} else if requestErr != nil || status != http.StatusServiceUnavailable || retryAfter == "" {
				t.Fatalf("the request ended as status=%d retry-after=%q err=%v, want the busy answer", status, retryAfter, requestErr)
			}
			waitFor(t, 5*time.Second, "the handler to release its slots", func() bool { return handler.Active() == 0 })
			if test.wantBound == "" {
				if logged := logBuffer.String(); logged != "" {
					t.Fatalf("the server logged %q for a client that left, want nothing", logged)
				}
			} else if !strings.Contains(logBuffer.String(), "stayed locked for the whole "+test.wantBound) {
				t.Fatalf("the server logged %q, want the bound %q", logBuffer.String(), test.wantBound)
			}
			release()
			httpGitOutput(t, "", "ls-remote", server.URL+"/git/sample.git")
		})
	}
}

// A Git request that ran out its repository wait is answered with the plain
// busy message. A Git client shows it for a refused ref advertisement:
//
//	remote: Git service is busy with other transfers; try again shortly
//	fatal: unable to access '.../git/sample.git/': The requested URL returned error: 503
//
// A refused transfer POST shows the status only, because Git prints the body of
// an advertisement refusal and not of an RPC refusal:
//
//	error: RPC failed; HTTP 503 curl 22 The requested URL returned error: 503
func TestGitClientSeesTheBusyRepositoryAnswer(t *testing.T) {
	manager, runner := newHTTPTestRepository(t)
	handler, err := New(runner, manager, "")
	noErr(t, err)
	lock := manager.Locks.For("sample")
	// The transfer POST meets the held repository, while the advertisement of the
	// same client is served normally, so the client reaches the POST path.
	var postHeld atomic.Bool
	handler.Authorize = func(request *http.Request) (bool, error) {
		// Only the first POST takes the lock, so a client that sends a second
		// POST cannot wait on the test's own hold.
		if request.Method == http.MethodPost && postHeld.CompareAndSwap(false, true) {
			lock.Lock()
		}
		return true, nil
	}
	useLimits(t, handler, func(limits *Limits) {
		limits.PerRepository, limits.ExtraSlots = 1, 0
		limits.QueueWait = 5 * time.Second
		limits.Operation = 150 * time.Millisecond
	})
	server := httptest.NewServer(handler)
	defer server.Close()

	// The message is asserted as text, so a change to it shows up here.
	const busy = "Git service is busy with other transfers; try again shortly"
	lock.Lock()
	advertisement, advertisementErr := httpGitCombined("", "ls-remote", server.URL+"/git/sample.git")
	lock.Unlock()
	if advertisementErr == nil || !strings.Contains(advertisement, busy) || !strings.Contains(advertisement, "503") {
		t.Fatalf("git ls-remote reported %v:\n%s", advertisementErr, advertisement)
	}
	clientRepository := t.TempDir()
	httpGitOutput(t, clientRepository, "init", "--quiet", "--initial-branch=main")
	transfer, transferErr := httpGitCombined(clientRepository, "fetch", "--no-tags", server.URL+"/git/sample.git", "+refs/heads/main:refs/remotes/probe/main")
	if postHeld.Load() {
		lock.Unlock()
	}
	if transferErr == nil || !strings.Contains(transfer, "503") {
		t.Fatalf("git fetch reported %v:\n%s", transferErr, transfer)
	}
}

// The busy answer names the limit that ended the wait, and a client that left
// before it is not reported as a failure. A connection deadline that passes at
// the same instant as the limit cancels the request, so an expired limit can
// reach the answer as a cancellation.
func TestBusyAnswerNamesTheLimitThatEndedTheWait(t *testing.T) {
	for _, test := range []struct {
		name   string
		limit  time.Time
		err    error
		logged bool
	}{
		{name: "expired limit reported as a cancellation", limit: time.Now().Add(-time.Second), err: context.Canceled, logged: true},
		{name: "expired limit", limit: time.Now().Add(-time.Second), err: context.DeadlineExceeded, logged: true},
		{name: "client left before the limit", limit: time.Now().Add(time.Minute), err: context.Canceled},
		{name: "client left with no limit", err: context.Canceled},
	} {
		t.Run(test.name, func(t *testing.T) {
			logBuffer := &bytes.Buffer{}
			previousLog := log.Writer()
			log.SetOutput(logBuffer)
			defer log.SetOutput(previousLog)
			writer := httptest.NewRecorder()
			answerBusyLocked(writer, route{repositoryID: "sample", service: "git-upload-pack"}, http.MethodGet, "operation limit", test.limit, test.err)
			if writer.Code != http.StatusServiceUnavailable || writer.Header().Get("Retry-After") == "" {
				t.Fatalf("answer=%d retry-after=%q, want the busy answer", writer.Code, writer.Header().Get("Retry-After"))
			}
			logged := strings.Contains(logBuffer.String(), "stayed locked for the whole operation limit")
			if logged != test.logged {
				t.Fatalf("the server logged %q, want logged=%v", logBuffer.String(), test.logged)
			}
		})
	}
}

// useLimits makes every transfer of handler run under its current limits
// changed by change, and returns them so a test can change them again
// between transfers.
func useLimits(t *testing.T, handler *Handler, change func(*Limits)) *Limits {
	t.Helper()
	current, err := handler.Limits(context.Background())
	noErr(t, err)
	limits := &current
	change(limits)
	handler.Limits = func(context.Context) (Limits, error) { return *limits, nil }
	return limits
}

func newHTTPTestRepository(t *testing.T) (*repository.Manager, *gitexec.Runner) {
	t.Helper()
	root := t.TempDir()
	stateDirectory, err := statetest.CopiedStateDirectory(filepath.Join(root, "state"))
	noErr(t, err)
	store, err := state.Open(context.Background(), stateDirectory)
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
	command.Env = testfixture.GitEnvironment(append(os.Environ(), "GIT_TERMINAL_PROMPT=0"))
	return command.CombinedOutput()
}

// Every name that repository creation accepts is reachable over Git HTTP,
// including a name that ends with a dot.
func TestSmartHTTPServesEveryCreatableRepositoryName(t *testing.T) {
	manager, runner := newHTTPTestRepository(t)
	handler, err := New(runner, manager, "")
	noErr(t, err)
	handler.Authorize = func(*http.Request) (bool, error) { return true, nil }
	server := httptest.NewServer(handler)
	defer server.Close()

	work := filepath.Join(t.TempDir(), "work")
	runHTTPGit(t, "", "init", "--initial-branch=main", work)
	runHTTPGit(t, work, "-c", "user.name=HTTP Test", "-c", "user.email=http@example.invalid", "commit", "--allow-empty", "-m", "first")
	for _, id := range []string{"trail.", "dotdot..", "a.b_c-1"} {
		if _, err := manager.Create(context.Background(), id, ""); err != nil {
			t.Fatalf("create %q: %v", id, err)
		}
		runHTTPGit(t, work, "push", server.URL+"/git/"+id+".git", "HEAD:refs/heads/main")
		if refs := httpGitOutput(t, "", "ls-remote", server.URL+"/git/"+id+".git"); !strings.Contains(refs, "refs/heads/main") {
			t.Fatalf("ls-remote %q = %q", id, refs)
		}
	}
	for _, target := range []string{"/git/Trail..git/info/refs?service=git-upload-pack", "/git/con.git/info/refs?service=git-upload-pack", "/git/x.git.git/info/refs?service=git-upload-pack"} {
		response, err := http.Get(server.URL + target)
		noErr(t, err)
		response.Body.Close()
		if response.StatusCode != http.StatusNotFound {
			t.Errorf("GET %s status=%d, want 404", target, response.StatusCode)
		}
	}
}
