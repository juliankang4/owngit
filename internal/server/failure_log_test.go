package server

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"owngit/internal/logtext"
)

// A failure is its step and cause. Repeats within its window are counted
// and reported with their count; a different step or cause is logged at
// once; the request that met a repeat is not part of what is counted.
func TestFailureLogCountsRepeatsOfOneFailure(t *testing.T) {
	serverLog := captureServerLog(t)
	failures := newFailureLog(time.Hour)
	failures.write("GET /", "settings read", "disk I/O error")
	failures.write("POST /login", "settings read", "disk I/O error")
	failures.write("GET /other", "settings read", "disk I/O error")
	failures.write("GET /", "settings read", "database is locked")
	failures.write("GET /", "session read", "disk I/O error")
	failures.flush()
	want := []string{
		`GET /: settings read could not be completed: "disk I/O error"`,
		`GET /: settings read could not be completed: "database is locked"`,
		`GET /: session read could not be completed: "disk I/O error"`,
		`settings read could not be completed 2 more times in the 1h0m0s after its last line: "disk I/O error"`,
	}
	lines := loggedFailures(serverLog, 0)
	if len(lines) != len(want) {
		t.Fatalf("logged %d lines, want %d:\n%s", len(lines), len(want), strings.Join(lines, "\n"))
	}
	for index, line := range lines {
		if !strings.HasSuffix(line, want[index]) {
			t.Errorf("line %d = %q, want it to end with %q", index, line, want[index])
		}
	}
	if logged := serverLog.String(); strings.Contains(logged, "/login") || strings.Contains(logged, "/other") {
		t.Fatalf("a counted request is named:\n%s", logged)
	}
}

// When its window ends the count is logged without another failure, and the
// next repeat after that is logged in full again.
func TestFailureWindowEndReportsTheCount(t *testing.T) {
	serverLog := captureServerLog(t)
	failures := newFailureLog(20 * time.Millisecond)
	failures.write("GET /", "settings read", "disk I/O error")
	failures.write("GET /", "settings read", "disk I/O error")
	deadline := time.Now().Add(5 * time.Second)
	for !strings.Contains(serverLog.String(), "1 more times") {
		if time.Now().After(deadline) {
			t.Fatalf("the count was not logged:\n%s", serverLog.String())
		}
		time.Sleep(5 * time.Millisecond)
	}
	failures.write("GET /again", "settings read", "disk I/O error")
	if !strings.Contains(serverLog.String(), `GET /again: settings read could not be completed: "disk I/O error"`) {
		t.Fatalf("the failure after its window was not logged:\n%s", serverLog.String())
	}
	failures.flush()
}

// Concurrent requests that meet one failure log it once and count the rest.
func TestFailureLogCountsConcurrentRepeats(t *testing.T) {
	serverLog := captureServerLog(t)
	failures := newFailureLog(time.Hour)
	var group sync.WaitGroup
	for index := range 50 {
		group.Go(func() { failures.write(fmt.Sprintf("GET /%d", index), "settings read", "disk I/O error") })
	}
	group.Wait()
	failures.flush()
	lines := loggedFailures(serverLog, 0)
	if len(lines) != 2 || !strings.Contains(lines[1], "49 more times") {
		t.Fatalf("logged:\n%s", strings.Join(lines, "\n"))
	}
}

// Past maximumCountedFailures a new failure is still logged, and so is each
// of its repeats, since nothing counts them.
func TestFailureLogBeyondItsBoundLogsEveryLine(t *testing.T) {
	serverLog := captureServerLog(t)
	failures := newFailureLog(time.Hour)
	for index := range maximumCountedFailures {
		failures.write("GET /", "read", fmt.Sprintf("cause %d", index))
	}
	failures.write("GET /", "read", "one more cause")
	failures.write("GET /", "read", "one more cause")
	if len(failures.counted) != maximumCountedFailures {
		t.Fatalf("%d failures counted", len(failures.counted))
	}
	if count := strings.Count(serverLog.String(), `read could not be completed: "one more cause"`); count != 2 {
		t.Fatalf("the uncounted failure was logged %d times", count)
	}
	failures.flush()
}

// Two causes that a log line shows alike, of one length and cut to the same
// beginning and end, are two failures: the second is logged at once.
func TestFailuresShownAlikeAreStillTwo(t *testing.T) {
	serverLog := captureServerLog(t)
	ends := strings.Repeat("e", 4096)
	first, second := errors.New(ends+strings.Repeat("a", 100)+ends), errors.New(ends+strings.Repeat("b", 100)+ends)
	if logtext.Cause(first) != logtext.Cause(second) {
		t.Fatal("the two causes are shown differently; the test needs them alike")
	}
	request := httptest.NewRequest(http.MethodGet, "/repositories/project", nil)
	logFailure(request, "repository read", first)
	logFailure(request, "repository read", second)
	if lines := loggedFailures(serverLog, 0); len(lines) != 2 || strings.Contains(strings.Join(lines, "\n"), "more times") {
		t.Fatalf("logged %d lines:\n%.300s", len(lines), strings.Join(lines, "\n"))
	}
}
