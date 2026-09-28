package testfixture

import (
	"os"
	"strings"
)

// SkipRaceExitWaitInChildren makes the processes this test binary starts
// from now on exit without the race detector's wait. Call it from TestMain
// of a package whose tests run the test binary itself as a child process.
//
// Built with -race, a process waits one second before it exits (GORACE
// atexit_sleep_ms), so that goroutines still running can report a race. A
// child that runs one command, one render or one open for a test has
// finished its work by then, so the wait only adds a second to each child.
// The race detector still checks everything a child runs, and a race it
// finds still fails the child. Other GORACE options are kept. The calling
// process read GORACE when it started, so only its children see the change.
// Programs built without -race ignore GORACE.
func SkipRaceExitWaitInChildren() error {
	return os.Setenv("GORACE", strings.TrimSpace(os.Getenv("GORACE")+" atexit_sleep_ms=0"))
}
