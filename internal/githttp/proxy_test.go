package githttp

import (
	"bytes"
	"crypto/rand"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// A reverse proxy that serves HTTP/1.1 half duplex, as Go's
// httputil.ReverseProxy does with default settings, stops forwarding the
// request body once it passes the response headers on. OwnGit must not send
// them before it has read the request body to its end.

// halfDuplexProxy serves a default httputil.ReverseProxy in front of target.
// A non-nil wrapBody replaces the body of each POST the proxy forwards.
func halfDuplexProxy(t *testing.T, target string, wrapBody func(io.ReadCloser) io.ReadCloser) string {
	t.Helper()
	backend, err := url.Parse(target)
	noErr(t, err)
	proxy := httptest.NewServer(&httputil.ReverseProxy{Rewrite: func(request *httputil.ProxyRequest) {
		request.SetURL(backend)
		if wrapBody != nil && request.Out.Method == http.MethodPost && request.Out.Body != nil {
			request.Out.Body = wrapBody(request.Out.Body)
		}
	}})
	t.Cleanup(proxy.Close)
	return proxy.URL
}

// Git sends a push over its 1 MiB post buffer chunked, so the proxy is still
// forwarding it when git-http-backend has written its headers.
func TestALargePushThroughAHalfDuplexProxyArrivesWhole(t *testing.T) {
	handler, work, _ := idleFixture(t, 1<<10, time.Minute)
	server := httptest.NewServer(handler)
	defer server.Close()
	remote := halfDuplexProxy(t, server.URL, nil) + "/git/sample.git"

	content := make([]byte, 8<<20)
	_, err := rand.Read(content)
	noErr(t, err)
	noErr(t, os.WriteFile(filepath.Join(work, "large.bin"), content, 0o600))
	runHTTPGit(t, work, "add", ".")
	runHTTPGit(t, work, "commit", "-q", "-m", "large")
	output, err := httpGitCombined(work, "push", "-q", remote, "HEAD:refs/heads/main")
	if err != nil {
		t.Fatalf("push of %d bytes through the proxy: %v\n%s", len(content), err, output)
	}
	repositoryPath, err := handler.Repositories.Path("sample")
	noErr(t, err)
	if got, want := httpGitOutput(t, "", "--git-dir", repositoryPath, "rev-parse", "refs/heads/main"), httpGitOutput(t, work, "rev-parse", "HEAD"); got != want {
		t.Fatalf("main is %s after the push, want %s", got, want)
	}

	clone := filepath.Join(t.TempDir(), "clone")
	output, err = httpGitCombined("", "clone", "-q", remote, clone)
	if err != nil {
		t.Fatalf("clone through the proxy: %v\n%s", err, output)
	}
	cloned, err := os.ReadFile(filepath.Join(clone, "large.bin"))
	noErr(t, err)
	if !bytes.Equal(cloned, content) {
		t.Fatalf("the cloned file differs from the pushed one (%d bytes, want %d)", len(cloned), len(content))
	}
}

// lateEndBody hands the proxy the body at once but reports its end late, as a
// proxy on a loaded machine may forward it.
type lateEndBody struct {
	io.ReadCloser
	delay time.Duration
}

func (body *lateEndBody) Read(buffer []byte) (int, error) {
	n, err := body.ReadCloser.Read(buffer)
	if err == io.EOF {
		time.Sleep(body.delay)
	}
	return n, err
}

// upload-pack reads its whole request before it answers, but
// git-http-backend writes its headers first. If they went out at once, the
// proxy would stop forwarding the end of the request and the clone would
// fail.
func TestACloneThroughAHalfDuplexProxyThatForwardsTheRequestLate(t *testing.T) {
	handler, _, _ := idleFixture(t, 64<<10, time.Minute)
	server := httptest.NewServer(handler)
	defer server.Close()
	remote := halfDuplexProxy(t, server.URL, func(body io.ReadCloser) io.ReadCloser {
		return &lateEndBody{ReadCloser: body, delay: 300 * time.Millisecond}
	}) + "/git/sample.git"
	output, err := httpGitCombined("", "clone", "-q", remote, filepath.Join(t.TempDir(), "clone"))
	if err != nil {
		t.Fatalf("clone through the proxy: %v\n%s", err, output)
	}
}
