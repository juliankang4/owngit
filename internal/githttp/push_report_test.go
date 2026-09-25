package githttp

import (
	"fmt"
	"strings"
	"testing"
)

func packetLine(payload string) string {
	return fmt.Sprintf("%04x%s", len(payload)+4, payload)
}

func sideband(band byte, payload string) string {
	return packetLine(string([]byte{band}) + payload)
}

// The push report finds failures in both report framings, split at any byte,
// and describes them only with fixed text (QA-023).
func TestPushReportClassifiesInBandFailures(t *testing.T) {
	const secret = "refs/heads/request-secret"
	for _, test := range []struct {
		name, response, stderr, want string
	}{
		{"success", sideband(2, "Resolving deltas\n") + sideband(1, packetLine("unpack ok\n")+packetLine("ok "+secret+"\n")+"0000") + "0000", "", ""},
		{"disk full", sideband(2, "error: unable to write loose object file: No space left on device\n") +
			sideband(1, packetLine("unpack unpack-objects abnormal exit\n")+packetLine("ng "+secret+" unpacker error\n")+"0000") + "0000",
			"", "push refused: repository storage is full"},
		{"read-only", sideband(1, packetLine("unpack unable to create temporary object directory\n")+packetLine("ng "+secret+" unpacker error\n")+"0000") + "0000",
			"", "push refused: repository storage is not writable"},
		{"hook declined", sideband(1, packetLine("unpack ok\n")+packetLine("ng "+secret+" pre-receive hook declined\n")+"0000") + "0000",
			"", "push refused: a server hook declined a ref update"},
		{"other refusal", sideband(1, packetLine("unpack ok\n")+packetLine("ng "+secret+" failed to update ref\n")+"0000") + "0000",
			"", "push refused: Git refused a ref update"},
		{"fatal band", sideband(3, "fatal: something\n"), "", "Git backend reported a fatal error"},
		{"no side band", packetLine("unpack index-pack abnormal exit\n") + packetLine("ng "+secret+" unpacker error\n") + "0000",
			"fatal: write error: No space left on device\n", "push refused: repository storage is full"},
	} {
		for _, step := range []int{1, 3, 7, 1 << 20} {
			report := &pushReport{}
			for start := 0; start < len(test.response); start += step {
				end := min(start+step, len(test.response))
				report.Write([]byte(test.response[start:end]))
			}
			report.scan([]byte(test.stderr))
			if got := report.reason(); got != test.want || strings.Contains(got, "secret") {
				t.Errorf("%s in writes of %d bytes: reason %q, want %q", test.name, step, got, test.want)
			}
		}
	}
	report := &pushReport{}
	report.Write([]byte("zzzz" + strings.Repeat("x", 1<<20)))
	if !report.stopped || len(report.pending) != 0 || report.reason() != "" {
		t.Fatal("a malformed response was not ignored")
	}
}
