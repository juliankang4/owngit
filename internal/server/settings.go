package server

import (
	"errors"
	"net/http"

	"owngit/internal/auth"
	"owngit/internal/state"
	"owngit/internal/webui"
)

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
	if !app.validCSRF(request, postValue(request, "csrf")) {
		app.renderError(writer, request, http.StatusForbidden, webui.MsgErrCSRF, "")
		return
	}
	action := postValue(request, "action")
	adminPassword := postValue(request, "admin_password")
	if err := app.Auth.VerifyCredential(request.Context(), "admin", adminPassword, request.RemoteAddr); err != nil {
		code := webui.MsgAdminFailed
		if errors.Is(err, auth.ErrRateLimited) {
			code = webui.MsgAdminLocked
		}
		app.renderSettings(writer, request, settings, csrf, action, []webui.Notice{webui.Error("admin_password", code)}, http.StatusUnauthorized)
		return
	}

	var err error
	switch action {
	case webui.ActionEnableAccessPassword, webui.ActionChangeAccessPassword:
		password := postValue(request, "access_password")
		if validateErr := auth.ValidatePassword(password); validateErr != nil {
			app.renderSettings(writer, request, settings, csrf, action, []webui.Notice{webui.Error("access_password", webui.MsgSetupAccessPassShort)}, http.StatusUnprocessableEntity)
			return
		}
		if adminPassword == password {
			app.renderSettings(writer, request, settings, csrf, action, []webui.Notice{webui.Error("access_password", webui.MsgSetupAdminSameAsGen)}, http.StatusUnprocessableEntity)
			return
		}
		var encoded string
		encoded, err = auth.HashPassword(password)
		if err == nil {
			err = app.Store.SetAccessPassword(request.Context(), encoded)
		}
		app.clearCookie(writer, request, generalCookie, true)
	case webui.ActionDisableAccessPassword:
		err = app.Store.DisableAccessPassword(request.Context())
		app.clearCookie(writer, request, generalCookie, true)
	case webui.ActionChangeAdminPassword:
		newPassword := postValue(request, "new_admin_password")
		if validateErr := auth.ValidatePassword(newPassword); validateErr != nil {
			app.renderSettings(writer, request, settings, csrf, action, []webui.Notice{webui.Error("new_admin_password", webui.MsgSetupAdminShort)}, http.StatusUnprocessableEntity)
			return
		}
		accessHash, hashErr := app.Store.PasswordHash(request.Context(), "access")
		if adminPassword == newPassword || hashErr != nil || (accessHash != "" && auth.CheckPassword(accessHash, newPassword)) {
			app.renderSettings(writer, request, settings, csrf, action, []webui.Notice{webui.Error("new_admin_password", webui.MsgSetupAdminSameAsGen)}, http.StatusUnprocessableEntity)
			return
		}
		var encoded string
		encoded, err = auth.HashPassword(newPassword)
		if err == nil {
			err = app.Store.SetAdminPassword(request.Context(), encoded)
		}
		app.clearCookie(writer, request, adminCookie, true)
	case webui.ActionAcknowledgeInsecure:
		if !formChecked(postValue(request, "insecure_ack")) {
			app.renderSettings(writer, request, settings, csrf, action, []webui.Notice{webui.Error("insecure_ack", webui.MsgSetupInsecureNeed)}, http.StatusUnprocessableEntity)
			return
		}
		err = app.Store.AcknowledgeInsecureHTTP(request.Context())
	default:
		app.renderSettings(writer, request, settings, csrf, action, []webui.Notice{webui.Error("action", webui.MsgSettingsUnknownAct)}, http.StatusBadRequest)
		return
	}
	if err != nil {
		app.renderSettings(writer, request, settings, csrf, action, []webui.Notice{webui.Error("", webui.MsgErrUnavailable)}, http.StatusServiceUnavailable)
		return
	}
	http.Redirect(writer, request, "/settings?notice=settings_saved", http.StatusSeeOther)
}

func (app *App) allowSettingsViewer(writer http.ResponseWriter, request *http.Request, settings state.Settings) (string, bool) {
	if settings.AccessMode == "open" {
		session, ok := app.requireGeneral(writer, request, settings)
		return session.CSRF, ok
	}
	if session, ok := app.cookieSession(request, "general", generalCookie); ok {
		return session.CSRF, true
	}
	if session, ok := app.cookieSession(request, "admin", adminCookie); ok {
		return session.CSRF, true
	}
	http.Redirect(writer, request, "/login?next=%2Fsettings", http.StatusSeeOther)
	return "", false
}

func (app *App) renderSettings(writer http.ResponseWriter, request *http.Request, settings state.Settings, csrf, pending string, notices []webui.Notice, status int) {
	chrome, err := app.chrome(writer, request, webui.SectionSettings, "", csrf)
	if err != nil {
		app.writePlainError(writer, http.StatusServiceUnavailable)
		return
	}
	chrome.Notices = notices
	storage := webui.StorageInfo{}
	if chrome.Viewer.AdminConfirmed {
		storage = webui.StorageInfo{Visible: true, Path: settings.RepositoryRoot}
	}
	mode := webui.AccessOpen
	if settings.AccessMode == "password" {
		mode = webui.AccessPassword
	}
	app.render(writer, status, webui.SettingsPage{
		Chrome: chrome, SubmitURL: "/settings", AccessMode: mode, AdminRequired: true,
		PendingAction: pending, Storage: storage, CloneHint: app.baseURL(request) + "/git/",
	})
}

func (app *App) baseURL(request *http.Request) string {
	scheme := "http"
	if request.TLS != nil {
		scheme = "https"
	}
	return scheme + "://" + request.Host
}
