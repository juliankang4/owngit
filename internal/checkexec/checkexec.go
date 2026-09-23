// Package checkexec runs project check commands in the user's own working
// environment. It tracks each command with an operating-system process owner,
// bounds captured output, and reports cleanup failures. It cannot contain a
// process that deliberately escapes its assigned group or job, and it does not
// isolate the filesystem. Commands inherit the user's environment and permissions.
package checkexec

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"owngit/internal/gitexec"
)

// Result statuses. They mirror the durable state statuses without importing
// the storage package.
const (
	StatusPassed      = "passed"
	StatusFailed      = "failed"
	StatusError       = "error"
	StatusCancelled   = "cancelled"
	StatusIncomplete  = "incomplete"
	StatusUnavailable = "unavailable"
)

const (
	defaultTimeout     = 10 * time.Minute
	defaultOutputLimit = 64 << 10
	terminationGrace   = 2 * time.Second
)

// DefaultTimeout and DefaultOutputLimit are the effective limits when an
// option is unset. Callers record them with the attempt instead of hiding them.
func DefaultTimeout() time.Duration { return defaultTimeout }

// DefaultOutputLimit is the effective per-check captured output bound.
func DefaultOutputLimit() int64 { return defaultOutputLimit }

type Definition struct {
	Name    string
	Command string
	// Executable and Arguments bypass the platform shell for trusted adapter
	// commands such as the Docker CLI. They are never populated from repository
	// workflow fields directly.
	Executable string
	Arguments  []string
}

type Result struct {
	Name      string
	Command   string
	Status    string
	ExitCode  *int
	Duration  time.Duration
	Output    string
	Truncated bool
	// CleanupError reports that the owned process group or its handle could not
	// be confirmed released. It makes the result non-success while ExitCode
	// still describes the command itself.
	CleanupError string
}

type Options struct {
	// Dir is the working directory. Empty means the current directory.
	Dir string
	// Timeout bounds one check. Zero uses the default.
	Timeout time.Duration
	// OutputLimit bounds the captured combined output per check. The first
	// bytes are kept and the rest is dropped, which sets Truncated and makes
	// the result incomplete rather than passed.
	OutputLimit int64
	// Redact replaces each literal value in captured output.
	Redact []string
	// Env overrides the inherited environment when non-nil.
	Env []string
}

// Run executes the definitions in order and returns one result per definition.
// The boolean reports whether the run was cancelled before every check
// finished. A cancelled run still returns the results collected so far.
func Run(ctx context.Context, definitions []Definition, options Options) ([]Result, bool) {
	results := make([]Result, 0, len(definitions))
	cancelled := false
	for _, definition := range definitions {
		if err := ctx.Err(); err != nil {
			results = append(results, Result{Name: definition.Name, Command: definition.Command, Status: StatusCancelled})
			cancelled = true
			continue
		}
		result, checkCancelled := runOne(ctx, definition, options)
		if checkCancelled {
			cancelled = true
		}
		results = append(results, result)
	}
	return results, cancelled
}

// Process functions are seams for focused cleanup-failure tests.
var (
	ownedProcessStartedObserver func() error
	attachOwnedProcess          = func(cmd *exec.Cmd) (*gitexec.ProcessOwner, error) {
		return gitexec.AttachOwnedProcessObserved(cmd, ownedProcessStartedObserver)
	}
	terminateOwnedProcess = gitexec.TerminateOwnedProcess
	closeOwnedProcess     = gitexec.CloseOwnedProcess
	killMainProcess       = killProcess
	cleanupWaitLimit      = 2 * time.Second
)

func runOne(ctx context.Context, definition Definition, options Options) (Result, bool) {
	result := Result{Name: definition.Name, Command: definition.Command}
	timeout := options.Timeout
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	limit := options.OutputLimit
	if limit <= 0 {
		limit = defaultOutputLimit
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	var cmd *exec.Cmd
	shell := definition.Executable == ""
	if shell {
		cmd = shellCommand(definition.Command)
	} else {
		cmd = exec.Command(definition.Executable, definition.Arguments...)
	}
	cmd.Dir = options.Dir
	if options.Env != nil {
		cmd.Env = options.Env
	}
	output := &boundedBuffer{limit: limit}
	cmd.Stdout = output
	cmd.Stderr = output
	gitexec.ConfigureOwnedProcess(cmd)
	if shell {
		configureShellCommand(cmd, definition.Command)
	}

	started := time.Now()
	if err := cmd.Start(); err != nil {
		result.Duration = time.Since(started)
		if errors.Is(err, exec.ErrNotFound) {
			result.Status = StatusUnavailable
		} else {
			result.Status = StatusError
		}
		result.Output = redact(err.Error(), options.Redact)
		return result, false
	}
	owner, err := attachOwnedProcess(cmd)
	if err != nil {
		wait := startProcessWait(cmd)
		result.Duration = time.Since(started)
		cleanupErr := errors.Join(fmt.Errorf("attach process owner: %w", err), cleanupUnattachedProcess(cmd.Process, wait))
		setExitCode(&result, wait)
		result.Status = StatusError
		result.Output = redact("contain process: "+err.Error(), options.Redact)
		setCleanupError(&result, cleanupErr, options.Redact)
		return result, false
	}

	wait := startProcessWait(cmd)
	interrupted := false
	select {
	case waitErr := <-wait.ch:
		wait.finish(waitErr)
	case <-runCtx.Done():
		interrupted = true
	}
	result.Duration = time.Since(started)
	cleanupErr := cleanupAttachedProcess(cmd.Process, owner, wait, interrupted)
	result.Output = redact(output.String(), options.Redact)
	result.Truncated = output.exceeded
	setExitCode(&result, wait)

	cancelled := interrupted && ctx.Err() != nil
	switch {
	case cancelled:
		result.Status = StatusCancelled
	case interrupted:
		result.Status = StatusIncomplete
	case !wait.done:
		result.Status = StatusError
	case wait.err != nil:
		if code, ok := gitexec.ExitCode(wait.err); ok {
			if code == 127 || code == 9009 {
				result.Status = StatusUnavailable
			} else {
				result.Status = StatusFailed
			}
		} else {
			result.Status = StatusError
		}
	case result.Truncated:
		result.Status = StatusIncomplete
	default:
		result.Status = StatusPassed
	}
	if cleanupErr != nil {
		setCleanupError(&result, cleanupErr, options.Redact)
		result.Status = StatusError
	}
	return result, cancelled
}

func killProcess(process *os.Process) error {
	if process == nil {
		return nil
	}
	if err := process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
		return fmt.Errorf("kill main process: %w", err)
	}
	return nil
}

type processWait struct {
	ch   <-chan error
	done bool
	err  error
}

func startProcessWait(cmd *exec.Cmd) *processWait {
	waitCh := make(chan error, 1)
	go func() { waitCh <- cmd.Wait() }()
	return &processWait{ch: waitCh}
}

func (wait *processWait) finish(err error) {
	wait.done = true
	wait.err = err
}

func (wait *processWait) await(limit time.Duration) bool {
	if wait.done {
		return true
	}
	if limit <= 0 {
		limit = time.Millisecond
	}
	timer := time.NewTimer(limit)
	defer timer.Stop()
	select {
	case err := <-wait.ch:
		wait.finish(err)
		return true
	case <-timer.C:
		return false
	}
}

func cleanupUnattachedProcess(process *os.Process, wait *processWait) error {
	var cleanupErr error
	if err := killMainProcess(process); err != nil {
		cleanupErr = errors.Join(cleanupErr, err)
	}
	if !wait.await(cleanupWaitLimit) {
		cleanupErr = errors.Join(cleanupErr, errors.New("process did not exit after attachment failure"))
		if err := killMainProcess(process); err != nil {
			cleanupErr = errors.Join(cleanupErr, fmt.Errorf("retry main-process kill: %w", err))
		}
		if !wait.await(cleanupWaitLimit) {
			cleanupErr = errors.Join(cleanupErr, errors.New("process wait remained blocked after attachment failure"))
		}
	}
	return errors.Join(cleanupErr, unexpectedWaitError(wait.err))
}

func cleanupAttachedProcess(process *os.Process, owner *gitexec.ProcessOwner, wait *processWait, interrupted bool) error {
	var cleanupErr error
	terminationErr := terminateOwnedProcess(owner, terminationGrace)
	if terminationErr != nil {
		cleanupErr = errors.Join(cleanupErr, terminationErr)
		if err := killMainProcess(process); err != nil {
			cleanupErr = errors.Join(cleanupErr, err)
		}
	}
	if interrupted || terminationErr != nil {
		if err := terminateOwnedProcess(owner, terminationGrace); err != nil {
			cleanupErr = errors.Join(cleanupErr, fmt.Errorf("retry owned-process termination: %w", err))
		}
	}
	if err := closeOwnedProcess(owner); err != nil {
		cleanupErr = errors.Join(cleanupErr, err)
	}
	if !wait.done && !wait.await(cleanupWaitLimit) {
		cleanupErr = errors.Join(cleanupErr, errors.New("process did not exit after owned-process termination"))
		if err := killMainProcess(process); err != nil {
			cleanupErr = errors.Join(cleanupErr, fmt.Errorf("fallback main-process kill: %w", err))
		}
		if !wait.await(cleanupWaitLimit) {
			cleanupErr = errors.Join(cleanupErr, errors.New("process wait remained blocked after cleanup"))
		}
	}
	return errors.Join(cleanupErr, unexpectedWaitError(wait.err))
}

func unexpectedWaitError(err error) error {
	if err == nil {
		return nil
	}
	if _, ok := gitexec.ExitCode(err); ok {
		return nil
	}
	return fmt.Errorf("wait for process cleanup: %w", err)
}

func setExitCode(result *Result, wait *processWait) {
	if wait == nil || !wait.done {
		return
	}
	if wait.err == nil {
		zero := 0
		result.ExitCode = &zero
		return
	}
	if code, ok := gitexec.ExitCode(wait.err); ok {
		result.ExitCode = &code
	}
}

func setCleanupError(result *Result, err error, secrets []string) {
	if err != nil {
		result.CleanupError = redact(err.Error(), secrets)
	}
}

func redact(value string, secrets []string) string {
	for _, secret := range secrets {
		if secret != "" {
			value = strings.ReplaceAll(value, secret, "[redacted]")
		}
	}
	return value
}

type boundedBuffer struct {
	mu       sync.Mutex
	buf      bytes.Buffer
	limit    int64
	exceeded bool
}

func (b *boundedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.exceeded {
		return len(p), nil
	}
	remaining := b.limit - int64(b.buf.Len())
	if remaining <= 0 {
		b.exceeded = true
		return len(p), nil
	}
	if int64(len(p)) > remaining {
		_, _ = b.buf.Write(p[:remaining])
		b.exceeded = true
		return len(p), nil
	}
	_, _ = b.buf.Write(p)
	return len(p), nil
}

func (b *boundedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}
