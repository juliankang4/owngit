package tailscale

import (
	"context"
	"errors"
	"net"
	"os"
	"time"

	"golang.org/x/sys/windows"
)

// tailscaledPipe is the named pipe of the Tailscale service's LocalAPI. Only
// an administrator's process can create a pipe under this prefix, so it is
// the service's; every user may connect to it.
const tailscaledPipe = `\\.\pipe\ProtectedPrefix\Administrators\Tailscale\tailscaled`

// localAPIDialer reaches the LocalAPI of the Tailscale service.
func localAPIDialer(bool) Dialer {
	return dialPipe
}

// dialPipe connects to tailscaledPipe. Tailscale identifies the user by the
// connection, which allows it identification and nothing more, as the
// tailscale command does. The handle is opened for overlapped I/O, so the
// connection supports deadlines and closing it ends a waiting read.
func dialPipe(ctx context.Context) (net.Conn, string, error) {
	name, err := windows.UTF16PtrFromString(tailscaledPipe)
	if err != nil {
		return nil, "", err
	}
	for {
		handle, err := windows.CreateFile(name, windows.GENERIC_READ|windows.GENERIC_WRITE, 0, nil, windows.OPEN_EXISTING,
			windows.FILE_FLAG_OVERLAPPED|windows.SECURITY_SQOS_PRESENT|windows.SECURITY_IDENTIFICATION, 0)
		if err == nil {
			return pipeConn{os.NewFile(uintptr(handle), tailscaledPipe)}, "", nil
		}
		// Every instance of the pipe is serving another client.
		if !errors.Is(err, windows.ERROR_PIPE_BUSY) {
			return nil, "", &os.PathError{Op: "open", Path: tailscaledPipe, Err: err}
		}
		select {
		case <-ctx.Done():
			return nil, "", ctx.Err()
		case <-time.After(20 * time.Millisecond):
		}
	}
}

// pipeConn is a connection over a named pipe.
type pipeConn struct{ *os.File }

func (pipeConn) LocalAddr() net.Addr  { return pipeAddr{} }
func (pipeConn) RemoteAddr() net.Addr { return pipeAddr{} }

type pipeAddr struct{}

func (pipeAddr) Network() string { return "pipe" }
func (pipeAddr) String() string  { return tailscaledPipe }
