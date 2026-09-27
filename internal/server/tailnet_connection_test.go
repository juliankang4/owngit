package server

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"owngit/internal/auth"
	"owngit/internal/tailscale/tailscaletest"
	"owngit/internal/webui"
)

// arriving makes every request look as if its connection came from peer to
// this server's local address, as the listener reports them, and names that
// address as the Host, as a browser that opened it does. The Host is
// allowed, as if it had been approved.
func arriving(t *testing.T, app *App, handler http.Handler, local, peer string) http.Handler {
	t.Helper()
	noErr(t, app.Hosts.Add(local))
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		address, err := net.ResolveTCPAddr("tcp", local)
		if err != nil {
			panic(err)
		}
		request.RemoteAddr = peer
		request.Host = local
		handler.ServeHTTP(writer, request.WithContext(context.WithValue(request.Context(), http.LocalAddrContextKey, address)))
	})
}

// A plain HTTP request that came from another tailnet device straight to
// one of this computer's Tailscale addresses, as Tailscale lists them, is
// labeled as encrypted by Tailscale and needs no acknowledgement of plain
// HTTP. The same request on the LAN, from this computer, to an address in
// the range that Tailscale does not list (another private network), or
// from a tailnet device that is a trusted proxy is not, whatever
// forwarding headers the proxy sent, valid or not.
func TestTheConnectionLabelNamesTheTailnet(t *testing.T) {
	proxy := func(header ...string) http.Header {
		values := http.Header{}
		for i := 0; i < len(header); i += 2 {
			values.Add(header[i], header[i+1])
		}
		return values
	}
	cases := []struct {
		name, local, peer string
		// proxy, when set, makes the peer a trusted proxy that sends it.
		proxy   http.Header
		tailnet bool
	}{
		{"tailnet IPv4", tailscaletest.IPv4 + ":7654", "100.64.0.9:50123", nil, true},
		{"tailnet IPv6", "[" + tailscaletest.IPv6 + "]:7654", "[fd7a:115c:a1e0::9]:50123", nil, true},
		{"home network", "192.168.1.5:7654", "192.168.1.9:50123", nil, false},
		{"loopback", "127.0.0.1:7654", "127.0.0.1:50123", nil, false},
		{"an address Tailscale does not list", "100.64.0.8:7654", "100.64.0.9:50123", nil, false},
		{"this computer at its own Tailscale address", tailscaletest.IPv4 + ":7654", tailscaletest.IPv4 + ":50123", nil, false},
		{"a device outside the tailnet ranges", tailscaletest.IPv4 + ":7654", "192.168.1.9:50123", nil, false},
		{"a tailnet proxy that forwarded plain HTTP", tailscaletest.IPv4 + ":7654", "100.64.0.9:50123", proxy("X-Forwarded-Proto", "http"), false},
		{"a tailnet proxy without forwarding headers", tailscaletest.IPv4 + ":7654", "100.64.0.9:50123", proxy(), false},
		{"a tailnet proxy with an invalid scheme", tailscaletest.IPv4 + ":7654", "100.64.0.9:50123", proxy("X-Forwarded-Proto", "gopher"), false},
		{"a tailnet proxy with a repeated scheme", tailscaletest.IPv4 + ":7654", "100.64.0.9:50123", proxy("X-Forwarded-Proto", "http", "X-Forwarded-Proto", "http"), false},
		{"a tailnet proxy with only a client address", tailscaletest.IPv4 + ":7654", "100.64.0.9:50123", proxy("X-Forwarded-For", "100.100.1.2"), false},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			app, _ := withTailscale(t, newUnacknowledgedApp(t), tailscaletest.State{Status: tailscaletest.Running()})
			readAddresses(t, app)
			if test.proxy != nil {
				app.Network = NewLiveNetwork(LiveNetworkConfig{Proxies: []netip.Prefix{netip.MustParsePrefix("100.64.0.9/32")}, Hosts: app.Hosts})
			}
			server := serve(t, arriving(t, app, app.Handler(), test.local, test.peer))
			client, _ := newBrowserClient(t)
			request, err := http.NewRequest(http.MethodGet, server.URL+"/settings", nil)
			noErr(t, err)
			for name, values := range test.proxy {
				request.Header[name] = values
			}
			response, err := client.Do(request)
			noErr(t, err)
			content, err := io.ReadAll(response.Body)
			response.Body.Close()
			noErr(t, err)
			body := string(content)
			if response.StatusCode != http.StatusOK {
				t.Fatalf("status=%d", response.StatusCode)
			}
			label := strings.Contains(body, enText(webui.MsgConnTailnet)) && strings.Contains(body, enText(webui.MsgConnTailnetNote))
			ack := strings.Contains(body, `name="action" value="acknowledge_insecure"`)
			if label != test.tailnet || ack == test.tailnet || strings.Contains(body, "conn--secure") != test.tailnet {
				t.Fatalf("label=%v acknowledgement offered=%v, want the tailnet label %v", label, ack, test.tailnet)
			}
			if test.tailnet && strings.Contains(body, enText(webui.MsgConnPlain)) {
				t.Fatal("a tailnet request is also called not encrypted by OwnGit")
			}
		})
	}
}

// readAddresses reads Tailscale's state once, as a page does in the
// background, so that pages know this computer's Tailscale addresses
// however long the fake takes to answer.
func readAddresses(t *testing.T, app *App) {
	t.Helper()
	_, err := app.Tailscale.read(context.Background())
	noErr(t, err)
}

// newUnacknowledgedApp is newConfiguredApp without plain HTTP accepted.
func newUnacknowledgedApp(t *testing.T) *App {
	t.Helper()
	app, store, repositoryRoot := newTestApp(t)
	noErr(t, os.MkdirAll(repositoryRoot, 0o700))
	canonical, err := filepath.EvalSymlinks(repositoryRoot)
	noErr(t, err)
	adminHash, err := auth.HashPassword("admin-password")
	noErr(t, err)
	noErr(t, store.CompleteSetup(context.Background(), canonical, "open", "", adminHash, false))
	app.Repositories.SetRoot(canonical)
	return app
}

// Web setup over the tailnet finishes without the plain HTTP
// acknowledgement; the same setup on the home network still needs it.
func TestSetupOverTheTailnetNeedsNoPlainHTTPAcknowledgement(t *testing.T) {
	for _, test := range []struct {
		name, local, peer string
		want              int
	}{
		{"tailnet", tailscaletest.IPv4 + ":7654", "100.64.0.9:50123", http.StatusSeeOther},
		{"home network", "192.168.1.5:7654", "192.168.1.9:50123", http.StatusUnprocessableEntity},
	} {
		t.Run(test.name, func(t *testing.T) {
			app, store, repositoryRoot := newTestApp(t)
			app, _ = withTailscale(t, app, tailscaletest.State{Status: tailscaletest.Running()})
			readAddresses(t, app)
			noErr(t, store.PutBootstrap(context.Background(), "synthetic-owner-token", time.Now().Add(time.Hour)))
			noErr(t, os.MkdirAll(repositoryRoot, 0o700))
			// Only the answers arrive from the other device; the setup link
			// is redeemed on this computer as usual.
			handler := app.Handler()
			remote := arriving(t, app, handler, test.local, test.peer)
			server := serve(t, http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				if request.Method == http.MethodPost && request.URL.Path == "/setup" {
					remote.ServeHTTP(writer, request)
					return
				}
				handler.ServeHTTP(writer, request)
			}))
			client, jar := newBrowserClient(t)
			request(t, client, http.MethodGet, server.URL+"/setup", nil, "").Body.Close()
			response := request(t, client, http.MethodPost, server.URL+"/setup/redeem", url.Values{
				"csrf": {cookieValue(t, jar, server.URL, preauthCookie)}, "token": {"synthetic-owner-token"},
			}, server.URL)
			response.Body.Close()
			session, ok, err := store.Session(context.Background(), cookieValue(t, jar, server.URL, setupCookie), "setup", time.Now())
			if err != nil || !ok {
				t.Fatalf("setup session ok=%v err=%v", ok, err)
			}
			response = request(t, client, http.MethodPost, server.URL+"/setup", url.Values{
				"csrf": {session.CSRF}, "storage_path": {repositoryRoot}, "access_mode": {"open"}, "admin_password": {"admin-password-one"},
			}, "http://"+test.local)
			response.Body.Close()
			if response.StatusCode != test.want {
				t.Fatalf("setup without the acknowledgement: status=%d, want %d", response.StatusCode, test.want)
			}
		})
	}
}

// The connection label does not wait for the tailscale command, which can
// take up to its time limit when tailscaled does not answer. Before the
// first reading has finished, a page waits for it only briefly and goes
// without the label; afterwards pages use the latest addresses while a new
// reading runs in the background.
func TestTheTailnetLabelDoesNotWaitForTailscale(t *testing.T) {
	// Each tailscale command answers after 3 s, so a reading takes 6 s.
	app, fake := withTailscale(t, newUnacknowledgedApp(t), tailscaletest.State{Status: tailscaletest.Running(), ReadDelay: 3000})
	server := serve(t, arriving(t, app, app.Handler(), tailscaletest.IPv4+":7654", "100.64.0.9:50123"))
	client, _ := newBrowserClient(t)
	labeled := func() bool {
		t.Helper()
		response, err := client.Get(server.URL + "/")
		noErr(t, err)
		content, err := io.ReadAll(response.Body)
		response.Body.Close()
		noErr(t, err)
		if response.StatusCode != http.StatusOK {
			t.Fatalf("status=%d", response.StatusCode)
		}
		return strings.Contains(string(content), enText(webui.MsgConnTailnet))
	}
	cache := &app.Tailscale.readings
	reading := func() chan struct{} {
		cache.mu.Lock()
		defer cache.mu.Unlock()
		return cache.refreshed
	}
	finished := func() { finishAddressReading(t, app.Tailscale) }

	if labeled() || reading() == nil {
		t.Fatal("the first page waited for the whole reading, or none runs")
	}
	finished()
	if !labeled() {
		t.Fatal("no label after the reading")
	}
	app.Tailscale.forget()
	if !labeled() || reading() == nil {
		t.Fatal("a page waited for a new reading instead of using the latest addresses")
	}
	finished()
	fake.Update(func(s *tailscaletest.State) { s.ReadDelay = 0 })
}
