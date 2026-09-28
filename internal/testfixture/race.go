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
// finds still fails the child. What is given up is a race in a goroutine
// that still runs after the child decided to exit.
//
// An atexit_sleep_ms that GORACE already sets is kept, so a developer can
// ask for the wait again, and a child that runs TestMain itself does not add
// the option twice. Other GORACE options are kept too. The calling process
// read GORACE when it started, so only its children see the change.
// Programs built without -race ignore GORACE.
func SkipRaceExitWaitInChildren() error {
	options := os.Getenv("GORACE")
	for _, option := range strings.Fields(options) {
		if strings.HasPrefix(option, "atexit_sleep_ms=") {
			return nil
		}
	}
	return os.Setenv("GORACE", strings.TrimSpace(options+" atexit_sleep_ms=0"))
}
