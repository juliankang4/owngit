package server

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"owngit/internal/auth"
	"owngit/internal/requestctx"
	"owngit/internal/webui"
)

func TestStalledOrdinaryFormTimesOutAndShutdownCompletes(t *testing.T) {
	app, _, _ := newTestApp(t)
	app.HTTPTimeout = 75 * time.Millisecond
	server := serve(t, app.Handler())
	address := strings.TrimPrefix(server.URL, "http://")
	connection, err := net.Dial("tcp", address)
	noErr(t, err)
	defer connection.Close()
	request := "POST /setup/redeem HTTP/1.1\r\nHost: " + address + "\r\nOrigin: " + server.URL + "\r\nContent-Type: application/x-www-form-urlencoded\r\nTransfer-Encoding: chunked\r\nConnection: close\r\n\r\n4\r\ncsrf\r\n"
	if _, err := io.WriteString(connection, request); err != nil {
		t.Fatal(err)
	}
	noErr(t, connection.SetReadDeadline(time.Now().Add(2*time.Second)))
	readDone := make(chan error, 1)
	go func() {
		_, readErr := io.ReadAll(connection)
		readDone <- readErr
	}()
	// Shutdown begins while the request body is still incomplete. The app's
	// ordinary-request deadline must release the handler before shutdown's own
	// deadline expires.
	time.Sleep(10 * time.Millisecond)
	shutdownContext, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	noErrf(t, server.Config.Shutdown(shutdownContext), "shutdown while ordinary form was stalled")
	readErr := <-readDone
	if timeout, ok := readErr.(net.Error); ok && timeout.Timeout() {
		t.Fatal("ordinary form connection did not close after its deadline")
	}
}

func TestUnknownHostIsRejectedBeforeApplication(t *testing.T) {
	called := false
	handler := NewHostPolicy("owngit.internal").Middleware(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true }))
	request := httptest.NewRequest(http.MethodGet, "http://attacker.invalid/", nil)
	request.Host = "attacker.invalid"
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusMisdirectedRequest || called {
		t.Fatalf("status=%d called=%v, want 421 before application", response.Code, called)
	}
}

func TestLocalNextRejectsExternalAndBackslashRedirects(t *testing.T) {
	for _, value := range []string{"//example.invalid", `/\\example.invalid`, "https://example.invalid", "/safe\r\nLocation: x",
		"/\t/example.invalid", "/\n/example.invalid", "/\r/example.invalid", "/\x00x", "/\x1b/example.invalid", "/\x7f/example.invalid"} {
		if got := localNext(value, "/fallback"); got != "/fallback" {
			t.Errorf("localNext(%q)=%q, want fallback", value, got)
		}
	}
	if got := localNext("/repositories/project?ref=main", "/fallback"); got != "/repositories/project?ref=main" {
		t.Fatalf("safe local path changed to %q", got)
	}
}

func TestOriginMustExactlyMatchRequest(t *testing.T) {
	called := false
	handler := NewHostPolicy("owngit.internal").Middleware(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true }))
	for _, origin := range []string{"http://owngit.internal.evil", "https://owngit.internal", "null", "http://owngit.internal/path"} {
		called = false
		request := httptest.NewRequest(http.MethodPost, "http://owngit.internal/settings", nil)
		request.Host = "owngit.internal"
		request.Header.Set("Origin", origin)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusForbidden || called {
			t.Errorf("origin %q: status=%d called=%v", origin, response.Code, called)
		}
	}
	request := httptest.NewRequest(http.MethodPost, "http://owngit.internal/settings", nil)
	request.Host = "owngit.internal"
	request.Header.Set("Origin", "http://owngit.internal")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || !called {
		t.Fatalf("matching origin status=%d called=%v", response.Code, called)
	}
}

// Forwarded headers from a peer that is not a trusted proxy change neither
// the Host check, the Origin check, cookie security nor the lockout key,
// whether no proxy is trusted or another address is.
func TestForwardedHeadersFromDirectPeersChangeNothing(t *testing.T) {
	for _, trusted := range [][]netip.Prefix{nil, {netip.MustParsePrefix("192.0.2.99/32"), netip.MustParsePrefix("10.0.0.0/8")}} {
		t.Run(fmt.Sprintf("trusted %v", trusted), func(t *testing.T) { forwardedHeadersChangeNothing(t, trusted) })
	}
}

func forwardedHeadersChangeNothing(t *testing.T, trusted []netip.Prefix) {
	app := newConfiguredApp(t)
	app.Hosts = NewHostPolicy("owngit.internal")
	app.Requests = requestctx.Resolver{TrustedProxies: trusted, HostAllowed: app.Hosts.Allows}
	handler := app.Handler()
	spoof := func(request *http.Request, client string) {
		request.Header.Set("X-Forwarded-For", client)
		request.Header.Set("X-Forwarded-Proto", "https")
		request.Header.Set("X-Forwarded-Host", "owngit.internal")
		request.Header.Set("Forwarded", "for="+client+";proto=https;host=owngit.internal")
	}

	unknown := httptest.NewRequest(http.MethodGet, "/", nil)
	unknown.Host = "attacker.invalid"
	spoof(unknown, "127.0.0.1")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, unknown)
	if response.Code != http.StatusMisdirectedRequest {
		t.Fatalf("forwarded approved Host passed the Host check: status=%d", response.Code)
	}

	httpsOrigin := httptest.NewRequest(http.MethodGet, "/settings", nil)
	httpsOrigin.Host = "owngit.internal"
	httpsOrigin.Header.Set("Origin", "https://owngit.internal")
	spoof(httpsOrigin, "127.0.0.1")
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, httpsOrigin)
	if response.Code != http.StatusForbidden {
		t.Fatalf("forwarded scheme satisfied an https Origin: status=%d", response.Code)
	}

	page := httptest.NewRequest(http.MethodGet, "/admin/login", nil)
	page.Host = "owngit.internal"
	spoof(page, "127.0.0.1")
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, page)
	for _, cookie := range response.Result().Cookies() {
		if cookie.Secure {
			t.Fatalf("cookie %s is Secure on a plain HTTP connection with a forwarded scheme", cookie.Name)
		}
	}

	// Wrong administrator passwords from one peer lock that peer out, even
	// when each attempt claims another forwarded client address.
	for attempt := 0; attempt < 4; attempt++ {
		login := httptest.NewRequest(http.MethodGet, "/admin/login", nil)
		login.Host = "owngit.internal"
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, login)
		preauth := ""
		for _, cookie := range response.Result().Cookies() {
			if cookie.Name == preauthCookie {
				preauth = cookie.Value
			}
		}
		form := url.Values{"csrf": {preauth}, "admin_password": {"wrong-password"}, "next": {"/"}}
		post := httptest.NewRequest(http.MethodPost, "/admin/login", strings.NewReader(form.Encode()))
		post.Host = "owngit.internal"
		post.RemoteAddr = "192.0.2.50:1000"
		post.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		post.Header.Set("Origin", "http://owngit.internal")
		post.AddCookie(&http.Cookie{Name: preauthCookie, Value: preauth})
		spoof(post, "203.0.113."+strconv.Itoa(attempt+1))
		response = httptest.NewRecorder()
		handler.ServeHTTP(response, post)
		if response.Code != http.StatusUnauthorized {
			t.Fatalf("wrong password attempt %d status=%d", attempt, response.Code)
		}
	}
	if err := app.Auth.VerifyCredential(context.Background(), "admin", "admin-password", "192.0.2.50"); !errors.Is(err, auth.ErrRateLimited) {
		t.Fatalf("peer 192.0.2.50 is not locked out: %v", err)
	}
	for attempt := 1; attempt <= 4; attempt++ {
		if err := app.Auth.VerifyCredential(context.Background(), "admin", "admin-password", "203.0.113."+strconv.Itoa(attempt)); err != nil {
			t.Fatalf("forwarded address 203.0.113.%d was charged: %v", attempt, err)
		}
	}
}

// remotePeer is the address of a connection from another device, the one
// httptest.NewRequest gives by default.
const remotePeer = "192.0.2.1:1234"

// Host values that name this computer, in the forms a client can send.
var loopbackHostValues = []string{
	"localhost", "LOCALHOST", "localhost.", "localhost:7654", "LocalHost.:7654",
	"127.0.0.1", "127.0.0.1:7654", "127.0.0.1.:7654",
	"::1", "[::1]", "[::1]:7654", "[0:0:0:0:0:0:0:1]:7654",
	"[::ffff:127.0.0.1]:7654", "::ffff:7f00:1",
}

// Peers of connections that do not come from this computer.
var nonLoopbackPeers = []string{
	remotePeer, "10.0.0.5:40000", "[2001:db8::5]:40000", "[fe80::1%eth0]:40000",
	"[::ffff:192.0.2.1]:40000", "192.0.2.1", "", "not-an-address",
}

// Any device can send Host: localhost, so a name that points at this computer
// is accepted only from a connection that comes from this computer.
func TestLoopbackHostNamesAreAcceptedOnlyFromLoopbackPeers(t *testing.T) {
	policy := NewHostPolicy("gitbox.lan", "dev.localhost", "127.0.0.2")
	names := append([]string{"dev.localhost:7654", "127.0.0.2:7654"}, loopbackHostValues...)
	for _, name := range names {
		for _, peer := range nonLoopbackPeers {
			if policy.Allows(name, peer) {
				t.Errorf("Host %q from peer %q is accepted", name, peer)
			}
		}
		for _, peer := range []string{"127.0.0.1:40000", "127.0.0.2:40000", "[::1]:40000", "[::ffff:127.0.0.1]:40000", "::1"} {
			if !policy.Allows(name, peer) {
				t.Errorf("Host %q from loopback peer %q is refused", name, peer)
			}
		}
	}
	// Other names do not depend on the peer.
	for _, peer := range append([]string{"127.0.0.1:40000"}, nonLoopbackPeers...) {
		if !policy.Allows("GitBox.lan:7654", peer) || policy.Allows("attacker.invalid", peer) {
			t.Errorf("peer %q changed the answer for a name that is not loopback", peer)
		}
	}
	// Removing a loopback name keeps it for this computer.
	policy.Remove("localhost")
	if !policy.Allows("localhost", "127.0.0.1:40000") {
		t.Fatal("Remove dropped localhost")
	}
}

// A device that sends a loopback Host is refused on every path, before and
// after setup, even in open access mode where Git needs no password. The
// loopback peer and a kept name from another device still work.
func TestRemotePeerWithLoopbackHostIsRefused(t *testing.T) {
	for _, initialized := range []bool{false, true} {
		t.Run(map[bool]string{false: "before setup", true: "after setup"}[initialized], func(t *testing.T) {
			var app *App
			if initialized {
				app = newConfiguredApp(t)
				_, err := app.Repositories.Create(context.Background(), "demo", "")
				noErr(t, err)
			} else {
				app, _, _ = newTestApp(t)
				app.Version = "9.9.9-test"
			}
			app.Hosts = NewHostPolicy("gitbox.lan")
			handler := app.Handler()
			send := func(path, host, peer string, cookies ...*http.Cookie) *httptest.ResponseRecorder {
				request := httptest.NewRequest(http.MethodGet, path, nil)
				request.Host, request.RemoteAddr = host, peer
				for _, cookie := range cookies {
					request.AddCookie(cookie)
				}
				response := httptest.NewRecorder()
				handler.ServeHTTP(response, request)
				return response
			}
			gitRefs := "/git/demo.git/info/refs?service=git-upload-pack"
			for _, host := range loopbackHostValues {
				for _, peer := range nonLoopbackPeers {
					for _, path := range []string{"/", "/api/v1/repositories", gitRefs, "/healthz", "/settings", "/login"} {
						if response := send(path, host, peer); response.Code != http.StatusMisdirectedRequest {
							t.Fatalf("GET %s with Host %q from %q: status=%d, want 421", path, host, peer, response.Code)
						}
					}
				}
			}

			page := send("/", "localhost", remotePeer)
			if body := page.Body.String(); !strings.HasPrefix(body, "unrecognized host\n") || !strings.Contains(body, webui.Text(webui.LangEN, webui.MsgHostRefusedHint)) {
				t.Fatalf("refused page does not say how to allow the address:\n%s", body)
			}
			korean := send("/", "localhost", remotePeer, &http.Cookie{Name: languageCookie, Value: string(webui.LangKO)})
			if !strings.Contains(korean.Body.String(), webui.Text(webui.LangKO, webui.MsgHostRefusedHint)) {
				t.Fatalf("refused page ignores the Korean language choice:\n%s", korean.Body.String())
			}
			if api := send("/api/v1/repositories", "localhost", remotePeer); !strings.Contains(api.Body.String(), `"unrecognized_host"`) {
				t.Fatalf("API refusal body: %s", api.Body.String())
			}

			// This computer and a kept name keep working.
			for _, host := range []string{"localhost:7654", "[::1]:7654"} {
				if response := send("/", host, "127.0.0.1:40000"); response.Code == http.StatusMisdirectedRequest {
					t.Fatalf("GET / with Host %q from this computer: status=%d", host, response.Code)
				}
			}
			if response := send("/", "gitbox.lan:7654", remotePeer); response.Code == http.StatusMisdirectedRequest {
				t.Fatalf("GET / with a kept Host from another device: status=%d", response.Code)
			}
			if initialized {
				for _, request := range []struct{ host, peer string }{{"localhost:7654", "[::1]:40000"}, {"gitbox.lan:7654", remotePeer}} {
					if response := send(gitRefs, request.host, request.peer); response.Code != http.StatusOK {
						t.Fatalf("Git from Host %q and peer %q: status=%d, want 200", request.host, request.peer, response.Code)
					}
				}
				return
			}
			// Before setup, the setup link works from a loopback Host as from
			// any other unknown Host: a redemption page that reveals nothing.
			setup := send("/setup", "localhost:7654", remotePeer)
			if body := setup.Body.String(); setup.Code != http.StatusOK || !strings.Contains(body, `action="/setup/redeem"`) ||
				strings.Contains(body, "9.9.9-test") || strings.Contains(body, "git version test") {
				t.Fatalf("setup from another device with Host localhost: status=%d\n%s", setup.Code, body)
			}
			if local := send("/setup", "localhost:7654", "127.0.0.1:40000"); !strings.Contains(local.Body.String(), "9.9.9-test") {
				t.Fatalf("setup on this computer lost the full page: status=%d", local.Code)
			}
		})
	}
}

// A trusted proxy on this computer, such as Tailscale Serve or a local
// reverse proxy, connects from loopback and may pass a loopback Host. A
// trusted proxy on another computer is judged by its own address: its kept
// Host works, a loopback Host is refused, and its X-Forwarded-Host cannot
// choose a loopback name.
func TestTrustedProxiesAndLoopbackHosts(t *testing.T) {
	app := newConfiguredApp(t)
	app.Hosts = NewHostPolicy("gitbox.lan")
	app.Requests = requestctx.Resolver{
		TrustedProxies: []netip.Prefix{netip.MustParsePrefix("127.0.0.1/32"), netip.MustParsePrefix("10.0.0.5/32")},
		HostAllowed:    app.Hosts.Allows,
	}
	handler := app.Handler()
	send := func(host, peer string, headers ...string) int {
		request := httptest.NewRequest(http.MethodGet, "/", nil)
		request.Host, request.RemoteAddr = host, peer
		for index := 0; index+1 < len(headers); index += 2 {
			request.Header.Set(headers[index], headers[index+1])
		}
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		return response.Code
	}
	forwarded := []string{"X-Forwarded-For", "203.0.113.9", "X-Forwarded-Proto", "https"}
	if status := send("localhost:7654", "127.0.0.1:40000", forwarded...); status == http.StatusMisdirectedRequest {
		t.Fatalf("local proxy with Host localhost: status=%d", status)
	}
	if status := send("gitbox.lan", "10.0.0.5:40000", forwarded...); status == http.StatusMisdirectedRequest {
		t.Fatalf("remote proxy with its kept Host: status=%d", status)
	}
	if status := send("localhost", "10.0.0.5:40000", forwarded...); status != http.StatusMisdirectedRequest {
		t.Fatalf("remote proxy with Host localhost: status=%d, want 421", status)
	}
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request.Host, request.RemoteAddr = "gitbox.lan", "10.0.0.5:40000"
	request.Header.Set("X-Forwarded-Host", "localhost")
	if host := app.Requests.Resolve(request).Host; host != "gitbox.lan" {
		t.Fatalf("remote proxy's X-Forwarded-Host chose %q", host)
	}
	request.RemoteAddr = "127.0.0.1:40000"
	if host := app.Requests.Resolve(request).Host; host != "localhost" {
		t.Fatalf("local proxy's X-Forwarded-Host localhost was ignored: %q", host)
	}
}
