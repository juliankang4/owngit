package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"owngit/internal/state"
	"owngit/internal/tailscale"
	"owngit/internal/tailscale/tailscaletest"
)

// tailnetDevice is the address of another device on the tailnet.
const tailnetDevice = "100.64.0.9:50123"

// sendDirect sends a request as a browser on another device sends it
// straight to OwnGit: from peer, by the Host it opened, with the given
// header lines.
func sendDirect(app *App, method, path, host, peer string, header ...string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, path, nil)
	request.RemoteAddr, request.Host = peer, host
	for index := 0; index+1 < len(header); index += 2 {
		request.Header.Add(header[index], header[index+1])
	}
	response := httptest.NewRecorder()
	app.Handler().ServeHTTP(response, request)
	return response
}

// movedTo is where response sends the browser over HTTPS, or "".
func movedTo(response *httptest.ResponseRecorder) string {
	if location := response.Header().Get("Location"); strings.HasPrefix(location, "https:") || response.Code == http.StatusTemporaryRedirect {
		return location
	}
	return ""
}

// A dashboard page that a tailnet device opens over plain HTTP by the
// Tailscale name on OwnGit's port goes to the same page at the HTTPS
// address, while Tailscale reports that address ready. Git, the API,
// health, assets, setup, downloads, forms, passwords, other addresses and
// requests that came through the HTTPS address stay where they are.
func TestBrowserPagesMoveToTheTailnetAddressWhileItIsReady(t *testing.T) {
	app, fake := tailscaleApp(t, tailscaletest.State{Status: tailscaletest.Running()})
	ctx := context.Background()
	// The name is an allowed Host of its own, so it is still accepted after
	// sharing is turned off.
	noErr(t, app.Store.AddTrustedHost(ctx, tailscaletest.Name))
	noErr(t, app.Hosts.Add(tailscaletest.Name))
	noErr(t, app.Hosts.Add(tailscaletest.IPv4))
	_, err := app.Tailscale.On(ctx, nil, 0)
	noErr(t, err)
	_, err = app.Repositories.Create(ctx, "project", "")
	noErr(t, err)
	opened := tailscaletest.Name + ":7654"
	address := "https://" + tailscaletest.Name

	for _, page := range []struct{ method, path string }{
		{http.MethodGet, "/"},
		{http.MethodGet, "/repositories/project/code?ref=main&path=docs%2Fa%20b.md"},
		{http.MethodGet, "/login?next=%2Fsettings%2Fnetwork"},
		{http.MethodHead, "/settings/network"},
	} {
		response := sendDirect(app, page.method, page.path, opened, tailnetDevice)
		if response.Code != http.StatusTemporaryRedirect || response.Header().Get("Location") != address+page.path || response.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("%s %s: %d to %q (Cache-Control %q)", page.method, page.path, response.Code, response.Header().Get("Location"), response.Header().Get("Cache-Control"))
		}
	}
	// The address comes from OwnGit's settings, not from the request.
	if location := movedTo(sendDirect(app, http.MethodGet, "/", strings.ToUpper(tailscaletest.Name)+":7654", tailnetDevice)); location != address+"/" {
		t.Fatalf("a Host in capitals moved to %q", location)
	}

	for _, stay := range []struct {
		what, method, path, host, peer string
		header                         []string
	}{
		{"a form", http.MethodPost, "/login", opened, tailnetDevice, nil},
		{"a request with a password", http.MethodGet, "/", opened, tailnetDevice, []string{"Authorization", "Basic b3duZ2l0OnNlY3JldA=="}},
		{"Git discovery", http.MethodGet, "/git/project.git/info/refs?service=git-upload-pack", opened, tailnetDevice, nil},
		{"the API", http.MethodGet, "/api/v1/repositories", opened, tailnetDevice, nil},
		{"health", http.MethodGet, HealthPath, opened, tailnetDevice, nil},
		{"an asset", http.MethodGet, "/assets/owngit.css", opened, tailnetDevice, nil},
		{"setup", http.MethodGet, "/setup", opened, tailnetDevice, nil},
		{"a raw file", http.MethodGet, "/repositories/project/raw?path=README.md", opened, tailnetDevice, nil},
		{"a raw file's headers", http.MethodHead, "/repositories/project/raw?path=README.md", opened, tailnetDevice, nil},
		{"an archive", http.MethodGet, "/repositories/project/archive?ref=main&format=zip", opened, tailnetDevice, nil},
		{"a Tailscale address", http.MethodGet, "/", tailscaletest.IPv4 + ":7654", tailnetDevice, nil},
		{"this computer", http.MethodGet, "/", "localhost:7654", "127.0.0.1:50123", nil},
		{"a device's own forwarding headers", http.MethodGet, "/", tailscaletest.IPv4 + ":7654", tailnetDevice, []string{"X-Forwarded-Host", opened, "X-Forwarded-Proto", "http"}},
		// A proxy that OwnGit does not trust passes the HTTPS address's own
		// Host on, without OwnGit's port.
		{"the Host of the HTTPS address", http.MethodGet, "/", tailscaletest.Name, tailnetDevice, nil},
		{"the HTTPS port", http.MethodGet, "/", tailscaletest.Name + ":443", tailnetDevice, nil},
	} {
		if response := sendDirect(app, stay.method, stay.path, stay.host, stay.peer, stay.header...); movedTo(response) != "" {
			t.Errorf("%s moved: %d to %q", stay.what, response.Code, movedTo(response))
		}
	}
	if response := throughServe(app, "/"); response.Code != http.StatusOK {
		t.Fatalf("a page through the HTTPS address got %d", response.Code)
	}

	// Whatever keeps the address from working keeps the page where it is,
	// and the page moves again once the address works.
	stays := func(what string) {
		t.Helper()
		app.Tailscale.forget()
		if response := sendDirect(app, http.MethodGet, "/", opened, tailnetDevice); response.Code != http.StatusOK {
			t.Fatalf("%s: %d to %q", what, response.Code, response.Header().Get("Location"))
		}
	}
	moves := func(what string) {
		t.Helper()
		app.Tailscale.forget()
		if location := movedTo(sendDirect(app, http.MethodGet, "/", opened, tailnetDevice)); location != address+"/" {
			t.Fatalf("%s: moved to %q", what, location)
		}
	}
	serve := fake.State().Serve
	fake.Update(func(s *tailscaletest.State) { s.Serve = tailscale.ServeConfig{} })
	stays("Tailscale no longer has the address")
	fake.Update(func(s *tailscaletest.State) { s.Serve = serve })
	moves("the address is back")
	fake.Update(func(s *tailscaletest.State) { s.Status.BackendState = "Stopped" })
	stays("Tailscale is stopped")
	fake.Update(func(s *tailscaletest.State) { s.Status = tailscaletest.Running() })
	moves("Tailscale runs again")
	rename(fake)
	stays("the computer was renamed")
	fake.Update(func(s *tailscaletest.State) { s.Status = tailscaletest.Running() })
	moves("the name is back")

	// When OwnGit cannot tell, it logs why and answers the page.
	serverLog := captureServerLog(t)
	record, _, err := app.Store.TailscaleServe(ctx)
	noErr(t, err)
	noErr(t, app.Store.Exec(ctx, `UPDATE metadata SET value = '{' WHERE key = 'tailscale_serve'`))
	stays("the sharing record cannot be read")
	checkLoggedSteps(t, "an unreadable sharing record", loggedFailures(serverLog, 0), "HTTPS address check")
	noErr(t, app.Store.SaveTailscaleServe(ctx, record))
	moves("the record is readable again")
	since := len(serverLog.String())
	fake.Update(func(s *tailscaletest.State) { s.HoldReads = true })
	started := time.Now()
	stays("Tailscale does not answer")
	if waited := time.Since(started); waited > httpsRedirectWait+5*time.Second {
		t.Fatalf("the page waited %s for Tailscale", waited)
	}
	checkLoggedSteps(t, "Tailscale not answering", loggedFailures(serverLog, since), "HTTPS address check")
	fake.Update(func(s *tailscaletest.State) { s.HoldReads = false })
	moves("Tailscale answers again")

	_, err = app.Tailscale.Off(ctx)
	noErr(t, err)
	stays("sharing is off")
}

// When the base URL saved before sharing was turned on is the Tailscale
// address itself, turning sharing off keeps it as the base URL. What Serve
// passed over HTTPS while sharing was on no longer counts, so pages stay
// where they are asked, and move again once sharing is back on.
func TestTurningSharingOffEndsTheMoveEvenWhenTheBaseURLNamesItsAddress(t *testing.T) {
	app, fake := tailscaleApp(t, tailscaletest.State{Status: tailscaletest.Running()})
	ctx := context.Background()
	address := "https://" + tailscaletest.Name
	noErr(t, app.Store.UpdateNetwork(ctx, state.NetworkUpdate{Settings: state.NetworkSettings{BaseURL: address}, AddHosts: []string{tailscaletest.Name}}))
	noErr(t, app.Hosts.Add(tailscaletest.Name))
	_, err := app.Tailscale.On(ctx, nil, 0)
	noErr(t, err)
	opened := tailscaletest.Name + ":7654"
	if response := throughServe(app, "/"); response.Code != http.StatusOK {
		t.Fatalf("a page through Serve got %d", response.Code)
	}
	if location := movedTo(sendDirect(app, http.MethodGet, "/", opened, tailnetDevice)); location != address+"/" {
		t.Fatalf("while sharing is ready: moved to %q", location)
	}

	_, err = app.Tailscale.Off(ctx)
	noErr(t, err)
	if app.Network.BaseURL() != address || len(fake.State().Serve.Web) != 0 {
		t.Fatalf("after turning off: base URL %q, Serve %+v", app.Network.BaseURL(), fake.State().Serve)
	}
	if response := sendDirect(app, http.MethodGet, "/", opened, tailnetDevice); response.Code != http.StatusOK {
		t.Fatalf("after turning off: %d to %q", response.Code, response.Header().Get("Location"))
	}

	_, err = app.Tailscale.On(ctx, nil, 0)
	noErr(t, err)
	if location := movedTo(sendDirect(app, http.MethodGet, "/", opened, tailnetDevice)); location != address+"/" {
		t.Fatalf("after turning on again: moved to %q", location)
	}
}

// Turning sharing on replaces a base URL that a trusted proxy served over
// HTTPS with the Tailscale address. What the proxy passed before no longer
// counts, so a page opened at the earlier name stays where it is asked.
func TestTurningSharingOnEndsTheMoveToTheEarlierBaseURL(t *testing.T) {
	app, _ := tailscaleApp(t, tailscaletest.State{Status: tailscaletest.Running()})
	ctx := context.Background()
	const base = "https://git.example.internal"
	noErr(t, app.Store.UpdateNetwork(ctx, state.NetworkUpdate{Settings: state.NetworkSettings{BaseURL: base}, AddHosts: []string{"git.example.internal"}, AddProxies: []string{"127.0.0.1"}}))
	noErr(t, app.Hosts.Add("git.example.internal"))
	// The running network as a start with these saved settings has it.
	app.Network = NewLiveNetwork(LiveNetworkConfig{
		Record: state.RunningNetwork{
			Listen: "127.0.0.1:7654", Address: "127.0.0.1:7654", ListenSource: NetworkSourceDefault,
			Origin: base, BaseURL: base, BaseURLSource: NetworkSourceSaved,
			SavedHosts: []string{"git.example.internal"}, TrustedProxies: []string{"127.0.0.1"}, TrustedProxiesSource: NetworkSourceSaved,
		},
		BaseURL: base, Proxies: []netip.Prefix{netip.MustParsePrefix("127.0.0.1/32")}, Hosts: app.Hosts,
		Publish: func(running state.RunningNetwork) { noErr(t, app.Store.PublishRunningNetwork(ctx, running)) },
	})
	app.Network.Publish()
	app.Tailscale.Live = app.Network
	opened := "git.example.internal:7654"
	if response := sendDirect(app, http.MethodGet, "/", "git.example.internal", "127.0.0.1:50123", "X-Forwarded-Proto", "https", "X-Forwarded-For", "192.168.1.9"); response.Code != http.StatusOK {
		t.Fatalf("a page through the proxy got %d", response.Code)
	}
	if location := movedTo(sendDirect(app, http.MethodGet, "/", opened, "192.168.1.9:50123")); location != base+"/" {
		t.Fatalf("before sharing: moved to %q", location)
	}

	_, err := app.Tailscale.On(ctx, nil, 0)
	noErr(t, err)
	if address := "https://" + tailscaletest.Name; app.Network.BaseURL() != address {
		t.Fatalf("after turning on: base URL %q, want %q", app.Network.BaseURL(), address)
	}
	if response := sendDirect(app, http.MethodGet, "/", opened, "192.168.1.9:50123"); response.Code != http.StatusOK {
		t.Fatalf("after turning on, a page by the earlier name: %d to %q", response.Code, response.Header().Get("Location"))
	}
}

// Before setup, every page goes to setup where it was asked, and setup, the
// setup link and the setup file flow stay on the address the owner opened.
func TestSetupStaysOnTheAddressItWasOpenedBy(t *testing.T) {
	app, _, _ := newTestApp(t)
	app, _ = withTailscale(t, app, tailscaletest.State{Status: tailscaletest.Running()})
	_, err := app.Tailscale.On(context.Background(), nil, 0)
	noErr(t, err)
	opened := tailscaletest.Name + ":7654"
	if response := sendDirect(app, http.MethodGet, "/", opened, tailnetDevice); response.Code != http.StatusSeeOther || response.Header().Get("Location") != "/setup" {
		t.Fatalf("a page before setup: %d to %q", response.Code, response.Header().Get("Location"))
	}
	for _, path := range []string{"/setup", "/setup/approval"} {
		if response := sendDirect(app, http.MethodGet, path, opened, tailnetDevice); response.Code != http.StatusOK {
			t.Fatalf("GET %s before setup: %d to %q", path, response.Code, response.Header().Get("Location"))
		}
	}
	if response := sendDirect(app, http.MethodPost, "/setup/redeem", opened, tailnetDevice); movedTo(response) != "" {
		t.Fatalf("the setup link's redemption moved to %q", movedTo(response))
	}
}

// A dashboard page opened directly over plain HTTP by the name of an HTTPS
// base URL moves there once a trusted proxy has passed OwnGit a request for
// that base URL over HTTPS. A proxy OwnGit does not trust, one that passed
// plain HTTP, or one that passed HTTPS for another name shows nothing, and a
// new base URL waits for its own request: one for the earlier base URL does
// not count for it.
func TestBrowserPagesMoveToTheBaseURLOnceItsProxyServedIt(t *testing.T) {
	app := newConfiguredApp(t)
	const base = "https://git.example.internal"
	loopback := []netip.Prefix{netip.MustParsePrefix("127.0.0.1/32")}
	network := func(base string, proxies []netip.Prefix) {
		app.Network = NewLiveNetwork(LiveNetworkConfig{BaseURL: base, Proxies: proxies, Hosts: app.Hosts})
		noErr(t, app.Hosts.Add(strings.TrimPrefix(base, "https://")))
	}
	fromProxy := func(host, scheme string) {
		t.Helper()
		if response := sendDirect(app, http.MethodGet, "/", host, "127.0.0.1:50123", "X-Forwarded-Proto", scheme, "X-Forwarded-For", "192.168.1.9"); movedTo(response) != "" || response.Code != http.StatusOK {
			t.Fatalf("a request through the proxy for %s over %s: %d to %q", host, scheme, response.Code, response.Header().Get("Location"))
		}
	}
	direct := func(host string) string {
		return movedTo(sendDirect(app, http.MethodGet, "/activity?days=7", host, "192.168.1.9:50123"))
	}
	opened := "git.example.internal:7654"

	network(base, nil)
	fromProxy("git.example.internal", "https")
	if location := direct(opened); location != "" {
		t.Fatalf("moved after an untrusted proxy's request: %q", location)
	}
	network(base, loopback)
	if location := direct(opened); location != "" {
		t.Fatalf("moved before the proxy passed any request: %q", location)
	}
	fromProxy("git.example.internal", "http")
	if location := direct(opened); location != "" {
		t.Fatalf("moved after the proxy passed plain HTTP: %q", location)
	}
	// Another site behind the same proxy proves nothing about the base URL.
	noErr(t, app.Hosts.Add("other.example.internal"))
	fromProxy("other.example.internal", "https")
	if location := direct(opened); location != "" {
		t.Fatalf("moved after the proxy passed HTTPS for another name: %q", location)
	}
	fromProxy("git.example.internal", "https")
	if location := direct(opened); location != base+"/activity?days=7" {
		t.Fatalf("moved to %q", location)
	}
	// A browser on the server computer can use the same loopback address as
	// the trusted proxy. With no forwarding address, moving it grants no
	// local authority and gets it onto the proven HTTPS address.
	if location := movedTo(sendDirect(app, http.MethodGet, "/activity?days=7", opened, "127.0.0.1:50123")); location != base+"/activity?days=7" {
		t.Fatalf("same-computer browser moved to %q", location)
	}
	// The proxy decides about plain HTTP it passes on, on any port.
	fromProxy("git.example.internal:8080", "http")
	for _, host := range []string{"git.example.internal", "git.example.internal:443", "192.168.1.5:7654"} {
		noErr(t, app.Hosts.Add(host))
		if location := direct(host); location != "" {
			t.Fatalf("a page by %s moved to %q", host, location)
		}
	}
	network("https://git2.example.internal", loopback)
	if location := direct("git2.example.internal:7654"); location != "" {
		t.Fatalf("a new base URL was used before its proxy passed a request: %q", location)
	}
	// A request for the earlier base URL that is noted only after the
	// change proves nothing about the base URL in use.
	app.Network.ProveHTTPS(base)
	if location := direct(opened); location != "" || app.Network.HTTPSBaseURL() != "" {
		t.Fatalf("the earlier base URL counted after the change: moved to %q", location)
	}
}

// A browser on a tailnet device that opens a page by the Tailscale name on
// OwnGit's port lands on the page at the HTTPS address after one redirect,
// while Git clones and pushes at both addresses.
func TestGitAndBrowsersAtBothAddresses(t *testing.T) {
	app, _ := tailscaleApp(t, tailscaletest.State{Status: tailscaletest.Running()})
	ctx := context.Background()
	_, err := app.Tailscale.On(ctx, nil, 0)
	noErr(t, err)
	_, err = app.Repositories.Create(ctx, "project", "")
	noErr(t, err)
	handler := app.Handler()
	// Plain HTTP from another tailnet device that opened the name on OwnGit's
	// port, and HTTPS as Tailscale Serve passes it on.
	plain := serve(t, http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		request.RemoteAddr, request.Host = tailnetDevice, tailscaletest.Name+":7654"
		handler.ServeHTTP(writer, request)
	}))
	secure := serve(t, http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		request.RemoteAddr, request.Host = "127.0.0.1:50123", tailscaletest.Name
		request.Header.Set("X-Forwarded-Proto", "https")
		request.Header.Set("X-Forwarded-For", "100.64.0.9")
		handler.ServeHTTP(writer, request)
	}))

	commit := []string{"-c", "user.name=Redirect Test", "-c", "user.email=redirect@example.invalid", "commit", "-q", "--allow-empty", "-m"}
	source := filepath.Join(t.TempDir(), "source")
	apiRunGit(t, "", "init", "-q", "--initial-branch=main", source)
	apiRunGit(t, source, append(commit, "first")...)
	apiRunGit(t, source, "push", "-q", plain.URL+"/git/project.git", "main")
	viaHTTPS := filepath.Join(t.TempDir(), "https")
	apiRunGit(t, "", "clone", "-q", secure.URL+"/git/project.git", viaHTTPS)
	apiRunGit(t, viaHTTPS, append(commit, "second")...)
	apiRunGit(t, viaHTTPS, "push", "-q", "origin", "main")
	viaHTTP := filepath.Join(t.TempDir(), "http")
	apiRunGit(t, "", "clone", "-q", plain.URL+"/git/project.git", viaHTTP)
	if cloned, pushed := apiGitOutput(t, viaHTTP, "rev-parse", "HEAD"), apiGitOutput(t, viaHTTPS, "rev-parse", "HEAD"); cloned != pushed {
		t.Fatalf("the plain HTTP clone has %s, the HTTPS push sent %s", cloned, pushed)
	}

	servers := map[string]string{"http": strings.TrimPrefix(plain.URL, "http://"), "https": strings.TrimPrefix(secure.URL, "http://")}
	var redirects []string
	browser := &http.Client{
		Transport: roundTripper(func(request *http.Request) (*http.Response, error) {
			request = request.Clone(request.Context())
			request.URL.Host, request.URL.Scheme = servers[request.URL.Scheme], "http"
			return http.DefaultTransport.RoundTrip(request)
		}),
		CheckRedirect: func(request *http.Request, _ []*http.Request) error {
			redirects = append(redirects, request.URL.String())
			return nil
		},
	}
	response, err := browser.Get("http://" + tailscaletest.Name + ":7654/repositories/project/commits?ref=main")
	noErr(t, err)
	response.Body.Close()
	want := "https://" + tailscaletest.Name + "/repositories/project/commits?ref=main"
	if response.StatusCode != http.StatusOK || len(redirects) != 1 || redirects[0] != want {
		t.Fatalf("the browser ended with %d after %q, want 200 after one redirect to %s", response.StatusCode, redirects, want)
	}
}

// roundTripper is an http.RoundTripper made of a function.
type roundTripper func(*http.Request) (*http.Response, error)

func (send roundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	return send(request)
}
