package importsync

import (
	"os"
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

// require stops the test at the caller unless ok.
func require(t testing.TB, ok bool, format string, args ...any) {
	t.Helper()
	if !ok {
		t.Fatalf(format, args...)
	}
}

// eq stops the test at the caller unless got equals want.
func eq[T comparable](t testing.TB, what string, got, want T) {
	t.Helper()
	if got != want {
		t.Fatalf("%s: got %v want %v", what, got, want)
	}
}

// fileIs checks that path holds exactly want.
func fileIs(t testing.TB, path, want string) {
	t.Helper()
	content, err := os.ReadFile(path)
	require(t, err == nil && string(content) == want, "%s: content=%q err=%v want %q", path, content, err, want)
}

// absent checks that nothing exists at path.
func absent(t testing.TB, path string) {
	t.Helper()
	_, err := os.Lstat(path)
	require(t, os.IsNotExist(err), "%s exists or is unreadable: %v", path, err)
}

// isSymlink checks that path is still a symbolic link.
func isSymlink(t testing.TB, path string) {
	t.Helper()
	info, err := os.Lstat(path)
	require(t, err == nil && info.Mode()&os.ModeSymlink != 0, "%s is not a symlink: %v err=%v", path, info, err)
}
