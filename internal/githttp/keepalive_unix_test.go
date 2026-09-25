//go:build !windows

package githttp

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

// readTimeoutTransport behaves like a reverse proxy's read timeout: a request
// fails when the backend sends nothing for timeout, from the moment the
// request is sent until the end of the response.
type readTimeoutTransport struct {
	timeout  time.Duration
	timeouts *atomic.Int32
}

func (transport readTimeoutTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	ctx, cancel := context.WithCancel(request.Context())
	timer := time.AfterFunc(transport.timeout, func() {
		transport.timeouts.Add(1)
		cancel()
	})
	response, err := http.DefaultTransport.RoundTrip(request.WithContext(ctx))
	if err != nil {
		timer.Stop()
		cancel()
		return nil, err
	}
	response.Body = &rearmingBody{ReadCloser: response.Body, timer: timer, timeout: transport.timeout, cancel: cancel}
	return response, nil
}

type rearmingBody struct {
	io.ReadCloser
	timer   *time.Timer
	timeout time.Duration
	cancel  context.CancelFunc
}

func (body *rearmingBody) Read(buffer []byte) (int, error) {
	n, err := body.ReadCloser.Read(buffer)
	if n > 0 {
		body.timer.Reset(body.timeout)
	}
	return n, err
}

func (body *rearmingBody) Close() error {
	body.timer.Stop()
	body.cancel()
	return body.ReadCloser.Close()
}

// Git's keepalive packets reach the client while Git prepares a pack or runs
// a hook, so a proxy whose read timeout is shorter than that quiet phase, but
// longer than Git's keepalive interval, keeps the transfer.
func TestKeepalivePacketsReachTheClientAtOnce(t *testing.T) {
	const proxyTimeout = 2500 * time.Millisecond
	const quiet = "4"
	handler, work, _ := idleFixture(t, 64<<10, time.Minute)
	server := httptest.NewServer(handler)
	defer server.Close()
	target, err := url.Parse(server.URL)
	noErr(t, err)
	var timeouts atomic.Int32
	proxy := httptest.NewServer(&httputil.ReverseProxy{
		Rewrite:       func(request *httputil.ProxyRequest) { request.SetURL(target) },
		FlushInterval: -1,
		Transport:     readTimeoutTransport{timeout: proxyTimeout, timeouts: &timeouts},
	})
	defer proxy.Close()
	remote := proxy.URL + "/git/sample.git"
	repositoryPath, err := handler.Repositories.Path("sample")
	noErr(t, err)
	// Keepalives every second instead of every five, so the test is short.
	runHTTPGit(t, "", "--git-dir", repositoryPath, "config", "uploadpack.keepAlive", "1")
	runHTTPGit(t, "", "--git-dir", repositoryPath, "config", "receive.keepAlive", "1")
	check := func(what string, started time.Time, output string, err error) {
		t.Helper()
		if err != nil || timeouts.Load() != 0 {
			t.Fatalf("%s through a proxy with a %s read timeout: %v, proxy timeouts %d: %s", what, proxyTimeout, err, timeouts.Load(), output)
		}
		if elapsed := time.Since(started); elapsed < 4*time.Second {
			t.Fatalf("%s took %s; Git was not quiet for longer than the proxy timeout", what, elapsed)
		}
	}

	// upload-pack: pack-objects starts only after a quiet phase. Git reads
	// uploadpack.packObjectsHook only from protected configuration, which
	// for OwnGit is its own global configuration file.
	root := t.TempDir()
	packHook := filepath.Join(root, "pack-objects-hook")
	noErr(t, os.WriteFile(packHook, []byte("#!/bin/sh\nsleep "+quiet+"\nexec \"$@\"\n"), 0o700))
	globalConfig := handler.Git.GlobalConfigPath
	noErr(t, os.WriteFile(globalConfig, []byte("[uploadpack]\n\tpackObjectsHook = "+packHook+"\n"), 0o600))
	t.Cleanup(func() { _ = os.WriteFile(globalConfig, nil, 0o600) })
	started := time.Now()
	output, err := httpGitCombined("", "clone", "-q", remote, filepath.Join(t.TempDir(), "clone"))
	check("clone", started, output, err)
	noErr(t, os.WriteFile(globalConfig, nil, 0o600))

	// receive-pack: the update hook runs after the whole pack arrived.
	hook := filepath.Join(repositoryPath, "hooks", "update")
	original, err := os.ReadFile(hook)
	noErr(t, err)
	noErr(t, os.WriteFile(hook+".owngit", original, 0o700))
	noErr(t, os.WriteFile(hook, []byte("#!/bin/sh\nsleep "+quiet+"\nexec "+quoteShell(hook+".owngit")+" \"$@\"\n"), 0o700))
	noErr(t, os.WriteFile(filepath.Join(work, "keepalive.txt"), []byte("keepalive\n"), 0o600))
	runHTTPGit(t, work, "add", ".")
	runHTTPGit(t, work, "commit", "-q", "-m", "keepalive")
	started = time.Now()
	output, err = httpGitCombined(work, "push", "-q", remote, "HEAD:refs/heads/main")
	check("push", started, output, err)
	if got, want := httpGitOutput(t, "", "--git-dir", repositoryPath, "rev-parse", "refs/heads/main"), httpGitOutput(t, work, "rev-parse", "HEAD"); got != want {
		t.Fatalf("main is %s after the push, want %s", got, want)
	}
}
