package server

import (
	"errors"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"time"

	"owngit/internal/auth"
	"owngit/internal/repository"
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
	} else if app.Approvals.Active() {
		app.handleSetupApprovalPage(writer, request)
		return
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
		Prerequisites: app.setupPrerequisites(),
		Form:          webui.SetupForm{SuggestedPath: app.SuggestedRepositoryRoot, AccessMode: webui.AccessOpen},
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
	answers := SetupAnswers{
		StoragePath:    strings.TrimSpace(postValue(request, "storage_path")),
		AccessMode:     postValue(request, "access_mode"),
		AccessPassword: postValue(request, "access_password"),
		AdminPassword:  postValue(request, "admin_password"),
		// OwnGit itself serves plain HTTP, so a browser request without TLS
		// must acknowledge it.
		InsecureAccepted: formChecked(postValue(request, "insecure_ack")),
	}
	form := webui.SetupForm{
		StoragePath: answers.StoragePath, SuggestedPath: app.SuggestedRepositoryRoot,
		AccessMode: webui.AccessMode(answers.AccessMode), InsecureAck: answers.InsecureAccepted,
	}
	notices, err := app.CompleteSetup(request.Context(), answers, request.TLS == nil)
	switch {
	case len(notices) != 0:
		app.renderSetupWizard(writer, request, session.CSRF, form, notices, http.StatusUnprocessableEntity)
		return
	case errors.Is(err, ErrSetupUnavailable):
		app.renderError(writer, request, http.StatusServiceUnavailable, webui.MsgErrUnavailable, "")
		return
	case errors.Is(err, ErrSetupNotSaved):
		app.renderError(writer, request, http.StatusConflict, webui.MsgSetupRaceLost, "")
		return
	case err != nil:
		// Committed, but the obsolete owner setup files remain.
		app.renderError(writer, request, http.StatusServiceUnavailable, webui.MsgErrUnavailable, "")
		return
	}
	app.clearCookie(writer, request, setupCookie, true)
	// Without shared access this browser opens the dashboard at once and
	// shows the notice there. With it, the address loses its notice at the
	// sign-in page, so the dashboard shows the notice after sign-in instead
	// (see handleOverview).
	if answers.AccessMode == "open" {
		app.setupFinished.Store(false)
	}
	app.noticeRedirect(writer, request, "/?notice=setup_completed", http.StatusSeeOther)
}

func (app *App) setupPrerequisites() []webui.Prerequisite {
	return []webui.Prerequisite{
		{Name: "git", Satisfied: app.GitVersion != "", Code: chooseMessage(app.GitVersion != "", webui.MsgPrereqGitFound, webui.MsgPrereqGitMissing), Detail: app.GitVersion},
		{Name: "git-http-backend", Satisfied: app.HTTPBackendFound, Code: chooseMessage(app.HTTPBackendFound, webui.MsgPrereqHTTPFound, webui.MsgPrereqHTTPMiss)},
	}
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
		Prerequisites: app.setupPrerequisites(),
	})
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
	page := webui.AuthPage{Chrome: chrome, Scope: scope, SubmitURL: submitURL, Next: localNext(request.URL.Query().Get("next"), "/")}
	app.keepRepositoryContext(request, &page)
	app.render(writer, http.StatusOK, page)
}

// keepRepositoryContext keeps the repository an administrator prompt was
// opened from on screen: the sidebar keeps that repository's sections with
// the requested one marked, and the page names the repository. It applies
// only to the administrator prompt, and only for a viewer who may already
// see the repository, so it never reveals a repository name to someone who
// has not passed general access.
func (app *App) keepRepositoryContext(request *http.Request, page *webui.AuthPage) {
	if page.Scope != webui.AuthAdmin || !page.Chrome.Viewer.GeneralUnlocked || page.Chrome.Nav.OverviewURL == "" {
		return
	}
	target, err := url.Parse(page.Next)
	if err != nil || !strings.HasPrefix(target.Path, "/repositories/") {
		return
	}
	parts := strings.Split(strings.TrimPrefix(target.Path, "/repositories/"), "/")
	id := parts[0]
	if id == "" || id == "new" || id == "new-import" {
		return
	}
	stored, exists, err := app.Store.Repository(request.Context(), id)
	if err != nil || !exists {
		return
	}
	active := webui.RepoTabOverview
	if len(parts) > 1 {
		switch parts[1] {
		case "import":
			active = webui.RepoTabImport
		case "settings":
			active = webui.RepoTabSettings
		case "delete":
			active = webui.RepoTabDelete
		case "tasks", "helper-credentials", "configured-checks", "runner-tokens":
			active = webui.RepoTabChecks
		}
	}
	base := app.baseRepositoryPage(request, page.Chrome, stored, repository.Summary{})
	page.Repo = base.Repo
	page.Tabs = repositoryTabs(base, active)
	page.Chrome.Nav.Section = webui.SectionRepository
	page.Chrome.Nav.ActiveRepoID = stored.ID
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
	page := webui.AuthPage{Chrome: chrome, Scope: scope, SubmitURL: request.URL.Path, Next: next, Locked: locked}
	app.keepRepositoryContext(request, &page)
	app.render(writer, status, page)
}

func (app *App) handleLogout(writer http.ResponseWriter, request *http.Request, scope webui.AuthScope) {
	if !parseForm(writer, request) {
		return
	}
	if !app.validCSRF(request, postValue(request, "csrf")) {
		app.renderError(writer, request, http.StatusForbidden, webui.MsgErrCSRF, "")
		return
	}
	// Leaving shared access is confirmed on the sign-in page it leads to,
	// not after the next sign-in. Ending the administrator session keeps the
	// shared session, so its confirmation stays on the dashboard.
	kind, cookieName, target := "general", generalCookie, "/login?notice=logout"
	if scope == webui.AuthAdmin {
		kind, cookieName, target = "admin", adminCookie, "/?notice=admin_logout"
	}
	app.deleteSessionCookie(request.Context(), request, kind, cookieName)
	app.clearCookie(writer, request, cookieName, true)
	app.noticeRedirect(writer, request, target, http.StatusSeeOther)
}

func chooseMessage(condition bool, yes, no webui.MessageCode) webui.MessageCode {
	if condition {
		return yes
	}
	return no
}
