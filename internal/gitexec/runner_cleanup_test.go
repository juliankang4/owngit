package gitexec

import (
	"context"
	"errors"
	"io"
	"os/exec"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// cleanupFaults runs the real termination and owner release, records their
// results, and then adds the injected failures to every command after the
// first skip. Tests read the fields after the runner call returns.
type cleanupFaults struct {
	skip                   int
	terminations, closes   int
	terminateErr, closeErr error
}

func injectCleanupFaults(runner *Runner, terminateErr, closeErr error) *cleanupFaults {
	faults := &cleanupFaults{}
	if runner.processSeam == nil {
		runner.processSeam = &processCleanupSeam{}
	}
	// Each command releases its owner once, after any termination, so closes
	// counts the commands that finished before this one.
	runner.processSeam.terminateFunc = func(owner *ProcessOwner, grace time.Duration) error {
		faults.terminations++
		err := TerminateOwnedProcess(owner, grace)
		faults.terminateErr = errors.Join(faults.terminateErr, err)
		if faults.closes < faults.skip {
			return err
		}
		return errors.Join(err, terminateErr)
	}
	runner.processSeam.closeFunc = func(owner *ProcessOwner) error {
		faults.closes++
		err := CloseOwnedProcess(owner)
		faults.closeErr = errors.Join(faults.closeErr, err)
		if faults.closes <= faults.skip {
			return err
		}
		return errors.Join(err, closeErr)
	}
	return faults
}

type failingStderr struct{ err error }

func (writer failingStderr) Write([]byte) (int, error) { return 0, writer.err }

// A failed termination or owner release stays a failure with its causes. It is
// never an output limit or a status a caller could read as Git's answer, and
// the output copied before it stays available.
func TestRunReportsOwnedProcessCleanupFailures(t *testing.T) {
	writeErr := errors.New("stderr destination failed")
	terminateErr := errors.New("injected termination failure")
	closeErr := errors.New("injected owner release failure")
	hold := "hold-stdout"
	cases := []struct {
		name, mode             string
		cancel                 bool
		limits                 CommandLimits
		terminateErr, closeErr error
		want                   []error
		cleanupFailed, limited bool
		exit                   int // -1: no status
		stdout                 string
		terminations           int
	}{
		{name: "clean success", mode: "success", exit: -1, stdout: "stdout-payload"},
		{name: "exit error", mode: "exit", exit: 94},
		{name: "stderr write failure", mode: "stderr-limit", limits: CommandLimits{Stderr: failingStderr{writeErr}}, want: []error{context.Canceled, writeErr}, exit: -1, stdout: "*", terminations: 1},
		{name: "stderr short write", mode: "stderr-limit", limits: CommandLimits{Stderr: failingStderr{}}, want: []error{context.Canceled, io.ErrShortWrite}, exit: -1, stdout: "*", terminations: 1},
		{name: "caller cancellation", mode: hold, cancel: true, want: []error{context.Canceled}, exit: -1, stdout: "ready\n", terminations: 1},
		{name: "deadline", mode: hold, limits: CommandLimits{Timeout: 300 * time.Millisecond}, want: []error{context.DeadlineExceeded}, exit: -1, stdout: "*", terminations: 1},
		{name: "termination failure", mode: hold, cancel: true, terminateErr: terminateErr, want: []error{context.Canceled, terminateErr}, cleanupFailed: true, exit: -1, stdout: "ready\n", terminations: 1},
		{name: "close failure after success", mode: "success", closeErr: closeErr, want: []error{closeErr}, cleanupFailed: true, exit: -1, stdout: "stdout-payload"},
		{name: "close failure after exit error", mode: "exit", closeErr: closeErr, want: []error{closeErr}, cleanupFailed: true, exit: -1},
		{name: "both cleanup failures", mode: hold, cancel: true, terminateErr: terminateErr, closeErr: closeErr, want: []error{context.Canceled, terminateErr, closeErr}, cleanupFailed: true, exit: -1, stdout: "ready\n", terminations: 1},
		{name: "output limit stop", mode: hold, limits: CommandLimits{OutputLimit: 3, StopAtOutputLimit: true}, limited: true, exit: -1, stdout: "rea", terminations: 1},
		{name: "output limit stop with cleanup failure", mode: hold, limits: CommandLimits{OutputLimit: 3, StopAtOutputLimit: true}, terminateErr: terminateErr, want: []error{context.Canceled, terminateErr}, cleanupFailed: true, exit: -1, stdout: "rea", terminations: 1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			runner := streamTestRunner(t, t.TempDir())
			faults := injectCleanupFaults(runner, c.terminateErr, c.closeErr)
			tap, ready := newCopyTap("ready\n")
			runner.stdoutCopyTap = tap
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if c.cancel {
				go func() {
					if waitForCopiedOutput(ready) == nil {
						cancel()
					}
				}()
			}
			limits := c.limits
			if limits.Timeout == 0 {
				limits.Timeout = 10 * time.Second
			}
			limits.Environment = []string{streamFixtureEnv + "=" + c.mode}
			result, err := runner.RunWithLimits(ctx, t.TempDir(), nil, limits)

			for _, want := range c.want {
				if !errors.Is(err, want) {
					t.Errorf("error %v does not match %v", err, want)
				}
			}
			if errors.Is(err, ErrProcessCleanup) != c.cleanupFailed {
				t.Errorf("cleanup failure reported=%v, want %v: %v", !c.cleanupFailed, c.cleanupFailed, err)
			}
			var limitErr *LimitError
			if errors.As(err, &limitErr) != c.limited {
				t.Errorf("limit reported=%v, want %v: %v", !c.limited, c.limited, err)
			}
			if code, ok := ExitCode(err); c.exit >= 0 && (!ok || code != c.exit) || c.exit < 0 && ok {
				t.Errorf("ExitCode=%d,%v, want %d: %v", code, ok, c.exit, err)
			}
			if len(c.want) == 0 && !c.limited && c.exit < 0 && err != nil {
				t.Errorf("unexpected error: %v", err)
			}
			if c.stdout != "*" && string(result.Stdout) != c.stdout {
				t.Errorf("stdout=%q, want %q", result.Stdout, c.stdout)
			}
			if faults.terminations != c.terminations || faults.closes != 1 {
				t.Errorf("terminations=%d closes=%d, want %d and 1", faults.terminations, faults.closes, c.terminations)
			}
			if faults.terminateErr != nil || faults.closeErr != nil {
				t.Errorf("real cleanup failed: terminate=%v close=%v", faults.terminateErr, faults.closeErr)
			}
		})
	}
}

// joinProbe records caller input reads and output writes that arrive after
// the call returned.
type joinProbe struct {
	returned, late atomic.Bool
	input          io.Reader
	ready          chan struct{}
	readyOnce      atomic.Bool
}

func (p *joinProbe) Read(b []byte) (int, error) {
	p.late.CompareAndSwap(false, p.returned.Load())
	return p.input.Read(b)
}

func (p *joinProbe) Write(b []byte) (int, error) {
	p.late.CompareAndSwap(false, p.returned.Load())
	if p.readyOnce.CompareAndSwap(false, true) {
		close(p.ready)
	}
	return len(b), nil
}

// A cleanup failure is reported only after Wait and the stdin copy ended, so
// nothing still reads the caller's input or writes its output.
func TestRunOwnedProcessJoinsBeforeReportingCleanupFailure(t *testing.T) {
	runner := streamTestRunner(t, t.TempDir())
	terminateErr := errors.New("injected termination failure")
	closeErr := errors.New("injected owner release failure")
	injectCleanupFaults(runner, terminateErr, closeErr)
	probe := &joinProbe{input: strings.NewReader("input"), ready: make(chan struct{})}
	cmd := exec.Command(runner.GitPath)
	cmd.Dir = t.TempDir()
	cmd.Env = runner.Environment(streamFixtureEnv + "=hold-stdout")
	cmd.Stdout = probe
	stdinPipe, err := cmd.StdinPipe()
	noErr(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		select {
		case <-probe.ready:
		case <-time.After(10 * time.Second):
		}
		cancel()
	}()
	waited, err := runOwnedProcess(ctx, cmd, runner.TerminationGrace, runner.processSeam, probe, stdinPipe, false)
	probe.returned.Store(true)
	if !waited || cmd.ProcessState == nil {
		t.Fatalf("returned before Wait: waited=%v state=%v", waited, cmd.ProcessState)
	}
	for _, want := range []error{context.Canceled, ErrProcessCleanup, terminateErr, closeErr} {
		if !errors.Is(err, want) {
			t.Fatalf("error %v does not match %v", err, want)
		}
	}
	time.Sleep(100 * time.Millisecond)
	if probe.late.Load() {
		t.Fatal("caller input or output was used after the call returned")
	}
}
