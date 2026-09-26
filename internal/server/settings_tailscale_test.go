package server

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"regexp"
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

// Every Settings viewer sees whether sharing is on, what turning it on
// records in public and the home network choice, which starts unticked for
// an owner who listens on this computer only.
func TestEveryViewerSeesTheTailscaleBlock(t *testing.T) {
	app, fake := tailscaleApp(t, tailscaletest.State{Status: tailscaletest.Running()})
	_, _, _, body := networkSettingsClient(t, app)
	for _, want := range []string{
		`id="tailscale"`, enText(webui.MsgTSOff), `value="tailscale_on"`, enText(webui.MsgTSHome),
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
	if result.status != http.StatusUnauthorized || !strings.Contains(result.body, `aria-describedby="tailscale_on-admin_password-note"`) {
		t.Fatalf("wrong administrator password: status=%d", result.status)
	}
	if len(fake.Writes()) != 0 {
		t.Fatalf("a wrong administrator password changed Tailscale: %q", fake.Writes())
	}
	if _, on, _ := app.Store.TailscaleServe(t.Context()); on {
		t.Fatal("a wrong administrator password turned sharing on")
	}

	result = browserForm(t, client, base+"/settings", tailscaleForm(csrf, webui.ActionTailscaleOn, "admin-password", false), base)
	if result.status != http.StatusSeeOther || result.header.Get("Location") != "/settings?notice=tailscale_on" {
		t.Fatalf("turn on: status=%d location=%q body=%s", result.status, result.header.Get("Location"), result.body)
	}
	body, _ := dashboardGET(t, client, base+result.header.Get("Location"))
	for _, want := range []string{enText(webui.MsgTSTurnedOn), enText(webui.MsgTSReady), `value="https://` + tailscaletest.Name + `/"`, `value="tailscale_off"`} {
		if !strings.Contains(body, want) {
			t.Errorf("after turning on, Settings lacks %q", want)
		}
	}
	// A page reached through the Tailscale endpoint says that Tailscale on
	// this computer encrypted it.
	if response := throughServe(app, "/settings"); response.Code != http.StatusOK || !strings.Contains(response.Body.String(), enText(webui.MsgConnTailscaleOn)) ||
		strings.Contains(response.Body.String(), enText(webui.MsgConnEncrypted)) {
		t.Fatalf("through Serve: status=%d, indicator not naming Tailscale", response.Code)
	}

	result = browserForm(t, client, base+"/settings", tailscaleForm(csrf, webui.ActionTailscaleOff, "admin-password", false), base)
	if result.status != http.StatusSeeOther || result.header.Get("Location") != "/settings?notice=tailscale_off" {
		t.Fatalf("turn off: status=%d body=%s", result.status, result.body)
	}
	if len(fake.Writes()) != 2 {
		t.Fatalf("writes=%q", fake.Writes())
	}
	if response := throughServe(app, "/settings"); response.Code != http.StatusMisdirectedRequest {
		t.Fatalf("after turning off, the Tailscale name got %d", response.Code)
	}
}

// A refusal is explained on the block, with what is on the port, even
// though the block then no longer offers the form.
func TestTailscaleRefusalIsExplainedOnTheBlock(t *testing.T) {
	app, fake := tailscaleApp(t, tailscaletest.State{Status: tailscaletest.Running()})
	client, base, csrf, _ := networkSettingsClient(t, app)
	fake.Update(func(fakeState *tailscaletest.State) {
		fakeState.Serve = tailscale.ServeConfig{
			TCP: map[string]tailscale.TCPHandler{"443": {HTTPS: true}},
			Web: map[string]tailscale.WebServer{tailscaletest.Name + ":443": {Handlers: map[string]tailscale.Handler{"/": {Proxy: "http://127.0.0.1:3000"}}}},
		}
	})
	result := browserForm(t, client, base+"/settings", tailscaleForm(csrf, webui.ActionTailscaleOn, "admin-password", true), base)
	if result.status != http.StatusConflict {
		t.Fatalf("status=%d", result.status)
	}
	for _, want := range []string{
		`id="tailscale_on-tailscale-note"`, `role="alert"`, "autofocus",
		enText(webui.TailscaleRefusalCode(TailscaleProblemTaken, true)),
		// The list below the alert, in both languages.
		"https://" + tailscaletest.Name + ":443/ to http://127.0.0.1:3000",
		"https://" + tailscaletest.Name + ":443/에서 http://127.0.0.1:3000(으)로 전달",
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
	_, err := app.Tailscale.On(t.Context(), nil)
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
	body, _ := dashboardGET(t, client, base+"/settings")
	for _, want := range []string{enText(webui.TailscaleWaitCode(TailscaleWaitUnfinished)), `value="tailscale_on"`, `value="tailscale_off"`} {
		if !strings.Contains(body, want) {
			t.Errorf("the unfinished state lacks %q", want)
		}
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

// A viewer who is not the administrator sees that the port is taken, but not
// the backend addresses of other services, addresses under earlier names or
// what Tailscale printed. An administrator session sees them.
func TestOnlyTheAdministratorSeesWhatElseTailscaleServes(t *testing.T) {
	app, fake := tailscaleApp(t, tailscaletest.State{Status: tailscaletest.Running()})
	fake.Update(func(s *tailscaletest.State) {
		s.Serve = tailscale.ServeConfig{
			TCP: map[string]tailscale.TCPHandler{"443": {HTTPS: true}},
			Web: map[string]tailscale.WebServer{
				tailscaletest.Name + ":443":  {Handlers: map[string]tailscale.Handler{"/": {Proxy: "http://127.0.0.1:3000"}}},
				"oldbox.tail0000.ts.net:443": {Handlers: map[string]tailscale.Handler{"/": {Proxy: "http://127.0.0.1:9090"}}},
			},
		}
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
	body, _ = dashboardGET(t, client, base+"/settings")
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
	change, err := app.Tailscale.On(ctx, nil)
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
	body, _ := dashboardGET(t, client, base+"/settings")
	steps := fmt.Sprintf(enText(webui.MsgTSChangedSteps), change.Record.Target)
	for _, want := range []string{enText(webui.TailscaleWaitCode(TailscaleWaitChanged)), steps, "http://127.0.0.1:7701"} {
		if !strings.Contains(body, want) {
			t.Errorf("the changed state lacks %q", want)
		}
	}
	for _, refused := range []string{`value="tailscale_on"`, `value="tailscale_off"`} {
		if strings.Contains(body, refused) {
			t.Errorf("the changed state offers %s, which would be refused", refused)
		}
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
	_, err = app.Tailscale.On(ctx, nil)
	noErr(t, err)
	fake.Update(func(s *tailscaletest.State) { s.Serve = changed })
	if _, err := app.Tailscale.Off(ctx); err == nil {
		t.Fatal("off accepted a changed endpoint")
	}
	_, err = app.Tailscale.On(ctx, nil)
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
	_, err := app.Tailscale.On(t.Context(), nil)
	noErr(t, err)
	fake.Update(func(s *tailscaletest.State) {
		s.Status.BackendState = "Stopped"
		s.WriteError = "Tailscale is stopped."
	})
	app.Tailscale.forget()
	client, base, csrf, _ := networkSettingsClient(t, app)
	result := browserForm(t, client, base+"/settings", tailscaleForm(csrf, webui.ActionTailscaleOff, "admin-password", false), base)
	if result.status != http.StatusConflict {
		t.Fatalf("turn off: status=%d", result.status)
	}
	for _, want := range []string{enText(webui.TailscaleProblemCode(string(tailscale.KindStopped))), "Tailscale is stopped."} {
		if !strings.Contains(result.body, want) {
			t.Errorf("the refusal lacks %q", want)
		}
	}
	if strings.Contains(result.body, enText(webui.MsgTSProblemFailed)) {
		t.Error("the refusal shows Tailscale's error as an unexplained failure")
	}
	if _, on, _ := app.Store.TailscaleServe(t.Context()); !on {
		t.Fatal("a refused turning off took back the settings")
	}
}

// Turning off from a page opened through the tailnet address ends on a page
// that needs nothing more from that address, which then no longer reaches
// OwnGit, and that names the address that works on this computer.
func TestTurningOffThroughTheTailnetAddressEndsOnAPageThatLoads(t *testing.T) {
	app, fake := tailscaleApp(t, tailscaletest.State{Status: tailscaletest.Running()})
	change, err := app.Tailscale.On(t.Context(), nil)
	noErr(t, err)
	page := throughServe(app, "/settings?lang=ko&appearance=dark")
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
	if next := throughServe(app, "/settings"); next.Code != http.StatusMisdirectedRequest {
		t.Fatalf("after turning off, the Tailscale name got %d", next.Code)
	}
}

// The step that clears the HTTPS port fits what is on it: "tailscale serve
// --https=443 off" removes only web handlers, so TCP forwarding, plain HTTP
// and a "tailscale serve" running in a terminal each get their own step,
// and anything else a general one. Both languages give the same command.
func TestTailscaleFixFitsWhatIsOnThePort(t *testing.T) {
	const name, target = tailscaletest.Name, "http://127.0.0.1:7654"
	https := map[string]tailscale.TCPHandler{"443": {HTTPS: true}}
	web := func(handlers map[string]tailscale.Handler) map[string]tailscale.WebServer {
		return map[string]tailscale.WebServer{name + ":443": {Handlers: handlers}}
	}
	other := web(map[string]tailscale.Handler{"/": {Proxy: "http://127.0.0.1:3000"}})
	for _, tc := range []struct {
		label   string
		config  tailscale.ServeConfig
		off     webui.MessageCode
		changed webui.MessageCode
		command string
	}{
		{"a web handler", tailscale.ServeConfig{TCP: https, Web: other}, webui.MsgTSRemoveSteps, webui.MsgTSChangedSteps, `"tailscale serve --https=443 off"`},
		{"a web handler with Funnel", tailscale.ServeConfig{TCP: https, Web: other, AllowFunnel: map[string]bool{name + ":443": true}}, webui.MsgTSRemoveSteps, webui.MsgTSChangedSteps, `"tailscale serve --https=443 off"`},
		{"TCP forwarding", tailscale.ServeConfig{TCP: map[string]tailscale.TCPHandler{"443": {TCPForward: "127.0.0.1:22"}}}, webui.MsgTSRemoveStepsTCP, webui.MsgTSChangedStepsOther, `"tailscale serve --tcp=443 off"`},
		{"plain HTTP", tailscale.ServeConfig{TCP: map[string]tailscale.TCPHandler{"443": {HTTP: true}}, Web: other}, webui.MsgTSRemoveStepsHTTP, webui.MsgTSChangedStepsOther, `"tailscale serve --http=443 off"`},
		{"a foreground session", tailscale.ServeConfig{Foreground: map[string]tailscale.ServeConfig{"session": {TCP: https, Web: other}}}, webui.MsgTSRemoveStepsForeground, webui.MsgTSChangedStepsOther, "Ctrl+C"},
		{"an incomplete setting", tailscale.ServeConfig{TCP: https}, webui.MsgTSRemoveStepsOther, webui.MsgTSChangedStepsOther, `"tailscale serve status"`},
	} {
		found := tc.config.Endpoint(name, 443, target).Found
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
			text := webui.Text(lang, off)
			if !strings.Contains(text, tc.command) {
				t.Errorf("%s, %s: %q lacks %s", tc.label, lang, text, tc.command)
			}
			if off != webui.MsgTSRemoveSteps && strings.Contains(text+webui.Text(lang, changed), "--https=443 off") {
				t.Errorf("%s, %s: gives a command that Tailscale refuses or that does not reach it: %q", tc.label, lang, text)
			}
		}
	}
	// The Settings page and "status" use the same choice.
	info := tailscaleInfo(TailscaleReport{Installed: true, Endpoint: TailscaleEndpointTaken,
		Found: tailscale.ServeConfig{TCP: map[string]tailscale.TCPHandler{"443": {TCPForward: "127.0.0.1:22"}}}.Endpoint(name, 443, target).Found})
	if info.FoundFix != webui.MsgTSRemoveStepsTCP {
		t.Errorf("Settings gives %s for TCP forwarding", info.FoundFix)
	}
}
