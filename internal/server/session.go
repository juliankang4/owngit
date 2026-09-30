package server

import (
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"owngit/internal/auth"
	"owngit/internal/requestctx"
	"owngit/internal/state"
	"owngit/internal/webui"
)

func (app *App) requireGeneral(writer http.ResponseWriter, request *http.Request, settings state.Settings) (state.Session, bool) {
	if settings.AccessMode == "open" {
		expires := app.now().Add(12 * time.Hour)
		if cookie, err := request.Cookie(generalCookie); err == nil && validOpenToken(cookie.Value) {
			return state.Session{Kind: "general", CSRF: cookie.Value, Version: settings.AccessSessionVersion, Expires: expires}, true
		}
		token := auth.RandomToken(32)
		app.setCookie(writer, request, generalCookie, token, expires, true)
		return state.Session{Kind: "general", CSRF: token, Version: settings.AccessSessionVersion, Expires: expires}, true
	}
	session, ok, err := app.cookieSession(request, "general", generalCookie)
	switch {
	case err != nil:
		app.answerUnavailable(writer, request, "session read", err)
	case ok:
		return session, true
	default:
		http.Redirect(writer, request, "/login?next="+url.QueryEscape(loginNext(request)), http.StatusSeeOther)
	}
	return state.Session{}, false
}

// loginNext is where signing in again returns to. A page read with GET returns
// to itself. A form submission returns to the page that held the form: the
// same-origin Referer when there is one, otherwise the submission address.
// Either can be an address that accepts only POST, such as a refused merge
// shown at its own route, so it is mapped to the GET page that holds that
// form. Returning there directly would answer 404.
func loginNext(request *http.Request) string {
	if request.Method == http.MethodGet || request.Method == http.MethodHead {
		// A result notice belongs to the moment of its action. Signing in
		// later must not bring it back.
		target := *request.URL
		if query := target.Query(); query.Has("notice") {
			query.Del("notice")
			target.RawQuery = query.Encode()
		}
		return localNext(target.RequestURI(), "/")
	}
	target := request.URL
	if referer, err := url.Parse(request.Header.Get("Referer")); err == nil && referer.Host == requestctx.Of(request).Host && (referer.Scheme == "http" || referer.Scheme == "https") {
		target = referer
	}
	path := target.EscapedPath()
	page := formPage(path)
	if page == path && target.RawQuery != "" {
		page += "?" + target.RawQuery
	}
	fallback := "/"
	if id, ok := repositoryOfPath(request.URL.EscapedPath()); ok {
		fallback = "/repositories/" + id
	}
	return localNext(page, fallback)
}

// formPage maps an address that accepts only POST to the GET page that holds
// its form. Every other address is returned unchanged.
func formPage(path string) string {
	switch path {
	case "/repositories":
		return "/repositories/new"
	case "/setup/redeem", "/setup/approval":
		return "/setup"
	case "/logout", "/admin/logout", releaseDismissPath:
		return "/"
	}
	id, ok := repositoryOfPath(path)
	if !ok {
		return path
	}
	base := "/repositories/" + id
	parts := strings.Split(strings.TrimPrefix(path, base+"/"), "/")
	switch {
	// Merge, close, reopen and the review actions of one pull request.
	case len(parts) >= 3 && parts[0] == "pull-requests":
		return base + "/pull-requests/" + parts[1]
	// Default branch and any later repository setting action.
	case len(parts) == 2 && parts[0] == "settings":
		return base + "/settings"
	// Restore preview.
	case len(parts) == 2 && parts[0] == "restore":
		return base + "/restore"
	}
	return path
}

// repositoryOfPath returns the repository identifier of a path below
// /repositories/<id>/.
func repositoryOfPath(path string) (string, bool) {
	rest, ok := strings.CutPrefix(path, "/repositories/")
	if !ok {
		return "", false
	}
	id, _, found := strings.Cut(rest, "/")
	return id, found && id != ""
}

// cookieSession returns the session of kind named by the request's cookie.
// ok is false for no cookie and for an unknown, expired or ended session. An
// error means the session could not be read and may be valid, so it neither
// grants nor refuses anything: the request is answered through
// answerUnavailable instead of being sent to sign in or refused as a forgery.
func (app *App) cookieSession(request *http.Request, kind, cookieName string) (state.Session, bool, error) {
	cookie, err := request.Cookie(cookieName)
	if err != nil || cookie.Value == "" {
		return state.Session{}, false, nil
	}
	session, ok, err := app.Auth.ValidateSession(request.Context(), cookie.Value, kind)
	return endedSessionKept(request, session, ok, err)
}

// endedSessionKept completes a session read. A session found ended whose
// removal failed is not valid: the failure is logged, and the session reads
// as none.
func endedSessionKept(request *http.Request, session state.Session, ok bool, err error) (state.Session, bool, error) {
	if errors.Is(err, state.ErrEndedSessionKept) {
		logFailure(request, "ended session removal", err)
		return state.Session{}, false, nil
	}
	return session, ok, err
}

// validCSRF reports whether submitted is the CSRF token of a session the
// request holds, or its open-access token. A token that matches a session
// that was read is valid even when another could not be read. A token that
// matches none is refused only when everything it could match was read;
// otherwise the error is returned.
func (app *App) validCSRF(request *http.Request, submitted string) (bool, error) {
	if submitted == "" {
		return false, nil
	}
	var readErr error
	matches := func(session state.Session, ok bool, err error) bool {
		if readErr == nil {
			readErr = err
		}
		return ok && constantEqual(session.CSRF, submitted)
	}
	if matches(app.setupSession(request)) ||
		matches(app.cookieSession(request, "admin", adminCookie)) ||
		matches(app.cookieSession(request, "general", generalCookie)) {
		return true, nil
	}
	settings, err := app.Store.Settings(request.Context())
	if err != nil {
		return false, err
	}
	if settings.AccessMode == "open" {
		if cookie, err := request.Cookie(generalCookie); err == nil && validOpenToken(cookie.Value) && constantEqual(cookie.Value, submitted) {
			return true, nil
		}
	}
	return false, readErr
}

// requireCSRF reports whether a form submission carries a valid CSRF token,
// and otherwise answers it: 403 for a token that matches nothing, and
// unavailable when that could not be decided.
func (app *App) requireCSRF(writer http.ResponseWriter, request *http.Request) bool {
	valid, err := app.validCSRF(request, postValue(request, "csrf"))
	switch {
	case err != nil:
		app.answerUnavailable(writer, request, "CSRF check", err)
	case !valid:
		app.renderError(writer, request, http.StatusForbidden, webui.MsgErrCSRF, "")
	}
	return valid
}

func validOpenToken(value string) bool {
	if len(value) != 43 {
		return false
	}
	decoded, err := base64.RawURLEncoding.DecodeString(value)
	return err == nil && len(decoded) == 32
}

func (app *App) preauthCSRF(writer http.ResponseWriter, request *http.Request) string {
	if cookie, err := request.Cookie(preauthCookie); err == nil && len(cookie.Value) >= 32 {
		return cookie.Value
	}
	token := auth.RandomToken(32)
	app.setCookie(writer, request, preauthCookie, token, app.now().Add(20*time.Minute), true)
	return token
}

func (app *App) validPreauthCSRF(request *http.Request, submitted string) bool {
	cookie, err := request.Cookie(preauthCookie)
	return err == nil && submitted != "" && constantEqual(cookie.Value, submitted)
}

// setCookie sets a Strict cookie: a request that another site starts does
// not carry it. Only the general session cookie can be Lax, by the owner's
// choice (setGeneralCookie).
func (app *App) setCookie(writer http.ResponseWriter, request *http.Request, name, value string, expires time.Time, httpOnly bool) {
	app.writeCookie(writer, request, name, value, expires, httpOnly, http.SameSiteStrictMode)
}

func (app *App) writeCookie(writer http.ResponseWriter, request *http.Request, name, value string, expires time.Time, httpOnly bool, sameSite http.SameSite) {
	http.SetCookie(writer, &http.Cookie{
		Name: name, Value: value, Path: "/", Expires: expires,
		MaxAge: int(expires.Sub(app.now()).Seconds()), HttpOnly: httpOnly,
		Secure: requestctx.Of(request).Secure(), SameSite: sameSite,
	})
}

func (app *App) clearCookie(writer http.ResponseWriter, request *http.Request, name string, httpOnly bool) {
	http.SetCookie(writer, &http.Cookie{
		Name: name, Value: "", Path: "/", MaxAge: -1, Expires: time.Unix(1, 0),
		HttpOnly: httpOnly, Secure: requestctx.Of(request).Secure(), SameSite: http.SameSiteStrictMode,
	})
}

// chrome builds the frame every page shares. Its error names the read that
// failed.
func (app *App) chrome(writer http.ResponseWriter, request *http.Request, section webui.NavSection, activeRepository, csrf string) (webui.Chrome, error) {
	settings, err := app.Store.Settings(request.Context())
	if err != nil {
		return webui.Chrome{}, fmt.Errorf("settings read: %w", err)
	}
	lang := app.language(writer, request)
	appearance := app.appearance(writer, request)
	general, generalOK, err := app.cookieSession(request, "general", generalCookie)
	if err != nil {
		return webui.Chrome{}, fmt.Errorf("session read: %w", err)
	}
	authority, err := app.adminAuthority(writer, request)
	if err != nil {
		return webui.Chrome{}, fmt.Errorf("administrator confirmation read: %w", err)
	}
	if csrf == "" {
		if authority.confirmed {
			csrf = authority.session.CSRF
		} else if generalOK {
			csrf = general.CSRF
		} else {
			setup, ok, err := app.setupSession(request)
			if err != nil {
				return webui.Chrome{}, fmt.Errorf("session read: %w", err)
			}
			if ok {
				csrf = setup.CSRF
			}
		}
	}
	accessMode := webui.AccessOpen
	if settings.AccessMode == "password" {
		accessMode = webui.AccessPassword
	}
	info := requestctx.Of(request)
	chrome := webui.Chrome{
		Lang: lang, Appearance: appearance, Now: app.now(), CurrentURL: request.URL.RequestURI(), CSRF: csrf, Version: app.Version,
		Viewer: webui.Viewer{
			AccessMode: accessMode, GeneralUnlocked: settings.AccessMode == "open" || generalOK,
			SetupComplete: settings.Initialized,
		},
		Connection: webui.Connection{
			Encrypted: info.Secure(), Proxy: info.Secure() && info.Proxied, Tailscale: app.throughTailscale(request), Tailnet: app.throughTailnet(request),
			Host: info.Host, InsecureAcknowledged: settings.InsecureHTTPAccepted,
		},
	}
	// A Host admitted only to redeem the setup link learns nothing about
	// this server, not even its version. See setup_host.go.
	if _, unknown := app.unknownHost(request); unknown {
		chrome.Version = ""
	}
	if settings.Initialized && (chrome.Viewer.GeneralUnlocked || authority.confirmed) {
		repositories, err := app.visibleRepositories(request)
		if err != nil {
			return webui.Chrome{}, fmt.Errorf("repository list read: %w", err)
		}
		query := strings.TrimSpace(request.URL.Query().Get("q"))
		nav := webui.Nav{
			Section: section, ActiveRepoID: activeRepository, Total: len(repositories), Query: query, Order: app.listOrder(writer, request),
			OverviewURL: "/", ActivityURL: "/activity", CodingURL: codingToolsPath, SettingsURL: "/settings", NewRepoURL: "/repositories/new", NewImportURL: "/repositories/new-import",
			AdminLoginURL: "/admin/login?next=" + url.QueryEscape(loginNext(request)),
		}
		if generalOK && settings.AccessMode == "password" {
			nav.LogoutURL = "/logout"
		}
		for _, repository := range repositories {
			if query != "" && !strings.Contains(strings.ToLower(repository.Name), strings.ToLower(query)) {
				continue
			}
			item := webui.NavRepository{
				ID: repository.ID, Name: repository.Name, URL: "/repositories/" + url.PathEscape(repository.Address), CountKnown: false,
			}
			item.LastActivity, _ = app.Repositories.CachedHeadDate(repository.ID)
			nav.Repositories = append(nav.Repositories, item)
		}
		// The dates come from snapshots already read, so building the sidebar
		// starts no Git process. The dashboard puts its own list in the same
		// order (see handleOverview).
		webui.OrderNav(nav.Repositories, nav.Order, lang)
		chrome.Nav = nav
	}
	fillAdminViewer(&chrome, authority)
	chrome.Notices = noticeFor(app.resultNotice(writer, request))
	return chrome, nil
}

// noticeRedirect ends an action by redirecting to target. When target names
// a result notice, the notice is also kept in a short-lived cookie, and the
// page shows the notice only while the two match (see resultNotice).
func (app *App) noticeRedirect(writer http.ResponseWriter, request *http.Request, target string, status int) {
	if parsed, err := url.Parse(target); err == nil {
		if notice := parsed.Query().Get("notice"); notice != "" {
			app.setCookie(writer, request, noticeCookie, notice, app.now().Add(noticeCookieMaxAge), true)
		}
	}
	http.Redirect(writer, request, target, status)
}

// resultNotice returns the result notice of the address when the action that
// produced it set the matching cookie, and clears the cookie so the notice
// is shown once. A crafted or reloaded address gives "".
func (app *App) resultNotice(writer http.ResponseWriter, request *http.Request) string {
	notice := request.URL.Query().Get("notice")
	if notice == "" {
		return ""
	}
	cookie, err := request.Cookie(noticeCookie)
	if err != nil || cookie.Value != notice {
		return ""
	}
	app.clearCookie(writer, request, noticeCookie, true)
	return notice
}

// setupSession returns the setup session named by the request's cookie, with
// the outcomes of cookieSession.
func (app *App) setupSession(request *http.Request) (state.Session, bool, error) {
	cookie, err := request.Cookie(setupCookie)
	if err != nil {
		return state.Session{}, false, nil
	}
	session, ok, err := app.Store.Session(request.Context(), cookie.Value, "setup", app.now())
	return endedSessionKept(request, session, ok, err)
}

func (app *App) language(writer http.ResponseWriter, request *http.Request) webui.Lang {
	if value, present := request.URL.Query()["lang"]; present {
		if len(value) == 1 {
			if lang, valid := webui.ParseLang(value[0]); valid {
				app.setCookie(writer, request, languageCookie, string(lang), app.now().Add(365*24*time.Hour), false)
				return lang
			}
		}
		return webui.DefaultLang
	}
	if cookie, err := request.Cookie(languageCookie); err == nil {
		if lang, valid := webui.ParseLang(cookie.Value); valid {
			return lang
		}
	}
	return webui.DefaultLang
}

// appearance reads the Light, Dark, or System choice. A valid appearance
// parameter, sent by the appearance links, is saved for later requests.
func (app *App) appearance(writer http.ResponseWriter, request *http.Request) webui.Appearance {
	if value, present := request.URL.Query()["appearance"]; present && len(value) == 1 {
		if appearance, valid := webui.ParseAppearance(value[0]); valid {
			app.setCookie(writer, request, appearanceCookie, string(appearance), app.now().Add(365*24*time.Hour), false)
			return appearance
		}
	}
	if cookie, err := request.Cookie(appearanceCookie); err == nil {
		if appearance, valid := webui.ParseAppearance(cookie.Value); valid {
			return appearance
		}
	}
	return webui.AppearanceSystem
}

// listOrder reads the repository list order. A valid order parameter, sent
// by the Sort form, is saved for later requests, like the appearance.
func (app *App) listOrder(writer http.ResponseWriter, request *http.Request) webui.ListOrder {
	if value, present := request.URL.Query()["order"]; present && len(value) == 1 {
		if order, valid := webui.ParseListOrder(value[0]); valid {
			app.setCookie(writer, request, orderCookie, string(order), app.now().Add(365*24*time.Hour), false)
			return order
		}
	}
	if cookie, err := request.Cookie(orderCookie); err == nil {
		if order, valid := webui.ParseListOrder(cookie.Value); valid {
			return order
		}
	}
	return webui.DefaultListOrder
}

// noticeFor turns a verified result notice into its message.
func noticeFor(notice string) []webui.Notice {
	switch notice {
	case "setup_completed":
		return []webui.Notice{webui.Success(webui.MsgSetupCompleted)}
	case "setup_file_remains":
		return []webui.Notice{webui.Success(webui.MsgSetupCompleted), {Kind: webui.NoticeWarning, Code: webui.MsgSetupFileRemains}}
	case "repository_created":
		return []webui.Notice{webui.Success(webui.MsgRepoCreated)}
	case "settings_saved", "tray_saved":
		return []webui.Notice{webui.Success(webui.MsgSettingsSaved)}
	case "network_saved":
		return []webui.Notice{webui.Success(webui.MsgNetSaved)}
	case "tailscale_on":
		return []webui.Notice{webui.Success(webui.MsgTSTurnedOn), webui.Info(webui.MsgTSFirstVisit)}
	case "tailscale_on_kept":
		return []webui.Notice{webui.Success(webui.MsgTSTurnedOn)}
	case "tailscale_off":
		return []webui.Notice{webui.Success(webui.MsgTSTurnedOff)}
	case "tailscale_moved":
		return []webui.Notice{{Kind: webui.NoticeWarning, Code: webui.MsgTSMoved}}
	case "access_password_saved":
		return []webui.Notice{webui.Success(webui.MsgSettingsAccessSaved)}
	case "access_enabled":
		return []webui.Notice{webui.Success(webui.MsgSettingsAccessEnabled)}
	case "access_changed":
		return []webui.Notice{webui.Success(webui.MsgSettingsAccessChanged)}
	case "access_disabled":
		return []webui.Notice{webui.Success(webui.MsgSettingsAccessDisabled)}
	case "admin_password_changed":
		return []webui.Notice{webui.Success(webui.MsgSettingsAdminChanged)}
	case "confirmation_saved":
		return []webui.Notice{webui.Success(webui.MsgConfirmSaved)}
	case "confirmation_off":
		return []webui.Notice{{Kind: webui.NoticeWarning, Code: webui.MsgConfirmTurnedOff}}
	case "session_saved":
		return []webui.Notice{webui.Success(webui.MsgSessionSaved)}
	case "initial_branch_saved":
		return []webui.Notice{webui.Success(webui.MsgInitialBranchSaved)}
	case "transfer_saved":
		return []webui.Notice{webui.Success(webui.MsgTransferSaved)}
	case "browse_saved":
		return []webui.Notice{webui.Success(webui.MsgBrowseSaved)}
	case "browse_looser":
		return []webui.Notice{webui.Success(webui.MsgBrowseSaved), {Kind: webui.NoticeWarning, Code: webui.MsgBrowseWarning}}
	case "maintenance_saved":
		return []webui.Notice{webui.Success(webui.MsgMaintenanceSaved)}
	case "maintenance_looser":
		return []webui.Notice{webui.Success(webui.MsgMaintenanceSaved), {Kind: webui.NoticeWarning, Code: webui.MsgMaintenanceWarning}}
	case "cleanup_off":
		return []webui.Notice{webui.Success(webui.MsgCleanupSavedOff)}
	case "cleanup_on":
		return []webui.Notice{webui.Success(webui.MsgCleanupSavedOn), {Kind: webui.NoticeWarning, Code: webui.MsgCleanupWarning}}
	case "transfer_looser":
		return []webui.Notice{{Kind: webui.NoticeWarning, Code: webui.MsgTransferSavedLooser}}
	case "check_logs_saved":
		return []webui.Notice{webui.Success(webui.MsgCheckLogsSaved)}
	case "kept_history_on":
		return []webui.Notice{webui.Success(webui.MsgKeptHistorySaved)}
	case "kept_history_off":
		return []webui.Notice{{Kind: webui.NoticeWarning, Code: webui.MsgKeptHistorySavedOff}}
	case "delete_name_on":
		return []webui.Notice{webui.Success(webui.MsgDeleteNameSaved)}
	case "delete_name_off":
		return []webui.Notice{{Kind: webui.NoticeWarning, Code: webui.MsgDeleteNameSavedOff}}
	case "login_limits_saved":
		return []webui.Notice{webui.Success(webui.MsgLoginLimitsSaved)}
	case "login_limits_looser":
		return []webui.Notice{{Kind: webui.NoticeWarning, Code: webui.MsgLoginLimitsSavedLooser}}
	case "cross_site_strict":
		return []webui.Notice{webui.Success(webui.MsgCrossSiteSaved)}
	case "cross_site_lax":
		return []webui.Notice{{Kind: webui.NoticeWarning, Code: webui.MsgCrossSiteSavedLax}}
	case "insecure_acknowledged":
		return []webui.Notice{webui.Success(webui.MsgSettingsAckDone)}
	case "logout":
		return []webui.Notice{webui.Success(webui.MsgLogoutDone)}
	case "admin_logout":
		return []webui.Notice{webui.Success(webui.MsgAdminEnded)}
	case "restore_success":
		return []webui.Notice{webui.Success(webui.MsgRestoreSuccess)}
	// Pull request results are not answered here. The pull request page
	// shows them only when its current state confirms them (see
	// pullRequestNotices), so a crafted link cannot report a merge that did
	// not happen.
	case "share_link_revoked":
		return []webui.Notice{webui.Success(webui.MsgShareRevokedNotice)}
	case "helper_credential_revoked":
		return []webui.Notice{webui.Success(webui.MsgHelperRevokedDone)}
	case "import_saved":
		return []webui.Notice{webui.Success(webui.MsgImportSaved)}
	case "import_refreshed":
		return []webui.Notice{webui.Success(webui.MsgImportRefreshed)}
	case "import_cancelled":
		return []webui.Notice{webui.Success(webui.MsgImportCancelled)}
	case "import_resolved":
		return []webui.Notice{webui.Success(webui.MsgImportResolved)}
	case "import_run_cancelled":
		return []webui.Notice{{Kind: webui.NoticeWarning, Code: webui.MsgImportRunCancelled}}
	case "import_cancel_none":
		return []webui.Notice{webui.Success(webui.MsgImportCancelNone)}
	case "import_credentials_saved":
		return []webui.Notice{webui.Success(webui.MsgImportCredentialsSaved)}
	case "import_credentials_cleared":
		return []webui.Notice{webui.Success(webui.MsgImportCredentialsCleared)}
	case "import_schedule_saved":
		return []webui.Notice{webui.Success(webui.MsgImportScheduleSaved)}
	case "import_started":
		return []webui.Notice{webui.Success(webui.MsgImportStarted)}
	case "import_failed":
		return []webui.Notice{webui.Error("", webui.MsgImportFailed)}
	// Configured checks. Every redirect this package issues has to resolve
	// here; a key with no case falls through to nil and the operator is told
	// nothing about what their submission did.
	case "check_policy_saved":
		return []webui.Notice{webui.Success(webui.MsgCCSaved)}
	case "check_policy_saved_enabled":
		return []webui.Notice{webui.Success(webui.MsgCCSavedEnabled)}
	case "checks_enabled":
		return []webui.Notice{webui.Success(webui.MsgCCEnabled)}
	case "check_policy_saved_turned_on":
		return []webui.Notice{webui.Success(webui.MsgCCSavedTurnedOn)}
	case "checks_disabled":
		return []webui.Notice{webui.Success(webui.MsgCCDisabled)}
	case "check_job_cancelled":
		return []webui.Notice{webui.Success(webui.MsgCCJobCancelled)}
	case "check_job_already_finished":
		// Not a success: the request was recorded, but the job had already
		// reached its own result and nothing was stopped or changed.
		return []webui.Notice{webui.Info(webui.MsgCCJobAlreadyFinished)}
	case "check_job_rerun":
		return []webui.Notice{webui.Success(webui.MsgCCJobRerunQueued)}
	case "check_job_rerun_existing":
		// Not a second success: nothing new was queued, and saying otherwise
		// would suggest a fresh run exists.
		return []webui.Notice{webui.Info(webui.MsgCCJobRerunExisting)}
	case "check_container_forgotten":
		return []webui.Notice{webui.Success(webui.MsgCCContainerForgotten)}
	case "runner_token_revoked":
		return []webui.Notice{webui.Success(webui.MsgRTRevokedDone)}
	case "repository_renamed":
		return []webui.Notice{webui.Success(webui.MsgRepoRenamed)}
	case "default_branch_saved":
		return []webui.Notice{webui.Success(webui.MsgRepoDefaultBranchSaved)}
	case "namespaces_saved":
		return []webui.Notice{webui.Success(webui.MsgNamespacesSaved)}
	case "namespaces_saved_unkept":
		return []webui.Notice{webui.Success(webui.MsgNamespacesSaved), {Kind: webui.NoticeWarning, Code: webui.MsgNamespacesUnkept}}
	case "history_saved":
		return []webui.Notice{webui.Success(webui.MsgRepoHistorySaved)}
	case "history_saved_kept_off":
		return []webui.Notice{webui.Success(webui.MsgRepoHistorySaved), {Kind: webui.NoticeWarning, Code: webui.MsgRepoHistoryKeptOff}}
	case "history_saved_protect_off":
		return []webui.Notice{webui.Success(webui.MsgRepoHistorySaved), {Kind: webui.NoticeWarning, Code: webui.MsgRepoHistoryProtectOff}}
	case "history_saved_both_off":
		return []webui.Notice{webui.Success(webui.MsgRepoHistorySaved), {Kind: webui.NoticeWarning, Code: webui.MsgRepoHistoryKeptOff}, {Kind: webui.NoticeWarning, Code: webui.MsgRepoHistoryProtectOff}}
	default:
		return nil
	}
}
