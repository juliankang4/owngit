//go:build !windows

package tailscale

import (
	"context"
	"errors"
	"io"
	"net"
	"os"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// OwnGit keeps a connection to a Unix socket only when the kernel reports
// that the process listening there runs as Tailscale's service account:
// root, or the account named for that socket (Synology DSM 7). A program of
// another account listening where Tailscale's socket belongs is refused and
// named, and receives nothing. A system that cannot report the account
// trusts no socket. The server here is synthetic and runs as the test's own
// account, which the test makes the service account where it is accepted.
func TestUnixSocketTrustsOnlyTailscalesServiceAccount(t *testing.T) {
	// A Unix socket path must be short.
	dir, err := os.MkdirTemp("/tmp", "ts")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	path := dir + "/tailscaled.sock"
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	received := make(chan int, 8)
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			content, _ := io.ReadAll(conn)
			conn.Close()
			received <- len(content)
		}
	}()

	me := uint32(os.Geteuid())
	saved := tailscaledUID
	t.Cleanup(func() { tailscaledUID = saved })
	reported := slices.Contains([]string{"linux", "darwin", "freebsd"}, runtime.GOOS)
	for _, test := range []struct {
		name     string
		service  uint32
		accounts []uint32
		trusted  bool
	}{
		{"another account listens", me + 1, nil, false},
		{"the service account listens", me, nil, true},
		{"the socket's own account listens", me + 1, []uint32{me}, true},
		{"yet another account is named", me + 1, []uint32{me + 2}, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			tailscaledUID = test.service
			conn, _, err := unixSocket(path, test.accounts...)(context.Background())
			if test.trusted && reported {
				if err != nil {
					t.Fatalf("refused: %v", err)
				}
				conn.Close()
				<-received
				return
			}
			var failure *Error
			if conn != nil || !errors.As(err, &failure) || failure.Kind != KindUntrustedSocket {
				t.Fatalf("conn=%v err=%v, want %s", conn, err, KindUntrustedSocket)
			}
			want := path
			if reported {
				want += ", uid " + strconv.FormatUint(uint64(me), 10)
			}
			if !strings.HasPrefix(failure.Detail, want) {
				t.Fatalf("detail %q, want it to start with %q", failure.Detail, want)
			}
			if n := <-received; n != 0 {
				t.Fatalf("the refused socket received %d bytes", n)
			}
		})
	}

	// Reading status or Serve through a refused socket sends nothing and
	// reports why.
	tailscaledUID = me + 1
	command := Command{LocalAPI: unixSocket(path)}
	if _, err := command.Status(context.Background()); KindOf(err) != KindUntrustedSocket {
		t.Fatalf("Status: %v", err)
	}
	if n := <-received; n != 0 {
		t.Fatalf("the refused socket received %d bytes", n)
	}
	if _, err := command.ServeConfig(context.Background()); KindOf(err) != KindUntrustedSocket {
		t.Fatalf("ServeConfig: %v", err)
	}
	if n := <-received; n != 0 {
		t.Fatalf("the refused socket received %d bytes", n)
	}
}
