// Package checkexec runs project check commands in the user's own working
// environment. It tracks each command with an operating-system process owner,
// bounds captured output, and reports cleanup failures. It cannot contain a
// process that deliberately escapes its assigned group or job, and it does not
// isolate the filesystem. Commands inherit the user's environment and permissions.
package checkexec

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"owngit/internal/gitexec"
	"owngit/internal/state"
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

// KeptOutputBytes is about the most output text a result keeps: the largest
// evidence stored from one check, the raw log. Excerpts and logs are cut from
// this head, so a check's output limit never sets how much memory a run holds.
const KeptOutputBytes = state.MaximumCheckLogBytes

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
	// OutputLimit bounds the combined output of one check. Every byte counts;
	// output beyond the limit stops the check, sets Truncated and makes the
	// result incomplete rather than passed. Whatever the limit, Result.Output
	// keeps only the head of the output text, one character past
	// KeptOutputBytes, with secrets and invalid UTF-8 already replaced.
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
	output := newBoundedBuffer(limit, options.Redact)
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
	case <-output.overflow:
		// Output past the limit stops the check as a timeout does.
		interrupted = true
	}
	result.Duration = time.Since(started)
	cleanupErr := cleanupAttachedProcess(cmd.Process, owner, wait, interrupted)
	result.Output = output.text()
	result.Truncated = output.exceeded()
	if result.Truncated {
		result.Output = fmt.Sprintf("[OwnGit stopped this check: its output passed the limit of %d bytes.]\n", limit) + result.Output
	}
	setExitCode(&result, wait)

	cancelled := interrupted && ctx.Err() != nil
	switch {
	case cancelled:
		result.Status = StatusCancelled
	case interrupted, result.Truncated:
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

// returned reports without blocking whether Wait has returned.
func (wait *processWait) returned() bool {
	if wait.done {
		return true
	}
	select {
	case err := <-wait.ch:
		wait.finish(err)
		return true
	default:
		return false
	}
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
		// Once Wait has returned, the main process is gone and its handle is
		// released: there is nothing to kill, and Windows would report EINVAL.
		// Descendants are left to the owner-termination retry below.
		if !wait.returned() {
			if err := killMainProcess(process); err != nil {
				cleanupErr = errors.Join(cleanupErr, err)
			}
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

// redact replaces secrets in a short message by the same rule as output.
func redact(value string, secrets []string) string {
	buffer := newBoundedBuffer(int64(len(value)), secrets)
	if len(buffer.secrets) == 0 {
		return value
	}
	_, _ = buffer.Write([]byte(value))
	return buffer.text()
}

// boundedBuffer counts every byte written against limit and keeps only the
// head of the stored text: the output with secrets replaced and invalid UTF-8
// runs replaced as the evidence builders do, up to just past KeptOutputBytes.
// It converts as bytes arrive, so a large output limit costs no more memory
// than the evidence OwnGit stores and output that shrinks when converted keeps
// the same head as the whole output would. Keeping one character past
// KeptOutputBytes lets every builder see that later text was dropped.
// overflow closes when the count first passes limit.
type boundedBuffer struct {
	mu          sync.Mutex
	limit       int64
	written     int64
	overflow    chan struct{}
	secrets     []string
	pending     []byte // bytes that may start a secret or an unfinished character
	covered     int    // bytes ahead that belong to a secret already replaced
	kept        strings.Builder
	lastInvalid bool
	full        bool
}

func newBoundedBuffer(limit int64, secrets []string) *boundedBuffer {
	var used []string
	for _, secret := range secrets {
		if secret != "" {
			used = append(used, secret)
		}
	}
	return &boundedBuffer{limit: limit, overflow: make(chan struct{}), secrets: used}
}

func (b *boundedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	before := b.written
	b.written += int64(len(p))
	if before <= b.limit && b.written > b.limit {
		close(b.overflow)
	}
	if within := b.limit - before; within > 0 && !b.full {
		b.pending = append(b.pending, p[:min(int64(len(p)), within)]...)
		b.convert(false)
	}
	return len(p), nil
}

// convert moves pending bytes into kept text. Every byte of every secret
// occurrence is replaced, and overlapping occurrences are one replaced range,
// so no part of a secret is kept. Unless final, it leaves bytes that could
// still start a secret or finish a character when more output arrives: fewer
// than the longest secret plus three bytes.
func (b *boundedBuffer) convert(final bool) {
	index := 0
	for index < len(b.pending) && !b.full {
		rest := b.pending[index:]
		if !final && b.mayStartSecret(rest) {
			break
		}
		if size := b.secretAt(rest); size > 0 {
			if b.covered == 0 {
				b.keep("[redacted]", false)
			}
			b.covered = max(b.covered, size)
		}
		if b.covered > 0 {
			b.covered--
			index++
			continue
		}
		if !final && !utf8.FullRune(rest) {
			break
		}
		character, width := utf8.DecodeRune(rest)
		// A secret that starts inside a character leaves its first bytes
		// invalid, as replacing it in the whole output would.
		for inside := 1; inside < width; inside++ {
			if !final && b.mayStartSecret(rest[inside:]) {
				b.pending = append(b.pending[:0], rest...)
				return
			}
			if b.secretAt(rest[inside:]) > 0 {
				character, width = utf8.RuneError, 1
			}
		}
		if character == utf8.RuneError && width == 1 {
			if !b.lastInvalid {
				b.keep("\uFFFD", true)
			}
			b.lastInvalid = true
		} else {
			b.keep(string(rest[:width]), false)
		}
		index += width
	}
	if b.full {
		b.pending = nil
		return
	}
	b.pending = append(b.pending[:0], b.pending[index:]...)
}

func (b *boundedBuffer) keep(text string, invalid bool) {
	b.kept.WriteString(text)
	b.lastInvalid = invalid
	b.full = b.kept.Len() > KeptOutputBytes
}

// secretAt returns the length of the longest secret that value starts with.
func (b *boundedBuffer) secretAt(value []byte) int {
	longest := 0
	for _, secret := range b.secrets {
		if len(secret) > longest && len(value) >= len(secret) && string(value[:len(secret)]) == secret {
			longest = len(secret)
		}
	}
	return longest
}

func (b *boundedBuffer) mayStartSecret(value []byte) bool {
	for _, secret := range b.secrets {
		if len(value) < len(secret) && secret[:len(value)] == string(value) {
			return true
		}
	}
	return false
}

// text returns the kept text. Bytes still pending at the end are converted,
// unless output past the limit was dropped: then they may be a cut secret and
// are dropped too.
func (b *boundedBuffer) text() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.written <= b.limit {
		b.convert(true)
	}
	return b.kept.String()
}

func (b *boundedBuffer) exceeded() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.written > b.limit
}
