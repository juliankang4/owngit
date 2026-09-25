//go:build darwin || linux

package firstrun

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

// fakeTailscale writes a fake tailscale command that records its arguments
// and then runs body. All values are synthetic.
func fakeTailscale(t *testing.T, body string) (string, string) {
	t.Helper()
	dir := t.TempDir()
	record := filepath.Join(dir, "arguments")
	path := filepath.Join(dir, "tailscale")
	script := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> '" + record + "'\nenv > '" + record + ".env'\n" + body + "\n"
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	return path, record
}

func TestTailscaleDetectionIsReadOnly(t *testing.T) {
	running := `echo '{"BackendState":"Running","Self":{"DNSName":"my-mac.tail0000.ts.net.","TailscaleIPs":["fd7a:115c:a1e0::7","100.64.0.7"]}}'`
	for _, c := range []struct {
		name string
		body string
		want Tailscale
	}{
		{"found", running, Tailscale{State: TailscaleRunning, IPv4: "100.64.0.7", Name: "my-mac.tail0000.ts.net"}},
		{"no MagicDNS", `echo '{"BackendState":"Running","Self":{"DNSName":"","TailscaleIPs":["100.64.0.7"]}}'`, Tailscale{State: TailscaleRunning, IPv4: "100.64.0.7"}},
		{"stopped", `echo '{"BackendState":"Stopped","Self":null}'`, Tailscale{State: TailscaleStopped}},
		{"logged out", `echo '{"BackendState":"NeedsLogin"}'`, Tailscale{State: TailscaleStopped}},
		{"daemon not running", `echo 'failed to connect to local tailscaled' >&2; exit 1`, Tailscale{State: TailscaleStopped}},
		{"malformed", `echo '{"BackendState":'`, Tailscale{State: TailscaleMissing}},
		{"no IPv4", `echo '{"BackendState":"Running","Self":{"TailscaleIPs":["fd7a::7"]}}'`, Tailscale{State: TailscaleMissing}},
		{"hostile name", `echo '{"BackendState":"Running","Self":{"DNSName":"x\u001b[2J.ts.net.","TailscaleIPs":["100.64.0.7"]}}'`, Tailscale{State: TailscaleRunning, IPv4: "100.64.0.7"}},
		{"timeout", `sleep 5`, Tailscale{State: TailscaleMissing}},
	} {
		t.Run(c.name, func(t *testing.T) {
			path, record := fakeTailscale(t, c.body)
			t.Setenv("OWNGIT_TEST_SECRET", "must-not-leak")
			started := time.Now()
			got := detectTailscale(context.Background(), path, 500*time.Millisecond)
			if got != c.want {
				t.Fatalf("got %+v want %+v", got, c.want)
			}
			if time.Since(started) > 3*time.Second {
				t.Fatal("detection was not bounded")
			}
			arguments, _ := os.ReadFile(record)
			if strings.TrimSpace(string(arguments)) != "status --json" {
				t.Fatalf("tailscale was run with %q", arguments)
			}
			environment, _ := os.ReadFile(record + ".env")
			if strings.Contains(string(environment), "must-not-leak") {
				t.Fatal("the environment was passed on")
			}
		})
	}
	if got := detectTailscale(context.Background(), filepath.Join(t.TempDir(), "absent"), time.Second); got.State != TailscaleMissing {
		t.Fatalf("missing command: %+v", got)
	}
}

// Without --tailscale, setup finds the command where Tailscale sharing does,
// starting with PATH.
func TestTailscaleDetectionFindsTheCommandOnPath(t *testing.T) {
	path, record := fakeTailscale(t, `echo '{"BackendState":"Running","Self":{"DNSName":"my-mac.tail0000.ts.net.","TailscaleIPs":["100.64.0.7"]}}'`)
	t.Setenv("PATH", filepath.Dir(path))
	want := Tailscale{State: TailscaleRunning, IPv4: "100.64.0.7", Name: "my-mac.tail0000.ts.net"}
	if got := detectTailscale(context.Background(), "", 2*time.Second); got != want {
		t.Fatalf("got %+v want %+v", got, want)
	}
	if arguments, _ := os.ReadFile(record); strings.TrimSpace(string(arguments)) != "status --json" {
		t.Fatalf("tailscale was run with %q", arguments)
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
