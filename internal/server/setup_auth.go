package server

import (
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"owngit/internal/auth"
	"owngit/internal/bootstrap"
	"owngit/internal/state"
	"owngit/internal/webui"
)

func (app *App) handleSetupGet(writer http.ResponseWriter, request *http.Request, settings state.Settings) {
	stage := webui.SetupWelcome
	csrf := ""
	if settings.Initialized {
		stage = webui.SetupUnavailable
	} else if session, ok := app.setupSession(request); ok {
		stage = webui.SetupWizard
		csrf = session.CSRF
	} else {
		csrf = app.preauthCSRF(writer, request)
	}
	chrome, err := app.chrome(writer, request, webui.SectionSetup, "", csrf)
	if err != nil {
		app.writePlainError(writer, http.StatusServiceUnavailable)
		return
	}
	page := webui.SetupPage{
		Chrome: chrome, Stage: stage, RedeemURL: "/setup/redeem", SubmitURL: "/setup",
		Prerequisites: []webui.Prerequisite{
			{Name: "git", Satisfied: app.GitVersion != "", Code: chooseMessage(app.GitVersion != "", webui.MsgPrereqGitFound, webui.MsgPrereqGitMissing), Detail: app.GitVersion},
			{Name: "git-http-backend", Satisfied: app.HTTPBackendFound, Code: chooseMessage(app.HTTPBackendFound, webui.MsgPrereqHTTPFound, webui.MsgPrereqHTTPMiss)},
		},
		Form: webui.SetupForm{SuggestedPath: app.SuggestedRepositoryRoot, AccessMode: webui.AccessOpen},
	}
	if settings.Initialized {
		page.Reason = webui.MsgSetupAlreadyDone
		page.RecoveryHint = webui.MsgSetupReissueHint
	}
	app.render(writer, http.StatusOK, page)
}

func (app *App) handleSetupRedeem(writer http.ResponseWriter, request *http.Request) {
	if !parseForm(writer, request) {
		return
	}
	if !app.validPreauthCSRF(request, postValue(request, "csrf")) {
		app.renderError(writer, request, http.StatusForbidden, webui.MsgErrCSRF, "")
		return
	}
	token := postValue(request, "token")
	if token == "" || len(token) > 256 {
		app.renderError(writer, request, http.StatusForbidden, webui.MsgSetupLinkInvalid, "")
		return
	}
	sessionToken, err := auth.RandomToken(32)
	if err != nil {
		app.writePlainError(writer, http.StatusInternalServerError)
		return
	}
	csrf, err := auth.RandomToken(32)
	if err != nil {
		app.writePlainError(writer, http.StatusInternalServerError)
		return
	}
	expires := app.now().Add(20 * time.Minute)
	redeemed, err := app.Store.RedeemBootstrap(request.Context(), token, sessionToken, csrf, app.now(), expires)
	if err != nil {
		app.writePlainError(writer, http.StatusServiceUnavailable)
		return
	}
	if !redeemed {
		app.renderError(writer, request, http.StatusForbidden, webui.MsgSetupLinkInvalid, "")
		return
	}
	app.setCookie(writer, request, setupCookie, sessionToken, expires, true)
	app.clearCookie(writer, request, preauthCookie, true)
	http.Redirect(writer, request, "/setup", http.StatusSeeOther)
}

func (app *App) handleSetupPost(writer http.ResponseWriter, request *http.Request) {
	if !parseForm(writer, request) {
		return
	}
	session, ok := app.setupSession(request)
	if !ok {
		app.renderError(writer, request, http.StatusForbidden, webui.MsgSetupSessionEnded, "")
		return
	}
	if !constantEqual(session.CSRF, postValue(request, "csrf")) {
		app.renderError(writer, request, http.StatusForbidden, webui.MsgErrCSRF, "")
		return
	}
	storagePath := strings.TrimSpace(postValue(request, "storage_path"))
	accessMode := postValue(request, "access_mode")
	accessPassword := postValue(request, "access_password")
	adminPassword := postValue(request, "admin_password")
	insecureAccepted := formChecked(postValue(request, "insecure_ack"))
	form := webui.SetupForm{
		StoragePath: storagePath, SuggestedPath: app.SuggestedRepositoryRoot,
		AccessMode: webui.AccessMode(accessMode), InsecureAck: insecureAccepted,
	}
	var notices []webui.Notice
	if storagePath == "" {
		notices = append(notices, webui.Error("storage_path", webui.MsgSetupStorageMissing))
	}
	if accessMode != "open" && accessMode != "password" {
		notices = append(notices, webui.Error("access_mode", webui.MsgErrBadRequest))
	}
	if accessMode == "password" {
		if accessPassword == "" {
			notices = append(notices, webui.Error("access_password", webui.MsgSetupAccessPassEmpty))
		} else if err := auth.ValidatePassword(accessPassword); err != nil {
			notices = append(notices, webui.Error("access_password", webui.MsgSetupAccessPassShort))
		}
	}
	if adminPassword == "" {
		notices = append(notices, webui.Error("admin_password", webui.MsgSetupAdminEmpty))
	} else if err := auth.ValidatePassword(adminPassword); err != nil {
		notices = append(notices, webui.Error("admin_password", webui.MsgSetupAdminShort))
	}
	if accessMode == "password" && accessPassword != "" && adminPassword == accessPassword {
		notices = append(notices, webui.Error("admin_password", webui.MsgSetupAdminSameAsGen))
	}
	if request.TLS == nil && !insecureAccepted {
		notices = append(notices, webui.Error("insecure_ack", webui.MsgSetupInsecureNeed))
	}
	canonical := ""
	if len(notices) == 0 {
		var err error
		canonical, err = app.prepareRepositoryRoot(storagePath)
		if err != nil {
			notices = append(notices, webui.Error("storage_path", webui.MsgSetupStorageInvalid))
		}
	}
	if len(notices) != 0 {
		app.renderSetupWizard(writer, request, session.CSRF, form, notices, http.StatusUnprocessableEntity)
		return
	}
	adminHash, err := auth.HashPassword(adminPassword)
	if err != nil {
		app.renderSetupWizard(writer, request, session.CSRF, form, []webui.Notice{webui.Error("admin_password", webui.MsgSetupAdminShort)}, http.StatusUnprocessableEntity)
		return
	}
	accessHash := ""
	if accessMode == "password" {
		accessHash, err = auth.HashPassword(accessPassword)
		if err != nil {
			app.renderSetupWizard(writer, request, session.CSRF, form, []webui.Notice{webui.Error("access_password", webui.MsgSetupAccessPassShort)}, http.StatusUnprocessableEntity)
			return
		}
	}
	unlock, err := bootstrap.AcquireSetupLock(request.Context(), app.Store.Dir())
	if err != nil {
		app.renderError(writer, request, http.StatusServiceUnavailable, webui.MsgErrUnavailable, "")
		return
	}
	completeErr := app.Store.CompleteSetup(request.Context(), canonical, accessMode, accessHash, adminHash, insecureAccepted)
	var cleanupErr error
	if completeErr == nil {
		cleanupErr = bootstrap.RemoveOwnerSetupFiles(app.Store.Dir())
	}
	unlock()
	if completeErr != nil {
		app.renderError(writer, request, http.StatusConflict, webui.MsgSetupRaceLost, "")
		return
	}
	if cleanupErr != nil {
		app.renderError(writer, request, http.StatusServiceUnavailable, webui.MsgErrUnavailable, "")
		return
	}
	app.Repositories.SetRoot(canonical)
	app.clearCookie(writer, request, setupCookie, true)
	http.Redirect(writer, request, "/?notice=setup_completed", http.StatusSeeOther)
}

func (app *App) renderSetupWizard(writer http.ResponseWriter, request *http.Request, csrf string, form webui.SetupForm, notices []webui.Notice, status int) {
	chrome, err := app.chrome(writer, request, webui.SectionSetup, "", csrf)
	if err != nil {
		app.writePlainError(writer, http.StatusServiceUnavailable)
		return
	}
	chrome.Notices = notices
	app.render(writer, status, webui.SetupPage{
		Chrome: chrome, Stage: webui.SetupWizard, SubmitURL: "/setup", RedeemURL: "/setup/redeem", Form: form,
		Prerequisites: []webui.Prerequisite{
			{Name: "git", Satisfied: app.GitVersion != "", Code: chooseMessage(app.GitVersion != "", webui.MsgPrereqGitFound, webui.MsgPrereqGitMissing), Detail: app.GitVersion},
			{Name: "git-http-backend", Satisfied: app.HTTPBackendFound, Code: chooseMessage(app.HTTPBackendFound, webui.MsgPrereqHTTPFound, webui.MsgPrereqHTTPMiss)},
		},
	})
}

func (app *App) prepareRepositoryRoot(value string) (string, error) {
	if !filepath.IsAbs(value) {
		return "", errors.New("repository root must be absolute")
	}
	clean := filepath.Clean(value)
	if err := os.MkdirAll(clean, 0o700); err != nil {
		return "", err
	}
	info, err := os.Stat(clean)
	if err != nil || !info.IsDir() {
		return "", errors.New("repository root is not a directory")
	}
	canonical, err := filepath.EvalSymlinks(clean)
	if err != nil {
		return "", err
	}
	stateDir, err := filepath.EvalSymlinks(app.Store.Dir())
	if err != nil {
		return "", err
	}
	if pathsOverlap(canonical, stateDir) {
		return "", errors.New("repository root must be separate from application state")
	}
	probe, err := os.CreateTemp(canonical, ".owngit-write-test-*")
	if err != nil {
		return "", err
	}
	probePath := probe.Name()
	if err := probe.Close(); err != nil {
		return "", err
	}
	if err := os.Remove(probePath); err != nil {
		return "", err
	}
	return canonical, nil
}

func pathsOverlap(left, right string) bool {
	leftToRight, leftErr := filepath.Rel(left, right)
	rightToLeft, rightErr := filepath.Rel(right, left)
	within := func(relative string, err error) bool {
		return err == nil && (relative == "." || (relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))))
	}
	return within(leftToRight, leftErr) || within(rightToLeft, rightErr)
}

func (app *App) handleLoginGet(writer http.ResponseWriter, request *http.Request, settings state.Settings, scope webui.AuthScope) {
	if scope == webui.AuthGeneral && settings.AccessMode == "open" {
		http.Redirect(writer, request, "/", http.StatusSeeOther)
		return
	}
	csrf := app.preauthCSRF(writer, request)
	chrome, err := app.chrome(writer, request, webui.SectionAuth, "", csrf)
	if err != nil {
		app.writePlainError(writer, http.StatusServiceUnavailable)
		return
	}
	submitURL := "/login"
	if scope == webui.AuthAdmin {
		submitURL = "/admin/login"
	}
	app.render(writer, http.StatusOK, webui.AuthPage{Chrome: chrome, Scope: scope, SubmitURL: submitURL, Next: localNext(request.URL.Query().Get("next"), "/")})
}

func (app *App) handleLoginPost(writer http.ResponseWriter, request *http.Request, settings state.Settings, scope webui.AuthScope) {
	if !parseForm(writer, request) {
		return
	}
	if !app.validPreauthCSRF(request, postValue(request, "csrf")) {
		app.renderError(writer, request, http.StatusForbidden, webui.MsgErrCSRF, "")
		return
	}
	kind, field, cookieName := "general", "password", generalCookie
	if scope == webui.AuthAdmin {
		kind, field, cookieName = "admin", "admin_password", adminCookie
	} else if settings.AccessMode == "open" {
		http.Redirect(writer, request, "/", http.StatusSeeOther)
		return
	}
	password := postValue(request, field)
	next := localNext(postValue(request, "next"), "/")
	if password == "" {
		code := webui.MsgLoginEmpty
		if scope == webui.AuthAdmin {
			code = webui.MsgAdminEmpty
		}
		app.renderLoginFailure(writer, request, scope, field, next, code, false, http.StatusUnprocessableEntity)
		return
	}
	session, err := app.Auth.Authenticate(request.Context(), kind, password, request.RemoteAddr)
	if err != nil {
		code := webui.MsgLoginFailed
		if scope == webui.AuthAdmin {
			code = webui.MsgAdminFailed
		}
		locked := errors.Is(err, auth.ErrRateLimited)
		if locked {
			if scope == webui.AuthAdmin {
				code = webui.MsgAdminLocked
			} else {
				code = webui.MsgLoginLocked
			}
		}
		app.renderLoginFailure(writer, request, scope, field, next, code, locked, http.StatusUnauthorized)
		return
	}
	app.setCookie(writer, request, cookieName, session.Token, session.Expires, true)
	app.clearCookie(writer, request, preauthCookie, true)
	http.Redirect(writer, request, next, http.StatusSeeOther)
}

func (app *App) renderLoginFailure(writer http.ResponseWriter, request *http.Request, scope webui.AuthScope, field, next string, code webui.MessageCode, locked bool, status int) {
	csrf := app.preauthCSRF(writer, request)
	chrome, err := app.chrome(writer, request, webui.SectionAuth, "", csrf)
	if err != nil {
		app.writePlainError(writer, http.StatusServiceUnavailable)
		return
	}
	chrome.Notices = []webui.Notice{webui.Error(field, code)}
	app.render(writer, status, webui.AuthPage{Chrome: chrome, Scope: scope, SubmitURL: request.URL.Path, Next: next, Locked: locked})
}

func (app *App) handleLogout(writer http.ResponseWriter, request *http.Request, scope webui.AuthScope) {
	if !parseForm(writer, request) {
		return
	}
	if !app.validCSRF(request, postValue(request, "csrf")) {
		app.renderError(writer, request, http.StatusForbidden, webui.MsgErrCSRF, "")
		return
	}
	kind, cookieName, notice := "general", generalCookie, "logout"
	if scope == webui.AuthAdmin {
		kind, cookieName, notice = "admin", adminCookie, "admin_logout"
	}
	app.deleteSessionCookie(request.Context(), request, kind, cookieName)
	app.clearCookie(writer, request, cookieName, true)
	http.Redirect(writer, request, "/?notice="+notice, http.StatusSeeOther)
}

func chooseMessage(condition bool, yes, no webui.MessageCode) webui.MessageCode {
	if condition {
		return yes
	}
	return no
}
