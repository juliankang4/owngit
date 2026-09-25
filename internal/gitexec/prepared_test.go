package gitexec

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

const (
	preparedHelperModeEnvironment = "OWNGIT_PREPARED_HELPER_MODE"
	// preparedHelperDescendantEnvironment names the test's TCP address that a
	// descendant of a hanging helper connects to and keeps open while it lives.
	preparedHelperDescendantEnvironment = "OWNGIT_PREPARED_HELPER_DESCENDANT"
	// preparedHelperFinalEnvironment names a file where a hanging helper
	// writes the final command it received, once it is about to hang.
	preparedHelperFinalEnvironment = "OWNGIT_PREPARED_HELPER_FINAL"
)

func TestMain(m *testing.M) {
	if mode := os.Getenv(preparedHelperModeEnvironment); mode != "" {
		os.Exit(runPreparedHelper(mode))
	}
	os.Exit(m.Run())
}

func runPreparedHelper(mode string) int {
	if mode == "descendant" {
		// Hold a connection to the test until the process ends or the test
		// closes its side, so the test sees the moment this process is gone.
		connection, err := net.Dial("tcp", os.Getenv(preparedHelperDescendantEnvironment))
		if err != nil {
			return 3
		}
		if _, err := os.Stdout.WriteString("ready\n"); err != nil {
			return 3
		}
		_, _ = connection.Read(make([]byte, 1))
		return 0
	}
	reader := bufio.NewScanner(os.Stdin)
	writer := bufio.NewWriter(os.Stdout)
	acknowledge := func(action string) bool {
		if _, err := fmt.Fprintf(writer, "%s: ok\n", action); err != nil {
			return false
		}
		return writer.Flush() == nil
	}
	for reader.Scan() {
		line := reader.Text()
		switch line {
		case "start":
			switch mode {
			case "bad-start":
				_, _ = writer.WriteString("start: not-ok\n")
				_ = writer.Flush()
				return 0
			case "oversized-ack":
				_, _ = writer.WriteString(strings.Repeat("x", 5000) + "\n")
				_ = writer.Flush()
				return 0
			}
			if !acknowledge("start") {
				return 2
			}
		case "prepare":
			if !acknowledge("prepare") {
				return 2
			}
		case "commit", "abort":
			if mode == "lost-final" {
				return 0
			}
			if mode == "hang-final" || mode == "hang-descendant" {
				if mode == "hang-descendant" {
					child := exec.Command(os.Args[0])
					child.Env = os.Environ()
					for index, entry := range child.Env {
						if strings.HasPrefix(entry, preparedHelperModeEnvironment+"=") {
							child.Env[index] = preparedHelperModeEnvironment + "=descendant"
						}
					}
					ready, err := child.StdoutPipe()
					if err != nil {
						return 4
					}
					if err := child.Start(); err != nil {
						return 4
					}
					// The final command is reported only once the descendant
					// holds its connection.
					if line, err := bufio.NewReader(ready).ReadString('\n'); err != nil || line != "ready\n" {
						return 4
					}
				}
				if final := os.Getenv(preparedHelperFinalEnvironment); final != "" {
					if err := os.WriteFile(final, []byte(line), 0o600); err != nil {
						return 3
					}
				}
				for {
					time.Sleep(time.Hour)
				}
			}
			if mode == "stderr-overflow" {
				_, _ = os.Stderr.WriteString(strings.Repeat("e", 4096))
			}
			if !acknowledge(line) {
				return 2
			}
			if mode == "trailing-output" {
				_, _ = writer.WriteString("unexpected\n")
				_ = writer.Flush()
			}
			return 0
		}
	}
	return 0
}

func newPreparedHelperRunner(t *testing.T) *Runner {
	t.Helper()
	runner, err := New(os.Args[0], filepath.Join(t.TempDir(), "runtime"))
	noErr(t, err)
	runner.TerminationGrace = 50 * time.Millisecond
	return runner
}

func runPreparedHelperMode(t *testing.T, mode string, outputLimit int64, callback func(context.Context) error) error {
	t.Helper()
	runner := newPreparedHelperRunner(t)
	_, err := runner.RunPreparedUpdateContext(context.Background(), t.TempDir(), []string{"verify refs/heads/main 0000000000000000000000000000000000000000"}, CommandLimits{
		Timeout:     5 * time.Second,
		OutputLimit: outputLimit,
		Environment: []string{preparedHelperModeEnvironment + "=" + mode},
	}, callback)
	return err
}

func TestPreparedUpdateRejectsBadAndLostAcknowledgements(t *testing.T) {
	for _, test := range []struct {
		mode string
		want string
	}{
		{mode: "bad-start", want: "unexpected start acknowledgement"},
		{mode: "lost-final", want: "read commit acknowledgement"},
		{mode: "trailing-output", want: "unexpected output after commit acknowledgement"},
	} {
		t.Run(test.mode, func(t *testing.T) {
			err := runPreparedHelperMode(t, test.mode, 8192, func(context.Context) error { return nil })
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("mode=%s err=%v", test.mode, err)
			}
		})
	}
}

func TestPreparedUpdateBoundsProtocolAndStderr(t *testing.T) {
	for _, test := range []struct {
		mode   string
		stream string
		limit  int64
	}{
		{mode: "oversized-ack", stream: "stdout acknowledgement", limit: 8192},
		{mode: "stderr-overflow", stream: "stderr", limit: 128},
	} {
		t.Run(test.mode, func(t *testing.T) {
			err := runPreparedHelperMode(t, test.mode, test.limit, func(context.Context) error { return nil })
			var limitErr *LimitError
			if !errors.As(err, &limitErr) || limitErr.Stream != test.stream {
				t.Fatalf("mode=%s limit=%+v err=%v", test.mode, limitErr, err)
			}
		})
	}
}

func TestPreparedUpdateExplicitAbortReturnsCallbackError(t *testing.T) {
	want := errors.New("reject prepared transaction")
	t.Run("normal", func(t *testing.T) {
		err := runPreparedHelperMode(t, "normal", 8192, func(context.Context) error { return want })
		if !errors.Is(err, want) {
			t.Fatalf("callback rejection err=%v", err)
		}
	})
	t.Run("protocol failure", func(t *testing.T) {
		err := runPreparedHelperMode(t, "lost-final", 8192, func(context.Context) error { return want })
		if !errors.Is(err, want) || !strings.Contains(err.Error(), "read abort acknowledgement") {
			t.Fatalf("callback and protocol rejection err=%v", err)
		}
	})
	t.Run("cleanup timeout", func(t *testing.T) {
		// The deadline passes only once the helper has received the abort and
		// hangs, so the run times out during cleanup however long the helper
		// takes to start. The command timeout only bounds a broken run.
		runner := newPreparedHelperRunner(t)
		ctx, final := deadlineAtFinalCommand(t, "abort")
		_, err := runner.RunPreparedUpdateContext(ctx, t.TempDir(), []string{"verify refs/heads/main 0000000000000000000000000000000000000000"}, CommandLimits{
			Timeout: time.Minute, Environment: []string{preparedHelperModeEnvironment + "=hang-final", final},
		}, func(context.Context) error { return want })
		if !errors.Is(err, want) || !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("callback and cleanup rejection err=%v", err)
		}
	})
}

// manualDeadline is a context whose deadline passes when the test calls
// expire, so a test can place a timeout at a known protocol step.
type manualDeadline struct {
	context.Context
	done    chan struct{}
	once    sync.Once
	expired time.Time
}

func newManualDeadline() *manualDeadline {
	return &manualDeadline{Context: context.Background(), done: make(chan struct{})}
}

func (d *manualDeadline) Done() <-chan struct{} { return d.done }

func (d *manualDeadline) Err() error {
	select {
	case <-d.done:
		return context.DeadlineExceeded
	default:
		return nil
	}
}

func (d *manualDeadline) expire() {
	d.once.Do(func() {
		d.expired = time.Now()
		close(d.done)
	})
}

// expiredAt reports when the deadline passed, or false if it has not, for
// example because the run ended with another error first. It never waits.
func (d *manualDeadline) expiredAt() (time.Time, bool) {
	select {
	case <-d.done:
		return d.expired, true
	default:
		return time.Time{}, false
	}
}

// deadlineAtFinalCommand returns a deadline that passes once a hanging helper
// reports that it received the final command want, and the environment entry
// that tells the helper where to report it. The timeout then falls in the
// step after that command however long the helper took to start. The test
// should also set a long command timeout, which only bounds a broken run.
func deadlineAtFinalCommand(t *testing.T, want string) (*manualDeadline, string) {
	t.Helper()
	ctx := newManualDeadline()
	t.Cleanup(ctx.expire)
	path := filepath.Join(t.TempDir(), "final-command")
	go func() {
		for {
			if data, err := os.ReadFile(path); err == nil && string(data) == want {
				ctx.expire()
				return
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(5 * time.Millisecond):
			}
		}
	}()
	return ctx, preparedHelperFinalEnvironment + "=" + path
}

func TestPreparedUpdateCancellationBoundsNonCooperativeCallback(t *testing.T) {
	// The deadline passes when the callback is entered, after the helper has
	// started and prepared however loaded the machine is, and the bound is
	// measured from that moment. The command timeout only bounds a broken run.
	runner := newPreparedHelperRunner(t)
	ctx := newManualDeadline()
	defer ctx.expire()
	entered := make(chan struct{})
	release := make(chan struct{})
	var expired time.Time
	_, err := runner.RunPreparedUpdateContext(ctx, t.TempDir(), []string{"verify refs/heads/main 0000000000000000000000000000000000000000"}, CommandLimits{
		Timeout: time.Minute, Environment: []string{preparedHelperModeEnvironment + "=normal"},
	}, func(context.Context) error {
		expired = time.Now()
		close(entered)
		ctx.expire()
		<-release
		return nil
	})
	returned := time.Now()
	close(release)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("non-cooperative callback err=%v", err)
	}
	select {
	case <-entered:
	default:
		t.Fatal("prepared callback was not entered")
	}
	if elapsed := returned.Sub(expired); elapsed > 500*time.Millisecond {
		t.Fatalf("non-cooperative callback exceeded bounded cancellation: %s", elapsed)
	}
}

func TestPreparedUpdateTerminationReapsDescendant(t *testing.T) {
	// The deadline passes once the helper's descendant holds its connection
	// and the helper hangs after the final command, so the descendant always
	// exists when cleanup starts. The descendant never exits on its own while
	// the test holds the connection, so the connection closes only when
	// cleanup ends the descendant, however long cleanup takes.
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	noErr(t, err)
	defer listener.Close()
	accepted := make(chan net.Conn, 1)
	go func() {
		if connection, err := listener.Accept(); err == nil {
			accepted <- connection
		}
		close(accepted)
	}()
	runner := newPreparedHelperRunner(t)
	ctx, final := deadlineAtFinalCommand(t, "commit")
	_, err = runner.RunPreparedUpdateContext(ctx, t.TempDir(), []string{"verify refs/heads/main 0000000000000000000000000000000000000000"}, CommandLimits{
		Timeout: time.Minute,
		Environment: []string{
			preparedHelperModeEnvironment + "=hang-descendant",
			preparedHelperDescendantEnvironment + "=" + listener.Addr().String(),
			final,
		},
	}, func(context.Context) error { return nil })
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("descendant cleanup err=%v", err)
	}
	if _, ok := ctx.expiredAt(); !ok {
		t.Fatal("the run ended before the helper reached its final command")
	}
	// The helper reported the final command only after the descendant had
	// connected, so Accept has a connection to return.
	connection, ok := <-accepted
	if !ok {
		t.Fatal("the descendant never connected")
	}
	// Closing the test's side also ends a descendant that survived.
	defer connection.Close()
	noErr(t, connection.SetReadDeadline(time.Now().Add(30*time.Second)))
	if _, err := connection.Read(make([]byte, 1)); err == nil || errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatalf("prepared descendant survived process-group cleanup: %v", err)
	}
}

// The deadline passes once the helper hangs after the final command, and the
// bound is measured from that moment, not from the helper start.
func TestPreparedUpdateTerminationWaitIsBounded(t *testing.T) {
	runner := newPreparedHelperRunner(t)
	ctx, final := deadlineAtFinalCommand(t, "commit")
	_, err := runner.RunPreparedUpdateContext(ctx, t.TempDir(), []string{"verify refs/heads/main 0000000000000000000000000000000000000000"}, CommandLimits{
		Timeout: time.Minute, Environment: []string{preparedHelperModeEnvironment + "=hang-final", final},
	}, func(context.Context) error { return nil })
	returned := time.Now()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("hung final acknowledgement err=%v", err)
	}
	expired, ok := ctx.expiredAt()
	if !ok {
		t.Fatal("the run ended before the helper reached its final command")
	}
	if elapsed := returned.Sub(expired); elapsed > time.Second {
		t.Fatalf("termination and reap exceeded bound: %s", elapsed)
	}
}

// The runner joins a callback that returns inside the termination grace and
// reports one that is still running, so a caller can complete its own barrier.
// Each callback expires the deadline when it is entered, after the helper has
// started and prepared, so machine load cannot let the deadline pass before
// the callback is admitted. The command timeout only bounds a broken run.
func TestPreparedUpdateJoinsOrReportsCallbackAfterDeadline(t *testing.T) {
	t.Run("joined", func(t *testing.T) {
		// The callback returns 100 ms after the deadline, well inside the
		// grace, so a runner that did not join it would return first.
		runner := newPreparedHelperRunner(t)
		runner.TerminationGrace = 2 * time.Second
		ctx := newManualDeadline()
		defer ctx.expire()
		finished := make(chan struct{})
		_, err := runner.RunPreparedUpdateContext(ctx, t.TempDir(), []string{"verify refs/heads/main 0000000000000000000000000000000000000000"}, CommandLimits{
			Timeout: time.Minute, Environment: []string{preparedHelperModeEnvironment + "=normal"},
		}, func(callbackCtx context.Context) error {
			defer close(finished)
			ctx.expire()
			<-callbackCtx.Done()
			time.Sleep(100 * time.Millisecond)
			return nil
		})
		if !errors.Is(err, context.DeadlineExceeded) || errors.Is(err, ErrPreparedCallbackDetached) {
			t.Fatalf("cooperative callback err=%v", err)
		}
		select {
		case <-finished:
		default:
			t.Fatal("runner returned before the cooperative callback finished")
		}
	})
	t.Run("detached", func(t *testing.T) {
		runner := newPreparedHelperRunner(t)
		ctx := newManualDeadline()
		defer ctx.expire()
		release := make(chan struct{})
		defer close(release)
		_, err := runner.RunPreparedUpdateContext(ctx, t.TempDir(), []string{"verify refs/heads/main 0000000000000000000000000000000000000000"}, CommandLimits{
			Timeout: time.Minute, Environment: []string{preparedHelperModeEnvironment + "=normal"},
		}, func(context.Context) error {
			ctx.expire()
			<-release
			return nil
		})
		if !errors.Is(err, context.DeadlineExceeded) || !errors.Is(err, ErrPreparedCallbackDetached) {
			t.Fatalf("non-cooperative callback err=%v", err)
		}
	})
}
