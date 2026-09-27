// Package logtext writes the parts of a server log line that come from a
// request or an error, by the same rules in every package that logs them:
// a request path is cut to a short prefix, an error is quoted onto its line,
// and an error is left out only when all of it is a state that works as
// intended.
package logtext

import (
	"errors"
	"fmt"
	"reflect"
	"strconv"
	"strings"
)

// maximumPath is the longest request path a log line holds whole.
const maximumPath = 256

// Path is an escaped request path for a log line. A path longer than
// maximumPath is cut to that prefix and marked with its full length, so a
// request cannot make one line as long as its path. An escaped path is
// printable ASCII, so the prefix is too.
func Path(escaped string) string {
	if len(escaped) <= maximumPath {
		return escaped
	}
	return fmt.Sprintf("%s...(cut, %d bytes)", escaped[:maximumPath], len(escaped))
}

// Cause is err for a log line: its text followed by each wrapped cause that
// text leaves out, quoted, so text such as Git's error output cannot start a
// line of its own. An error written for a client, such as a pull request
// problem, carries the operator's cause only in its chain. err is never nil.
func Cause(err error) string {
	text := err.Error()
	for cause := errors.Unwrap(err); cause != nil; cause = errors.Unwrap(cause) {
		if detail := cause.Error(); !strings.Contains(text, detail) {
			text += ": " + detail
		}
	}
	return strconv.Quote(text)
}

// Intended reports whether every cause in err's tree is one of the intended
// states: each branch of a joined error must end in one. A single intended
// state beside a real failure does not hide that failure, as errors.Is on
// the whole tree would.
func Intended(err error, states ...error) bool {
	if err == nil {
		return false
	}
	for _, state := range states {
		if is(err, state) {
			return true
		}
	}
	switch wrapped := err.(type) {
	case interface{ Unwrap() []error }:
		causes := wrapped.Unwrap()
		for _, cause := range causes {
			if !Intended(cause, states...) {
				return false
			}
		}
		return len(causes) > 0
	case interface{ Unwrap() error }:
		return Intended(wrapped.Unwrap(), states...)
	}
	return false
}

// is reports whether err itself, not a cause it wraps, is target, as
// errors.Is decides for one error.
func is(err, target error) bool {
	if reflect.TypeOf(err).Comparable() && err == target {
		return true
	}
	match, ok := err.(interface{ Is(error) bool })
	return ok && match.Is(target)
}
