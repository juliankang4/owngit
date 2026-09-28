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

	"owngit/internal/state"
	"owngit/internal/tailscale"
	"owngit/internal/tailscale/tailscaletest"
	"owngit/internal/webui"
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
	// A page may have started a reading of Tailscale's addresses in the
	// background (Tailscale.addresses). It must end before the fake does,
	// or it would run the fake of a later test.
	t.Cleanup(func() { finishAddressReading(t, app.Tailscale) })
	return app, fake
}

// finishAddressReading waits for the reading of Tailscale's addresses that
// a page started in the background, if one runs.
func finishAddressReading(t *testing.T, sharing *Tailscale) {
	t.Helper()
	sharing.readings.mu.Lock()
	done := sharing.readings.refreshed
	sharing.readings.mu.Unlock()
	if done == nil {
		return
	}
	select {
	case <-done:
	case <-time.After(time.Minute):
		t.Error("the background reading of Tailscale's addresses did not finish")
	}
}

// throughServe sends a request as Tailscale Serve passes it on: from
// 127.0.0.1, with the Host the client used and the forwarded headers Serve
// sets.
func throughServe(app *App, path string) *httptest.ResponseRecorder {
	return sendThroughServe(app, httptest.NewRequest(http.MethodGet, path, nil))
}

// sendThroughServe sends request as Tailscale Serve passes one on.
func sendThroughServe(app *App, request *http.Request) *httptest.ResponseRecorder {
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

	change, err := app.Tailscale.On(ctx, nil, 0)
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

// An endpoint that points at OwnGit exactly but that OwnGit has no record
// of making, such as one left by an interrupted change or a rename back, is
// treated as taken: the report says so with removal steps, and turning on
// refuses and writes nothing. A record that says OwnGit did not create its
// endpoint (kept by an earlier version) still leaves it when turning off.
func TestAnUnrecordedOwnGitEndpointIsTreatedAsTaken(t *testing.T) {
	name := tailscaletest.Name
	exact := tailscale.ServeConfig{
		TCP: map[string]tailscale.TCPHandler{"443": {HTTPS: true}},
		Web: map[string]tailscale.WebServer{name + ":443": {Handlers: map[string]tailscale.Handler{"/": {Proxy: "http://127.0.0.1:7654"}}}},
	}
	app, fake := tailscaleApp(t, tailscaletest.State{Status: tailscaletest.Running(), Serve: exact})
	ctx := context.Background()
	report, err := app.Tailscale.Report(ctx)
	noErr(t, err)
	info := tailscaleInfo(report)
	if report.Endpoint != TailscaleEndpointUnrecorded || report.CanTurnOn || len(report.Found) != 1 ||
		info.FoundNote != webui.MsgTSUnrecorded || info.FoundFix != webui.MsgTSRemoveSteps {
		t.Fatalf("report=%+v info=%+v", report, info)
	}
	_, err = app.Tailscale.On(ctx, nil, 0)
	var refusal *TailscaleError
	if !errors.As(err, &refusal) || refusal.Problem != TailscaleProblemUnrecorded || len(fake.Writes()) != 0 {
		t.Fatalf("err=%v writes=%q", err, fake.Writes())
	}
	if _, _, _, record := savedSharing(t, app.Store); record != nil {
		t.Fatalf("a refusal saved %+v", record)
	}

	noErr(t, app.Store.UpdateNetwork(ctx, state.NetworkUpdate{Tailscale: &state.TailscaleServe{
		Name: name, HTTPSPort: 443, Target: "http://127.0.0.1:7654", Confirmed: true, BaseURL: "https://" + name,
	}}))
	change, err := app.Tailscale.Off(ctx)
	noErr(t, err)
	if change.Endpoint != "left" || len(fake.Writes()) != 0 || !fake.State().Serve.Endpoint(name, 443, "http://127.0.0.1:7654").Exact {
		t.Fatalf("change=%+v writes=%q", change, fake.Writes())
	}
}

// Turning off removes nothing when the endpoint changed after OwnGit made
// it, and cleans up OwnGit's settings when the owner removed it.
func TestTailscaleOffChangesNothingWhenTheEndpointChanged(t *testing.T) {
	app, fake := tailscaleApp(t, tailscaletest.State{Status: tailscaletest.Running()})
	ctx := context.Background()
	_, err := app.Tailscale.On(ctx, nil, 0)
	noErr(t, err)
	key := tailscaletest.Name + ":443"
	fake.Update(func(fakeState *tailscaletest.State) {
		fakeState.Serve.Web[key].Handlers["/"] = tailscale.Handler{Proxy: "http://127.0.0.1:3000"}
	})
	report, err := app.Tailscale.Report(ctx)
	noErr(t, err)
	if report.Ready || report.Endpoint != TailscaleEndpointChanged || !slices.Contains(report.Waiting, TailscaleWaitChanged) || report.CanTurnOn || report.CanTurnOff {
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
		{"another control server", func(fakeState *tailscaletest.State) {
			fakeState.Status.Self.DNSName, fakeState.Status.CertDomains = "gitbox.headscale.internal.", nil
		}, string(tailscale.KindHTTPSUnavailable), 0},
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
			_, err := app.Tailscale.On(context.Background(), nil, 0)
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
		_, err := app.Tailscale.On(context.Background(), nil, 0)
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
	change, err := app.Tailscale.On(ctx, nil, 0)
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
	_, err = app.Tailscale.On(ctx, nil, 0)
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
	change, err := app.Tailscale.On(ctx, nil, 0)
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
	change, err = app.Tailscale.On(ctx, &yes, 0)
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
	_, err := app.Tailscale.On(ctx, nil, 0)
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
	flagged.ApplyTailscale(record, "")
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
	_, err := app.Tailscale.On(ctx, nil, 0)
	noErr(t, err)
	hash := fixturePasswordHash(t, "shared-password-for-tests")
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
	change, err := app.Tailscale.On(ctx, nil, 0)
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
	_, err := app.Tailscale.On(cancelled, nil, 0)
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
			_, errs[i] = app.Tailscale.On(ctx, nil, 0)
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
		_, err := app.Tailscale.On(context.Background(), nil, 0)
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

const renamed = "newbox.tail0000.ts.net"

// rename makes the fake computer take a new name, as a rename in the
// Tailscale admin console does. Serve keeps its handlers under the old name.
func rename(fake *tailscaletest.Fake) {
	fake.Update(func(s *tailscaletest.State) {
		s.Status.Self.DNSName, s.Status.CertDomains = renamed+".", []string{renamed}
	})
}

// After a rename, turning off cannot remove the endpoint under the old name
// ("tailscale serve" works only under the current name), so it takes back
// OwnGit's settings and says how to remove the endpoint. (Tailscale issue
// 16992.)
func TestTurningOffAfterARenameTakesBackTheSettings(t *testing.T) {
	app, fake := tailscaleApp(t, tailscaletest.State{Status: tailscaletest.Running()})
	ctx := context.Background()
	noErr(t, app.Store.UpdateNetwork(ctx, state.NetworkUpdate{Settings: state.NetworkSettings{BaseURL: "http://gitbox.lan:7654"}}))
	_, err := app.Tailscale.On(ctx, nil, 0)
	noErr(t, err)
	rename(fake)
	report, err := app.Tailscale.Report(ctx)
	noErr(t, err)
	if report.Ready || !reflect.DeepEqual(report.Waiting, []string{TailscaleWaitName}) || len(report.Stale) != 1 || !tailscaleInfo(report).CanTurnOn {
		t.Fatalf("after the rename: %+v", report)
	}
	change, err := app.Tailscale.Off(ctx)
	noErr(t, err)
	if change.Endpoint != "stale" {
		t.Fatalf("endpoint=%q, want stale", change.Endpoint)
	}
	if writes := fake.Writes(); len(writes) != 1 {
		t.Fatalf("turning off after the rename wrote to Tailscale: %q", writes)
	}
	settings, hosts, proxies, record := savedSharing(t, app.Store)
	if settings.BaseURL != "http://gitbox.lan:7654" || len(hosts) != 0 || len(proxies) != 0 || record != nil {
		t.Fatalf("after off: base URL %q, hosts %v, proxies %v, record %+v", settings.BaseURL, hosts, proxies, record)
	}
	report, err = app.Tailscale.Report(ctx)
	noErr(t, err)
	if report.On || report.Endpoint != TailscaleEndpointFree || len(report.Stale) != 1 || report.Stale[0].Address != "https://"+tailscaletest.Name+":443/" {
		t.Fatalf("report after off: %+v", report)
	}
	// Turning on again uses the new name beside the old endpoint.
	change, err = app.Tailscale.On(ctx, nil, 0)
	noErr(t, err)
	if change.Endpoint != "created" || change.Record.Name != renamed {
		t.Fatalf("turning on after the rename: %+v", change)
	}
	if report, err = app.Tailscale.Report(ctx); err != nil || !report.Ready {
		t.Fatalf("ready after turning on with the new name: %+v %v", report, err)
	}
}

// After a rename, turning on again moves sharing to the new name: a new
// endpoint, the new base URL and allowed name, and the old name that OwnGit
// had added is no longer accepted.
func TestTurningOnAfterARenameMovesToTheNewName(t *testing.T) {
	app, fake := tailscaleApp(t, tailscaletest.State{Status: tailscaletest.Running()})
	ctx := context.Background()
	_, err := app.Tailscale.On(ctx, nil, 0)
	noErr(t, err)
	rename(fake)
	change, err := app.Tailscale.On(ctx, nil, 0)
	noErr(t, err)
	if change.Endpoint != "created" || change.Record.Name != renamed || change.RemovedHost != tailscaletest.Name {
		t.Fatalf("turning on after the rename: %+v", change)
	}
	settings, hosts, proxies, record := savedSharing(t, app.Store)
	if settings.BaseURL != "https://"+renamed || !reflect.DeepEqual(hosts, []string{renamed}) || !reflect.DeepEqual(proxies, []string{loopbackProxy}) ||
		record == nil || record.AddedHost != renamed || record.AddedProxy != loopbackProxy {
		t.Fatalf("after moving: base URL %q, hosts %v, proxies %v, record %+v", settings.BaseURL, hosts, proxies, record)
	}
	if app.Hosts.Allows(tailscaletest.Name, remotePeer) || !app.Hosts.Allows(renamed, remotePeer) || app.Network.TailscaleName() != renamed {
		t.Fatal("the running server does not follow the new name")
	}
	report, err := app.Tailscale.Report(ctx)
	noErr(t, err)
	if !report.Ready || report.Endpoint != TailscaleEndpointOwnGit || len(report.Stale) != 1 {
		t.Fatalf("report after moving: %+v", report)
	}
	change, err = app.Tailscale.Off(ctx)
	noErr(t, err)
	if change.Endpoint != "removed" {
		t.Fatalf("turning off: %+v", change)
	}
	if _, hosts, proxies, record := savedSharing(t, app.Store); len(hosts) != 0 || len(proxies) != 0 || record != nil {
		t.Fatalf("after off: hosts %v, proxies %v, record %+v", hosts, proxies, record)
	}
}

// After a rename, an endpoint for the new name that is exactly what OwnGit
// would make, but that OwnGit did not write, is used without taking it
// over: turning off leaves it, and it then shows as not made by OwnGit.
func TestTurningOnAfterARenameDoesNotTakeOverAnEndpointForTheNewName(t *testing.T) {
	app, fake := tailscaleApp(t, tailscaletest.State{Status: tailscaletest.Running()})
	ctx := context.Background()
	first, err := app.Tailscale.On(ctx, nil, 0)
	noErr(t, err)
	rename(fake)
	fake.Update(func(s *tailscaletest.State) {
		s.Serve.Web[renamed+":443"] = tailscale.WebServer{Handlers: map[string]tailscale.Handler{"/": {Proxy: first.Record.Target}}}
	})
	before := len(fake.Writes())
	change, err := app.Tailscale.On(ctx, nil, 0)
	noErr(t, err)
	if change.Endpoint != "kept" || change.Record.Created || change.Record.Name != renamed || len(fake.Writes()) != before {
		t.Fatalf("turning on after the rename: %+v, writes %q", change, fake.Writes()[before:])
	}
	change, err = app.Tailscale.Off(ctx)
	noErr(t, err)
	if change.Endpoint != "left" || len(fake.Writes()) != before {
		t.Fatalf("turning off: %+v, writes %q", change, fake.Writes()[before:])
	}
	if !fake.State().Serve.Endpoint(renamed, 443, first.Record.Target).Exact {
		t.Fatal("turning off removed an endpoint that OwnGit did not write")
	}
	report, err := app.Tailscale.Report(ctx)
	noErr(t, err)
	if report.On || report.Endpoint != TailscaleEndpointUnrecorded || report.CanTurnOn {
		t.Fatalf("report after off: %+v", report)
	}
}

// reads counts the fake's read commands.
func reads(fake *tailscaletest.Fake) int {
	count := 0
	for _, call := range fake.Calls() {
		if call == "status --json" || call == "serve status --json" {
			count++
		}
	}
	return count
}

// Concurrent reports, such as many Settings views at once, share one
// reading of Tailscale and reuse it for a few seconds, so the number of
// tailscale processes does not follow the request rate. A change reads
// again.
func TestReportsShareOneReadingOfTailscale(t *testing.T) {
	app, fake := tailscaleApp(t, tailscaletest.State{Status: tailscaletest.Running(), ReadDelay: 300})
	ctx := context.Background()
	var wait sync.WaitGroup
	for range 40 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			report, err := app.Tailscale.Report(ctx)
			if err != nil || report.Name != tailscaletest.Name {
				t.Errorf("report: %+v %v", report, err)
			}
		}()
	}
	wait.Wait()
	if got := reads(fake); got != 2 {
		t.Fatalf("40 concurrent reports ran %d read commands, want 2: %q", got, fake.Calls())
	}
	_, err := app.Tailscale.Report(ctx)
	noErr(t, err)
	if got := reads(fake); got != 2 {
		t.Fatalf("a report within the reading's lifetime ran tailscale again: %d", got)
	}
	fake.Update(func(s *tailscaletest.State) { s.ReadDelay = 0 })
	_, err = app.Tailscale.On(ctx, nil, 0)
	noErr(t, err)
	before := reads(fake)
	report, err := app.Tailscale.Report(ctx)
	noErr(t, err)
	if reads(fake) != before+2 || !report.Ready {
		t.Fatalf("the report after turning on did not read again: %d reads, %+v", reads(fake)-before, report)
	}
}

// Many Settings views at once, from viewers without the administrator
// password, start at most one reading.
func TestConcurrentSettingsViewsStartOneReading(t *testing.T) {
	app, fake := tailscaleApp(t, tailscaletest.State{Status: tailscaletest.Running(), ReadDelay: 300})
	client, base, _, _ := networkSettingsClient(t, app)
	app.Tailscale.forget()
	before := len(fake.Calls())
	var wait sync.WaitGroup
	for range 20 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			if _, status := dashboardGET(t, client, base+"/settings/network"); status != http.StatusOK {
				t.Errorf("status=%d", status)
			}
		}()
	}
	wait.Wait()
	if got := len(fake.Calls()) - before; got > 2 {
		t.Fatalf("20 concurrent Settings views ran tailscale %d times, want at most 2", got)
	}
}

// With "serve --listen", the option decides where the server listens: the
// home network choice changes nothing, so it is not offered, does not count
// as accepting plain HTTP, and the page names the option's address instead
// of the saved port.
func TestAListenOptionDecidesInsteadOfTheHomeNetworkChoice(t *testing.T) {
	unacknowledged, store, root := newTestApp(t)
	ctx := context.Background()
	noErr(t, store.CompleteSetup(ctx, root, "open", "", "synthetic-admin-hash", false))
	app, fake := withTailscale(t, unacknowledged, tailscaletest.State{Status: tailscaletest.Running()})
	app.Network = NewLiveNetwork(LiveNetworkConfig{
		Record: state.RunningNetwork{
			Listen: "127.0.0.1:7890", Address: "127.0.0.1:7890", ListenSource: NetworkSourceFlag,
			Origin: "http://127.0.0.1:7890", BaseURLSource: NetworkSourceDefault,
			SavedHosts: []string{}, TrustedProxies: []string{}, TrustedProxiesSource: NetworkSourceDefault,
		},
		Hosts:   app.Hosts,
		Publish: func(running state.RunningNetwork) { noErr(t, app.Store.PublishRunningNetwork(ctx, running)) },
	})
	app.Network.Publish()
	app.Tailscale.Live = app.Network

	report, err := app.Tailscale.Report(ctx)
	noErr(t, err)
	info := tailscaleInfo(report)
	if report.ListenOption != "127.0.0.1:7890" || info.ListenOption != "127.0.0.1:7890" || info.HomeListen != "" {
		t.Fatalf("report=%+v info=%+v", report, info)
	}
	_, _, _, page := networkSettingsClient(t, app)
	block := page[strings.Index(page, `id="grp-tailscale"`):]
	block = block[:strings.Index(block, "</section>")]
	if on, _, _ := tailscaleOffers(page); !on || strings.Contains(block, `name="home_network"`) || !strings.Contains(block, "--listen 127.0.0.1:7890") ||
		strings.Contains(block, "7654") {
		t.Fatal("the Tailscale block offers the home network choice or names the saved port under a --listen option")
	}

	yes := true
	change, err := app.Tailscale.On(ctx, &yes, 0)
	noErr(t, err)
	if change.ListenChanged || change.ListenOption != "127.0.0.1:7890" || !slices.Contains(fake.Writes(), "serve --bg --https=443 http://127.0.0.1:7890") {
		t.Fatalf("change=%+v writes=%q", change, fake.Writes())
	}
	settings, err := app.Store.Settings(ctx)
	noErr(t, err)
	if saved, _, _, _ := savedSharing(t, app.Store); saved.Listen != "" || settings.InsecureHTTPAccepted {
		t.Fatalf("a choice that changed nothing saved listen %q or accepted plain HTTP (%v)", saved.Listen, settings.InsecureHTTPAccepted)
	}
}

// refuseConfirmedSharing makes every save of a confirmed sharing record
// fail, so turning on writes its endpoint and then cannot save its settings.
// The returned function lets saves succeed again.
func refuseConfirmedSharing(t *testing.T, store *state.Store) func() {
	t.Helper()
	noErr(t, store.Exec(context.Background(), `CREATE TRIGGER refuse_confirmed_insert BEFORE INSERT ON metadata WHEN NEW.key='tailscale_serve' AND json_extract(NEW.value,'$.confirmed') BEGIN SELECT RAISE(ABORT, 'injected failure'); END;
CREATE TRIGGER refuse_confirmed_update BEFORE UPDATE ON metadata WHEN NEW.key='tailscale_serve' AND json_extract(NEW.value,'$.confirmed') BEGIN SELECT RAISE(ABORT, 'injected failure'); END`))
	return func() {
		noErr(t, store.Exec(context.Background(), `DROP TRIGGER refuse_confirmed_insert; DROP TRIGGER refuse_confirmed_update`))
	}
}

// Turning sharing on again over a confirmed record, after a rename or after
// the endpoint disappeared, writes a new endpoint. When its settings then
// cannot be saved, that endpoint is still recorded as OwnGit's, so the next
// turning on finishes the change and the next turning off removes it.
func TestTurningOnAgainThatCouldNotBeSavedIsFinishedNextTime(t *testing.T) {
	for _, situation := range []struct {
		name string
		// prepare turns sharing on and then changes Tailscale so that
		// turning on again writes a new endpoint; it returns the name the
		// endpoint is written for.
		prepare func(t *testing.T, app *App, fake *tailscaletest.Fake) string
	}{
		{"after a rename", func(t *testing.T, app *App, fake *tailscaletest.Fake) string {
			_, err := app.Tailscale.On(context.Background(), nil, 0)
			noErr(t, err)
			rename(fake)
			return renamed
		}},
		{"after the owner's endpoint that OwnGit used disappeared", func(t *testing.T, app *App, fake *tailscaletest.Fake) string {
			// After a rename OwnGit uses the owner's endpoint for the new
			// name without taking it over (Created false), and then that
			// endpoint disappears.
			first, err := app.Tailscale.On(context.Background(), nil, 0)
			noErr(t, err)
			rename(fake)
			fake.Update(func(s *tailscaletest.State) {
				s.Serve.Web[renamed+":443"] = tailscale.WebServer{Handlers: map[string]tailscale.Handler{"/": {Proxy: first.Record.Target}}}
			})
			change, err := app.Tailscale.On(context.Background(), nil, 0)
			noErr(t, err)
			if change.Endpoint != endpointKept || change.Record.Created {
				t.Fatalf("turning on over the owner's endpoint: %+v", change)
			}
			fake.Update(func(s *tailscaletest.State) { delete(s.Serve.Web, renamed+":443") })
			return renamed
		}},
	} {
		for _, next := range []string{"on", "off"} {
			t.Run(situation.name+", then "+next, func(t *testing.T) {
				app, fake := tailscaleApp(t, tailscaletest.State{Status: tailscaletest.Running()})
				ctx := context.Background()
				name := situation.prepare(t, app, fake)
				allow := refuseConfirmedSharing(t, app.Store)
				if _, err := app.Tailscale.On(ctx, nil, 0); !errors.Is(err, ErrTailscaleAhead) {
					t.Fatalf("turning on with a settings save that fails: %v", err)
				}
				allow()
				_, _, _, record := savedSharing(t, app.Store)
				if record == nil || record.Confirmed || !record.Created || record.Name != name {
					t.Fatalf("the written endpoint is not recorded as OwnGit's: %+v", record)
				}
				target := record.Target
				if next == "on" {
					change, err := app.Tailscale.On(ctx, nil, 0)
					noErr(t, err)
					if change.Endpoint != endpointKept || !change.Record.Created || !change.Record.Confirmed || change.Record.Name != name {
						t.Fatalf("turning on again: %+v", change)
					}
				}
				change, err := app.Tailscale.Off(ctx)
				noErr(t, err)
				if change.Endpoint != "removed" {
					t.Fatalf("turning off: %+v", change)
				}
				settings, hosts, proxies, record := savedSharing(t, app.Store)
				if settings.BaseURL != "" || len(hosts) != 0 || len(proxies) != 0 || record != nil {
					t.Fatalf("after off: base URL %q, hosts %v, proxies %v, record %+v", settings.BaseURL, hosts, proxies, record)
				}
				if !fake.State().Serve.Endpoint(name, 443, target).Free {
					t.Fatal("turning off left the endpoint OwnGit wrote")
				}
				report, err := app.Tailscale.Report(ctx)
				noErr(t, err)
				if report.On || report.Endpoint != TailscaleEndpointFree || !report.CanTurnOn {
					t.Fatalf("report after off: %+v", report)
				}
			})
		}
	}
}

// Turning on again that Tailscale refused wrote nothing, so the confirmed
// record stays as it was.
func TestTurningOnAgainThatTailscaleRefusedKeepsTheRecord(t *testing.T) {
	app, fake := tailscaleApp(t, tailscaletest.State{Status: tailscaletest.Running()})
	ctx := context.Background()
	_, err := app.Tailscale.On(ctx, nil, 0)
	noErr(t, err)
	_, _, _, before := savedSharing(t, app.Store)
	rename(fake)
	fake.Update(func(s *tailscaletest.State) { s.WriteError = "Access denied: serve config denied" })
	if _, err := app.Tailscale.On(ctx, nil, 0); err == nil || errors.Is(err, ErrTailscaleAhead) {
		t.Fatalf("turning on that Tailscale refused: %v", err)
	}
	if _, _, _, after := savedSharing(t, app.Store); !reflect.DeepEqual(after, before) {
		t.Fatalf("record after a refused turning on %+v, want %+v", after, before)
	}
}

// When a refused turning on cannot put the record from before back, the
// answer is that failure, not the refusal: the page says the change was not
// saved and the cause is logged once, since the record left may name as
// OwnGit's an endpoint that OwnGit did not write.
func TestTurningOnWhoseRecordCannotBePutBackIsNotARefusal(t *testing.T) {
	app, fake := tailscaleApp(t, tailscaletest.State{Status: tailscaletest.Running()})
	ctx := context.Background()
	// After a rename OwnGit uses the owner's endpoint for the new name
	// (Created false); then that endpoint disappears and Tailscale refuses
	// to write it again.
	first, err := app.Tailscale.On(ctx, nil, 0)
	noErr(t, err)
	rename(fake)
	fake.Update(func(s *tailscaletest.State) {
		s.Serve.Web[renamed+":443"] = tailscale.WebServer{Handlers: map[string]tailscale.Handler{"/": {Proxy: first.Record.Target}}}
	})
	change, err := app.Tailscale.On(ctx, nil, 0)
	noErr(t, err)
	if change.Endpoint != endpointKept || change.Record.Created {
		t.Fatalf("turning on over the owner's endpoint: %+v", change)
	}
	client, base, csrf, _ := networkSettingsClient(t, app)
	fake.Update(func(s *tailscaletest.State) {
		delete(s.Serve.Web, renamed+":443")
		s.WriteError = "Access denied: serve config denied"
	})
	refuseConfirmedSharing(t, app.Store)
	serverLog := captureServerLog(t)
	result := browserForm(t, client, base+"/settings", tailscaleForm(csrf, webui.ActionTailscaleOn, "admin-password", false), base)
	if result.status != http.StatusServiceUnavailable || !strings.Contains(result.body, enText(webui.MsgSettingsNotSaved)) {
		t.Fatalf("status=%d body=%s", result.status, result.body)
	}
	lines := loggedFailures(serverLog, 0)
	checkLoggedSteps(t, "a record that cannot be put back", lines, "Tailscale sharing change")
	if logged := strings.Join(lines, "\n"); !strings.Contains(logged, "Access denied") || !strings.Contains(logged, "could not be put back") {
		t.Errorf("the log does not name both causes: %s", logged)
	}
}

// Replacing OwnGit's own endpoint with a new target, when the outcome is
// unknown and Tailscale in fact kept the old endpoint, is finished by the
// next turning on, which rewrites the port, and by the next turning off,
// which removes the endpoint, as when the record was not touched.
func TestReplacingOwnGitsEndpointWithAnUnknownOutcomeIsFinishedNextTime(t *testing.T) {
	for _, next := range []string{"on", "off"} {
		t.Run(next, func(t *testing.T) {
			app, fake := tailscaleApp(t, tailscaletest.State{Status: tailscaletest.Running()})
			ctx := context.Background()
			first, err := app.Tailscale.On(ctx, nil, 0)
			noErr(t, err)
			// The server moves to another port, so turning on again replaces
			// the endpoint's target; Tailscale accepts the write without
			// keeping it and then cannot be read.
			saved, err := app.Store.NetworkSettings(ctx)
			noErr(t, err)
			saved.Listen = "127.0.0.1:18080"
			noErr(t, app.Store.UpdateNetwork(ctx, state.NetworkUpdate{Settings: saved}))
			fake.Update(func(s *tailscaletest.State) {
				s.Wrote, s.IgnoreWrites, s.ServeReadErrorAfterWrite = false, true, "synthetic serve status failure"
			})
			if _, err := app.Tailscale.On(ctx, nil, 0); !errors.Is(err, ErrTailscaleAhead) {
				t.Fatalf("turning on with an unknown outcome: %v", err)
			}
			fake.Update(func(s *tailscaletest.State) { s.Wrote, s.IgnoreWrites, s.ServeReadErrorAfterWrite = false, false, "" })
			if _, _, _, record := savedSharing(t, app.Store); record == nil || record.Confirmed || !record.Created || record.HTTPSPort != 443 || record.Target != first.Record.Target {
				t.Fatalf("pending record %+v, want OwnGit's endpoint on 443 to %s", record, first.Record.Target)
			}
			target := tailscale.Target(18080)
			if next == "on" {
				change, err := app.Tailscale.On(ctx, nil, 0)
				noErr(t, err)
				if change.Endpoint != endpointCreated || change.Record.HTTPSPort != 443 || change.Record.Target != target || !change.Record.Created || !change.Record.Confirmed {
					t.Fatalf("turning on again: %+v", change)
				}
				if serve := fake.State().Serve; !serve.Endpoint(tailscaletest.Name, 443, target).Exact || len(serve.Web) != 1 {
					t.Fatalf("after turning on again Tailscale serves %v", serve.Web)
				}
				return
			}
			change, err := app.Tailscale.Off(ctx)
			noErr(t, err)
			if change.Endpoint != "removed" {
				t.Fatalf("turning off: %+v", change)
			}
			if _, _, _, record := savedSharing(t, app.Store); record != nil || len(fake.State().Serve.Web) != 0 {
				t.Fatalf("after off: record %+v, Tailscale serves %v", record, fake.State().Serve.Web)
			}
		})
	}
}

// After the server's port changes, turning on again does not rewrite an
// endpoint the owner made, which OwnGit used without taking it over: it
// refuses and says so, naming OwnGit's local address now, and turning off
// leaves the endpoint.
func TestPortChangeDoesNotTakeOverTheOwnersEndpoint(t *testing.T) {
	app, fake := tailscaleApp(t, tailscaletest.State{Status: tailscaletest.Running()})
	ctx := context.Background()
	first, err := app.Tailscale.On(ctx, nil, 0)
	noErr(t, err)
	rename(fake)
	fake.Update(func(s *tailscaletest.State) {
		s.Serve.Web[renamed+":443"] = tailscale.WebServer{Handlers: map[string]tailscale.Handler{"/": {Proxy: first.Record.Target}}}
	})
	change, err := app.Tailscale.On(ctx, nil, 0)
	noErr(t, err)
	if change.Endpoint != endpointKept || change.Record.Created {
		t.Fatalf("turning on over the owner's endpoint: %+v", change)
	}
	_, _, _, before := savedSharing(t, app.Store)
	saved, err := app.Store.NetworkSettings(ctx)
	noErr(t, err)
	saved.Listen = "127.0.0.1:18080"
	noErr(t, app.Store.UpdateNetwork(ctx, state.NetworkUpdate{Settings: saved}))
	writes := len(fake.Writes())
	_, err = app.Tailscale.On(ctx, nil, 0)
	var refusal *TailscaleError
	if !errors.As(err, &refusal) || refusal.Problem != TailscaleProblemOwnersEndpoint || refusal.Detail != tailscale.Target(18080) || len(fake.Writes()) != writes {
		t.Fatalf("turning on after the port change: err=%v, writes %q", err, fake.Writes()[writes:])
	}
	if webui.TailscaleProblemCode(refusal.Problem) == webui.MsgTSProblemFailed {
		t.Fatalf("the refusal %q has no explanation of its own", refusal.Problem)
	}
	if _, _, _, after := savedSharing(t, app.Store); !reflect.DeepEqual(after, before) {
		t.Fatalf("record after the refused turning on %+v, want %+v", after, before)
	}
	off, err := app.Tailscale.Off(ctx)
	noErr(t, err)
	if off.Endpoint != "left" || !fake.State().Serve.Endpoint(renamed, 443, first.Record.Target).Exact {
		t.Fatalf("turning off: %+v; the owner's endpoint is %+v", off, fake.State().Serve.Endpoint(renamed, 443, first.Record.Target))
	}
}
