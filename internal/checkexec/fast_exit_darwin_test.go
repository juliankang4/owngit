//go:build darwin

package checkexec

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"syscall"
	"testing"
	"time"

	"owngit/internal/gitexec"
)

func TestRunReportsExitedUnreapedSuccessfulCommandAsPassed(t *testing.T) {
	originalAttach := attachOwnedProcess
	t.Cleanup(func() { attachOwnedProcess = originalAttach })
	attachOwnedProcess = func(cmd *exec.Cmd) (*gitexec.ProcessOwner, error) {
		deadline := time.Now().Add(2 * time.Second)
		for {
			_, err := syscall.Getpgid(cmd.Process.Pid)
			if errors.Is(err, syscall.ESRCH) {
				return originalAttach(cmd)
			}
			if err != nil {
				return nil, fmt.Errorf("observe synthetic check process group: %w", err)
			}
			if time.Now().After(deadline) {
				return nil, errors.New("synthetic check did not reach the exited, unreaped state")
			}
			time.Sleep(time.Millisecond)
		}
	}

	results, cancelled := Run(context.Background(), []Definition{{Name: "fast success", Command: ":"}}, Options{Timeout: 5 * time.Second})
	if cancelled || len(results) != 1 {
		t.Fatalf("results=%+v cancelled=%v", results, cancelled)
	}
	result := results[0]
	if result.Status != StatusPassed || result.ExitCode == nil || *result.ExitCode != 0 {
		t.Fatalf("result=%+v, want passed exit code 0", result)
	}
	if result.CleanupError != "" {
		t.Fatalf("cleanup error=%q, want none", result.CleanupError)
	}
}
