package gitexec

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// A protocol goroutine delayed before callback admission past the deadline and
// cleanup grace must not make the runner report a detached callback. The gate
// closes first, the delayed admission is refused, and no callback starts after
// the return, so the caller sees only the context and lifecycle errors.
// The deadline passes when the protocol reaches the admission check, after the
// helper has started and prepared, so machine load cannot move it earlier. The
// command timeout only bounds a broken run.
func TestPreparedUnstartedCallbackCannotBeReportedDetached(t *testing.T) {
	runner := newPreparedHelperRunner(t)
	runner.TerminationGrace = 40 * time.Millisecond
	ctx := newManualDeadline()
	defer ctx.expire()
	entered := make(chan struct{})
	release := make(chan struct{})
	protocolExited := make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(unblock)
	seam := &preparedProtocolSeam{
		beforeAdmission: func() {
			close(entered)
			ctx.expire()
			<-release
		},
		afterProtocol: func() { close(protocolExited) },
	}
	var calls atomic.Int32
	result := make(chan error, 1)
	go func() {
		_, err := runner.runPreparedUpdateContext(ctx, t.TempDir(),
			[]string{"verify refs/heads/main 0000000000000000000000000000000000000000"},
			CommandLimits{Timeout: time.Minute, Environment: []string{preparedHelperModeEnvironment + "=normal"}},
			func(context.Context) error { calls.Add(1); return nil }, seam)
		result <- err
	}()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("protocol did not reach the pre-admission check")
	}
	var err error
	select {
	case err = <-result:
	case <-time.After(3 * time.Second):
		t.Fatal("runner did not return within its bounded cleanup window")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("deadline cause missing: %v", err)
	}
	if errors.Is(err, ErrPreparedCallbackDetached) {
		t.Fatalf("never-admitted callback was reported detached: %v", err)
	}
	unblock()
	select {
	case <-protocolExited:
	case <-time.After(3 * time.Second):
		t.Fatal("protocol did not finish after finite recovery")
	}
	if calls.Load() != 0 {
		t.Fatalf("callback started after the return: %d", calls.Load())
	}
}

// The opposite order: an admitted callback that outlives the join grace is
// genuinely detached, so a caller can hold its guards until the callback
// finishes. Closing the gate must not over-cancel a running callback. The
// callback expires the deadline when it is entered, after the helper has
// started and prepared, and the bound is measured from that moment. The
// command timeout only bounds a broken run.
func TestPreparedAdmittedCallbackIsStillReportedDetached(t *testing.T) {
	runner := newPreparedHelperRunner(t)
	runner.TerminationGrace = 40 * time.Millisecond
	ctx := newManualDeadline()
	defer ctx.expire()
	entered := make(chan struct{})
	finished := make(chan struct{})
	release := make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(unblock)
	var expired time.Time
	_, err := runner.RunPreparedUpdateContext(ctx, t.TempDir(),
		[]string{"verify refs/heads/main 0000000000000000000000000000000000000000"},
		CommandLimits{Timeout: time.Minute, Environment: []string{preparedHelperModeEnvironment + "=normal"}},
		func(context.Context) error {
			expired = time.Now()
			close(entered)
			ctx.expire()
			<-release
			close(finished)
			return nil
		})
	returned := time.Now()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("deadline cause missing: %v", err)
	}
	if !errors.Is(err, ErrPreparedCallbackDetached) {
		t.Fatalf("admitted unfinished callback was not reported detached: %v", err)
	}
	select {
	case <-entered:
	default:
		t.Fatal("callback was not admitted before the return")
	}
	if elapsed := returned.Sub(expired); elapsed > 3*time.Second {
		t.Fatalf("detached return exceeded its bound: %s", elapsed)
	}
	select {
	case <-finished:
		t.Fatal("callback finished before the detached return")
	default:
	}
	unblock()
	select {
	case <-finished:
	case <-time.After(3 * time.Second):
		t.Fatal("callback did not finish after release")
	}
}

// The gate itself: a started callback is reported and releases its join only
// when it finishes, while a gate closed first refuses a later admission.
func TestPreparedAdmissionSerializesStartAndClose(t *testing.T) {
	startedGate := newPreparedAdmission()
	if !startedGate.admit() {
		t.Fatal("pending admission was refused")
	}
	startedGate.finish()
	admitted, done := startedGate.close()
	if !admitted {
		t.Fatal("admitted callback was not reported")
	}
	select {
	case <-done:
	default:
		t.Fatal("finished admission did not release the join channel")
	}
	if startedGate.admit() {
		t.Fatal("admission succeeded twice")
	}

	closedGate := newPreparedAdmission()
	if admitted, _ := closedGate.close(); admitted {
		t.Fatal("closed gate reported an admitted callback")
	}
	if closedGate.admit() {
		t.Fatal("admission after close was accepted")
	}
}
