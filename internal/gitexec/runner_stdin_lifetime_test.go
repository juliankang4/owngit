package gitexec

import (
	"bytes"
	"context"
	"errors"
	"io"
	"io/fs"
	"os/exec"
	"runtime"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

type borrowedInputReader struct {
	release  <-chan struct{}
	returned *atomic.Bool
	late     *atomic.Int32
}

func (input *borrowedInputReader) Read(p []byte) (int, error) {
	<-input.release
	if input.returned.Load() {
		input.late.Add(1)
	}
	return 0, io.EOF
}

func TestRunFailedAttachmentDoesNotOutliveBorrowedStdin(t *testing.T) {
	runner := streamTestRunner(t, t.TempDir())
	runner.TerminationGrace = 40 * time.Millisecond
	attachErr := errors.New("injected attachment failure")
	release := make(chan struct{})
	waitFinished := make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(unblock)
	var started atomic.Pointer[exec.Cmd]
	t.Cleanup(func() {
		unblock()
		if cmd := started.Load(); cmd != nil && cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		select {
		case <-waitFinished:
		case <-time.After(3 * time.Second):
		}
	})
	var returned atomic.Bool
	var late atomic.Int32
	runner.processSeam = &processCleanupSeam{
		attachFunc: func(cmd *exec.Cmd) (*ProcessOwner, error) {
			started.Store(cmd)
			return nil, attachErr
		},
		waitFunc: func(cmd *exec.Cmd) error {
			defer close(waitFinished)
			return cmd.Wait()
		},
	}
	input := &borrowedInputReader{release: release, returned: &returned, late: &late}
	_, err := runner.RunWithLimits(context.Background(), t.TempDir(), input, CommandLimits{
		Timeout: 2 * time.Second, Environment: []string{streamFixtureEnv + "=hold-stdout"},
	})
	returned.Store(true)
	unblock()
	select {
	case <-waitFinished:
	case <-time.After(3 * time.Second):
		t.Fatal("real Wait did not finish after releasing the input fixture")
	}
	if !errors.Is(err, attachErr) {
		t.Fatalf("attachment cause missing: %v", err)
	}
	if late.Load() != 0 {
		t.Fatalf("borrowed stdin remained active after failed Run returned: late reads=%d, error=%v", late.Load(), err)
	}
}

type countedReader struct {
	data     []byte
	offset   int
	returned *atomic.Bool
	late     *atomic.Int32
}

func (r *countedReader) Read(p []byte) (int, error) {
	if r.returned.Load() {
		r.late.Add(1)
	}
	if r.offset >= len(r.data) {
		return 0, io.EOF
	}
	n := copy(p, r.data[r.offset:])
	r.offset += n
	return n, nil
}

func TestRunDeliversStdinBeforeReturn(t *testing.T) {
	runner := streamTestRunner(t, t.TempDir())
	payload := []byte("stdin-payload-line\nsecond-line")
	var returned atomic.Bool
	var late atomic.Int32
	input := &countedReader{data: payload, returned: &returned, late: &late}
	result, err := runner.RunWithLimits(context.Background(), t.TempDir(), input, CommandLimits{
		Timeout: 2 * time.Second, Environment: []string{streamFixtureEnv + "=echo-stdin"},
	})
	returned.Store(true)
	if err != nil {
		t.Fatalf("successful stdin copy returned %v", err)
	}
	if !bytes.Equal(result.Stdout, payload) {
		t.Fatalf("stdout=%q, want %q", result.Stdout, payload)
	}
	if late.Load() != 0 {
		t.Fatalf("stdin copy outlived Run: late reads=%d", late.Load())
	}
}

func TestRunIgnoresEarlyStdinClose(t *testing.T) {
	runner := streamTestRunner(t, t.TempDir())
	input := bytes.NewReader(bytes.Repeat([]byte("x"), 256<<10))
	result, err := runner.RunWithLimits(context.Background(), t.TempDir(), input, CommandLimits{
		Timeout: 2 * time.Second, Environment: []string{streamFixtureEnv + "=exit-without-reading-stdin"},
	})
	noErr(t, err, "early stdin close was fatal")
	if string(result.Stdout) != "closed-stdin" {
		t.Fatalf("stdout=%q, want closed-stdin", result.Stdout)
	}
}

type readerFunc func([]byte) (int, error)

func (f readerFunc) Read(p []byte) (int, error) { return f(p) }

func TestRunReportsCallerStdinReadError(t *testing.T) {
	runner := streamTestRunner(t, t.TempDir())
	readErr := errors.New("caller read failed")
	_, err := runner.RunWithLimits(context.Background(), t.TempDir(), readerFunc(func([]byte) (int, error) {
		return 0, readErr
	}), CommandLimits{
		Timeout: 2 * time.Second, Environment: []string{streamFixtureEnv + "=echo-stdin"},
	})
	if !errors.Is(err, readErr) {
		t.Fatalf("caller read error lost: %v", err)
	}
}

func TestSkipOwnedStdinCopyErrorMatchesExecSemantics(t *testing.T) {
	writePipe := &fs.PathError{Op: "write", Path: "|1", Err: syscall.EPIPE}
	if !skipOwnedStdinCopyError(writePipe) {
		t.Fatal("EPIPE write was fatal")
	}
	for _, errno := range []syscall.Errno{109, 232} {
		if !windowsStdinPipeErrno(errno) {
			t.Fatalf("Windows pipe errno %d was not recognized", errno)
		}
		err := &fs.PathError{Op: "write", Path: "|1", Err: errno}
		if runtime.GOOS == "windows" && !skipOwnedStdinCopyError(err) {
			t.Fatalf("Windows pipe errno %d was fatal", errno)
		}
		if runtime.GOOS != "windows" && skipOwnedStdinCopyError(err) {
			t.Fatalf("Windows pipe errno %d was ignored on %s", errno, runtime.GOOS)
		}
	}
	if !skipOwnedStdinCopyError(io.ErrClosedPipe) {
		t.Fatal("closed pipe was fatal")
	}
	if skipOwnedStdinCopyError(&fs.PathError{Op: "read", Path: "|1", Err: syscall.EPIPE}) {
		t.Fatal("reader EPIPE was ignored")
	}
	if skipOwnedStdinCopyError(errors.New("caller read failed")) {
		t.Fatal("caller read error was ignored")
	}
	if skipOwnedStdinCopyError(nil) {
		t.Fatal("nil error was treated as skippable")
	}
}
