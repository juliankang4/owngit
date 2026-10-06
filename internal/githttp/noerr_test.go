package githttp

import (
	"strings"
	"testing"
)

// noErr stops the test at the caller when err is not nil. An optional
// context prefixes the error exactly as t.Fatalf("context: %v", err) would.
func noErr(t testing.TB, err error, context ...string) {
	t.Helper()
	if err == nil {
		return
	}
	if len(context) > 0 {
		t.Fatalf("%s: %v", context[0], err)
	}
	t.Fatal(err)
}

// quoteShell wraps one value in single quotes and escapes the quotes inside
// it, so a POSIX shell fixture receives the value as one argument. Its callers
// either build only on Unix or stop on Windows at run time, so this file
// carries no build tag and the package still builds there.
func quoteShell(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}
