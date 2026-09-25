package githttp

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"
)

// A client may send its next request on the same connection unless the
// response said Connection: close. So after a response to a request whose
// body the server did not read to the end, the connection must either be
// announced as closing or still serve the next request. Otherwise a client
// that reuses it gets EOF.
func TestResponseToAnUnreadBodyKeepsTheConnectionUsable(t *testing.T) {
	manager, runner := newHTTPTestRepository(t)
	runner.TerminationGrace = 25 * time.Millisecond
	handler, err := New(runner, manager, "", 1)
	noErr(t, err)
	backend, err := os.Executable()
	noErr(t, err)
	handler.BackendPath = backend
	authorized := true
	handler.Authorize = func(*http.Request) bool { return authorized }
	handler.MaximumRequest = 1 << 20
	handler.QueueWait = 100 * time.Millisecond
	server := httptest.NewServer(handler)
	defer server.Close()
	address := server.Listener.Addr().String()

	bomb := gzipBytes(t, make([]byte, 64<<20))
	corrupt := gzipBytes(t, bytes.Repeat([]byte("0032have 0000000000000000000000000000000000000000\n"), 2000))
	corrupt = append(corrupt[:len(corrupt)/2:len(corrupt)/2], bytes.Repeat([]byte{0xff}, 1024)...)
	small := bytes.Repeat([]byte("0"), 64<<10)
	large := bytes.Repeat([]byte("0"), 512<<10)
	tooLarge := bytes.Repeat([]byte("0"), 2<<20)
	type request struct {
		name, service, encoding string
		body                    []byte
	}
	cases := []request{
		{"gzip body inflating past the request limit", "git-receive-pack", "gzip", bomb},
		{"body past the request limit", "git-receive-pack", "", tooLarge},
		{"gzip body corrupt in the middle", "git-upload-pack", "gzip", corrupt},
		{"body that is not gzip", "git-upload-pack", "gzip", []byte("not gzip at all")},
		{"unsupported encoding, small body", "git-upload-pack", "br", small},
		{"unsupported encoding, large body", "git-upload-pack", "br", large},
	}
	check := func(t *testing.T, address string, test request) {
		t.Helper()
		connection, err := net.Dial("tcp", address)
		noErr(t, err)
		defer connection.Close()
		noErr(t, connection.SetDeadline(time.Now().Add(10*time.Second)))
		encoding := ""
		if test.encoding != "" {
			encoding = "Content-Encoding: " + test.encoding + "\r\n"
		}
		_, err = fmt.Fprintf(connection, "POST /git/sample.git/%s HTTP/1.1\r\nHost: example.test\r\nContent-Type: application/x-%s-request\r\n%sContent-Length: %d\r\n\r\n",
			test.service, test.service, encoding, len(test.body))
		noErr(t, err)
		// The server may answer before it read the body, and may never read
		// all of it.
		go func() { _, _ = connection.Write(test.body) }()
		reader := bufio.NewReader(connection)
		response, err := http.ReadResponse(reader, nil)
		noErr(t, err, "read the first response")
		_, err = io.Copy(io.Discard, response.Body)
		noErr(t, err, "read the first response body")
		response.Body.Close()
		t.Logf("status %d, Connection: close %v", response.StatusCode, response.Close)
		if response.Close {
			return
		}
		// Give the server time to close a connection it will not reuse.
		time.Sleep(100 * time.Millisecond)
		_, err = io.WriteString(connection, "POST /git/sample.git/git-upload-pack HTTP/1.1\r\nHost: example.test\r\n"+
			"Content-Type: application/x-git-upload-pack-request\r\nContent-Length: 4\r\n\r\n0000")
		if err == nil {
			var next *http.Response
			next, err = http.ReadResponse(reader, nil)
			if err == nil {
				_, err = io.Copy(io.Discard, next.Body)
				next.Body.Close()
			}
			if err == nil {
				return
			}
		}
		t.Fatalf("status %d kept the connection open (no Connection: close), but the next request on it failed: %v", response.StatusCode, err)
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) { check(t, address, test) })
	}

	t.Run("authentication required", func(t *testing.T) {
		authorized = false
		defer func() { authorized = true }()
		check(t, address, request{service: "git-receive-pack", body: large})
	})
	t.Run("no free transfer slot", func(t *testing.T) {
		// An endless ref advertisement that nobody reads holds the only slot.
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		holder, err := http.NewRequestWithContext(ctx, http.MethodGet, server.URL+"/git/sample.git/info/refs?service=git-upload-pack", nil)
		noErr(t, err)
		response, err := http.DefaultClient.Do(holder)
		noErr(t, err)
		defer response.Body.Close()
		waitFor(t, 5*time.Second, "the slot holder", func() bool { return handler.Active() == 1 })
		check(t, address, request{service: "git-receive-pack", body: small})
	})
	t.Run("request limit reached in the gzip header", func(t *testing.T) {
		tiny, err := New(runner, manager, backend, 1)
		noErr(t, err)
		tiny.Authorize = func(*http.Request) bool { return true }
		tiny.MaximumRequest = 8
		tinyServer := httptest.NewServer(tiny)
		defer tinyServer.Close()
		for _, body := range [][]byte{bomb, gzipBytes(t, large)} {
			check(t, tinyServer.Listener.Addr().String(), request{service: "git-upload-pack", encoding: "gzip", body: body})
		}
	})
	t.Run("push that Git stops reading", func(t *testing.T) {
		// git-http-backend sends its headers before it reads a push, and
		// receive-pack stops at the flush packet that ends an empty command
		// list, leaving the rest of the body unread.
		real, err := New(runner, manager, "", 1)
		noErr(t, err)
		real.Authorize = func(*http.Request) bool { return true }
		realServer := httptest.NewServer(real)
		defer realServer.Close()
		for _, rest := range [][]byte{small, large} {
			check(t, realServer.Listener.Addr().String(), request{service: "git-receive-pack", body: append([]byte("0000"), rest...)})
		}

	})
}
