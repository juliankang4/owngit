package server

import (
	"context"
	"encoding/base64"
	"net/http"
	"net/url"
	"sort"
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
		token, err := auth.RandomToken(32)
		if err != nil {
			app.writePlainError(writer, http.StatusInternalServerError)
			return state.Session{}, false
		}
		app.setCookie(writer, request, generalCookie, token, expires, true)
		return state.Session{Kind: "general", CSRF: token, Version: settings.AccessSessionVersion, Expires: expires}, true
	}
	if session, ok := app.cookieSession(request, "general", generalCookie); ok {
		return session, true
	}
	http.Redirect(writer, request, "/login?next="+url.QueryEscape(loginNext(request)), http.StatusSeeOther)
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

func (app *App) cookieSession(request *http.Request, kind, cookieName string) (state.Session, bool) {
	cookie, err := request.Cookie(cookieName)
	if err != nil || cookie.Value == "" {
		return state.Session{}, false
	}
	session, ok, err := app.Auth.ValidateSession(request.Context(), cookie.Value, kind)
	if err != nil || !ok {
		return state.Session{}, false
	}
	return session, true
}

func (app *App) validCSRF(request *http.Request, submitted string) bool {
	if submitted == "" {
		return false
	}
	for _, candidate := range []struct{ kind, cookie string }{
		{"setup", setupCookie}, {"admin", adminCookie}, {"general", generalCookie},
	} {
		if candidate.kind == "setup" {
			cookie, err := request.Cookie(candidate.cookie)
			if err != nil {
				continue
			}
			session, ok, err := app.Store.Session(request.Context(), cookie.Value, "setup", app.now())
			if err == nil && ok && constantEqual(session.CSRF, submitted) {
				return true
			}
			continue
		}
		if session, ok := app.cookieSession(request, candidate.kind, candidate.cookie); ok && constantEqual(session.CSRF, submitted) {
			return true
		}
	}
	settings, err := app.Store.Settings(request.Context())
	if err != nil || settings.AccessMode != "open" {
		return false
	}
	cookie, err := request.Cookie(generalCookie)
	return err == nil && validOpenToken(cookie.Value) && constantEqual(cookie.Value, submitted)
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
	token, err := auth.RandomToken(32)
	if err != nil {
		return ""
	}
	app.setCookie(writer, request, preauthCookie, token, app.now().Add(20*time.Minute), true)
	return token
}

func (app *App) validPreauthCSRF(request *http.Request, submitted string) bool {
	cookie, err := request.Cookie(preauthCookie)
	return err == nil && submitted != "" && constantEqual(cookie.Value, submitted)
}

func (app *App) setCookie(writer http.ResponseWriter, request *http.Request, name, value string, expires time.Time, httpOnly bool) {
	http.SetCookie(writer, &http.Cookie{
		Name: name, Value: value, Path: "/", Expires: expires,
		MaxAge: int(expires.Sub(app.now()).Seconds()), HttpOnly: httpOnly,
		Secure: requestctx.Of(request).Secure(), SameSite: http.SameSiteStrictMode,
	})
}

func (app *App) clearCookie(writer http.ResponseWriter, request *http.Request, name string, httpOnly bool) {
	http.SetCookie(writer, &http.Cookie{
		Name: name, Value: "", Path: "/", MaxAge: -1, Expires: time.Unix(1, 0),
		HttpOnly: httpOnly, Secure: requestctx.Of(request).Secure(), SameSite: http.SameSiteStrictMode,
	})
}

func (app *App) chrome(writer http.ResponseWriter, request *http.Request, section webui.NavSection, activeRepository, csrf string) (webui.Chrome, error) {
	settings, err := app.Store.Settings(request.Context())
	if err != nil {
		return webui.Chrome{}, err
	}
	lang := app.language(writer, request)
	appearance := app.appearance(writer, request)
	general, generalOK := app.cookieSession(request, "general", generalCookie)
	admin, adminOK := app.cookieSession(request, "admin", adminCookie)
	if csrf == "" {
		if adminOK {
			csrf = admin.CSRF
		} else if generalOK {
			csrf = general.CSRF
		} else if setup, ok := app.setupSession(request); ok {
			csrf = setup.CSRF
		}
	}
	accessMode := webui.AccessOpen
	if settings.AccessMode == "password" {
		accessMode = webui.AccessPassword
	}
	chrome := webui.Chrome{
		Lang: lang, Appearance: appearance, Now: app.now(), CurrentURL: request.URL.RequestURI(), CSRF: csrf, Version: app.Version,
		Viewer: webui.Viewer{
			AccessMode: accessMode, GeneralUnlocked: settings.AccessMode == "open" || generalOK,
			AdminConfirmed: adminOK, SetupComplete: settings.Initialized,
		},
		Connection: webui.Connection{Encrypted: requestctx.Of(request).Secure(), Host: requestctx.Of(request).Host, InsecureAcknowledged: settings.InsecureHTTPAccepted},
	}
	if adminOK {
		chrome.Viewer.AdminExpiresAt = admin.Expires
	}
	// A Host admitted only to redeem the setup link learns nothing about
	// this server, not even its version. See setup_host.go.
	if _, unknown := app.unknownHost(request); unknown {
		chrome.Version = ""
	}
	if settings.Initialized && (chrome.Viewer.GeneralUnlocked || adminOK) {
		repositories, err := app.Store.Repositories(request.Context())
		if err != nil {
			return webui.Chrome{}, err
		}
		query := strings.TrimSpace(request.URL.Query().Get("q"))
		nav := webui.Nav{
			Section: section, ActiveRepoID: activeRepository, Total: len(repositories), Query: query,
			OverviewURL: "/", ActivityURL: "/activity", SettingsURL: "/settings", NewRepoURL: "/repositories/new", NewImportURL: "/repositories/new-import",
			AdminLoginURL: "/admin/login?next=" + url.QueryEscape(loginNext(request)),
		}
		if generalOK && settings.AccessMode == "password" {
			nav.LogoutURL = "/logout"
		}
		if adminOK {
			nav.AdminLogoutURL = "/admin/logout"
		}
		for _, repository := range repositories {
			if query != "" && !strings.Contains(strings.ToLower(repository.Name), strings.ToLower(query)) {
				continue
			}
			item := webui.NavRepository{
				ID: repository.ID, Name: repository.Name, URL: "/repositories/" + url.PathEscape(repository.ID), CountKnown: false,
			}
			if app.Repositories != nil {
				item.LastActivity, _ = app.Repositories.CachedHeadDate(repository.ID)
			}
			nav.Repositories = append(nav.Repositories, item)
		}
		// Most recently active first. The dates come from snapshots already
		// read, so building the sidebar starts no Git process; a repository
		// without a known date keeps the store's order after the dated ones.
		sort.SliceStable(nav.Repositories, func(left, right int) bool {
			a, b := nav.Repositories[left].LastActivity, nav.Repositories[right].LastActivity
			if a.IsZero() != b.IsZero() {
				return !a.IsZero()
			}
			return a.After(b)
		})
		chrome.Nav = nav
	}
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

func (app *App) setupSession(request *http.Request) (state.Session, bool) {
	cookie, err := request.Cookie(setupCookie)
	if err != nil {
		return state.Session{}, false
	}
	session, ok, err := app.Store.Session(request.Context(), cookie.Value, "setup", app.now())
	return session, err == nil && ok
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

// noticeFor turns a verified result notice into its message.
func noticeFor(notice string) []webui.Notice {
	switch notice {
	case "setup_completed":
		return []webui.Notice{webui.Success(webui.MsgSetupCompleted)}
	case "repository_created":
		return []webui.Notice{webui.Success(webui.MsgRepoCreated)}
	case "settings_saved":
		return []webui.Notice{webui.Success(webui.MsgSettingsSaved)}
	case "access_password_saved":
		return []webui.Notice{webui.Success(webui.MsgSettingsAccessSaved)}
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
	case "runner_token_revoked":
		return []webui.Notice{webui.Success(webui.MsgRTRevokedDone)}
	case "default_branch_saved":
		return []webui.Notice{webui.Success(webui.MsgRepoDefaultBranchSaved)}
	default:
		return nil
	}
}

func (app *App) deleteSessionCookie(ctx context.Context, request *http.Request, kind, name string) {
	if cookie, err := request.Cookie(name); err == nil {
		_ = app.Store.DeleteSession(ctx, cookie.Value, kind)
	}
}
