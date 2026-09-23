package server

import (
	"context"
	"encoding/base64"
	"net/http"
	"net/url"
	"strings"
	"time"

	"owngit/internal/auth"
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
	next := url.QueryEscape(localNext(request.URL.RequestURI(), "/"))
	http.Redirect(writer, request, "/login?next="+next, http.StatusSeeOther)
	return state.Session{}, false
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

// browserAdminSession returns the administrator session on a page that was
// authorized with general authority. Unlike requireBrowserAdmin it never
// redirects, and it reports the selected session so a page can decide what to
// offer rather than what to allow.
func (app *App) browserAdminSession(request *http.Request) (state.Session, bool) {
	return app.cookieSession(request, "admin", adminCookie)
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
		Secure: request.TLS != nil, SameSite: http.SameSiteStrictMode,
	})
}

func (app *App) clearCookie(writer http.ResponseWriter, request *http.Request, name string, httpOnly bool) {
	http.SetCookie(writer, &http.Cookie{
		Name: name, Value: "", Path: "/", MaxAge: -1, Expires: time.Unix(1, 0),
		HttpOnly: httpOnly, Secure: request.TLS != nil, SameSite: http.SameSiteStrictMode,
	})
}

func (app *App) chrome(writer http.ResponseWriter, request *http.Request, section webui.NavSection, activeRepository, csrf string) (webui.Chrome, error) {
	settings, err := app.Store.Settings(request.Context())
	if err != nil {
		return webui.Chrome{}, err
	}
	lang := app.language(writer, request)
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
		Lang: lang, Now: app.now(), CurrentURL: request.URL.RequestURI(), CSRF: csrf, Version: app.Version,
		Viewer: webui.Viewer{
			AccessMode: accessMode, GeneralUnlocked: settings.AccessMode == "open" || generalOK,
			AdminConfirmed: adminOK, SetupComplete: settings.Initialized,
		},
		Connection: webui.Connection{Encrypted: request.TLS != nil, Host: request.Host, InsecureAcknowledged: settings.InsecureHTTPAccepted},
	}
	if adminOK {
		chrome.Viewer.AdminExpiresAt = admin.Expires
		chrome.Storage = webui.StorageInfo{Visible: true, Path: settings.RepositoryRoot}
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
			AdminLoginURL: "/admin/login?next=" + url.QueryEscape(request.URL.RequestURI()),
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
			nav.Repositories = append(nav.Repositories, webui.NavRepository{
				ID: repository.ID, Name: repository.Name, URL: "/repositories/" + url.PathEscape(repository.ID), CountKnown: false,
			})
		}
		chrome.Nav = nav
	}
	chrome.Notices = noticeFromQuery(request)
	return chrome, nil
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

func noticeFromQuery(request *http.Request) []webui.Notice {
	switch request.URL.Query().Get("notice") {
	case "setup_completed":
		return []webui.Notice{webui.Success(webui.MsgSetupCompleted)}
	case "repository_created":
		return []webui.Notice{webui.Success(webui.MsgRepoCreated)}
	case "settings_saved":
		return []webui.Notice{webui.Success(webui.MsgSettingsSaved)}
	case "logout":
		return []webui.Notice{webui.Success(webui.MsgLogoutDone)}
	case "admin_logout":
		return []webui.Notice{webui.Success(webui.MsgAdminEnded)}
	case "restore_success":
		return []webui.Notice{webui.Success(webui.MsgRestoreSuccess)}
	case "pull_request_created":
		return []webui.Notice{webui.Success(webui.MsgPRCreated)}
	case "review_requested":
		return []webui.Notice{webui.Success(webui.MsgPRReviewAsked)}
	case "review_skipped":
		return []webui.Notice{webui.Success(webui.MsgPRReviewSkipped)}
	case "pull_request_merged":
		return []webui.Notice{webui.Success(webui.MsgPRMerged)}
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
	default:
		return nil
	}
}

func (app *App) deleteSessionCookie(ctx context.Context, request *http.Request, kind, name string) {
	if cookie, err := request.Cookie(name); err == nil {
		_ = app.Store.DeleteSession(ctx, cookie.Value, kind)
	}
}
