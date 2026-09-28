package server

import (
	"crypto/sha256"
	"log"
	"sync"
	"time"

	"owngit/internal/logtext"
)

const (
	// repeatWindow is how long a failure that was just logged is counted
	// instead of logged again.
	repeatWindow = time.Minute
	// maximumCountedFailures bounds the failures counted at once. Each holds
	// its step, a digest of its cause and the cause as a log line shows it,
	// which logtext bounds to about 16 KiB, so the counts stay below 5 MiB.
	maximumCountedFailures = 256
)

// failures is the server log's record of failures being counted. The
// standard logger is one per process, so this is too.
var failures = newFailureLog(repeatWindow)

// failureLog writes failure lines to the server log so that one failure met
// by many requests, as when OwnGit state cannot be read, does not fill the
// log. A failure is its step and its whole cause, never the request's
// method or path, which the client chooses. The cause is known by a digest
// of its whole text, so two causes that a log line would show alike, being
// cut to the same beginning and end, stay two failures. Its first line is logged at once with the
// request that met it. The same failure within the window after that line
// is counted, and the count is logged when the window ends. A failure that
// differs in step or cause is logged at once. When maximumCountedFailures
// are being counted, a new failure is logged but not counted, so each of
// its repeats is logged too; nothing is left out.
type failureLog struct {
	mu      sync.Mutex
	window  time.Duration
	counted map[failure]*repeats
}

type failure struct {
	step  string
	cause [sha256.Size]byte
}

// repeats counts the lines a failure's window left out. shown is its cause
// as its line shows it.
type repeats struct {
	shown string
	count int
	timer *time.Timer
}

func newFailureLog(window time.Duration) *failureLog {
	return &failureLog{window: window, counted: map[failure]*repeats{}}
}

// write logs that step of request could not be completed because of cause,
// or counts it while the same failure is in its window. request is already
// bounded for a log line; cause is the whole cause (see logtext.Chain).
func (failures *failureLog) write(request, step, cause string) {
	key := failure{step, sha256.Sum256([]byte(cause))}
	failures.mu.Lock()
	defer failures.mu.Unlock()
	if repeated, counting := failures.counted[key]; counting {
		repeated.count++
		return
	}
	shown := logtext.Quote(cause)
	log.Printf("%s: %s could not be completed: %s", request, step, shown)
	if len(failures.counted) < maximumCountedFailures {
		repeated := &repeats{shown: shown}
		repeated.timer = time.AfterFunc(failures.window, func() { failures.end(key, repeated) })
		failures.counted[key] = repeated
	}
}

// end closes the window of key and logs how many lines it left out. A
// window that flush already closed logs nothing.
func (failures *failureLog) end(key failure, repeated *repeats) {
	failures.mu.Lock()
	defer failures.mu.Unlock()
	if failures.counted[key] != repeated {
		return
	}
	delete(failures.counted, key)
	failures.report(key, repeated)
}

// flush closes every window and logs the lines each left out, as when the
// server stops before the windows end.
func (failures *failureLog) flush() {
	failures.mu.Lock()
	defer failures.mu.Unlock()
	for key, repeated := range failures.counted {
		repeated.timer.Stop()
		delete(failures.counted, key)
		failures.report(key, repeated)
	}
}

func (failures *failureLog) report(key failure, repeated *repeats) {
	if repeated.count > 0 {
		log.Printf("%s could not be completed %d more times in the %s after its last line: %s", key.step, repeated.count, failures.window, repeated.shown)
	}
}
