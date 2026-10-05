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
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"owngit/internal/hostmem"
)

const defaultOutputLimit = 8 << 20

// stderrLimit bounds what a command's stderr keeps. stderr is not the
// command's output: it is Git's explanation of a failure, which error text
// carries to logs and users, so its bound does not follow the output limit
// of a read, which can be a few bytes. Git reports a failure in a few short
// lines, and 64 KiB keeps hundreds of them while bounding the memory of every
// running command. A failure whose stderr passed the bound says it was cut;
// a command that succeeded is not failed for what it wrote there.
const stderrLimit = 64 << 10

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

	// transfers is how many requests may build a pack at once, which divides the
	// packing memory (see package hostmem). Zero means the default.
	// A pointer, so a copy of the Runner shares it.
	transfers *atomic.Int32

	// processSeam optionally injects the owned-process cleanup operations.
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

// LimitError reports that a stream of the named command passed its limit.
type LimitError struct {
	Command string
	Stream  string
	Limit   int64
}

func (e *LimitError) Error() string {
	return fmt.Sprintf("%s: %s exceeded the %d-byte limit", e.Command, e.Stream, e.Limit)
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
		transfers:        new(atomic.Int32),
	}
	if automatic {
		runner.GitPath, runner.GitSource = runner.preferGitBehindShim(gitPath)
	}
	return runner, nil
}

// commandConfig is configuration every Git command receives at command-line
// scope, which overrides the system, global and repository files. It preserves
// exact Git name bytes and disables automatic maintenance, including before
// OwnGit prepares a repository or finishes restoring one. Explicit maintenance
// commands are unaffected. Packing is bounded by the memory the computer
// allows OwnGit (see package hostmem), so clones, fetches and backups finish
// on a small host instead of being killed.
func (r *Runner) commandConfig(textOutput bool) [][2]string {
	config := [][2]string{
		{"maintenance.auto", "false"},
		{"gc.auto", "0"},
		{"receive.autogc", "false"},
		{"core.precomposeUnicode", "false"},
	}
	if runtime.GOOS == "windows" {
		config = append(config, [2]string{"core.longpaths", "true"})
	}
	config = append(config, hostmem.PackingConfig(hostmem.Ceiling(), runtime.NumCPU(), r.packingTransfers())...)
	// A large-file threshold changes what a diff or merge shows, so commands
	// that read text output (see readsTextOutput) do not get one.
	if threshold := hostmem.BigFileThreshold(hostmem.Ceiling(), r.packingTransfers()); !textOutput && threshold != "" {
		config = append(config, [2]string{"core.bigFileThreshold", threshold})
	}
	return config
}

// ErrMemoryBusy reports that a command run under a lock found no free slot of
// the memory gate; the caller leaves and tries again later.
var ErrMemoryBusy = errors.New("Git memory is in use by transfers")

type noGateWait struct{}

// WithoutGateWait marks ctx for a command run while a repository lock is held.
func WithoutGateWait(ctx context.Context) context.Context {
	return context.WithValue(ctx, noGateWait{}, true)
}

func packsInBackground(name string) bool {
	switch strings.TrimPrefix(name, "git ") {
	case "bundle", "repack":
		return true
	}
	return false
}

// readsTextOutput reports whether the Git command named by commandName
// produces output that depends on telling text from binary files or on
// merging text: diff, diff-tree, diff-index, log, show, blame, format-patch,
// range-diff, grep (all show a file above core.bigFileThreshold as binary),
// and merge-tree, merge-file, merge, apply, rebase, cherry-pick (a text merge
// or patch above it is refused or treated as binary). Every other command,
// archive, cat-file, hash-object, pack and ref commands included, gives the
// same bytes with or without the threshold and gets it, which bounds memory.
func readsTextOutput(name string) bool {
	switch strings.TrimPrefix(name, "git ") {
	case "diff", "diff-tree", "diff-index", "log", "show", "blame", "format-patch", "range-diff", "grep",
		"merge-tree", "merge-file", "merge", "apply", "rebase", "cherry-pick":
		return true
	}
	return false
}

// SetTransfers tells the runner how many requests that build a pack the
// server admits at once, so the packing bounds of later commands share the
// memory among them.
func (r *Runner) SetTransfers(n int) {
	if r.transfers != nil {
		r.transfers.Store(int32(n))
	}
}

func (r *Runner) packingTransfers() int {
	if r.transfers != nil {
		if n := int(r.transfers.Load()); n > 0 {
			return n
		}
	}
	return hostmem.DefaultPackers(hostmem.Ceiling())
}

// Environment returns the complete, intentionally small environment used for
// Git. Extra entries are appended; GIT_CONFIG_COUNT, GIT_CONFIG_KEY_n and
// GIT_CONFIG_VALUE_n entries among them are numbered after commandConfig so
// that Git sees both.
func (r *Runner) Environment(extra ...string) []string { return r.environment(false, extra...) }

// environment is Environment for a command that reads text output when
// textOutput is true.
func (r *Runner) environment(textOutput bool, extra ...string) []string {
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
	config := r.commandConfig(textOutput)
	for i, setting := range config {
		env = append(env, fmt.Sprintf("GIT_CONFIG_KEY_%d=%s", i, setting[0]), fmt.Sprintf("GIT_CONFIG_VALUE_%d=%s", i, setting[1]))
	}
	count := len(config)
	for _, entry := range extra {
		name, value, _ := strings.Cut(entry, "=")
		if name == "GIT_CONFIG_COUNT" {
			if added, err := strconv.Atoi(value); err == nil && added > 0 {
				count = len(config) + added
			}
			continue
		}
		for _, prefix := range []string{"GIT_CONFIG_KEY_", "GIT_CONFIG_VALUE_"} {
			if index, ok := strings.CutPrefix(name, prefix); ok {
				if n, err := strconv.Atoi(index); err == nil && n >= 0 {
					entry = prefix + strconv.Itoa(len(config)+n) + "=" + value
				}
			}
		}
		env = append(env, entry)
	}
	return append(env, "GIT_CONFIG_COUNT="+strconv.Itoa(count))
}

// CommitDate is t as a Git date with this computer's UTC offset at that
// time. Git runs with TZ=UTC, so a commit OwnGit writes itself gets its
// date from CommitDate and shows the owner's local time, like the commits
// pushed next to it.
func CommitDate(t time.Time) string {
	return strconv.FormatInt(t.Unix(), 10) + " " + t.In(time.Local).Format("-0700")
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
	// *LimitError, as for a command that finished, provided the stopped
	// process was cleaned up. Use it only for a read whose partial output is
	// shown as incomplete, such as a diff.
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
	name := commandName(args)
	// A backup's bundle and maintenance's repack are background work: they
	// wait for a slot of the memory gate, which transfers share.
	// A command run under a repository lock never waits for a slot, because a
	// transfer holding a slot may be waiting for that lock.
	if gate := hostmem.Shared.Load(); gate != nil && packsInBackground(name) {
		var release func()
		if ctx.Value(noGateWait{}) != nil {
			if release = gate.TryAcquireBackground(); release == nil {
				return Result{}, ErrMemoryBusy
			}
		} else {
			var err error
			if release, err = gate.Acquire(ctx); err != nil {
				return Result{}, err
			}
		}
		defer release()
	}
	var stdout, stderr limitedBuffer
	stdout.limit = limit
	stderr.limit = stderrLimit
	cmd := exec.Command(r.GitPath, args...)
	cmd.Dir = dir
	cmd.Env = r.environment(readsTextOutput(name), extraEnv...)
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
			return Result{}, fmt.Errorf("%s: %w", name, pipeErr)
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
		return Result{}, fmt.Errorf("%s: %w", name, err)
	}
	result := Result{Stdout: stdout.Bytes(), Stderr: stderr.Bytes()}
	// A command stopped at its output limit ends with the cancellation that
	// stopped it. That cancellation came from the limit only when neither the
	// caller's context nor the timeout ended first. If stopping it failed, the
	// limit is only named in the message, not wrapped, so no caller takes the
	// partial output for a usable prefix.
	if err != nil && stopAtLimit && stdout.hasExceeded() && ctx.Err() == nil && errors.Is(runCtx.Err(), context.Canceled) {
		limitErr := &LimitError{Command: name, Stream: "stdout", Limit: limit}
		if !errors.Is(err, ErrProcessCleanup) {
			return result, limitErr
		}
		return result, fmt.Errorf("%s: %w", limitErr.Error(), err)
	}
	// A limit describes only a command that completed successfully and was
	// cleaned up. Process failure, timeout and cleanup failure remain
	// authoritative even when captured output also reached its bound.
	if err != nil {
		return result, commandFailure(name, err, &stderr)
	}
	if stdout.exceeded {
		return result, &LimitError{Command: name, Stream: "stdout", Limit: limit}
	}
	return result, nil
}

// Output runs cmd, a Git command whose environment the caller chose, such as
// the CLI's Git in a user's clone, and returns its stdout. It reports a
// failure as Runner does: the Git command's name and the reason Git wrote to
// stderr, bounded by stderrLimit. Its stdout has no limit, as with
// exec.Cmd.Output.
func Output(cmd *exec.Cmd) ([]byte, error) {
	var stderr limitedBuffer
	stderr.limit = stderrLimit
	cmd.Stderr = &stderr
	output, err := cmd.Output()
	if err != nil {
		return output, commandFailure(commandName(cmd.Args[1:]), err, &stderr)
	}
	return output, nil
}

// commandFailure is the error for a command that failed: its name, the
// failure, and the reason the command wrote to stderr, marked when it was cut.
func commandFailure(name string, err error, stderr *limitedBuffer) error {
	message := strings.TrimSpace(string(stderr.Bytes()))
	if message == "" {
		return fmt.Errorf("%s: %w", name, err)
	}
	if stderr.hasExceeded() {
		message += fmt.Sprintf(" [stderr cut at %d bytes]", stderr.limit)
	}
	return fmt.Errorf("%s: %w: %s", name, err, message)
}

// gitOptionsWithValue are Git's global options that take the next argument
// as their value (handle_options in Git's git.c).
var gitOptionsWithValue = map[string]bool{
	"-C": true, "-c": true, "--git-dir": true, "--work-tree": true, "--namespace": true,
	"--config-env": true, "--shallow-file": true, "--attr-source": true,
}

// gitQueryOptions are the options Git runs in place of a command: --version
// and --help are the version and help commands spelled as options, and the
// others print a path and exit.
var gitQueryOptions = map[string]bool{
	"--version": true, "-v": true, "--help": true, "-h": true,
	"--exec-path": true, "--html-path": true, "--man-path": true, "--info-path": true,
}

// commandName names the Git command that args run, such as "git cat-file",
// for error text. It never includes an argument, because arguments can be
// paths, URLs or configuration that carries a credential. Git reads global
// options before the command, and some take the next argument as their
// value, so the command is the first argument that is neither such an option
// nor its value. An option this list does not know would leave its value in
// that place, so the candidate is the name only when it has the form of a
// Git command. Otherwise, or when args name no command, the name is "git".
func commandName(args []string) string {
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case gitOptionsWithValue[arg]:
			i++
		case gitQueryOptions[arg]:
			return "git " + arg
		case !strings.HasPrefix(arg, "-"):
			if !isGitCommandToken(arg) {
				return "git"
			}
			return "git " + arg
		}
	}
	return "git"
}

// isGitCommandToken reports whether s has the form of a Git command, such as
// cat-file or for-each-ref: a lowercase letter, then lowercase letters,
// digits and hyphens.
func isGitCommandToken(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !('a' <= c && c <= 'z' || i > 0 && ('0' <= c && c <= '9' || c == '-')) {
			return false
		}
	}
	return s != ""
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
	return r.stream(ctx, "Git backend", exec.Command(executable), false, dir, stdin, extraEnv, consume)
}

// StreamGit runs Git with args as Stream runs a backend, with no input: its
// output goes to consume while it runs. The caller bounds the time with ctx
// and the output in consume; returning an error from consume, or cancelling
// ctx, stops Git and its owned descendants before StreamGit returns.
func (r *Runner) StreamGit(ctx context.Context, dir string, consume func(io.Reader) error, args ...string) ([]byte, error) {
	return r.stream(ctx, commandName(args), exec.Command(r.GitPath, args...), readsTextOutput(commandName(args)), dir, nil, nil, consume)
}

// stream runs cmd for Stream and StreamGit; name names it in error text. Its
// stderr keeps the runner's output limit, not stderrLimit, because a Smart
// HTTP caller reads receive-pack's messages there, and passing that limit is
// an error, so the caller knows it did not read all of them.
func (r *Runner) stream(ctx context.Context, name string, cmd *exec.Cmd, textOutput bool, dir string, stdin io.ReadCloser, extraEnv []string, consume func(io.Reader) error) ([]byte, error) {
	var stderr limitedBuffer
	stderr.limit = r.OutputLimit
	if stderr.limit <= 0 {
		stderr.limit = defaultOutputLimit
	}
	cmd.Dir = dir
	cmd.Env = r.environment(textOutput, extraEnv...)
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
		if err := r.processSeam.terminate(owner, grace); err != nil {
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
	if err := r.processSeam.close(owner); err != nil {
		cleanupErr = errors.Join(cleanupErr, err)
	}
	var primary error
	switch {
	case stderr.exceeded:
		primary = &LimitError{Command: name, Stream: "stderr", Limit: stderr.limit}
	case consumeErr != nil:
		primary = consumeErr
	case waitErr != nil:
		primary = commandFailure(name, waitErr, &stderr)
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
// still pending. After attachment, a failed termination or owner release is
// joined to the command's own error under ErrProcessCleanup.
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
	var runErr, cleanupErr error
	select {
	case runErr = <-waitCh:
		<-copyDone
		if runErr == nil {
			runErr = copyErr
		}
	case <-ctx.Done():
		if err := seam.terminate(owner, grace); err != nil {
			cleanupErr = fmt.Errorf("terminate owned process: %w", err)
		}
		<-waitCh
		<-copyDone
		runErr = ctx.Err()
	}
	if err := seam.close(owner); err != nil {
		cleanupErr = errors.Join(cleanupErr, fmt.Errorf("release process owner: %w", err))
	}
	if cleanupErr != nil {
		return true, errors.Join(runErr, fmt.Errorf("%w: %w", ErrProcessCleanup, cleanupErr))
	}
	return true, runErr
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

// ExitCode reports the exit status carried by err. A command whose owned
// process cleanup failed has no status to report, even if it also exited
// with one, so a caller never reads that failure as Git's answer.
func ExitCode(err error) (int, bool) {
	if errors.Is(err, ErrProcessCleanup) {
		return 0, false
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode(), true
	}
	return 0, false
}
