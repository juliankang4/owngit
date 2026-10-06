package server

import (
	"errors"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"path/filepath"
	"strings"
	"time"

	"owngit/internal/auth"
	"owngit/internal/repository"
	"owngit/internal/requestctx"
	"owngit/internal/state"
	"owngit/internal/webui"
)

func (app *App) handleSetupGet(writer http.ResponseWriter, request *http.Request, settings state.Settings) {
	stage := webui.SetupWelcome
	csrf := ""
	_, unknownHost := app.unknownHost(request)
	if settings.Initialized {
		stage = webui.SetupUnavailable
	} else if session, ok, err := app.setupSessionForHost(request); err != nil {
		app.answerUnavailable(writer, request, "session read", err)
		return
	} else if ok {
		stage = webui.SetupWizard
		csrf = session.CSRF
	} else if app.Approvals.Active() && !unknownHost {
		app.handleSetupApprovalPage(writer, request)
		return
	} else {
		csrf = app.preauthCSRF(writer, request)
	}
	chrome, err := app.chrome(writer, request, webui.SectionSetup, "", csrf)
	if err != nil {
		app.answerUnavailable(writer, request, "page frame read", err)
		return
	}
	page := webui.SetupPage{
		Chrome: chrome, Stage: stage, RedeemURL: "/setup/redeem", SubmitURL: "/setup",
		Prerequisites: app.setupPrerequisites(),
		Form:          webui.SetupForm{SuggestedPath: app.SuggestedRepositoryRoot, AccessMode: webui.AccessOpen},
	}
	// Before redemption an unknown Host sees only the redemption form.
	if unknownHost && stage != webui.SetupWizard {
		page.Prerequisites, page.Form = nil, webui.SetupForm{}
	}
	if stage == webui.SetupWizard {
		page.KeepHost, page.KeepHostSetupOnly = app.setupHostToKeep(request), unknownHost
		// On a computer without a screen the owner usually keeps the address
		// used by a known private client. A public or unknown forwarded client
		// gets the safer shared-password default and does not keep the Host by
		// default.
		notice, protect := setupAccessNotice(request)
		page.Form.KeepHost = page.KeepHost != "" && app.HeadlessListen != "" && !protect
		if protect {
			page.Form.AccessMode = webui.AccessPassword
			page.Chrome.Notices = append(page.Chrome.Notices, notice)
		}
	}
	if settings.Initialized {
		page.Reason = webui.MsgSetupAlreadyDone
		page.RecoveryHint = webui.MsgSetupReissueHint
	}
	app.render(writer, request, http.StatusOK, page)
}

func (app *App) handleSetupRedeem(writer http.ResponseWriter, request *http.Request) {
	if !app.parseForm(writer, request) {
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
	sessionToken, csrf := auth.RandomToken(32), auth.RandomToken(32)
	expires := app.now().Add(20 * time.Minute)
	redeemed, err := app.Store.RedeemBootstrap(request.Context(), token, sessionToken, csrf, app.now(), expires)
	if err != nil {
		app.answerUnavailable(writer, request, "bootstrap redemption", err)
		return
	}
	if !redeemed {
		app.renderError(writer, request, http.StatusForbidden, webui.MsgSetupLinkInvalid, "")
		return
	}
	// Redemption replaced any earlier setup session, so the binding follows
	// the new one.
	if host, unknown := app.unknownHost(request); unknown {
		app.setupHosts.set(sessionToken, host)
	} else {
		app.setupHosts.clear()
	}
	app.setCookie(writer, request, cookieNameForScheme(request, setupCookie), sessionToken, expires, true)
	app.clearCookie(writer, request, cookieNameForScheme(request, preauthCookie), true)
	http.Redirect(writer, request, "/setup", http.StatusSeeOther)
}

func (app *App) handleSetupPost(writer http.ResponseWriter, request *http.Request) {
	if !app.parseForm(writer, request) {
		return
	}
	session, ok, err := app.setupSessionForHost(request)
	if err != nil {
		app.answerUnavailable(writer, request, "session read", err)
		return
	}
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
	// The Host to keep comes from this request, never from the form, and
	// only when the wizard would offer it.
	keepHost := formChecked(postValue(request, "keep_host"))
	if keepHost {
		answers.KeepHost = app.setupHostToKeep(request)
	}
	form := webui.SetupForm{
		StoragePath: answers.StoragePath, SuggestedPath: app.SuggestedRepositoryRoot,
		AccessMode: webui.AccessMode(answers.AccessMode), InsecureAck: answers.InsecureAccepted, KeepHost: keepHost,
	}
	// Plain HTTP needs the acknowledgement, unless it came over the
	// tailnet, which Tailscale encrypted.
	feedback, err := app.CompleteSetup(request.Context(), answers, !requestctx.Of(request).Secure() && !app.throughTailnet(request))
	switch {
	case len(feedback.Problems) != 0:
		notices := append(feedback.Problems, feedback.Warnings...)
		app.renderSetupWizard(writer, request, session.CSRF, form, notices, http.StatusUnprocessableEntity)
		return
	case errors.Is(err, ErrSetupUnavailable):
		app.renderError(writer, request, unavailable(request, "setup completion", err), webui.MsgErrUnavailable, "")
		return
	case errors.Is(err, ErrSetupCompletedElsewhere):
		app.renderError(writer, request, http.StatusConflict, webui.MsgSetupRaceLost, "")
		return
	case err != nil:
		// Setup is saved and in effect; only the used owner setup files
		// remain. The result says so, and the log names them and why, as the
		// terminal does.
		logFailure(request, "setup file removal", err)
	}
	app.clearCookie(writer, request, cookieNameForScheme(request, setupCookie), true)
	app.returnToLocalListen(request.Context(), answers.KeepHost)
	// An unknown Host that was not kept is refused from now on, so the
	// result is shown here instead of on the dashboard.
	if _, unknown := app.unknownHost(request); unknown {
		app.renderSetupDoneElsewhere(writer, request)
		return
	}
	// Without shared access this browser opens the dashboard at once and
	// shows the notice there. With it, the address loses its notice at the
	// sign-in page, so the dashboard shows the notice after sign-in instead
	// (see handleOverview).
	notice := setupResultNotice(err, len(feedback.Warnings) != 0)
	if answers.AccessMode == "open" {
		app.setupResult.Store(nil)
	}
	app.noticeRedirect(writer, request, "/?notice="+notice, http.StatusSeeOther)
}

// renderSetupDoneElsewhere tells a browser on an unknown Host that setup is
// finished but its address is no longer accepted. It links nowhere, because
// every page on this Host now answers 421.
func (app *App) renderSetupDoneElsewhere(writer http.ResponseWriter, request *http.Request) {
	chrome, err := app.chrome(writer, request, webui.SectionSetup, "", "")
	if err != nil {
		app.answerUnavailable(writer, request, "page frame read", err)
		return
	}
	chrome.Nav = webui.Nav{}
	hint := webui.MsgSetupDoneHostNotKeptHint
	if app.listenReturned.Load() {
		hint = webui.MsgSetupDoneLocalOnlyHint
	}
	app.render(writer, request, http.StatusOK, webui.SetupPage{Chrome: chrome, Stage: webui.SetupUnavailable, Reason: webui.MsgSetupDoneHostNotKept, RecoveryHint: hint})
}

// publicPeer reports whether the request comes from a known public Internet
// address: not loopback, not a private or tailnet range, not link-local.
// Behind a trusted reverse proxy it is the established client boundary.
func publicPeer(request *http.Request) bool {
	host := requestctx.Of(request).ClientAddress
	if split, _, err := net.SplitHostPort(host); err == nil {
		host = split
	}
	address, err := netip.ParseAddr(host)
	if err != nil {
		return false
	}
	address = address.Unmap()
	return !(address.IsLoopback() || address.IsPrivate() || tailnetRange.Contains(address) ||
		address.IsLinkLocalUnicast() || address.IsUnspecified())
}

// tailnetRange is the shared address space (RFC 6598) that Tailscale uses.
var tailnetRange = netip.MustParsePrefix("100.64.0.0/10")

func publicNetworkNotice() webui.Notice {
	return webui.Notice{Kind: webui.NoticeInfo, Code: webui.MsgSetupPublicNetwork, Field: "access_mode"}
}

func setupAccessNotice(request *http.Request) (webui.Notice, bool) {
	if requestctx.Of(request).ClientProvenance == requestctx.ClientForwardedUnknown {
		return webui.Notice{Kind: webui.NoticeWarning, Code: webui.MsgForwardedClientUnknown, Field: "access_mode"}, true
	}
	if publicPeer(request) {
		return publicNetworkNotice(), true
	}
	return webui.Notice{}, false
}

func (app *App) setupPrerequisites() []webui.Prerequisite {
	return []webui.Prerequisite{
		{Name: "git", Satisfied: app.GitVersion != "", Code: chooseMessage(app.GitVersion != "", webui.MsgPrereqGitFound, webui.MsgPrereqGitMissing), Detail: app.GitVersion},
		{Name: "git-http-backend", Satisfied: app.HTTPBackendFound, Code: chooseMessage(app.HTTPBackendFound, webui.MsgPrereqHTTPFound, webui.MsgPrereqHTTPMiss)},
	}
}

func (app *App) renderSetupWizard(writer http.ResponseWriter, request *http.Request, csrf string, form webui.SetupForm, notices []webui.Notice, status int) {
	_, unknownHost := app.unknownHost(request)
	chrome, err := app.chrome(writer, request, webui.SectionSetup, "", csrf)
	if err != nil {
		app.answerUnavailable(writer, request, "page frame read", err)
		return
	}
	chrome.Notices = notices
	if notice, protect := setupAccessNotice(request); protect {
		chrome.Notices = append(chrome.Notices, notice)
	}
	app.render(writer, request, status, webui.SetupPage{
		Chrome: chrome, Stage: webui.SetupWizard, SubmitURL: "/setup", RedeemURL: "/setup/redeem", Form: form,
		Prerequisites: app.setupPrerequisites(), KeepHost: app.setupHostToKeep(request), KeepHostSetupOnly: unknownHost,
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
		app.answerUnavailable(writer, request, "page frame read", err)
		return
	}
	submitURL := "/login"
	if scope == webui.AuthAdmin {
		submitURL = "/admin/login"
	}
	page := webui.AuthPage{Chrome: chrome, Scope: scope, SubmitURL: submitURL, Next: localNext(request.URL.Query().Get("next"), "/")}
	app.keepRepositoryContext(request, &page)
	app.render(writer, request, http.StatusOK, page)
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
	name := parts[0]
	if name == "" || name == "new" || name == "new-import" {
		return
	}
	address, found, err := app.Store.ResolveRepositoryName(request.Context(), name, app.now())
	if err != nil || !found || address.Current != name {
		return
	}
	stored, exists, err := app.Store.Repository(request.Context(), address.RepositoryID)
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
	if !app.parseForm(writer, request) {
		return
	}
	if !app.validPreauthCSRF(request, postValue(request, "csrf")) {
		app.renderError(writer, request, http.StatusForbidden, webui.MsgErrCSRF, "")
		return
	}
	kind, field, cookieName := "general", "password", cookieNameForScheme(request, generalCookie)
	if scope == webui.AuthAdmin {
		kind, field, cookieName = "admin", "admin_password", cookieNameForScheme(request, adminCookie)
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
	// A shared sign-in's cookie follows the saved cross-site link choice,
	// read before the session starts so that no session is left without
	// its cookie.
	links := state.CrossSiteStrict
	if kind == "general" {
		var err error
		if links, err = app.Store.CrossSiteLinks(request.Context()); err != nil {
			if errors.As(err, new(*state.PolicyError)) {
				logFailure(request, "sign-in", err)
				app.renderLoginFailure(writer, request, scope, "", next, webui.MsgCrossSiteUnreadableIn, false, http.StatusConflict)
			} else {
				app.renderLoginFailure(writer, request, scope, "", next, webui.MsgErrUnavailable, false, unavailable(request, "sign-in", err))
			}
			return
		}
	}
	// The new session replaces the one of kind this browser holds, so no
	// copy of the old token outlives it.
	replaced := ""
	if cookie, err := request.Cookie(cookieName); err == nil {
		replaced = cookie.Value
	}
	session, err := app.Auth.Authenticate(request.Context(), kind, password, requestctx.Of(request).ClientAddress, replaced)
	if err != nil {
		admin := scope == webui.AuthAdmin
		var policyErr *state.PolicyError
		switch {
		case errors.Is(err, auth.ErrRateLimited):
			locked := chooseMessage(admin, webui.MsgAdminLocked, webui.MsgLoginLocked)
			if auth.IsServerWide(err) {
				locked = webui.MsgAdminLockedServerWide
			}
			app.renderLoginFailure(writer, request, scope, field, next, locked, true, http.StatusTooManyRequests)
		case errors.Is(err, auth.ErrInvalidCredentials):
			app.renderLoginFailure(writer, request, scope, field, next, chooseMessage(admin, webui.MsgAdminFailed, webui.MsgLoginFailed), false, http.StatusUnauthorized)
		case errors.As(err, &policyErr):
			logFailure(request, "sign-in", err)
			code := webui.MsgSessionUnreadableSignIn
			if policyErr.Setting() == "login_limits" {
				code = webui.MsgLoginLimitsUnreadable
			}
			app.renderLoginFailure(writer, request, scope, "", next, code, false, http.StatusConflict)
		default:
			// The password was not judged, or the session could not be saved.
			app.renderLoginFailure(writer, request, scope, "", next, webui.MsgErrUnavailable, false, unavailable(request, "sign-in", err))
		}
		return
	}
	if kind == "general" {
		app.setGeneralCookie(writer, request, session.Token, session.Expires, links)
	} else {
		app.setCookie(writer, request, cookieName, session.Token, session.Expires, true)
	}
	app.clearCookie(writer, request, cookieNameForScheme(request, preauthCookie), true)
	http.Redirect(writer, request, next, http.StatusSeeOther)
}

func (app *App) renderLoginFailure(writer http.ResponseWriter, request *http.Request, scope webui.AuthScope, field, next string, code webui.MessageCode, locked bool, status int) {
	csrf := app.preauthCSRF(writer, request)
	chrome, err := app.chrome(writer, request, webui.SectionAuth, "", csrf)
	if err != nil {
		app.answerUnavailable(writer, request, "page frame read", err)
		return
	}
	chrome.Notices = []webui.Notice{webui.Error(field, code)}
	page := webui.AuthPage{Chrome: chrome, Scope: scope, SubmitURL: request.URL.Path, Next: next, Locked: locked}
	app.keepRepositoryContext(request, &page)
	app.render(writer, request, status, page)
}

// adminPasswordNotice is the notice and status of a browser form refused by
// confirmAdmin. A missing, wrong or rate-limited password belongs to field.
// Unreadable login attempt limits name that setting, because a wrong
// password cannot be counted until they are set again. Any other check
// that could not be completed is a page-level unavailable notice, because
// the password may well be right.
func adminPasswordNotice(request *http.Request, err error, field string) (webui.Notice, int) {
	var policyErr *state.PolicyError
	switch {
	case errors.As(err, &policyErr) && policyErr.Setting() == "login_limits":
		logFailure(request, "administrator password check", err)
		return webui.Error(field, webui.MsgLoginLimitsUnreadable), http.StatusConflict
	case errors.Is(err, errAdminPasswordMissing):
		return webui.Error(field, webui.MsgAdminEmpty), http.StatusUnauthorized
	case errors.Is(err, auth.ErrRateLimited):
		if auth.IsServerWide(err) {
			return webui.Error(field, webui.MsgAdminLockedServerWide), http.StatusTooManyRequests
		}
		return webui.Error(field, webui.MsgAdminLocked), http.StatusTooManyRequests
	case errors.Is(err, auth.ErrInvalidCredentials):
		return webui.Error(field, webui.MsgAdminFailed), http.StatusUnauthorized
	}
	return webui.Error("", webui.MsgErrUnavailable), unavailable(request, "administrator password check", err)
}

func (app *App) handleLogout(writer http.ResponseWriter, request *http.Request, scope webui.AuthScope) {
	if !app.parseForm(writer, request) {
		return
	}
	if !app.requireCSRF(writer, request) {
		return
	}
	// Leaving shared access is confirmed on the sign-in page it leads to,
	// not after the next sign-in. Signing out also ends this browser's
	// administrator confirmation. End ends only the administrator
	// confirmation and keeps the shared session, so its confirmation stays
	// on the dashboard.
	type ending struct{ kind, cookie string }
	endings, target := []ending{{"general", generalCookie}, {"admin", adminCookie}}, "/login?notice=logout"
	if scope == webui.AuthAdmin {
		endings, target = []ending{{"admin", adminCookie}}, "/?notice=admin_logout"
		if app.signOutKeepsSecureAddress(request) {
			// The administrator confirmation of the secure address stays:
			// this plain request never carried its cookie. A reader who
			// holds no plain general session cannot open the dashboard, so
			// the sign-in page carries the sentence instead of the page
			// the dashboard would send them to.
			target = "/?notice=admin_logout_secure_kept"
			settings, err := app.Store.Settings(request.Context())
			if err != nil {
				app.answerUnavailable(writer, request, "settings read", err)
				return
			}
			if !app.generalAccess(request, settings) {
				target = "/login?notice=admin_logout_secure_kept"
			}
		}
	} else if app.signOutKeepsSecureAddress(request) {
		// This sign-out cannot end the session of the secure address: its
		// cookie never reached this one. The sign-in page says so.
		target = "/login?notice=logout_secure_kept"
	}
	// The browser forgets a session only after the server ended it. A
	// failed sign-out says the reader is still signed in, and the kept
	// cookies let them try again; a cleared one would leave a live session
	// this browser can no longer end.
	for _, end := range endings {
		for _, name := range signOutCookieNames(request, end.cookie) {
			cookie, err := request.Cookie(name)
			if err != nil {
				continue
			}
			if err := app.Store.DeleteSession(request.Context(), cookie.Value, end.kind); err != nil {
				app.renderError(writer, request, unavailable(request, "sign-out", err), webui.MsgLogoutFailed, "")
				return
			}
		}
	}
	for _, end := range endings {
		for _, name := range signOutCookieNames(request, end.cookie) {
			app.clearCookie(writer, request, name, true)
		}
	}
	app.noticeRedirect(writer, request, target, http.StatusSeeOther)
}

func chooseMessage(condition bool, yes, no webui.MessageCode) webui.MessageCode {
	if condition {
		return yes
	}
	return no
}
