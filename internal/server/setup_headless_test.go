package server

import (
	"context"
	"html/template"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strings"
	"testing"
	"time"

	"owngit/internal/state"
	"owngit/internal/webui"
)

// browserFrom drives setup through a server that sees every connection as
// coming from peer, as a device at that address would. A host other than ""
// replaces the Host and Origin the browser sends, as when the device used
// that address.
func browserFrom(t *testing.T, app *App, peer string, host ...string) *hostBrowser {
	t.Helper()
	handler := app.Handler()
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		request.RemoteAddr = peer
		if len(host) == 1 {
			request.Host = host[0]
			if request.Header.Get("Origin") != "" {
				request.Header.Set("Origin", "http://"+host[0])
			}
		}
		handler.ServeHTTP(writer, request)
	}))
	t.Cleanup(server.Close)
	client, jar := newBrowserClient(t)
	t.Cleanup(client.CloseIdleConnections)
	return &hostBrowser{t: t, client: client, jar: jar, server: server.URL}
}

const keptBox = `name="keep_host" value="1" checked`

// On a computer without a screen, the address setup was opened by stays
// ticked to keep; elsewhere the box starts unticked.
func TestHeadlessSetupKeepsTheAddressByDefault(t *testing.T) {
	for _, headless := range []bool{true, false} {
		app, store, _ := newTestApp(t)
		if headless {
			app.HeadlessListen = HeadlessListenAddress
		}
		noErr(t, store.PutBootstrap(context.Background(), "synthetic-owner-token", time.Now().Add(time.Hour)))
		page := newHostBrowser(t, app, "192.168.1.20:7654").redeem("synthetic-owner-token")
		if !strings.Contains(page, `name="keep_host"`) {
			t.Fatalf("headless=%v: the wizard does not offer to keep the address", headless)
		}
		if got := strings.Contains(page, keptBox); got != headless {
			t.Fatalf("headless=%v: keep box ticked=%v", headless, got)
		}
	}
}

// Setup on a computer without a screen that keeps no address saves the
// local listen address, so the next start listens only where OwnGit still
// answers, and the page says so. Keeping the address, a saved base URL, an
// allowed Host, or an owner-chosen listen address leave it alone.
func TestHeadlessSetupWithoutAKeptAddressListensLocally(t *testing.T) {
	cases := []struct {
		name       string
		headless   bool
		keep       bool
		prepare    func(*state.Store)
		wantListen string
	}{
		{"headless, not kept", true, false, nil, DefaultListenAddress},
		{"headless, kept", true, true, nil, HeadlessListenAddress},
		{"not headless", false, false, nil, HeadlessListenAddress},
		{"headless, base URL saved", true, false, func(store *state.Store) {
			noErr(t, store.UpdateNetwork(context.Background(), state.NetworkUpdate{Settings: state.NetworkSettings{Listen: HeadlessListenAddress, BaseURL: "http://gitbox.test:7654"}}))
		}, HeadlessListenAddress},
		{"headless, allowed Host saved", true, false, func(store *state.Store) {
			noErr(t, store.AddTrustedHost(context.Background(), "gitbox.test"))
		}, HeadlessListenAddress},
		{"headless, another listen address saved", true, false, func(store *state.Store) {
			noErr(t, store.UpdateNetwork(context.Background(), state.NetworkUpdate{Settings: state.NetworkSettings{Listen: "0.0.0.0:7720"}}))
		}, "0.0.0.0:7720"},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			app, store, repositoryRoot := newTestApp(t)
			noErr(t, store.UpdateNetwork(context.Background(), state.NetworkUpdate{Settings: state.NetworkSettings{Listen: HeadlessListenAddress}}))
			if test.prepare != nil {
				test.prepare(store)
			}
			if test.headless {
				app.HeadlessListen = HeadlessListenAddress
			}
			noErr(t, store.PutBootstrap(context.Background(), "synthetic-owner-token", time.Now().Add(time.Hour)))
			browser := newHostBrowser(t, app, "192.168.1.20:7654")
			browser.redeem("synthetic-owner-token")
			extra := url.Values{"keep_host": {""}}
			if test.keep {
				extra.Set("keep_host", "on")
			}
			status, page := browser.finishPage(store, repositoryRoot, extra)
			if status != http.StatusOK && status != http.StatusSeeOther {
				t.Fatalf("finish status=%d", status)
			}
			saved, err := store.NetworkSettings(context.Background())
			noErr(t, err)
			if saved.Listen != test.wantListen {
				t.Fatalf("saved listen %q, want %q", saved.Listen, test.wantListen)
			}
			localOnly := strings.Contains(page, template.HTMLEscapeString(webui.Text(webui.LangEN, webui.MsgSetupDoneLocalOnlyHint)))
			if want := test.wantListen == DefaultListenAddress; localOnly != want {
				t.Fatalf("done page says local only=%v, want %v:\n%s", localOnly, want, page)
			}
		})
	}
}

// Setup opened from a public Internet address selects the shared password
// and says why; a private, tailnet or loopback peer keeps open access
// selected.
func TestSetupFromAPublicAddressSelectsTheSharedPassword(t *testing.T) {
	note := template.HTMLEscapeString(webui.Text(webui.LangEN, webui.MsgSetupPublicNetwork))
	for peer, public := range map[string]bool{
		"203.0.113.9:40000":       true,
		"[2001:db8::9]:40000":     true,
		"192.168.1.30:40000":      false,
		"100.64.0.1:40000":        false,
		"[fd00::9]:40000":         false,
		"[fe80::1%eth0]:40000":    false,
		"127.0.0.1:40000":         false,
		"[::ffff:10.0.0.9]:40000": false,
	} {
		app, store, _ := newTestApp(t)
		noErr(t, store.PutBootstrap(context.Background(), "synthetic-owner-token", time.Now().Add(time.Hour)))
		page := browserFrom(t, app, peer).redeem("synthetic-owner-token")
		password := strings.Contains(page, `name="access_mode" value="password" checked`)
		if password != public || strings.Contains(page, note) != public {
			t.Errorf("peer %s: password selected=%v, note shown=%v, want %v", peer, password, strings.Contains(page, note), public)
		}
	}
}

func TestSetupWithUnknownForwardedClientSelectsTheSharedPassword(t *testing.T) {
	app, store, _ := newTestApp(t)
	app.Network = NewLiveNetwork(LiveNetworkConfig{
		Proxies: []netip.Prefix{netip.MustParsePrefix("127.0.0.1/32")}, Hosts: app.Hosts,
	})
	noErr(t, store.PutBootstrap(context.Background(), "synthetic-owner-token", time.Now().Add(time.Hour)))
	page := browserFrom(t, app, "127.0.0.1:40000").redeem("synthetic-owner-token")
	if !strings.Contains(page, `name="access_mode" value="password" checked`) ||
		!strings.Contains(page, "Forwarded request, original address unknown") {
		t.Fatalf("unknown forwarded client did not get the safe default and warning:\n%s", page)
	}
	if got := webui.Text(webui.LangKO, webui.MsgForwardedClientUnknown); got != "프록시를 거쳐 온 접속, 원래 주소 확인 불가" {
		t.Fatalf("Korean unknown-client warning %q", got)
	}
}

// finishPage is finish that also returns the page it ends on.
func (browser *hostBrowser) finishPage(store *state.Store, repositoryRoot string, extra url.Values) (int, string) {
	browser.t.Helper()
	session, ok, err := store.Session(context.Background(), cookieValue(browser.t, browser.jar, browser.server, setupCookie), "setup", time.Now())
	if err != nil || !ok {
		browser.t.Fatalf("setup session ok=%v err=%v", ok, err)
	}
	values := url.Values{"csrf": {session.CSRF}, "storage_path": {repositoryRoot}, "access_mode": {"open"},
		"admin_password": {"admin-password-one"}, "insecure_ack": {"on"}}
	for key, value := range extra {
		values[key] = value
	}
	return browser.do(http.MethodPost, "/setup", values)
}

// On a computer without a screen the keep-address box starts ticked for a
// private peer and unticked for a public one, which matches the notice.
func TestHeadlessKeepDefaultFollowsThePeer(t *testing.T) {
	for peer, ticked := range map[string]bool{"192.168.1.30:40000": true, "203.0.113.9:40000": false} {
		app, store, _ := newTestApp(t)
		app.HeadlessListen = HeadlessListenAddress
		noErr(t, store.PutBootstrap(context.Background(), "synthetic-owner-token", time.Now().Add(time.Hour)))
		page := browserFrom(t, app, peer, "192.168.1.20:7654").redeem("synthetic-owner-token")
		if !strings.Contains(page, `name="keep_host"`) {
			t.Fatalf("peer %s: the keep box is not offered", peer)
		}
		if got := strings.Contains(page, keptBox); got != ticked {
			t.Errorf("peer %s: keep box ticked=%v, want %v", peer, got, ticked)
		}
	}
}
