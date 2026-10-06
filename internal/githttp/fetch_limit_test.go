package githttp

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"strings"
	"testing"
	"time"
)

// An upload-pack request over the bound the backend buffers is refused with
// 413 and the message a refused want gets, whether the request declares its
// length or sends the same body in chunks. Answering 502 instead would blame
// OwnGit for a request it refused on purpose.
func TestFetchRequestOverTheBufferBoundGets413(t *testing.T) {
	for _, chunked := range []bool{false, true} {
		name := "declared length"
		if chunked {
			name = "chunked body"
		}
		t.Run(name, func(t *testing.T) {
			handler, _, head := idleFixture(t, 16, time.Minute)
			logs := captureLog(t)
			server := httptest.NewServer(handler)
			defer server.Close()

			// A fetch whose want list is padded one mebibyte past the bound the
			// Git backend buffers, as an over-bound request looks.
			var padded strings.Builder
			padded.WriteString(pkt("command=fetch\n") + pkt("object-format=sha1\n") + "0001" + pkt("want "+head+"\n"))
			for padded.Len() < maximumFetchRequest+(1<<20) {
				padded.WriteString(pkt("have " + strings.Repeat("x", 65516) + "\n"))
			}
			padded.WriteString("0000" + pkt("done\n") + "0000")
			body := []byte(padded.String())
			connection, err := net.Dial("tcp", server.Listener.Addr().String())
			noErr(t, err)
			defer connection.Close()
			noErr(t, connection.SetDeadline(time.Now().Add(20*time.Second)))
			headers := "POST /git/sample.git/git-upload-pack HTTP/1.1\r\nHost: example.test\r\n" +
				"Content-Type: application/x-git-upload-pack-request\r\n"
			if chunked {
				_, err = io.WriteString(connection, headers+"Transfer-Encoding: chunked\r\n\r\n")
			} else {
				_, err = io.WriteString(connection, fmt.Sprintf("%sContent-Length: %d\r\n\r\n", headers, len(body)))
			}
			noErr(t, err)
			go func() {
				if chunked {
					writer := httputil.NewChunkedWriter(connection)
					_, _ = writer.Write(body)
					_ = writer.Close()
					_, _ = io.WriteString(connection, "\r\n")
					return
				}
				_, _ = connection.Write(body)
			}()
			response, err := http.ReadResponse(bufio.NewReader(connection), nil)
			noErr(t, err, "read the response")
			answer, err := io.ReadAll(response.Body)
			noErr(t, err)
			_ = response.Body.Close()
			if response.StatusCode != http.StatusRequestEntityTooLarge {
				t.Fatalf("status %d, want 413; body=%q log:\n%s", response.StatusCode, answer, logs.String())
			}
			if !strings.Contains(string(answer), requestTooLarge) {
				t.Fatalf("body=%q, want %q", answer, requestTooLarge)
			}
			logged := logs.String()
			bound := `Git fetch request for repository "sample" failed: `
			if !strings.Contains(logged, bound+requestTooLarge) && !strings.Contains(logged, bound+errFetchTooLarge.Error()) {
				t.Fatalf("the log does not name the bound the request passed:\n%s", logged)
			}
		})
	}
}
