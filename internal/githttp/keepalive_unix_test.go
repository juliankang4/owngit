//go:build !windows

package githttp

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httptrace"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

// readTimeoutTransport behaves like a half-duplex proxy's read timeout: a
// request fails when the backend sends nothing for timeout, after the
// request is sent until the end of the response.
type readTimeoutTransport struct {
	timeout  time.Duration
	timeouts *atomic.Int32
	probes   map[string]*keepaliveProbe
}

func (transport readTimeoutTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	ctx, cancel := context.WithCancel(request.Context())
	clock := &proxyReadClock{timeout: transport.timeout, cancel: func() {
		transport.timeouts.Add(1)
		cancel()
	}}
	trace := &httptrace.ClientTrace{WroteRequest: func(info httptrace.WroteRequestInfo) {
		if info.Err == nil {
			clock.start()
		}
	}}
	response, err := http.DefaultTransport.RoundTrip(request.WithContext(httptrace.WithClientTrace(ctx, trace)))
	if err != nil {
		clock.stop()
		cancel()
		return nil, err
	}
	response.Body = &rearmingBody{ReadCloser: response.Body, clock: clock, cancel: cancel, probe: transport.probes[request.URL.Path]}
	return response, nil
}

// WroteRequest starts the clock only once the transport has flushed the whole
// request. A response read before that event cannot start the clock itself.
type proxyReadClock struct {
	mu      sync.Mutex
	timer   *time.Timer
	timeout time.Duration
	cancel  func()
	stopped bool
}

func (clock *proxyReadClock) start() {
	clock.mu.Lock()
	defer clock.mu.Unlock()
	if !clock.stopped {
		clock.timer = time.AfterFunc(clock.timeout, clock.cancel)
	}
}

func (clock *proxyReadClock) rearm() {
	clock.mu.Lock()
	defer clock.mu.Unlock()
	if clock.timer != nil && !clock.stopped {
		clock.timer.Reset(clock.timeout)
	}
}

func (clock *proxyReadClock) stop() {
	clock.mu.Lock()
	defer clock.mu.Unlock()
	clock.stopped = true
	if clock.timer != nil {
		clock.timer.Stop()
	}
}

type rearmingBody struct {
	io.ReadCloser
	clock  *proxyReadClock
	cancel context.CancelFunc
	probe  *keepaliveProbe
}

func (body *rearmingBody) Read(buffer []byte) (int, error) {
	n, err := body.ReadCloser.Read(buffer)
	if n > 0 {
		body.clock.rearm()
		if body.probe != nil {
			if probeErr := body.probe.observe(buffer[:n]); probeErr != nil {
				return n, probeErr
			}
		}
	}
	return n, err
}

func (body *rearmingBody) Close() error {
	body.clock.stop()
	body.cancel()
	return body.ReadCloser.Close()
}

// keepaliveProbe releases a quiet Git hook only after at least four empty
// sideband packets cross the connection over a phase longer than the timeout.
type keepaliveProbe struct {
	mu           sync.Mutex
	ready        string
	release      *os.File
	pending      []byte
	count        int
	first        time.Time
	last         time.Time
	quietFor     time.Duration
	minimumQuiet time.Duration
	released     bool
	largestGap   time.Duration
}

func newKeepaliveProbe(t *testing.T, root, name string, minimumQuiet time.Duration) *keepaliveProbe {
	t.Helper()
	fifo := filepath.Join(root, name+"-release")
	noErr(t, syscall.Mkfifo(fifo, 0o600))
	// Keeping both ends open makes releasing the hook a nonblocking write,
	// even if cancellation stops Git just before it opens the FIFO.
	release, err := os.OpenFile(fifo, os.O_RDWR, 0)
	noErr(t, err)
	t.Cleanup(func() { _ = release.Close() })
	return &keepaliveProbe{ready: filepath.Join(root, name+"-ready"), release: release, minimumQuiet: minimumQuiet}
}

func (probe *keepaliveProbe) waitScript() string {
	return "printf ready >" + quoteShell(probe.ready) + "\nread -r release <" + quoteShell(probe.release.Name()) + "\n"
}

func (probe *keepaliveProbe) observe(content []byte) error {
	probe.mu.Lock()
	defer probe.mu.Unlock()
	probe.pending = append(probe.pending, content...)
	for len(probe.pending) >= 4 {
		size, err := strconv.ParseUint(string(probe.pending[:4]), 16, 16)
		if err != nil || size == 3 {
			return fmt.Errorf("invalid Git packet header %q", probe.pending[:4])
		}
		if size < 4 {
			probe.pending = probe.pending[4:]
			continue
		}
		if len(probe.pending) < int(size) {
			break
		}
		if size == 5 && (probe.pending[4] == 1 || probe.pending[4] == 2) {
			if _, err := os.Stat(probe.ready); err == nil {
				now := time.Now()
				if probe.count == 0 {
					probe.first = now
				}
				if !probe.last.IsZero() && now.Sub(probe.last) > probe.largestGap {
					probe.largestGap = now.Sub(probe.last)
				}
				probe.last = now
				probe.count++
				quietFor := now.Sub(probe.first)
				if !probe.released && probe.count >= 4 && quietFor > probe.minimumQuiet {
					// The hook stays blocked from the first observation to release.
					probe.quietFor = quietFor
					if _, err := probe.release.WriteString("continue\n"); err != nil {
						return err
					}
					probe.released = true
				}
			} else if !os.IsNotExist(err) {
				return err
			}
		}
		probe.pending = probe.pending[size:]
	}
	return nil
}

// Git's keepalive packets reach the client while Git prepares a pack or runs
// a hook, so a proxy whose read timeout is shorter than that quiet phase, but
// longer than Git's keepalive interval, keeps the transfer. The response
// still waits for the end of the request body, which a half-duplex proxy
// stops forwarding once it passes response headers on.
func TestKeepalivePacketsReachTheClientAtOnce(t *testing.T) {
	const proxyTimeout = 2500 * time.Millisecond
	handler, work, _ := idleFixture(t, 64<<10, time.Minute)
	server := httptest.NewServer(handler)
	defer server.Close()
	target, err := url.Parse(server.URL)
	noErr(t, err)
	var timeouts atomic.Int32
	root := t.TempDir()
	cloneProbe := newKeepaliveProbe(t, root, "clone", proxyTimeout)
	pushProbe := newKeepaliveProbe(t, root, "push", proxyTimeout)
	proxy := httptest.NewServer(&httputil.ReverseProxy{
		Rewrite:       func(request *httputil.ProxyRequest) { request.SetURL(target) },
		FlushInterval: -1,
		Transport: readTimeoutTransport{timeout: proxyTimeout, timeouts: &timeouts, probes: map[string]*keepaliveProbe{
			"/git/sample.git/git-upload-pack":  cloneProbe,
			"/git/sample.git/git-receive-pack": pushProbe,
		}},
	})
	defer proxy.Close()
	remote := proxy.URL + "/git/sample.git"
	repositoryPath, err := handler.Repositories.Path("sample")
	noErr(t, err)
	// Keepalives every second instead of every five, so the test is short.
	runHTTPGit(t, "", "--git-dir", repositoryPath, "config", "uploadpack.keepAlive", "1")
	runHTTPGit(t, "", "--git-dir", repositoryPath, "config", "receive.keepAlive", "1")
	check := func(what string, probe *keepaliveProbe, output string, err error) {
		t.Helper()
		if err != nil || timeouts.Load() != 0 {
			t.Fatalf("%s through a proxy with a %s read timeout: %v, proxy timeouts %d: %s", what, proxyTimeout, err, timeouts.Load(), output)
		}
		probe.mu.Lock()
		defer probe.mu.Unlock()
		if probe.count < 4 || probe.largestGap >= proxyTimeout {
			t.Fatalf("%s: %d keepalives while the hook waited, largest gap %s, want four with gaps below %s", what, probe.count, probe.largestGap, proxyTimeout)
		}
		if probe.quietFor <= proxyTimeout {
			t.Fatalf("%s: observed quiet phase %s, want longer than %s", what, probe.quietFor, proxyTimeout)
		}
		t.Logf("%s: %d keepalives while the hook waited for at least %s, largest gap %s", what, probe.count, probe.quietFor, probe.largestGap)
	}

	// upload-pack: pack-objects starts only after a quiet phase. Git reads
	// uploadpack.packObjectsHook only from protected configuration, which
	// for OwnGit is its own global configuration file.
	packHook := filepath.Join(root, "pack-objects-hook")
	noErr(t, os.WriteFile(packHook, []byte("#!/bin/sh\n"+cloneProbe.waitScript()+"exec \"$@\"\n"), 0o700))
	globalConfig := handler.Git.GlobalConfigPath
	noErr(t, os.WriteFile(globalConfig, []byte("[uploadpack]\n\tpackObjectsHook = "+packHook+"\n"), 0o600))
	t.Cleanup(func() { _ = os.WriteFile(globalConfig, nil, 0o600) })
	output, err := httpGitCombined("", "clone", "-q", remote, filepath.Join(t.TempDir(), "clone"))
	check("clone", cloneProbe, output, err)
	noErr(t, os.WriteFile(globalConfig, nil, 0o600))

	// receive-pack: the update hook runs after the whole pack arrived.
	hook := filepath.Join(repositoryPath, "hooks", "update")
	original, err := os.ReadFile(hook)
	noErr(t, err)
	noErr(t, os.WriteFile(hook+".owngit", original, 0o700))
	noErr(t, os.WriteFile(hook, []byte("#!/bin/sh\n"+pushProbe.waitScript()+"exec "+quoteShell(hook+".owngit")+" \"$@\"\n"), 0o700))
	// A push larger than Git's 1 MiB post buffer is sent chunked, and the
	// proxy, which serves HTTP/1.1 half duplex, forwards it only until it
	// passes response headers on.
	content := make([]byte, 4<<20)
	_, err = rand.Read(content)
	noErr(t, err)
	noErr(t, os.WriteFile(filepath.Join(work, "keepalive.bin"), content, 0o600))
	runHTTPGit(t, work, "add", ".")
	runHTTPGit(t, work, "commit", "-q", "-m", "keepalive")
	output, err = httpGitCombined(work, "push", "-q", remote, "HEAD:refs/heads/main")
	check("push", pushProbe, output, err)
	if got, want := httpGitOutput(t, "", "--git-dir", repositoryPath, "rev-parse", "refs/heads/main"), httpGitOutput(t, work, "rev-parse", "HEAD"); got != want {
		t.Fatalf("main is %s after the push, want %s", got, want)
	}
}

// The response headers leave when the request body ends, not with Git's first
// output after it, so a proxy waiting for them sees the transfer move while a
// hook runs. Here keepalives are off and the update hook waits until the
// proxy has received the headers.
func TestPushResponseHeadersLeaveWhenTheRequestBodyEnds(t *testing.T) {
	handler, work, _ := idleFixture(t, 1<<10, time.Minute)
	server := httptest.NewServer(handler)
	defer server.Close()
	target, err := url.Parse(server.URL)
	noErr(t, err)
	release := filepath.Join(t.TempDir(), "headers-arrived")
	proxy := httptest.NewServer(&httputil.ReverseProxy{
		Rewrite: func(request *httputil.ProxyRequest) { request.SetURL(target) },
		ModifyResponse: func(response *http.Response) error {
			// Git probes with a small POST before it streams a large one.
			if response.Request.Method == http.MethodPost && response.Request.ContentLength < 0 {
				return os.WriteFile(release, nil, 0o600)
			}
			return nil
		},
	})
	defer proxy.Close()
	repositoryPath, err := handler.Repositories.Path("sample")
	noErr(t, err)
	runHTTPGit(t, "", "--git-dir", repositoryPath, "config", "receive.keepAlive", "0")
	hook := filepath.Join(repositoryPath, "hooks", "update")
	original, err := os.ReadFile(hook)
	noErr(t, err)
	noErr(t, os.WriteFile(hook+".owngit", original, 0o700))
	// The bound only keeps a failing run from hanging.
	script := "#!/bin/sh\ni=0\nwhile [ ! -e " + quoteShell(release) + " ]; do\n" +
		"\ti=$((i+1))\n\tif [ $i -gt 600 ]; then echo 'no response headers while the hook ran' >&2; exit 1; fi\n\tsleep 0.05\ndone\n" +
		"exec " + quoteShell(hook+".owngit") + " \"$@\"\n"
	noErr(t, os.WriteFile(hook, []byte(script), 0o700))

	// A chunked push, so the body is still arriving when git-http-backend
	// writes its headers. With -q, Git writes nothing until the hook ends.
	content := make([]byte, 4<<20)
	_, err = rand.Read(content)
	noErr(t, err)
	noErr(t, os.WriteFile(filepath.Join(work, "hook.bin"), content, 0o600))
	runHTTPGit(t, work, "add", ".")
	runHTTPGit(t, work, "commit", "-q", "-m", "hook")
	output, err := httpGitCombined(work, "push", "-q", proxy.URL+"/git/sample.git", "HEAD:refs/heads/main")
	if err != nil {
		t.Fatalf("push: %v\n%s", err, output)
	}
	if got, want := httpGitOutput(t, "", "--git-dir", repositoryPath, "rev-parse", "refs/heads/main"), httpGitOutput(t, work, "rev-parse", "HEAD"); got != want {
		t.Fatalf("main is %s after the push, want %s:\n%s", got, want, output)
	}
}

// The response headers of a fetch leave once the request body has ended,
// however the body is framed, so a proxy sees the transfer move while Git
// prepares the pack. The pack-objects hook waits until the client has the
// headers; the chunked bodies send their last chunk in a later write.
func TestFetchResponseHeadersLeaveWhenTheRequestBodyEndsForEveryFraming(t *testing.T) {
	handler, _, head := idleFixture(t, 16, time.Minute)
	server := httptest.NewServer(handler)
	defer server.Close()
	packHook := filepath.Join(t.TempDir(), "pack-objects-hook")
	noErr(t, os.WriteFile(handler.Git.GlobalConfigPath, []byte("[uploadpack]\n\tpackObjectsHook = "+packHook+"\n"), 0o600))
	t.Cleanup(func() { _ = os.WriteFile(handler.Git.GlobalConfigPath, nil, 0o600) })

	want := fmt.Sprintf("want %s side-band-64k\n", head)
	plain := []byte(fmt.Sprintf("%04x%s0000%04xdone\n", 4+len(want), want, 4+len("done\n")))
	for _, framing := range []struct {
		name          string
		gzip, chunked bool
	}{{"length", false, false}, {"length gzip", true, false}, {"chunked", false, true}, {"chunked gzip", true, true}} {
		t.Run(framing.name, func(t *testing.T) {
			release := filepath.Join(t.TempDir(), "headers-arrived")
			// The bound only keeps a failing run from hanging.
			noErr(t, os.WriteFile(packHook, []byte("#!/bin/sh\ni=0\nwhile [ ! -e "+quoteShell(release)+" ]; do\n"+
				"\ti=$((i+1))\n\tif [ $i -gt 600 ]; then echo 'no response headers while pack-objects waited' >&2; exit 1; fi\n\tsleep 0.05\ndone\n"+
				"exec \"$@\"\n"), 0o700))
			body := plain
			header := "POST /git/sample.git/git-upload-pack HTTP/1.1\r\nHost: example.test\r\nContent-Type: application/x-git-upload-pack-request\r\n"
			if framing.gzip {
				body = gzipBytes(t, plain)
				header += "Content-Encoding: gzip\r\n"
			}
			connection, err := net.Dial("tcp", server.Listener.Addr().String())
			noErr(t, err)
			defer connection.Close()
			noErr(t, connection.SetDeadline(time.Now().Add(20*time.Second)))
			if framing.chunked {
				_, err = fmt.Fprintf(connection, "%sTransfer-Encoding: chunked\r\n\r\n%x\r\n%s\r\n", header, len(body), body)
				noErr(t, err)
				// A separate, later last chunk: sent together, net/http
				// reports the end of the body with the data.
				time.Sleep(100 * time.Millisecond)
				_, err = io.WriteString(connection, "0\r\n\r\n")
			} else {
				_, err = fmt.Fprintf(connection, "%sContent-Length: %d\r\n\r\n%s", header, len(body), body)
			}
			noErr(t, err)
			reader := bufio.NewReader(connection)
			response, err := http.ReadResponse(reader, nil)
			noErr(t, err, "read the response headers while pack-objects waits")
			noErr(t, os.WriteFile(release, nil, 0o600))
			content, err := io.ReadAll(response.Body)
			noErr(t, err)
			if response.StatusCode != http.StatusOK || !bytes.Contains(content, []byte("PACK")) {
				t.Fatalf("status %d, %d bytes without a pack", response.StatusCode, len(content))
			}
		})
	}
}
