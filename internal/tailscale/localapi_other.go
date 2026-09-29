//go:build !darwin && !windows

package tailscale

import (
	"bufio"
	"os"
	"os/user"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
)

// localAPIFor reaches the LocalAPI of tailscaled at its default socket.
func localAPIFor(string) (bool, Dialer) {
	path, service := tailscaledSocket("/")
	var accounts []uint32
	if service != "" {
		if account, err := user.Lookup(service); err == nil {
			if uid, err := strconv.ParseUint(account.Uid, 10, 32); err == nil {
				accounts = append(accounts, uint32(uid))
			}
		}
	}
	return false, unixSocket(path, accounts...)
}

// linuxDistros are the checks of Tailscale's distro detection, in its order,
// up to the last distribution whose socket differs: the first marker found
// names the distribution. Each marker is a file, or a folder when dir is set.
var linuxDistros = []struct {
	marker string
	dir    bool
	name   string
}{
	{"/usr/syno", true, "synology"},
	{"/usr/local/bin/freenas-debug", false, "truenas"},
	{"/usr/bin/ubnt-device-info", false, "ubnt"},
	{"/opt/google/cros-containers/bin/garcon", false, "crostini"},
	{"/etc/debian_version", false, "debian"},
	{"/etc/arch-release", false, "arch"},
	{"/etc/openwrt_version", false, "openwrt"},
	{"/run/current-system/sw/bin/nixos-version", false, "nixos"},
	{"/etc/config/uLinux.conf", false, "qnap"},
	{"/gokrazy", true, "gokrazy"},
}

// tailscaledSocket is where tailscaled listens for its LocalAPI by default:
// the socket the tailscale command uses without --socket. It follows
// paths.DefaultTailscaledSocket and version/distro in Tailscale's source
// (v1.102.5, commit 5fb2a81b065b), with the same checks in the same order.
// root is "/" except in tests; the paths returned are under it. service is
// the account Tailscale's package runs tailscaled as instead of root, or "":
// "tailscale" on Synology DSM 7 (privilege-dsm7 in Tailscale's source).
func tailscaledSocket(root string) (path, service string) {
	have := func(path string, dir bool) bool {
		info, err := os.Stat(filepath.Join(root, path))
		return err == nil && (!dir || info.IsDir())
	}
	distro := ""
	if runtime.GOOS == "linux" {
		for _, check := range linuxDistros {
			if have(check.marker, check.dir) {
				distro = check.name
				break
			}
		}
	}
	switch distro {
	case "synology":
		if dsmVersion(root) == 6 {
			return filepath.Join(root, "/var/packages/Tailscale/etc/tailscaled.sock"), ""
		}
		// DSM 7, and any version not detected.
		return filepath.Join(root, "/var/packages/Tailscale/var/tailscaled.sock"), "tailscale"
	case "gokrazy":
		return filepath.Join(root, "/perm/tailscaled/tailscaled.sock"), ""
	case "qnap":
		return filepath.Join(root, "/tmp/tailscale/tailscaled.sock"), ""
	}
	if have("/var/run", true) {
		return filepath.Join(root, "/var/run/tailscale/tailscaled.sock"), ""
	}
	return "tailscaled.sock", ""
}

// dsmVersion is the major version of Synology DSM: from the variable a
// package runs with, or else from /etc/VERSION; 0 when not found.
func dsmVersion(root string) int {
	if version, _ := strconv.Atoi(os.Getenv("SYNOPKG_DSM_VERSION_MAJOR")); version != 0 {
		return version
	}
	file, err := os.Open(filepath.Join(root, "/etc/VERSION"))
	if err != nil {
		return 0
	}
	defer file.Close()
	for lines := bufio.NewScanner(file); lines.Scan(); {
		switch strings.TrimSpace(lines.Text()) {
		case `majorversion="7"`:
			return 7
		case `majorversion="6"`:
			return 6
		}
	}
	return 0
}
