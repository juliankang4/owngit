package gitexec

import (
	"context"
	"fmt"
	"io"
	"os/exec"
	"time"
)

// RunOwned runs a prepared command that is not Git, such as OwnGit's own
// binary in a helper mode, with the same ownership as Git commands: its own
// process group on Unix and a job object on Windows. It copies stdin to the
// process, if not nil, and waits for it. When ctx ends first, the process and
// everything it started are terminated, with grace between the polite and the
// forced stop, and RunOwned returns ctx.Err() once they are gone.
//
// The caller sets cmd.Path, arguments, environment and output writers. The
// output writers may still be called while RunOwned returns an error for a
// process whose ownership could not be established, so they must be safe for
// concurrent use.
func RunOwned(ctx context.Context, cmd *exec.Cmd, stdin io.Reader, grace time.Duration) error {
	var stdinPipe io.WriteCloser
	if stdin != nil {
		var err error
		if stdinPipe, err = cmd.StdinPipe(); err != nil {
			return fmt.Errorf("open helper input: %w", err)
		}
	}
	if grace <= 0 {
		grace = 2 * time.Second
	}
	waited, err := runOwnedProcess(ctx, cmd, grace, nil, stdin, stdinPipe)
	if !waited {
		return fmt.Errorf("contain helper process: %w", err)
	}
	return err
}
