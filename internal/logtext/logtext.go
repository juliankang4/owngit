// Package logtext writes the parts of a server log line that come from a
// request or an error, by the same rules in every package that logs them:
// a request's method and path are cut to a short prefix, an error is quoted
// onto its line and cut to a bounded size, and an error is left out only
// when all of it is a state that works as intended. Each part a line takes
// from a request or an error is bounded, so neither can make a line long.
package logtext

import (
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"strconv"
	"strings"
	"unicode/utf8"
)

const (
	// maximumMethod is the longest request method a log line holds whole.
	// The longest registered method, UPDATEREDIRECTREF, has 17 bytes.
	maximumMethod = 32
	// maximumPath is the longest request path a log line holds whole.
	maximumPath = 256
	// maximumCause is the most bytes of an error's text a log line holds.
	maximumCause = 4096
)

// Request is a request's method and escaped path for a log line, each longer
// than its maximum cut to that prefix and marked with its full length. Both
// are printable ASCII (net/http accepts only a token as a method), so each
// prefix is too.
func Request(request *http.Request) string {
	return cut(request.Method, maximumMethod) + " " + cut(request.URL.EscapedPath(), maximumPath)
}

func cut(text string, maximum int) string {
	if len(text) <= maximum {
		return text
	}
	return fmt.Sprintf("%s...(cut, %d bytes)", text[:maximum], len(text))
}

// Cause is err for a log line: Quote(Chain(err)). err is never nil.
func Cause(err error) string {
	return Quote(Chain(err))
}

// Chain is err's text followed by each wrapped cause that text leaves out.
// An error written for a client, such as a pull request problem, carries
// the operator's cause only in its chain. It is the whole cause, which a log
// line holds only through Quote. err is never nil.
func Chain(err error) string {
	text := err.Error()
	for cause := errors.Unwrap(err); cause != nil; cause = errors.Unwrap(cause) {
		if detail := cause.Error(); !strings.Contains(text, detail) {
			text += ": " + detail
		}
	}
	return text
}

// Quote is a cause's text for a log line, quoted, so text such as Git's
// error output cannot start a line of its own.
//
// A text longer than maximumCause keeps half of that from its beginning,
// which names what failed, and half from its end, which holds the deepest
// cause, such as Git's last message, and is marked with its full length:
// "beginning"...(cut, N bytes)..."end". Each part has at most
// maximumCause/2+utf8.UTFMax-1 bytes, and quoting at most quadruples a byte
// (an invalid byte becomes \xHH), so a cause on a line has at most
// 4*(maximumCause+2*(utf8.UTFMax-1))+4 bytes plus the marker: 16,412 plus
// about 30.
func Quote(text string) string {
	if len(text) <= maximumCause {
		return strconv.Quote(text)
	}
	head, tail := text[:runeStart(text, maximumCause/2)], text[runeStart(text, len(text)-maximumCause/2):]
	return fmt.Sprintf("%s...(cut, %d bytes)...%s", strconv.Quote(head), len(text), strconv.Quote(tail))
}

// runeStart moves index back to the start of the character it falls in, so
// a cut never splits a valid character into bytes that quoting would escape.
// A character has at most utf8.UTFMax bytes, so it moves back at most
// utf8.UTFMax-1; in a run of invalid bytes, which quoting escapes one by one
// anyway, it stops there instead of walking back through the run.
func runeStart(text string, index int) int {
	for back := 0; back < utf8.UTFMax-1 && index > 0 && !utf8.RuneStart(text[index]); back++ {
		index--
	}
	return index
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
