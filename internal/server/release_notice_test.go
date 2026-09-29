package server

import (
	"context"
	"fmt"
	"html"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"owngit/internal/releasecheck"
	"owngit/internal/state"
	"owngit/internal/webui"
)

// releaseEndpoint is a local stand-in for GitHub whose tag the test chooses.
type releaseEndpoint struct {
	tag  atomic.Value
	hits atomic.Int32
}

func newReleaseEndpoint(t *testing.T, tag string) (*releaseEndpoint, string) {
	t.Helper()
	endpoint := &releaseEndpoint{}
	endpoint.tag.Store(tag)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		endpoint.hits.Add(1)
		fmt.Fprintf(writer, `{"tag_name":%q,"draft":false,"prerelease":false}`, endpoint.tag.Load().(string))
	}))
	t.Cleanup(server.Close)
	return endpoint, server.URL
}

// releaseApp is an open-mode installation whose release check reads a local
// endpoint. It never contacts GitHub.
func releaseApp(t *testing.T, tag string) (*App, *state.Store, *releaseEndpoint, *httptest.Server) {
	t.Helper()
	app, store, repositoryRoot := newTestApp(t)
	noErr(t, os.MkdirAll(repositoryRoot, 0o700))
	canonical, _ := filepath.EvalSymlinks(repositoryRoot)
	adminHash := fixturePasswordHash(t, "admin-password")
	noErr(t, store.CompleteSetup(context.Background(), canonical, "open", "", adminHash, true))
	app.Repositories.SetRoot(canonical)
	app.Version = "1.0.2"
	endpoint, endpointURL := newReleaseEndpoint(t, tag)
	app.Releases = &releasecheck.Checker{
		Current: app.Version, URL: endpointURL,
		Enabled: func(ctx context.Context) (bool, error) {
			settings, err := store.Settings(ctx)
			return settings.UpdateCheck, err
		},
	}
	return app, store, endpoint, serve(t, app.Handler())
}

func TestReleaseNoticeAppearsOnlyForANewerRelease(t *testing.T) {
	for _, tc := range []struct {
		tag   string
		shown bool
	}{
		{"v1.0.3", true},
		{"v1.0.2", false},
		{"v1.0.1", false},
		{"v1.0.3-rc.1", false},
		{"nonsense", false},
	} {
		app, _, _, server := releaseApp(t, tc.tag)
		_ = app.Releases.Check(context.Background())
		client, _ := newBrowserClient(t)
		body, status := dashboardGET(t, client, server.URL+"/")
		if status != http.StatusOK {
			t.Fatalf("%s: dashboard status=%d", tc.tag, status)
		}
		if got := strings.Contains(body, "data-release-notice"); got != tc.shown {
			t.Errorf("%s: notice shown=%v, want %v", tc.tag, got, tc.shown)
		}
		if tc.shown {
			for _, want := range []string{
				"OwnGit 1.0.3 is available. You are running 1.0.2.",
				`href="https://github.com/juliankang4/owngit/releases/tag/v1.0.3"`,
				`href="` + releasecheck.UpdateGuideURL + `"`,
				`action="` + releaseDismissPath + `"`,
			} {
				if !strings.Contains(body, want) {
					t.Errorf("%s: dashboard lacks %q", tc.tag, want)
				}
			}
			// The notice leads the dashboard, above the activity graph.
			if strings.Index(body, "data-release-notice") > strings.Index(body, "data-graph") {
				t.Errorf("%s: the notice is not above the activity graph", tc.tag)
			}
		}
	}
}

func TestReleaseNoticeIsNotShownWithoutAChecker(t *testing.T) {
	app, _, _, server := releaseApp(t, "v1.0.3")
	_ = app.Releases.Check(context.Background())
	app.Releases = nil // as with --no-update-check
	client, _ := newBrowserClient(t)
	if body, _ := dashboardGET(t, client, server.URL+"/"); strings.Contains(body, "data-release-notice") {
		t.Fatal("a notice appeared although the check is disabled by the start option")
	}
}

func TestDismissHidesOneVersionInThisBrowser(t *testing.T) {
	app, _, endpoint, server := releaseApp(t, "v1.0.3")
	noErr(t, app.Releases.Check(context.Background()))
	client, jar := newBrowserClient(t)
	body, _ := dashboardGET(t, client, server.URL+"/")
	if !strings.Contains(body, "data-release-notice") {
		t.Fatal("the notice is missing before dismissal")
	}
	token := cookieValue(t, jar, server.URL, generalCookie)

	// Without a valid CSRF token nothing is stored.
	response := request(t, client, http.MethodPost, server.URL+releaseDismissPath, url.Values{"csrf": {"wrong"}, "version": {"1.0.3"}}, server.URL)
	if response.StatusCode != http.StatusForbidden {
		t.Fatalf("dismiss with a wrong token status=%d", response.StatusCode)
	}
	// A value that is not a version is refused.
	response = request(t, client, http.MethodPost, server.URL+releaseDismissPath, url.Values{"csrf": {token}, "version": {"1.0.3; Path=/x"}}, server.URL)
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("dismiss with a malformed version status=%d", response.StatusCode)
	}
	if body, _ := dashboardGET(t, client, server.URL+"/"); !strings.Contains(body, "data-release-notice") {
		t.Fatal("a refused dismissal hid the notice")
	}

	response = request(t, client, http.MethodPost, server.URL+releaseDismissPath, url.Values{"csrf": {token}, "version": {"1.0.3"}}, server.URL)
	if response.StatusCode != http.StatusSeeOther || response.Header.Get("Location") != "/" {
		t.Fatalf("dismiss status=%d location=%q", response.StatusCode, response.Header.Get("Location"))
	}
	if body, _ := dashboardGET(t, client, server.URL+"/"); strings.Contains(body, "data-release-notice") {
		t.Fatal("the dismissed version is still announced")
	}

	// Another browser still sees it.
	other, _ := newBrowserClient(t)
	if body, _ := dashboardGET(t, other, server.URL+"/"); !strings.Contains(body, "data-release-notice") {
		t.Fatal("dismissal in one browser hid the notice in another")
	}

	// A newer release is announced again in the browser that dismissed.
	endpoint.tag.Store("v1.0.4")
	noErr(t, app.Releases.Check(context.Background()))
	body, _ = dashboardGET(t, client, server.URL+"/")
	if !strings.Contains(body, "OwnGit 1.0.4 is available.") {
		t.Fatal("a newer release after a dismissal is not announced")
	}
}

func TestUpdateCheckSettingNeedsTheAdministratorAndTakesEffect(t *testing.T) {
	app, store, endpoint, server := releaseApp(t, "v1.0.3")
	noErr(t, app.Releases.Check(context.Background()))
	client, jar := newBrowserClient(t)

	body, status := dashboardGET(t, client, server.URL+"/settings")
	if status != http.StatusOK {
		t.Fatalf("settings status=%d", status)
	}
	// Visible to everyone, with the change behind the administrator password.
	if !strings.Contains(body, `value="`+webui.ActionSetUpdateCheck+`"`) || !updateSwitchOn(t, body) {
		t.Fatal("settings does not show the update check switch turned on")
	}
	token := cookieValue(t, jar, server.URL, generalCookie)
	turnOff := url.Values{"csrf": {token}, "action": {webui.ActionSetUpdateCheck}, "update_check": {"off"}}

	response := request(t, client, http.MethodPost, server.URL+"/settings", turnOff, server.URL)
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("turning off without the administrator password status=%d", response.StatusCode)
	}
	turnOff.Set("admin_password", "wrong-password")
	response = request(t, client, http.MethodPost, server.URL+"/settings", turnOff, server.URL)
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("turning off with a wrong administrator password status=%d", response.StatusCode)
	}
	if settings, _ := store.Settings(context.Background()); !settings.UpdateCheck {
		t.Fatal("the setting changed without administrator confirmation")
	}

	turnOff.Set("admin_password", "admin-password")
	response = request(t, client, http.MethodPost, server.URL+"/settings", turnOff, server.URL)
	if response.StatusCode != http.StatusSeeOther {
		t.Fatalf("turning off status=%d", response.StatusCode)
	}
	if settings, _ := store.Settings(context.Background()); settings.UpdateCheck {
		t.Fatal("turning off was not saved")
	}
	// Off hides the current notice at once and stops further requests.
	if body, _ := dashboardGET(t, client, server.URL+"/"); strings.Contains(body, "data-release-notice") {
		t.Fatal("the notice is still shown after the check was turned off")
	}
	before := endpoint.hits.Load()
	app.Releases.CheckIfEnabled(context.Background())
	if endpoint.hits.Load() != before {
		t.Fatal("a check turned off in Settings still made a request")
	}
	if body, _ := dashboardGET(t, client, server.URL+"/settings"); updateSwitchOn(t, body) {
		t.Fatal("settings does not show the check turned off")
	}

	// An unknown value is refused.
	bad := url.Values{"csrf": {token}, "action": {webui.ActionSetUpdateCheck}, "update_check": {"sometimes"}, "admin_password": {"admin-password"}}
	if response := request(t, client, http.MethodPost, server.URL+"/settings", bad, server.URL); response.StatusCode != http.StatusBadRequest {
		t.Fatalf("unknown value status=%d", response.StatusCode)
	}

	turnOn := url.Values{"csrf": {token}, "action": {webui.ActionSetUpdateCheck}, "update_check": {"on"}, "admin_password": {"admin-password"}}
	if response := request(t, client, http.MethodPost, server.URL+"/settings", turnOn, server.URL); response.StatusCode != http.StatusSeeOther {
		t.Fatalf("turning on status=%d", response.StatusCode)
	}
	app.Releases.CheckIfEnabled(context.Background())
	if body, _ := dashboardGET(t, client, server.URL+"/"); !strings.Contains(body, "data-release-notice") {
		t.Fatal("turning the check back on did not bring the notice back")
	}
}

func TestSettingsReportTheStartOptionOverride(t *testing.T) {
	app, store, _, server := releaseApp(t, "v1.0.3")
	app.Releases = nil // as with --no-update-check
	noErr(t, store.SetUpdateCheck(context.Background(), true))
	client, _ := newBrowserClient(t)
	body, _ := dashboardGET(t, client, server.URL+"/settings")
	for _, want := range []webui.MessageCode{webui.MsgSettingsUpdateForced, webui.MsgSettingsUpdateSavedOn} {
		if !strings.Contains(body, webui.Text(webui.LangEN, want)) {
			t.Errorf("settings lacks %q", webui.Text(webui.LangEN, want))
		}
	}
}

// updateSwitchOn reports whether the update check switch of a Settings
// page is turned on.
func updateSwitchOn(t *testing.T, body string) bool {
	t.Helper()
	at := strings.Index(body, `id="upd-check"`)
	if at < 0 {
		t.Fatal("settings has no update check switch")
	}
	start := strings.LastIndex(body[:at], "<")
	tag := body[start : start+strings.Index(body[start:], ">")]
	if !strings.Contains(tag, `name="update_check" value="on"`) {
		t.Fatalf("the update check switch does not send on: %s", tag)
	}
	return strings.Contains(tag, " checked")
}

// The identity in the toolbar leads to the dashboard on every kind of page,
// including setup and error pages that have no sidebar.
func TestLogoLinksToTheDashboard(t *testing.T) {
	_, _, _, server := releaseApp(t, "v1.0.2")
	client, _ := newBrowserClient(t)
	for _, path := range []string{"/", "/settings", "/activity", "/repositories/new", "/no-such-page"} {
		body, _ := dashboardGET(t, client, server.URL+path)
		if !strings.Contains(body, `<a class="toolbar__home" href="/" aria-label="OwnGit home"`) {
			t.Errorf("%s: the logo does not link to the dashboard", path)
		}
	}

	fresh, _, _ := newTestApp(t)
	setupServer := serve(t, fresh.Handler())
	body, _ := dashboardGET(t, client, setupServer.URL+"/setup")
	if !strings.Contains(body, `<a class="toolbar__home" href="/"`) {
		t.Error("/setup: the logo does not link home")
	}
}

// The notice offers the update command of this installation to copy; the
// server never runs it. The command holds this computer's paths, so only a
// confirmed administrator sees it; everyone else is told where to get it.
// Without a command, only the guide link remains.
func TestReleaseNoticeShowsTheUpdateCommandToTheAdministrator(t *testing.T) {
	for _, tc := range []struct {
		command, start string
		restart        bool
		note           string
	}{
		{"brew upgrade owngit && '/x y/owngit' service install", "", false, "Run this in a terminal on the computer where OwnGit runs."},
		{"npm install -g owngit@1.0.3", "", true, "then restart OwnGit."},
		{"Expand-Archive 'z.zip' 'C:\\new'", "C:\\new\\owngit.exe", false, "then start OwnGit from C:\\new\\owngit.exe."},
		{"", "", false, ""},
	} {
		app, _, _, server := releaseApp(t, "v1.0.3")
		app.UpdateCommand = func(version string) (string, string, bool) {
			if version != "1.0.3" {
				t.Errorf("command asked for %q", version)
			}
			return tc.command, tc.start, tc.restart
		}
		_ = app.Releases.Check(context.Background())
		client, jar := newBrowserClient(t)
		body, _ := dashboardGET(t, client, server.URL+"/")
		if strings.Contains(body, `data-copy="release-command"`) || strings.Contains(body, "/x y/owngit") || strings.Contains(body, "C:\\new") {
			t.Errorf("%q: a visitor who is not a confirmed administrator sees the command", tc.command)
		}
		if hidden := strings.Contains(body, "only the administrator sees it here"); hidden != (tc.command != "") {
			t.Errorf("%q: visitor note shown=%v", tc.command, hidden)
		}
		if tc.command != "" && !strings.Contains(body, `href="/admin/login?next=`) {
			t.Errorf("%q: the visitor has no way to confirm as administrator", tc.command)
		}

		browserGET(t, client, server.URL+"/admin/login")
		browserForm(t, client, server.URL+"/admin/login", url.Values{"csrf": {cookieValue(t, jar, server.URL, preauthCookie)}, "admin_password": {"admin-password"}, "next": {"/"}}, server.URL)
		body, _ = dashboardGET(t, client, server.URL+"/")
		if shown := strings.Contains(body, `data-copy="release-command"`); shown != (tc.command != "") {
			t.Errorf("%q: administrator copy field shown=%v", tc.command, shown)
		}
		if tc.command != "" && (!strings.Contains(body, `value="`+html.EscapeString(tc.command)+`"`) || !strings.Contains(body, tc.note)) {
			t.Errorf("%q: the escaped command or the note %q is missing", tc.command, tc.note)
		}
		if strings.Contains(body, "only the administrator sees it here") {
			t.Errorf("%q: the administrator is told to confirm", tc.command)
		}
		if !strings.Contains(body, `href="`+releasecheck.UpdateGuideURL+`"`) {
			t.Errorf("%q: the guide link is missing", tc.command)
		}
	}
}
