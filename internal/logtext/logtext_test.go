package logtext

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestPathIsCutWithItsLength(t *testing.T) {
	whole := "/" + strings.Repeat("a", maximumPath-1)
	if got := Path(whole); got != whole {
		t.Fatalf("a path of %d bytes was changed to %q", len(whole), got)
	}
	long := whole + "b%0A"
	if got, want := Path(long), whole+"...(cut, 260 bytes)"; got != want {
		t.Fatalf("Path=%q, want %q", got, want)
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
