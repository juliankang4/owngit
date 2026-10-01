package server

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func pageTransferServer(t *testing.T, idle time.Duration, content []byte, sendBuffer ...int) (*httptest.Server, <-chan error) {
	t.Helper()
	results := make(chan error, 8)
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		request, deadlines, cancel := startDeadlines(writer, request, idle, idle/4, time.Hour, time.Second)
		defer cancel()
		defer deadlines.finish()
		writer.Header().Set("Content-Length", fmt.Sprint(len(content)))
		err := writePage(writer, request, http.StatusOK, content)
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
	return server, results
}

func TestPageTransferProgressOutlivesTheWorkDeadlineAndReusesConnection(t *testing.T) {
	content := bytes.Repeat([]byte("page bytes\n"), 1<<20)
	server, results := pageTransferServer(t, 200*time.Millisecond, content)
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
				time.Sleep(5 * time.Millisecond)
			}
		}
		response.Body.Close()
		if !bytes.Equal(received.Bytes(), content) || response.Close {
			t.Fatalf("transfer round %d differs or closes keep-alive", round)
		}
		if round == 0 && time.Since(started) < 200*time.Millisecond {
			t.Fatal("fixture did not outlast the work deadline")
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
	content := bytes.Repeat([]byte("p"), 64<<10)
	server, results := pageTransferServer(t, 30*time.Second, content, 1024)
	connection, err := net.DialTimeout("tcp", server.Listener.Addr().String(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	_ = connection.(*net.TCPConn).SetReadBuffer(1024)
	_ = connection.SetDeadline(time.Now().Add(120 * time.Second))
	fmt.Fprint(connection, "GET /page HTTP/1.1\r\nHost: example.invalid\r\n\r\n")
	response, err := http.ReadResponse(bufio.NewReader(connection), nil)
	if err != nil {
		t.Fatal(err)
	}
	var received bytes.Buffer
	chunk := make([]byte, 1024)
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
		time.Sleep(750 * time.Millisecond)
	}
	response.Body.Close()
	elapsed := time.Since(started)
	if elapsed <= 30*time.Second || !bytes.Equal(received.Bytes(), content) {
		t.Fatalf("long transfer took %s and delivered %d of %d bytes", elapsed, received.Len(), len(content))
	}
	if err := <-results; err != nil {
		t.Fatal(err)
	}
	t.Logf("progressing transfer: %s, %d byte-identical bytes", elapsed, received.Len())
}

// The default-deadline stalled-reader control is skipped with -short too.
func TestPageTransferLongStallIsCut(t *testing.T) {
	if testing.Short() {
		t.Skip("real-time stalled socket")
	}
	server, results := pageTransferServer(t, 30*time.Second, bytes.Repeat([]byte("x"), 16<<20))
	connection, err := net.DialTimeout("tcp", server.Listener.Addr().String(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	fmt.Fprint(connection, "GET /page HTTP/1.1\r\nHost: example.invalid\r\n\r\n")
	started := time.Now()
	select {
	case err := <-results:
		if err == nil || time.Since(started) < 30*time.Second {
			t.Fatalf("stalled reader ended early or passed: %v after %s", err, time.Since(started))
		}
	case <-time.After(40 * time.Second):
		t.Fatal("default-deadline stalled reader did not stop")
	}
	t.Logf("stalled reader cut after %s", time.Since(started))
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
