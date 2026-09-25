package firstrun

import (
	"fmt"

	"golang.org/x/sys/unix"
)

// unlockPseudoTerminal grants and unlocks the terminal side of the master
// fd and returns its path. The terminal side has the master's unit number,
// so its path is found without the name ioctl, which needs a buffer.
func unlockPseudoTerminal(fd int) (string, error) {
	if err := unix.IoctlSetInt(fd, unix.TIOCPTYGRANT, 0); err != nil {
		return "", err
	}
	if err := unix.IoctlSetInt(fd, unix.TIOCPTYUNLK, 0); err != nil {
		return "", err
	}
	var master unix.Stat_t
	if err := unix.Fstat(fd, &master); err != nil {
		return "", err
	}
	path := fmt.Sprintf("/dev/ttys%03d", unix.Minor(uint64(master.Rdev)))
	var terminal unix.Stat_t
	if err := unix.Stat(path, &terminal); err != nil {
		return "", err
	}
	if unix.Minor(uint64(terminal.Rdev)) != unix.Minor(uint64(master.Rdev)) {
		return "", fmt.Errorf("%s is not the terminal side", path)
	}
	return path, nil
}
