//go:build windows

package checkexec

import (
	"os/exec"
	"syscall"
)

func shellCommand(_ string) *exec.Cmd {
	cmd := exec.Command("cmd")
	// cmd.exe does not parse the CommandLineToArgvW quoting that os/exec
	// normally applies. The raw command line is installed after process-owner
	// configuration so the ownership flags and command text are both retained.
	cmd.Args = nil
	return cmd
}

func configureShellCommand(cmd *exec.Cmd, command string) {
	// /s strips this outer pair and leaves the command's own quotes intact.
	cmd.SysProcAttr.CmdLine = syscall.EscapeArg(cmd.Path) + ` /s /c "` + command + `"`
}
