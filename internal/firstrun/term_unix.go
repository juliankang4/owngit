//go:build darwin || linux

package firstrun

import (
	"errors"
	"os"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

// stopSignals end setup while the terminal is in setup mode: a closed
// terminal and a quit request. Interrupt and termination already stop the
// server through its context.
var stopSignals = []os.Signal{syscall.SIGHUP, syscall.SIGQUIT}

// continueSignals arrive when a stopped OwnGit continues. The shell may have
// changed the terminal mode meanwhile, so setup mode is applied again.
var continueSignals = []os.Signal{syscall.SIGCONT}

func isTerminal(file *os.File) bool {
	_, err := unix.IoctlGetTermios(int(file.Fd()), getTermios)
	return err == nil
}

// foreground reports whether this process is in the foreground process
// group of the terminal file. A background job, such as `owngit serve &`,
// is stopped by the system when it reads the terminal or changes its mode,
// so it must do neither.
func foreground(file *os.File) bool {
	group, err := unix.IoctlGetInt(int(file.Fd()), unix.TIOCGPGRP)
	return err == nil && group == unix.Getpgrp()
}

// enterSetupMode turns off echo, line editing and signal keys on the input
// terminal, so hidden answers are never shown and Ctrl-C arrives as a key.
// Output processing stays on. It refuses when OwnGit is not in the
// terminal's foreground, and changes the mode later only while it is.
func enterSetupMode(input, output *os.File) (setupMode, error) {
	if !foreground(input) || !foreground(output) {
		return setupMode{}, errBackground
	}
	fd := int(input.Fd())
	previous, err := unix.IoctlGetTermios(fd, getTermios)
	if err != nil {
		return setupMode{}, err
	}
	mode := *previous
	mode.Lflag &^= unix.ECHO | unix.ICANON | unix.ISIG | unix.IEXTEN
	mode.Cc[unix.VMIN] = 1
	mode.Cc[unix.VTIME] = 0
	if err := unix.IoctlSetTermios(fd, setTermios, &mode); err != nil {
		return setupMode{}, err
	}
	// From the background the shell owns the terminal and its mode; the
	// shell restores its own mode when it takes the terminal back.
	apply := func(settings *unix.Termios) error {
		if !foreground(input) {
			return nil
		}
		return unix.IoctlSetTermios(fd, setTermios, settings)
	}
	return setupMode{
		restore: func() error { return apply(previous) },
		resume:  func() error { return apply(&mode) },
		escapes: true,
	}, nil
}

// readTerminal copies what the terminal sends to input until done is closed
// or the terminal can no longer be read, then closes input. It never blocks
// in a read: it waits for input with poll and reads only while OwnGit is in
// the terminal's foreground, since a read from the background would make
// the system stop the whole server. Input typed while OwnGit is in the
// background stays in the terminal for the shell or the job that owns it.
// When OwnGit returns to the foreground without being stopped, as with
// `fg` after `bg`, no signal says so, so resume is called here to apply
// setup mode again.
func readTerminal(done <-chan struct{}, file *os.File, input chan<- []byte, resume func()) {
	defer close(input)
	fd := int(file.Fd())
	buffer := make([]byte, 256)
	defer clear(buffer)
	wasForeground := true
	for {
		select {
		case <-done:
			return
		default:
		}
		group, err := unix.IoctlGetInt(fd, unix.TIOCGPGRP)
		if err != nil {
			// The terminal is gone, as after a hangup.
			return
		}
		inForeground := group == unix.Getpgrp()
		if inForeground && !wasForeground {
			resume()
		}
		wasForeground = inForeground
		if !inForeground {
			select {
			case <-done:
				return
			case <-time.After(readTick):
			}
			continue
		}
		fds := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}
		ready, err := unix.Poll(fds, int(readTick/time.Millisecond))
		switch {
		case errors.Is(err, unix.EINTR), err == nil && ready == 0:
			continue
		case err != nil, fds[0].Revents&unix.POLLNVAL != 0:
			return
		}
		// Moved to the background while waiting: leave the input.
		if fds[0].Revents&(unix.POLLHUP|unix.POLLERR) == 0 && !foreground(file) {
			continue
		}
		n, err := unix.Read(fd, buffer)
		if errors.Is(err, unix.EINTR) || errors.Is(err, unix.EAGAIN) {
			continue
		}
		if err != nil || n <= 0 {
			return
		}
		chunk := append([]byte(nil), buffer[:n]...)
		clear(buffer[:n])
		select {
		case input <- chunk:
		case <-done:
			clear(chunk)
			return
		}
	}
}

func terminalColumns(output *os.File) (int, bool) {
	size, err := unix.IoctlGetWinsize(int(output.Fd()), unix.TIOCGWINSZ)
	if err != nil || size.Col == 0 {
		return 0, false
	}
	return int(size.Col), true
}
