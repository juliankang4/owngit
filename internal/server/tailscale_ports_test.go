package server

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"html"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"owngit/internal/state"
	"owngit/internal/tailscale"
	"owngit/internal/tailscale/tailscaletest"
	"owngit/internal/webui"
)

// otherService adds to config a web handler of another program on each of
// ports, for name.
func otherService(config tailscale.ServeConfig, name string, ports ...int) tailscale.ServeConfig {
	if config.TCP == nil {
		config.TCP = map[string]tailscale.TCPHandler{}
	}
	if config.Web == nil {
		config.Web = map[string]tailscale.WebServer{}
	}
	for _, port := range ports {
		text := strconv.Itoa(port)
		config.TCP[text] = tailscale.TCPHandler{HTTPS: true}
		config.Web[name+":"+text] = tailscale.WebServer{Handlers: map[string]tailscale.Handler{"/": {Proxy: "http://127.0.0.1:3000"}}}
	}
	return config
}

// portEntries returns what config has on port: its TCP setting, the web
// handlers of every name there and whether Funnel is open on it.
func portEntries(config tailscale.ServeConfig, port int) map[string]any {
	text := strconv.Itoa(port)
	entries := map[string]any{"tcp": config.TCP[text]}
	for key, server := range config.Web {
		if _, p, _ := net.SplitHostPort(key); p == text {
			entries["web "+key] = server
		}
	}
	for key, open := range config.AllowFunnel {
		if _, p, _ := net.SplitHostPort(key); p == text {
			entries["funnel "+key] = open
		}
	}
	return entries
}

// With HTTPS port 443 used by something else, whatever it is, turning on
// uses 8443 without asking, leaves 443 exactly as it was, and turning off
// removes only its own entry. The address, clone addresses and the allowed
// Host all carry the port.
func TestTailscaleSharingUsesAnotherPortWhen443IsTaken(t *testing.T) {
	name := tailscaletest.Name
	cases := map[string]tailscale.ServeConfig{
		"a web handler": otherService(tailscale.ServeConfig{}, name, 443),
		"TCP forwarding": {
			TCP: map[string]tailscale.TCPHandler{"443": {TCPForward: "127.0.0.1:22"}},
		},
		"Funnel": {
			TCP:         map[string]tailscale.TCPHandler{"443": {HTTPS: true}},
			Web:         map[string]tailscale.WebServer{name + ":443": {Handlers: map[string]tailscale.Handler{"/": {Proxy: "http://127.0.0.1:7654"}}}},
			AllowFunnel: map[string]bool{name + ":443": true},
		},
	}
	for label, config := range cases {
		t.Run(label, func(t *testing.T) {
			app, fake := tailscaleApp(t, tailscaletest.State{Status: tailscaletest.Running(), Serve: config})
			ctx := context.Background()
			before := portEntries(fake.State().Serve, 443)

			report, err := app.Tailscale.Report(ctx)
			noErr(t, err)
			if !report.CanTurnOn || report.Endpoint != TailscaleEndpointFree || report.TurnOnPort != 8443 ||
				!reflect.DeepEqual(report.TurnOnPassed, []int{443}) || report.PortsTaken {
				t.Fatalf("report before=%+v", report)
			}
			change, err := app.Tailscale.On(ctx, nil, 0)
			noErr(t, err)
			if change.Endpoint != "created" || change.Record.HTTPSPort != 8443 || !reflect.DeepEqual(change.PassedPorts, []int{443}) {
				t.Fatalf("change=%+v", change)
			}
			if len(fake.Writes()) != 1 || !fake.Endpoint(8443, "http://127.0.0.1:7654").Exact {
				t.Fatalf("writes=%q", fake.Writes())
			}
			origin := "https://" + name + ":8443"
			settings, hosts, _, record := savedSharing(t, app.Store)
			if settings.BaseURL != origin || !reflect.DeepEqual(hosts, []string{name}) || record == nil || record.HTTPSPort != 8443 || record.BaseURL != origin {
				t.Fatalf("saved %+v %v %+v", settings, hosts, record)
			}
			report, err = app.Tailscale.Report(ctx)
			noErr(t, err)
			if !report.Ready || report.URL != origin+"/" || report.Endpoint != TailscaleEndpointOwnGit {
				t.Fatalf("report after=%+v", report)
			}
			// Tailscale Serve passes on the Host the client used, with the
			// port.
			request := httptest.NewRequest(http.MethodGet, "/settings/network", nil)
			request.RemoteAddr, request.Host = "127.0.0.1:50123", name+":8443"
			request.Header.Set("X-Forwarded-Host", name+":8443")
			request.Header.Set("X-Forwarded-Proto", "https")
			response := httptest.NewRecorder()
			app.Handler().ServeHTTP(response, request)
			if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), webui.Text(webui.LangEN, webui.MsgConnTailscaleOn)) {
				t.Fatalf("a request through Serve on 8443 got %d, or no Tailscale label", response.Code)
			}
			if clone := app.cloneURL(httptest.NewRequest(http.MethodGet, "/", nil), "project"); clone != origin+"/git/project.git" {
				t.Fatalf("clone address=%q", clone)
			}

			change, err = app.Tailscale.Off(ctx)
			noErr(t, err)
			if change.Endpoint != "removed" || len(fake.Writes()) != 2 || !fake.Endpoint(8443, "").Free {
				t.Fatalf("off change=%+v writes=%q", change, fake.Writes())
			}
			if after := portEntries(fake.State().Serve, 443); !reflect.DeepEqual(after, before) {
				t.Fatalf("port 443 changed:\nbefore %+v\nafter  %+v", before, after)
			}
			if settings, hosts, proxies, record := savedSharing(t, app.Store); settings.BaseURL != "" || len(hosts) != 0 || len(proxies) != 0 || record != nil {
				t.Fatalf("after off: %+v %v %v %+v", settings, hosts, proxies, record)
			}
			noFunnelOrReset(t, fake)
		})
	}
}

// When 8443 is also taken, 10000 is next; when all three are taken,
// turning on refuses, lists what is on each and writes nothing.
func TestTailscaleSharingPortOrder(t *testing.T) {
	name := tailscaletest.Name
	app, fake := tailscaleApp(t, tailscaletest.State{Status: tailscaletest.Running(), Serve: otherService(tailscale.ServeConfig{}, name, 443, 8443)})
	ctx := context.Background()
	report, err := app.Tailscale.Report(ctx)
	noErr(t, err)
	if info := tailscaleInfo(report); info.TurnOnPort != "10000" || info.PassedPorts != "443, 8443" || info.PortNote != webui.MsgTSPortsNote {
		t.Fatalf("Settings block before turning on=%+v", info)
	}
	request := httptest.NewRequest(http.MethodGet, "/settings/network", nil)
	request.RemoteAddr, request.Host = "127.0.0.1:50123", "127.0.0.1:7654"
	response := httptest.NewRecorder()
	app.Handler().ServeHTTP(response, request)
	if note := html.EscapeString(fmt.Sprintf(webui.Text(webui.LangEN, webui.MsgTSPortsNote), "443, 8443", "10000")); !strings.Contains(response.Body.String(), note) {
		t.Fatalf("the Settings page (%d) lacks %q", response.Code, note)
	}
	change, err := app.Tailscale.On(ctx, nil, 0)
	noErr(t, err)
	if change.Record.HTTPSPort != 10000 || !reflect.DeepEqual(change.PassedPorts, []int{443, 8443}) {
		t.Fatalf("change=%+v", change)
	}
	_, err = app.Tailscale.Off(ctx)
	noErr(t, err)

	fake.Update(func(s *tailscaletest.State) { s.Serve = otherService(s.Serve, name, 10000) })
	writes := len(fake.Writes())
	report, err = app.Tailscale.Report(ctx)
	noErr(t, err)
	if report.CanTurnOn || !report.PortsTaken || report.Endpoint != TailscaleEndpointTaken || len(report.Found) != 3 {
		t.Fatalf("report with every port taken=%+v", report)
	}
	_, err = app.Tailscale.On(ctx, nil, 0)
	var refusal *TailscaleError
	if !errors.As(err, &refusal) || refusal.Problem != TailscaleProblemTaken || len(refusal.Found) != 3 {
		t.Fatalf("err=%v", err)
	}
	if len(fake.Writes()) != writes {
		t.Fatalf("a refusal wrote %q", fake.Writes()[writes:])
	}
	if settings, hosts, proxies, record := savedSharing(t, app.Store); settings != (state.NetworkSettings{}) || len(hosts) != 0 || len(proxies) != 0 || record != nil {
		t.Fatalf("a refusal saved %+v %v %v %+v", settings, hosts, proxies, record)
	}
	if response := throughServe(app, "/"); response.Code != http.StatusMisdirectedRequest {
		t.Fatalf("a refusal changed the running server: %d", response.Code)
	}
	info := tailscaleInfo(report)
	if len(info.Found) != 3 || info.FoundFix == "" || info.FoundFixPort != "" || info.CanTurnOn {
		t.Fatalf("Settings block=%+v", info)
	}
}

// A port the owner names is used as it is, even when 443 is free, and is
// refused without trying another when something else uses it. Sharing
// that is on moves to another named port only after turning off.
func TestTailscaleSharingOnANamedPort(t *testing.T) {
	name := tailscaletest.Name
	app, fake := tailscaleApp(t, tailscaletest.State{Status: tailscaletest.Running(), Serve: otherService(tailscale.ServeConfig{}, name, 9443)})
	ctx := context.Background()
	_, err := app.Tailscale.On(ctx, nil, 9443)
	var refusal *TailscaleError
	if !errors.As(err, &refusal) || refusal.Problem != TailscaleProblemTaken || len(fake.Writes()) != 0 {
		t.Fatalf("a named port in use: err=%v writes=%q", err, fake.Writes())
	}
	change, err := app.Tailscale.On(ctx, nil, 4443)
	noErr(t, err)
	if change.Record.HTTPSPort != 4443 || len(change.PassedPorts) != 0 || change.Record.BaseURL != "https://"+name+":4443" {
		t.Fatalf("change=%+v", change)
	}
	_, err = app.Tailscale.On(ctx, nil, 443)
	if !errors.As(err, &refusal) || refusal.Problem != TailscaleProblemOtherPort || refusal.Detail != "https://"+name+":4443/" {
		t.Fatalf("moving while on: err=%v", err)
	}
	if len(fake.Writes()) != 1 || !fake.Endpoint(4443, "http://127.0.0.1:7654").Exact {
		t.Fatalf("writes=%q", fake.Writes())
	}
	// The same port again changes nothing.
	change, err = app.Tailscale.On(ctx, nil, 4443)
	noErr(t, err)
	if change.Endpoint != "kept" || len(fake.Writes()) != 1 {
		t.Fatalf("again: %+v %q", change, fake.Writes())
	}
}

// A record written by 1.1.0, which always used 443, keeps working: it is
// ready, turning on again keeps it, and turning off removes it.
func TestTailscaleSharingRecordOfAnEarlierVersion(t *testing.T) {
	name := tailscaletest.Name
	target := "http://127.0.0.1:7654"
	app, fake := tailscaleApp(t, tailscaletest.State{Status: tailscaletest.Running(), Serve: tailscale.ServeConfig{
		TCP: map[string]tailscale.TCPHandler{"443": {HTTPS: true}},
		Web: map[string]tailscale.WebServer{name + ":443": {Handlers: map[string]tailscale.Handler{"/": {Proxy: target}}}},
	}})
	ctx := context.Background()
	// The record as 1.1.0 saved it, and the settings it added.
	noErr(t, app.Store.UpdateNetwork(ctx, state.NetworkUpdate{
		Settings: state.NetworkSettings{BaseURL: "https://" + name}, AddHosts: []string{name}, AddProxies: []string{loopbackProxy},
		Tailscale: &state.TailscaleServe{
			Name: name, HTTPSPort: 443, Target: target, Created: true, Confirmed: true, CreatedAt: 1,
			BaseURL: "https://" + name, AddedProxy: loopbackProxy, AddedHost: name,
		},
	}))
	app.Network.ApplyTailscale(state.TailscaleServe{Name: name, HTTPSPort: 443, BaseURL: "https://" + name}, "")
	report, err := app.Tailscale.Report(ctx)
	noErr(t, err)
	if !report.Ready || report.URL != "https://"+name+"/" || report.CanTurnOn || !report.CanTurnOff {
		t.Fatalf("report=%+v", report)
	}
	change, err := app.Tailscale.On(ctx, nil, 0)
	noErr(t, err)
	if change.Endpoint != "kept" || change.Record.HTTPSPort != 443 || len(fake.Writes()) != 0 {
		t.Fatalf("on again: %+v %q", change, fake.Writes())
	}
	change, err = app.Tailscale.Off(ctx)
	noErr(t, err)
	if change.Endpoint != "removed" || len(fake.Writes()) != 1 || !fake.Endpoint(443, "").Free {
		t.Fatalf("off: %+v %q", change, fake.Writes())
	}
}

// After a rename, sharing on 8443 moves to the new name on the same port,
// and the old name's entry shows as an address under an earlier name.
// While the new name's ports are all taken, turning on again is not
// offered, and the page lists what is there.
func TestTailscaleSharingOnAnotherPortAfterARename(t *testing.T) {
	name := tailscaletest.Name
	app, fake := tailscaleApp(t, tailscaletest.State{Status: tailscaletest.Running(), Serve: otherService(tailscale.ServeConfig{}, name, 443)})
	ctx := context.Background()
	_, err := app.Tailscale.On(ctx, nil, 0)
	noErr(t, err)
	rename(fake)

	// Every port the new name could use is taken.
	fake.Update(func(s *tailscaletest.State) { s.Serve = otherService(s.Serve, renamed, 443, 8443, 10000) })
	report, err := app.Tailscale.Report(ctx)
	noErr(t, err)
	if report.CanTurnOn || !report.PortsTaken || len(report.Found) != 3 || !slices.Contains(report.Waiting, TailscaleWaitName) {
		t.Fatalf("with the new name's ports taken: %+v", report)
	}
	if info := tailscaleInfo(report); info.CanTurnOn || len(info.Found) != 3 || info.FoundNote == "" {
		t.Fatalf("Settings block=%+v", info)
	}
	writes := len(fake.Writes())
	_, err = app.Tailscale.On(ctx, nil, 0)
	var refusal *TailscaleError
	if !errors.As(err, &refusal) || refusal.Problem != TailscaleProblemTaken || len(fake.Writes()) != writes {
		t.Fatalf("turning on: err=%v writes=%q", err, fake.Writes()[writes:])
	}

	// With the new name's 8443 free again, turning on uses it.
	fake.Update(func(s *tailscaletest.State) {
		delete(s.Serve.Web, renamed+":8443")
	})
	report, err = app.Tailscale.Report(ctx)
	noErr(t, err)
	if !report.CanTurnOn || report.TurnOnPort != 8443 || report.PortsTaken {
		t.Fatalf("with 8443 free: %+v", report)
	}
	change, err := app.Tailscale.On(ctx, nil, 0)
	noErr(t, err)
	if change.Endpoint != "created" || change.Record.Name != renamed || change.Record.HTTPSPort != 8443 || change.Record.BaseURL != "https://"+renamed+":8443" {
		t.Fatalf("after the rename: %+v", change)
	}
	report, err = app.Tailscale.Report(ctx)
	noErr(t, err)
	stale := slices.ContainsFunc(report.Stale, func(use tailscale.Use) bool { return use.Address == "https://"+name+":8443/" })
	if !report.Ready || !stale {
		t.Fatalf("report=%+v", report)
	}
	change, err = app.Tailscale.Off(ctx)
	noErr(t, err)
	if change.Endpoint != "removed" || !fake.Endpoint(8443, "").Free {
		t.Fatalf("off: %+v %q", change, fake.Writes())
	}
	if len(fake.State().Serve.Web[name+":8443"].Handlers) != 1 {
		t.Fatal("turning off under the new name removed the old name's entry")
	}
}

// A turning on that was interrupted after Tailscale made OwnGit's address
// on 8443 left a record that was never confirmed. Naming another port then
// is refused as for sharing that is on, because a record for the new port
// would replace the only record of the address on 8443. Turning off
// removes that address, and the named port works afterwards. Without the
// address, the named port is used at once.
func TestTailscaleSharingNamedPortAfterAnInterruptedTurningOn(t *testing.T) {
	name := tailscaletest.Name
	target := "http://127.0.0.1:7654"
	interrupted := state.TailscaleServe{Name: name, HTTPSPort: 8443, Target: target, Created: true, CreatedAt: 1}
	madeOn8443 := tailscale.ServeConfig{
		TCP: map[string]tailscale.TCPHandler{"8443": {HTTPS: true}},
		Web: map[string]tailscale.WebServer{name + ":8443": {Handlers: map[string]tailscale.Handler{"/": {Proxy: target}}}},
	}
	ctx := context.Background()

	app, fake := tailscaleApp(t, tailscaletest.State{Status: tailscaletest.Running(), Serve: madeOn8443})
	noErr(t, app.Store.SaveTailscaleServe(ctx, interrupted))
	report, err := app.Tailscale.Report(ctx)
	noErr(t, err)
	if !report.On || !report.CanTurnOff || report.Endpoint != TailscaleEndpointOwnGit || !slices.Contains(report.Waiting, TailscaleWaitUnfinished) {
		t.Fatalf("report of the interrupted turning on=%+v", report)
	}
	_, err = app.Tailscale.On(ctx, nil, 10000)
	var refusal *TailscaleError
	if !errors.As(err, &refusal) || refusal.Problem != TailscaleProblemOtherPort || refusal.Detail != "https://"+name+":8443/" || len(fake.Writes()) != 0 {
		t.Fatalf("another named port: err=%v writes=%q", err, fake.Writes())
	}
	if _, _, _, record := savedSharing(t, app.Store); record == nil || *record != interrupted {
		t.Fatalf("the refusal changed the record: %+v", record)
	}
	change, err := app.Tailscale.Off(ctx)
	noErr(t, err)
	if change.Endpoint != "removed" || len(fake.Writes()) != 1 || !fake.Endpoint(8443, "").Free {
		t.Fatalf("off: %+v %q", change, fake.Writes())
	}
	change, err = app.Tailscale.On(ctx, nil, 10000)
	noErr(t, err)
	if change.Endpoint != "created" || change.Record.HTTPSPort != 10000 {
		t.Fatalf("on after off: %+v", change)
	}

	// The interrupted turning on left no address.
	app, fake = tailscaleApp(t, tailscaletest.State{Status: tailscaletest.Running()})
	noErr(t, app.Store.SaveTailscaleServe(ctx, interrupted))
	change, err = app.Tailscale.On(ctx, nil, 10000)
	noErr(t, err)
	if change.Endpoint != "created" || change.Record.HTTPSPort != 10000 || len(fake.Writes()) != 1 || !fake.Endpoint(10000, target).Exact {
		t.Fatalf("with no address left: %+v %q", change, fake.Writes())
	}
}

// OwnGit's address on a named port under an earlier name is shown after
// turning off, which leaves it because Tailscale removes it only under that
// name, although no record names the port any more.
func TestTailscaleSharingShowsAnEarlierNameOnANamedPort(t *testing.T) {
	name := tailscaletest.Name
	app, fake := tailscaleApp(t, tailscaletest.State{Status: tailscaletest.Running()})
	ctx := context.Background()
	_, err := app.Tailscale.On(ctx, nil, 4443)
	noErr(t, err)
	rename(fake)
	change, err := app.Tailscale.Off(ctx)
	noErr(t, err)
	if change.Endpoint != "stale" {
		t.Fatalf("off after the rename: %+v", change)
	}
	report, err := app.Tailscale.Report(ctx)
	noErr(t, err)
	if report.On || !slices.ContainsFunc(report.Stale, func(use tailscale.Use) bool { return use.Address == "https://"+name+":4443/" }) {
		t.Fatalf("report after off=%+v", report)
	}
}

// When anything else changes Tailscale's Serve configuration after turning
// on or off read it, at any moment before Tailscale applies OwnGit's change,
// Tailscale applies nothing: OwnGit reports the other change, keeps the
// sharing record as it was, and leaves what the other program put there,
// handlers and Funnel alike.
func TestTailscaleChangesStopWhenServeChangesMeanwhile(t *testing.T) {
	ctx := context.Background()
	other := otherService(tailscale.ServeConfig{AllowFunnel: map[string]bool{tailscaletest.Name + ":443": true}}, tailscaletest.Name, 443)
	for _, test := range []struct {
		name string
		on   bool
		// meanwhile changes Serve after OwnGit's read.
		meanwhile func(*Tailscale, *tailscaletest.Fake)
	}{
		{"on, before OwnGit asks", true, func(sharing *Tailscale, fake *tailscaletest.Fake) {
			sharing.BeforeServe = func(string) { fake.Update(func(s *tailscaletest.State) { s.Serve = other }) }
		}},
		{"on, as Tailscale gets the change", true, func(_ *Tailscale, fake *tailscaletest.Fake) {
			fake.Update(func(s *tailscaletest.State) { s.ChangedBeforeWrite = &other })
		}},
		{"off, as Tailscale gets the change", false, func(_ *Tailscale, fake *tailscaletest.Fake) {
			fake.Update(func(s *tailscaletest.State) { s.ChangedBeforeWrite = &other })
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			app, fake := tailscaleApp(t, tailscaletest.State{Status: tailscaletest.Running()})
			if !test.on {
				_, err := app.Tailscale.On(ctx, nil, 0)
				noErr(t, err)
			}
			_, _, _, before := savedSharing(t, app.Store)
			writes := len(fake.Writes())
			test.meanwhile(app.Tailscale, fake)
			var err error
			if test.on {
				_, err = app.Tailscale.On(ctx, nil, 0)
			} else {
				_, err = app.Tailscale.Off(ctx)
			}
			var refusal *TailscaleError
			if !errors.As(err, &refusal) || refusal.Problem != TailscaleProblemServeChanged || errors.Is(err, ErrTailscaleAhead) {
				t.Fatalf("err=%v, want the refusal %s", err, TailscaleProblemServeChanged)
			}
			if serve := fake.State().Serve; !reflect.DeepEqual(serve, other) || len(fake.Writes()) != writes+1 {
				t.Fatalf("Tailscale has %+v after %q, want the other change %+v", serve, fake.Writes()[writes:], other)
			}
			if _, _, _, after := savedSharing(t, app.Store); !reflect.DeepEqual(after, before) {
				t.Fatalf("the sharing record is %+v, want %+v", after, before)
			}
		})
	}
}

// Tailscale checks a change before it applies anything, so a change it
// refuses, here for a user who may not change Serve, was not applied,
// whatever someone else did meanwhile: turning on does not take a matching
// endpoint that another user made as OwnGit's, and turning off keeps
// sharing on when another user removed the endpoint.
func TestTailscaleRefusedChangeWasNotAppliedWhateverTheReadBackShows(t *testing.T) {
	ctx := context.Background()
	target := tailscale.Target(7654)
	exact := tailscale.ServeConfig{
		TCP: map[string]tailscale.TCPHandler{"443": {HTTPS: true}},
		Web: map[string]tailscale.WebServer{tailscaletest.Name + ":443": {Handlers: map[string]tailscale.Handler{"/": {Proxy: target}}}},
	}
	for _, test := range []struct {
		name      string
		on        bool
		meanwhile tailscale.ServeConfig
	}{
		{"on, another user adds the same endpoint", true, exact},
		{"off, another user removes the endpoint", false, tailscale.ServeConfig{}},
	} {
		t.Run(test.name, func(t *testing.T) {
			app, fake := tailscaleApp(t, tailscaletest.State{Status: tailscaletest.Running()})
			if !test.on {
				_, err := app.Tailscale.On(ctx, nil, 0)
				noErr(t, err)
			}
			_, _, _, before := savedSharing(t, app.Store)
			meanwhile := test.meanwhile
			fake.Update(func(s *tailscaletest.State) { s.ChangedBeforeWrite, s.WriteDenied = &meanwhile, true })
			var err error
			if test.on {
				_, err = app.Tailscale.On(ctx, nil, 0)
			} else {
				_, err = app.Tailscale.Off(ctx)
			}
			var refusal *TailscaleError
			if !errors.As(err, &refusal) || refusal.Problem != string(tailscale.KindPermission) || errors.Is(err, ErrTailscaleAhead) {
				t.Fatalf("err=%v, want the permission refusal alone", err)
			}
			if _, _, _, after := savedSharing(t, app.Store); !reflect.DeepEqual(after, before) {
				t.Fatalf("the sharing record is %+v, want %+v", after, before)
			}
			if serve := fake.State().Serve; !reflect.DeepEqual(serve, meanwhile) {
				t.Fatalf("Tailscale has %+v, want the other user's %+v", serve, meanwhile)
			}
		})
	}
}

// answerLostConn passes a Serve change to the fake LocalAPI, which applies
// it, then stops Tailscale and loses the answer, as when Tailscale goes down
// right after it applied a change. It does so once, while armed.
type answerLostConn struct {
	net.Conn
	fake  *tailscaletest.Fake
	armed *atomic.Bool
	post  bool
}

func (conn *answerLostConn) Write(content []byte) (int, error) {
	if bytes.HasPrefix(content, []byte("POST ")) {
		conn.post = true
	}
	return conn.Conn.Write(content)
}

func (conn *answerLostConn) Read(content []byte) (int, error) {
	if !conn.post || !conn.armed.CompareAndSwap(true, false) {
		return conn.Conn.Read(content)
	}
	if response, err := http.ReadResponse(bufio.NewReader(conn.Conn), nil); err == nil {
		io.Copy(io.Discard, response.Body)
		response.Body.Close()
	}
	conn.fake.Update(func(s *tailscaletest.State) { s.Status.BackendState = "Stopped" })
	return 0, io.ErrUnexpectedEOF
}

// A change whose answer was lost may be in Tailscale even when Tailscale
// is stopped afterwards: the stopped state explains the failure but is not
// Tailscale refusing the change. Turning on keeps its pending record, which
// turning on again confirms, and turning off says the change may be there.
// Tailscale said nothing, so the refusal has no detail; the lost connection
// is named only in the error's text, which the log shows.
func TestTailscaleChangeWithALostAnswerMayBeApplied(t *testing.T) {
	ctx := context.Background()
	app, fake := tailscaleApp(t, tailscaletest.State{Status: tailscaletest.Running()})
	var armed atomic.Bool
	app.Tailscale.Find = func() (tailscale.Command, error) {
		command := fake.Command()
		dial := command.LocalAPI
		command.LocalAPI = func(ctx context.Context) (net.Conn, string, error) {
			conn, password, err := dial(ctx)
			if err != nil {
				return nil, "", err
			}
			return &answerLostConn{Conn: conn, fake: fake, armed: &armed}, password, nil
		}
		return command, nil
	}
	target := tailscale.Target(7654)
	running := func() { fake.Update(func(s *tailscaletest.State) { s.Status.BackendState = "Running" }) }

	armed.Store(true)
	_, err := app.Tailscale.On(ctx, nil, 0)
	if !errors.Is(err, ErrTailscaleAhead) || !fake.Endpoint(443, target).Exact {
		t.Fatalf("turning on: err=%v, endpoint %+v", err, fake.Endpoint(443, target))
	}
	var refusal *TailscaleError
	if !errors.As(err, &refusal) || refusal.Problem != string(tailscale.KindStopped) || refusal.Detail != "" || !strings.Contains(err.Error(), io.ErrUnexpectedEOF.Error()) {
		t.Fatalf("turning on: %#v, text %q", refusal, err)
	}
	if _, _, _, record := savedSharing(t, app.Store); record == nil || !record.Created || record.Confirmed {
		t.Fatalf("record after a lost answer: %+v", record)
	}
	running()
	change, err := app.Tailscale.On(ctx, nil, 0)
	if err != nil || change.Endpoint != endpointKept || !change.Record.Confirmed {
		t.Fatalf("turning on again: %+v %v", change, err)
	}

	armed.Store(true)
	_, err = app.Tailscale.Off(ctx)
	if !errors.Is(err, ErrTailscaleAhead) || fake.Endpoint(443, target).Exact {
		t.Fatalf("turning off: err=%v, endpoint %+v", err, fake.Endpoint(443, target))
	}
	running()
	if _, err := app.Tailscale.Off(ctx); err != nil {
		t.Fatalf("turning off again: %v", err)
	}
	if _, _, _, record := savedSharing(t, app.Store); record != nil {
		t.Fatalf("record after turning off: %+v", record)
	}
}
