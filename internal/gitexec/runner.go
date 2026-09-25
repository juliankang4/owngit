package gitexec

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"
)

const defaultOutputLimit = 8 << 20

// Stream cleanup seams. Tests replace these to inject termination and owner
// close failures; production uses the real implementations.
var (
	streamTerminateOwnedProcess = TerminateOwnedProcess
	streamCloseOwnedProcess     = CloseOwnedProcess
)

// Runner executes Git with an app-owned configuration and environment.
type Runner struct {
	GitPath          string
	HomeDir          string
	GlobalConfigPath string
	TempDir          string
	Timeout          time.Duration
	OutputLimit      int64
	TerminationGrace time.Duration
	// GitSource says how New chose GitPath, for the startup log.
	GitSource string

	// processSeam optionally injects the attachment-failure cleanup operations.
	// Tests set it; production leaves it nil for the real operations.
	processSeam *processCleanupSeam

	// stdoutCopyTap and stderrCopyTap, when set by tests, observe bytes as they
	// are copied into the output buffers. Production leaves them nil, and then
	// the command writers are the buffers themselves.
	stdoutCopyTap io.Writer
	stderrCopyTap io.Writer
}

type Result struct {
	Stdout []byte
	Stderr []byte
}

type LimitError struct {
	Stream string
	Limit  int64
}

func (e *LimitError) Error() string {
	return fmt.Sprintf("git %s exceeded the %d-byte limit", e.Stream, e.Limit)
}

// New returns a runner for gitPath, or for the Git found on PATH when gitPath
// is empty. On macOS a Git found on PATH that is the /usr/bin/git shim is
// replaced by the same-version Git behind it; see preferGitBehindShim.
func New(gitPath, runtimeDir string) (*Runner, error) {
	automatic := gitPath == ""
	source := gitSourceFlag
	if automatic {
		source = gitSourcePath
		var err error
		gitPath, err = exec.LookPath("git")
		if err != nil {
			return nil, fmt.Errorf("find Git: %w", err)
		}
	}
	gitPath, err := filepath.Abs(gitPath)
	if err != nil {
		return nil, fmt.Errorf("resolve Git path: %w", err)
	}
	if info, err := os.Stat(gitPath); err != nil || info.IsDir() {
		return nil, fmt.Errorf("Git executable is unavailable at %q", gitPath)
	}

	home := filepath.Join(runtimeDir, "git-home")
	temp := filepath.Join(runtimeDir, "tmp")
	if err := os.MkdirAll(home, 0o700); err != nil {
		return nil, fmt.Errorf("create isolated Git home: %w", err)
	}
	if err := os.MkdirAll(temp, 0o700); err != nil {
		return nil, fmt.Errorf("create Git temporary directory: %w", err)
	}
	configPath := filepath.Join(runtimeDir, "gitconfig.empty")
	file, err := os.OpenFile(configPath, os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, fmt.Errorf("create isolated Git config: %w", err)
	}
	if err := file.Close(); err != nil {
		return nil, fmt.Errorf("close isolated Git config: %w", err)
	}

	runner := &Runner{
		GitPath:          gitPath,
		HomeDir:          home,
		GlobalConfigPath: configPath,
		TempDir:          temp,
		Timeout:          2 * time.Minute,
		OutputLimit:      defaultOutputLimit,
		TerminationGrace: 2 * time.Second,
		GitSource:        source,
	}
	if automatic {
		runner.GitPath, runner.GitSource = runner.preferGitBehindShim(gitPath)
	}
	return runner, nil
}

// Environment returns the complete, intentionally small environment used for Git.
func (r *Runner) Environment(extra ...string) []string {
	path := filepath.Dir(r.GitPath)
	if runtime.GOOS != "windows" {
		path += string(os.PathListSeparator) + "/usr/bin:/bin"
	}
	env := []string{
		"PATH=" + path,
		"HOME=" + r.HomeDir,
		"XDG_CONFIG_HOME=" + r.HomeDir,
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_SYSTEM=" + r.GlobalConfigPath,
		"GIT_CONFIG_GLOBAL=" + r.GlobalConfigPath,
		"GIT_TERMINAL_PROMPT=0",
		"GIT_ASKPASS=",
		"GIT_SSH_COMMAND=",
		"LC_ALL=C",
		"LANG=C",
		"TZ=UTC",
		"TMPDIR=" + r.TempDir,
	}
	if runtime.GOOS == "windows" {
		env = append(env,
			"GIT_CONFIG_COUNT=1",
			"GIT_CONFIG_KEY_0=core.longpaths",
			"GIT_CONFIG_VALUE_0=true",
		)
		for _, key := range []string{"SystemRoot", "WINDIR", "COMSPEC", "PATHEXT", "TEMP", "TMP"} {
			if value := os.Getenv(key); value != "" {
				env = append(env, key+"="+value)
			}
		}
	}
	return append(env, extra...)
}

func (r *Runner) Run(ctx context.Context, dir string, stdin io.Reader, args ...string) (Result, error) {
	return r.run(ctx, dir, stdin, r.OutputLimit, nil, 0, args...)
}

// CommandLimits override the runner defaults for one owned command. A zero
// field keeps the runner value. Environment entries are appended to the
// isolated environment and never inherit the host environment.
type CommandLimits struct {
	Timeout     time.Duration
	OutputLimit int64
	Environment []string
	// StopAtOutputLimit ends the command as soon as its output passes
	// OutputLimit instead of letting it run to the end with the rest of its
	// output discarded. The result is then the output up to the limit and a
	// *LimitError, as for a command that finished. Use it only for a read
	// whose partial output is shown as incomplete, such as a diff.
	StopAtOutputLimit bool
}

// RunWithLimits executes Git with per-command bounds. Import work uses it for
// long but finite indexing, verification and inspection commands.
func (r *Runner) RunWithLimits(ctx context.Context, dir string, stdin io.Reader, limits CommandLimits, args ...string) (Result, error) {
	limit := r.OutputLimit
	if limits.OutputLimit > 0 {
		limit = limits.OutputLimit
	}
	return r.runCommand(ctx, dir, stdin, limit, limits.Environment, limits.Timeout, limits.StopAtOutputLimit, args...)
}

// RunWithEnvironment executes Git with the runner's isolated environment plus
// the supplied variables. Callers use this for Git-owned controls such as a
// private index path, never to inherit the host environment.
func (r *Runner) RunWithEnvironment(ctx context.Context, dir string, stdin io.Reader, extraEnv []string, args ...string) (Result, error) {
	return r.run(ctx, dir, stdin, r.OutputLimit, extraEnv, 0, args...)
}

func (r *Runner) RunWithOutputLimit(ctx context.Context, dir string, stdin io.Reader, limit int64, args ...string) (Result, error) {
	return r.run(ctx, dir, stdin, limit, nil, 0, args...)
}

func (r *Runner) run(ctx context.Context, dir string, stdin io.Reader, limit int64, extraEnv []string, commandTimeout time.Duration, args ...string) (Result, error) {
	return r.runCommand(ctx, dir, stdin, limit, extraEnv, commandTimeout, false, args...)
}

func (r *Runner) runCommand(ctx context.Context, dir string, stdin io.Reader, limit int64, extraEnv []string, commandTimeout time.Duration, stopAtLimit bool, args ...string) (Result, error) {
	if limit <= 0 {
		limit = defaultOutputLimit
	}
	var stdout, stderr limitedBuffer
	stdout.limit = limit
	stderr.limit = limit
	cmd := exec.Command(r.GitPath, args...)
	cmd.Dir = dir
	cmd.Env = r.Environment(extraEnv...)
	cmd.Stdout = observedCommandWriter(&stdout, r.stdoutCopyTap)
	cmd.Stderr = observedCommandWriter(&stderr, r.stderrCopyTap)
	// Copy caller stdin only after attachment succeeds. Assigning cmd.Stdin
	// would let os/exec read the caller at Start, before attachment, and that
	// copy can outlive a bounded attachment-failure return.
	var stdinPipe io.WriteCloser
	if stdin != nil {
		var pipeErr error
		stdinPipe, pipeErr = cmd.StdinPipe()
		if pipeErr != nil {
			return Result{}, fmt.Errorf("git %s: %w", commandName(args), pipeErr)
		}
	}

	timeout := r.Timeout
	if commandTimeout > 0 {
		timeout = commandTimeout
	}
	if timeout <= 0 {
		timeout = 2 * time.Minute
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if stopAtLimit {
		stdout.exceededHook = cancel
	}
	waited, err := runOwnedProcess(runCtx, cmd, r.TerminationGrace, r.processSeam, stdin, stdinPipe)
	if !waited {
		// Attachment cleanup returned while the delayed Wait still owns the
		// output buffers, so copied output is not stable. Caller stdin is not
		// read on this path.
		return Result{}, fmt.Errorf("git %s: %w", commandName(args), err)
	}
	result := Result{Stdout: stdout.Bytes(), Stderr: stderr.Bytes()}
	// A command stopped at its output limit ends with the cancellation that
	// stopped it. That cancellation came from the limit only when neither the
	// caller's context nor the timeout ended first.
	if err != nil && stopAtLimit && stdout.hasExceeded() && ctx.Err() == nil && errors.Is(runCtx.Err(), context.Canceled) {
		return result, &LimitError{Stream: "stdout", Limit: limit}
	}
	// A limit describes only a command that completed successfully. Process
	// failure and timeout remain authoritative even when captured output also
	// reached its bound.
	if err != nil {
		message := strings.TrimSpace(string(result.Stderr))
		if message != "" {
			return result, fmt.Errorf("git %s: %w: %s", commandName(args), err, message)
		}
		return result, fmt.Errorf("git %s: %w", commandName(args), err)
	}
	if stdout.exceeded {
		return result, &LimitError{Stream: "stdout", Limit: limit}
	}
	if stderr.exceeded {
		return result, &LimitError{Stream: "stderr", Limit: limit}
	}
	return result, nil
}

func commandName(args []string) string {
	if len(args) == 0 {
		return "command"
	}
	for _, arg := range args {
		if !strings.HasPrefix(arg, "-") {
			return arg
		}
	}
	return "command"
}

// Stream starts an owned Git process, passes stdout to consume, and does not
// return until the process and its owned descendants have been reaped. It owns
// both subprocess pipes and closes stdin when it stops the process. Stream
// also waits for the stdin copy to end, so Close must release a Read blocked
// on stdin. A net/http server request body does not: its Close waits for the
// blocked Read, so a caller must make Close end that Read, for example by
// expiring the connection read deadline.
//
// Cancellation terminates the process before it closes stdin, so a cancelled
// process never sees a clean end of its input. A stdin reader that must not
// end the input cleanly can cancel ctx and block in Read until Close.
func (r *Runner) Stream(ctx context.Context, executable string, dir string, stdin io.ReadCloser, extraEnv []string, consume func(io.Reader) error) ([]byte, error) {
	return r.stream(ctx, exec.Command(executable), dir, stdin, extraEnv, consume)
}

// StreamGit runs Git with args as Stream runs a backend, with no input: its
// output goes to consume while it runs. The caller bounds the time with ctx
// and the output in consume; returning an error from consume, or cancelling
// ctx, stops Git and its owned descendants before StreamGit returns.
func (r *Runner) StreamGit(ctx context.Context, dir string, consume func(io.Reader) error, args ...string) ([]byte, error) {
	return r.stream(ctx, exec.Command(r.GitPath, args...), dir, nil, nil, consume)
}

func (r *Runner) stream(ctx context.Context, cmd *exec.Cmd, dir string, stdin io.ReadCloser, extraEnv []string, consume func(io.Reader) error) ([]byte, error) {
	var stderr limitedBuffer
	stderr.limit = r.OutputLimit
	if stderr.limit <= 0 {
		stderr.limit = defaultOutputLimit
	}
	cmd.Dir = dir
	cmd.Env = r.Environment(extraEnv...)
	cmd.Stderr = &stderr
	stdinPipe, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("open Git backend input: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = stdinPipe.Close()
		return nil, fmt.Errorf("open Git backend output: %w", err)
	}
	ConfigureOwnedProcess(cmd)
	if err := cmd.Start(); err != nil {
		_ = stdinPipe.Close()
		_ = stdout.Close()
		return nil, fmt.Errorf("start Git backend: %w", err)
	}
	owner, err := r.processSeam.attach(cmd)
	if err != nil {
		_ = stdinPipe.Close()
		_ = stdout.Close()
		_, cleanupErr := cleanupUnattachedStartedProcess(cmd, r.TerminationGrace, err, r.processSeam)
		return nil, fmt.Errorf("contain Git backend process: %w", cleanupErr)
	}
	inputCh := make(chan error, 1)
	if stdin == nil {
		_ = stdinPipe.Close()
		inputCh <- nil
	} else {
		go func() {
			_, copyErr := io.Copy(stdinPipe, stdin)
			closeErr := stdinPipe.Close()
			if copyErr == nil {
				copyErr = closeErr
			}
			inputCh <- copyErr
		}()
	}
	consumeCh := make(chan error, 1)
	go func() { consumeCh <- consume(stdout) }()

	grace := r.TerminationGrace
	if grace <= 0 {
		grace = 2 * time.Second
	}
	// Wait starts exactly once. Abort paths start Wait before terminating so
	// the group leader is reaped while its group is signaled. When the
	// consumer stopped early, stdout is closed first so Wait never truncates a
	// read it still needs. Cancellation discards the output and terminates
	// before closing stdout: on Windows, closing a pipe waits for a pending
	// read, which a silent process would hold until it exits by itself.
	var waitOnce sync.Once
	waitCh := make(chan error, 1)
	startWait := func() {
		waitOnce.Do(func() {
			go func() { waitCh <- cmd.Wait() }()
		})
	}
	var cleanupErr error
	terminated := false
	terminate := func() {
		if terminated {
			return
		}
		terminated = true
		if err := streamTerminateOwnedProcess(owner, grace); err != nil {
			cleanupErr = errors.Join(cleanupErr, err)
		}
	}

	var consumeErr error
	select {
	case consumeErr = <-consumeCh:
		// StdoutPipe must be completely consumed before Wait. An error means
		// the consumer stopped early, so close stdout and reap the writer
		// before terminating its group.
		if consumeErr != nil {
			_ = stdout.Close()
			startWait()
			terminate()
		}
	case <-ctx.Done():
		consumeErr = ctx.Err()
		startWait()
		terminate()
		_ = stdout.Close()
		// Only now close the input, which ends the stdin copy: the process
		// is gone and cannot mistake the close for a complete request.
		closeInput(stdin)
		_ = stdinPipe.Close()
		if err := <-consumeCh; consumeErr == nil {
			consumeErr = err
		}
	}

	// A backend may exit without consuming its complete request. Closing the
	// source here ends a stdin copy blocked on that request before process
	// cleanup. If the output ended while ctx was cancelled, terminate first, as
	// above.
	if ctx.Err() != nil {
		startWait()
		terminate()
	}
	closeInput(stdin)
	_ = stdinPipe.Close()
	startWait()
	var waitErr error
	select {
	case waitErr = <-waitCh:
	case <-ctx.Done():
		if consumeErr == nil {
			consumeErr = ctx.Err()
		}
		terminate()
		_ = stdout.Close()
		waitErr = <-waitCh
	}
	inputErr := <-inputCh
	if err := streamCloseOwnedProcess(owner); err != nil {
		cleanupErr = errors.Join(cleanupErr, err)
	}
	var primary error
	switch {
	case stderr.exceeded:
		primary = &LimitError{Stream: "stderr", Limit: stderr.limit}
	case consumeErr != nil:
		primary = consumeErr
	case waitErr != nil:
		message := strings.TrimSpace(string(stderr.Bytes()))
		if message != "" {
			primary = fmt.Errorf("Git backend: %w: %s", waitErr, message)
		} else {
			primary = fmt.Errorf("Git backend: %w", waitErr)
		}
	case inputErr != nil && !errors.Is(inputErr, os.ErrClosed) && !errors.Is(inputErr, io.ErrClosedPipe):
		primary = fmt.Errorf("stream Git backend input: %w", inputErr)
	}
	if cleanupErr != nil {
		if primary == nil {
			return stderr.Bytes(), cleanupErr
		}
		return stderr.Bytes(), errors.Join(primary, cleanupErr)
	}
	return stderr.Bytes(), primary
}

func closeInput(reader io.ReadCloser) {
	if reader != nil {
		_ = reader.Close()
	}
}

func observedCommandWriter(primary, tap io.Writer) io.Writer {
	if tap == nil {
		return primary
	}
	return io.MultiWriter(primary, tap)
}

func closeOwnedStdin(pipe io.WriteCloser) {
	if pipe != nil {
		_ = pipe.Close()
	}
}

// skipOwnedStdinCopyError reports copy errors that os/exec treats as non-fatal
// when a child closes stdin early: a direct write error on the child's stdin
// pipe. os.ErrClosed is included because Wait closes a StdinPipe after the
// child exits. Any caller read error, including io.ErrClosedPipe, is reported.
func skipOwnedStdinCopyError(err error) bool {
	pathErr, ok := err.(*fs.PathError)
	if !ok || pathErr.Op != "write" || pathErr.Path != "|1" {
		return false
	}
	if errors.Is(pathErr.Err, syscall.EPIPE) || errors.Is(pathErr.Err, os.ErrClosed) {
		return true
	}
	// os/exec ignores Windows ERROR_BROKEN_PIPE (109) and ERROR_NO_DATA (232).
	return runtime.GOOS == "windows" && windowsStdinPipeErrno(pathErr.Err)
}

func windowsStdinPipeErrno(err error) bool {
	errno, ok := err.(syscall.Errno)
	return ok && (errno == 109 || errno == 232)
}

// runOwnedProcess starts cmd and waits for it. Caller stdin is copied only
// after attachment succeeds, and that copy finishes before a successful
// return. Attachment failure closes the child pipe and does not read the
// caller. waited is false only when attachment cleanup returns with Wait
// still pending.
func runOwnedProcess(ctx context.Context, cmd *exec.Cmd, grace time.Duration, seam *processCleanupSeam, stdin io.Reader, stdinPipe io.WriteCloser) (bool, error) {
	ConfigureOwnedProcess(cmd)
	if err := cmd.Start(); err != nil {
		closeOwnedStdin(stdinPipe)
		return true, err
	}
	owner, err := seam.attach(cmd)
	if err != nil {
		closeOwnedStdin(stdinPipe)
		return cleanupUnattachedStartedProcess(cmd, grace, err, seam)
	}
	defer CloseOwnedProcess(owner)

	copyDone := make(chan struct{})
	var copyErr error
	if stdin != nil && stdinPipe != nil {
		go func() {
			defer close(copyDone)
			_, err := io.Copy(stdinPipe, stdin)
			closeErr := stdinPipe.Close()
			if skipOwnedStdinCopyError(err) {
				err = nil
			}
			if err == nil && closeErr != nil && !errors.Is(closeErr, os.ErrClosed) {
				err = closeErr
			}
			copyErr = err
		}()
	} else {
		close(copyDone)
	}

	waitCh := make(chan error, 1)
	go func() { waitCh <- cmd.Wait() }()
	select {
	case waitErr := <-waitCh:
		<-copyDone
		if waitErr == nil {
			waitErr = copyErr
		}
		return true, waitErr
	case <-ctx.Done():
		TerminateOwnedProcess(owner, grace)
		<-waitCh
		<-copyDone
		return true, ctx.Err()
	}
}

type limitedBuffer struct {
	mu       sync.Mutex
	buf      bytes.Buffer
	limit    int64
	exceeded bool
	// exceededHook, when set, runs once when the output first passes limit.
	exceededHook func()
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.exceeded {
		return len(p), nil
	}
	remaining := b.limit - int64(b.buf.Len())
	if remaining <= 0 {
		b.markExceeded()
		return len(p), nil
	}
	if int64(len(p)) > remaining {
		_, _ = b.buf.Write(p[:remaining])
		b.markExceeded()
		return len(p), nil
	}
	_, _ = b.buf.Write(p)
	return len(p), nil
}

// markExceeded records the overflow. The caller holds b.mu.
func (b *limitedBuffer) markExceeded() {
	b.exceeded = true
	if b.exceededHook != nil {
		b.exceededHook()
	}
}

func (b *limitedBuffer) hasExceeded() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.exceeded
}

func (b *limitedBuffer) Bytes() []byte {
	b.mu.Lock()
	defer b.mu.Unlock()
	return bytes.Clone(b.buf.Bytes())
}

func ExitCode(err error) (int, bool) {
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode(), true
	}
	return 0, false
}
