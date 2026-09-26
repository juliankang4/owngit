package server

import (
	"context"
	"errors"
	"fmt"
	"html"
	"net"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strconv"
	"strings"
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
			if want := []string{"serve --bg --https=8443 http://127.0.0.1:7654"}; !reflect.DeepEqual(fake.Writes(), want) {
				t.Fatalf("writes=%q, want %q", fake.Writes(), want)
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
			request := httptest.NewRequest(http.MethodGet, "/settings", nil)
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
			if change.Endpoint != "removed" || fake.Writes()[1] != "serve --https=8443 --set-path=/ off" {
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
	request := httptest.NewRequest(http.MethodGet, "/settings", nil)
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
	if want := []string{"serve --bg --https=4443 http://127.0.0.1:7654"}; !reflect.DeepEqual(fake.Writes(), want) {
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
	if change.Endpoint != "removed" || !reflect.DeepEqual(fake.Writes(), []string{"serve --https=443 --set-path=/ off"}) {
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
	if change.Endpoint != "removed" || fake.Writes()[len(fake.Writes())-1] != "serve --https=8443 --set-path=/ off" {
		t.Fatalf("off: %+v %q", change, fake.Writes())
	}
	if len(fake.State().Serve.Web[name+":8443"].Handlers) != 1 {
		t.Fatal("turning off under the new name removed the old name's entry")
	}
}
