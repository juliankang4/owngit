//go:build !windows

package gitexec

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRunnerReportsOutputLimitOnlyAfterSuccessfulExit(t *testing.T) {
	runner := outputFixtureRunner(t)
	result, err := runner.RunWithOutputLimit(context.Background(), "", nil, 8, "success-stdout")
	var limitErr *LimitError
	if !errors.As(err, &limitErr) || limitErr.Stream != "stdout" || limitErr.Limit != 8 {
		t.Fatalf("result=%+v err=%v, want stdout LimitError", result, err)
	}
	if string(result.Stdout) != "01234567" {
		t.Fatalf("captured stdout=%q, want the first 8 bytes", result.Stdout)
	}
	if !strings.HasPrefix(err.Error(), "git success-stdout: ") {
		t.Fatalf("limit error %q does not name the command", err)
	}
}

// stderr is Git's explanation of a failure, not the command's output, so its
// bound is stderrLimit whatever the output limit of the read. What passes that
// bound is dropped, and the failure says so; on success stderr is not used.
func TestRunnerBoundsStderrIndependentlyOfOutputLimit(t *testing.T) {
	runner := outputFixtureRunner(t)
	for _, test := range []struct {
		mode     string
		exit     int
		stderr   int
		contains string
	}{
		{mode: "success-stderr", stderr: 16},
		{mode: "failure-stderr", exit: 2, stderr: 16, contains: ": 0123456789abcdef"},
		{mode: "success-long-stderr", stderr: stderrLimit},
		{mode: "failure-long-stderr", exit: 2, stderr: stderrLimit, contains: fmt.Sprintf("[stderr cut at %d bytes]", stderrLimit)},
	} {
		t.Run(test.mode, func(t *testing.T) {
			result, err := runner.RunWithOutputLimit(context.Background(), "", nil, 8, test.mode)
			if len(result.Stderr) != test.stderr {
				t.Fatalf("captured stderr=%d bytes, want %d", len(result.Stderr), test.stderr)
			}
			if test.exit == 0 {
				if err != nil {
					t.Fatalf("successful command err=%v", err)
				}
				return
			}
			if code, ok := ExitCode(err); !ok || code != test.exit {
				t.Fatalf("exit code=(%d,%v) err=%v, want %d", code, ok, err, test.exit)
			}
			if !strings.HasPrefix(err.Error(), "git "+test.mode+": exit status 2: ") || !strings.Contains(err.Error(), test.contains) {
				t.Fatalf("error %.200q, want the command, its exit and %q", err, test.contains)
			}
		})
	}
}

func TestRunnerPreservesProcessFailureBeforeOutputLimit(t *testing.T) {
	runner := outputFixtureRunner(t)
	for _, mode := range []string{"failure-stdout", "failure-stderr"} {
		t.Run(mode, func(t *testing.T) {
			result, err := runner.RunWithOutputLimit(context.Background(), "", nil, 8, mode)
			var limitErr *LimitError
			if err == nil || errors.As(err, &limitErr) {
				t.Fatalf("result=%+v err=%v, want process failure instead of output limit", result, err)
			}
			if code, ok := ExitCode(err); !ok || code != 2 {
				t.Fatalf("exit code=(%d,%v) err=%v, want 2", code, ok, err)
			}
		})
	}
}

func TestRunnerPreservesInternalTimeoutBeforeOutputLimit(t *testing.T) {
	runner := outputFixtureRunner(t)
	runner.Timeout = 100 * time.Millisecond
	result, err := runner.RunWithOutputLimit(context.Background(), "", nil, 8, "timeout-stdout")
	var limitErr *LimitError
	if !errors.Is(err, context.DeadlineExceeded) || errors.As(err, &limitErr) {
		t.Fatalf("result=%+v err=%v, want internal deadline instead of output limit", result, err)
	}
}

// StopAtOutputLimit ends a command that keeps running once its output passes
// the limit, and reports the limit with the output up to it. Without it the
// same command runs until its timeout.
func TestRunnerStopsAtOutputLimitWhenAsked(t *testing.T) {
	runner := outputFixtureRunner(t)
	runner.Timeout = 5 * time.Second
	started := time.Now()
	result, err := runner.RunWithLimits(context.Background(), "", nil, CommandLimits{OutputLimit: 8, StopAtOutputLimit: true}, "timeout-stdout")
	var limitErr *LimitError
	if !errors.As(err, &limitErr) || limitErr.Stream != "stdout" || string(result.Stdout) != "01234567" || time.Since(started) > 3*time.Second {
		t.Fatalf("result=%q err=%v after %s, want a prompt stdout LimitError", result.Stdout, err, time.Since(started))
	}
	result, err = runner.RunWithLimits(context.Background(), "", nil, CommandLimits{OutputLimit: 8, StopAtOutputLimit: true}, "success-stdout")
	if !errors.As(err, &limitErr) || string(result.Stdout) != "01234567" {
		t.Fatalf("a finished command: result=%q err=%v", result.Stdout, err)
	}
	result, err = runner.RunWithLimits(context.Background(), "", nil, CommandLimits{OutputLimit: 64, StopAtOutputLimit: true}, "success-stdout")
	if err != nil || string(result.Stdout) != "0123456789abcdef" {
		t.Fatalf("output within the limit: result=%q err=%v", result.Stdout, err)
	}
	// The caller's own cancellation stays a cancellation.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := runner.RunWithLimits(ctx, "", nil, CommandLimits{OutputLimit: 8, StopAtOutputLimit: true}, "timeout-stdout"); errors.As(err, &limitErr) || err == nil {
		t.Fatalf("a canceled caller got %v", err)
	}
	runner.Timeout = 300 * time.Millisecond
	if _, err := runner.RunWithLimits(context.Background(), "", nil, CommandLimits{OutputLimit: 8}, "timeout-stdout"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("without StopAtOutputLimit: err=%v, want the timeout", err)
	}
}

func outputFixtureRunner(t *testing.T) *Runner {
	t.Helper()
	root := t.TempDir()
	script := filepath.Join(root, "git-output-fixture")
	content := `#!/bin/sh
case "$1" in
success-stdout)
  printf '0123456789abcdef'
  exit 0
  ;;
success-stderr)
  printf '0123456789abcdef' >&2
  exit 0
  ;;
failure-stdout)
  printf '0123456789abcdef'
  exit 2
  ;;
failure-stderr)
  printf '0123456789abcdef' >&2
  exit 2
  ;;
success-long-stderr)
  head -c 70000 /dev/zero | tr '\0' e >&2
  exit 0
  ;;
failure-long-stderr)
  head -c 70000 /dev/zero | tr '\0' e >&2
  exit 2
  ;;
timeout-stdout)
  printf '0123456789abcdef'
  exec /bin/sleep 5
  ;;
*)
  exit 64
  ;;
esac
`
	noErr(t, os.WriteFile(script, []byte(content), 0o700))
	config := filepath.Join(root, "gitconfig.empty")
	noErr(t, os.WriteFile(config, nil, 0o600))
	return &Runner{
		GitPath: script, HomeDir: root, GlobalConfigPath: config, TempDir: root,
		Timeout: time.Second, OutputLimit: 8, TerminationGrace: 20 * time.Millisecond,
	}
}
