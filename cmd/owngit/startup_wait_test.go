package main

import (
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

// A storage step that does not answer is reported while it waits and again
// when it continues; a fast step logs nothing.
func TestReportSlowStepNamesTheWait(t *testing.T) {
	var mu sync.Mutex
	var lines []string
	logf := func(format string, args ...any) {
		mu.Lock()
		defer mu.Unlock()
		lines = append(lines, fmt.Sprintf(format, args...))
	}
	if err := reportSlowStep(logf, time.Minute, "the folder /x to answer", func() error { return nil }); err != nil || len(lines) != 0 {
		t.Fatalf("fast step: err %v, log %q", err, lines)
	}
	release := make(chan struct{})
	waiting := make(chan struct{})
	logged := func(format string, args ...any) {
		logf(format, args...)
		if len(lines) == 1 {
			close(waiting)
		}
	}
	go func() { <-waiting; close(release) }()
	err := reportSlowStep(logged, 10*time.Millisecond, "the folder /x to answer", func() error { <-release; return nil })
	mu.Lock()
	defer mu.Unlock()
	if err != nil || len(lines) != 2 || !strings.Contains(lines[0], "still waiting for the folder /x") || !strings.Contains(lines[1], "continued") {
		t.Fatalf("slow step: err %v, log %q", err, lines)
	}
}
