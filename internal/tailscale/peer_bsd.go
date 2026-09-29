//go:build darwin || freebsd

package tailscale

import (
	"net"

	"golang.org/x/sys/unix"
)

// socketPeer returns the account of the process that listens at the other
// end of conn, as the kernel recorded it when that process listened
// (LOCAL_PEERCRED).
func socketPeer(conn *net.UnixConn) (uint32, error) {
	raw, err := conn.SyscallConn()
	if err != nil {
		return 0, err
	}
	var credentials *unix.Xucred
	var credentialsErr error
	if err := raw.Control(func(fd uintptr) {
		credentials, credentialsErr = unix.GetsockoptXucred(int(fd), unix.SOL_LOCAL, unix.LOCAL_PEERCRED)
	}); err != nil {
		return 0, err
	}
	if credentialsErr != nil {
		return 0, credentialsErr
	}
	return credentials.Uid, nil
}
