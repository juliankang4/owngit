//go:build !windows

package githttp

import (
	"context"
	"crypto/rand"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// linkBuffer sizes the socket buffers of the slow link, set before the
// connection exists so the operating system keeps to them. Without it,
// automatic sizing would let the queued bytes, and so the time a single write
// waits for a slow reader, differ widely between systems.
const linkBuffer = 64 << 10

func setLinkBuffers(_, _ string, connection syscall.RawConn) error {
	var err error
	if controlErr := connection.Control(func(fd uintptr) {
		err = syscall.SetsockoptInt(int(fd), syscall.SOL_SOCKET, syscall.SO_SNDBUF, linkBuffer)
		if err == nil {
			err = syscall.SetsockoptInt(int(fd), syscall.SOL_SOCKET, syscall.SO_RCVBUF, linkBuffer)
		}
	}); controlErr != nil {
		return controlErr
	}
	return err
}

// slowLinkServer serves handler on a listener with linkBuffer socket buffers.
func slowLinkServer(t *testing.T, handler http.Handler) *httptest.Server {
	t.Helper()
	listener, err := (&net.ListenConfig{Control: setLinkBuffers}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	noErr(t, err)
	server := httptest.NewUnstartedServer(handler)
	_ = server.Listener.Close()
	server.Listener = listener
	server.Start()
	t.Cleanup(server.Close)
	return server
}

// throttledProxy forwards connections to target at about rate bytes per
// second in each direction, in small steady steps. It returns its address.
func throttledProxy(t *testing.T, target string, rate int) string {
	t.Helper()
	listener, err := (&net.ListenConfig{Control: setLinkBuffers}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	noErr(t, err)
	t.Cleanup(func() { _ = listener.Close() })
	const step = 4 << 10
	pause := time.Second * step / time.Duration(rate)
	pipe := func(destination, source net.Conn) {
		defer destination.Close()
		defer source.Close()
		buffer := make([]byte, step)
		for {
			n, err := source.Read(buffer)
			if n > 0 {
				if _, err := destination.Write(buffer[:n]); err != nil {
					return
				}
				time.Sleep(pause * time.Duration(n) / step)
			}
			if err != nil {
				return
			}
		}
	}
	dialer := &net.Dialer{Control: setLinkBuffers}
	go func() {
		for {
			client, err := listener.Accept()
			if err != nil {
				return
			}
			upstream, err := dialer.Dial("tcp", target)
			if err != nil {
				_ = client.Close()
				continue
			}
			go pipe(upstream, client)
			go pipe(client, upstream)
		}
	}()
	return listener.Addr().String()
}

// Clients that move data slowly but steadily keep going for longer than the
// idle limit: a clone, a push and an archive download. Each single write or
// read waits much less than the idle limit, while the transfer as a whole
// takes about twice as long.
func TestIdleLimitKeepsSlowSteadyTransfers(t *testing.T) {
	const idle = time.Second
	const size = 4 << 20
	const rate = 2 << 20
	handler, work, commitOID := idleFixture(t, size, idle)
	logs := captureLog(t)
	server := slowLinkServer(t, http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/archive" {
			if err := handler.ServeArchive(writer, request, "sample", commitOID, ArchiveZip, "sample", "sample.zip"); err != nil {
				t.Errorf("archive: %v", err)
			}
			return
		}
		handler.ServeHTTP(writer, request)
	}))
	proxy := "http://" + throttledProxy(t, server.Listener.Addr().String(), rate)
	longer := func(what string, started time.Time) {
		t.Helper()
		if elapsed := time.Since(started); elapsed < idle*3/2 {
			t.Fatalf("%s took %s, not longer than the idle limit", what, elapsed)
		}
	}

	started := time.Now()
	clone := filepath.Join(t.TempDir(), "clone")
	if output, err := httpGitCombined("", "clone", "-q", proxy+"/git/sample.git", clone); err != nil {
		t.Fatalf("clone: %v: %s\nlog: %s", err, output, logs.String())
	}
	longer("clone", started)
	if got := httpGitOutput(t, clone, "rev-parse", "HEAD"); got != commitOID {
		t.Fatalf("cloned %s, want %s", got, commitOID)
	}

	random := make([]byte, size)
	_, err := rand.Read(random)
	noErr(t, err)
	noErr(t, os.WriteFile(filepath.Join(work, "random.bin"), random, 0o600))
	runHTTPGit(t, work, "commit", "-q", "-am", "more")
	started = time.Now()
	if output, err := httpGitCombined(work, "push", "-q", proxy+"/git/sample.git", "HEAD:refs/heads/main"); err != nil {
		t.Fatalf("push: %v: %s\nlog: %s", err, output, logs.String())
	}
	longer("push", started)

	started = time.Now()
	response, err := http.Get(proxy + "/archive")
	noErr(t, err)
	content, err := io.ReadAll(response.Body)
	response.Body.Close()
	noErr(t, err, "read the archive")
	longer("archive", started)
	if entries, err := zipEntries(content); response.StatusCode != http.StatusOK || err != nil || len(entries) == 0 {
		t.Fatalf("archive: status %d, %d entries: %v", response.StatusCode, len(entries), err)
	}
	if strings.Contains(logs.String(), "idle limit") {
		t.Fatalf("a steady transfer hit the idle limit: %s", logs.String())
	}
}

// Git phases without output that last longer than the idle limit do not end
// a transfer: Git preparing its answer before the first byte, and a push
// whose update hook runs after the whole pack arrived, with and without
// Git's own keepalive packets.
func TestIdleLimitSparesQuietGit(t *testing.T) {
	const idle = 300 * time.Millisecond
	handler, work, _ := idleFixture(t, 64<<10, idle)
	logs := captureLog(t)
	realBackend, err := DiscoverBackend(context.Background(), handler.Git)
	noErr(t, err)
	// While the marker exists, the backend waits a second before Git starts
	// to answer a POST request.
	root := t.TempDir()
	marker := filepath.Join(root, "quiet")
	handler.BackendPath = filepath.Join(root, "git-http-backend")
	script := "#!/bin/sh\ntest -e " + quoteShell(marker) + " && test \"$REQUEST_METHOD\" = POST && sleep 1\nexec " + quoteShell(realBackend) + "\n"
	noErr(t, os.WriteFile(handler.BackendPath, []byte(script), 0o700))
	server := httptest.NewServer(handler)
	defer server.Close()
	remote := server.URL + "/git/sample.git"

	noErr(t, os.WriteFile(marker, nil, 0o600))
	delayed := func(what string, started time.Time) {
		t.Helper()
		if elapsed := time.Since(started); elapsed < time.Second {
			t.Fatalf("%s took %s; Git was not quiet for longer than the idle limit", what, elapsed)
		}
	}
	started := time.Now()
	clone := filepath.Join(t.TempDir(), "clone")
	if output, err := httpGitCombined("", "clone", "-q", remote, clone); err != nil {
		t.Fatalf("clone with a quiet start: %v: %s", err, output)
	}
	delayed("clone with a quiet start", started)
	noErr(t, os.WriteFile(filepath.Join(work, "quiet.txt"), []byte("quiet start\n"), 0o600))
	runHTTPGit(t, work, "add", ".")
	runHTTPGit(t, work, "commit", "-q", "-m", "quiet start")
	started = time.Now()
	if output, err := httpGitCombined(work, "push", "-q", remote, "HEAD:refs/heads/main"); err != nil {
		t.Fatalf("push with a quiet start: %v: %s", err, output)
	}
	delayed("push with a quiet start", started)

	noErr(t, os.Remove(marker))
	repositoryPath, err := handler.Repositories.Path("sample")
	noErr(t, err)
	hook := filepath.Join(repositoryPath, "hooks", "update")
	original, err := os.ReadFile(hook)
	noErr(t, err)
	noErr(t, os.WriteFile(hook+".owngit", original, 0o700))
	noErr(t, os.WriteFile(hook, []byte("#!/bin/sh\nsleep 1.3\nexec "+quoteShell(hook+".owngit")+" \"$@\"\n"), 0o700))
	// Keepalives every second, and none at all.
	for _, keepAlive := range []string{"1", "0"} {
		runHTTPGit(t, "", "--git-dir", repositoryPath, "config", "receive.keepAlive", keepAlive)
		noErr(t, os.WriteFile(filepath.Join(work, "quiet.txt"), []byte("quiet hook "+keepAlive+"\n"), 0o600))
		runHTTPGit(t, work, "commit", "-q", "-am", "quiet hook "+keepAlive)
		started = time.Now()
		if output, err := httpGitCombined(work, "push", "-q", remote, "HEAD:refs/heads/main"); err != nil {
			t.Fatalf("push with a quiet hook, receive.keepAlive=%s: %v: %s", keepAlive, err, output)
		}
		delayed("push with a quiet hook", started)
		if got, want := httpGitOutput(t, "", "--git-dir", repositoryPath, "rev-parse", "refs/heads/main"), httpGitOutput(t, work, "rev-parse", "HEAD"); got != want {
			t.Fatalf("main is %s after the push, want %s", got, want)
		}
	}
	if strings.Contains(logs.String(), "idle limit") {
		t.Fatalf("a quiet Git phase hit the idle limit: %s", logs.String())
	}
}
