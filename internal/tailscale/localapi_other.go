//go:build !darwin && !windows

package tailscale

// tailscaledSocket is where tailscaled listens for its LocalAPI by default.
const tailscaledSocket = "/var/run/tailscale/tailscaled.sock"

// localAPIDialer reaches the LocalAPI of tailscaled.
func localAPIDialer(bool) Dialer {
	return unixSocket(tailscaledSocket)
}
