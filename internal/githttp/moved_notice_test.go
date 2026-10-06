package githttp

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// copiedBody runs copyCGIResponse over a CGI answer whose body is body and
// returns what the client received and what the observer read.
func copiedBody(t *testing.T, body, notice string) (string, string) {
	t.Helper()
	answer := "Status: 200 OK\r\nContent-Type: application/x-git-upload-pack-result\r\n\r\n" + body
	recorder := httptest.NewRecorder()
	writer := &responseState{
		ResponseWriter: recorder,
		deadlines:      &transferDeadlines{controller: http.NewResponseController(recorder)},
		bodyEnded:      func() bool { return true },
		flushDelay:     time.Hour,
	}
	var observed bytes.Buffer
	if err := (&Handler{}).copyCGIResponse(writer, strings.NewReader(answer), &observed, 1<<20, notice); err != nil {
		t.Fatalf("copyCGIResponse: %v", err)
	}
	return recorder.Body.String(), observed.String()
}

// The notice of a request that named an earlier address goes into the response
// body at the one point the Git protocol carries a server message: before the
// first side-band packet, wherever that is. These cases pin the body the
// client receives, and that the observer reads the backend's own bytes once
// and no more: it feeds the push report, which the notice must not reach.
// A section delimiter is four bytes on the wire although "0001" reads as one,
// and a protocol 2 shallow fetch sends one between its shallow-info section
// and its pack, so a scan that reads one byte there misreads the pack and
// loses the message.
func TestMovedNoticeBoundaries(t *testing.T) {
	const notice = "This repository moved to http://host/git/renamed.git"
	inserted := sideband(2, notice+"\n")
	shallow := packetLine("shallow-info\n") + packetLine("shallow "+strings.Repeat("a", 40)+"\n") + "0001"
	pack := packetLine("packfile\n") + sideband(1, "PACKdata") + "0000"
	for _, test := range []struct {
		name string
		body string
		want string
	}{
		{"a section header before the pack", pack, packetLine("packfile\n") + inserted + sideband(1, "PACKdata") + "0000"},
		{"the delimiter of a shallow-info section", shallow + pack, shallow + packetLine("packfile\n") + inserted + sideband(1, "PACKdata") + "0000"},
		{"a delimiter and then a flush", "0001" + "0000", "0001" + "0000"},
		{"a flush before any side-band packet", packetLine("ack\n") + "0000", packetLine("ack\n") + "0000"},
		{"a body that is not pkt-lines", "PACKdata", "PACKdata"},
	} {
		t.Run(test.name, func(t *testing.T) {
			client, observed := copiedBody(t, test.body, notice)
			if client != test.want {
				t.Fatalf("the client received %q, want %q", client, test.want)
			}
			if observed != test.body {
				t.Fatalf("the observer read %q, want the backend's body %q", observed, test.body)
			}
		})
	}
}
