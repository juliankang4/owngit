package server

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"reflect"
	"strings"
	"testing"

	"owngit/internal/tailscale"
	"owngit/internal/tailscale/tailscaletest"
	"owngit/internal/webui"
)

// busyServe has another program on 443, with two paths, a Funnel on 8443,
// a handler of another name on 10000 and a handler on 5000.
func busyServe() tailscale.ServeConfig {
	name := tailscaletest.Name
	config := otherService(tailscale.ServeConfig{AllowFunnel: map[string]bool{name + ":8443": true}}, name, 443, 8443, 5000)
	config.Web[name+":443"].Handlers["/api"] = tailscale.Handler{Proxy: "http://127.0.0.1:4000"}
	config.TCP["10000"] = tailscale.TCPHandler{HTTPS: true}
	config.Web["oldbox.tail0000.ts.net:10000"] = tailscale.WebServer{Handlers: map[string]tailscale.Handler{"/": {Proxy: "http://127.0.0.1:9090"}}}
	return config
}

// OwnGit replaces what another service has on a port only as the owner
// reviewed it: a change after the review, however small, replaces nothing
// and shows what is there now. It never replaces a Funnel port, and the
// replacement leaves every other port and name exactly as they were.
func TestReplacingAnEndpointOnlyAsReviewed(t *testing.T) {
	ctx := context.Background()
	app, fake := tailscaleApp(t, tailscaletest.State{Status: tailscaletest.Running(), Serve: busyServe()})
	_, err := app.Tailscale.On(ctx, nil, 443)
	var refusal *TailscaleError
	if !errors.As(err, &refusal) || refusal.Problem != TailscaleProblemTaken || refusal.Occupied == nil || !refusal.Occupied.Replaceable || len(refusal.Occupied.Found) != 2 {
		t.Fatalf("a taken named port: err=%v occupied=%+v", err, refusal.Occupied)
	}
	reviewed := refusal.Occupied.Digest

	fake.Update(func(s *tailscaletest.State) {
		s.Serve.Web[tailscaletest.Name+":443"].Handlers["/api"] = tailscale.Handler{Proxy: "http://127.0.0.1:4001"}
	})
	_, err = app.Tailscale.Replace(ctx, nil, 443, reviewed)
	if !errors.As(err, &refusal) || refusal.Problem != TailscaleProblemReplaceChanged || refusal.Occupied == nil || refusal.Occupied.Digest == reviewed || len(fake.Writes()) != 0 {
		t.Fatalf("a port changed after the review: err=%v writes=%q", err, fake.Writes())
	}
	if !refused(err) {
		t.Error("a changed port is not answered as refused")
	}

	_, err = app.Tailscale.On(ctx, nil, 8443)
	if !errors.As(err, &refusal) || refusal.Occupied == nil || refusal.Occupied.Replaceable {
		t.Fatalf("a Funnel port offered for replacement: err=%v", err)
	}
	_, err = app.Tailscale.Replace(ctx, nil, 8443, refusal.Occupied.Digest)
	if !errors.As(err, &refusal) || refusal.Problem != TailscaleProblemNotReplaceable || len(fake.Writes()) != 0 {
		t.Fatalf("replacing a Funnel port: err=%v writes=%q", err, fake.Writes())
	}

	before := fake.State().Serve
	_, err = app.Tailscale.On(ctx, nil, 443)
	errors.As(err, &refusal)
	change, err := app.Tailscale.Replace(ctx, nil, 443, refusal.Occupied.Digest)
	noErr(t, err)
	if change.Endpoint != endpointCreated || !change.Record.Created || change.Record.BaseURL != "https://"+tailscaletest.Name || !fake.Endpoint(443, tailscale.Target(7654)).Exact {
		t.Fatalf("replacing: %+v serve=%+v", change, fake.State().Serve)
	}
	after := fake.State().Serve
	for _, port := range []int{8443, 10000, 5000} {
		if !reflect.DeepEqual(portEntries(after, port), portEntries(before, port)) {
			t.Fatalf("port %d changed: %+v, was %+v", port, portEntries(after, port), portEntries(before, port))
		}
	}
	if response := throughServe(app, "/"); response.Code == http.StatusMisdirectedRequest {
		t.Fatal("the running server does not answer at the replaced address")
	}
	// OwnGit made the endpoint that is there now, so turning off removes
	// it; what it replaced does not come back.
	off, err := app.Tailscale.Off(ctx)
	noErr(t, err)
	if off.Endpoint != "removed" || !fake.Endpoint(443, "").Free {
		t.Fatalf("off after replacing: %+v", off)
	}
}

// A replacement is still bound to Tailscale's version of the whole
// configuration when it writes: another change that lands in between,
// here on another port, is kept and the replacement is refused.
func TestReplacingKeepsAChangeMadeMeanwhile(t *testing.T) {
	ctx := context.Background()
	app, fake := tailscaleApp(t, tailscaletest.State{Status: tailscaletest.Running(), Serve: busyServe()})
	_, err := app.Tailscale.On(ctx, nil, 443)
	var refusal *TailscaleError
	errors.As(err, &refusal)
	meanwhile := busyServe()
	meanwhile.AllowFunnel[tailscaletest.Name+":5000"] = true
	fake.Update(func(s *tailscaletest.State) { s.ChangedBeforeWrite = &meanwhile })
	_, err = app.Tailscale.Replace(ctx, nil, 443, refusal.Occupied.Digest)
	if !errors.As(err, &refusal) || refusal.Problem != TailscaleProblemServeChanged {
		t.Fatalf("err=%v, want %s", err, TailscaleProblemServeChanged)
	}
	if serve := fake.State().Serve; !reflect.DeepEqual(serve, meanwhile) {
		t.Fatalf("Tailscale has %+v, want the change made meanwhile", serve)
	}
	if _, on, err := app.Store.TailscaleServe(ctx); err != nil || on {
		t.Fatalf("sharing recorded on=%v err=%v", on, err)
	}
}

// In Settings, a custom port saved while sharing is on moves it there and
// warns about clones; a taken port offers to replace exactly what is
// listed, and a stale review is refused and shown again.
func TestSettingsMovesSharingAndReplacesAReviewedEndpoint(t *testing.T) {
	name := tailscaletest.Name
	app, fake := tailscaleApp(t, tailscaletest.State{Status: tailscaletest.Running(), Serve: otherService(tailscale.ServeConfig{}, name, 9443)})
	client, base, csrf, _ := networkSettingsClient(t, app)
	form := func(fields map[string]string) url.Values {
		values := tailscaleForm(csrf, webui.ActionSaveTailscale, "admin-password", false)
		values.Set("tailscale", "on")
		for field, value := range fields {
			values.Set(field, value)
		}
		return values
	}
	requireSaved(t, "turning on", browserForm(t, client, base+"/settings", form(nil), base))
	page, _ := dashboardGET(t, client, base+"/settings/network")
	if !strings.Contains(page, `name="tailscale_port" data-saved="auto"`) || !strings.Contains(page, enText(webui.MsgTSPortHelp)) {
		t.Fatalf("the port choice is not offered:\n%s", page)
	}
	for _, bad := range []string{"0", "65536", "port"} {
		result := browserForm(t, client, base+"/settings", form(map[string]string{"tailscale_port": "custom", "tailscale_https_port": bad}), base)
		if result.status != http.StatusUnprocessableEntity || !strings.Contains(result.body, enText(webui.MsgTSPortInvalid)) {
			t.Fatalf("port %q: status=%d", bad, result.status)
		}
	}
	// Only the port changes: that is a change, not "Nothing changed".
	result := browserForm(t, client, base+"/settings", form(map[string]string{"tailscale_port": "custom", "tailscale_https_port": "4443"}), base)
	if result.status != http.StatusSeeOther || result.header.Get("Location") != "/settings/network?notice=tailscale_moved#grp-tailscale" {
		t.Fatalf("moving: status=%d location=%q", result.status, result.header.Get("Location"))
	}
	page, _ = dashboardGET(t, client, base+result.header.Get("Location"))
	if !strings.Contains(page, enText(webui.MsgTSMoved)) || !strings.Contains(page, `value="https://`+name+`:4443/"`) || !strings.Contains(page, `name="tailscale_https_port" type="text" inputmode="numeric" autocomplete="off" spellcheck="false"`+"\n"+`                 value="4443"`) {
		t.Fatalf("after moving:\n%s", page)
	}
	if !fake.Endpoint(443, "").Free || !fake.Endpoint(4443, tailscale.Target(7654)).Exact {
		t.Fatalf("serve after moving: %+v", fake.State().Serve)
	}
	// Automatic keeps the port sharing uses now.
	result = browserForm(t, client, base+"/settings", form(map[string]string{"tailscale_port": "auto"}), base)
	if result.status != http.StatusOK || !strings.Contains(result.body, enText(webui.MsgSettingsNothing)) {
		t.Fatalf("automatic while on: status=%d", result.status)
	}

	// A taken custom port: refused, with what is there and its replacement.
	result = browserForm(t, client, base+"/settings", form(map[string]string{"tailscale_port": "custom", "tailscale_https_port": "9443"}), base)
	if result.status != http.StatusConflict || !strings.Contains(result.body, "https://"+name+":9443/ to http://127.0.0.1:3000") || !strings.Contains(result.body, `name="replace_digest"`) {
		t.Fatalf("a taken port: status=%d", result.status)
	}
	digest := formValue(t, result.body[strings.Index(result.body, `name="replace_digest"`)-200:], "replace_digest")
	fake.Update(func(s *tailscaletest.State) {
		s.Serve.Web[name+":9443"].Handlers["/docs"] = tailscale.Handler{Text: "docs"}
	})
	replace := form(map[string]string{"tailscale_port": "custom", "tailscale_https_port": "9443", "replace_digest": digest})
	result = browserForm(t, client, base+"/settings", replace, base)
	if result.status != http.StatusConflict || !strings.Contains(result.body, enText(webui.TailscaleProblemCode(TailscaleProblemReplaceChanged))) || !strings.Contains(result.body, "https://"+name+":9443/docs") {
		t.Fatalf("a stale review: status=%d", result.status)
	}
	if !fake.Endpoint(4443, tailscale.Target(7654)).Exact || fake.Endpoint(9443, "").Free {
		t.Fatalf("a stale review changed Serve: %+v", fake.State().Serve)
	}
	replace.Set("replace_digest", formValue(t, result.body[strings.Index(result.body, `name="replace_digest"`)-200:], "replace_digest"))
	result = browserForm(t, client, base+"/settings", replace, base)
	if result.status != http.StatusSeeOther || result.header.Get("Location") != "/settings/network?notice=tailscale_moved#grp-tailscale" {
		t.Fatalf("replacing: status=%d location=%q", result.status, result.header.Get("Location"))
	}
	if !fake.Endpoint(9443, tailscale.Target(7654)).Exact || !fake.Endpoint(4443, "").Free {
		t.Fatalf("serve after replacing: %+v", fake.State().Serve)
	}
}
