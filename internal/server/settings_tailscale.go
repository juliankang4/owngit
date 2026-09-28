package server

import (
	"bytes"
	"errors"
	"net/http"
	"strconv"
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
// page. What Tailscale reports, its failures included, is in the report; a
// reading that did not finish before the page's deadline is Tailscale not
// answering in time (Tailscale.read). Any other error is a failed read of
// OwnGit's own state, which is logged and shown as such, not as a Tailscale
// problem.
//
// Only an administrator sees what Tailscale reports beyond OwnGit's own
// endpoint: what else is on the port, addresses under earlier names and what
// Tailscale printed, since they can name other services on this computer.
// Other viewers see that the port is taken.
//
// refused is the problem of a refusal the page shows above the block, or
// "": the block does not repeat the same message.
func (app *App) tailscaleBlock(request *http.Request, admin bool, refused string) webui.TailscaleInfo {
	report, err := app.Tailscale.Report(request.Context())
	var info webui.TailscaleInfo
	switch {
	case errors.Is(err, errReadingUnfinished):
		info.Problem = webui.TailscaleProblemCode(string(tailscale.KindTimeout))
	case err != nil:
		logFailure(request, "sharing state read", err)
		info.Problem = webui.MsgTSStateUnreadable
		if errors.Is(err, errReadingsStopped) {
			// The server is stopping and reads Tailscale no more; nothing
			// failed, which logFailure knows too.
			info.Problem = webui.MsgErrUnavailable
		}
	default:
		info = tailscaleInfo(report)
	}
	if refused != "" && info.Problem == webui.TailscaleProblemCode(refused) {
		info.Problem, info.ProblemDetail = "", ""
	}
	if !admin {
		info.ProblemDetail, info.Stale = "", nil
		info.Problem = webui.TailscaleProblemBrief(info.Problem)
		if info.Found != nil {
			info.Found, info.FoundFix, info.FoundFixPort, info.FoundFixValue = nil, "", "", ""
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
		BaseURLOption: report.BaseURLOption,
	}
	// Before sharing is on, a port other than 443 is named with the reason.
	if !report.On && report.TurnOnPort != 0 && len(report.TurnOnPassed) > 0 {
		info.TurnOnPort, info.PassedPorts = strconv.Itoa(report.TurnOnPort), PortList(report.TurnOnPassed)
		info.PortNote = webui.TailscalePortNote(len(report.TurnOnPassed))
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
		info.FoundFixPort = strconv.Itoa(report.Sharing.HTTPSPort)
	case report.PortsTaken:
		info.Found, info.FoundNote, info.FoundFix = tailscaleUses(report.Found), webui.MsgTSTaken, webui.MsgTSTakenSteps
	case !report.On && report.Endpoint == TailscaleEndpointUnrecorded:
		info.Found, info.FoundNote = tailscaleUses(report.Found), webui.MsgTSUnrecorded
		info.FoundFix, _ = TailscaleFix(report.Found, false, "")
		info.FoundFixPort = strconv.Itoa(report.TurnOnPort)
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

// changeTailscale handles the Tailscale group, and the ActionTailscaleOn and
// ActionTailscaleOff forms of 1.1.2, after the administrator password was
// verified.
func (app *App) changeTailscale(writer http.ResponseWriter, request *http.Request, settings state.Settings, csrf, action string) {
	homeNetwork := new(bool)
	*homeNetwork = formChecked(postValue(request, "home_network"))
	// The switch of the Tailscale group sends what sharing should be.
	if action == webui.ActionSaveTailscale {
		var on bool
		switch postValue(request, "tailscale") {
		case "on":
			action, on = webui.ActionTailscaleOn, true
		case "off", "":
			action = webui.ActionTailscaleOff
		default:
			app.renderSettings(writer, request, settings, csrf, webui.ActionSaveTailscale, []webui.Notice{webui.Error("action", webui.MsgSettingsUnknownAct)}, http.StatusBadRequest)
			return
		}
		// Asking for what sharing already is changes nothing. Sharing that
		// is on but unfinished is turned on again. A reading that failed
		// leaves the decision to turning on or off, which reports why.
		// The group then shows what is saved, not what was sent. A home
		// network choice sent while sharing stays off is used only when
		// sharing is turned on, which the answer says.
		if report, err := app.Tailscale.Report(request.Context()); err == nil {
			if on && report.On && !report.CanTurnOn || !on && !report.On {
				notice := webui.MsgSettingsNothing
				if !on && report.ListenOption == "" && *homeNetwork != report.HomeNetwork {
					notice = webui.MsgSettingsTSHomeOff
				}
				app.renderSettingsPage(writer, request, settings, csrf, webui.ActionSaveTailscale, []webui.Notice{webui.Info(notice)}, http.StatusOK, settingsView{Unchanged: true})
				return
			}
		}
		// The group offers the home network choice only while sharing is
		// off. Turning on again keeps what was chosen then (see planListen).
		_, wasOn, err := app.Store.TailscaleServe(request.Context())
		if err != nil {
			app.renderNotSaved(writer, request, settings, csrf, webui.ActionSaveTailscale, "sharing state read", err)
			return
		}
		if wasOn {
			homeNetwork = nil
		}
	}
	if action == webui.ActionTailscaleOn {
		change, err := app.Tailscale.On(request.Context(), homeNetwork, 0)
		if err != nil {
			app.renderTailscaleRefusal(writer, request, settings, csrf, action, err)
			return
		}
		// Only a new address waits for a certificate on its first visit.
		notice := "tailscale_on"
		if change.Endpoint != endpointCreated {
			notice = "tailscale_on_kept"
		}
		app.settingsSaved(writer, request, settingsResultURL(action, notice))
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
	app.settingsSaved(writer, request, settingsResultURL(action, "tailscale_off"))
}

// renderTailscaleOff answers with the page that needs nothing more from the
// address that sharing no longer serves.
func (app *App) renderTailscaleOff(writer http.ResponseWriter, request *http.Request, local string) {
	var body bytes.Buffer
	page := webui.TailscaleOffPage{Lang: app.language(writer, request), Appearance: app.appearance(writer, request), Local: local}
	if err := app.Renderer.RenderTailscaleOff(&body, page); err != nil {
		app.writePlainError(writer, internalError(request, "page render", err))
		return
	}
	writer.Header().Set("Content-Type", "text/html; charset=utf-8")
	writer.Header().Set("Cache-Control", "no-store")
	writer.WriteHeader(http.StatusOK)
	_, _ = writer.Write(body.Bytes())
}

// renderTailscaleRefusal shows, inside the form that was sent, why turning
// sharing on or off did not finish. When Tailscale may already have the
// change (ErrTailscaleAhead), the page says so first, whatever the cause. A
// TailscaleError then explains itself; any other failure left the settings
// unsaved, which the first notice or MsgSettingsNotSaved says. Only a
// refusal the owner can fix, with Tailscale not ahead, is answered 409;
// every other failure is unavailable and logged.
func (app *App) renderTailscaleRefusal(writer http.ResponseWriter, request *http.Request, settings state.Settings, csrf, action string, err error) {
	ahead := errors.Is(err, ErrTailscaleAhead)
	var notices []webui.Notice
	if ahead {
		notices = append(notices, webui.Error("tailscale", webui.MsgTSNotSavedAhead))
	}
	view := settingsView{AdminVerified: true}
	var refusal *TailscaleError
	switch {
	case errors.As(err, &refusal):
		// What is on the port is listed in the block, in the page's language.
		notice := webui.Notice{Kind: webui.NoticeError, Code: webui.TailscaleRefusalCode(refusal.Problem, len(refusal.Found) > 0), Field: "tailscale", Detail: refusal.Detail}
		if len(refusal.Found) > 0 {
			notice.Detail = ""
		}
		notices = append(notices, notice)
		if refusal.Problem == TailscaleProblemReadBack && refusal.MacApp {
			notices = append(notices, webui.Notice{Kind: webui.NoticeInfo, Code: webui.MsgTSReadBackMacApp, Field: "tailscale"})
		}
		view.TailscaleRefused = refusal.Problem
	case !ahead:
		notices = append(notices, webui.Error("tailscale", webui.MsgSettingsNotSaved))
	}
	status := http.StatusConflict
	if ahead || !refused(err) {
		status = unavailable(request, "Tailscale sharing change", err)
	}
	app.renderSettingsPage(writer, request, settings, csrf, action, notices, status, view)
}

// PortList writes ports for a message, such as "443, 8443".
func PortList(ports []int) string {
	texts := make([]string, len(ports))
	for i, port := range ports {
		texts[i] = strconv.Itoa(port)
	}
	return strings.Join(texts, ", ")
}

// tailscaleUses turns what Tailscale has on its port into the page's form.
func tailscaleUses(uses []tailscale.Use) []webui.TailscaleUse {
	converted := make([]webui.TailscaleUse, len(uses))
	for i, use := range uses {
		converted[i] = webui.TailscaleUse{Kind: use.Kind, Address: use.Address, Target: use.Target}
	}
	return converted
}

// TailscaleFix is the step that clears what is on one HTTPS port: undoing
// a changed endpoint while sharing is on (changed), or removing a use so
// that sharing can be turned on. Its message takes the port as the first
// value and the returned value, when not empty, as the second.
// "tailscale serve --https=PORT off" removes only web handlers: Tailscale
// refuses it while the port forwards TCP, it does not remove plain HTTP,
// and a "tailscale serve" running in a terminal ends only there. Other uses
// get the step that fits them.
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
