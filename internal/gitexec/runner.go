package gitexec

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

const defaultOutputLimit = 8 << 20

// Runner executes Git with an app-owned configuration and environment.
type Runner struct {
	GitPath          string
	HomeDir          string
	GlobalConfigPath string
	TempDir          string
	Timeout          time.Duration
	OutputLimit      int64
	TerminationGrace time.Duration
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

func New(gitPath, runtimeDir string) (*Runner, error) {
	if gitPath == "" {
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

	return &Runner{
		GitPath:          gitPath,
		HomeDir:          home,
		GlobalConfigPath: configPath,
		TempDir:          temp,
		Timeout:          2 * time.Minute,
		OutputLimit:      defaultOutputLimit,
		TerminationGrace: 2 * time.Second,
	}, nil
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
		for _, key := range []string{"SystemRoot", "WINDIR", "COMSPEC", "PATHEXT", "TEMP", "TMP"} {
			if value := os.Getenv(key); value != "" {
				env = append(env, key+"="+value)
			}
		}
	}
	return append(env, extra...)
}

func (r *Runner) Run(ctx context.Context, dir string, stdin io.Reader, args ...string) (Result, error) {
	return r.RunWithOutputLimit(ctx, dir, stdin, r.OutputLimit, args...)
}

func (r *Runner) RunWithOutputLimit(ctx context.Context, dir string, stdin io.Reader, limit int64, args ...string) (Result, error) {
	if limit <= 0 {
		limit = defaultOutputLimit
	}
	var stdout, stderr limitedBuffer
	stdout.limit = limit
	stderr.limit = limit
	cmd := exec.Command(r.GitPath, args...)
	cmd.Dir = dir
	cmd.Env = r.Environment()
	cmd.Stdin = stdin
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	timeout := r.Timeout
	if timeout <= 0 {
		timeout = 2 * time.Minute
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	err := runOwnedProcess(runCtx, cmd, r.TerminationGrace)
	result := Result{Stdout: stdout.Bytes(), Stderr: stderr.Bytes()}
	if stdout.exceeded {
		return result, &LimitError{Stream: "stdout", Limit: limit}
	}
	if stderr.exceeded {
		return result, &LimitError{Stream: "stderr", Limit: limit}
	}
	if err != nil {
		message := strings.TrimSpace(string(result.Stderr))
		if message != "" {
			return result, fmt.Errorf("git %s: %w: %s", commandName(args), err, message)
		}
		return result, fmt.Errorf("git %s: %w", commandName(args), err)
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
// both subprocess pipes: cancellation closes the request reader and pipes so a
// stalled upload cannot keep a copy goroutine or child process alive.
func (r *Runner) Stream(ctx context.Context, executable string, dir string, stdin io.ReadCloser, extraEnv []string, consume func(io.Reader) error) ([]byte, error) {
	var stderr limitedBuffer
	stderr.limit = r.OutputLimit
	if stderr.limit <= 0 {
		stderr.limit = defaultOutputLimit
	}
	cmd := exec.Command(executable)
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
	configureOwnedProcess(cmd)
	if err := cmd.Start(); err != nil {
		_ = stdinPipe.Close()
		_ = stdout.Close()
		return nil, fmt.Errorf("start Git backend: %w", err)
	}
	owner, err := attachOwnedProcess(cmd)
	if err != nil {
		_ = stdinPipe.Close()
		_ = stdout.Close()
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return nil, fmt.Errorf("contain Git backend process: %w", err)
	}
	defer closeOwnedProcess(owner)

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
	var consumeErr error
	select {
	case consumeErr = <-consumeCh:
		// StdoutPipe must be completely consumed before Wait. An error means
		// the consumer stopped early, so terminate the writer before reaping.
		if consumeErr != nil {
			_ = stdout.Close()
			terminateOwnedProcess(owner, grace)
		}
	case <-ctx.Done():
		consumeErr = ctx.Err()
		closeInput(stdin)
		_ = stdinPipe.Close()
		_ = stdout.Close()
		terminateOwnedProcess(owner, grace)
		if err := <-consumeCh; consumeErr == nil {
			consumeErr = err
		}
	}

	// A backend may exit without consuming its complete request. Closing the
	// source here releases a blocked network-body read before process cleanup.
	closeInput(stdin)
	_ = stdinPipe.Close()
	waitCh := make(chan error, 1)
	go func() { waitCh <- cmd.Wait() }()
	var waitErr error
	select {
	case waitErr = <-waitCh:
	case <-ctx.Done():
		if consumeErr == nil {
			consumeErr = ctx.Err()
		}
		_ = stdout.Close()
		terminateOwnedProcess(owner, grace)
		waitErr = <-waitCh
	}
	inputErr := <-inputCh
	if stderr.exceeded {
		return stderr.Bytes(), &LimitError{Stream: "stderr", Limit: stderr.limit}
	}
	if consumeErr != nil {
		return stderr.Bytes(), consumeErr
	}
	if waitErr != nil {
		message := strings.TrimSpace(string(stderr.Bytes()))
		if message != "" {
			return stderr.Bytes(), fmt.Errorf("Git backend: %w: %s", waitErr, message)
		}
		return stderr.Bytes(), fmt.Errorf("Git backend: %w", waitErr)
	}
	if inputErr != nil && !errors.Is(inputErr, os.ErrClosed) && !errors.Is(inputErr, io.ErrClosedPipe) {
		return stderr.Bytes(), fmt.Errorf("stream Git backend input: %w", inputErr)
	}
	return stderr.Bytes(), nil
}

func closeInput(reader io.ReadCloser) {
	if reader != nil {
		_ = reader.Close()
	}
}

func runOwnedProcess(ctx context.Context, cmd *exec.Cmd, grace time.Duration) error {
	configureOwnedProcess(cmd)
	if err := cmd.Start(); err != nil {
		return err
	}
	owner, err := attachOwnedProcess(cmd)
	if err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return err
	}
	defer closeOwnedProcess(owner)

	waitCh := make(chan error, 1)
	go func() { waitCh <- cmd.Wait() }()
	select {
	case err := <-waitCh:
		return err
	case <-ctx.Done():
		terminateOwnedProcess(owner, grace)
		<-waitCh
		return ctx.Err()
	}
}

type limitedBuffer struct {
	mu       sync.Mutex
	buf      bytes.Buffer
	limit    int64
	exceeded bool
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
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
