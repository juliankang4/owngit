package server

import (
	"context"
	"errors"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"owngit/internal/state"
	"owngit/internal/tailscale"
	"owngit/internal/tailscale/tailscaletest"
	"owngit/internal/webui"
)

func tailscaleForm(csrf, action, adminPassword string, homeNetwork bool) url.Values {
	values := url.Values{"csrf": {csrf}, "action": {action}, "admin_password": {adminPassword}}
	if homeNetwork {
		values.Set("home_network", "1")
	}
	return values
}

// tailscaleOffers reads what the Tailscale group of a Settings page offers:
// turning sharing on, turning it off, and turning it on again, which saving
// the group does while sharing is on but unfinished.
func tailscaleOffers(page string) (on, off, again bool) {
	at := strings.Index(page, `id="ts-switch"`)
	group := strings.Index(page, `id="grp-tailscale"`)
	if at < 0 || group < 0 {
		return false, false, false
	}
	start := strings.LastIndex(page[:at], "<")
	tag := page[start : start+strings.Index(page[start:], ">")]
	section := page[group : group+strings.Index(page[group:], ">")]
	enabled, checked := !strings.Contains(tag, " disabled"), strings.Contains(tag, " checked")
	return enabled && !checked, enabled && checked, strings.Contains(section, "data-group-open")
}

// Every Settings viewer sees whether sharing is on, what turning it on
// records in public and the home network choice, which starts unticked for
// an owner who listens on this computer only.
func TestEveryViewerSeesTheTailscaleBlock(t *testing.T) {
	app, fake := tailscaleApp(t, tailscaletest.State{Status: tailscaletest.Running()})
	_, _, _, body := networkSettingsClient(t, app)
	if on, _, _ := tailscaleOffers(body); !on {
		t.Error("the Tailscale group does not offer turning sharing on")
	}
	for _, want := range []string{
		`id="grp-tailscale"`, enText(webui.MsgTSOff), enText(webui.MsgTSHome),
		tailscaletest.Name, "0.0.0.0:7654", "127.0.0.1:7654",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the Tailscale block lacks %q", want)
		}
	}
	if strings.Contains(body, `name="home_network" value="1" checked`) {
		t.Error("the home network is ticked by default")
	}
	if len(fake.Writes()) != 0 {
		t.Fatalf("showing the block changed Tailscale: %q", fake.Writes())
	}
}

// Turning sharing on and off in Settings asks for the administrator
// password and changes the running server at once.
func TestTurningTailscaleSharingOnAndOffInSettings(t *testing.T) {
	app, fake := tailscaleApp(t, tailscaletest.State{Status: tailscaletest.Running()})
	client, base, csrf, _ := networkSettingsClient(t, app)

	result := browserForm(t, client, base+"/settings", tailscaleForm(csrf, webui.ActionTailscaleOn, "wrong-password", false), base)
	if result.status != http.StatusUnauthorized || !strings.Contains(result.body, `aria-describedby="tailscale-admin_password-note"`) {
		t.Fatalf("wrong administrator password: status=%d", result.status)
	}
	if len(fake.Writes()) != 0 {
		t.Fatalf("a wrong administrator password changed Tailscale: %q", fake.Writes())
	}
	if _, on, _ := app.Store.TailscaleServe(t.Context()); on {
		t.Fatal("a wrong administrator password turned sharing on")
	}

	result = browserForm(t, client, base+"/settings", tailscaleForm(csrf, webui.ActionTailscaleOn, "admin-password", false), base)
	if result.status != http.StatusSeeOther || result.header.Get("Location") != "/settings/network?notice=tailscale_on#grp-tailscale" {
		t.Fatalf("turn on: status=%d location=%q body=%s", result.status, result.header.Get("Location"), result.body)
	}
	body, _ := dashboardGET(t, client, base+result.header.Get("Location"))
	for _, want := range []string{enText(webui.MsgTSTurnedOn), enText(webui.MsgTSFirstVisit), enText(webui.MsgTSReady), `value="https://` + tailscaletest.Name + `/"`} {
		if !strings.Contains(body, want) {
			t.Errorf("after turning on, Settings lacks %q", want)
		}
	}
	if _, off, _ := tailscaleOffers(body); !off {
		t.Error("after turning on, Settings does not offer turning off")
	}
	// Turning on again keeps the address, which has its certificate, so
	// the first-visit note is left out.
	result = browserForm(t, client, base+"/settings", tailscaleForm(csrf, webui.ActionTailscaleOn, "admin-password", false), base)
	if result.status != http.StatusSeeOther || result.header.Get("Location") != "/settings/network?notice=tailscale_on_kept#grp-tailscale" {
		t.Fatalf("turn on again: status=%d location=%q", result.status, result.header.Get("Location"))
	}
	body, _ = dashboardGET(t, client, base+result.header.Get("Location"))
	if !strings.Contains(body, enText(webui.MsgTSTurnedOn)) || strings.Contains(body, enText(webui.MsgTSFirstVisit)) {
		t.Error("turning on again: the notice lacks \"on\" or has the first-visit note")
	}
	// A page reached through the Tailscale endpoint says that Tailscale on
	// this computer encrypted it.
	if response := throughServe(app, "/settings/network"); response.Code != http.StatusOK || !strings.Contains(response.Body.String(), enText(webui.MsgConnTailscaleOn)) ||
		strings.Contains(response.Body.String(), enText(webui.MsgConnEncrypted)) {
		t.Fatalf("through Serve: status=%d, indicator not naming Tailscale", response.Code)
	}

	result = browserForm(t, client, base+"/settings", tailscaleForm(csrf, webui.ActionTailscaleOff, "admin-password", false), base)
	if result.status != http.StatusSeeOther || result.header.Get("Location") != "/settings/network?notice=tailscale_off#grp-tailscale" {
		t.Fatalf("turn off: status=%d body=%s", result.status, result.body)
	}
	if len(fake.Writes()) != 2 {
		t.Fatalf("writes=%q", fake.Writes())
	}
	if response := throughServe(app, "/settings/network"); response.Code != http.StatusMisdirectedRequest {
		t.Fatalf("after turning off, the Tailscale name got %d", response.Code)
	}
}

// A refusal is explained on the block, with what is on the port, even
// though the block then no longer offers the form.
func TestTailscaleRefusalIsExplainedOnTheBlock(t *testing.T) {
	app, fake := tailscaleApp(t, tailscaletest.State{Status: tailscaletest.Running()})
	client, base, csrf, _ := networkSettingsClient(t, app)
	// Every port OwnGit would choose is taken, which the page did not show
	// when it was opened.
	fake.Update(func(fakeState *tailscaletest.State) {
		fakeState.Serve = otherService(tailscale.ServeConfig{}, tailscaletest.Name, 443, 8443, 10000)
	})
	result := browserForm(t, client, base+"/settings", tailscaleForm(csrf, webui.ActionTailscaleOn, "admin-password", true), base)
	if result.status != http.StatusConflict {
		t.Fatalf("status=%d", result.status)
	}
	for _, want := range []string{
		`id="tailscale-tailscale-note"`, `role="alert"`, "autofocus",
		enText(webui.TailscaleRefusalCode(TailscaleProblemTaken, true)),
		// The list below the alert, in both languages.
		"https://" + tailscaletest.Name + ":443/ to http://127.0.0.1:3000",
		"https://" + tailscaletest.Name + ":8443/ to http://127.0.0.1:3000",
		"https://" + tailscaletest.Name + ":10000/에서 http://127.0.0.1:3000(으)로 전달",
		enText(webui.MsgTSTakenSteps),
	} {
		if !strings.Contains(result.body, want) {
			t.Errorf("the refusal lacks %q", want)
		}
	}
	// Listed once, in the block below the alert.
	list := regexp.MustCompile(`(?s)<ul class="tsfound">.*?</ul>`)
	if lists := list.FindAllString(result.body, -1); len(lists) != 1 || strings.Contains(list.ReplaceAllString(result.body, ""), "127.0.0.1:3000") {
		t.Errorf("what is on the port is not listed exactly once: %d lists", len(lists))
	}
	if len(fake.Writes()) != 0 {
		t.Fatalf("writes=%q", fake.Writes())
	}
	if settings, _, _, record := savedSharing(t, app.Store); settings.Listen != "" || record != nil {
		t.Fatalf("a refusal saved %+v %+v", settings, record)
	}
}

// The indicator names Tailscale only for HTTPS that the trusted loopback
// proxy forwarded for the shared name.
func TestConnectionNamesTailscaleOnlyForItsEndpoint(t *testing.T) {
	app, _ := tailscaleApp(t, tailscaletest.State{Status: tailscaletest.Running()})
	_, err := app.Tailscale.On(t.Context(), nil, 0)
	noErr(t, err)
	cases := []struct {
		name, host, proto string
		want              bool
	}{
		{"through Serve", tailscaletest.Name, "https", true},
		{"the name without forwarded HTTPS", tailscaletest.Name, "", false},
		{"forwarded HTTPS for another name", "localhost:7654", "https", false},
	}
	for _, test := range cases {
		request := httptest.NewRequest(http.MethodGet, "/", nil)
		request.RemoteAddr, request.Host = "127.0.0.1:50123", test.host
		if test.proto != "" {
			request.Header.Set("X-Forwarded-Proto", test.proto)
		}
		var got bool
		app.Network.Resolver().Middleware(http.HandlerFunc(func(_ http.ResponseWriter, inner *http.Request) {
			got = app.throughTailscale(inner)
		})).ServeHTTP(httptest.NewRecorder(), request)
		if got != test.want {
			t.Errorf("%s: through Tailscale=%v, want %v", test.name, got, test.want)
		}
	}
}

// An unfinished turning on asks to turn sharing on again and offers the
// form to do so, without asking for a restart that would not help.
func TestUnfinishedSharingOffersTurningOnAgain(t *testing.T) {
	app, fake := tailscaleApp(t, tailscaletest.State{Status: tailscaletest.Running()})
	ctx := context.Background()
	target := tailscale.Target(7654)
	noErr(t, app.Store.SaveTailscaleServe(ctx, state.TailscaleServe{Name: tailscaletest.Name, HTTPSPort: 443, Target: target, Created: true}))
	fake.Update(func(s *tailscaletest.State) {
		s.Serve = tailscale.ServeConfig{
			TCP: map[string]tailscale.TCPHandler{"443": {HTTPS: true}},
			Web: map[string]tailscale.WebServer{tailscaletest.Name + ":443": {Handlers: map[string]tailscale.Handler{"/": {Proxy: target}}}},
		}
	})
	report, err := app.Tailscale.Report(ctx)
	noErr(t, err)
	if !reflect.DeepEqual(report.Waiting, []string{TailscaleWaitUnfinished}) {
		t.Fatalf("waiting=%q", report.Waiting)
	}
	client, base, csrf, _ := networkSettingsClient(t, app)
	body, _ := dashboardGET(t, client, base+"/settings/network")
	if !strings.Contains(body, enText(webui.TailscaleWaitCode(TailscaleWaitUnfinished))) || !strings.Contains(body, enText(webui.MsgSettingsTSAgain)) {
		t.Error("the unfinished state does not say that saving turns sharing on again")
	}
	if _, off, again := tailscaleOffers(body); !off || !again {
		t.Errorf("the unfinished state offers turning off %v and on again %v", off, again)
	}
	if strings.Contains(body, enText(webui.TailscaleWaitCode(TailscaleWaitRestart))) {
		t.Error("the unfinished state asks for a restart")
	}
	result := browserForm(t, client, base+"/settings", tailscaleForm(csrf, webui.ActionTailscaleOn, "admin-password", false), base)
	if result.status != http.StatusSeeOther {
		t.Fatalf("turning on again: status=%d", result.status)
	}
	if report, err := app.Tailscale.Report(ctx); err != nil || !report.Ready {
		t.Fatalf("after turning on again: %+v %v", report, err)
	}
}

// saveTailscaleGroup posts the Tailscale group as its form sends it: the
// switch and, while sharing is off, the home network choice.
func saveTailscaleGroup(t *testing.T, client *http.Client, base, csrf, switchValue string, homeNetwork bool) browserHTTPResult {
	t.Helper()
	values := tailscaleForm(csrf, webui.ActionSaveTailscale, "admin-password", homeNetwork)
	if switchValue != "" {
		values.Set("tailscale", switchValue)
	}
	return browserForm(t, client, base+"/settings/network", values, base)
}

// Saving the Tailscale group with the switch as it is changes nothing and
// says so. The form has no home network choice while sharing is on, so the
// save must not read one from it: the listen address that the owner opened
// to the home network stays.
func TestSavingTheUnchangedTailscaleGroupChangesNothing(t *testing.T) {
	app, fake := tailscaleApp(t, tailscaletest.State{Status: tailscaletest.Running()})
	client, base, csrf, _ := networkSettingsClient(t, app)
	nothing := enText(webui.MsgSettingsNothing)

	result := saveTailscaleGroup(t, client, base, csrf, "on", true)
	if result.status != http.StatusSeeOther {
		t.Fatalf("turning on with the home network: status=%d", result.status)
	}
	before, _, _ := savedNetwork(t, app.Store)
	if host, _, _ := strings.Cut(before.Listen, ":"); host != "0.0.0.0" {
		t.Fatalf("turning on with the home network saved listen %q", before.Listen)
	}
	writes := len(fake.Writes())
	result = saveTailscaleGroup(t, client, base, csrf, "on", false)
	after, _, _ := savedNetwork(t, app.Store)
	if result.status != http.StatusOK || !strings.Contains(settingsGroup(t, result.body, "tailscale"), nothing) {
		t.Errorf("unchanged save while on: status=%d location=%q", result.status, result.header.Get("Location"))
	}
	if after.Listen != before.Listen || len(fake.Writes()) != writes {
		t.Errorf("unchanged save while on changed listen %q to %q, or wrote %q", before.Listen, after.Listen, fake.Writes()[writes:])
	}

	if result = saveTailscaleGroup(t, client, base, csrf, "", false); result.status != http.StatusSeeOther {
		t.Fatalf("turning off: status=%d", result.status)
	}
	// Turning off keeps the listen address, so the form shows the home
	// network choice ticked, and sends it so.
	writes = len(fake.Writes())
	result = saveTailscaleGroup(t, client, base, csrf, "", true)
	if result.status != http.StatusOK || !strings.Contains(settingsGroup(t, result.body, "tailscale"), nothing) || len(fake.Writes()) != writes {
		t.Errorf("unchanged save while off: status=%d writes=%q", result.status, fake.Writes()[writes:])
	}
}

// homeNetworkTicked reports whether the home network choice of a Settings
// page is ticked.
func homeNetworkTicked(t *testing.T, page string) bool {
	t.Helper()
	at := strings.Index(page, `name="home_network"`)
	if at < 0 {
		t.Fatal("the page has no home network choice")
	}
	start := strings.LastIndex(page[:at], "<")
	return strings.Contains(page[start:start+strings.Index(page[start:], ">")], " checked")
}

// While sharing is off, the home network choice is used only when sharing
// is turned on. Changing it alone saves nothing, and the answer says so and
// shows the choice as saved, not as sent, so the page never claims a
// listen address that is not the one saved.
func TestAHomeNetworkChoiceWithoutSharingIsNotShownAsSaved(t *testing.T) {
	app, fake := tailscaleApp(t, tailscaletest.State{Status: tailscaletest.Running()})
	noErr(t, app.Store.UpdateNetwork(context.Background(), state.NetworkUpdate{Settings: state.NetworkSettings{Listen: "0.0.0.0:7654"}}))
	client, base, csrf, body := networkSettingsClient(t, app)
	if !homeNetworkTicked(t, body) {
		t.Fatal("a saved listen address on every network does not tick the home network choice")
	}
	for _, lang := range []webui.Lang{webui.LangEN, webui.LangKO} {
		dashboardGET(t, client, base+"/settings/network?lang="+string(lang))
		text := func(code webui.MessageCode) string { return html.EscapeString(webui.Text(lang, code)) }
		// Unticked, with the switch still off.
		result := saveTailscaleGroup(t, client, base, csrf, "", false)
		group := settingsGroup(t, result.body, "tailscale")
		if result.status != http.StatusOK || !strings.Contains(group, text(webui.MsgSettingsTSHomeOff)) || strings.Contains(group, text(webui.MsgSettingsNothing)) {
			t.Errorf("%s: unticking the home network while sharing is off: status=%d, the answer does not say why nothing was saved", lang, result.status)
		}
		if !homeNetworkTicked(t, result.body) {
			t.Errorf("%s: the answer shows the home network choice that was not saved", lang)
		}
		// Ticked, as saved: nothing to say but that nothing changed.
		result = saveTailscaleGroup(t, client, base, csrf, "", true)
		group = settingsGroup(t, result.body, "tailscale")
		if result.status != http.StatusOK || !strings.Contains(group, text(webui.MsgSettingsNothing)) || !homeNetworkTicked(t, result.body) {
			t.Errorf("%s: the unchanged choice: status=%d", lang, result.status)
		}
	}
	if settings, _, _ := savedNetwork(t, app.Store); settings.Listen != "0.0.0.0:7654" || len(fake.Writes()) != 0 {
		t.Fatalf("the saves changed listen to %q or wrote %q", settings.Listen, fake.Writes())
	}
}

// Saving the group of sharing that is on but unfinished turns it on again
// and keeps the home network choice made when it was turned on, which that
// form does not offer again.
func TestTurningOnAgainKeepsTheHomeNetwork(t *testing.T) {
	app, fake := tailscaleApp(t, tailscaletest.State{Status: tailscaletest.Running()})
	ctx := context.Background()
	target := tailscale.Target(7654)
	noErr(t, app.Store.UpdateNetwork(ctx, state.NetworkUpdate{Settings: state.NetworkSettings{Listen: "0.0.0.0:7654"}}))
	noErr(t, app.Store.SaveTailscaleServe(ctx, state.TailscaleServe{Name: tailscaletest.Name, HTTPSPort: 443, Target: target, Created: true}))
	fake.Update(func(s *tailscaletest.State) {
		s.Serve = tailscale.ServeConfig{
			TCP: map[string]tailscale.TCPHandler{"443": {HTTPS: true}},
			Web: map[string]tailscale.WebServer{tailscaletest.Name + ":443": {Handlers: map[string]tailscale.Handler{"/": {Proxy: target}}}},
		}
	})
	client, base, csrf, body := networkSettingsClient(t, app)
	if _, _, again := tailscaleOffers(body); !again {
		t.Fatal("the unfinished state does not offer turning on again")
	}
	if strings.Contains(settingsGroup(t, body, "tailscale"), `name="home_network"`) {
		t.Fatal("the form of sharing that is on offers the home network choice")
	}
	result := saveTailscaleGroup(t, client, base, csrf, "on", false)
	if result.status != http.StatusSeeOther {
		t.Fatalf("turning on again: status=%d", result.status)
	}
	if settings, _, _ := savedNetwork(t, app.Store); settings.Listen != "0.0.0.0:7654" {
		t.Errorf("turning on again changed the saved listen address to %q", settings.Listen)
	}
	if report, err := app.Tailscale.Report(ctx); err != nil || !report.Ready {
		t.Fatalf("after turning on again: %+v %v", report, err)
	}
}

// A viewer who is not the administrator sees that the port is taken, but not
// the backend addresses of other services, addresses under earlier names or
// what Tailscale printed. An administrator session sees them.
func TestOnlyTheAdministratorSeesWhatElseTailscaleServes(t *testing.T) {
	app, fake := tailscaleApp(t, tailscaletest.State{Status: tailscaletest.Running()})
	fake.Update(func(s *tailscaletest.State) {
		s.Serve = otherService(tailscale.ServeConfig{}, tailscaletest.Name, 443, 8443, 10000)
		s.Serve.Web["oldbox.tail0000.ts.net:443"] = tailscale.WebServer{Handlers: map[string]tailscale.Handler{"/": {Proxy: "http://127.0.0.1:9090"}}}
	})
	client, base, _, body := networkSettingsClient(t, app)
	if !strings.Contains(body, enText(webui.MsgTSTakenBrief)) {
		t.Error("a viewer is not told that the port is taken")
	}
	for _, secret := range []string{"127.0.0.1:3000", "127.0.0.1:9090", "oldbox", enText(webui.MsgTSStale)} {
		if strings.Contains(body, secret) {
			t.Errorf("a viewer sees %q", secret)
		}
	}
	settings, err := app.Store.Settings(context.Background())
	noErr(t, err)
	noErr(t, app.Store.CreateSession(context.Background(), "tailscale-admin-session", "admin", "tailscale-admin-csrf", settings.AdminSessionVersion, time.Now().Add(time.Hour)))
	parsed, _ := url.Parse(base)
	client.Jar.SetCookies(parsed, []*http.Cookie{{Name: adminCookie, Value: "tailscale-admin-session", Path: "/"}})
	body, _ = dashboardGET(t, client, base+"/settings/network")
	for _, want := range []string{enText(webui.MsgTSTaken), "127.0.0.1:3000", "https://oldbox.tail0000.ts.net:443/"} {
		if !strings.Contains(body, want) {
			t.Errorf("the administrator does not see %q", want)
		}
	}
}

// A viewer who is not the administrator gets a complete sentence for a
// Tailscale error instead of one that ends before the hidden detail.
func TestAViewerGetsACompleteSentenceForATailscaleError(t *testing.T) {
	app, _ := tailscaleApp(t, tailscaletest.State{Status: tailscaletest.Running(), StatusError: "unexpected answer secret-detail-7"})
	report, err := app.Tailscale.Report(context.Background())
	noErr(t, err)
	if report.Problem != string(tailscale.KindFailed) {
		t.Fatalf("problem=%q, want failed", report.Problem)
	}
	_, _, _, body := networkSettingsClient(t, app)
	if !strings.Contains(body, enText(webui.TailscaleProblemBrief(webui.MsgTSProblemFailed))) || strings.Contains(body, "secret-detail-7") ||
		strings.Contains(body, enText(webui.MsgTSProblemFailed)+"<") {
		t.Fatal("a viewer does not get the complete sentence, or sees the detail")
	}
}

// After the endpoint was changed by hand, neither turning on nor off would
// work, so neither is offered: the administrator gets the exact commands
// that make turning off work, and after following one, turning off works.
func TestAChangedEndpointOffersTheStepsThatWork(t *testing.T) {
	app, fake := tailscaleApp(t, tailscaletest.State{Status: tailscaletest.Running()})
	ctx := context.Background()
	change, err := app.Tailscale.On(ctx, nil, 0)
	noErr(t, err)
	changed := tailscale.ServeConfig{
		TCP: map[string]tailscale.TCPHandler{"443": {HTTPS: true}},
		Web: map[string]tailscale.WebServer{tailscaletest.Name + ":443": {Handlers: map[string]tailscale.Handler{"/": {Proxy: "http://127.0.0.1:7701"}}}},
	}
	fake.Update(func(s *tailscaletest.State) { s.Serve = changed })
	app.Tailscale.forget()
	client, base, _, _ := networkSettingsClient(t, app)
	settings, err := app.Store.Settings(ctx)
	noErr(t, err)
	noErr(t, app.Store.CreateSession(ctx, "changed-admin-session", "admin", "changed-admin-csrf", settings.AdminSessionVersion, time.Now().Add(time.Hour)))
	parsed, _ := url.Parse(base)
	client.Jar.SetCookies(parsed, []*http.Cookie{{Name: adminCookie, Value: "changed-admin-session", Path: "/"}})
	body, _ := dashboardGET(t, client, base+"/settings/network")
	steps := fmt.Sprintf(enText(webui.MsgTSChangedSteps), "443", change.Record.Target)
	for _, want := range []string{enText(webui.TailscaleWaitCode(TailscaleWaitChanged)), steps, "http://127.0.0.1:7701"} {
		if !strings.Contains(body, want) {
			t.Errorf("the changed state lacks %q", want)
		}
	}
	if on, off, again := tailscaleOffers(body); on || off || again {
		t.Errorf("the changed state offers turning on %v, off %v or on again %v, which would be refused", on, off, again)
	}
	// Following the first step: the entry is removed, and turning off works.
	fake.Update(func(s *tailscaletest.State) { s.Serve = tailscale.ServeConfig{} })
	off, err := app.Tailscale.Off(ctx)
	noErr(t, err)
	if off.Endpoint != "gone" {
		t.Fatalf("off after removing the entry: %+v", off)
	}
	// Following the second step instead: OwnGit's address is back, and
	// turning off removes it.
	_, err = app.Tailscale.On(ctx, nil, 0)
	noErr(t, err)
	fake.Update(func(s *tailscaletest.State) { s.Serve = changed })
	if _, err := app.Tailscale.Off(ctx); err == nil {
		t.Fatal("off accepted a changed endpoint")
	}
	_, err = app.Tailscale.On(ctx, nil, 0)
	if err == nil {
		t.Fatal("on accepted a changed endpoint")
	}
	fake.Update(func(s *tailscaletest.State) {
		s.Serve.Web[tailscaletest.Name+":443"] = tailscale.WebServer{Handlers: map[string]tailscale.Handler{"/": {Proxy: change.Record.Target}}}
	})
	if off, err := app.Tailscale.Off(ctx); err != nil || off.Endpoint != "removed" {
		t.Fatalf("off after putting OwnGit's address back: %+v %v", off, err)
	}
}

// When Tailscale is stopped it may still show its configuration and refuse
// only the change. Turning off then says that Tailscale is off and how to
// turn it on, and the administrator also sees what Tailscale printed.
func TestTurningOffWhileTailscaleIsStoppedSaysSo(t *testing.T) {
	app, fake := tailscaleApp(t, tailscaletest.State{Status: tailscaletest.Running()})
	_, err := app.Tailscale.On(t.Context(), nil, 0)
	noErr(t, err)
	fake.Update(func(s *tailscaletest.State) {
		s.Status.BackendState = "Stopped"
		s.WriteError = "Tailscale is stopped."
	})
	app.Tailscale.forget()
	stopped := enText(webui.TailscaleProblemCode(string(tailscale.KindStopped)))
	// The page says once that Tailscale is stopped and does not offer
	// turning off, which would be refused.
	client, base, csrf, page := networkSettingsClient(t, app)
	shown := func(body string) int { return strings.Count(body, `data-en="`+stopped+`"`) }
	if _, off, _ := tailscaleOffers(page); shown(page) != 1 || off {
		t.Fatalf("while stopped the page shows the problem %d times, or offers turning off", shown(page))
	}
	// A page opened before Tailscale stopped still sends the form.
	result := browserForm(t, client, base+"/settings", tailscaleForm(csrf, webui.ActionTailscaleOff, "admin-password", false), base)
	if result.status != http.StatusConflict {
		t.Fatalf("turn off: status=%d", result.status)
	}
	for _, want := range []string{stopped, "Tailscale is stopped."} {
		if !strings.Contains(result.body, want) {
			t.Errorf("the refusal lacks %q", want)
		}
	}
	if count := shown(result.body); count != 1 {
		t.Errorf("the refusal shows the problem %d times", count)
	}
	if strings.Contains(result.body, enText(webui.MsgTSProblemFailed)) {
		t.Error("the refusal shows Tailscale's error as an unexplained failure")
	}
	if _, on, _ := app.Store.TailscaleServe(t.Context()); !on {
		t.Fatal("a refused turning off took back the settings")
	}
}

// Turning off is not offered only while it would have to remove OwnGit's
// address and Tailscale cannot change its configuration: when it is
// stopped, signed out, starting, or its command does not work. Without the
// address, after a rename, or with a problem that does not keep the address
// from being removed, turning off changes what it can and stays offered.
func TestTurningOffIsHiddenOnlyWhenTailscaleWouldRefuse(t *testing.T) {
	record := state.TailscaleServe{Name: tailscaletest.Name, HTTPSPort: 443, Created: true}
	here := TailscaleReport{Name: tailscaletest.Name, Endpoint: TailscaleEndpointOwnGit}
	for _, test := range []struct {
		name    string
		problem tailscale.Kind
		change  func(*TailscaleReport, *state.TailscaleServe)
		refused bool
	}{
		{"no problem", "", nil, false},
		{"stopped", tailscale.KindStopped, nil, true},
		{"signed out", tailscale.KindLoggedOut, nil, true},
		{"starting", tailscale.KindNotRunning, nil, true},
		{"not installed", tailscale.KindNotInstalled, nil, true},
		{"not answering", tailscale.KindTimeout, nil, true},
		{"stopped without a name", tailscale.KindStopped, func(report *TailscaleReport, _ *state.TailscaleServe) { report.Name = "" }, true},
		{"waiting for approval", tailscale.KindNeedsApproval, nil, false},
		{"MagicDNS off", tailscale.KindMagicDNSOff, nil, false},
		{"certificates off", tailscale.KindHTTPSOff, nil, false},
		{"stopped, address gone", tailscale.KindStopped, func(report *TailscaleReport, _ *state.TailscaleServe) { report.Endpoint = TailscaleEndpointMissing }, false},
		{"stopped, renamed", tailscale.KindStopped, func(report *TailscaleReport, _ *state.TailscaleServe) { report.Name = renamed }, false},
		{"stopped, address not made by OwnGit", tailscale.KindStopped, func(_ *TailscaleReport, record *state.TailscaleServe) { record.Created = false }, false},
	} {
		report, record := here, record
		report.Problem = string(test.problem)
		if test.change != nil {
			test.change(&report, &record)
		}
		if got := offNeedsTailscale(report, record); got != test.refused {
			t.Errorf("%s: turning off hidden=%v, want %v", test.name, got, test.refused)
		}
	}
}

// Turning off from a page opened through the tailnet address ends on a page
// that needs nothing more from that address, which then no longer reaches
// OwnGit, and that names the address that works on this computer.
func TestTurningOffThroughTheTailnetAddressEndsOnAPageThatLoads(t *testing.T) {
	app, fake := tailscaleApp(t, tailscaletest.State{Status: tailscaletest.Running()})
	change, err := app.Tailscale.On(t.Context(), nil, 0)
	noErr(t, err)
	page := throughServe(app, "/settings/network?lang=ko&appearance=dark")
	if page.Code != http.StatusOK {
		t.Fatalf("settings through Serve: %d", page.Code)
	}
	var csrf string
	for _, cookie := range page.Result().Cookies() {
		if cookie.Name == generalCookie {
			csrf = cookie.Value
		}
	}
	request := httptest.NewRequest(http.MethodPost, "/settings", strings.NewReader(tailscaleForm(csrf, webui.ActionTailscaleOff, "admin-password", false).Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("Origin", "https://"+tailscaletest.Name)
	for _, cookie := range page.Result().Cookies() {
		request.AddCookie(cookie)
	}
	response := sendThroughServe(app, request)
	body := response.Body.String()
	if response.Code != http.StatusOK || response.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("turn off through Serve: status=%d body=%s", response.Code, body)
	}
	for _, want := range []string{
		`<html lang="ko">`, `content="dark"`, webui.Text(webui.LangKO, webui.MsgTSTurnedOff),
		`href="` + change.Record.Target + `/settings"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the off page lacks %q", want)
		}
	}
	for _, needs := range []string{"<script", "<link", "<img", "/assets/", "src="} {
		if strings.Contains(body, needs) {
			t.Errorf("the off page loads more from the address it turned off: %q", needs)
		}
	}
	if len(fake.Writes()) != 2 {
		t.Fatalf("writes=%q", fake.Writes())
	}
	if _, on, _ := app.Store.TailscaleServe(t.Context()); on {
		t.Fatal("sharing is still on")
	}
	if next := throughServe(app, "/settings/network"); next.Code != http.StatusMisdirectedRequest {
		t.Fatalf("after turning off, the Tailscale name got %d", next.Code)
	}
}

// The step that clears the HTTPS port fits what is on it: "tailscale serve
// --https=443 off" removes only web handlers, so TCP forwarding, plain HTTP
// and a "tailscale serve" running in a terminal each get their own step,
// and anything else a general one. Both languages give the same command.
func TestTailscaleFixFitsWhatIsOnThePort(t *testing.T) {
	const name, target = tailscaletest.Name, "http://127.0.0.1:7654"
	// The steps name the port they concern, here one other than 443.
	https := map[string]tailscale.TCPHandler{"8443": {HTTPS: true}}
	web := func(handlers map[string]tailscale.Handler) map[string]tailscale.WebServer {
		return map[string]tailscale.WebServer{name + ":8443": {Handlers: handlers}}
	}
	other := web(map[string]tailscale.Handler{"/": {Proxy: "http://127.0.0.1:3000"}})
	for _, tc := range []struct {
		label   string
		config  tailscale.ServeConfig
		off     webui.MessageCode
		changed webui.MessageCode
		command string
	}{
		{"a web handler", tailscale.ServeConfig{TCP: https, Web: other}, webui.MsgTSRemoveSteps, webui.MsgTSChangedSteps, `"tailscale serve --https=8443 off"`},
		{"a web handler with Funnel", tailscale.ServeConfig{TCP: https, Web: other, AllowFunnel: map[string]bool{name + ":8443": true}}, webui.MsgTSRemoveSteps, webui.MsgTSChangedSteps, `"tailscale serve --https=8443 off"`},
		{"TCP forwarding", tailscale.ServeConfig{TCP: map[string]tailscale.TCPHandler{"8443": {TCPForward: "127.0.0.1:22"}}}, webui.MsgTSRemoveStepsTCP, webui.MsgTSChangedStepsOther, `"tailscale serve --tcp=8443 off"`},
		{"plain HTTP", tailscale.ServeConfig{TCP: map[string]tailscale.TCPHandler{"8443": {HTTP: true}}, Web: other}, webui.MsgTSRemoveStepsHTTP, webui.MsgTSChangedStepsOther, `"tailscale serve --http=8443 off"`},
		{"a foreground session", tailscale.ServeConfig{Foreground: map[string]tailscale.ServeConfig{"session": {TCP: https, Web: other}}}, webui.MsgTSRemoveStepsForeground, webui.MsgTSChangedStepsOther, "Ctrl+C"},
		{"an incomplete setting", tailscale.ServeConfig{TCP: https}, webui.MsgTSRemoveStepsOther, webui.MsgTSChangedStepsOther, `"tailscale serve status"`},
	} {
		found := tc.config.Endpoint(name, 8443, target).Found
		if len(found) == 0 {
			t.Fatalf("%s: nothing found", tc.label)
		}
		off, _ := TailscaleFix(found, false, "")
		changed, value := TailscaleFix(found, true, target)
		if off != tc.off || changed != tc.changed {
			t.Errorf("%s (%q): off=%s changed=%s, want %s %s", tc.label, found, off, changed, tc.off, tc.changed)
		}
		if (changed == webui.MsgTSChangedSteps) != (value == target) {
			t.Errorf("%s: value %q for %s", tc.label, value, changed)
		}
		for _, lang := range []webui.Lang{webui.LangEN, webui.LangKO} {
			text := fmt.Sprintf(webui.Text(lang, off), "8443", "")
			changedText := fmt.Sprintf(webui.Text(lang, changed), "8443", value)
			if !strings.Contains(text, tc.command) || strings.Contains(text+changedText, "%!") || strings.Contains(text+changedText, " 443") || strings.Contains(text+changedText, "=443") {
				t.Errorf("%s, %s: %q lacks %s, or names another port", tc.label, lang, text, tc.command)
			}
			if off != webui.MsgTSRemoveSteps && strings.Contains(text+changedText, "--https=8443 off") {
				t.Errorf("%s, %s: gives a command that Tailscale refuses or that does not reach it: %q", tc.label, lang, text)
			}
			if changed == webui.MsgTSChangedSteps && !strings.Contains(changedText, `"tailscale serve --bg --https=8443 `+target+`"`) {
				t.Errorf("%s, %s: %q", tc.label, lang, changedText)
			}
		}
	}
	// The Settings page and "status" use the same choice, for the port of
	// the record.
	record := state.TailscaleServe{Name: name, HTTPSPort: 8443, Target: target}
	info := tailscaleInfo(TailscaleReport{Installed: true, On: true, Sharing: &record, Endpoint: TailscaleEndpointChanged,
		Found: tailscale.ServeConfig{TCP: map[string]tailscale.TCPHandler{"8443": {TCPForward: "127.0.0.1:22"}}}.Endpoint(name, 8443, target).Found})
	if info.FoundFix != webui.MsgTSChangedStepsOther || info.FoundFixPort != "8443" {
		t.Errorf("Settings gives %s for port %q for TCP forwarding", info.FoundFix, info.FoundFixPort)
	}
}

// While a --base-url option keeps the running server giving out another
// address, sharing does not claim the HTTPS clone address: the report
// names the option, and the page says so in place of the clone hint.
func TestABaseURLOptionIsNamedInsteadOfTheHTTPSCloneAddress(t *testing.T) {
	app := newConfiguredApp(t)
	app, _ = withTailscale(t, app, tailscaletest.State{Status: tailscaletest.Running()})
	const option = "http://gitbox.lan:7654"
	app.Network = NewLiveNetwork(LiveNetworkConfig{
		Record: state.RunningNetwork{
			Listen: "127.0.0.1:7654", Address: "127.0.0.1:7654", ListenSource: NetworkSourceDefault,
			BaseURL: option, BaseURLSource: NetworkSourceFlag, Origin: option,
			SavedHosts: []string{}, TrustedProxies: []string{}, TrustedProxiesSource: NetworkSourceDefault,
		},
		BaseURL: option, Hosts: app.Hosts,
		Publish: func(running state.RunningNetwork) {
			noErr(t, app.Store.PublishRunningNetwork(context.Background(), running))
		},
	})
	app.Network.Publish()
	app.Tailscale.Live = app.Network
	_, err := app.Tailscale.On(t.Context(), nil, 0)
	noErr(t, err)
	report, err := app.Tailscale.Report(t.Context())
	noErr(t, err)
	if report.BaseURLOption != option || !report.Ready {
		t.Fatalf("report=%+v", report)
	}
	if clone := app.cloneURL(httptest.NewRequest(http.MethodGet, "/", nil), "project"); clone != option+"/git/project.git" {
		t.Fatalf("clone address=%q", clone)
	}
	_, _, _, page := networkSettingsClient(t, app)
	hint := html.EscapeString(fmt.Sprintf(webui.Text(webui.LangEN, webui.MsgTSCloneHint), report.URL))
	note := html.EscapeString(fmt.Sprintf(webui.Text(webui.LangEN, webui.MsgTSBaseURLOption), option))
	if !strings.Contains(page, note) || strings.Contains(page, hint) {
		t.Fatal("the page claims the HTTPS clone address, or does not name the option")
	}
}

// A request never waits for Tailscale beyond its own deadline. When
// Tailscale answers too slowly, a refused turning on is still answered
// within the page limit, and the Tailscale block says that Tailscale did not
// answer in time instead of waiting for the reading.
func TestSharingPageDoesNotWaitForTailscaleBeyondItsDeadline(t *testing.T) {
	app, fake := tailscaleApp(t, tailscaletest.State{Status: tailscaletest.Running()})
	app.HTTPTimeout = verifiedPage
	client, base, csrf, _ := networkSettingsClient(t, app)
	// Turning on reads the status once and fails; the page's reading then
	// waits for Tailscale until the test lets it answer, beyond the deadline.
	fake.Update(func(s *tailscaletest.State) {
		s.StatusError, s.HoldReads, s.PassReads, s.Calls = "synthetic unexplained failure", true, 1, nil
	})
	release := func() { fake.Update(func(s *tailscaletest.State) { s.HoldReads = false }) }
	t.Cleanup(release)
	values := tailscaleForm(csrf, webui.ActionTailscaleOn, "admin-password", false)
	// A page that waited for the held reading would never answer.
	ctx, cancel := context.WithTimeout(context.Background(), hangBound)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/settings", strings.NewReader(values.Encode()))
	noErr(t, err)
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("Origin", base)
	started := time.Now()
	response, err := client.Do(request)
	elapsed := time.Since(started)
	if err != nil {
		t.Fatalf("no answer after %v: %v", elapsed, err)
	}
	body, err := io.ReadAll(response.Body)
	response.Body.Close()
	if err != nil {
		t.Fatalf("the answer broke off after %v: %v", time.Since(started), err)
	}
	if response.StatusCode != http.StatusServiceUnavailable || elapsed >= app.HTTPTimeout ||
		!strings.Contains(string(body), enText(webui.TailscaleProblemCode(string(tailscale.KindTimeout)))) {
		t.Fatalf("status=%d after %v, body=%s", response.StatusCode, elapsed, body)
	}
	// Only turning on was answered; the page's reading is still waiting.
	fake.AwaitHeldReads(1)
	if calls := fake.Calls(); !slices.Equal(calls, []string{"status --json"}) {
		t.Fatalf("answered %q, want only the status read of turning on", calls)
	}
	// The reading goes on in the background; wait for it before the fake
	// is removed.
	release()
	_, err = app.Tailscale.Report(context.Background())
	noErr(t, err)
}

// When OwnGit cannot read its own state for the Tailscale block, the block
// says so and the cause is logged once; it is not shown as an error that
// Tailscale reported.
func TestSharingStateThatOwnGitCannotReadIsNotBlamedOnTailscale(t *testing.T) {
	app, _ := tailscaleApp(t, tailscaletest.State{Status: tailscaletest.Running()})
	app.Tailscale.Observe = func(context.Context) (state.RunningObservation, error) {
		return state.RunningObservation{}, errors.New("synthetic running record read failure")
	}
	serverLog := captureServerLog(t)
	_, _, _, body := networkSettingsClient(t, app)
	if !strings.Contains(body, enText(webui.MsgTSStateUnreadable)) || strings.Contains(body, enText(webui.MsgTSProblemFailed)) {
		t.Fatalf("the Tailscale block does not say that OwnGit could not read its state:\n%s", body)
	}
	lines := loggedFailures(serverLog, 0)
	checkLoggedSteps(t, "the sharing state read", lines, "sharing state read")
	if !strings.Contains(strings.Join(lines, "\n"), "synthetic running record read failure") {
		t.Errorf("the log does not name the cause: %q", lines)
	}
}
