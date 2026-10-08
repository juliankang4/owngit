//go:build !windows

package checkexec

import (
	"fmt"
	"os/exec"
	"time"
)

const terminationGrace = 2 * time.Second

func shellCommand(command string) *exec.Cmd {
	return exec.Command("sh", "-c", command)
}

func configureShellCommand(_ *exec.Cmd, _ string) {}

func configureCommandLine(_ *exec.Cmd, line string) error {
	if line != "" {
		return fmt.Errorf("CommandLine requires Windows")
	}
	return nil
}
