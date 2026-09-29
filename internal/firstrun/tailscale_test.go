//go:build darwin || linux

package firstrun

import (
	"context"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
	"unicode/utf8"

	"owngit/internal/tailscale/tailscaletest"
)

// Setup only reads Tailscale's status, from the fake's LocalAPI here, and
// tells running, installed but not running, and not found apart. All values
// are synthetic.
func TestTailscaleDetectionIsReadOnly(t *testing.T) {
	running := tailscaletest.Running()
	for _, c := range []struct {
		name   string
		change func(*tailscaletest.State)
		want   Tailscale
	}{
		{"found", func(*tailscaletest.State) {}, Tailscale{State: TailscaleRunning, IPv4: tailscaletest.IPv4, Name: tailscaletest.Name}},
		{"no MagicDNS name", func(s *tailscaletest.State) { s.Status.Self.DNSName = "" }, Tailscale{State: TailscaleRunning, IPv4: tailscaletest.IPv4}},
		{"stopped", func(s *tailscaletest.State) { s.Status.BackendState, s.Status.Self = "Stopped", nil }, Tailscale{State: TailscaleStopped}},
		{"logged out", func(s *tailscaletest.State) { s.Status.BackendState = "NeedsLogin" }, Tailscale{State: TailscaleStopped}},
		{"daemon not running", func(s *tailscaletest.State) { s.NotRunning = true }, Tailscale{State: TailscaleStopped}},
		{"status failed", func(s *tailscaletest.State) { s.StatusError = "synthetic failure" }, Tailscale{State: TailscaleStopped}},
		{"no IPv4", func(s *tailscaletest.State) { s.Status.Self.TailscaleIPs = []string{tailscaletest.IPv6} }, Tailscale{State: TailscaleMissing}},
		{"hostile name", func(s *tailscaletest.State) { s.Status.Self.DNSName = "x\u001b[2J.ts.net." }, Tailscale{State: TailscaleRunning, IPv4: tailscaletest.IPv4}},
		{"timeout", func(s *tailscaletest.State) { s.ReadDelay = 1200 }, Tailscale{State: TailscaleMissing}},
	} {
		t.Run(c.name, func(t *testing.T) {
			state := tailscaletest.State{Status: running}
			state.Status.Self = &tailscaletest.Self{DNSName: running.Self.DNSName, TailscaleIPs: running.Self.TailscaleIPs}
			c.change(&state)
			fake := tailscaletest.New(t, state)
			started := time.Now()
			got := readTailscale(context.Background(), fake.Command(), 300*time.Millisecond)
			if got != c.want {
				t.Fatalf("got %+v want %+v", got, c.want)
			}
			if time.Since(started) > time.Second {
				t.Fatal("detection was not bounded")
			}
			if writes := fake.Writes(); len(writes) != 0 {
				t.Fatalf("detection asked Tailscale for %q", writes)
			}
		})
	}
	if got := detectTailscale(context.Background(), filepath.Join(t.TempDir(), "absent"), time.Second); got.State != TailscaleMissing {
		t.Fatalf("missing command: %+v", got)
	}
}

func TestTailscaleCommand(t *testing.T) {
	found := Tailscale{State: TailscaleRunning, IPv4: "100.64.0.7", Name: "my-mac.tail0000.ts.net"}
	if got := tailscaleCommand(found, "7654", ""); got != "owngit network set --listen 100.64.0.7:7654 --base-url http://my-mac.tail0000.ts.net:7654" {
		t.Fatal(got)
	}
	if got := tailscaleCommand(Tailscale{IPv4: "100.64.0.7"}, "7654", "/srv/it's here"); got != `owngit network set --listen 100.64.0.7:7654 --base-url http://100.64.0.7:7654 --state-dir '/srv/it'\''s here'` {
		t.Fatal(got)
	}
}

// A state folder name with a tab, an escape, a direction override or bytes
// that are not UTF-8 is still printed as a command that names exactly that
// folder, and the printed command holds none of those characters raw.
func TestShellQuoteNamesExactlyTheValue(t *testing.T) {
	values := []string{
		"/srv/owngit", "/srv/it's here", "/tmp/한글 폴더", "/tmp/e\u0301\u1100\u1161",
		"/tmp/a\tb", "/tmp/x'y\\z\x1b[2J\x07", "/tmp/\u202eevil\u2066", "/tmp/\xff\xfe\u0085end", "/tmp/$HOME `id` \"q\"",
	}
	for _, value := range values {
		quoted := shellQuote(value)
		for _, r := range quoted {
			if unshowable(r) {
				t.Fatalf("%q holds %U raw", quoted, r)
			}
		}
		if !utf8.ValidString(quoted) {
			t.Fatalf("%q is not UTF-8", quoted)
		}
	}
	ran := false
	for _, shell := range []string{"bash", "zsh"} {
		path, err := exec.LookPath(shell)
		if err != nil {
			continue
		}
		ran = true
		for _, value := range values {
			out, err := exec.Command(path, "-c", "printf %s "+shellQuote(value)).Output()
			if err != nil || string(out) != value {
				t.Errorf("%s read %q as %q (err=%v)", shell, shellQuote(value), out, err)
			}
		}
	}
	if !ran {
		t.Skip("no bash or zsh to read the quoted values")
	}
}
