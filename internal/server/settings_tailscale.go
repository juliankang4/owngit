package server

import (
	"context"
	"errors"
	"net/http"
	"slices"
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
		if info.Found != nil {
			info.Found = nil
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
	case !report.On && report.Endpoint == TailscaleEndpointTaken:
		info.Found, info.FoundNote = tailscaleUses(report.Found), webui.MsgTSTaken
	}
	info.Stale = tailscaleUses(report.Stale)
	// Turning on is offered while sharing is off and the port is free, and
	// while it is on but waits for exactly that: an unfinished turning on or
	// a renamed computer.
	again := slices.Contains(report.Waiting, TailscaleWaitUnfinished) || slices.Contains(report.Waiting, TailscaleWaitName)
	info.CanTurnOn = report.Installed && report.Problem == "" &&
		(!report.On && report.Endpoint == TailscaleEndpointFree || report.On && again)
	home, local := true, false
	info.HomeListen, info.LocalListen = planListen(report.Listen, &home), planListen(report.Listen, &local)
	return info
}

// changeTailscale handles ActionTailscaleOn and ActionTailscaleOff after the
// administrator password was verified.
func (app *App) changeTailscale(writer http.ResponseWriter, request *http.Request, settings state.Settings, csrf, action string) {
	if app.Tailscale == nil {
		app.renderTailscaleRefusal(writer, request, settings, csrf, action, &TailscaleError{Problem: string(tailscale.KindNotInstalled)})
		return
	}
	var err error
	notice := "tailscale_off"
	if action == webui.ActionTailscaleOn {
		homeNetwork := formChecked(postValue(request, "home_network"))
		_, err = app.Tailscale.On(request.Context(), &homeNetwork)
		notice = "tailscale_on"
	} else {
		_, err = app.Tailscale.Off(request.Context())
	}
	if err != nil {
		app.renderTailscaleRefusal(writer, request, settings, csrf, action, err)
		return
	}
	app.noticeRedirect(writer, request, "/settings?notice="+notice, http.StatusSeeOther)
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

// TailscaleUsesText describes what Tailscale has on its port in English,
// for the command line and errors.
func TailscaleUsesText(uses []tailscale.Use) string {
	texts := make([]string, len(uses))
	for i, use := range tailscaleUses(uses) {
		texts[i] = webui.TailscaleUseText(webui.LangEN, use)
	}
	return strings.Join(texts, "; ")
}
