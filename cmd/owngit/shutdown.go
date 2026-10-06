package main

import (
	"context"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"owngit/internal/gitexec"
)

const (
	// shutdownDeadline is the whole time a stop may take, counted from the
	// stop signal. Every wait during the stop ends at this one deadline, so
	// the stop fits what the service managers allow (service.StopTimeout,
	// and launchd's own 60 s cap). Work still running at the deadline ends the process
	// (giveUp), and the next start reconciles it.
	shutdownDeadline = 45 * time.Second
	// gitCleanupReserve is kept at the end of the deadline for ending the
	// Git processes of requests that were cut off.
	gitCleanupReserve = 5 * time.Second
)

// shutdownClock holds the deadline of one stop. It starts when the serving
// context ends, or at the first wait when serve fails and returns instead.
type shutdownClock struct {
	once sync.Once
	// status is the exit status of giveUp.
	status atomic.Int32
	limit  time.Duration
	at     time.Time
}

func newShutdownClock(ctx context.Context, limit time.Duration) *shutdownClock {
	clock := &shutdownClock{limit: limit}
	context.AfterFunc(ctx, clock.start)
	return clock
}

func (clock *shutdownClock) start() {
	clock.once.Do(func() { clock.at = time.Now().Add(clock.limit) })
}

// deadline is the moment the stop must be over.
func (clock *shutdownClock) deadline() time.Time {
	clock.start()
	return clock.at
}

func (clock *shutdownClock) context() (context.Context, context.CancelFunc) {
	return context.WithDeadline(context.Background(), clock.deadline())
}

// exitProcess replaces os.Exit in tests; it is nil in production.
var exitProcess func(status int)

// ownedTerminationGrace bounds, per owned process group, the polite stop
// before the forced one when OwnGit gives up.
const ownedTerminationGrace = 500 * time.Millisecond

// giveUp is the teardown when the deadline passed with work still running.
// Storage is not released and the store is not closed under that work: the
// process exits at once with the status of a signal stop, the operating
// system releases its locks, and the next start records what was
// interrupted, as after a crash. The process groups and jobs OwnGit
// started (Git, checks, helpers) are ended first, so no work outlives the
// locks. The status is 0 after a stop signal, and 1 after a serving error.
func (clock *shutdownClock) giveUp(logf func(string, ...any), what string) {
	logf("%s: still running at the shutdown deadline; exiting now, and the next start records what was interrupted", what)
	if err := gitexec.TerminateAllOwnedProcesses(ownedTerminationGrace); err != nil {
		logf("could not end every process OwnGit started: %v", err)
	}
	status := int(clock.status.Load())
	if exitProcess != nil {
		exitProcess(status)
		return
	}
	os.Exit(status)
}

// stop runs one subsystem's stop within the deadline and logs what it could
// not finish.
func (clock *shutdownClock) stop(logf func(string, ...any), what string, stop func(context.Context) error) {
	ctx, cancel := clock.context()
	defer cancel()
	if err := stop(ctx); err != nil {
		logf("%s: %v", what, err)
		if ctx.Err() != nil {
			clock.giveUp(logf, what)
		}
	}
}

// wait waits for done, or until the deadline, and then gives up.
func (clock *shutdownClock) wait(logf func(string, ...any), what string, done <-chan struct{}) {
	ctx, cancel := clock.context()
	defer cancel()
	select {
	case <-done:
	case <-ctx.Done():
		clock.giveUp(logf, what)
	}
}
