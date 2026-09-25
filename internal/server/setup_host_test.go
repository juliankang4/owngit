package server

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"reflect"
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
		if strings.Contains(page, `name="keep_host" value="1" checked`) {
			t.Fatal("the keep-host box starts ticked")
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
