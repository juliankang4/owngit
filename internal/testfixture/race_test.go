package testfixture

import (
	"os"
	"testing"
)

func TestSkipRaceExitWaitInChildrenKeepsAnExplicitWait(t *testing.T) {
	for _, test := range []struct{ before, after string }{
		{"", "atexit_sleep_ms=0"},
		{"halt_on_error=1", "halt_on_error=1 atexit_sleep_ms=0"},
		{"atexit_sleep_ms=2000", "atexit_sleep_ms=2000"},
		{"halt_on_error=1 atexit_sleep_ms=0", "halt_on_error=1 atexit_sleep_ms=0"},
	} {
		t.Setenv("GORACE", test.before)
		if err := SkipRaceExitWaitInChildren(); err != nil {
			t.Fatal(err)
		}
		if got := os.Getenv("GORACE"); got != test.after {
			t.Errorf("GORACE %q became %q, want %q", test.before, got, test.after)
		}
	}
}
