package server

import (
	"bytes"
	"errors"
	"net/http"
	"slices"
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
// "": the block does not repeat the same message. occupied is what that
// refusal found on the port the owner named, which the block offers to
// replace like the ports it reads itself.
func (app *App) tailscaleBlock(request *http.Request, admin bool, refused string, occupied *TailscaleOccupied) webui.TailscaleInfo {
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
	if occupied != nil && !slices.ContainsFunc(info.Occupied, func(shown webui.TailscaleOccupied) bool { return shown.Port == strconv.Itoa(occupied.Port) }) {
		info.Occupied = append([]webui.TailscaleOccupied{tailscaleOccupied(info.Name, *occupied)}, info.Occupied...)
	}
	if refused != "" && info.Problem == webui.TailscaleProblemCode(refused) {
		info.Problem, info.ProblemDetail = "", ""
	}
	if !admin {
		info.ProblemDetail, info.Stale = "", nil
		info.Problem = webui.TailscaleProblemBrief(info.Problem)
		if info.FoundNote != "" {
			info.Found, info.FoundFix, info.FoundFixPort, info.FoundFixValue = nil, "", "", ""
			info.FoundNote = webui.TailscalePortNoteBrief(info.FoundNote)
		}
		info.Occupied = nil
	}
	return info
}

// tailscaleOccupied turns what another service has on a port into the
// page's form.
func tailscaleOccupied(name string, occupied TailscaleOccupied) webui.TailscaleOccupied {
	return webui.TailscaleOccupied{
		Port: strconv.Itoa(occupied.Port), Address: TailscaleOrigin(name, occupied.Port) + "/",
		Uses: tailscaleUses(occupied.Found), Replaceable: occupied.Replaceable, Digest: occupied.Digest,
	}
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
	// What is on each port is listed port by port, with its replacement,
	// instead of in one list.
	for _, occupied := range report.Occupied {
		info.Occupied = append(info.Occupied, tailscaleOccupied(report.Name, occupied))
		info.Found, info.FoundNote = nil, webui.MsgTSTakenBelow
	}
	// Automatic keeps the port sharing uses now, so a port it would not
	// choose first is shown as the custom port it is.
	info.PortMode = "auto"
	if report.On && !slices.Contains(tailscaleHTTPSPorts, report.Sharing.HTTPSPort) {
		info.PortMode, info.HTTPSPort = "custom", strconv.Itoa(report.Sharing.HTTPSPort)
	}
	// With every port Automatic tries taken, sharing can still be turned
	// on at a custom port.
	if !report.On && report.PortsTaken && report.Problem == "" {
		info.CanTurnOnCustom, info.PortMode = true, "custom"
	}
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
//
// The group's HTTPS port is Automatic (0: the ports tailscaleHTTPSPorts
// lists, or the port sharing uses now) or a custom port, which moves
// sharing that is on to it. A replace_digest field asks to replace what
// another service has on the custom port, exactly as the owner reviewed it.
func (app *App) changeTailscale(writer http.ResponseWriter, request *http.Request, settings state.Settings, csrf, action string) {
	homeNetwork := new(bool)
	*homeNetwork = formChecked(postValue(request, "home_network"))
	port := 0
	switch postValue(request, "tailscale_port") {
	case "", "auto":
	case "custom":
		number, err := strconv.Atoi(strings.TrimSpace(postValue(request, "tailscale_https_port")))
		if err != nil || number < 1 || number > 65535 {
			app.renderSettingsPage(writer, request, settings, csrf, webui.ActionSaveTailscale, []webui.Notice{webui.Error("tailscale_https_port", webui.MsgTSPortInvalid)}, http.StatusUnprocessableEntity, settingsView{AdminVerified: true})
			return
		}
		port = number
	default:
		app.renderSettings(writer, request, settings, csrf, webui.ActionSaveTailscale, []webui.Notice{webui.Error("action", webui.MsgSettingsUnknownAct)}, http.StatusBadRequest)
		return
	}
	replace := postValue(request, "replace_digest")
	if replace != "" && port == 0 {
		app.renderSettings(writer, request, settings, csrf, webui.ActionSaveTailscale, []webui.Notice{webui.Error("action", webui.MsgSettingsUnknownAct)}, http.StatusBadRequest)
		return
	}
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
			samePort := port == 0 || report.Sharing != nil && port == report.Sharing.HTTPSPort
			if on && report.On && !report.CanTurnOn && samePort && replace == "" || !on && !report.On {
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
		// A page opened at the address a move ends follows the result to
		// the new address.
		away := app.throughTailscale(request)
		var change TailscaleChange
		var err error
		if replace != "" {
			// The replacement form offers no home network choice, so the
			// listen address stays as saved.
			change, err = app.Tailscale.Replace(request.Context(), nil, port, replace)
		} else {
			change, err = app.Tailscale.On(request.Context(), homeNetwork, port)
		}
		if err != nil {
			app.renderTailscaleRefusal(writer, request, settings, csrf, action, err)
			return
		}
		// Only a new address waits for a certificate on its first visit.
		notice := "tailscale_on"
		switch {
		case change.MovedFrom != "":
			notice = "tailscale_moved"
		case change.Endpoint != endpointCreated:
			notice = "tailscale_on_kept"
		}
		target := settingsResultURL(action, notice)
		if away && change.MovedFrom != "" {
			target = TailscaleOrigin(change.Record.Name, change.Record.HTTPSPort) + target
		}
		app.settingsSaved(writer, request, target)
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
	if errors.Is(err, ErrTailscaleMoveStopped) {
		notices = append(notices, webui.Error("tailscale", webui.MsgTSMoveStopped))
	}
	view := settingsView{AdminVerified: true}
	var refusal *TailscaleError
	switch {
	case errors.As(err, &refusal):
		view.TailscaleOccupied = refusal.Occupied
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
