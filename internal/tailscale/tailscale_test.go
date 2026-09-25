package tailscale_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"owngit/internal/tailscale"
	"owngit/internal/tailscale/tailscaletest"
)

func TestMain(m *testing.M) {
	tailscaletest.RunIfFake()
	os.Exit(m.Run())
}

func TestFindUsesOnlyTheGivenPath(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "tailscale")
	if _, err := tailscale.Find(missing); tailscale.KindOf(err) != tailscale.KindNotInstalled {
		t.Fatalf("missing override: err=%v", err)
	}
	if _, err := tailscale.Find(t.TempDir()); tailscale.KindOf(err) != tailscale.KindNotInstalled {
		t.Fatalf("a directory was accepted as the command: err=%v", err)
	}
	fake := tailscaletest.New(t, tailscaletest.State{Status: tailscaletest.Running()})
	command, err := tailscale.Find(fake.Path)
	if err != nil || command.Path != fake.Path {
		t.Fatalf("Find(%q) = %+v, %v", fake.Path, command, err)
	}
}

// Each state Tailscale can be in maps to one problem with its own fix.
func TestStatusNamesWhatKeepsHTTPSFromWorking(t *testing.T) {
	cases := []struct {
		name   string
		change func(*tailscaletest.State)
		want   tailscale.Kind
	}{
		{"ready", func(*tailscaletest.State) {}, ""},
		{"signed out", func(state *tailscaletest.State) { state.Status.BackendState = "NeedsLogin" }, tailscale.KindLoggedOut},
		{"turned off", func(state *tailscaletest.State) { state.Status.BackendState = "Stopped" }, tailscale.KindStopped},
		{"awaiting approval", func(state *tailscaletest.State) { state.Status.BackendState = "NeedsMachineAuth" }, tailscale.KindNeedsApproval},
		{"MagicDNS off", func(state *tailscaletest.State) { state.Status.CurrentTailnet.MagicDNSEnabled = false }, tailscale.KindMagicDNSOff},
		{"HTTPS off", func(state *tailscaletest.State) { state.Status.CertDomains = nil }, tailscale.KindHTTPSOff},
		{"certificate for another name", func(state *tailscaletest.State) { state.Status.CertDomains = []string{"other.tail0000.ts.net"} }, tailscale.KindHTTPSOff},
		{"daemon not running", func(state *tailscaletest.State) {
			state.StatusError = "failed to connect to local Tailscale daemon for /localapi/v0/status; not running? Is tailscaled running?"
		}, tailscale.KindNotRunning},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			state := tailscaletest.State{Status: tailscaletest.Running()}
			test.change(&state)
			fake := tailscaletest.New(t, state)
			status, err := tailscale.Command{Path: fake.Path}.Status(context.Background())
			if err == nil {
				err = status.Usable()
			}
			got := tailscale.Kind("")
			if err != nil {
				got = tailscale.KindOf(err)
			}
			if got != test.want {
				t.Fatalf("problem=%q, want %q (err=%v)", got, test.want, err)
			}
			if test.want == "" && status.Name != tailscaletest.Name {
				t.Fatalf("name=%q", status.Name)
			}
		})
	}
}

// OwnGit adds and removes exactly one handler with argument arrays; it
// never runs funnel or "serve reset".
func TestServeWritesExactlyOneHTTPSHandler(t *testing.T) {
	fake := tailscaletest.New(t, tailscaletest.State{Status: tailscaletest.Running()})
	command := tailscale.Command{Path: fake.Path}
	ctx := context.Background()
	target := tailscale.Target(7654)
	if err := command.ServeHTTPS(ctx, 443, target); err != nil {
		t.Fatal(err)
	}
	config, err := command.ServeConfig(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if endpoint := config.Endpoint(tailscaletest.Name, 443, target); !endpoint.Exact {
		t.Fatalf("after writing: %+v", endpoint)
	}
	if err := command.RemoveHTTPS(ctx, 443); err != nil {
		t.Fatal(err)
	}
	config, err = command.ServeConfig(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if endpoint := config.Endpoint(tailscaletest.Name, 443, target); !endpoint.Free {
		t.Fatalf("after removing: %+v", endpoint)
	}
	want := []string{"serve --bg --https=443 http://127.0.0.1:7654", "serve --https=443 --set-path=/ off"}
	if !reflect.DeepEqual(fake.Writes(), want) {
		t.Fatalf("writes=%q, want %q", fake.Writes(), want)
	}
}

// A Linux user who is not Tailscale's operator gets a permission problem,
// and a timeout is reported as such.
func TestServeFailuresAreClassified(t *testing.T) {
	fake := tailscaletest.New(t, tailscaletest.State{
		Status:     tailscaletest.Running(),
		WriteError: "Access denied: serve config denied\n\nUse 'sudo tailscale serve' or 'tailscale set --operator=$USER'",
	})
	err := tailscale.Command{Path: fake.Path}.ServeHTTPS(context.Background(), 443, tailscale.Target(7654))
	var failure *tailscale.Error
	if !errors.As(err, &failure) || failure.Kind != tailscale.KindPermission || !strings.Contains(failure.Detail, "Access denied") {
		t.Fatalf("err=%#v", err)
	}
	fake.Update(func(state *tailscaletest.State) {
		state.WriteError = "unexpected\x1b[31m failure\n" + strings.Repeat("x", 500)
	})
	err = tailscale.Command{Path: fake.Path}.ServeHTTPS(context.Background(), 443, tailscale.Target(7654))
	if !errors.As(err, &failure) || failure.Kind != tailscale.KindFailed || strings.ContainsAny(failure.Detail, "\x1b\n") || len(failure.Detail) > 310 {
		t.Fatalf("detail is not one short printable line: %q", failure.Detail)
	}
}

func TestEndpointTellsOwnGitsFromEverythingElse(t *testing.T) {
	const name = "box.tail0000.ts.net"
	target := tailscale.Target(7654)
	web := func(host string, handlers map[string]tailscale.Handler) map[string]tailscale.WebServer {
		return map[string]tailscale.WebServer{host: {Handlers: handlers}}
	}
	https := map[string]tailscale.TCPHandler{"443": {HTTPS: true}}
	owngit := map[string]tailscale.Handler{"/": {Proxy: target}}
	cases := []struct {
		name        string
		config      tailscale.ServeConfig
		free, exact bool
		found       string
	}{
		{"nothing", tailscale.ServeConfig{}, true, false, ""},
		{"another port only", tailscale.ServeConfig{TCP: map[string]tailscale.TCPHandler{"8443": {HTTPS: true}}, Web: web(name+":8443", owngit)}, true, false, ""},
		{"OwnGit's", tailscale.ServeConfig{TCP: https, Web: web(name+":443", owngit)}, false, true, target},
		{"another program", tailscale.ServeConfig{TCP: https, Web: web(name+":443", map[string]tailscale.Handler{"/": {Proxy: "http://127.0.0.1:3000"}})}, false, false, "http://127.0.0.1:3000"},
		{"another path beside OwnGit's", tailscale.ServeConfig{TCP: https, Web: web(name+":443", map[string]tailscale.Handler{"/": {Proxy: target}, "/grafana": {Proxy: "http://127.0.0.1:3000"}})}, false, false, "/grafana"},
		{"files", tailscale.ServeConfig{TCP: https, Web: web(name+":443", map[string]tailscale.Handler{"/": {Path: "/srv/www"}})}, false, false, "/srv/www"},
		{"TCP forwarding", tailscale.ServeConfig{TCP: map[string]tailscale.TCPHandler{"443": {TCPForward: "127.0.0.1:22"}}}, false, false, "tcp_forward 443 127.0.0.1:22"},
		{"OwnGit's with Funnel", tailscale.ServeConfig{TCP: https, Web: web(name+":443", owngit), AllowFunnel: map[string]bool{name + ":443": true}}, false, false, "funnel"},
		{"a foreground session", tailscale.ServeConfig{Foreground: map[string]tailscale.ServeConfig{"session": {TCP: https, Web: web(name+":443", owngit)}}}, false, false, "foreground"},
		{"another program with Funnel under another name", tailscale.ServeConfig{TCP: https, Web: web("old.tail0000.ts.net:443", owngit), AllowFunnel: map[string]bool{"old.tail0000.ts.net:443": true}}, false, false, "funnel"},
		{"incomplete", tailscale.ServeConfig{TCP: https}, false, false, "incomplete"},
	}
	for _, test := range cases {
		endpoint := test.config.Endpoint(name, 443, target)
		if endpoint.Free != test.free || endpoint.Exact != test.exact {
			t.Errorf("%s: free=%v exact=%v, want %v %v (%q)", test.name, endpoint.Free, endpoint.Exact, test.free, test.exact, endpoint.Found)
		}
		if test.found != "" && !slices.ContainsFunc(endpoint.Found, func(use tailscale.Use) bool {
			return strings.Contains(use.Kind+" "+use.Address+" "+use.Target, test.found)
		}) {
			t.Errorf("%s: found=%q lacks %q", test.name, endpoint.Found, test.found)
		}
	}
	// A handler left under the name the computer had before a rename
	// answers for nothing: the port stays free for the current name, and
	// OwnGit's endpoint beside it is still exact.
	old := web("old.tail0000.ts.net:443", owngit)
	for label, config := range map[string]tailscale.ServeConfig{
		"a handler under an earlier name":             {TCP: https, Web: old},
		"a handler under an earlier name without TCP": {Web: old},
	} {
		endpoint := config.Endpoint(name, 443, target)
		if !endpoint.Free || endpoint.Exact || len(endpoint.Found) != 0 || len(endpoint.Stale) != 1 || endpoint.Stale[0].Address != "https://old.tail0000.ts.net:443/" {
			t.Errorf("%s: %+v", label, endpoint)
		}
	}
	both := tailscale.ServeConfig{TCP: https, Web: map[string]tailscale.WebServer{"old.tail0000.ts.net:443": {Handlers: owngit}, name + ":443": {Handlers: owngit}}}
	if endpoint := both.Endpoint(name, 443, target); !endpoint.Exact || len(endpoint.Stale) != 1 {
		t.Errorf("OwnGit's endpoint beside an earlier name's: %+v", endpoint)
	}
	if config, err := tailscale.ParseServeConfig([]byte("null\n")); err != nil || !config.Endpoint(name, 443, target).Free {
		t.Fatalf("an empty configuration: %+v %v", config, err)
	}
	if _, err := tailscale.ParseServeConfig([]byte("{not json")); tailscale.KindOf(err) != tailscale.KindUnreadable {
		t.Fatalf("unreadable configuration: %v", err)
	}
}
