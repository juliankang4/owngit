package githttp

import (
	"bufio"
	"bytes"
	"compress/zlib"
	"context"
	"crypto/rand"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"owngit/internal/state"
)

// A request that declares a length over the request limit is refused before
// Git starts, with 413 and the limit in the log. Refusing it later is not
// certain: receive-pack stops reading at the flush packet that ends an empty
// command list, and the backend can exit before the body reaches the limit
// (on Windows, whose pipes hold little, it usually does).
func TestADeclaredLengthOverTheRequestLimitIsRefusedBeforeGit(t *testing.T) {
	handler, _, _ := idleFixture(t, 16, time.Minute)
	useLimits(t, handler, func(limits *Limits) { limits.MaximumRequest = 64 << 10 })
	logs := captureLog(t)
	server := httptest.NewServer(handler)
	defer server.Close()
	// A request that reached Git would wait for this lock.
	lock := handler.Repositories.Locks.For("sample")
	lock.Lock()
	defer lock.Unlock()

	body := append([]byte("0000"), bytes.Repeat([]byte("0"), 512<<10)...)
	for round := 0; round < 3; round++ {
		connection, err := net.Dial("tcp", server.Listener.Addr().String())
		noErr(t, err)
		noErr(t, connection.SetDeadline(time.Now().Add(10*time.Second)))
		_, err = fmt.Fprintf(connection, "POST /git/sample.git/git-receive-pack HTTP/1.1\r\nHost: example.test\r\n"+
			"Content-Type: application/x-git-receive-pack-request\r\nContent-Length: %d\r\n\r\n", len(body))
		noErr(t, err)
		go func() { _, _ = connection.Write(body) }()
		response, err := http.ReadResponse(bufio.NewReader(connection), nil)
		noErr(t, err)
		_ = response.Body.Close()
		_ = connection.Close()
		if response.StatusCode != http.StatusRequestEntityTooLarge {
			t.Fatalf("round %d: status %d, want 413", round, response.StatusCode)
		}
	}
	if got := strings.Count(logs.String(), `Git push request for repository "sample" failed: request body exceeded the size limit`); got != 3 {
		t.Fatalf("log names the request limit %d times, want 3: %s", got, logs.String())
	}
}

// A request body that ends before its declared length stops Git at once.
// git-http-backend copies a declared length to Git and loops without end
// when its input closes early, so closing the input alone would keep it
// running, with the repository lock, until the operation limit. Here the
// failed read of the connection cancels the request, which stops Git; a read
// error of the request itself, such as the limit on an inflated body, stops it
// through the input (see TestSmartHTTPInflatesGzipBodiesWithinTheRequestLimit).
func TestABodyThatEndsEarlyStopsGitAtOnce(t *testing.T) {
	handler, work, head := idleFixture(t, 16, time.Minute)
	returned := make(chan time.Duration, 4)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		started := time.Now()
		handler.ServeHTTP(writer, request)
		if request.Method == http.MethodPost {
			returned <- time.Since(started)
		}
	}))
	defer server.Close()

	// One command and the start of a pack: receive-pack is still reading
	// the pack when the body ends.
	command := fmt.Sprintf("%s %s refs/heads/cut\x00report-status\n", strings.Repeat("0", len(head)), head)
	prefix := fmt.Sprintf("%04x%s0000PACK\x00\x00\x00\x02\x00\x00\x00\x01", 4+len(command), command)
	for round := 0; round < 3; round++ {
		connection, err := net.Dial("tcp", server.Listener.Addr().String())
		noErr(t, err)
		_, err = fmt.Fprintf(connection, "POST /git/sample.git/git-receive-pack HTTP/1.1\r\nHost: example.test\r\n"+
			"Content-Type: application/x-git-receive-pack-request\r\nContent-Length: %d\r\n\r\n%s", 64<<10, prefix)
		noErr(t, err)
		go func() { _, _ = io.Copy(io.Discard, connection) }()
		// Let Git start reading the pack, then end the body. The client
		// still reads, so writing the response does not stop Git either.
		time.Sleep(200 * time.Millisecond)
		noErr(t, connection.(*net.TCPConn).CloseWrite())
		select {
		case elapsed := <-returned:
			if elapsed > 5*time.Second {
				t.Fatalf("round %d: the transfer ended after %s", round, elapsed)
			}
		case <-time.After(10 * time.Second):
			t.Fatalf("round %d: the transfer did not end; Git kept running after the body ended", round)
		}
		_ = connection.Close()
	}

	// The repository is free: a push goes through at once.
	noErr(t, os.WriteFile(filepath.Join(work, "after.txt"), []byte("after\n"), 0o600))
	runHTTPGit(t, work, "add", ".")
	runHTTPGit(t, work, "commit", "-q", "-m", "after")
	started := time.Now()
	runHTTPGit(t, work, "push", "-q", server.URL+"/git/sample.git", "HEAD:refs/heads/main")
	if elapsed := time.Since(started); elapsed > 5*time.Second {
		t.Fatalf("push after the cut requests took %s", elapsed)
	}
}

// git-http-backend writes its headers before it reads a push, but they wait
// for the end of the request body. So when the request limit stops a push
// sent without a length before Git answered, the client gets 413 instead of
// an empty success.
func TestAChunkedPushOverTheRequestLimitGets413(t *testing.T) {
	handler, _, head := idleFixture(t, 16, time.Minute)
	useLimits(t, handler, func(limits *Limits) { limits.MaximumRequest = 256 << 10 })
	logs := captureLog(t)
	server := httptest.NewServer(handler)
	defer server.Close()

	// One command without capabilities, so Git sends nothing while it
	// reads, then a pack whose only object is larger than the limit.
	command := fmt.Sprintf("%s %s refs/heads/large\n", strings.Repeat("0", len(head)), head)
	var body bytes.Buffer
	fmt.Fprintf(&body, "%04x%s0000PACK\x00\x00\x00\x02\x00\x00\x00\x01", 4+len(command), command)
	content := make([]byte, 1<<20)
	_, err := rand.Read(content)
	noErr(t, err)
	size := len(content)
	body.WriteByte(byte(0x80 | 3<<4 | size&15))
	for size >>= 4; size > 0; size >>= 7 {
		next := byte(size & 0x7f)
		if size > 0x7f {
			next |= 0x80
		}
		body.WriteByte(next)
	}
	compressor := zlib.NewWriter(&body)
	_, err = compressor.Write(content)
	noErr(t, err)
	noErr(t, compressor.Close())

	connection, err := net.Dial("tcp", server.Listener.Addr().String())
	noErr(t, err)
	defer connection.Close()
	noErr(t, connection.SetDeadline(time.Now().Add(20*time.Second)))
	_, err = io.WriteString(connection, "POST /git/sample.git/git-receive-pack HTTP/1.1\r\nHost: example.test\r\n"+
		"Content-Type: application/x-git-receive-pack-request\r\nTransfer-Encoding: chunked\r\n\r\n")
	noErr(t, err)
	go func() {
		chunked := httputil.NewChunkedWriter(connection)
		_, _ = chunked.Write(body.Bytes())
		_ = chunked.Close()
		_, _ = io.WriteString(connection, "\r\n")
	}()
	response, err := http.ReadResponse(bufio.NewReader(connection), nil)
	noErr(t, err, "read the response")
	_ = response.Body.Close()
	if response.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("status %d, want 413; log:\n%s", response.StatusCode, logs.String())
	}
	if !strings.Contains(logs.String(), `Git push request for repository "sample" failed: request body exceeded the size limit`) {
		t.Fatalf("the log does not name the request limit:\n%s", logs.String())
	}
}

// Each transfer reads the limits the owner saved when it starts, so a new
// limit applies to the next transfer, and a saved value that cannot be read
// refuses the transfer instead of falling back to a default.
func TestTransfersUseTheLimitsSavedWhenTheyStart(t *testing.T) {
	manager, runner := newHTTPTestRepository(t)
	handler, err := New(runner, manager, "")
	noErr(t, err)
	handler.Authorize = func(*http.Request) (bool, error) { return true, nil }
	logs := captureLog(t)
	server := httptest.NewServer(handler)
	defer server.Close()
	push := func() int {
		t.Helper()
		body := append([]byte("0000"), bytes.Repeat([]byte("0"), 2<<20)...)
		response, err := http.Post(server.URL+"/git/sample.git/git-receive-pack", "application/x-git-receive-pack-request", bytes.NewReader(body))
		noErr(t, err)
		_, _ = io.Copy(io.Discard, response.Body)
		_ = response.Body.Close()
		return response.StatusCode
	}
	save := func(bytes int64) {
		t.Helper()
		noErr(t, manager.Store.SavePolicies(context.Background(), state.PolicyChange{GitTransfer: &state.GitTransferLimits{MaximumBytes: bytes, Operation: time.Minute, PerRepository: 4, ExtraSlots: 1, Idle: time.Minute, QueueWait: 90 * time.Second}}))
	}
	save(1 << 20)
	if status := push(); status != http.StatusRequestEntityTooLarge {
		t.Fatalf("a 2 MiB push under a 1 MiB limit: status %d, want 413", status)
	}
	save(4 << 20)
	if status := push(); status == http.StatusRequestEntityTooLarge {
		t.Fatal("a 2 MiB push under a 4 MiB limit was refused as too large")
	}

	// The same size bounds what a transfer sends: a clone of a 2 MiB
	// history is cut off under 1 MiB and completes under 4 MiB.
	work := filepath.Join(t.TempDir(), "work")
	runHTTPGit(t, "", "init", "--initial-branch=main", work)
	noise := make([]byte, 2<<20)
	_, err = rand.Read(noise)
	noErr(t, err)
	noErr(t, os.WriteFile(filepath.Join(work, "noise.bin"), noise, 0o600))
	runHTTPGit(t, work, "add", ".")
	runHTTPGit(t, work, "-c", "user.name=Test", "-c", "user.email=test@example.invalid", "commit", "-m", "noise")
	bare, err := manager.Path("sample")
	noErr(t, err)
	runHTTPGit(t, work, "push", bare, "HEAD:refs/heads/main")
	save(1 << 20)
	if output, err := httpGitCombined("", "clone", "--bare", server.URL+"/git/sample.git", filepath.Join(t.TempDir(), "cut")); err == nil {
		t.Fatalf("a 2 MiB clone under a 1 MiB limit completed:\n%s", output)
	}
	if !strings.Contains(logs.String(), "response exceeded the size limit") {
		t.Fatalf("the log does not name the response limit:\n%s", logs.String())
	}
	save(4 << 20)
	runHTTPGit(t, "", "clone", "--bare", server.URL+"/git/sample.git", filepath.Join(t.TempDir(), "whole"))
	noErr(t, manager.Store.Exec(context.Background(), `UPDATE metadata SET value='{"maximum_bytes":1}' WHERE key='git_transfer_limits'`))
	if status := push(); status != http.StatusConflict {
		t.Fatalf("a push under limits that cannot be read: status %d, want 409", status)
	}
	if !strings.Contains(logs.String(), "the Git transfer limits could not be read") {
		t.Fatalf("the log does not say why: %s", logs.String())
	}
}
