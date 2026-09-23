package gitexec

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const (
	preparedHelperModeEnvironment   = "OWNGIT_PREPARED_HELPER_MODE"
	preparedHelperMarkerEnvironment = "OWNGIT_PREPARED_HELPER_MARKER"
)

func TestMain(m *testing.M) {
	if mode := os.Getenv(preparedHelperModeEnvironment); mode != "" {
		os.Exit(runPreparedHelper(mode))
	}
	os.Exit(m.Run())
}

func runPreparedHelper(mode string) int {
	if mode == "delayed-marker" {
		time.Sleep(300 * time.Millisecond)
		if err := os.WriteFile(os.Getenv(preparedHelperMarkerEnvironment), []byte("descendant survived"), 0o600); err != nil {
			return 3
		}
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
							child.Env[index] = preparedHelperModeEnvironment + "=delayed-marker"
						}
					}
					if err := child.Start(); err != nil {
						return 4
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
		runner := newPreparedHelperRunner(t)
		_, err := runner.RunPreparedUpdateContext(context.Background(), t.TempDir(), []string{"verify refs/heads/main 0000000000000000000000000000000000000000"}, CommandLimits{
			Timeout: 50 * time.Millisecond, Environment: []string{preparedHelperModeEnvironment + "=hang-final"},
		}, func(context.Context) error { return want })
		if !errors.Is(err, want) || !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("callback and cleanup rejection err=%v", err)
		}
	})
}

func TestPreparedUpdateCancellationBoundsNonCooperativeCallback(t *testing.T) {
	runner := newPreparedHelperRunner(t)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	entered := make(chan struct{})
	release := make(chan struct{})
	started := time.Now()
	_, err := runner.RunPreparedUpdateContext(ctx, t.TempDir(), []string{"verify refs/heads/main 0000000000000000000000000000000000000000"}, CommandLimits{
		Timeout: time.Second, Environment: []string{preparedHelperModeEnvironment + "=normal"},
	}, func(context.Context) error {
		close(entered)
		<-release
		return nil
	})
	close(release)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("non-cooperative callback err=%v", err)
	}
	if time.Since(started) > 500*time.Millisecond {
		t.Fatalf("non-cooperative callback exceeded bounded cancellation: %s", time.Since(started))
	}
	select {
	case <-entered:
	default:
		t.Fatal("prepared callback was not entered")
	}
}

func TestPreparedUpdateTerminationReapsDescendant(t *testing.T) {
	runner := newPreparedHelperRunner(t)
	marker := filepath.Join(t.TempDir(), "descendant-survived")
	_, err := runner.RunPreparedUpdateContext(context.Background(), t.TempDir(), []string{"verify refs/heads/main 0000000000000000000000000000000000000000"}, CommandLimits{
		Timeout: 50 * time.Millisecond,
		Environment: []string{
			preparedHelperModeEnvironment + "=hang-descendant",
			preparedHelperMarkerEnvironment + "=" + marker,
		},
	}, func(context.Context) error { return nil })
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("descendant cleanup err=%v", err)
	}
	time.Sleep(400 * time.Millisecond)
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("prepared descendant survived process-group cleanup: %v", err)
	}
}

func TestPreparedUpdateTerminationWaitIsBounded(t *testing.T) {
	runner := newPreparedHelperRunner(t)
	started := time.Now()
	_, err := runner.RunPreparedUpdateContext(context.Background(), t.TempDir(), []string{"verify refs/heads/main 0000000000000000000000000000000000000000"}, CommandLimits{
		Timeout: 50 * time.Millisecond, Environment: []string{preparedHelperModeEnvironment + "=hang-final"},
	}, func(context.Context) error { return nil })
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("hung final acknowledgement err=%v", err)
	}
	if time.Since(started) > time.Second {
		t.Fatalf("termination and reap exceeded bound: %s", time.Since(started))
	}
}

// The runner joins a callback that returns inside the termination grace and
// reports one that is still running, so a caller can complete its own barrier.
func TestPreparedUpdateJoinsOrReportsCallbackAfterDeadline(t *testing.T) {
	t.Run("joined", func(t *testing.T) {
		runner := newPreparedHelperRunner(t)
		runner.TerminationGrace = 300 * time.Millisecond
		finished := make(chan struct{})
		_, err := runner.RunPreparedUpdateContext(context.Background(), t.TempDir(), []string{"verify refs/heads/main 0000000000000000000000000000000000000000"}, CommandLimits{
			Timeout: 50 * time.Millisecond, Environment: []string{preparedHelperModeEnvironment + "=normal"},
		}, func(ctx context.Context) error {
			defer close(finished)
			<-ctx.Done()
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
		release := make(chan struct{})
		defer close(release)
		_, err := runner.RunPreparedUpdateContext(context.Background(), t.TempDir(), []string{"verify refs/heads/main 0000000000000000000000000000000000000000"}, CommandLimits{
			Timeout: 50 * time.Millisecond, Environment: []string{preparedHelperModeEnvironment + "=normal"},
		}, func(context.Context) error {
			<-release
			return nil
		})
		if !errors.Is(err, context.DeadlineExceeded) || !errors.Is(err, ErrPreparedCallbackDetached) {
			t.Fatalf("non-cooperative callback err=%v", err)
		}
	})
}
