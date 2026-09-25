package gitexec

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// Stream fixtures reuse the test binary as the backend executable so the same
// tests can run on Unix and Windows. The fixture environment variable is set
// only for the child process.
const (
	streamFixtureEnv     = "OWNGIT_GITEXEC_STREAM_FIXTURE"
	streamFixturePIDFile = "OWNGIT_GITEXEC_STREAM_FIXTURE_PID"
	streamFixtureMarker  = "OWNGIT_GITEXEC_STREAM_FIXTURE_MARKER"
)

func init() {
	mode := os.Getenv(streamFixtureEnv)
	if mode == "" {
		return
	}
	runStreamFixture(mode)
	os.Exit(0)
}

func runStreamFixture(mode string) {
	switch mode {
	case "success":
		fmt.Fprint(os.Stdout, "stdout-payload")
		fmt.Fprint(os.Stderr, "stderr-payload")
	case "hold-stdout":
		writeStreamFixturePID(os.Getenv(streamFixturePIDFile))
		fmt.Fprint(os.Stdout, "ready\n")
		time.Sleep(30 * time.Second)
	case "stderr-limit":
		writeStreamFixturePID(os.Getenv(streamFixturePIDFile))
		fmt.Fprint(os.Stderr, strings.Repeat("e", 64))
		fmt.Fprint(os.Stdout, "ready\n")
		time.Sleep(30 * time.Second)
	case "echo-stdin":
		_, _ = io.Copy(os.Stdout, os.Stdin)
	case "exit-without-reading-stdin":
		fmt.Fprint(os.Stdout, "closed-stdin")
	case "mark-stdin-eof":
		// Records that the process saw a clean end of its input.
		if _, err := io.Copy(io.Discard, os.Stdin); err == nil {
			_ = os.WriteFile(os.Getenv(streamFixtureMarker), []byte("eof"), 0o600)
		}
	default:
		os.Exit(94)
	}
}

func writeStreamFixturePID(path string) {
	if path == "" {
		return
	}
	_ = os.WriteFile(path, []byte(strconv.Itoa(os.Getpid())), 0o600)
}

// streamTestExecutable returns the absolute path of the running test binary.
// The native harness may invoke it through a relative path, and Stream runs the
// fixture with cmd.Dir set to a temporary directory.
func streamTestExecutable(t *testing.T) string {
	t.Helper()
	executable, err := os.Executable()
	noErr(t, err, "resolve test executable")
	absolute, err := filepath.Abs(executable)
	noErr(t, err, "resolve absolute test executable")
	return absolute
}

func streamTestRunner(t *testing.T, root string) *Runner {
	t.Helper()
	config := filepath.Join(root, "gitconfig.empty")
	noErr(t, os.WriteFile(config, nil, 0o600))
	return &Runner{
		GitPath:          streamTestExecutable(t),
		HomeDir:          root,
		GlobalConfigPath: config,
		TempDir:          root,
		Timeout:          time.Second,
		OutputLimit:      8 << 20,
		TerminationGrace: 50 * time.Millisecond,
	}
}

// streamCleanupSeams records the real cleanup results while injecting sentinel
// failures. Tests that install seams must not run in parallel.
type streamCleanupSeams struct {
	terminateErr error
	closeErr     error
}

func installStreamCleanupSeams(t *testing.T, terminateErr, closeErr error) *streamCleanupSeams {
	t.Helper()
	seams := &streamCleanupSeams{}
	originalTerminate := streamTerminateOwnedProcess
	originalClose := streamCloseOwnedProcess
	streamTerminateOwnedProcess = func(owner *ProcessOwner, grace time.Duration) error {
		seams.terminateErr = TerminateOwnedProcess(owner, grace)
		return errors.Join(seams.terminateErr, terminateErr)
	}
	streamCloseOwnedProcess = func(owner *ProcessOwner) error {
		seams.closeErr = CloseOwnedProcess(owner)
		return errors.Join(seams.closeErr, closeErr)
	}
	t.Cleanup(func() {
		streamTerminateOwnedProcess = originalTerminate
		streamCloseOwnedProcess = originalClose
	})
	return seams
}

// awaitStreamConsumerStart waits for the consumer to report readiness. A
// premature Stream return or a bounded timeout fails the test. Cancellation is
// followed by a bounded join attempt; continued blockage is reported explicitly.
func awaitStreamConsumerStart(t *testing.T, started <-chan struct{}, done <-chan error, cancel context.CancelFunc) {
	t.Helper()
	select {
	case <-started:
		return
	case err := <-done:
		t.Fatalf("Stream returned before the consumer started: %v", err)
	case <-time.After(5 * time.Second):
	}
	cancel()
	select {
	case err := <-done:
		t.Fatalf("consumer did not start within the bound; Stream returned %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("consumer did not start and Stream did not return after cancellation")
	}
}

func TestStreamConsumesFullOutputBeforeWait(t *testing.T) {
	root := t.TempDir()
	runner := streamTestRunner(t, root)
	var consumed bytes.Buffer
	stderr, err := runner.Stream(context.Background(), runner.GitPath, root, nil,
		[]string{streamFixtureEnv + "=success"},
		func(reader io.Reader) error {
			_, err := io.Copy(&consumed, reader)
			return err
		})
	if err != nil {
		t.Fatalf("Stream error=%v", err)
	}
	if consumed.String() != "stdout-payload" {
		t.Fatalf("consumed=%q, want stdout-payload", consumed.String())
	}
	if string(stderr) != "stderr-payload" {
		t.Fatalf("stderr=%q, want stderr-payload", stderr)
	}
}

func TestStreamPreservesConsumerErrorWithCleanupFailures(t *testing.T) {
	root := t.TempDir()
	runner := streamTestRunner(t, root)
	consumerErr := errors.New("consumer stopped early")
	terminateErr := errors.New("termination failed")
	closeErr := errors.New("owner close failed")
	seams := installStreamCleanupSeams(t, terminateErr, closeErr)
	_, err := runner.Stream(context.Background(), runner.GitPath, root, nil,
		[]string{streamFixtureEnv + "=hold-stdout"},
		func(reader io.Reader) error {
			buffer := make([]byte, len("ready\n"))
			if _, err := io.ReadFull(reader, buffer); err != nil {
				return err
			}
			return consumerErr
		})
	if !errors.Is(err, consumerErr) {
		t.Fatalf("Stream error=%v, want consumer sentinel", err)
	}
	if !errors.Is(err, terminateErr) {
		t.Fatalf("Stream error=%v, want termination sentinel", err)
	}
	if !errors.Is(err, closeErr) {
		t.Fatalf("Stream error=%v, want close sentinel", err)
	}
	if seams.terminateErr != nil {
		t.Fatalf("real termination failed: %v", seams.terminateErr)
	}
	if seams.closeErr != nil {
		t.Fatalf("real owner close failed: %v", seams.closeErr)
	}
}

func TestStreamReturnsCloseFailureWithoutPrimaryError(t *testing.T) {
	root := t.TempDir()
	runner := streamTestRunner(t, root)
	closeErr := errors.New("owner close failed")
	seams := installStreamCleanupSeams(t, nil, closeErr)
	_, err := runner.Stream(context.Background(), runner.GitPath, root, nil,
		[]string{streamFixtureEnv + "=success"},
		func(reader io.Reader) error {
			_, err := io.Copy(io.Discard, reader)
			return err
		})
	if !errors.Is(err, closeErr) {
		t.Fatalf("Stream error=%v, want close sentinel", err)
	}
	if seams.closeErr != nil {
		t.Fatalf("real owner close failed: %v", seams.closeErr)
	}
}

func TestStreamPreservesStderrLimitWithCleanupFailures(t *testing.T) {
	root := t.TempDir()
	runner := streamTestRunner(t, root)
	runner.OutputLimit = 8
	consumerErr := errors.New("consumer stopped early")
	terminateErr := errors.New("termination failed")
	closeErr := errors.New("owner close failed")
	seams := installStreamCleanupSeams(t, terminateErr, closeErr)
	_, err := runner.Stream(context.Background(), runner.GitPath, root, nil,
		[]string{streamFixtureEnv + "=stderr-limit"},
		func(reader io.Reader) error {
			buffer := make([]byte, len("ready\n"))
			if _, err := io.ReadFull(reader, buffer); err != nil {
				return err
			}
			return consumerErr
		})
	var limitErr *LimitError
	if !errors.As(err, &limitErr) || limitErr.Stream != "stderr" || limitErr.Limit != 8 {
		t.Fatalf("Stream error=%v, want stderr LimitError", err)
	}
	if !errors.Is(err, terminateErr) || !errors.Is(err, closeErr) {
		t.Fatalf("Stream error=%v, want cleanup sentinels", err)
	}
	if errors.Is(err, consumerErr) {
		t.Fatalf("Stream error=%v, consumer error must not be joined when the limit is primary", err)
	}
	if seams.terminateErr != nil || seams.closeErr != nil {
		t.Fatalf("real cleanup failed: terminate=%v close=%v", seams.terminateErr, seams.closeErr)
	}
}

func TestStreamPreservesCancellationWithCleanupFailures(t *testing.T) {
	root := t.TempDir()
	runner := streamTestRunner(t, root)
	terminateErr := errors.New("termination failed")
	closeErr := errors.New("owner close failed")
	seams := installStreamCleanupSeams(t, terminateErr, closeErr)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	consumerStarted := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		_, err := runner.Stream(ctx, runner.GitPath, root, nil,
			[]string{streamFixtureEnv + "=hold-stdout"},
			func(reader io.Reader) error {
				buffer := make([]byte, len("ready\n"))
				if _, err := io.ReadFull(reader, buffer); err != nil {
					return err
				}
				close(consumerStarted)
				_, err := io.Copy(io.Discard, reader)
				return err
			})
		done <- err
	}()
	awaitStreamConsumerStart(t, consumerStarted, done, cancel)
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Stream error=%v, want context cancellation", err)
		}
		if !errors.Is(err, terminateErr) || !errors.Is(err, closeErr) {
			t.Fatalf("Stream error=%v, want cleanup sentinels", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Stream did not return after cancellation")
	}
	if seams.terminateErr != nil || seams.closeErr != nil {
		t.Fatalf("real cleanup failed: terminate=%v close=%v", seams.terminateErr, seams.closeErr)
	}
}

// QA-051: cancellation must terminate the process while the consumer's read
// of its output is still pending. Closing stdout first ends that read on Unix
// and can block on Windows until the silent process exits by itself, so the
// read must still be pending when termination starts.
func TestStreamCancellationTerminatesBeforeClosingStdout(t *testing.T) {
	root := t.TempDir()
	runner := streamTestRunner(t, root)
	consumerReturned := make(chan struct{})
	readPendingAtTermination := false
	originalTerminate := streamTerminateOwnedProcess
	streamTerminateOwnedProcess = func(owner *ProcessOwner, grace time.Duration) error {
		select {
		case <-consumerReturned:
		case <-time.After(200 * time.Millisecond):
			readPendingAtTermination = true
		}
		return originalTerminate(owner, grace)
	}
	t.Cleanup(func() { streamTerminateOwnedProcess = originalTerminate })

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	consumerStarted := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		_, err := runner.Stream(ctx, runner.GitPath, root, nil,
			[]string{streamFixtureEnv + "=hold-stdout"},
			func(reader io.Reader) error {
				defer close(consumerReturned)
				buffer := make([]byte, len("ready\n"))
				if _, err := io.ReadFull(reader, buffer); err != nil {
					return err
				}
				close(consumerStarted)
				_, err := io.Copy(io.Discard, reader)
				return err
			})
		done <- err
	}()
	awaitStreamConsumerStart(t, consumerStarted, done, cancel)
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Stream error=%v, want context cancellation", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Stream did not return after cancellation")
	}
	if !readPendingAtTermination {
		t.Fatal("the consumer's read had ended before termination started; stdout was closed first")
	}
}

// cancelOnSecondRead delivers one chunk, then cancels the stream and blocks
// until Stream closes it, like a request decoder that finds the rest invalid.
type cancelOnSecondRead struct {
	cancel context.CancelFunc
	reads  int
	closed chan struct{}
	once   sync.Once
}

func (input *cancelOnSecondRead) Read(p []byte) (int, error) {
	input.reads++
	if input.reads == 1 {
		return copy(p, "partial request"), nil
	}
	input.cancel()
	<-input.closed
	return 0, io.ErrClosedPipe
}

func (input *cancelOnSecondRead) Close() error {
	input.once.Do(func() { close(input.closed) })
	return nil
}

// A cancelled Stream must terminate the process before it closes stdin, so
// the process never sees a clean end of a request that was cut short. The
// termination seam is delayed to give a process that did see the end of its
// input ample time to record it.
func TestStreamCancellationTerminatesBeforeClosingStdin(t *testing.T) {
	root := t.TempDir()
	runner := streamTestRunner(t, root)
	originalTerminate := streamTerminateOwnedProcess
	streamTerminateOwnedProcess = func(owner *ProcessOwner, grace time.Duration) error {
		time.Sleep(300 * time.Millisecond)
		return originalTerminate(owner, grace)
	}
	t.Cleanup(func() { streamTerminateOwnedProcess = originalTerminate })

	run := func(stdin io.ReadCloser, ctx context.Context, marker string) error {
		_, err := runner.Stream(ctx, runner.GitPath, root, stdin,
			[]string{streamFixtureEnv + "=mark-stdin-eof", streamFixtureMarker + "=" + marker},
			func(reader io.Reader) error {
				_, err := io.Copy(io.Discard, reader)
				return err
			})
		return err
	}

	control := filepath.Join(root, "control-eof")
	noErr(t, run(io.NopCloser(strings.NewReader("complete request")), context.Background(), control), "control Stream")
	if _, err := os.Stat(control); err != nil {
		t.Fatalf("fixture did not record a clean end of input: %v", err)
	}

	cancelled := filepath.Join(root, "cancelled-eof")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	input := &cancelOnSecondRead{cancel: cancel, closed: make(chan struct{})}
	done := make(chan error, 1)
	go func() { done <- run(input, ctx, cancelled) }()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Stream error=%v, want context cancellation", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Stream did not return after cancellation")
	}
	if _, err := os.Stat(cancelled); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("cancelled process saw a clean end of its input (marker stat: %v)", err)
	}
	select {
	case <-input.closed:
	default:
		t.Fatal("Stream did not close the blocked stdin reader")
	}
}
