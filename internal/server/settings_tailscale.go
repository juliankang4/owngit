package server

import (
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
func (app *App) tailscaleBlock(ctx context.Context) webui.TailscaleInfo {
	if app.Tailscale == nil {
		return webui.TailscaleInfo{Problem: webui.TailscaleProblemCode(string(tailscale.KindNotInstalled))}
	}
	report, err := app.Tailscale.Report(ctx)
	if err != nil {
		return webui.TailscaleInfo{Problem: webui.MsgTSProblemFailed}
	}
	return tailscaleInfo(report)
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
	info.CanTurnOn = !report.On && report.Installed && report.Problem == "" && report.Endpoint == TailscaleEndpointFree
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
		app.renderSettings(writer, request, settings, csrf, action, []webui.Notice{webui.Error("tailscale", webui.MsgErrUnavailable)}, http.StatusServiceUnavailable)
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
	app.renderSettings(writer, request, settings, csrf, action, notices, http.StatusConflict)
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
