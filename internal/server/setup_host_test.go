package server

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"

	"owngit/internal/state"
)

// hostBrowser drives the web setup with one Host, as a browser on another
// device that reached the server by that name. Every connection goes to the
// test server whatever the name.
type hostBrowser struct {
	t      *testing.T
	client *http.Client
	jar    http.CookieJar
	server string // the origin the browser uses, "http://" + Host
}

func newHostBrowser(t *testing.T, app *App, host string) *hostBrowser {
	t.Helper()
	client, jar := newBrowserClient(t)
	address := strings.TrimPrefix(serve(t, app.Handler()).URL, "http://")
	if host == "" {
		host = address
	}
	client.Transport = &http.Transport{DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, address)
	}}
	t.Cleanup(client.CloseIdleConnections)
	return &hostBrowser{t: t, client: client, jar: jar, server: "http://" + host}
}

func (browser *hostBrowser) do(method, path string, values url.Values) (int, string) {
	browser.t.Helper()
	var body io.Reader
	if values != nil {
		body = strings.NewReader(values.Encode())
	}
	request, err := http.NewRequest(method, browser.server+path, body)
	noErr(browser.t, err)
	if values != nil {
		request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		request.Header.Set("Origin", browser.server)
	}
	response, err := browser.client.Do(request)
	noErr(browser.t, err)
	defer response.Body.Close()
	content, err := io.ReadAll(response.Body)
	noErr(browser.t, err)
	return response.StatusCode, string(content)
}

// redeem presents the setup token and returns the wizard page.
func (browser *hostBrowser) redeem(token string) string {
	browser.t.Helper()
	if status, _ := browser.do(http.MethodGet, "/setup", nil); status != http.StatusOK {
		browser.t.Fatalf("GET /setup status=%d", status)
	}
	preauth := cookieValue(browser.t, browser.jar, browser.server, preauthCookie)
	if status, _ := browser.do(http.MethodPost, "/setup/redeem", url.Values{"csrf": {preauth}, "token": {token}}); status != http.StatusSeeOther {
		browser.t.Fatalf("redeem status=%d", status)
	}
	status, page := browser.do(http.MethodGet, "/setup", nil)
	if status != http.StatusOK {
		browser.t.Fatalf("wizard status=%d", status)
	}
	return page
}

func (browser *hostBrowser) finish(store *state.Store, repositoryRoot string, extra url.Values) int {
	browser.t.Helper()
	session, ok, err := store.Session(context.Background(), cookieValue(browser.t, browser.jar, browser.server, setupCookie), "setup", time.Now())
	if err != nil || !ok {
		browser.t.Fatalf("setup session ok=%v err=%v", ok, err)
	}
	noErr(browser.t, os.MkdirAll(repositoryRoot, 0o700))
	values := url.Values{"csrf": {session.CSRF}, "storage_path": {repositoryRoot}, "access_mode": {"open"},
		"admin_password": {"admin-password-one"}, "insecure_ack": {"on"}}
	for key, value := range extra {
		values[key] = value
	}
	status, _ := browser.do(http.MethodPost, "/setup", values)
	return status
}

func trustedHosts(t *testing.T, store *state.Store) []string {
	t.Helper()
	hosts, err := store.TrustedHosts(context.Background())
	noErr(t, err)
	return hosts
}

// A Host accepted for this run only, by a serve flag or the listen address,
// can be kept for later starts from the setup form, and only when ticked.
func TestSetupOffersToKeepTheHostItWasReachedBy(t *testing.T) {
	for _, keep := range []bool{true, false} {
		app, store, repositoryRoot := newTestApp(t)
		app.Hosts = NewHostPolicy("gitbox.test")
		noErr(t, store.PutBootstrap(context.Background(), "synthetic-owner-token", time.Now().Add(time.Hour)))
		browser := newHostBrowser(t, app, "GitBox.test:7720")
		page := browser.redeem("synthetic-owner-token")
		if !strings.Contains(page, `name="keep_host"`) || !strings.Contains(page, `<span class="mono">gitbox.test</span>`) {
			t.Fatalf("wizard does not offer to keep gitbox.test:\n%s", page)
		}
		if strings.Contains(page, `name="keep_host" value="1" checked`) || strings.Contains(page, "only to finish setup") {
			t.Fatal("the keep-host box starts ticked or describes a setup-only address")
		}
		extra := url.Values{}
		if keep {
			extra.Set("keep_host", "on")
		}
		if status := browser.finish(store, repositoryRoot, extra); status != http.StatusSeeOther {
			t.Fatalf("finish status=%d", status)
		}
		want := []string(nil)
		if keep {
			want = []string{"gitbox.test"}
		}
		if got := trustedHosts(t, store); !reflect.DeepEqual(got, want) {
			t.Fatalf("keep=%v: trusted hosts=%v, want %v", keep, got, want)
		}
	}
}

// No offer is made, and a posted box saves nothing, for loopback names and
// for names the next start accepts anyway.
func TestSetupDoesNotOfferHostsThatStayAccepted(t *testing.T) {
	cases := []struct {
		name  string
		host  string
		saved func(*state.Store)
	}{
		{"loopback", "", func(*state.Store) {}},
		{"localhost", "localhost:7720", func(*state.Store) {}},
		{"allowed Host", "gitbox.test:7720", func(store *state.Store) { noErr(t, store.AddTrustedHost(context.Background(), "GITBOX.test")) }},
		{"saved base URL", "gitbox.test:7720", func(store *state.Store) {
			noErr(t, store.UpdateNetwork(context.Background(), state.NetworkUpdate{Settings: state.NetworkSettings{BaseURL: "http://gitbox.test:7720"}}))
		}},
		{"saved listen address", "192.168.1.20:7720", func(store *state.Store) {
			noErr(t, store.UpdateNetwork(context.Background(), state.NetworkUpdate{Settings: state.NetworkSettings{Listen: "192.168.1.20:7720"}}))
		}},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			app, store, repositoryRoot := newTestApp(t)
			app.Hosts = NewHostPolicy("gitbox.test", "192.168.1.20")
			test.saved(store)
			before := trustedHosts(t, store)
			noErr(t, store.PutBootstrap(context.Background(), "synthetic-owner-token", time.Now().Add(time.Hour)))
			browser := newHostBrowser(t, app, test.host)
			if page := browser.redeem("synthetic-owner-token"); strings.Contains(page, `name="keep_host"`) {
				t.Fatal("wizard offers to keep a Host that stays accepted")
			}
			if status := browser.finish(store, repositoryRoot, url.Values{"keep_host": {"on"}}); status != http.StatusSeeOther {
				t.Fatalf("finish status=%d", status)
			}
			if got := trustedHosts(t, store); !reflect.DeepEqual(got, before) {
				t.Fatalf("trusted hosts changed from %v to %v", before, got)
			}
		})
	}
}

// status requests path and returns only the status.
func (browser *hostBrowser) status(method, path string, values url.Values) int {
	browser.t.Helper()
	status, _ := browser.do(method, path, values)
	return status
}

// Before setup is complete, a browser on a Host the server was not told
// about can open the setup link: it gets only a redemption page, that page's
// files, and redemption. A redeemed session works only on that Host, and the
// Host is accepted after setup only when the owner keeps it.
func TestUnknownHostReachesSetupOnlyWithTheSetupLink(t *testing.T) {
	for _, keep := range []bool{false, true} {
		t.Run(map[bool]string{false: "not kept", true: "kept"}[keep], func(t *testing.T) {
			app, store, repositoryRoot := newTestApp(t)
			app.Version = "9.9.9-test"
			accepted := 0
			app.OnHostAccepted = func() { accepted++ }
			noErr(t, store.PutBootstrap(context.Background(), "synthetic-owner-token", time.Now().Add(time.Hour)))
			browser := newHostBrowser(t, app, "192.168.1.20:7720")

			status, page := browser.do(http.MethodGet, "/setup", nil)
			if status != http.StatusOK || !strings.Contains(page, `action="/setup/redeem"`) {
				t.Fatalf("redemption page status=%d", status)
			}
			for _, secret := range []string{"git version test", "9.9.9-test", repositoryRoot, "OwnGit-Repositories", `name="storage_path"`} {
				if strings.Contains(page, secret) {
					t.Fatalf("redemption page reveals %q", secret)
				}
			}
			// Every file the page loads is served, and nothing else.
			assets := regexpAssets.FindAllStringSubmatch(page, -1)
			if len(assets) == 0 {
				t.Fatal("redemption page loads no assets")
			}
			for _, match := range assets {
				if status := browser.status(http.MethodGet, match[1], nil); status != http.StatusOK {
					t.Fatalf("asset %s status=%d", match[1], status)
				}
			}
			_, css := browser.do(http.MethodGet, "/assets/owngit.css", nil)
			for _, match := range regexpCSSURL.FindAllStringSubmatch(css, -1) {
				if status := browser.status(http.MethodGet, "/assets/"+match[1], nil); status != http.StatusOK {
					t.Fatalf("stylesheet file %s status=%d", match[1], status)
				}
			}
			for _, refused := range []struct{ method, path string }{
				{http.MethodGet, "/"}, {http.MethodHead, "/setup"}, {http.MethodGet, "/setup/approval"}, {http.MethodGet, "/settings"},
				{http.MethodGet, "/login"}, {http.MethodGet, "/api/v1/repositories"}, {http.MethodGet, "/git/demo.git/info/refs?service=git-upload-pack"},
				{http.MethodGet, "/assets/fonts/PRETENDARD-LICENSE.txt"}, {http.MethodGet, "/assets/"},
			} {
				if status := browser.status(refused.method, refused.path, nil); status != http.StatusMisdirectedRequest {
					t.Fatalf("%s %s status=%d, want 421", refused.method, refused.path, status)
				}
			}
			preauth := cookieValue(t, browser.jar, browser.server, preauthCookie)
			// No token, a wrong token, and the token on any other path.
			if status := browser.status(http.MethodPost, "/setup/redeem", url.Values{"csrf": {preauth}}); status != http.StatusForbidden {
				t.Fatalf("redeem without token status=%d", status)
			}
			if status := browser.status(http.MethodPost, "/setup/redeem", url.Values{"csrf": {preauth}, "token": {"wrong-token"}}); status != http.StatusForbidden {
				t.Fatalf("redeem with wrong token status=%d", status)
			}
			for _, path := range []string{"/setup", "/setup/approval", "/", "/login", "/repositories"} {
				if status := browser.status(http.MethodPost, path, url.Values{"csrf": {preauth}, "token": {"synthetic-owner-token"}}); status != http.StatusMisdirectedRequest {
					t.Fatalf("token posted to %s status=%d, want 421", path, status)
				}
			}
			if trusted := trustedHosts(t, store); len(trusted) != 0 || app.Hosts.Allows("192.168.1.20") {
				t.Fatal("a refused attempt changed the accepted Hosts")
			}

			page = browser.redeem("synthetic-owner-token")
			if !strings.Contains(page, `name="storage_path"`) || !strings.Contains(page, `<span class="mono">192.168.1.20</span>`) ||
				!strings.Contains(page, "only to finish setup") {
				t.Fatal("the redeemed session did not open the wizard with the keep offer for a setup-only address")
			}
			// The same session from a second unknown Host gets nothing more.
			other := newHostBrowser(t, app, "192.168.1.21:7720")
			setupToken := cookieValue(t, browser.jar, browser.server, setupCookie)
			otherURL, _ := url.Parse(other.server)
			other.jar.SetCookies(otherURL, []*http.Cookie{{Name: setupCookie, Value: setupToken, Path: "/"}})
			if _, page := other.do(http.MethodGet, "/setup", nil); strings.Contains(page, `name="storage_path"`) {
				t.Fatal("a second Host opened the wizard with the bound session")
			}
			session, _, err := store.Session(context.Background(), setupToken, "setup", time.Now())
			noErr(t, err)
			if status := other.status(http.MethodPost, "/setup", url.Values{"csrf": {session.CSRF}, "storage_path": {repositoryRoot},
				"access_mode": {"open"}, "admin_password": {"admin-password-one"}, "insecure_ack": {"on"}}); status != http.StatusMisdirectedRequest {
				t.Fatalf("finish from a second Host status=%d, want 421", status)
			}

			extra := url.Values{}
			if keep {
				extra.Set("keep_host", "on")
			}
			status = browser.finish(store, repositoryRoot, extra)
			settings, err := store.Settings(context.Background())
			noErr(t, err)
			if !settings.Initialized {
				t.Fatal("setup did not finish")
			}
			if keep {
				if status != http.StatusSeeOther || !app.Hosts.Allows("192.168.1.20:7720") || accepted != 1 ||
					!reflect.DeepEqual(trustedHosts(t, store), []string{"192.168.1.20"}) {
					t.Fatalf("kept: status=%d allowed=%v accepted=%d trusted=%v", status, app.Hosts.Allows("192.168.1.20"), accepted, trustedHosts(t, store))
				}
				if status := browser.status(http.MethodGet, "/", nil); status != http.StatusOK {
					t.Fatalf("kept Host dashboard status=%d", status)
				}
			} else {
				if status != http.StatusOK || app.Hosts.Allows("192.168.1.20") || accepted != 0 || len(trustedHosts(t, store)) != 0 {
					t.Fatalf("not kept: status=%d accepted=%d trusted=%v", status, accepted, trustedHosts(t, store))
				}
				for _, path := range []string{"/", "/setup", "/assets/owngit.css"} {
					if status := browser.status(http.MethodGet, path, nil); status != http.StatusMisdirectedRequest {
						t.Fatalf("after setup GET %s status=%d, want 421", path, status)
					}
				}
			}
			// After setup every other unknown Host is refused as before.
			for _, request := range []struct{ method, path string }{{http.MethodGet, "/setup"}, {http.MethodGet, "/assets/owngit.css"}, {http.MethodPost, "/setup/redeem"}} {
				if status := other.status(request.method, request.path, nil); status != http.StatusMisdirectedRequest {
					t.Fatalf("after setup %s %s from another Host status=%d", request.method, request.path, status)
				}
			}
		})
	}
}

// An expired link from an unknown Host is refused like any expired link, and
// its Host stays limited to the redemption page.
func TestUnknownHostWithAnExpiredSetupLink(t *testing.T) {
	app, store, _ := newTestApp(t)
	noErr(t, store.PutBootstrap(context.Background(), "synthetic-owner-token", time.Now().Add(-time.Minute)))
	browser := newHostBrowser(t, app, "192.168.1.20:7720")
	if status := browser.status(http.MethodGet, "/setup", nil); status != http.StatusOK {
		t.Fatalf("redemption page status=%d", status)
	}
	preauth := cookieValue(t, browser.jar, browser.server, preauthCookie)
	if status := browser.status(http.MethodPost, "/setup/redeem", url.Values{"csrf": {preauth}, "token": {"synthetic-owner-token"}}); status != http.StatusForbidden {
		t.Fatalf("expired redeem status=%d", status)
	}
	if _, page := browser.do(http.MethodGet, "/setup", nil); strings.Contains(page, `name="storage_path"`) {
		t.Fatal("an expired link opened the wizard")
	}
	if status := browser.status(http.MethodGet, "/", nil); status != http.StatusMisdirectedRequest {
		t.Fatalf("GET / status=%d", status)
	}
}

// Redeeming a new setup link on an accepted Host replaces the setup session,
// so a session redeemed earlier on an unknown Host no longer works there.
func TestRedemptionOnAnAcceptedHostEndsTheUnknownHostSession(t *testing.T) {
	app, store, repositoryRoot := newTestApp(t)
	noErr(t, store.PutBootstrap(context.Background(), "synthetic-owner-token", time.Now().Add(time.Hour)))
	remote := newHostBrowser(t, app, "192.168.1.20:7720")
	remote.redeem("synthetic-owner-token")
	session, ok, err := store.Session(context.Background(), cookieValue(t, remote.jar, remote.server, setupCookie), "setup", time.Now())
	if err != nil || !ok {
		t.Fatalf("remote setup session ok=%v err=%v", ok, err)
	}
	noErr(t, store.PutBootstrap(context.Background(), "synthetic-second-token", time.Now().Add(time.Hour)))
	local := newHostBrowser(t, app, "")
	local.redeem("synthetic-second-token")
	noErr(t, os.MkdirAll(repositoryRoot, 0o700))
	if status := remote.status(http.MethodPost, "/setup", url.Values{"csrf": {session.CSRF}, "storage_path": {repositoryRoot},
		"access_mode": {"open"}, "admin_password": {"admin-password-one"}, "insecure_ack": {"on"}}); status != http.StatusMisdirectedRequest {
		t.Fatalf("finish with a replaced session status=%d", status)
	}
	if status := local.finish(store, repositoryRoot, nil); status != http.StatusSeeOther {
		t.Fatalf("finish on the accepted Host status=%d", status)
	}
}

var (
	regexpAssets = regexp.MustCompile(`(?:href|src)="(/assets/[^"?]+)`)
	regexpCSSURL = regexp.MustCompile(`url\("\./([^"]+)"\)`)
)

// A setup link works once: after redemption, the same token is refused on the
// same unknown Host and on another, as on any accepted Host.
func TestUnknownHostCannotReuseARedeemedSetupLink(t *testing.T) {
	app, store, _ := newTestApp(t)
	noErr(t, store.PutBootstrap(context.Background(), "synthetic-owner-token", time.Now().Add(time.Hour)))
	first := newHostBrowser(t, app, "192.168.1.20:7720")
	first.redeem("synthetic-owner-token")
	for _, browser := range []*hostBrowser{first, newHostBrowser(t, app, "192.168.1.21:7720")} {
		// A fresh cookie jar, so the first browser's setup session is gone.
		_, jar := newBrowserClient(t)
		browser.client.Jar, browser.jar = jar, jar
		if status := browser.status(http.MethodGet, "/setup", nil); status != http.StatusOK {
			t.Fatalf("%s redemption page status=%d", browser.server, status)
		}
		preauth := cookieValue(t, browser.jar, browser.server, preauthCookie)
		if status := browser.status(http.MethodPost, "/setup/redeem", url.Values{"csrf": {preauth}, "token": {"synthetic-owner-token"}}); status != http.StatusForbidden {
			t.Fatalf("%s reused token status=%d, want 403", browser.server, status)
		}
		if _, page := browser.do(http.MethodGet, "/setup", nil); strings.Contains(page, `name="storage_path"`) {
			t.Fatalf("%s opened the wizard with a reused token", browser.server)
		}
	}
}

// While setup waits for approval in the terminal, an unknown Host still gets
// only the redemption page: it can neither see nor ask for approval.
func TestUnknownHostCannotUseTerminalApproval(t *testing.T) {
	app, store, _ := newTestApp(t)
	app.Approvals = NewSetupApprovals()
	app.Approvals.Open()
	noErr(t, store.PutBootstrap(context.Background(), "synthetic-owner-token", time.Now().Add(time.Hour)))
	local := newHostBrowser(t, app, "")
	if _, page := local.do(http.MethodGet, "/setup", nil); !strings.Contains(page, `action="/setup/approval"`) {
		t.Fatal("an accepted Host does not get the approval page")
	}
	remote := newHostBrowser(t, app, "192.168.1.20:7720")
	status, page := remote.do(http.MethodGet, "/setup", nil)
	if status != http.StatusOK || strings.Contains(page, `action="/setup/approval"`) || !strings.Contains(page, `action="/setup/redeem"`) {
		t.Fatalf("unknown Host during terminal approval: status=%d, approval form shown or redemption form missing", status)
	}
	preauth := cookieValue(t, remote.jar, remote.server, preauthCookie)
	for _, method := range []string{http.MethodGet, http.MethodPost} {
		if status := remote.status(method, "/setup/approval", url.Values{"csrf": {preauth}}); status != http.StatusMisdirectedRequest {
			t.Fatalf("%s /setup/approval from an unknown Host status=%d, want 421", method, status)
		}
	}
	if pending, ok := app.Approvals.Pending(); ok {
		t.Fatalf("an unknown Host created an approval request: %+v", pending)
	}
	// The setup link still works from that Host.
	if page := remote.redeem("synthetic-owner-token"); !strings.Contains(page, `name="storage_path"`) {
		t.Fatal("the setup link did not open the wizard during terminal approval")
	}
}
