//go:build !linux && !darwin && !freebsd

package tailscale

import (
	"errors"
	"net"
)

// socketPeer cannot tell on this system which account listens at the other
// end of conn, so no Unix socket is trusted here.
func socketPeer(*net.UnixConn) (uint32, error) {
	return 0, errors.New("this system does not report which account serves a socket")
}
