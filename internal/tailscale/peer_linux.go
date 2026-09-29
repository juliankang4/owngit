package tailscale

import (
	"net"

	"golang.org/x/sys/unix"
)

// socketPeer returns the account of the process that listens at the other
// end of conn, as the kernel recorded it when that process listened
// (SO_PEERCRED).
func socketPeer(conn *net.UnixConn) (uint32, error) {
	raw, err := conn.SyscallConn()
	if err != nil {
		return 0, err
	}
	var credentials *unix.Ucred
	var credentialsErr error
	if err := raw.Control(func(fd uintptr) {
		credentials, credentialsErr = unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED)
	}); err != nil {
		return 0, err
	}
	if credentialsErr != nil {
		return 0, credentialsErr
	}
	return credentials.Uid, nil
}
