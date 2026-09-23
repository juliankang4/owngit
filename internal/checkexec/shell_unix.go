//go:build !windows

package checkexec

import "os/exec"

func shellCommand(command string) *exec.Cmd {
	return exec.Command("sh", "-c", command)
}

func configureShellCommand(_ *exec.Cmd, _ string) {}
