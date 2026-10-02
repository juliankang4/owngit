package server

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestPageTransferTotalCapCutsAProgressingReply(t *testing.T) {
	if testing.Short() {
		t.Skip("uses a paced TCP transfer")
	}
	previous := pageTransferTimeLimit
	pageTransferTimeLimit = time.Second
	t.Cleanup(func() { pageTransferTimeLimit = previous })
	prefix := `<p data-page-transfer-pending>Receiving the page</p>`
	tail := `<link data-page-transfer-complete href="/assets/page-complete.css">`
	content := []byte(prefix + strings.Repeat("x", 8<<20) + tail)
	server, results := pageTransferServer(t, 3*time.Second, content, 4096)
	connection, err := net.DialTimeout("tcp", server.Listener.Addr().String(), time.Second)
	noErr(t, err)
	defer connection.Close()
	noErr(t, connection.(*net.TCPConn).SetReadBuffer(64<<10))
	noErr(t, connection.SetDeadline(time.Now().Add(10*time.Second)))
	started := time.Now()
	_, err = fmt.Fprint(connection, "GET /page HTTP/1.1\r\nHost: example.invalid\r\n\r\n")
	noErr(t, err)
	response, err := http.ReadResponse(bufio.NewReader(connection), nil)
	noErr(t, err)
	defer response.Body.Close()
	var received bytes.Buffer
	chunk := make([]byte, 64<<10)
	for {
		n, readErr := response.Body.Read(chunk)
		received.Write(chunk[:n])
		if readErr != nil {
			if readErr != io.ErrUnexpectedEOF {
				t.Fatalf("cut reply error=%v", readErr)
			}
			break
		}
		time.Sleep(40 * time.Millisecond)
	}
	if err := <-results; err == nil {
		t.Fatal("total cap did not fail the response write")
	}
	elapsed := time.Since(started)
	if elapsed < time.Second || elapsed >= 3*time.Second || received.Len() <= 4096 || received.Len() >= len(content) {
		t.Fatalf("cap control: elapsed=%s bytes=%d", elapsed, received.Len())
	}
	if !strings.Contains(received.String(), prefix) || strings.Contains(received.String(), tail) {
		t.Fatal("cut reply lost its pending notice or included completion")
	}
	t.Logf("progressing client cut after %s; received %d of %d bytes without completion", elapsed, received.Len(), len(content))
}
