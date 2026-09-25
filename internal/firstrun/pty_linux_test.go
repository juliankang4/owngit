package firstrun

import (
	"fmt"

	"golang.org/x/sys/unix"
)

// unlockPseudoTerminal unlocks the terminal side of the master fd and
// returns its path.
func unlockPseudoTerminal(fd int) (string, error) {
	if err := unix.IoctlSetPointerInt(fd, unix.TIOCSPTLCK, 0); err != nil {
		return "", err
	}
	number, err := unix.IoctlGetUint32(fd, unix.TIOCGPTN)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("/dev/pts/%d", number), nil
}
