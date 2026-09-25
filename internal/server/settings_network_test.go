package server

import (
	"context"
	"html"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"owngit/internal/auth"
	"owngit/internal/state"
	"owngit/internal/webui"
)

// networkSettingsClient opens Settings as an ordinary open-mode visitor and
// returns the client, the form's CSRF token and the rendered page.
func networkSettingsClient(t *testing.T, app *App) (*http.Client, string, string, string) {
	t.Helper()
	server := serve(t, app.Handler())
	client, jar := newBrowserClient(t)
	body, status := dashboardGET(t, client, server.URL+"/settings")
	if status != http.StatusOK {
		t.Fatalf("settings status=%d", status)
	}
	return client, server.URL, cookieValue(t, jar, server.URL, generalCookie), body
}

func saveNetworkForm(csrf, revision, adminPassword string, fields map[string]string) url.Values {
	values := url.Values{
		"csrf": {csrf}, "action": {webui.ActionSaveNetwork}, "network_revision": {revision}, "admin_password": {adminPassword},
	}
	for name, value := range fields {
		values.Set(name, value)
	}
	return values
}

func savedNetwork(t *testing.T, store *state.Store) (state.NetworkSettings, []string, []string) {
	t.Helper()
	ctx := context.Background()
	settings, err := store.NetworkSettings(ctx)
	noErr(t, err)
	hosts, err := store.TrustedHosts(ctx)
	noErr(t, err)
	proxies, err := store.TrustedProxies(ctx)
	noErr(t, err)
	return settings, hosts, proxies
}

// runningRecord publishes a record as serve would for the saved values.
func runningRecord(t *testing.T, app *App, running state.RunningNetwork) {
	t.Helper()
	if running.AcceptedHosts == nil {
		running.AcceptedHosts = []string{"127.0.0.1", "::1", "localhost"}
	}
	noErr(t, app.Store.PublishRunningNetwork(context.Background(), running))
}

func TestEveryVisitorSeesTheNetworkSettings(t *testing.T) {
	app := newConfiguredApp(t)
	ctx := context.Background()
	noErr(t, app.Store.UpdateNetwork(ctx, state.NetworkUpdate{
		Settings: state.NetworkSettings{Listen: "0.0.0.0:7654", BaseURL: "http://gitbox.test:7654"},
		AddHosts: []string{"lan.test"}, AddProxies: []string{"10.0.0.0/8"},
	}))
	_, _, _, body := networkSettingsClient(t, app)
	for _, want := range []string{
		`id="network"`, "0.0.0.0:7654", "http://gitbox.test:7654", "lan.test", "10.0.0.0/8",
		`name="action" value="save_network"`, `name="network_revision"`, "owngit network reset",
		// The listen address reaches other devices over plain HTTP.
		enText(webui.MsgNetPlainHTTP),
	} {
		if !strings.Contains(body, want) {
			t.Errorf("Settings for an ordinary visitor lacks %q", want)
		}
	}
	// This app does not hold the running-record lock, so nothing is claimed
	// about the running server, and no process ID is shown.
	if !strings.Contains(body, enText(webui.MsgNetUnconfirmed)) || !strings.Contains(body, enText(webui.MsgNetNotKnown)) {
		t.Error("an app without the running-record lock did not say that the running values are not confirmed")
	}
	// The storage location stays administrator-only.
	settings, err := app.Store.Settings(ctx)
	noErr(t, err)
	if strings.Contains(body, settings.RepositoryRoot) {
		t.Error("the storage location reached an ordinary visitor")
	}
}

// enText is the English catalog text as the page escapes it.
func enText(code webui.MessageCode) string {
	return html.EscapeString(webui.Text(webui.LangEN, code))
}

// The block repeats the note "owngit network set" prints for an https base
// URL without a trusted proxy.
func TestNetworkSettingsNoteAnHTTPSAddressWithoutATrustedProxy(t *testing.T) {
	app := newConfiguredApp(t)
	ctx := context.Background()
	noErr(t, app.Store.UpdateNetwork(ctx, state.NetworkUpdate{Settings: state.NetworkSettings{BaseURL: "https://git.example.internal"}}))
	_, _, _, body := networkSettingsClient(t, app)
	if !strings.Contains(body, enText(webui.MsgNetHTTPSProxy)) {
		t.Fatal("an https base URL without a trusted proxy was not noted")
	}
	noErr(t, app.Store.UpdateNetwork(ctx, state.NetworkUpdate{Settings: state.NetworkSettings{BaseURL: "https://git.example.internal"}, AddProxies: []string{"127.0.0.1"}}))
	_, _, _, body = networkSettingsClient(t, app)
	if strings.Contains(body, enText(webui.MsgNetHTTPSProxy)) {
		t.Fatal("the https note stayed after a proxy was trusted")
	}
}

func TestNetworkSettingsTrustTheRunningRecordOnlyWhileTheLockIsHeld(t *testing.T) {
	app := newConfiguredApp(t)
	runningRecord(t, app, state.RunningNetwork{
		PID: 4242, Listen: "192.0.2.1:9000", Address: "192.0.2.1:9000", ListenSource: "saved", BaseURLSource: "default",
		TrustedProxies: []string{}, TrustedProxiesSource: "default",
	})
	// A record left by a server that ended without cleanup is never shown as
	// running.
	_, _, _, body := networkSettingsClient(t, app)
	if strings.Contains(body, "192.0.2.1:9000") || !strings.Contains(body, enText(webui.MsgNetUnconfirmed)) {
		t.Fatal("a record the app does not vouch for was shown as the running server")
	}
	app.RunningRecordLive = true
	_, _, _, body = networkSettingsClient(t, app)
	if !strings.Contains(body, "192.0.2.1:9000") || strings.Contains(body, "4242") {
		t.Fatal("the live record was not shown, or its process ID was")
	}
}

func TestNetworkSettingsShowWhatWaitsForARestart(t *testing.T) {
	app := newConfiguredApp(t)
	app.RunningRecordLive = true
	ctx := context.Background()
	noErr(t, app.Store.UpdateNetwork(ctx, state.NetworkUpdate{Settings: state.NetworkSettings{Listen: "127.0.0.1:7791"}}))
	runningRecord(t, app, state.RunningNetwork{
		Listen: "127.0.0.1:7791", Address: "127.0.0.1:7791", ListenSource: "saved", BaseURLSource: "default",
		Origin: "http://127.0.0.1:7791", TrustedProxies: []string{}, TrustedProxiesSource: "default",
	})
	_, _, _, body := networkSettingsClient(t, app)
	if !strings.Contains(body, enText(webui.MsgNetCurrent)) || strings.Contains(body, enText(webui.MsgNetPending)) {
		t.Fatal("the running server uses the saved settings, but the page did not say so")
	}

	noErr(t, app.Store.UpdateNetwork(ctx, state.NetworkUpdate{
		Settings: state.NetworkSettings{Listen: "127.0.0.1:7792"}, AddHosts: []string{"lan.test"},
	}))
	_, _, _, body = networkSettingsClient(t, app)
	if !strings.Contains(body, enText(webui.MsgNetRestart)) {
		t.Fatal("a saved change that waits for a restart was not reported")
	}
	// Two rows changed, each marked in words next to its values.
	if got := strings.Count(body, `class="netrow__pending"`); got != 2 {
		t.Fatalf("pending rows=%d, want 2 (listen address and allowed names)", got)
	}

	// A value from a start option wins for this run, so it waits for
	// nothing, and the page names the option.
	runningRecord(t, app, state.RunningNetwork{
		Listen: "127.0.0.1:7793", Address: "127.0.0.1:7793", ListenSource: "flag", BaseURLSource: "default",
		AcceptedHosts: []string{"127.0.0.1", "::1", "lan.test", "localhost"}, SavedHosts: []string{"lan.test"},
		TrustedProxies: []string{}, TrustedProxiesSource: "default",
	})
	_, _, _, body = networkSettingsClient(t, app)
	if !strings.Contains(body, enText(webui.MsgNetCurrentOpt)) || !strings.Contains(body, enText(webui.MsgNetOptionNote)) ||
		!strings.Contains(body, enText(webui.MsgNetFromOption)) || strings.Contains(body, `class="netrow__pending"`) {
		t.Fatal("a value from a start option was not shown as such")
	}
}

func TestSavingNetworkSettingsNeedsTheAdministratorPassword(t *testing.T) {
	app := newConfiguredApp(t)
	client, base, csrf, body := networkSettingsClient(t, app)
	result := browserForm(t, client, base+"/settings", saveNetworkForm(csrf, formValue(t, body, "network_revision"), "wrong-password", map[string]string{
		"listen": "0.0.0.0:7777", "base_url": "http://typed.test:7777", "allowed_hosts": "typed-name.test",
	}), base)
	if result.status != http.StatusUnauthorized {
		t.Fatalf("save with a wrong administrator password status=%d", result.status)
	}
	if !strings.Contains(result.body, enText(webui.MsgAdminFailed)) || !strings.Contains(result.body, `aria-invalid="true" aria-describedby="save_network-admin_password-note"`) {
		t.Fatal("the refused save did not mark the administrator password")
	}
	// Like the other Settings forms, a failed confirmation shows the saved
	// values again and repeats nothing that was submitted.
	for _, typed := range []string{"0.0.0.0:7777", "typed.test", "typed-name.test", "wrong-password"} {
		if strings.Contains(result.body, typed) {
			t.Errorf("the refused save echoed %q", typed)
		}
	}
	settings, hosts, _ := savedNetwork(t, app.Store)
	if settings != (state.NetworkSettings{}) || len(hosts) != 0 {
		t.Fatalf("a save with a wrong administrator password changed %+v %v", settings, hosts)
	}
}

func TestSavingNetworkSettingsRefusesInvalidValues(t *testing.T) {
	app := newConfiguredApp(t)
	client, base, csrf, body := networkSettingsClient(t, app)
	revision := formValue(t, body, "network_revision")
	for _, test := range []struct {
		field, value, detail string
		code                 webui.MessageCode
	}{
		{"listen", "gitbox.test", "", webui.MsgNetBadListen},
		{"listen", "127.0.0.1:99999", "", webui.MsgNetBadListen},
		{"base_url", "http://gitbox.test:7654/owngit", "", webui.MsgNetBadBaseURL},
		{"base_url", "gitbox.test", "", webui.MsgNetBadBaseURL},
		{"allowed_hosts", "good.test\nbad_host!", "bad_host!", webui.MsgNetBadHost},
		{"trusted_proxies", "10.0.0.0/8, 0.0.0.0/0", "0.0.0.0/0", webui.MsgNetBadProxy},
		{"trusted_proxies", "proxy.test", "proxy.test", webui.MsgNetBadProxy},
	} {
		result := browserForm(t, client, base+"/settings", saveNetworkForm(csrf, revision, "admin-password", map[string]string{test.field: test.value}), base)
		if result.status != http.StatusUnprocessableEntity {
			t.Fatalf("%s=%q: status=%d", test.field, test.value, result.status)
		}
		note := `aria-invalid="true" aria-describedby="save_network-` + test.field + `-note`
		if !strings.Contains(result.body, note) || !strings.Contains(result.body, `id="save_network-`+test.field+`-note"`) ||
			!strings.Contains(result.body, enText(test.code)) {
			t.Errorf("%s=%q: the field error is not attached to the field", test.field, test.value)
		}
		if !strings.Contains(result.body, ` autofocus>`) || strings.Count(result.body, "autofocus") != 1 {
			t.Errorf("%s=%q: the refused form does not place the reader on one field", test.field, test.value)
		}
		// The submitted text comes back so it can be corrected.
		if !strings.Contains(result.body, strings.Split(test.value, "\n")[0]) || test.detail != "" && !strings.Contains(result.body, `<span class="mono">`+test.detail+`</span>`) {
			t.Errorf("%s=%q: the submitted value or the refused entry is not shown", test.field, test.value)
		}
		settings, hosts, proxies := savedNetwork(t, app.Store)
		if settings != (state.NetworkSettings{}) || len(hosts) != 0 || len(proxies) != 0 {
			t.Fatalf("%s=%q: a refused save changed the settings", test.field, test.value)
		}
	}
}

func TestSavedNetworkSettingsApplyAtTheNextStart(t *testing.T) {
	app := newConfiguredApp(t)
	app.RunningRecordLive = true
	ctx := context.Background()
	// An allowed name stored by an older approve-host in another spelling.
	noErr(t, app.Store.AddTrustedHost(ctx, "OLD.test."))
	noErr(t, app.Store.AddTrustedHost(ctx, "Gone.test"))
	noErr(t, app.Store.UpdateNetwork(ctx, state.NetworkUpdate{AddProxies: []string{"192.0.2.9"}}))
	runningRecord(t, app, state.RunningNetwork{
		Listen: DefaultListenAddress, Address: DefaultListenAddress, ListenSource: "default", BaseURLSource: "default",
		SavedHosts: []string{"gone.test", "old.test"}, AcceptedHosts: []string{"127.0.0.1", "::1", "gone.test", "localhost", "old.test"},
		TrustedProxies: []string{"192.0.2.9"}, TrustedProxiesSource: "saved",
	})
	client, base, csrf, body := networkSettingsClient(t, app)
	if !strings.Contains(body, enText(webui.MsgNetCurrent)) {
		t.Fatal("before the save the running server should use the saved settings")
	}
	result := browserForm(t, client, base+"/settings", saveNetworkForm(csrf, formValue(t, body, "network_revision"), "admin-password", map[string]string{
		"listen": " 127.0.0.1:7795 ", "base_url": "HTTP://Gitbox.test:7795", "allowed_hosts": "old.test\r\nLAN.test, lan.test",
		"trusted_proxies": "10.1.0.0/16\n::ffff:127.0.0.1",
	}), base)
	if result.status != http.StatusSeeOther || result.header.Get("Location") != "/settings?notice=network_saved" {
		t.Fatalf("save status=%d location=%q body=%s", result.status, result.header.Get("Location"), result.body)
	}
	settings, hosts, proxies := savedNetwork(t, app.Store)
	if settings.Listen != "127.0.0.1:7795" || settings.BaseURL != "http://Gitbox.test:7795" {
		t.Fatalf("saved listen=%q base URL=%q", settings.Listen, settings.BaseURL)
	}
	if strings.Join(NormalizedHosts(hosts), ",") != "lan.test,old.test" || strings.Join(proxies, ",") != "10.1.0.0/16,127.0.0.1" {
		t.Fatalf("saved hosts=%v proxies=%v", hosts, proxies)
	}
	// Saving does not change the running server.
	if app.Hosts.Allows("lan.test") || app.Hosts.Allows("gitbox.test:7795") || app.BaseURL != "" {
		t.Fatal("saving network settings changed the running server")
	}
	body, _ = dashboardGET(t, client, base+"/settings?notice=network_saved")
	if !strings.Contains(body, enText(webui.MsgNetSaved)) || !strings.Contains(body, enText(webui.MsgNetRestart)) {
		t.Fatal("after the save the page did not say that the settings apply at the next start")
	}
}

func TestSavingNetworkSettingsFromAnOutdatedPageIsRefused(t *testing.T) {
	app := newConfiguredApp(t)
	client, base, csrf, body := networkSettingsClient(t, app)
	revision := formValue(t, body, "network_revision")
	// Another browser or "owngit network set" allows a name meanwhile.
	noErr(t, app.Store.AddTrustedHost(context.Background(), "approved.test"))
	result := browserForm(t, client, base+"/settings", saveNetworkForm(csrf, revision, "admin-password", map[string]string{"listen": "127.0.0.1:7796"}), base)
	if result.status != http.StatusConflict || !strings.Contains(result.body, enText(webui.MsgNetStale)) || !strings.Contains(result.body, "approved.test") {
		t.Fatalf("outdated save status=%d", result.status)
	}
	settings, hosts, _ := savedNetwork(t, app.Store)
	if settings.Listen != "" || len(hosts) != 1 {
		t.Fatalf("an outdated save changed the settings: %+v %v", settings, hosts)
	}
}

func TestListeningBeyondThisComputerNeedsThePlainHTTPAcknowledgement(t *testing.T) {
	app, store, repositoryRoot := newTestApp(t)
	noErr(t, os.MkdirAll(repositoryRoot, 0o700))
	canonical, _ := filepath.EvalSymlinks(repositoryRoot)
	adminHash, _ := auth.HashPassword("admin-password")
	noErr(t, store.CompleteSetup(context.Background(), canonical, "open", "", adminHash, false))
	app.Repositories.SetRoot(canonical)
	client, base, csrf, body := networkSettingsClient(t, app)
	revision := formValue(t, body, "network_revision")
	if !strings.Contains(body, enText(webui.MsgNetAckHelp)) {
		t.Fatal("the Network form does not offer the plain HTTP acknowledgement")
	}
	// Only this computer: no acknowledgement needed.
	result := browserForm(t, client, base+"/settings", saveNetworkForm(csrf, revision, "admin-password", map[string]string{"listen": "127.0.0.1:7797"}), base)
	if result.status != http.StatusSeeOther {
		t.Fatalf("loopback save status=%d", result.status)
	}
	body, _ = dashboardGET(t, client, base+"/settings")
	revision = formValue(t, body, "network_revision")
	result = browserForm(t, client, base+"/settings", saveNetworkForm(csrf, revision, "admin-password", map[string]string{"listen": "0.0.0.0:7797"}), base)
	if result.status != http.StatusUnprocessableEntity || !strings.Contains(result.body, `aria-describedby="save_network-insecure_ack-note net-ack-help"`) {
		t.Fatalf("every-network save without the acknowledgement status=%d", result.status)
	}
	if settings, _, _ := savedNetwork(t, store); settings.Listen != "127.0.0.1:7797" {
		t.Fatalf("a refused save changed the listen address to %q", settings.Listen)
	}
	values := saveNetworkForm(csrf, revision, "admin-password", map[string]string{"listen": "0.0.0.0:7797", "insecure_ack": "1"})
	result = browserForm(t, client, base+"/settings", values, base)
	if result.status != http.StatusSeeOther {
		t.Fatalf("acknowledged save status=%d", result.status)
	}
	current, err := store.Settings(context.Background())
	noErr(t, err)
	if settings, _, _ := savedNetwork(t, store); settings.Listen != "0.0.0.0:7797" || !current.InsecureHTTPAccepted {
		t.Fatalf("acknowledged save: listen=%q acknowledged=%v", settings.Listen, current.InsecureHTTPAccepted)
	}
}
