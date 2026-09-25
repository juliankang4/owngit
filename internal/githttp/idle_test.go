package githttp

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/rand"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// waitFor polls condition until it holds or within passes.
func waitFor(t *testing.T, within time.Duration, what string, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(within)
	for !condition() {
		if time.Now().After(deadline) {
			t.Fatalf("%s did not happen within %s", what, within)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// idleFixture is a repository whose main branch holds size random bytes, and
// a handler with the given idle limit and a 20-second operation limit.
func idleFixture(t *testing.T, size int, idle time.Duration) (*Handler, string, string) {
	t.Helper()
	manager, runner := newHTTPTestRepository(t)
	runner.TerminationGrace = 25 * time.Millisecond
	repositoryPath, err := manager.Path("sample")
	noErr(t, err)
	work := filepath.Join(t.TempDir(), "work")
	runHTTPGit(t, "", "init", "-q", "--initial-branch=main", work)
	runHTTPGit(t, work, "config", "user.name", "Idle Test")
	runHTTPGit(t, work, "config", "user.email", "idle-test@example.invalid")
	random := make([]byte, size)
	_, err = rand.Read(random)
	noErr(t, err)
	noErr(t, os.WriteFile(filepath.Join(work, "random.bin"), random, 0o600))
	runHTTPGit(t, work, "add", ".")
	runHTTPGit(t, work, "commit", "-q", "-m", "random")
	runHTTPGit(t, work, "push", "-q", repositoryPath, "HEAD:refs/heads/main")
	handler, err := New(runner, manager, "", 2)
	noErr(t, err)
	handler.Authorize = func(*http.Request) bool { return true }
	handler.IdleTimeout = idle
	handler.OperationTimeout = 20 * time.Second
	return handler, work, httpGitOutput(t, work, "rev-parse", "HEAD")
}

// uploadPackRequest asks for commitOID in protocol version 0, without side
// band, so the response is the pack itself.
func uploadPackRequest(commitOID string) string {
	return fmt.Sprintf("%04xwant %s\n0000%04xdone\n", 4+len("want \n")+len(commitOID), commitOID, 4+len("done\n"))
}

// A clone whose client stops reading is stopped at the idle limit, long
// before the operation limit, and the push that waited for the repository
// then runs.
func TestIdleLimitStopsAStalledReaderAndFreesTheRepository(t *testing.T) {
	const idle = 500 * time.Millisecond
	handler, work, commitOID := idleFixture(t, 24<<20, idle)
	logs := captureLog(t)
	server := httptest.NewServer(handler)
	defer server.Close()

	connection, err := net.Dial("tcp", server.Listener.Addr().String())
	noErr(t, err)
	defer connection.Close()
	body := uploadPackRequest(commitOID)
	_, err = fmt.Fprintf(connection, "POST /git/sample.git/git-upload-pack HTTP/1.1\r\nHost: example.test\r\n"+
		"Content-Type: application/x-git-upload-pack-request\r\nContent-Length: %d\r\n\r\n%s", len(body), body)
	noErr(t, err)
	waitFor(t, 5*time.Second, "the clone start", func() bool { return handler.Active() == 1 })

	// The push waits for the repository lock that the clone holds.
	noErr(t, os.WriteFile(filepath.Join(work, "next.txt"), []byte("next\n"), 0o600))
	runHTTPGit(t, work, "add", ".")
	runHTTPGit(t, work, "commit", "-q", "-m", "next")
	started := time.Now()
	pushed := make(chan error, 1)
	go func() {
		output, err := httpGitCombined(work, "push", server.URL+"/git/sample.git", "HEAD:refs/heads/main")
		if err != nil {
			err = fmt.Errorf("%w: %s", err, output)
		}
		pushed <- err
	}()
	select {
	case err := <-pushed:
		t.Fatalf("push finished while the stalled clone held the repository: %v", err)
	case <-time.After(idle / 2):
	}
	select {
	case err := <-pushed:
		noErr(t, err, "push after the stalled clone")
	case <-time.After(10 * time.Second):
		t.Fatalf("push still waiting after %s; the stalled clone kept the repository", time.Since(started))
	}
	waitFor(t, 3*time.Second, "the end of the stalled clone", func() bool { return handler.Active() == 0 })
	want := `Git fetch request for repository "sample" failed: no data moved for 500ms (Git transfer idle limit)`
	if !strings.Contains(logs.String(), want) {
		t.Fatalf("log %q lacks %q", logs.String(), want)
	}
}

// A request body that stops arriving is stopped at the idle limit, and the
// repository is free again.
func TestIdleLimitStopsAStalledRequestBody(t *testing.T) {
	const idle = 400 * time.Millisecond
	var gzipped bytes.Buffer
	compressor := gzip.NewWriter(&gzipped)
	_, _ = compressor.Write([]byte(strings.Repeat("0032want 0000000000000000000000000000000000000000\n", 200)))
	noErr(t, compressor.Close())
	for _, test := range []struct {
		name, service, headers, sent string
	}{
		{"push with a length", "git-receive-pack", "Content-Length: 100000\r\n", "0098"},
		{"chunked push", "git-receive-pack", "Transfer-Encoding: chunked\r\n", "4\r\n0098\r\n"},
		{"gzip fetch", "git-upload-pack", "Content-Encoding: gzip\r\nContent-Length: 100000\r\n", gzipped.String()[:gzipped.Len()/2]},
	} {
		t.Run(test.name, func(t *testing.T) {
			handler, _, _ := idleFixture(t, 16, idle)
			logs := captureLog(t)
			server := httptest.NewServer(handler)
			defer server.Close()
			connection, err := net.Dial("tcp", server.Listener.Addr().String())
			noErr(t, err)
			defer connection.Close()
			started := time.Now()
			_, err = fmt.Fprintf(connection, "POST /git/sample.git/%s HTTP/1.1\r\nHost: example.test\r\n"+
				"Content-Type: application/x-%s-request\r\n%s\r\n%s", test.service, test.service, test.headers, test.sent)
			noErr(t, err)
			waitFor(t, 5*time.Second, "the transfer start", func() bool { return handler.Active() == 1 })
			waitFor(t, 5*time.Second, "the end of the stalled transfer", func() bool { return handler.Active() == 0 })
			if elapsed := time.Since(started); elapsed < idle {
				t.Fatalf("transfer ended after %s, before the idle limit", elapsed)
			}
			lockContext, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			lock := handler.Repositories.Locks.For("sample")
			noErr(t, lock.LockContext(lockContext), "take the repository lock after the stalled transfer")
			lock.Unlock()
			kind := "push"
			if test.service == "git-upload-pack" {
				kind = "fetch"
			}
			want := fmt.Sprintf(`Git %s request for repository "sample" failed: no data moved for 400ms (Git transfer idle limit)`, kind)
			waitFor(t, 2*time.Second, "the idle limit log line", func() bool { return strings.Contains(logs.String(), want) })
		})
	}
}
