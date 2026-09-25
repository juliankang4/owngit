package server

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"

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
