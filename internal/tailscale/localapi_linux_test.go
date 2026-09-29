package tailscale

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// OwnGit uses the socket the tailscale command uses by default, with
// Tailscale's distro checks in Tailscale's order: a NAS or appliance with
// its own socket is found, and a system that an earlier check names first
// keeps the default socket. Only Synology DSM 7 runs tailscaled as its own
// account instead of root. The file systems here are synthetic.
func TestTailscaledSocketFollowsTheTailscaleCommand(t *testing.T) {
	for _, test := range []struct {
		name    string
		files   []string // a name ending in "/" is a folder
		version string   // SYNOPKG_DSM_VERSION_MAJOR
		want    string   // "" for the relative "tailscaled.sock"
		service string   // the account tailscaled runs as instead of root
	}{
		{"any Linux", []string{"var/run/"}, "", "/var/run/tailscale/tailscaled.sock", ""},
		{"no /var/run", nil, "", "", ""},
		{"Synology DSM 7", []string{"usr/syno/", "var/run/"}, "", "/var/packages/Tailscale/var/tailscaled.sock", "tailscale"},
		{"Synology DSM 6 by its package", []string{"usr/syno/"}, "6", "/var/packages/Tailscale/etc/tailscaled.sock", ""},
		{"Synology DSM 6 by /etc/VERSION", []string{"usr/syno/", "etc/VERSION=majorversion=\"6\"\n"}, "", "/var/packages/Tailscale/etc/tailscaled.sock", ""},
		{"Synology found before Debian", []string{"usr/syno/", "etc/debian_version"}, "7", "/var/packages/Tailscale/var/tailscaled.sock", "tailscale"},
		{"QNAP", []string{"etc/config/uLinux.conf", "var/run/"}, "", "/tmp/tailscale/tailscaled.sock", ""},
		{"Debian found before QNAP", []string{"etc/debian_version", "etc/config/uLinux.conf", "var/run/"}, "", "/var/run/tailscale/tailscaled.sock", ""},
		{"gokrazy", []string{"gokrazy/"}, "", "/perm/tailscaled/tailscaled.sock", ""},
		{"a gokrazy file is not gokrazy", []string{"gokrazy", "var/run/"}, "", "/var/run/tailscale/tailscaled.sock", ""},
		{"a /usr/syno file is not Synology", []string{"usr/syno", "var/run/"}, "", "/var/run/tailscale/tailscaled.sock", ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("SYNOPKG_DSM_VERSION_MAJOR", test.version)
			root := t.TempDir()
			for _, file := range test.files {
				name, content, _ := strings.Cut(file, "=")
				path := filepath.Join(root, name)
				if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
					t.Fatal(err)
				}
				if strings.HasSuffix(name, "/") {
					if err := os.MkdirAll(path, 0o700); err != nil {
						t.Fatal(err)
					}
				} else if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			want := "tailscaled.sock"
			if test.want != "" {
				want = filepath.Join(root, test.want)
			}
			if got, service := tailscaledSocket(root); got != want || service != test.service {
				t.Fatalf("socket %q of %q, want %q of %q", got, service, want, test.service)
			}
		})
	}
}
