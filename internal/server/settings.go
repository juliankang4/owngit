package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/url"

	"owngit/internal/auth"
	"owngit/internal/requestctx"
	"owngit/internal/state"
	"owngit/internal/webui"
)

// Settings is split into tabs, each its own address (webui.SettingsTabs),
// and each tab into groups: one form per group, with its own Save. A group
// posts only its own fields, to its tab's address, so saving it can neither
// save nor reset what another group holds.
//
// The page's script can save a group without leaving the page, for a form
// without a password field (groupSave in owngit.js); a form that asks for
// the administrator password, as every group does today, is always posted
// by the browser. The script sends the same form with the
// settingsGroupHeader header, which selects only the form of the answer;
// the change and its checks are the same:
//
//   - saved: 200 with JSON {"location": URL}, the address a browser
//     without the script is redirected to. The result notice is kept for
//     that address as for the redirect. When it is this tab, the script
//     reads it and puts the group's new state, with its saved notice, in
//     place; otherwise it goes there.
//   - refused: the tab itself, as without the script, with the refused
//     group showing its notices and the non-secret values it sent. The
//     script takes that group from it.
//
// Anything else, such as a sign-in page after the session ended, is not a
// saved change.
const settingsGroupHeader = "X-OwnGit-Group"

// isSettingsPath reports whether path is the address of a Settings tab.
func isSettingsPath(path string) bool {
	_, ok := webui.SettingsTabOfPath(path)
	return ok
}

func (app *App) handleSettingsGet(writer http.ResponseWriter, request *http.Request, settings state.Settings) {
	csrf, ok := app.allowSettingsViewer(writer, request, settings)
	if !ok {
		return
	}
	app.renderSettings(writer, request, settings, csrf, "", nil, http.StatusOK)
}

func (app *App) handleSettingsPost(writer http.ResponseWriter, request *http.Request, settings state.Settings) {
	csrf, ok := app.allowSettingsViewer(writer, request, settings)
	if !ok {
		return
	}
	if !parseForm(writer, request) {
		return
	}
	if !app.requireCSRF(writer, request) {
		return
	}
	action := postValue(request, "action")
	adminPassword := postValue(request, "admin_password")
	if err := app.Auth.VerifyCredential(request.Context(), "admin", adminPassword, requestctx.Of(request).ClientAddress); err != nil {
		notice, status := adminPasswordNotice(request, err, "admin_password")
		app.renderSettings(writer, request, settings, csrf, action, []webui.Notice{notice}, status)
		return
	}
	if action == webui.ActionSaveAccess {
		var changes bool
		action, changes = accessAction(request, settings)
		if action == "" {
			app.renderSettings(writer, request, settings, csrf, webui.ActionSaveAccess, []webui.Notice{webui.Error("action", webui.MsgSettingsUnknownAct)}, http.StatusBadRequest)
			return
		}
		if !changes {
			app.renderSettings(writer, request, settings, csrf, action, []webui.Notice{webui.Info(webui.MsgSettingsNothing)}, http.StatusOK)
			return
		}
	}

	var err error
	// ends is the session cookie a saved change ends in this browser. It is
	// cleared only once the change is saved; a change that was not saved
	// changes nothing, this browser's session included.
	var ends string
	// notice is the result the page shows in the saved group.
	notice := "settings_saved"
	switch action {
	case webui.ActionEnableAccessPassword, webui.ActionChangeAccessPassword:
		password := postValue(request, "access_password")
		if validateErr := auth.ValidatePassword(password); validateErr != nil {
			app.renderSettings(writer, request, settings, csrf, action, []webui.Notice{webui.Error("access_password", passwordRuleMessage(validateErr, webui.MsgSetupAccessPassShort))}, http.StatusUnprocessableEntity)
			return
		}
		if adminPassword == password {
			app.renderSettings(writer, request, settings, csrf, action, []webui.Notice{webui.Error("access_password", webui.MsgSetupGenSameAsAdmin)}, http.StatusUnprocessableEntity)
			return
		}
		var encoded string
		encoded, err = auth.HashPassword(password)
		if err == nil {
			err = app.Store.SetAccessPassword(request.Context(), encoded)
		}
		ends, notice = generalCookie, "access_changed"
		if action == webui.ActionEnableAccessPassword {
			notice = "access_enabled"
		}
	case webui.ActionDisableAccessPassword:
		err = app.Store.DisableAccessPassword(request.Context())
		ends, notice = generalCookie, "access_disabled"
	case webui.ActionChangeAdminPassword:
		newPassword := postValue(request, "new_admin_password")
		if validateErr := auth.ValidatePassword(newPassword); validateErr != nil {
			app.renderSettings(writer, request, settings, csrf, action, []webui.Notice{webui.Error("new_admin_password", passwordRuleMessage(validateErr, webui.MsgSetupAdminShort))}, http.StatusUnprocessableEntity)
			return
		}
		if adminPassword == newPassword {
			app.renderSettings(writer, request, settings, csrf, action, []webui.Notice{webui.Error("new_admin_password", webui.MsgSettingsAdminSame)}, http.StatusUnprocessableEntity)
			return
		}
		accessHash, hashErr := app.Store.PasswordHash(request.Context(), "access")
		if hashErr != nil {
			app.renderNotSaved(writer, request, settings, csrf, action, "shared password read", hashErr)
			return
		}
		if accessHash != "" && auth.CheckPassword(accessHash, newPassword) {
			app.renderSettings(writer, request, settings, csrf, action, []webui.Notice{webui.Error("new_admin_password", webui.MsgSetupAdminSameAsGen)}, http.StatusUnprocessableEntity)
			return
		}
		var encoded string
		encoded, err = auth.HashPassword(newPassword)
		if err == nil {
			err = app.Store.SetAdminPassword(request.Context(), encoded)
		}
		ends, notice = adminCookie, "admin_password_changed"
	case webui.ActionAcknowledgeInsecure:
		if !formChecked(postValue(request, "insecure_ack")) {
			app.renderSettings(writer, request, settings, csrf, action, []webui.Notice{webui.Error("insecure_ack", webui.MsgSetupInsecureNeed)}, http.StatusUnprocessableEntity)
			return
		}
		err = app.Store.AcknowledgeInsecureHTTP(request.Context())
		notice = "insecure_acknowledged"
	case webui.ActionSetUpdateCheck:
		// An unticked switch sends nothing, which means off.
		value := postValue(request, "update_check")
		if value != "on" && value != "off" && value != "" {
			app.renderSettings(writer, request, settings, csrf, action, []webui.Notice{webui.Error("action", webui.MsgSettingsUnknownAct)}, http.StatusBadRequest)
			return
		}
		err = app.Store.SetUpdateCheck(request.Context(), value == "on")
		// Turning the check on asks for an answer soon instead of waiting
		// for the daily interval. Turning it off takes effect at once,
		// because the dashboard and the checker both read the saved value.
		if err == nil && value == "on" && app.Releases != nil {
			app.Releases.Wake()
		}
	case webui.ActionSaveNetwork:
		app.saveNetwork(writer, request, settings, csrf)
		return
	case webui.ActionSaveTailscale, webui.ActionTailscaleOn, webui.ActionTailscaleOff:
		app.changeTailscale(writer, request, settings, csrf, action)
		return
	default:
		app.renderSettings(writer, request, settings, csrf, action, []webui.Notice{webui.Error("action", webui.MsgSettingsUnknownAct)}, http.StatusBadRequest)
		return
	}
	if err != nil {
		app.renderNotSaved(writer, request, settings, csrf, action, "settings save", err)
		return
	}
	if ends != "" {
		app.clearCookie(writer, request, ends, true)
	}
	// A new shared password signs out every general session, this browser's
	// too. Without an administrator session Settings would send it to the
	// sign-in page and drop the confirmation, so the confirmation goes there.
	if action == webui.ActionEnableAccessPassword || action == webui.ActionChangeAccessPassword {
		// A session that could not be read may have ended, so the
		// confirmation goes where it is shown either way.
		_, admin, err := app.cookieSession(request, "admin", adminCookie)
		if err != nil {
			logFailure(request, "session read", err)
		}
		if !admin {
			app.settingsSaved(writer, request, "/login?notice=access_password_saved&next="+url.QueryEscape(webui.SettingsTabURL(webui.SettingsAccess)))
			return
		}
	}
	app.settingsSaved(writer, request, settingsResultURL(action, notice))
}

// accessAction turns a save of the Access group into the change it asks
// for: turning the shared password on, changing it or turning it off.
// changes is false when the form asks for what is already saved. The action
// is "" for a mode that does not exist.
func accessAction(request *http.Request, settings state.Settings) (action string, changes bool) {
	passwordNow := settings.AccessMode == "password"
	switch postValue(request, "access_mode") {
	case "open":
		return webui.ActionDisableAccessPassword, passwordNow
	case "password":
		if !passwordNow {
			return webui.ActionEnableAccessPassword, true
		}
		return webui.ActionChangeAccessPassword, postValue(request, "access_password") != ""
	}
	return "", false
}

// settingsResultURL is where a saved change of action shows notice: its
// group on its tab.
func settingsResultURL(action, notice string) string {
	group := webui.SettingsActionGroup(action)
	return webui.SettingsTabURL(webui.SettingsGroupTab(group)) + "?notice=" + url.QueryEscape(notice) + "#grp-" + group
}

// settingsNoticeGroups names the group that shows each result notice of a
// saved Settings change.
var settingsNoticeGroups = map[string]string{
	"settings_saved":         webui.GroupUpdate,
	"access_enabled":         webui.GroupAccess,
	"access_changed":         webui.GroupAccess,
	"access_disabled":        webui.GroupAccess,
	"admin_password_changed": webui.GroupAdmin,
	"insecure_acknowledged":  webui.GroupConnection,
	"network_saved":          webui.GroupNetwork,
	"tailscale_on":           webui.GroupTailscale,
	"tailscale_on_kept":      webui.GroupTailscale,
	"tailscale_off":          webui.GroupTailscale,
}

// settingsSaved ends a saved Settings change: a redirect to target, or for
// the page's script the same target as JSON (see settingsGroupHeader).
func (app *App) settingsSaved(writer http.ResponseWriter, request *http.Request, target string) {
	if request.Header.Get(settingsGroupHeader) == "" {
		app.noticeRedirect(writer, request, target, http.StatusSeeOther)
		return
	}
	if parsed, err := url.Parse(target); err == nil {
		if notice := parsed.Query().Get("notice"); notice != "" {
			app.setCookie(writer, request, noticeCookie, notice, app.now().Add(noticeCookieMaxAge), true)
		}
	}
	body, err := json.Marshal(struct {
		Location string `json:"location"`
	}{target})
	if err != nil {
		app.writePlainError(writer, internalError(request, "settings answer", err))
		return
	}
	writer.Header().Set("Content-Type", "application/json")
	writer.Header().Set("Cache-Control", "no-store")
	writer.WriteHeader(http.StatusOK)
	_, _ = writer.Write(body)
}

// passwordRuleMessage names the password rule a refused password broke.
func passwordRuleMessage(err error, tooShort webui.MessageCode) webui.MessageCode {
	if errors.Is(err, auth.ErrPasswordTooLong) {
		return webui.MsgPasswordTooLong
	}
	return tooShort
}

func (app *App) allowSettingsViewer(writer http.ResponseWriter, request *http.Request, settings state.Settings) (string, bool) {
	if settings.AccessMode == "open" {
		session, ok := app.requireGeneral(writer, request, settings)
		return session.CSRF, ok
	}
	// Either session shows Settings, so one that was read decides even when
	// the other could not be read.
	general, generalOK, generalErr := app.cookieSession(request, "general", generalCookie)
	if generalOK {
		return general.CSRF, true
	}
	admin, adminOK, adminErr := app.cookieSession(request, "admin", adminCookie)
	switch {
	case adminOK:
		return admin.CSRF, true
	case generalErr != nil || adminErr != nil:
		app.answerUnavailable(writer, request, "session read", errors.Join(generalErr, adminErr))
	default:
		http.Redirect(writer, request, "/login?next="+url.QueryEscape(loginNext(request)), http.StatusSeeOther)
	}
	return "", false
}

// renderNotSaved answers a settings change that failed before anything was
// changed: the form says the change was not saved, and step and its cause
// are logged once.
func (app *App) renderNotSaved(writer http.ResponseWriter, request *http.Request, settings state.Settings, csrf, action, step string, err error) {
	app.renderSettings(writer, request, settings, csrf, action, []webui.Notice{webui.Error("", webui.MsgSettingsNotSaved)}, unavailable(request, step, err))
}

func (app *App) renderSettings(writer http.ResponseWriter, request *http.Request, settings state.Settings, csrf, action string, notices []webui.Notice, status int) {
	app.renderSettingsPage(writer, request, settings, csrf, action, notices, status, settingsView{})
}

// settingsView is what a refused form brings to the Settings page that
// shows it again.
type settingsView struct {
	// Network is what a refused Network save submitted. Without it the
	// form shows what the request sent, unless NetworkStale: the form was
	// opened before another change, so it shows the values saved now.
	Network      *webui.NetworkForm
	NetworkStale bool
	// AdminVerified is true when this request verified the administrator
	// password, so the page may show what only an administrator sees.
	AdminVerified bool
	// TailscaleRefused is the problem of a refused Tailscale change shown
	// on the page, which the Tailscale block then does not repeat.
	TailscaleRefused string
}

// settingsDraftFields are the non-secret fields a refused form shows again.
// A switch or checkbox is written "on" or "off", since an unticked one
// sends nothing.
var settingsDraftFields = map[string]bool{
	"access_mode": false, "update_check": true, "tailscale": true, "home_network": true, "insecure_ack": true,
}

// settingsDraft collects what a refused form sent, for settingsDraftFields.
func settingsDraft(request *http.Request) map[string]string {
	draft := map[string]string{}
	for field, checkbox := range settingsDraftFields {
		value := postValue(request, field)
		switch {
		case checkbox:
			draft[field] = "off"
			if formChecked(value) {
				draft[field] = "on"
			}
		case value != "":
			draft[field] = value
		}
	}
	return draft
}

// renderSettingsPage renders a Settings tab. action is the form that was
// sent, or "": its group shows notices and the values it sent, on its tab.
// Otherwise the tab is the one the address names, and a result notice in
// the address is shown in the group it belongs to.
func (app *App) renderSettingsPage(writer http.ResponseWriter, request *http.Request, settings state.Settings, csrf, action string, notices []webui.Notice, status int, view settingsView) {
	chrome, err := app.chrome(writer, request, webui.SectionSettings, "", csrf)
	if err != nil {
		app.answerUnavailable(writer, request, "page frame read", err)
		return
	}
	tab, _ := webui.SettingsTabOfPath(request.URL.Path)
	page := webui.SettingsPage{Chrome: chrome, AccessMode: webui.AccessOpen, AdminRequired: true}
	if settings.AccessMode == "password" {
		page.AccessMode = webui.AccessPassword
	}
	switch group := webui.SettingsActionGroup(action); {
	case group != "":
		tab, page.Group, page.Notices, page.Chrome.Notices = webui.SettingsGroupTab(group), group, notices, nil
		if request.Method == http.MethodPost {
			page.Draft = settingsDraft(request)
		}
	case notices != nil:
		// A form this page does not know: its answer stands above the tab.
		page.Chrome.Notices = notices
	default:
		// The result of a saved change, kept by its redirect, is shown in
		// its group when that group is on this tab.
		group := settingsNoticeGroups[request.URL.Query().Get("notice")]
		if len(chrome.Notices) > 0 && group != "" && webui.SettingsGroupTab(group) == tab {
			page.Group, page.Notices, page.Chrome.Notices = group, chrome.Notices, nil
		}
	}
	page.Tab, page.SubmitURL = tab, webui.SettingsTabURL(tab)

	report, err := app.networkReport(request.Context())
	if err != nil {
		app.answerUnavailable(writer, request, "network settings read", err)
		return
	}
	page.Network = networkInfo(report)
	switch {
	case view.Network != nil:
		page.Network.Form = *view.Network
	case page.Group == webui.GroupNetwork && page.Draft != nil && !view.NetworkStale:
		page.Network.Form = networkForm(request)
	}
	if chrome.Viewer.AdminConfirmed {
		page.Storage = webui.StorageInfo{Visible: true, Path: settings.RepositoryRoot}
		page.CloneHint = app.serverOrigin(request) + "/git/"
	}
	page.UpdateCheck = webui.UpdateCheckInfo{Enabled: settings.UpdateCheck, ForcedOff: app.Releases == nil}
	// Only the Network tab shows sharing on the tailnet, and reading it
	// asks Tailscale, so the other tabs do not.
	if tab == webui.SettingsNetwork {
		page.Tailscale = app.tailscaleBlock(request, chrome.Viewer.AdminConfirmed || view.AdminVerified, view.TailscaleRefused)
	}
	app.render(writer, request, status, page)
}
