package server

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"strings"

	"owngit/internal/state"
	"owngit/internal/tailscale"
	"owngit/internal/webui"
)

// The Tailscale block of the Settings page shows TailscaleReport and turns
// sharing on or off with the same rules as "owngit tailscale". Unlike that
// command it changes the running server at once, since the page is served
// by it.

// tailscaleBlock reads the state of sharing for the page. It never fails the
// page: a state that cannot be read is shown as a Tailscale problem.
//
// Only an administrator sees what Tailscale reports beyond OwnGit's own
// endpoint: what else is on the port, addresses under earlier names and what
// Tailscale printed, since they can name other services on this computer.
// Other viewers see that the port is taken.
func (app *App) tailscaleBlock(ctx context.Context, admin bool) webui.TailscaleInfo {
	if app.Tailscale == nil {
		return webui.TailscaleInfo{Problem: webui.TailscaleProblemCode(string(tailscale.KindNotInstalled))}
	}
	report, err := app.Tailscale.Report(ctx)
	if err != nil {
		return webui.TailscaleInfo{Problem: webui.MsgTSProblemFailed}
	}
	info := tailscaleInfo(report)
	if !admin {
		info.ProblemDetail, info.Stale = "", nil
		info.Problem = webui.TailscaleProblemBrief(info.Problem)
		if info.Found != nil {
			info.Found, info.FoundFix, info.FoundFixValue = nil, "", ""
			info.FoundNote = webui.TailscalePortNoteBrief(info.FoundNote)
		}
	}
	return info
}

// tailscaleInfo turns the report into the page block.
func tailscaleInfo(report TailscaleReport) webui.TailscaleInfo {
	info := webui.TailscaleInfo{
		On: report.On, Ready: report.Ready, URL: report.URL, Name: report.Name,
		MacApp: report.MacApp, HomeNetwork: report.HomeNetwork, ProblemDetail: report.ProblemDetail,
	}
	if report.Sharing != nil && info.Name == "" {
		info.Name = report.Sharing.Name
	}
	if report.Problem != "" {
		info.Problem = webui.TailscaleProblemCode(report.Problem)
	}
	for _, wait := range report.Waiting {
		if wait != TailscaleWaitTailscale {
			info.Waiting = append(info.Waiting, webui.TailscaleWaitCode(wait))
		}
	}
	switch {
	case report.On && report.Endpoint == TailscaleEndpointChanged:
		info.Found, info.FoundNote = tailscaleUses(report.Found), webui.MsgTSChanged
		info.FoundFix, info.FoundFixValue = TailscaleFix(report.Found, true, report.Sharing.Target)
	case !report.On && report.Endpoint == TailscaleEndpointTaken:
		info.Found, info.FoundNote = tailscaleUses(report.Found), webui.MsgTSTaken
		info.FoundFix, _ = TailscaleFix(report.Found, false, "")
	case !report.On && report.Endpoint == TailscaleEndpointUnrecorded:
		info.Found, info.FoundNote = tailscaleUses(report.Found), webui.MsgTSUnrecorded
		info.FoundFix, _ = TailscaleFix(report.Found, false, "")
	}
	info.Stale = tailscaleUses(report.Stale)
	info.CanTurnOn, info.CanTurnOff = report.CanTurnOn, report.CanTurnOff
	// A --listen option decides where the running server listens, so the
	// home network choice would change nothing then.
	info.ListenOption = report.ListenOption
	if info.ListenOption == "" {
		home, local := true, false
		info.HomeListen, info.LocalListen = planListen(report.Listen, &home), planListen(report.Listen, &local)
	}
	return info
}

// changeTailscale handles ActionTailscaleOn and ActionTailscaleOff after the
// administrator password was verified.
func (app *App) changeTailscale(writer http.ResponseWriter, request *http.Request, settings state.Settings, csrf, action string) {
	if app.Tailscale == nil {
		app.renderTailscaleRefusal(writer, request, settings, csrf, action, &TailscaleError{Problem: string(tailscale.KindNotInstalled)})
		return
	}
	if action == webui.ActionTailscaleOn {
		homeNetwork := formChecked(postValue(request, "home_network"))
		if _, err := app.Tailscale.On(request.Context(), &homeNetwork); err != nil {
			app.renderTailscaleRefusal(writer, request, settings, csrf, action, err)
			return
		}
		app.noticeRedirect(writer, request, "/settings?notice=tailscale_on", http.StatusSeeOther)
		return
	}
	// A page opened through the tailnet address is answered through it once
	// more, but the next request would find Tailscale's address gone or
	// OwnGit no longer accepting the name. So that answer is the result page
	// itself instead of a redirect.
	away := app.throughTailscale(request)
	change, err := app.Tailscale.Off(request.Context())
	if err != nil {
		app.renderTailscaleRefusal(writer, request, settings, csrf, action, err)
		return
	}
	if away {
		app.renderTailscaleOff(writer, request, change.Record.Target+"/")
		return
	}
	app.noticeRedirect(writer, request, "/settings?notice=tailscale_off", http.StatusSeeOther)
}

// renderTailscaleOff answers with the page that needs nothing more from the
// address that sharing no longer serves.
func (app *App) renderTailscaleOff(writer http.ResponseWriter, request *http.Request, local string) {
	var body bytes.Buffer
	page := webui.TailscaleOffPage{Lang: app.language(writer, request), Appearance: app.appearance(writer, request), Local: local}
	if err := app.Renderer.RenderTailscaleOff(&body, page); err != nil {
		app.writePlainError(writer, http.StatusInternalServerError)
		return
	}
	writer.Header().Set("Content-Type", "text/html; charset=utf-8")
	writer.Header().Set("Cache-Control", "no-store")
	writer.WriteHeader(http.StatusOK)
	_, _ = writer.Write(body.Bytes())
}

// renderTailscaleRefusal shows why turning sharing on or off did nothing,
// inside the form that was sent.
func (app *App) renderTailscaleRefusal(writer http.ResponseWriter, request *http.Request, settings state.Settings, csrf, action string, err error) {
	var refusal *TailscaleError
	if !errors.As(err, &refusal) {
		app.renderSettingsPage(writer, request, settings, csrf, action, []webui.Notice{webui.Error("tailscale", webui.MsgErrUnavailable)}, http.StatusServiceUnavailable, settingsView{AdminVerified: true})
		return
	}
	// What is on the port is listed in the block, in the page's language.
	notice := webui.Notice{Kind: webui.NoticeError, Code: webui.TailscaleRefusalCode(refusal.Problem, len(refusal.Found) > 0), Field: "tailscale", Detail: refusal.Detail}
	if len(refusal.Found) > 0 {
		notice.Detail = ""
	}
	notices := []webui.Notice{notice}
	if refusal.Problem == TailscaleProblemReadBack && refusal.MacApp {
		notices = append(notices, webui.Notice{Kind: webui.NoticeInfo, Code: webui.MsgTSReadBackMacApp, Field: "tailscale"})
	}
	app.renderSettingsPage(writer, request, settings, csrf, action, notices, http.StatusConflict, settingsView{AdminVerified: true})
}

// tailscaleUses turns what Tailscale has on its port into the page's form.
func tailscaleUses(uses []tailscale.Use) []webui.TailscaleUse {
	converted := make([]webui.TailscaleUse, len(uses))
	for i, use := range uses {
		converted[i] = webui.TailscaleUse{Kind: use.Kind, Address: use.Address, Target: use.Target}
	}
	return converted
}

// TailscaleFix is the step that clears what is on the HTTPS port: undoing
// a changed endpoint while sharing is on (changed), or removing a use so
// that sharing can be turned on. The value, when not empty, goes into the
// message. "tailscale serve --https=443 off" removes only web handlers:
// Tailscale refuses it while the port forwards TCP, it does not remove
// plain HTTP, and a "tailscale serve" running in a terminal ends only
// there. Other uses get the step that fits them.
func TailscaleFix(found []tailscale.Use, changed bool, target string) (webui.MessageCode, string) {
	kinds := map[string]bool{}
	for _, use := range found {
		kinds[use.Kind] = true
	}
	web := func(kind string) bool {
		switch kind {
		case tailscale.UseProxy, tailscale.UseFiles, tailscale.UseRedirect, tailscale.UseText, tailscale.UseEmpty, tailscale.UseFunnel:
			return true
		}
		return false
	}
	onlyWebAnd := func(extra string) bool {
		for kind := range kinds {
			if kind != extra && !web(kind) {
				return false
			}
		}
		return true
	}
	code := webui.MsgTSRemoveStepsOther
	switch {
	case kinds[tailscale.UseForeground]:
		code = webui.MsgTSRemoveStepsForeground
	case len(kinds) == 1 && kinds[tailscale.UseTCPForward]:
		code = webui.MsgTSRemoveStepsTCP
	case kinds[tailscale.UsePlainHTTP] && onlyWebAnd(tailscale.UsePlainHTTP):
		code = webui.MsgTSRemoveStepsHTTP
	case onlyWebAnd(""):
		if changed {
			return webui.MsgTSChangedSteps, target
		}
		return webui.MsgTSRemoveSteps, ""
	}
	if changed {
		return webui.MsgTSChangedStepsOther, ""
	}
	return code, ""
}

// TailscaleUsesText describes what Tailscale has on its port in English,
// for the command line and errors.
func TailscaleUsesText(uses []tailscale.Use) string {
	texts := make([]string, len(uses))
	for i, use := range tailscaleUses(uses) {
		texts[i] = webui.TailscaleUseText(webui.LangEN, use)
	}
	return strings.Join(texts, "; ")
}
