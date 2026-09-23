//go:build !windows

package gitexec

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestRunnerReportsOutputLimitOnlyAfterSuccessfulExit(t *testing.T) {
	runner := outputFixtureRunner(t)
	for _, test := range []struct {
		name   string
		mode   string
		stream string
	}{
		{name: "stdout", mode: "success-stdout", stream: "stdout"},
		{name: "stderr", mode: "success-stderr", stream: "stderr"},
	} {
		t.Run(test.name, func(t *testing.T) {
			result, err := runner.RunWithOutputLimit(context.Background(), "", nil, 8, test.mode)
			var limitErr *LimitError
			if !errors.As(err, &limitErr) || limitErr.Stream != test.stream || limitErr.Limit != 8 {
				t.Fatalf("result=%+v err=%v, want %s LimitError", result, err, test.stream)
			}
			if len(result.Stdout) > 8 || len(result.Stderr) > 8 {
				t.Fatalf("captured output exceeded limit: stdout=%d stderr=%d", len(result.Stdout), len(result.Stderr))
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
timeout-stdout)
  printf '0123456789abcdef'
  exec /bin/sleep 5
  ;;
*)
  exit 64
  ;;
esac
`
	if err := os.WriteFile(script, []byte(content), 0o700); err != nil {
		t.Fatal(err)
	}
	config := filepath.Join(root, "gitconfig.empty")
	if err := os.WriteFile(config, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	return &Runner{
		GitPath: script, HomeDir: root, GlobalConfigPath: config, TempDir: root,
		Timeout: time.Second, OutputLimit: 8, TerminationGrace: 20 * time.Millisecond,
	}
}
