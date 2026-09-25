package server

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"owngit/internal/auth"
	"owngit/internal/state"
	"owngit/internal/tailscale"
	"owngit/internal/tailscale/tailscaletest"
)

// tailscaleApp is a configured app that runs like serve with a live network
// on 127.0.0.1:7654, holds the running-record lock, and uses a fake
// tailscale that reports state.
func tailscaleApp(t *testing.T, fakeState tailscaletest.State) (*App, *tailscaletest.Fake) {
	t.Helper()
	return withTailscale(t, newConfiguredApp(t), fakeState)
}

func withTailscale(t *testing.T, app *App, fakeState tailscaletest.State) (*App, *tailscaletest.Fake) {
	t.Helper()
	fake := tailscaletest.New(t, fakeState)
	app.RunningRecordLive = true
	app.Network = NewLiveNetwork(LiveNetworkConfig{
		Record: state.RunningNetwork{
			Listen: "127.0.0.1:7654", Address: "127.0.0.1:7654", ListenSource: NetworkSourceDefault,
			Origin: "http://127.0.0.1:7654", BaseURLSource: NetworkSourceDefault,
			SavedHosts: []string{}, TrustedProxies: []string{}, TrustedProxiesSource: NetworkSourceDefault,
		},
		Hosts: app.Hosts,
		Publish: func(running state.RunningNetwork) {
			noErr(t, app.Store.PublishRunningNetwork(context.Background(), running))
		},
	})
	app.Network.Publish()
	app.Tailscale = &Tailscale{
		Store: app.Store,
		Find:  func() (tailscale.Command, error) { return tailscale.Find(fake.Path) },
		Observe: func(ctx context.Context) (state.RunningObservation, error) {
			return app.Store.OwnRunningNetwork(ctx, app.RunningRecordLive)
		},
		Live: app.Network,
	}
	return app, fake
}

// throughServe sends a request as Tailscale Serve passes it on: from
// 127.0.0.1, with the Host the client used and the forwarded headers Serve
// sets.
func throughServe(app *App, path string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodGet, path, nil)
	request.RemoteAddr = "127.0.0.1:50123"
	request.Host = tailscaletest.Name
	request.Header.Set("X-Forwarded-Host", tailscaletest.Name)
	request.Header.Set("X-Forwarded-Proto", "https")
	request.Header.Set("X-Forwarded-For", "100.64.0.9")
	response := httptest.NewRecorder()
	app.Handler().ServeHTTP(response, request)
	return response
}

// savedSharing returns the saved network settings and the sharing record.
func savedSharing(t *testing.T, store *state.Store) (state.NetworkSettings, []string, []string, *state.TailscaleServe) {
	t.Helper()
	settings, hosts, proxies := savedNetwork(t, store)
	record, on, err := store.TailscaleServe(context.Background())
	noErr(t, err)
	if !on {
		return settings, hosts, proxies, nil
	}
	return settings, hosts, proxies, &record
}

func noFunnelOrReset(t *testing.T, fake *tailscaletest.Fake) {
	t.Helper()
	for _, call := range fake.Calls() {
		if strings.Contains(call, "funnel") || strings.Contains(call, "reset") || strings.Contains(call, "sudo") {
			t.Fatalf("OwnGit ran %q", call)
		}
	}
}

func TestTurningTailscaleSharingOnAndOff(t *testing.T) {
	app, fake := tailscaleApp(t, tailscaletest.State{Status: tailscaletest.Running()})
	ctx := context.Background()
	if response := throughServe(app, "/"); response.Code != http.StatusMisdirectedRequest {
		t.Fatalf("before sharing, a request by the Tailscale name got %d", response.Code)
	}

	change, err := app.Tailscale.On(ctx, nil)
	noErr(t, err)
	if change.Endpoint != "created" || change.ListenChanged {
		t.Fatalf("change=%+v", change)
	}
	if want := []string{"serve --bg --https=443 http://127.0.0.1:7654"}; !reflect.DeepEqual(fake.Writes(), want) {
		t.Fatalf("writes=%q, want %q", fake.Writes(), want)
	}
	settings, hosts, proxies, record := savedSharing(t, app.Store)
	origin := "https://" + tailscaletest.Name
	if settings != (state.NetworkSettings{BaseURL: origin}) || !reflect.DeepEqual(hosts, []string{tailscaletest.Name}) || !reflect.DeepEqual(proxies, []string{"127.0.0.1"}) {
		t.Fatalf("saved settings=%+v hosts=%v proxies=%v", settings, hosts, proxies)
	}
	if record == nil || !record.Confirmed || !record.Created || record.Target != "http://127.0.0.1:7654" || record.HTTPSPort != 443 ||
		record.AddedProxy != "127.0.0.1" || record.AddedHost != tailscaletest.Name || record.PreviousBaseURL != "" || record.BaseURL != origin {
		t.Fatalf("record=%+v", record)
	}

	// The running server uses it at once: it accepts the name, treats the
	// request from Serve as HTTPS and shows the HTTPS clone address.
	report, err := app.Tailscale.Report(ctx)
	noErr(t, err)
	if !report.On || !report.Ready || report.Endpoint != TailscaleEndpointOwnGit || report.URL != origin+"/" || len(report.Waiting) != 0 {
		t.Fatalf("report=%+v", report)
	}
	network, err := app.networkReport(ctx)
	noErr(t, err)
	if network.RestartNeeded || network.Running.BaseURL != origin || !slices.Contains(network.Running.AcceptedHosts, tailscaletest.Name) {
		t.Fatalf("network report after turning on: %+v running=%+v", network, network.Running)
	}
	response := throughServe(app, "/")
	if response.Code != http.StatusOK {
		t.Fatalf("a request through Serve got %d", response.Code)
	}
	secure := false
	for _, cookie := range response.Result().Cookies() {
		secure = secure || (cookie.Name == generalCookie && cookie.Secure)
	}
	if !secure {
		t.Fatal("the session cookie of a request through Serve is not Secure")
	}
	cloneRequest := httptest.NewRequest(http.MethodGet, "/", nil)
	if clone := app.cloneURL(cloneRequest, "project"); clone != origin+"/git/project.git" {
		t.Fatalf("clone address=%q", clone)
	}

	change, err = app.Tailscale.Off(ctx)
	noErr(t, err)
	if change.Endpoint != "removed" {
		t.Fatalf("off change=%+v", change)
	}
	if want := []string{"serve --bg --https=443 http://127.0.0.1:7654", "serve --https=443 --set-path=/ off"}; !reflect.DeepEqual(fake.Writes(), want) {
		t.Fatalf("writes=%q, want %q", fake.Writes(), want)
	}
	settings, hosts, proxies, record = savedSharing(t, app.Store)
	if settings != (state.NetworkSettings{}) || len(hosts) != 0 || len(proxies) != 0 || record != nil {
		t.Fatalf("after off: settings=%+v hosts=%v proxies=%v record=%+v", settings, hosts, proxies, record)
	}
	if response := throughServe(app, "/"); response.Code != http.StatusMisdirectedRequest {
		t.Fatalf("after sharing is off, a request by the Tailscale name got %d", response.Code)
	}
	network, err = app.networkReport(ctx)
	noErr(t, err)
	if network.RestartNeeded || network.Running.BaseURL != "" || len(network.Running.TrustedProxies) != 0 {
		t.Fatalf("network report after turning off: %+v running=%+v", network, network.Running)
	}
	noFunnelOrReset(t, fake)
}

// Tailscale's HTTPS port used by anything else, Funnel included, is left
// alone and shown to the owner.
func TestTailscaleSharingRefusesAPortInUse(t *testing.T) {
	name := tailscaletest.Name
	cases := map[string]tailscale.ServeConfig{
		"another program": {
			TCP: map[string]tailscale.TCPHandler{"443": {HTTPS: true}},
			Web: map[string]tailscale.WebServer{name + ":443": {Handlers: map[string]tailscale.Handler{"/": {Proxy: "http://127.0.0.1:3000"}}}},
		},
		"OwnGit's address open to Funnel": {
			TCP:         map[string]tailscale.TCPHandler{"443": {HTTPS: true}},
			Web:         map[string]tailscale.WebServer{name + ":443": {Handlers: map[string]tailscale.Handler{"/": {Proxy: "http://127.0.0.1:7654"}}}},
			AllowFunnel: map[string]bool{name + ":443": true},
		},
	}
	for label, config := range cases {
		t.Run(label, func(t *testing.T) {
			app, fake := tailscaleApp(t, tailscaletest.State{Status: tailscaletest.Running(), Serve: config})
			_, err := app.Tailscale.On(context.Background(), nil)
			var refusal *TailscaleError
			if !errors.As(err, &refusal) || refusal.Problem != TailscaleProblemTaken || len(refusal.Found) == 0 {
				t.Fatalf("err=%v", err)
			}
			if len(fake.Writes()) != 0 {
				t.Fatalf("OwnGit changed Tailscale: %q", fake.Writes())
			}
			settings, hosts, proxies, record := savedSharing(t, app.Store)
			if settings != (state.NetworkSettings{}) || len(hosts) != 0 || len(proxies) != 0 || record != nil {
				t.Fatalf("a refusal saved something: %+v %v %v %+v", settings, hosts, proxies, record)
			}
			report, err := app.Tailscale.Report(context.Background())
			noErr(t, err)
			if report.Endpoint != TailscaleEndpointTaken || len(report.Found) == 0 {
				t.Fatalf("report=%+v", report)
			}
		})
	}
}

// An endpoint that already points at OwnGit exactly is used, and left in
// place when sharing is turned off, since OwnGit did not create it.
func TestTailscaleSharingLeavesAnEndpointItDidNotCreate(t *testing.T) {
	name := tailscaletest.Name
	app, fake := tailscaleApp(t, tailscaletest.State{Status: tailscaletest.Running(), Serve: tailscale.ServeConfig{
		TCP: map[string]tailscale.TCPHandler{"443": {HTTPS: true}},
		Web: map[string]tailscale.WebServer{name + ":443": {Handlers: map[string]tailscale.Handler{"/": {Proxy: "http://127.0.0.1:7654"}}}},
	}})
	ctx := context.Background()
	change, err := app.Tailscale.On(ctx, nil)
	noErr(t, err)
	if change.Endpoint != "kept" || change.Record.Created {
		t.Fatalf("change=%+v", change)
	}
	change, err = app.Tailscale.Off(ctx)
	noErr(t, err)
	if change.Endpoint != "left" || len(fake.Writes()) != 0 {
		t.Fatalf("change=%+v writes=%q", change, fake.Writes())
	}
	if config := fake.State().Serve; !config.Endpoint(name, 443, "http://127.0.0.1:7654").Exact {
		t.Fatalf("the endpoint OwnGit did not create is gone: %+v", config)
	}
	if settings, _, _, record := savedSharing(t, app.Store); settings.BaseURL != "" || record != nil {
		t.Fatalf("settings=%+v record=%+v", settings, record)
	}
}

// Turning off removes nothing when the endpoint changed after OwnGit made
// it, and cleans up OwnGit's settings when the owner removed it.
func TestTailscaleOffChangesNothingWhenTheEndpointChanged(t *testing.T) {
	app, fake := tailscaleApp(t, tailscaletest.State{Status: tailscaletest.Running()})
	ctx := context.Background()
	_, err := app.Tailscale.On(ctx, nil)
	noErr(t, err)
	key := tailscaletest.Name + ":443"
	fake.Update(func(fakeState *tailscaletest.State) {
		fakeState.Serve.Web[key].Handlers["/"] = tailscale.Handler{Proxy: "http://127.0.0.1:3000"}
	})
	report, err := app.Tailscale.Report(ctx)
	noErr(t, err)
	if report.Ready || report.Endpoint != TailscaleEndpointChanged || !slices.Contains(report.Waiting, TailscaleWaitEndpoint) {
		t.Fatalf("report after a change=%+v", report)
	}
	before, hostsBefore, proxiesBefore, recordBefore := savedSharing(t, app.Store)
	_, err = app.Tailscale.Off(ctx)
	var refusal *TailscaleError
	if !errors.As(err, &refusal) || refusal.Problem != TailscaleProblemChanged || !strings.Contains(TailscaleUsesText(refusal.Found), "127.0.0.1:3000") {
		t.Fatalf("err=%v", err)
	}
	if len(fake.Writes()) != 1 {
		t.Fatalf("OwnGit changed a foreign endpoint: %q", fake.Writes())
	}
	after, hostsAfter, proxiesAfter, recordAfter := savedSharing(t, app.Store)
	if after != before || !reflect.DeepEqual(hostsAfter, hostsBefore) || !reflect.DeepEqual(proxiesAfter, proxiesBefore) || !reflect.DeepEqual(recordAfter, recordBefore) {
		t.Fatal("a refused turn-off changed OwnGit's settings")
	}
	if response := throughServe(app, "/"); response.Code != http.StatusOK {
		t.Fatalf("a refused turn-off changed the running server: %d", response.Code)
	}

	// The owner removed the endpoint; turning off now takes back OwnGit's
	// settings without touching Tailscale.
	fake.Update(func(fakeState *tailscaletest.State) { fakeState.Serve = tailscale.ServeConfig{} })
	change, err := app.Tailscale.Off(ctx)
	noErr(t, err)
	if change.Endpoint != "gone" || len(fake.Writes()) != 1 {
		t.Fatalf("change=%+v writes=%q", change, fake.Writes())
	}
	if settings, hosts, proxies, record := savedSharing(t, app.Store); settings.BaseURL != "" || len(hosts) != 0 || len(proxies) != 0 || record != nil {
		t.Fatalf("after off: %+v %v %v %+v", settings, hosts, proxies, record)
	}
}

// Every problem with Tailscale is reported with its kind, and nothing is
// saved. A write that fails or is not kept leaves no record behind.
func TestTailscaleProblemsChangeNothing(t *testing.T) {
	cases := []struct {
		name   string
		change func(*tailscaletest.State)
		want   string
		writes int
	}{
		{"signed out", func(fakeState *tailscaletest.State) { fakeState.Status.BackendState = "NeedsLogin" }, string(tailscale.KindLoggedOut), 0},
		{"HTTPS off", func(fakeState *tailscaletest.State) { fakeState.Status.CertDomains = nil }, string(tailscale.KindHTTPSOff), 0},
		{"MagicDNS off", func(fakeState *tailscaletest.State) { fakeState.Status.CurrentTailnet.MagicDNSEnabled = false }, string(tailscale.KindMagicDNSOff), 0},
		{"daemon down", func(fakeState *tailscaletest.State) {
			fakeState.StatusError = "failed to connect to local Tailscale daemon; it doesn't appear to be running"
		}, string(tailscale.KindNotRunning), 0},
		{"not the operator", func(fakeState *tailscaletest.State) {
			fakeState.WriteError = "Access denied: serve config denied"
		}, string(tailscale.KindPermission), 1},
		{"change not kept", func(fakeState *tailscaletest.State) { fakeState.IgnoreWrites = true }, TailscaleProblemReadBack, 1},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			fakeState := tailscaletest.State{Status: tailscaletest.Running()}
			test.change(&fakeState)
			app, fake := tailscaleApp(t, fakeState)
			_, err := app.Tailscale.On(context.Background(), nil)
			var refusal *TailscaleError
			if !errors.As(err, &refusal) || refusal.Problem != test.want {
				t.Fatalf("err=%v, want problem %s", err, test.want)
			}
			if len(fake.Writes()) != test.writes {
				t.Fatalf("writes=%q", fake.Writes())
			}
			settings, hosts, proxies, record := savedSharing(t, app.Store)
			if settings != (state.NetworkSettings{}) || len(hosts) != 0 || len(proxies) != 0 || record != nil {
				t.Fatalf("a failure saved something: %+v %v %v %+v", settings, hosts, proxies, record)
			}
			if response := throughServe(app, "/"); response.Code != http.StatusMisdirectedRequest {
				t.Fatalf("a failure changed the running server: %d", response.Code)
			}
			noFunnelOrReset(t, fake)
		})
	}
	t.Run("not installed", func(t *testing.T) {
		app, _ := tailscaleApp(t, tailscaletest.State{Status: tailscaletest.Running()})
		app.Tailscale.Find = func() (tailscale.Command, error) { return tailscale.Command{}, tailscale.ErrNotInstalled }
		_, err := app.Tailscale.On(context.Background(), nil)
		var refusal *TailscaleError
		if !errors.As(err, &refusal) || refusal.Problem != string(tailscale.KindNotInstalled) {
			t.Fatalf("err=%v", err)
		}
		report, err := app.Tailscale.Report(context.Background())
		noErr(t, err)
		if report.Installed || report.Problem != string(tailscale.KindNotInstalled) {
			t.Fatalf("report=%+v", report)
		}
	})
}

// Settings saved before sharing stay after it: a base URL comes back, and a
// proxy or name that was already saved is kept.
func TestTailscaleSharingTakesBackOnlyWhatItAdded(t *testing.T) {
	app, _ := tailscaleApp(t, tailscaletest.State{Status: tailscaletest.Running()})
	ctx := context.Background()
	noErr(t, app.Store.UpdateNetwork(ctx, state.NetworkUpdate{
		Settings:   state.NetworkSettings{BaseURL: "http://gitbox.lan:7654"},
		AddHosts:   []string{strings.ToUpper(tailscaletest.Name)},
		AddProxies: []string{"127.0.0.0/8"},
	}))
	change, err := app.Tailscale.On(ctx, nil)
	noErr(t, err)
	if change.Record.AddedProxy != "" || change.Record.AddedHost != "" || change.Record.PreviousBaseURL != "http://gitbox.lan:7654" {
		t.Fatalf("record=%+v", change.Record)
	}
	_, err = app.Tailscale.Off(ctx)
	noErr(t, err)
	settings, hosts, proxies, _ := savedSharing(t, app.Store)
	if settings.BaseURL != "http://gitbox.lan:7654" || len(hosts) != 1 || !reflect.DeepEqual(proxies, []string{"127.0.0.0/8"}) {
		t.Fatalf("after off: %+v %v %v", settings, hosts, proxies)
	}

	// A base URL the owner changed while sharing was on is left alone.
	_, err = app.Tailscale.On(ctx, nil)
	noErr(t, err)
	noErr(t, app.Store.UpdateNetwork(ctx, state.NetworkUpdate{Settings: state.NetworkSettings{BaseURL: "http://other.lan:7654"}}))
	_, err = app.Tailscale.Off(ctx)
	noErr(t, err)
	if settings, _, _, _ := savedSharing(t, app.Store); settings.BaseURL != "http://other.lan:7654" {
		t.Fatalf("base URL=%q", settings.BaseURL)
	}
}

// Turning sharing on keeps the listen address unless the owner chooses, or
// Tailscale cannot reach it; nothing opens the home network by itself.
func TestTailscaleListenChoice(t *testing.T) {
	yes, no := true, false
	cases := []struct {
		listen string
		home   *bool
		want   string
	}{
		{"127.0.0.1:7654", nil, "127.0.0.1:7654"},
		{"localhost:7654", nil, "localhost:7654"},
		{"0.0.0.0:7654", nil, "0.0.0.0:7654"},
		{":7654", nil, ":7654"},
		{"100.64.0.7:7654", nil, "127.0.0.1:7654"},
		{"192.168.1.20:7700", nil, "127.0.0.1:7700"},
		{"127.0.0.1:7654", &yes, "0.0.0.0:7654"},
		{"0.0.0.0:7654", &yes, "0.0.0.0:7654"},
		{"100.64.0.7:7654", &yes, "0.0.0.0:7654"},
		{"0.0.0.0:7654", &no, "127.0.0.1:7654"},
		{"127.0.0.1:7654", &no, "127.0.0.1:7654"},
		{"[::1]:7654", &no, "127.0.0.1:7654"},
	}
	for _, test := range cases {
		if got := planListen(test.listen, test.home); got != test.want {
			t.Errorf("planListen(%q, %v) = %q, want %q", test.listen, test.home, got, test.want)
		}
	}

	app, fake := tailscaleApp(t, tailscaletest.State{Status: tailscaletest.Running()})
	ctx := context.Background()
	noErr(t, app.Store.UpdateNetwork(ctx, state.NetworkUpdate{Settings: state.NetworkSettings{Listen: "100.64.0.7:7700"}}))
	change, err := app.Tailscale.On(ctx, nil)
	noErr(t, err)
	if !change.ListenChanged || change.Listen != "127.0.0.1:7700" || !slices.Contains(fake.Writes(), "serve --bg --https=443 http://127.0.0.1:7700") {
		t.Fatalf("change=%+v writes=%q", change, fake.Writes())
	}
	if settings, _, _, _ := savedSharing(t, app.Store); settings.Listen != "127.0.0.1:7700" {
		t.Fatalf("saved listen=%q", settings.Listen)
	}
	// The running server still listens on 7654, so the new port needs a
	// restart.
	report, err := app.Tailscale.Report(ctx)
	noErr(t, err)
	if report.Ready || !slices.Contains(report.Waiting, TailscaleWaitRestart) {
		t.Fatalf("report=%+v", report)
	}

	// Choosing the home network is an explicit acknowledgement of plain
	// HTTP there.
	unacknowledged, store, root := newTestApp(t)
	noErr(t, store.CompleteSetup(ctx, root, "open", "", "synthetic-admin-hash", false))
	app, _ = withTailscale(t, unacknowledged, tailscaletest.State{Status: tailscaletest.Running()})
	change, err = app.Tailscale.On(ctx, &yes)
	noErr(t, err)
	if change.Listen != "0.0.0.0:7654" {
		t.Fatalf("change=%+v", change)
	}
	if settings, err := app.Store.Settings(ctx); err != nil || !settings.InsecureHTTPAccepted {
		t.Fatalf("the home network choice was not recorded as accepting plain HTTP: %+v %v", settings, err)
	}
}

// Readiness comes from what the running server uses, through the same rule
// as "owngit network show": a change made outside the server waits for a
// restart, and a server that cannot be confirmed is never called ready.
func TestTailscaleReadinessFollowsTheRunningServer(t *testing.T) {
	app, _ := tailscaleApp(t, tailscaletest.State{Status: tailscaletest.Running()})
	ctx := context.Background()
	// As "owngit tailscale on" does from another process: settings and
	// Tailscale change, the running server does not.
	app.Tailscale.Live = nil
	_, err := app.Tailscale.On(ctx, nil)
	noErr(t, err)
	report, err := app.Tailscale.Report(ctx)
	noErr(t, err)
	if report.Ready || !reflect.DeepEqual(report.Waiting, []string{TailscaleWaitRestart}) {
		t.Fatalf("report=%+v", report)
	}
	app.RunningRecordLive = false
	report, err = app.Tailscale.Report(ctx)
	noErr(t, err)
	if report.Ready || !reflect.DeepEqual(report.Waiting, []string{TailscaleWaitUnknown}) {
		t.Fatalf("unconfirmed server: %+v", report)
	}
	app.RunningRecordLive = true
	// A server that trusts proxies only by a start option is not reached.
	flagged := NewLiveNetwork(LiveNetworkConfig{
		Record: state.RunningNetwork{
			Listen: "127.0.0.1:7654", Address: "127.0.0.1:7654", ListenSource: NetworkSourceDefault, BaseURLSource: NetworkSourceDefault,
			TrustedProxies: []string{}, TrustedProxiesSource: NetworkSourceFlag,
		},
		Hosts:   app.Hosts,
		Publish: func(running state.RunningNetwork) { noErr(t, app.Store.PublishRunningNetwork(ctx, running)) },
	})
	app.Network = flagged
	record, _, err := app.Store.TailscaleServe(ctx)
	noErr(t, err)
	flagged.ApplyTailscale(record)
	report, err = app.Tailscale.Report(ctx)
	noErr(t, err)
	if report.Ready || !reflect.DeepEqual(report.Waiting, []string{TailscaleWaitOption}) {
		t.Fatalf("start option: %+v", report)
	}
	if response := throughServe(app, "/"); response.Code != http.StatusOK || strings.Contains(response.Header().Get("Set-Cookie"), "Secure") {
		t.Fatalf("a server started with --trusted-proxy trusted 127.0.0.1 anyway: %d %q", response.Code, response.Header().Get("Set-Cookie"))
	}
}

// OwnGit never answers a request that Tailscale forwarded from the public
// Internet through Funnel, and Tailscale's identity headers grant nothing.
func TestTailscaleHeadersGrantNothing(t *testing.T) {
	app, _ := tailscaleApp(t, tailscaletest.State{Status: tailscaletest.Running()})
	ctx := context.Background()
	_, err := app.Tailscale.On(ctx, nil)
	noErr(t, err)
	hash, err := auth.HashPassword("shared-password-for-tests")
	noErr(t, err)
	noErr(t, app.Store.SetAccessPassword(ctx, hash))

	send := func(path string, header map[string]string) *httptest.ResponseRecorder {
		request := httptest.NewRequest(http.MethodGet, path, nil)
		request.RemoteAddr = "127.0.0.1:50123"
		request.Host = tailscaletest.Name
		request.Header.Set("X-Forwarded-Proto", "https")
		for name, value := range header {
			request.Header.Set(name, value)
		}
		response := httptest.NewRecorder()
		app.Handler().ServeHTTP(response, request)
		return response
	}
	for _, path := range []string{"/", "/settings", "/api/v1/repositories", "/git/project.git/info/refs?service=git-upload-pack"} {
		if response := send(path, map[string]string{"Tailscale-Funnel-Request": "?1"}); response.Code != http.StatusForbidden {
			t.Errorf("Funnel request to %s: status=%d", path, response.Code)
		}
	}
	identity := map[string]string{
		"Tailscale-User-Login": "owner@example.com", "Tailscale-User-Name": "Owner",
		"Tailscale-User-Profile-Pic": "https://example.com/p.png", "Tailscale-App-Capabilities": `{"example.com/cap/admin":[{}]}`,
	}
	if response := send("/settings", identity); response.Code != http.StatusSeeOther || !strings.HasPrefix(response.Header().Get("Location"), "/login") {
		t.Errorf("identity headers opened Settings: %d %q", response.Code, response.Header().Get("Location"))
	}
	if response := send("/api/v1/repositories", identity); response.Code != http.StatusUnauthorized {
		t.Errorf("identity headers opened the API: %d", response.Code)
	}
	if response := send("/git/project.git/info/refs?service=git-upload-pack", identity); response.Code != http.StatusUnauthorized {
		t.Errorf("identity headers opened Git: %d", response.Code)
	}
}

// Turning on again after a turning on that was interrupted between the
// Tailscale write and the settings save keeps the owner's base URL for
// turning off. (Regression test from the security review.)
func TestTurningOnAfterAnInterruptionKeepsThePreviousBaseURL(t *testing.T) {
	app, fake := tailscaleApp(t, tailscaletest.State{Status: tailscaletest.Running()})
	ctx := context.Background()
	noErr(t, app.Store.UpdateNetwork(ctx, state.NetworkUpdate{Settings: state.NetworkSettings{BaseURL: "http://gitbox.lan:7654"}}))
	// What turning on leaves behind when the process dies right after the
	// write: an unconfirmed record and OwnGit's endpoint.
	target := tailscale.Target(7654)
	noErr(t, app.Store.SaveTailscaleServe(ctx, state.TailscaleServe{Name: tailscaletest.Name, HTTPSPort: 443, Target: target, Created: true}))
	fake.Update(func(s *tailscaletest.State) {
		s.Serve = tailscale.ServeConfig{
			TCP: map[string]tailscale.TCPHandler{"443": {HTTPS: true}},
			Web: map[string]tailscale.WebServer{tailscaletest.Name + ":443": {Handlers: map[string]tailscale.Handler{"/": {Proxy: target}}}},
		}
	})
	change, err := app.Tailscale.On(ctx, nil)
	noErr(t, err)
	if !change.Record.Created || change.Record.AddedProxy != loopbackProxy || change.Record.AddedHost != tailscaletest.Name {
		t.Fatalf("record after the retry: %+v", change.Record)
	}
	_, err = app.Tailscale.Off(ctx)
	noErr(t, err)
	settings, hosts, proxies, record := savedSharing(t, app.Store)
	if settings.BaseURL != "http://gitbox.lan:7654" || len(hosts) != 0 || len(proxies) != 0 || record != nil {
		t.Fatalf("after interruption, retry and off: base URL %q, hosts %v, proxies %v, record %+v", settings.BaseURL, hosts, proxies, record)
	}
}

// A request cancelled halfway, such as by a closed browser tab, does not
// leave the change half done.
func TestACancelledRequestStillFinishesTheChange(t *testing.T) {
	app, fake := tailscaleApp(t, tailscaletest.State{Status: tailscaletest.Running()})
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := app.Tailscale.On(cancelled, nil)
	noErr(t, err)
	if _, _, _, record := savedSharing(t, app.Store); record == nil || !record.Confirmed {
		t.Fatalf("record after a cancelled request: %+v", record)
	}
	_, err = app.Tailscale.Off(cancelled)
	noErr(t, err)
	if len(fake.Writes()) != 2 {
		t.Fatalf("writes=%q", fake.Writes())
	}
}

// Overlapping changes, such as a double click on "Turn on", run one after
// the other, so turning off still takes back the proxy and name that
// turning on added. (After a case from the security review.)
func TestOverlappingChangesKeepWhatOwnGitAdded(t *testing.T) {
	app, fake := tailscaleApp(t, tailscaletest.State{Status: tailscaletest.Running(), WriteDelay: 300})
	ctx := context.Background()
	var wait sync.WaitGroup
	errs := make([]error, 2)
	for i := range errs {
		wait.Add(1)
		go func() {
			defer wait.Done()
			time.Sleep(time.Duration(i) * 100 * time.Millisecond)
			_, errs[i] = app.Tailscale.On(ctx, nil)
		}()
	}
	wait.Wait()
	for _, err := range errs {
		noErr(t, err)
	}
	if _, _, _, record := savedSharing(t, app.Store); record == nil || record.AddedProxy != loopbackProxy || record.AddedHost != tailscaletest.Name {
		t.Fatalf("record after overlapping turning on: %+v", record)
	}
	fake.Update(func(s *tailscaletest.State) { s.WriteDelay = 0 })
	_, err := app.Tailscale.Off(ctx)
	noErr(t, err)
	_, hosts, proxies, record := savedSharing(t, app.Store)
	if record != nil || len(hosts) != 0 || len(proxies) != 0 {
		t.Fatalf("after overlapping on and off: hosts=%v proxies=%v record=%+v", hosts, proxies, record)
	}
	if slices.ContainsFunc(app.Network.Resolver().TrustedProxies, func(prefix netip.Prefix) bool { return prefix.Contains(netip.MustParseAddr(loopbackProxy)) }) {
		t.Fatal("the running server still trusts 127.0.0.1 after turning off")
	}
}

// Another process changing sharing, such as "owngit tailscale on", holds
// the state directory's lock, and a change here waits for it.
func TestAChangeWaitsForAnotherProcess(t *testing.T) {
	app, fake := tailscaleApp(t, tailscaletest.State{Status: tailscaletest.Running()})
	release, err := app.Store.LockTailscaleChange(context.Background())
	noErr(t, err)
	done := make(chan error, 1)
	go func() {
		_, err := app.Tailscale.On(context.Background(), nil)
		done <- err
	}()
	time.Sleep(300 * time.Millisecond)
	if calls := fake.Calls(); len(calls) != 0 {
		release()
		t.Fatalf("the change ran while another process held the lock: %q", calls)
	}
	release()
	select {
	case err := <-done:
		noErr(t, err)
	case <-time.After(20 * time.Second):
		t.Fatal("the change did not continue after the lock was released")
	}
}
