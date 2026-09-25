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

// Only a complete report that refused every command before a write proves
// that a push changed no ref. Anything else, including a report cut short,
// counts as a possible change.
func TestPushReportProvesUnchangedRefsOnlyFromACompleteRefusal(t *testing.T) {
	const ref = "refs/notes/refused"
	status := func(lines ...string) string {
		var content string
		for _, line := range lines {
			content += packetLine(line + "\n")
		}
		return content + "0000"
	}
	declined := status("unpack ok", "ng "+ref+" hook declined")
	for _, test := range []struct {
		name, response, stderr string
		want                   bool
	}{
		{"declined by the hook", sideband(2, "OwnGit accepts only branch and tag refs\n") + sideband(1, declined) + "0000", "", true},
		{"declined without a side band", declined, "", true},
		{"unpack failure", sideband(1, status("unpack index-pack abnormal exit", "ng "+ref+" unpacker error")) + "0000", "", true},
		{"accepted ref", sideband(1, status("unpack ok", "ok refs/heads/main", "ng "+ref+" hook declined")) + "0000", "", false},
		{"atomic failure", sideband(1, status("unpack ok", "ng refs/heads/main atomic push failure", "ng "+ref+" hook declined")) + "0000", "", false},
		{"failed update", sideband(1, status("unpack ok", "ng refs/heads/main failed to update ref")) + "0000", "", false},
		{"history not preserved", sideband(2, "could not preserve previous history\n") + sideband(1, declined) + "0000", "", false},
		{"history not preserved on stderr", declined, "could not preserve previous history\n", false},
		{"unknown line", sideband(1, status("unpack ok", "option refname refs/heads/main", "ng "+ref+" hook declined")) + "0000", "", false},
		{"line after the list", sideband(1, declined+packetLine("ok refs/heads/main\n")) + "0000", "", false},
		{"cut before the closing flush", sideband(1, packetLine("unpack ok\n")+packetLine("ng "+ref+" hook declined\n")), "", false},
		{"no unpack line", sideband(1, packetLine("ng "+ref+" hook declined\n")+"0000") + "0000", "", false},
		{"fatal band", sideband(1, declined) + sideband(3, "fatal: something\n"), "", false},
		{"empty", "", "", false},
	} {
		for _, step := range []int{1, 5, 1 << 20} {
			report := &pushReport{}
			for start := 0; start < len(test.response); start += step {
				report.Write([]byte(test.response[start:min(start+step, len(test.response))]))
			}
			report.scan([]byte(test.stderr))
			if got := report.refsUnchanged(); got != test.want {
				t.Errorf("%s in writes of %d bytes: refsUnchanged=%v, want %v", test.name, step, got, test.want)
			}
		}
	}
}
