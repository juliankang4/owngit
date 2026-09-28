package logtext

import (
	"context"
	"errors"
	"fmt"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestRequestIsCutWithItsLength(t *testing.T) {
	whole := "/" + strings.Repeat("a", maximumPath-1)
	if got, want := Request(httptest.NewRequest("PROPFIND", whole, nil)), "PROPFIND "+whole; got != want {
		t.Fatalf("Request=%q, want %q", got, want)
	}
	method := strings.Repeat("M", maximumMethod+1)
	long := httptest.NewRequest(method, whole+"b%0A", nil)
	if got, want := Request(long), method[:maximumMethod]+"...(cut, 33 bytes) "+whole+"...(cut, 260 bytes)"; got != want {
		t.Fatalf("Request=%q, want %q", got, want)
	}
}

func TestCauseIsQuotedWithItsChain(t *testing.T) {
	client := clientError{cause: errors.New("fatal: bad object\n\x1b[31m")}
	if got, want := Cause(client), `"shown to the client: fatal: bad object\n\x1b[31m"`; got != want {
		t.Fatalf("Cause=%s, want %s", got, want)
	}
}

// clientError is written for a client and carries its cause only in its
// chain, as a pull request problem does.
type clientError struct{ cause error }

func (e clientError) Error() string { return "shown to the client" }
func (e clientError) Unwrap() error { return e.cause }

// matchesIntended is an error type that names an intended state by Is.
type matchesIntended struct{}

func (matchesIntended) Error() string        { return "runtime unavailable" }
func (matchesIntended) Is(target error) bool { return target == errIntended }

var errIntended = errors.New("intended")

func TestIntendedOnlyWhenEveryBranchIs(t *testing.T) {
	failure := errors.New("disk I/O error")
	for _, check := range []struct {
		what string
		err  error
		want bool
	}{
		{"the state", errIntended, true},
		{"a wrapped state", fmt.Errorf("read: %w", errIntended), true},
		{"a type that is the state", fmt.Errorf("start: %w", matchesIntended{}), true},
		{"a joined pair of states", errors.Join(errIntended, fmt.Errorf("stop: %w", context.Canceled)), true},
		{"a failure", failure, false},
		{"a state joined with a failure", errors.Join(errIntended, failure), false},
		{"a state wrapped with a failure", fmt.Errorf("%w: %w", errIntended, failure), false},
		{"a failure deep in a joined tree", errors.Join(errIntended, errors.Join(context.Canceled, failure)), false},
		{"a client error over a state", clientError{cause: errIntended}, true},
		{"a client error without a cause", clientError{}, false},
	} {
		if got := Intended(check.err, errIntended, context.Canceled); got != check.want {
			t.Errorf("Intended(%s)=%v, want %v", check.what, got, check.want)
		}
	}
}

// A long cause keeps its beginning, which names what failed, and its end,
// which holds the deepest cause, and is marked with its full length.
func TestLongCauseIsCutWithItsLength(t *testing.T) {
	middle := strings.Repeat("progress line\n", 5000)
	err := fmt.Errorf("git fetch: exit status 128: %s", middle+"fatal: 한글 end")
	text := err.Error()
	got := Cause(err)
	if len(got) >= 4*maximumCause+64 {
		t.Fatalf("Cause is %d bytes for a %d-byte cause", len(got), len(text))
	}
	if !strings.HasPrefix(got, `"git fetch: exit status 128: progress line\n`) || !strings.HasSuffix(got, `fatal: 한글 end"`) ||
		!strings.Contains(got, fmt.Sprintf(`"...(cut, %d bytes)..."`, len(text))) {
		t.Fatalf("Cause=%.120s...%s", got, got[len(got)-80:])
	}
	// A run of bytes that are not UTF-8 is cut at the same bound; each byte
	// is quoted as \xHH, so this is the longest a cause can be on a line.
	invalid := errors.New("git: " + strings.Repeat("\x80", 70000))
	if got := Cause(invalid); len(got) > 4*(maximumCause+2*(utf8.UTFMax-1))+4+len("...(cut, 70005 bytes)...") ||
		!strings.HasPrefix(got, `"git: \x80`) || !strings.Contains(got, "...(cut, 70005 bytes)...") {
		t.Fatalf("Cause of invalid bytes is %d bytes: %.60s", len(got), got)
	}
	short := errors.New(strings.Repeat("x", maximumCause))
	if got, want := Cause(short), strconv.Quote(short.Error()); got != want {
		t.Fatal("a cause of the maximum length was cut")
	}
}
