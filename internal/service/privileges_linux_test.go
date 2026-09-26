package service

import (
	"fmt"
	"os"
	"os/exec"
	"syscall"
	"testing"

	"golang.org/x/sys/unix"
)

// The helper runs in a child that has a pseudo terminal as its controlling
// terminal. It reports whether /dev/tty opens before and after
// DetachTerminal through its exit status.
func TestDetachTerminalHelper(t *testing.T) {
	if os.Getenv("OWNGIT_DETACH_HELPER") != "1" {
		t.Skip("runs only as the child of TestDetachTerminalDropsTheControllingTerminal")
	}
	tty, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		os.Exit(3) // the setup gave the child no controlling terminal
	}
	tty.Close()
	if err := DetachTerminal(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(4)
	}
	if tty, err := os.OpenFile("/dev/tty", os.O_RDWR, 0); err == nil {
		tty.Close()
		os.Exit(5) // still attached
	}
	// A child started now has no controlling terminal either.
	if err := exec.Command("sh", "-c", "exec 3<>/dev/tty").Run(); err == nil {
		os.Exit(6)
	}
	os.Exit(0)
}

func TestDetachTerminalDropsTheControllingTerminal(t *testing.T) {
	master, err := os.OpenFile("/dev/ptmx", os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		t.Skipf("no pseudo terminals here: %v", err)
	}
	defer master.Close()
	if err := unix.IoctlSetPointerInt(int(master.Fd()), unix.TIOCSPTLCK, 0); err != nil {
		t.Fatal(err)
	}
	number, err := unix.IoctlGetUint32(int(master.Fd()), unix.TIOCGPTN)
	if err != nil {
		t.Fatal(err)
	}
	terminal, err := os.OpenFile(fmt.Sprintf("/dev/pts/%d", number), os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer terminal.Close()
	child := exec.Command(os.Args[0], "-test.run=^TestDetachTerminalHelper$")
	child.Env = append(os.Environ(), "OWNGIT_DETACH_HELPER=1")
	child.Stdin, child.Stdout, child.Stderr = terminal, terminal, terminal
	child.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true, Ctty: 0}
	err = child.Run()
	var exit *exec.ExitError
	switch {
	case err == nil:
	case errorsAs(err, &exit):
		t.Fatalf("helper exit status %d (3: no terminal to start with, 4: detach failed, 5: /dev/tty still opens, 6: a child still opens /dev/tty)", exit.ExitCode())
	default:
		t.Fatal(err)
	}
}

func errorsAs(err error, target **exec.ExitError) bool {
	exit, ok := err.(*exec.ExitError)
	if ok {
		*target = exit
	}
	return ok
}
