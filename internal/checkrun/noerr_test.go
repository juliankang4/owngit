package checkrun

import "testing"

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
