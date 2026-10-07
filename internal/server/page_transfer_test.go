package server

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

func pageTransferServer(t *testing.T, idle time.Duration, content []byte, sendBuffer ...int) (*httptest.Server, <-chan error) {
	t.Helper()
	server, results, _ := timedPageTransferServer(t, idle, content, sendBuffer...)
	return server, results
}

type pageTransferTiming struct {
	elapsed         time.Duration
	deadlineExpired bool
}

func timedPageTransferServer(t *testing.T, idle time.Duration, content []byte, sendBuffer ...int) (*httptest.Server, <-chan error, <-chan pageTransferTiming) {
	t.Helper()
	results := make(chan error, 8)
	transfers := make(chan pageTransferTiming, 8)
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		request, deadlines, cancel := startDeadlines(writer, request, idle, idle/4, time.Hour, time.Second)
		defer cancel()
		defer deadlines.finish()
		writer.Header().Set("Content-Length", fmt.Sprint(len(content)))
		started := time.Now()
		err := writePage(writer, request, http.StatusOK, content)
		transfers <- pageTransferTiming{elapsed: time.Since(started), deadlineExpired: !time.Now().Before(deadlines.current)}
		results <- err
		if err != nil {
			panic(http.ErrAbortHandler)
		}
	}))
	if len(sendBuffer) > 0 {
		server.Config.ConnState = func(connection net.Conn, state http.ConnState) {
			if state == http.StateNew {
				_ = connection.(*net.TCPConn).SetWriteBuffer(sendBuffer[0])
			}
		}
	}
	server.Start()
	t.Cleanup(server.Close)
	return server, results, transfers
}

func TestPageTransferProgressOutlivesTheWorkDeadlineAndReusesConnection(t *testing.T) {
	if testing.Short() {
		t.Skip("uses a paced TCP transfer")
	}
	// Leave room for TCP window probes under race instrumentation. The helper's
	// work deadline is shorter than idle, and the paced transfer exceeds both.
	idle := 3 * time.Second
	workTimeout := idle - idle/4
	transferTime := 4 * time.Second
	content := bytes.Repeat([]byte("page bytes\n"), 1<<20)
	server, results := pageTransferServer(t, idle, content)
	connection, err := net.DialTimeout("tcp", server.Listener.Addr().String(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	if tcp, ok := connection.(*net.TCPConn); ok {
		_ = tcp.SetReadBuffer(64 << 10)
	}
	_ = connection.SetDeadline(time.Now().Add(15 * time.Second))
	reader := bufio.NewReader(connection)
	for round := 0; round < 2; round++ {
		_, err := fmt.Fprint(connection, "GET /page HTTP/1.1\r\nHost: example.invalid\r\n\r\n")
		if err != nil {
			t.Fatal(err)
		}
		response, err := http.ReadResponse(reader, nil)
		if err != nil {
			t.Fatal(err)
		}
		var received bytes.Buffer
		chunk := make([]byte, 64<<10)
		started := time.Now()
		for {
			n, err := response.Body.Read(chunk)
			received.Write(chunk[:n])
			if err == io.EOF {
				break
			}
			if err != nil {
				t.Fatalf("progressing body stopped at %d bytes: %v", received.Len(), err)
			}
			if round == 0 {
				// Pace by bytes, not read count: partial reads must not lengthen
				// the fixture unpredictably under instrumentation.
				time.Sleep(time.Duration(n) * transferTime / time.Duration(len(content)))
			}
		}
		response.Body.Close()
		if !bytes.Equal(received.Bytes(), content) || response.Close {
			t.Fatalf("transfer round %d differs or closes keep-alive", round)
		}
		if round == 0 {
			elapsed := time.Since(started)
			if elapsed < workTimeout || elapsed < idle {
				t.Fatal("fixture did not outlast the work and initial connection deadlines")
			}
			t.Logf("progressing transfer: %s, %d byte-identical bytes; work %s, idle %s", elapsed, received.Len(), workTimeout, idle)
		}
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
}

// These real-time socket checks are skipped with -short. Scaling TCP's
// deadlines would change its zero-window and retransmission behavior.
func TestPageTransferLongProgressCompletesByteIdentical(t *testing.T) {
	if testing.Short() {
		t.Skip("long socket transfer")
	}
	idle := 30 * time.Second
	transferTime := 40 * time.Second
	// Buffers smaller than one kernel page can stall a write even as queued
	// bytes drain. Keep normal-sized buffers and pace enough data that the
	// server, not just the client draining its buffer, outlasts idle.
	content := bytes.Repeat([]byte("p"), 4<<20)
	server, results, transfers := timedPageTransferServer(t, idle, content, 64<<10)
	connection, err := net.DialTimeout("tcp", server.Listener.Addr().String(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	if err := connection.(*net.TCPConn).SetReadBuffer(64 << 10); err != nil {
		t.Fatal(err)
	}
	_ = connection.SetDeadline(time.Now().Add(60 * time.Second))
	fmt.Fprint(connection, "GET /page HTTP/1.1\r\nHost: example.invalid\r\n\r\n")
	response, err := http.ReadResponse(bufio.NewReader(connection), nil)
	if err != nil {
		t.Fatal(err)
	}
	var received bytes.Buffer
	chunk := make([]byte, 64<<10)
	started := time.Now()
	for {
		n, err := response.Body.Read(chunk)
		received.Write(chunk[:n])
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("long progressing body stopped: %v", err)
		}
		time.Sleep(time.Duration(n) * transferTime / time.Duration(len(content)))
	}
	response.Body.Close()
	elapsed := time.Since(started)
	if elapsed <= idle || !bytes.Equal(received.Bytes(), content) {
		t.Fatalf("long transfer took %s and delivered %d of %d bytes", elapsed, received.Len(), len(content))
	}
	if err := <-results; err != nil {
		t.Fatal(err)
	}
	serverElapsed := (<-transfers).elapsed
	if serverElapsed <= idle {
		t.Fatalf("server transfer ended after %s; buffered client reads do not prove renewed deadlines", serverElapsed)
	}
	t.Logf("progressing transfer: client %s, server %s, %d byte-identical bytes; idle %s", elapsed, serverElapsed, received.Len(), idle)
}

// The default-deadline stalled-reader control is skipped with -short too.
func TestPageTransferLongStallIsCut(t *testing.T) {
	if testing.Short() {
		t.Skip("real-time stalled socket")
	}
	server, results, transfers := timedPageTransferServer(t, 30*time.Second, bytes.Repeat([]byte("x"), 16<<20))
	connection, err := net.DialTimeout("tcp", server.Listener.Addr().String(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	fmt.Fprint(connection, "GET /page HTTP/1.1\r\nHost: example.invalid\r\n\r\n")
	select {
	case err := <-results:
		timing := <-transfers
		// Compare with the server's last write deadline, not the client's request time.
		if !errors.Is(err, os.ErrDeadlineExceeded) || !timing.deadlineExpired || timing.elapsed < 30*time.Second {
			t.Fatalf("stalled reader did not reach its write deadline: %v after %s (expired=%v)", err, timing.elapsed, timing.deadlineExpired)
		}
		t.Logf("stalled reader cut after %s on the server", timing.elapsed)
	case <-time.After(40 * time.Second):
		t.Fatal("default-deadline stalled reader did not stop")
	}
}

func TestPageTransferStallAndDisconnectAreNotCompleteReplies(t *testing.T) {
	content := bytes.Repeat([]byte("x"), 16<<20)
	for _, disconnect := range []bool{false, true} {
		t.Run(fmt.Sprint("disconnect=", disconnect), func(t *testing.T) {
			server, results := pageTransferServer(t, 150*time.Millisecond, content)
			connection, err := net.DialTimeout("tcp", server.Listener.Addr().String(), time.Second)
			if err != nil {
				t.Fatal(err)
			}
			defer connection.Close()
			_ = connection.SetDeadline(time.Now().Add(3 * time.Second))
			fmt.Fprint(connection, "GET /page HTTP/1.1\r\nHost: example.invalid\r\n\r\n")
			if disconnect {
				connection.Close()
			}
			select {
			case err := <-results:
				if err == nil {
					t.Fatal("unread transfer was reported complete")
				}
			case <-time.After(2 * time.Second):
				t.Fatal("stalled transfer did not stop")
			}
			if !disconnect {
				response, err := http.ReadResponse(bufio.NewReader(connection), nil)
				if err != nil {
					t.Fatal(err)
				}
				body, err := io.ReadAll(response.Body)
				response.Body.Close()
				if err == nil || len(body) >= len(content) || response.ContentLength != int64(len(content)) {
					t.Fatalf("partial reply framed as complete: bytes=%d err=%v length=%d", len(body), err, response.ContentLength)
				}
			}
		})
	}
}

func TestPageChunksAreBoundedAndExpiredAuthenticationCannotRestartThem(t *testing.T) {
	writer := &recordPageWriter{header: make(http.Header)}
	request := httptest.NewRequest(http.MethodGet, "/page", nil)
	request, deadlines, cancel := startDeadlines(writer, request, time.Millisecond, 0, time.Hour, time.Second)
	defer cancel()
	deadlines.current = time.Now().Add(-time.Second)
	before := writer.deadlines
	if err := writePage(writer, request, http.StatusOK, []byte(strings.Repeat("x", pageReplyChunkBytes*3+1))); err != nil {
		t.Fatal(err)
	}
	if writer.largest > pageReplyChunkBytes || writer.deadlines != before {
		t.Fatalf("chunk=%d deadlines refreshed past auth=%d", writer.largest, writer.deadlines-before)
	}
}

type recordPageWriter struct {
	header             http.Header
	largest, deadlines int
}

func (w *recordPageWriter) Header() http.Header { return w.header }
func (*recordPageWriter) WriteHeader(int)       {}
func (w *recordPageWriter) Write(content []byte) (int, error) {
	w.largest = max(w.largest, len(content))
	return len(content), nil
}
func (w *recordPageWriter) SetWriteDeadline(time.Time) error { w.deadlines++; return nil }
func (*recordPageWriter) SetReadDeadline(time.Time) error    { return nil }
func (*recordPageWriter) Flush()                             {}
