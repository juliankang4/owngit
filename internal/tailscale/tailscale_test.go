package tailscale_test

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/netip"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"owngit/internal/tailscale"
	"owngit/internal/tailscale/tailscaletest"
)

// Each fake's LocalAPI answers from its own state, also while another fake
// exists, so a request made for one test cannot report or record the fake of
// another.
func TestFakesKeepTheirOwnState(t *testing.T) {
	first := tailscaletest.New(t, tailscaletest.State{Status: tailscaletest.Running()})
	other := tailscaletest.Running()
	other.Self.DNSName, other.CertDomains = "other.tail0000.ts.net.", []string{"other.tail0000.ts.net"}
	second := tailscaletest.New(t, tailscaletest.State{Status: other})
	for _, test := range []struct {
		fake *tailscaletest.Fake
		want string
	}{{first, tailscaletest.Name}, {second, "other.tail0000.ts.net"}} {
		status, err := test.fake.Command().Status(context.Background())
		if err != nil || status.Name != test.want || !reflect.DeepEqual(test.fake.Calls(), []string{tailscaletest.StatusRead}) {
			t.Fatalf("want %s: status=%+v err=%v calls=%q", test.want, status, err, test.fake.Calls())
		}
	}
}

// LocalAPI requests and the test's own reads and changes overlap without
// failing or losing a change.
func TestFakeStateSurvivesOverlappingRequestsAndReads(t *testing.T) {
	fake := tailscaletest.New(t, tailscaletest.State{Status: tailscaletest.Running()})
	command := fake.Command()
	const commands = 8
	errs := make(chan error, commands)
	for range commands {
		go func() {
			_, err := command.Status(context.Background())
			errs <- err
		}()
	}
	// The test changes and reads the state while the requests run.
	changes := 0
	for done := 0; done < commands; {
		select {
		case err := <-errs:
			if err != nil {
				t.Fatal(err)
			}
			done++
		case <-time.After(5 * time.Millisecond):
			fake.Update(func(s *tailscaletest.State) { s.Calls = append(s.Calls, []string{"test", strconv.Itoa(changes)}) })
			changes++
			_ = fake.State()
		}
	}
	reads, marks := 0, 0
	for _, call := range fake.Calls() {
		switch {
		case call == tailscaletest.StatusRead:
			reads++
		case call == "test "+strconv.Itoa(marks):
			marks++
		default:
			// A lost change shows as a later mark in the place of the missing one.
			t.Fatalf("unexpected or out of order call %q in %q", call, fake.Calls())
		}
	}
	if reads != commands || marks != changes {
		t.Fatalf("%d reads and %d changes recorded, want %d and %d", reads, marks, commands, changes)
	}
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
	if err != nil || command.Path != fake.Path || command.LocalAPI == nil {
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
		// Headscale gives names under its own domain and no certificates;
		// no Tailscale admin console setting changes that.
		{"another control server", func(state *tailscaletest.State) {
			state.Status.Self.DNSName, state.Status.CertDomains = "gitbox.headscale.internal.", nil
		}, tailscale.KindHTTPSUnavailable},
		{"daemon not running", func(state *tailscaletest.State) { state.NotRunning = true }, tailscale.KindNotRunning},
		{"status refused", func(state *tailscaletest.State) { state.StatusError = "synthetic status failure" }, tailscale.KindFailed},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			state := tailscaletest.State{Status: tailscaletest.Running()}
			test.change(&state)
			fake := tailscaletest.New(t, state)
			status, err := fake.Command().Status(context.Background())
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
			if test.want == "" && (status.Name != tailscaletest.Name ||
				!reflect.DeepEqual(status.Addresses, []netip.Addr{netip.MustParseAddr(tailscaletest.IPv4), netip.MustParseAddr(tailscaletest.IPv6)})) {
				t.Fatalf("name=%q addresses=%v", status.Name, status.Addresses)
			}
		})
	}
}

// OwnGit adds and removes exactly one handler, through the LocalAPI; it
// never turns on Funnel or resets Serve.
func TestServeWritesExactlyOneHTTPSHandler(t *testing.T) {
	fake := tailscaletest.New(t, tailscaletest.State{Status: tailscaletest.Running()})
	command := fake.Command()
	ctx := context.Background()
	target := tailscale.Target(7654)
	config, err := command.ServeConfig(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := command.ServeHTTPS(ctx, config, tailscaletest.Name, 443, target); err != nil {
		t.Fatal(err)
	}
	config, err = command.ServeConfig(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if endpoint := config.Endpoint(tailscaletest.Name, 443, target); !endpoint.Exact {
		t.Fatalf("after writing: %+v", endpoint)
	}
	if err := command.RemoveHTTPS(ctx, config, tailscaletest.Name, 443); err != nil {
		t.Fatal(err)
	}
	config, err = command.ServeConfig(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if endpoint := config.Endpoint(tailscaletest.Name, 443, target); !endpoint.Free {
		t.Fatalf("after removing: %+v", endpoint)
	}
	if serve := fake.State().Serve; len(serve.TCP)+len(serve.Web)+len(serve.AllowFunnel) != 0 {
		t.Fatalf("after removing, Tailscale still has %+v", serve)
	}
	if want := []string{tailscaletest.ServeWrite, tailscaletest.ServeWrite}; !reflect.DeepEqual(fake.Writes(), want) {
		t.Fatalf("writes=%q, want %q", fake.Writes(), want)
	}
}

// ownersServe is a Serve configuration with something of the owner's on
// every place near OwnGit's endpoint on port 443: another handler and Funnel
// on 8443, a handler under an earlier name on 443, a foreground session, and
// a field OwnGit does not know.
func ownersServe() (tailscale.ServeConfig, map[string]json.RawMessage) {
	return tailscale.ServeConfig{
		TCP: map[string]tailscale.TCPHandler{"443": {HTTPS: true}, "8443": {HTTPS: true}},
		Web: map[string]tailscale.WebServer{
			tailscaletest.Name + ":8443": {Handlers: map[string]tailscale.Handler{"/": {Proxy: "http://127.0.0.1:3000"}}},
			"oldbox.tail0000.ts.net:443": {Handlers: map[string]tailscale.Handler{"/": {Proxy: "http://127.0.0.1:3001"}}},
		},
		AllowFunnel: map[string]bool{tailscaletest.Name + ":8443": true},
		Foreground: map[string]tailscale.ServeConfig{"session": {
			TCP: map[string]tailscale.TCPHandler{"9000": {TCPForward: "127.0.0.1:22"}},
		}},
	}, map[string]json.RawMessage{
		"Services": json.RawMessage(`{"svc:web":{"TCP":{"443":{"HTTPS":true}}}}`),
	}
}

// sameJSON reports whether a and b encode to the same JSON; the fake's
// state file indents what it keeps.
func sameJSON(a, b any) bool {
	encodedA, errA := json.Marshal(a)
	encodedB, errB := json.Marshal(b)
	return errA == nil && errB == nil && string(encodedA) == string(encodedB)
}

// Adding and removing OwnGit's endpoint keeps every other handler, Funnel
// setting and field as Tailscale had it, also fields OwnGit does not know.
func TestServeChangesKeepEverythingElse(t *testing.T) {
	serve, extra := ownersServe()
	fake := tailscaletest.New(t, tailscaletest.State{Status: tailscaletest.Running(), Serve: serve, ServeExtra: extra})
	command := fake.Command()
	ctx := context.Background()
	target := tailscale.Target(7654)
	config, err := command.ServeConfig(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := command.ServeHTTPS(ctx, config, tailscaletest.Name, 443, target); err != nil {
		t.Fatal(err)
	}
	after := fake.State()
	want, _ := ownersServe()
	want.Web[tailscaletest.Name+":443"] = tailscale.WebServer{Handlers: map[string]tailscale.Handler{"/": {Proxy: target}}}
	if !reflect.DeepEqual(after.Serve, want) || !sameJSON(after.ServeExtra, extra) {
		t.Fatalf("after adding: %+v %s", after.Serve, after.ServeExtra)
	}
	config, err = command.ServeConfig(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := command.RemoveHTTPS(ctx, config, tailscaletest.Name, 443); err != nil {
		t.Fatal(err)
	}
	// Port 443 keeps its TCP setting for the handler under the earlier name.
	after = fake.State()
	want, _ = ownersServe()
	if !reflect.DeepEqual(after.Serve, want) || !sameJSON(after.ServeExtra, extra) {
		t.Fatalf("after removing: %+v %s", after.Serve, after.ServeExtra)
	}
}

// A change applies only to the configuration it was made from: when
// anything changed Serve after OwnGit read it, Tailscale changes nothing,
// and the answer says so.
func TestServeChangesBindToTheConfigurationRead(t *testing.T) {
	target := tailscale.Target(7654)
	owners, _ := ownersServe()
	exact := tailscale.ServeConfig{
		TCP: map[string]tailscale.TCPHandler{"443": {HTTPS: true}},
		Web: map[string]tailscale.WebServer{tailscaletest.Name + ":443": {Handlers: map[string]tailscale.Handler{"/": {Proxy: target}}}},
	}
	for _, test := range []struct {
		name   string
		before tailscale.ServeConfig
		change func(tailscale.Command, tailscale.ServeConfig) error
	}{
		{"adding", tailscale.ServeConfig{}, func(command tailscale.Command, read tailscale.ServeConfig) error {
			return command.ServeHTTPS(context.Background(), read, tailscaletest.Name, 443, target)
		}},
		{"removing", exact, func(command tailscale.Command, read tailscale.ServeConfig) error {
			return command.RemoveHTTPS(context.Background(), read, tailscaletest.Name, 443)
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			fake := tailscaletest.New(t, tailscaletest.State{Status: tailscaletest.Running(), Serve: test.before})
			command := fake.Command()
			read, err := command.ServeConfig(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			fake.Update(func(s *tailscaletest.State) { s.Serve = owners })
			if err := test.change(command, read); tailscale.KindOf(err) != tailscale.KindServeChanged {
				t.Fatalf("err=%v, want %s", err, tailscale.KindServeChanged)
			}
			if serve := fake.State().Serve; !reflect.DeepEqual(serve, owners) {
				t.Fatalf("Tailscale has %+v, want the other change %+v", serve, owners)
			}
		})
	}
}

// Tailscale before 1.50 gives no version and would apply a change whatever
// happened meanwhile, so OwnGit reports it as outdated and never writes.
func TestServeWithoutVersionIsOutdated(t *testing.T) {
	fake := tailscaletest.New(t, tailscaletest.State{Status: tailscaletest.Running(), Unversioned: true})
	command := fake.Command()
	if _, err := command.ServeConfig(context.Background()); tailscale.KindOf(err) != tailscale.KindOutdated {
		t.Fatalf("read: err=%v, want %s", err, tailscale.KindOutdated)
	}
	// A configuration not read from Tailscale has nothing to bind to.
	if err := command.ServeHTTPS(context.Background(), tailscale.ServeConfig{}, tailscaletest.Name, 443, tailscale.Target(7654)); tailscale.KindOf(err) != tailscale.KindOutdated {
		t.Fatalf("write: err=%v, want %s", err, tailscale.KindOutdated)
	}
	if len(fake.Writes()) != 0 {
		t.Fatalf("writes=%q", fake.Writes())
	}
}

// A user who is not Tailscale's operator gets a permission problem, an error
// from Tailscale is kept as one short printable line, and a LocalAPI that
// does not answer is reported as not running.
func TestServeFailuresAreClassified(t *testing.T) {
	fake := tailscaletest.New(t, tailscaletest.State{Status: tailscaletest.Running(), WriteDenied: true})
	command := fake.Command()
	read, err := command.ServeConfig(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	err = command.ServeHTTPS(context.Background(), read, tailscaletest.Name, 443, tailscale.Target(7654))
	var failure *tailscale.Error
	if !errors.As(err, &failure) || failure.Kind != tailscale.KindPermission || !strings.Contains(failure.Detail, "serve config denied") {
		t.Fatalf("err=%#v", err)
	}
	fake.Update(func(state *tailscaletest.State) {
		state.WriteDenied, state.WriteError = false, "unexpected\x1b[31m failure\n"+strings.Repeat("x", 500)
	})
	err = command.ServeHTTPS(context.Background(), read, tailscaletest.Name, 443, tailscale.Target(7654))
	if !errors.As(err, &failure) || failure.Kind != tailscale.KindFailed || strings.ContainsAny(failure.Detail, "\x1b\n") || len(failure.Detail) > 310 {
		t.Fatalf("detail is not one short printable line: %q", failure.Detail)
	}
	closed := tailscale.Command{Path: fake.Path, LocalAPI: func(ctx context.Context) (net.Conn, string, error) {
		var dialer net.Dialer
		conn, err := dialer.DialContext(ctx, "tcp", "127.0.0.1:1")
		return conn, "", err
	}}
	if _, err := closed.ServeConfig(context.Background()); tailscale.KindOf(err) != tailscale.KindNotRunning {
		t.Fatalf("closed LocalAPI: err=%v", err)
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

// Only Tailscale's address ranges count as tailnet addresses. The status
// keeps every address Tailscale reports for this computer, also outside
// those ranges, as a control server such as Headscale can give.
func TestTailnetAddresses(t *testing.T) {
	for address, want := range map[string]bool{
		"100.64.0.1": true, "100.127.255.254": true, "::ffff:100.100.1.2": true, "fd7a:115c:a1e0::1": true, "fd7a:115c:a1e0:ab12::9": true,
		"100.63.255.255": false, "100.128.0.1": false, "192.168.1.5": false, "127.0.0.1": false, "fd7a:115c:a1e1::1": false, "::1": false,
	} {
		if got := tailscale.InTailnetRange(netip.MustParseAddr(address)); got != want {
			t.Errorf("%s: %v, want %v", address, got, want)
		}
	}
	state := tailscaletest.State{Status: tailscaletest.Running()}
	state.Status.Self.TailscaleIPs = []string{"100.64.0.7", "not an address", "10.1.2.3"}
	fake := tailscaletest.New(t, state)
	status, err := fake.Command().Status(context.Background())
	if err != nil || !reflect.DeepEqual(status.Addresses, []netip.Addr{netip.MustParseAddr("100.64.0.7"), netip.MustParseAddr("10.1.2.3")}) {
		t.Fatalf("addresses=%v err=%v", status.Addresses, err)
	}
}
