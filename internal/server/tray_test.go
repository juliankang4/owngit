package server

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"owngit/internal/state"
	"owngit/internal/webui"
)

const (
	trayTestToken = "synthetic-tray-token"
	trayTestProof = "synthetic-tray-proof"
)

// trayNonce is the nonce of trayGET's requests.
const trayNonce = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"

// trayGET asks the tray status of handler as a program on this computer
// would, with token, and decodes the answer.
func trayGET(t *testing.T, handler http.Handler, token string, change func(*http.Request)) (int, TrayStatus, string) {
	t.Helper()
	var status TrayStatus
	code, body, errorCode := trayRead(t, handler, TrayStatusPath, token, change)
	if code == http.StatusOK {
		noErr(t, json.Unmarshal(body, &status))
	}
	return code, status, errorCode
}

// trayRead asks path of handler as a program on this computer would, with
// token, and checks the proof of an answer. It returns the body and, for a
// refusal, its error code.
func trayRead(t *testing.T, handler http.Handler, path, token string, change func(*http.Request)) (int, []byte, string) {
	t.Helper()
	request := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:7654"+path, nil)
	request.RemoteAddr = "127.0.0.1:50000"
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	request.Header.Set(state.TrayNonceHeader, trayNonce)
	if change != nil {
		change(request)
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	var failure struct {
		Error struct{ Code string } `json:"error"`
	}
	body := recorder.Body.Bytes()
	if recorder.Code == http.StatusOK {
		if proof := recorder.Header().Get(state.TrayProofHeader); proof != state.TrayProof(trayTestProof, request.Header.Get(state.TrayNonceHeader), body) {
			t.Fatalf("the answer carries the proof %q", proof)
		}
	} else {
		noErr(t, json.Unmarshal(body, &failure))
	}
	return recorder.Code, body, failure.Error.Code
}

// Git pushes through the server become the tray's recent pushes, newest
// first, with the actor and the time OwnGit received them, and a newer
// release makes the state ask for attention with this install's command.
func TestTrayStatusListsPushesThroughTheServer(t *testing.T) {
	app, store, _, server := releaseApp(t, "v1.0.3")
	noErr(t, app.Releases.Check(context.Background()))
	app.TrayToken, app.TrayProof = trayTestToken, trayTestProof
	app.GitHTTP.OnPush = app.RecordPush
	app.UpdateCommand = func(version string) (string, string, bool) { return "brew upgrade owngit", "", false }
	start := time.Now().Add(-time.Second)
	for _, name := range []string{"notes", "site"} {
		_, err := app.Repositories.Create(context.Background(), name, "")
		noErr(t, err)
	}
	work := filepath.Join(t.TempDir(), "work")
	apiRunGit(t, "", "init", "--initial-branch=main", work)
	apiRunGit(t, work, "config", "user.name", "Tray Test")
	apiRunGit(t, work, "config", "user.email", "tray@example.invalid")
	// A commit dated long ago: the push time is when OwnGit received it.
	noErr(t, os.WriteFile(filepath.Join(work, "file.txt"), []byte("first\n"), 0o600))
	apiRunGit(t, work, "add", ".")
	apiRunGit(t, work, "-c", "user.useConfigOnly=true", "commit", "--date=2001-02-03T04:05:06Z", "-m", "old")
	apiRunGit(t, work, "push", server.URL+"/git/notes.git", "HEAD:refs/heads/main")
	apiRunGit(t, work, "push", server.URL+"/git/site.git", "HEAD:refs/heads/main", "HEAD:refs/heads/work")
	apiRunGit(t, work, "tag", "v1")
	apiRunGit(t, work, "push", server.URL+"/git/notes.git", "refs/tags/v1")
	// Up to date: nothing is recorded.
	apiRunGit(t, work, "push", server.URL+"/git/site.git", "HEAD:refs/heads/main")
	apiRunGit(t, work, "commit", "--allow-empty", "-m", "second")
	apiRunGit(t, work, "push", server.URL+"/git/site.git", "HEAD:refs/heads/main")

	request, err := http.NewRequest(http.MethodGet, server.URL+TrayStatusPath, nil)
	noErr(t, err)
	request.Header.Set("Authorization", "Bearer "+trayTestToken)
	request.Header.Set(state.TrayNonceHeader, trayNonce)
	response, err := http.DefaultClient.Do(request)
	noErr(t, err)
	defer response.Body.Close()
	var status TrayStatus
	noErr(t, json.NewDecoder(response.Body).Decode(&status))
	if response.StatusCode != http.StatusOK || response.Header.Get("Cache-Control") != "no-store" {
		t.Fatalf("status %d, Cache-Control %q", response.StatusCode, response.Header.Get("Cache-Control"))
	}
	if status.State != "attention" || !status.Shown || status.Update == nil || status.Update.Version != "1.0.3" || status.Update.Command != "brew upgrade owngit" {
		t.Fatalf("state %q update %+v", status.State, status.Update)
	}
	if status.CloneAddress != server.URL+"/git/" || status.DashboardURL != server.URL || status.Version != "1.0.2" {
		t.Fatalf("addresses %q %q version %q", status.DashboardURL, status.CloneAddress, status.Version)
	}
	want := []struct {
		repository, ref, branch string
		refs                    int
	}{{"site", "refs/heads/main", "main", 1}, {"notes", "refs/tags/v1", "", 1}, {"site", "refs/heads/main", "main", 2}}
	if len(status.Pushes) != 3 {
		t.Fatalf("pushes %+v", status.Pushes)
	}
	for index, push := range status.Pushes {
		expected := want[index]
		if push.Repository != expected.repository || push.Ref != expected.ref || push.Branch != expected.branch || push.RefsUpdated != expected.refs ||
			push.Actor != (state.Actor{Kind: state.ActorAccess}) || push.ActorLabel != "General access" || push.PushedAt.Before(start.Truncate(time.Second)) {
			t.Errorf("push %d = %+v, want %+v", index, push, expected)
		}
	}
	events, err := store.RecentPushes(context.Background(), 10)
	noErr(t, err)
	if len(events) != 4 {
		t.Fatalf("recorded %d pushes, want 4", len(events))
	}
}

// Only a program with the private tray token reads status or events. The
// token remains authoritative when loopback is also configured as a trusted
// proxy, and the liveness check stays empty.
func TestTrayStatusRequiresThePrivateToken(t *testing.T) {
	app := newConfiguredApp(t)
	noErr(t, app.Hosts.Add("owngit.example"))
	app.Network = NewLiveNetwork(LiveNetworkConfig{
		Proxies: []netip.Prefix{netip.MustParsePrefix("127.0.0.1/32")}, Hosts: app.Hosts,
	})
	handler := app.Handler()
	for _, path := range []string{TrayStatusPath, TrayEventsPath} {
		app.TrayToken, app.TrayProof = "", ""
		if code, _, _ := trayRead(t, handler, path, trayTestToken, nil); code != http.StatusNotFound {
			t.Fatalf("%s without a published token: %d, want 404", path, code)
		}
		app.TrayToken, app.TrayProof = trayTestToken, trayTestProof
		for name, test := range map[string]struct {
			token  string
			change func(*http.Request)
			code   int
			error  string
		}{
			"no token":    {"", nil, http.StatusUnauthorized, "unauthorized"},
			"wrong token": {"another-token", nil, http.StatusUnauthorized, "unauthorized"},
			"basic auth": {"", func(request *http.Request) { request.SetBasicAuth("owngit", trayTestToken) },
				http.StatusUnauthorized, "unauthorized"},
			"token from another address": {trayTestToken, func(request *http.Request) {
				request.Host, request.RemoteAddr = "owngit.example:7654", "192.0.2.10:50000"
			}, http.StatusOK, ""},
			"token through a proxy": {trayTestToken, func(request *http.Request) {
				request.Header.Set("X-Forwarded-For", "192.0.2.10")
			}, http.StatusOK, ""},
			"post":        {trayTestToken, func(request *http.Request) { request.Method = http.MethodPost }, http.StatusMethodNotAllowed, "method_not_allowed"},
			"no nonce":    {trayTestToken, func(request *http.Request) { request.Header.Del(state.TrayNonceHeader) }, http.StatusBadRequest, "invalid_nonce"},
			"short nonce": {trayTestToken, func(request *http.Request) { request.Header.Set(state.TrayNonceHeader, "AAAA") }, http.StatusBadRequest, "invalid_nonce"},
			"this computer by its own address": {trayTestToken, func(request *http.Request) {
				request.Host, request.RemoteAddr = "owngit.example:7654", "192.0.2.5:50000"
				local := &net.TCPAddr{IP: net.ParseIP("192.0.2.5"), Port: 7654}
				*request = *request.WithContext(context.WithValue(request.Context(), http.LocalAddrContextKey, local))
			}, http.StatusOK, ""},
		} {
			code, _, errorCode := trayRead(t, handler, path, test.token, test.change)
			if code != test.code || errorCode != test.error {
				t.Errorf("%s %s: %d %q, want %d %q", path, name, code, errorCode, test.code, test.error)
			}
		}
	}
	request := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:7654"+HealthPath, nil)
	request.RemoteAddr = "127.0.0.1:50000"
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK || recorder.Body.Len() != 0 {
		t.Fatalf("health answered %d with %q", recorder.Code, recorder.Body.String())
	}
}

// Running, attention and unavailable are distinct answers. A check that
// could not run is listed but asks for no attention, and the checkup is
// reused for a minute.
func TestTrayStatusStates(t *testing.T) {
	notSetUp, _, _ := newTestApp(t)
	notSetUp.TrayToken, notSetUp.TrayProof = trayTestToken, trayTestProof
	if code, status, _ := trayGET(t, notSetUp.Handler(), trayTestToken, nil); code != http.StatusOK || status.State != "attention" || !status.SetupRequired {
		t.Fatalf("before setup: %d %+v", code, status)
	}

	app := newConfiguredApp(t)
	app.TrayToken, app.TrayProof = trayTestToken, trayTestProof
	now := time.Unix(1_900_000_000, 0)
	app.Now = func() time.Time { return now }
	runs := 0
	findings := []webui.Finding{{Code: webui.MsgDoctorUncheckedFirewall, Args: []string{"ufw needs root"}, Unchecked: true}}
	app.Diagnose = func(context.Context) []webui.Finding { runs++; return findings }
	code, status, _ := trayGET(t, app.Handler(), trayTestToken, nil)
	if code != http.StatusOK || status.State != "running" || status.SetupRequired || status.Update != nil || len(status.Findings) != 1 || len(status.Pushes) != 0 {
		t.Fatalf("running: %d %+v", code, status)
	}
	findings = []webui.Finding{{Code: webui.MsgDoctorUncheckedFirewall, Args: []string{"ufw needs root"}}}
	if _, status, _ := trayGET(t, app.Handler(), trayTestToken, nil); status.State != "running" || runs != 1 {
		t.Fatalf("within a minute the checkup ran %d times, state %q", runs, status.State)
	}
	now = now.Add(trayCheckupReuse)
	_, status, _ = trayGET(t, app.Handler(), trayTestToken, func(request *http.Request) { request.URL.RawQuery = "lang=ko" })
	if status.State != "attention" || runs != 2 || status.Findings[0].Message != (webui.Finding{Code: webui.MsgDoctorUncheckedFirewall, Args: []string{"ufw needs root"}}).Sentence(webui.LangKO) {
		t.Fatalf("after a minute: runs %d, %+v", runs, status)
	}

	// A request that ends during the checkup does not end the checkup,
	// whose result the next minute reuses.
	now = now.Add(trayCheckupReuse)
	ctx, cancel := context.WithCancel(context.Background())
	var checkupCancelled bool
	app.Diagnose = func(checkup context.Context) []webui.Finding {
		cancel()
		checkupCancelled = checkup.Err() != nil
		return findings
	}
	trayGET(t, app.Handler(), trayTestToken, func(request *http.Request) { *request = *request.WithContext(ctx) })
	if checkupCancelled {
		t.Fatal("the checkup ended with the request that asked for it")
	}

	// Every field is present, also when false or empty, for clients that
	// decode into fixed types.
	if encoded, _ := json.Marshal(TrayFinding{}); !strings.Contains(string(encoded), `"unchecked":false`) || !strings.Contains(string(encoded), `"repair":""`) {
		t.Errorf("a finding omits fields: %s", encoded)
	}
	if encoded, _ := json.Marshal(TrayUpdate{}); !strings.Contains(string(encoded), `"restart":false`) || !strings.Contains(string(encoded), `"start":""`) {
		t.Errorf("an update omits fields: %s", encoded)
	}

	// A stored push that cannot be read makes the status unavailable, not
	// empty and not stopped.
	_, err := app.Repositories.Create(context.Background(), "notes", "")
	noErr(t, err)
	noErr(t, app.Store.Exec(context.Background(), `INSERT INTO push_events(repository_id,ref_name,old_oid,new_oid,refs_updated,actor,pushed_at) VALUES('notes','refs/heads/main','','`+strings.Repeat("a", 40)+`',1,'{"kind":"someone"}',1)`))
	if code, _, errorCode := trayGET(t, app.Handler(), trayTestToken, nil); code != http.StatusServiceUnavailable || errorCode != "status_unavailable" {
		t.Fatalf("unreadable push: %d %q", code, errorCode)
	}
}

// traySwitchOn reports whether the Settings page shows the icon switch on.
func traySwitchOn(t *testing.T, body string) bool {
	t.Helper()
	at := strings.Index(body, `id="tray-icon"`)
	if at < 0 {
		t.Fatal("settings has no icon switch")
	}
	start := strings.LastIndex(body[:at], "<")
	tag := body[start : start+strings.Index(body[start:], ">")]
	return strings.Contains(tag, " checked")
}

// The Settings switch hides and shows the icon of the computer that runs
// OwnGit, behind the administrator password, and says whose computer that
// is; a hidden icon never stops Git.
func TestSettingsSwitchShowsTheIconAgain(t *testing.T) {
	app, store, _, server := releaseApp(t, "v1.0.3")
	desktop := true
	app.TrayAvailable, app.TrayDesktop = true, func() bool { return desktop }
	client, jar := newBrowserClient(t)
	body, _ := dashboardGET(t, client, server.URL+"/settings")
	if !traySwitchOn(t, body) || !strings.Contains(body, webui.Text(webui.LangEN, webui.MsgSettingsTrayScope)) || strings.Contains(body, webui.Text(webui.LangEN, webui.MsgSettingsTrayNoDesktop)) {
		t.Fatal("settings does not show the icon on for the computer that runs OwnGit")
	}
	token := cookieValue(t, jar, server.URL, generalCookie)
	hide := url.Values{"csrf": {token}, "action": {webui.ActionSetTrayIcon}, "tray_icon": {"off"}}
	if response := request(t, client, http.MethodPost, server.URL+"/settings", hide, server.URL); response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("hiding without the administrator password status=%d", response.StatusCode)
	}
	if hidden, err := app.trayHidden(); err != nil || hidden {
		t.Fatalf("hidden without confirmation: %v %v", hidden, err)
	}
	hide.Set("admin_password", "admin-password")
	if response := request(t, client, http.MethodPost, server.URL+"/settings", hide, server.URL); response.StatusCode != http.StatusSeeOther {
		t.Fatalf("hiding status=%d", response.StatusCode)
	}
	held, err := state.OpenStateDirectory(store.Dir())
	noErr(t, err)
	defer held.Close()
	if hidden, err := state.TrayHidden(held); err != nil || !hidden {
		t.Fatalf("the choice was not saved on this computer: %v %v", hidden, err)
	}
	if body, _ := dashboardGET(t, client, server.URL+"/settings"); traySwitchOn(t, body) {
		t.Fatal("settings still shows the icon on")
	}
	if response := request(t, client, http.MethodGet, server.URL+"/git/missing.git/info/refs?service=git-upload-pack", nil, ""); response.StatusCode != http.StatusNotFound {
		t.Fatalf("Git answered %d with the icon hidden", response.StatusCode)
	}
	bad := url.Values{"csrf": {token}, "action": {webui.ActionSetTrayIcon}, "tray_icon": {"sometimes"}, "admin_password": {"admin-password"}}
	if response := request(t, client, http.MethodPost, server.URL+"/settings", bad, server.URL); response.StatusCode != http.StatusBadRequest {
		t.Fatalf("unknown value status=%d", response.StatusCode)
	}
	show := url.Values{"csrf": {token}, "action": {webui.ActionSetTrayIcon}, "tray_icon": {"on"}, "admin_password": {"admin-password"}}
	if response := request(t, client, http.MethodPost, server.URL+"/settings", show, server.URL); response.StatusCode != http.StatusSeeOther {
		t.Fatalf("showing status=%d", response.StatusCode)
	}
	if hidden, err := state.TrayHidden(held); err != nil || hidden {
		t.Fatalf("showing was not saved: %v %v", hidden, err)
	}
	// Asked when the page is shown, not when OwnGit started.
	desktop = false
	if body, _ := dashboardGET(t, client, server.URL+"/settings"); !traySwitchOn(t, body) || !strings.Contains(body, webui.Text(webui.LangEN, webui.MsgSettingsTrayNoDesktop)) {
		t.Fatal("settings does not say that a computer without a desktop shows no icon")
	}

	// A choice that cannot be read is shown as such, not as on or off.
	noErr(t, os.Mkdir(filepath.Join(store.Dir(), state.TrayHiddenFile), 0o700))
	body, _ = dashboardGET(t, client, server.URL+"/settings")
	if traySwitchOn(t, body) || !strings.Contains(body, webui.Text(webui.LangEN, webui.MsgSettingsTrayUnreadable)) {
		t.Fatal("settings does not say that the choice could not be read")
	}
	noErr(t, os.Remove(filepath.Join(store.Dir(), state.TrayHiddenFile)))

	// An install whose service runs as its own account offers no icon: one
	// line, no switch, and a save is refused.
	app.TrayAvailable = false
	body, _ = dashboardGET(t, client, server.URL+"/settings")
	if strings.Contains(body, `id="tray-icon"`) || !strings.Contains(body, webui.Text(webui.LangEN, webui.MsgSettingsTrayUnavailable)) ||
		strings.Contains(body, webui.Text(webui.LangEN, webui.MsgSettingsTrayScope)) {
		t.Fatal("settings offers the icon on an install without one")
	}
	if response := request(t, client, http.MethodPost, server.URL+"/settings", hide, server.URL); response.StatusCode != http.StatusConflict {
		t.Fatalf("hiding on an install without an icon status=%d", response.StatusCode)
	}
	if hidden, err := state.TrayHidden(held); err != nil || hidden {
		t.Fatalf("an install without an icon saved the choice: %v %v", hidden, err)
	}
}
