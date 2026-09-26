//go:build darwin || linux

package firstrun

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// The helper roles run in child processes of this test binary.
const (
	helperRole   = "OWNGIT_FOREGROUND_HELPER"
	helperReport = "OWNGIT_FOREGROUND_REPORT"
)

// A background job of a shell, such as `owngit serve &`, may not use the
// terminal: changing its mode or reading it makes the system stop the whole
// process. So only the foreground process group counts as interactive, and
// setup mode refuses to start in the background. The test starts a session
// on a new pseudo terminal whose leader is in the foreground, and the
// leader starts a second process in its own, background, process group.
func TestOnlyTheForegroundProcessGroupUsesTheTerminal(t *testing.T) {
	master, slavePath := openPseudoTerminal(t)
	defer master.Close()
	var transcript bytes.Buffer
	copied := make(chan struct{})
	go func() { _, _ = io.Copy(&transcript, master); close(copied) }()
	slave, err := os.OpenFile(slavePath, os.O_RDWR|unix.O_NOCTTY, 0)
	if err != nil {
		t.Skipf("cannot open the pseudo terminal: %v", err)
	}
	defer slave.Close()
	report := filepath.Join(t.TempDir(), "report")
	leader := exec.Command(os.Args[0], "-test.run=^TestForegroundHelper$")
	leader.Env = append(os.Environ(), helperRole+"=leader", helperReport+"="+report)
	leader.Stdin, leader.Stdout, leader.Stderr = slave, slave, slave
	leader.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true, Ctty: 0}
	if err := leader.Start(); err != nil {
		t.Fatal(err)
	}
	// A line typed for the foreground, once the leader has left setup mode:
	// the background reader must leave it there.
	for deadline := time.Now().Add(30 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		if got, _ := os.ReadFile(report); strings.Contains(string(got), "leader setup mode=") {
			break
		}
		if time.Now().After(deadline) {
			_ = leader.Process.Kill()
			t.Fatal("the leader did not start")
		}
	}
	if _, err := master.Write([]byte("typed\n")); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- leader.Wait() }()
	select {
	case <-done:
	case <-time.After(60 * time.Second):
		_ = leader.Process.Kill()
		t.Fatal("the helper session did not finish")
	}
	got, _ := os.ReadFile(report)
	want := "leader interactive=true\nleader setup mode=ok\nbackground interactive=false\nbackground setup mode=" +
		errBackground.Error() + "\nbackground reader ended, read 0 bytes\nbackground finished\nterminal mode unchanged\n" +
		"input left for the foreground: \"typed\\n\"\nforeground reader ended, read 0 bytes\n"
	if string(got) != want {
		master.Close()
		<-copied
		t.Fatalf("report:\n%s\nwant:\n%s\nterminal:\n%s", got, want, transcript.String())
	}
}

// TestForegroundHelper is one of the processes of the test above; it does
// nothing in an ordinary test run.
func TestForegroundHelper(t *testing.T) {
	role := os.Getenv(helperRole)
	if role == "" {
		t.Skip("run by TestOnlyTheForegroundProcessGroupUsesTheTerminal")
	}
	out, err := os.OpenFile(os.Getenv(helperReport), os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer out.Close()
	say := func(format string, arguments ...any) { fmt.Fprintf(out, format+"\n", arguments...) }
	fmt.Fprintf(out, "%s interactive=%v\n", role, Interactive(os.Stdin, os.Stdout))
	mode, err := enterSetupMode(os.Stdin, os.Stdout)
	if err != nil {
		say("%s setup mode=%v", role, err)
	} else {
		_ = mode.restore()
		say("%s setup mode=ok", role)
	}
	if role != "leader" {
		// The terminal reader runs in the background while a line waits in
		// the terminal. It must neither read it (the system would stop this
		// process) nor keep running after done.
		say("background reader %s", readFor(time.Second))
		return
	}
	// Wait for the line the test types, so the background reader sees
	// input waiting.
	if ready, err := unix.Poll([]unix.PollFd{{Fd: int32(os.Stdin.Fd()), Events: unix.POLLIN}}, 10000); err != nil || ready != 1 {
		t.Fatalf("no typed line: %d %v", ready, err)
	}
	before, err := unix.IoctlGetTermios(int(os.Stdin.Fd()), getTermios)
	if err != nil {
		t.Fatal(err)
	}
	child := exec.Command(os.Args[0], "-test.run=^TestForegroundHelper$")
	child.Env = append(os.Environ(), helperRole+"=background")
	child.Stdin, child.Stdout, child.Stderr = os.Stdin, os.Stdout, os.Stderr
	child.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	// A background process that changed the terminal mode would be stopped
	// by the system and never finish.
	var status unix.WaitStatus
	deadline := time.Now().Add(30 * time.Second)
	for {
		pid, err := unix.Wait4(child.Process.Pid, &status, unix.WNOHANG|unix.WUNTRACED, nil)
		if err != nil && !errors.Is(err, unix.EINTR) {
			t.Fatal(err)
		}
		if pid == child.Process.Pid && status.Stopped() {
			say("background stopped by %v", status.StopSignal())
			_ = child.Process.Kill()
			return
		}
		if pid == child.Process.Pid && status.Exited() {
			say("background finished")
			break
		}
		if time.Now().After(deadline) {
			say("background did not finish")
			_ = child.Process.Kill()
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	after, err := unix.IoctlGetTermios(int(os.Stdin.Fd()), getTermios)
	if err != nil {
		t.Fatal(err)
	}
	if before.Lflag == after.Lflag && bytes.Equal(before.Cc[:], after.Cc[:]) {
		say("terminal mode unchanged")
	} else {
		say("terminal mode changed")
	}
	line := make([]byte, 64)
	n, _ := os.Stdin.Read(line)
	say("input left for the foreground: %q", line[:n])
	// In the foreground with nothing typed, the reader ends soon after
	// done, instead of waiting in a read that outlives setup.
	say("foreground reader %s", readFor(200*time.Millisecond))
}

// readFor runs the terminal reader on stdin for d, then closes done, and
// reports whether the reader ended and what it read. The reader checks done
// every readTick; the 10-second bound only separates a reader that ends from
// one blocked in a read, however slow the machine is.
func readFor(d time.Duration) string {
	done := make(chan struct{})
	input := make(chan []byte, 16)
	go readTerminal(done, os.Stdin, input, func() {})
	time.Sleep(d)
	close(done)
	read := 0
	deadline := time.After(10 * time.Second)
	for {
		select {
		case chunk, ok := <-input:
			if !ok {
				return fmt.Sprintf("ended, read %d bytes", read)
			}
			read += len(chunk)
		case <-deadline:
			return fmt.Sprintf("still running, read %d bytes", read)
		}
	}
}

// openPseudoTerminal opens a new pseudo terminal and returns its master side
// and the path of its terminal side.
func openPseudoTerminal(t *testing.T) (*os.File, string) {
	t.Helper()
	fd, err := unix.Open("/dev/ptmx", unix.O_RDWR|unix.O_NOCTTY|unix.O_CLOEXEC, 0)
	if err != nil {
		t.Skipf("no pseudo terminals: %v", err)
	}
	master := os.NewFile(uintptr(fd), "/dev/ptmx")
	path, err := unlockPseudoTerminal(fd)
	if err != nil {
		master.Close()
		t.Skipf("cannot set up the pseudo terminal: %v", err)
	}
	if !strings.HasPrefix(path, "/dev/") {
		master.Close()
		t.Skipf("unexpected terminal path %q", path)
	}
	return master, path
}
