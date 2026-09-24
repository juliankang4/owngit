package server

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"owngit/internal/auth"
	"owngit/internal/githttp"
	"owngit/internal/importsync"
	"owngit/internal/pullrequest"
	"owngit/internal/repository"
	"owngit/internal/state"
	"owngit/internal/webui"
)

const (
	generalCookie  = "owngit_general"
	adminCookie    = "owngit_admin"
	setupCookie    = "owngit_setup"
	preauthCookie  = "owngit_preauth"
	languageCookie = "owngit_lang"
)

type App struct {
	Store                   *state.Store
	Auth                    *auth.Manager
	Repositories            *repository.Manager
	PullRequests            *pullrequest.Service
	Imports                 *importsync.Service
	GitHTTP                 *githttp.Handler
	Renderer                *webui.Renderer
	Hosts                   *HostPolicy
	SuggestedRepositoryRoot string
	GitVersion              string
	HTTPBackendFound        bool
	// Version is the running application version. It comes from the single
	// version source and is never read from storage or a remote value.
	Version     string
	HTTPTimeout time.Duration
	// ImportRunTimeout is the deadline of an import run started by this
	// server. The request itself keeps ImportResponseMargin more, so a run
	// that reaches its deadline still returns its result. Zero uses the import
	// service default. It is not a second hard-coded limit.
	ImportRunTimeout time.Duration
	ActivityLimit    int
	Now              func() time.Time
	// requestObserver runs after the per-request deadline is installed. Tests
	// use it to observe that deadline. Production leaves it nil.
	requestObserver func(*http.Request)
	// OnSetupComplete runs once after first-run setup succeeds in this
	// process, so work that an initialized startup begins can begin now.
	OnSetupComplete func()
	// WakeChecks is an advisory nonblocking reconciliation signal.
	WakeChecks func(repositoryID string)
	// CheckRuntimeUnavailableCode and CheckRuntimeUnavailableReason expose a
	// sanitized owner-visible startup status. Empty code means available.
	CheckRuntimeUnavailableCode   string
	CheckRuntimeUnavailableReason string
	// activity caches activity observations by ref key. See activityCache.
	activity activityCache
}

func (app *App) Handler() http.Handler {
	return app.Hosts.Middleware(http.HandlerFunc(app.serveHTTP))
}

func (app *App) AuthorizeGit(request *http.Request) bool {
	settings, err := app.Store.Settings(request.Context())
	if err != nil || !settings.Initialized {
		return false
	}
	if settings.AccessMode == "open" {
		return true
	}
	_, password, ok := request.BasicAuth()
	return ok && app.Auth.VerifyCredential(request.Context(), "general", password, request.RemoteAddr) == nil
}

// ImportResponseMargin is how long an import run request outlives the run's
// own deadline. A run that reaches its deadline still records its outcome and
// delivers the result to the client inside this margin.
const ImportResponseMargin = time.Minute

// ImportRunRequestTimeout is the request and response deadline for an import
// run whose own deadline is runTimeout.
func ImportRunRequestTimeout(runTimeout time.Duration) time.Duration {
	return runTimeout + ImportResponseMargin
}

// importRunTimeout is the run deadline the service applies to runs started by
// this server.
func (app *App) importRunTimeout() time.Duration {
	if app.ImportRunTimeout > 0 {
		return app.ImportRunTimeout
	}
	return importsync.DefaultLimits().RunTimeout
}

// importRunLimits pins the run deadline so the run ends before its request.
func (app *App) importRunLimits() importsync.Limits {
	return importsync.Limits{RunTimeout: app.importRunTimeout()}
}

// replyReserve is the most time an ordinary request keeps after its work
// deadline for writing the response.
const replyReserve = 5 * time.Second

// requestTimeout returns how long a request may take and how much of that
// time is reserved after the handler's work deadline for writing the
// response. Work that runs out of time, such as Git on slow storage, then
// still produces an error page instead of an empty reply. An import run keeps
// ImportResponseMargin beyond its own run deadline instead.
func (app *App) requestTimeout(request *http.Request) (time.Duration, time.Duration) {
	if importRunRequest(request) {
		return ImportRunRequestTimeout(app.importRunTimeout()), 0
	}
	timeout := 30 * time.Second
	if app.HTTPTimeout > 0 {
		timeout = app.HTTPTimeout
	}
	return timeout, min(replyReserve, timeout/4)
}

func importRunRequest(request *http.Request) bool {
	if request == nil || request.Method != http.MethodPost {
		return false
	}
	path := request.URL.Path
	if path == "/repositories/new-import" {
		return true
	}
	const apiPrefix = "/api/v1/repositories/"
	if strings.HasPrefix(path, apiPrefix) && strings.HasSuffix(path, "/import/run") {
		id := strings.TrimSuffix(strings.TrimPrefix(path, apiPrefix), "/import/run")
		return id != "" && !strings.Contains(id, "/")
	}
	const repoPrefix = "/repositories/"
	if strings.HasPrefix(path, repoPrefix) && strings.HasSuffix(path, "/import") {
		id := strings.TrimSuffix(strings.TrimPrefix(path, repoPrefix), "/import")
		if id == "" || strings.Contains(id, "/") {
			return false
		}
		return peekFormAction(request) == webui.ActionImportRefresh
	}
	return false
}

func peekFormAction(request *http.Request) string {
	if request.Body == nil {
		return ""
	}
	content, err := io.ReadAll(io.LimitReader(request.Body, 1<<20+1))
	request.Body = io.NopCloser(bytes.NewReader(content))
	request.GetBody = func() (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(content)), nil
	}
	if err != nil || len(content) > 1<<20 {
		return ""
	}
	values, err := url.ParseQuery(string(content))
	if err != nil {
		return ""
	}
	return values.Get("action")
}

func (app *App) serveHTTP(writer http.ResponseWriter, request *http.Request) {
	if strings.HasPrefix(request.URL.Path, "/git/") {
		app.GitHTTP.ServeHTTP(writer, request)
		return
	}

	timeout, reserve := app.requestTimeout(request)
	deadline := time.Now().Add(timeout)
	requestContext, cancel := context.WithDeadline(request.Context(), deadline.Add(-reserve))
	defer cancel()
	request = request.WithContext(requestContext)
	controller := http.NewResponseController(writer)
	_ = controller.SetReadDeadline(deadline)
	_ = controller.SetWriteDeadline(deadline)
	if app.requestObserver != nil {
		app.requestObserver(request)
	}
	defer func() {
		if time.Now().Before(deadline) {
			_ = controller.SetReadDeadline(time.Time{})
			_ = controller.SetWriteDeadline(time.Time{})
			return
		}
		_ = request.Body.Close()
	}()
	if strings.HasPrefix(request.URL.Path, "/assets/") {
		app.Renderer.Assets().ServeHTTP(writer, cloneWithPath(request, strings.TrimPrefix(request.URL.Path, "/assets")))
		return
	}

	settings, err := app.Store.Settings(request.Context())
	if err != nil {
		if strings.HasPrefix(request.URL.Path, "/api/") {
			writeAPIError(writer, http.StatusServiceUnavailable, "state_unavailable", "OwnGit state is unavailable.", nil)
		} else {
			app.writePlainError(writer, http.StatusServiceUnavailable)
		}
		return
	}
	if strings.HasPrefix(request.URL.Path, "/api/") {
		app.handleAPI(writer, request, settings)
		return
	}
	if !settings.Initialized && request.URL.Path != "/setup" && request.URL.Path != "/setup/redeem" {
		http.Redirect(writer, request, "/setup", http.StatusSeeOther)
		return
	}
	if settings.Initialized && (request.URL.Path == "/setup/redeem" || (request.URL.Path == "/setup" && request.Method != http.MethodGet)) {
		app.renderError(writer, request, http.StatusConflict, webui.MsgSetupAlreadyDone, "")
		return
	}

	switch {
	case request.URL.Path == "/" && request.Method == http.MethodGet:
		app.handleOverview(writer, request, settings)
	case request.URL.Path == "/setup" && request.Method == http.MethodGet:
		app.handleSetupGet(writer, request, settings)
	case request.URL.Path == "/setup" && request.Method == http.MethodPost:
		app.handleSetupPost(writer, request)
	case request.URL.Path == "/setup/redeem" && request.Method == http.MethodPost:
		app.handleSetupRedeem(writer, request)
	case request.URL.Path == "/login" && request.Method == http.MethodGet:
		app.handleLoginGet(writer, request, settings, webui.AuthGeneral)
	case request.URL.Path == "/login" && request.Method == http.MethodPost:
		app.handleLoginPost(writer, request, settings, webui.AuthGeneral)
	case request.URL.Path == "/logout" && request.Method == http.MethodPost:
		app.handleLogout(writer, request, webui.AuthGeneral)
	case request.URL.Path == "/admin/login" && request.Method == http.MethodGet:
		app.handleLoginGet(writer, request, settings, webui.AuthAdmin)
	case request.URL.Path == "/admin/login" && request.Method == http.MethodPost:
		app.handleLoginPost(writer, request, settings, webui.AuthAdmin)
	case request.URL.Path == "/admin/logout" && request.Method == http.MethodPost:
		app.handleLogout(writer, request, webui.AuthAdmin)
	case request.URL.Path == "/settings" && request.Method == http.MethodGet:
		app.handleSettingsGet(writer, request, settings)
	case request.URL.Path == "/settings" && request.Method == http.MethodPost:
		app.handleSettingsPost(writer, request, settings)
	case request.URL.Path == "/repositories/new" && request.Method == http.MethodGet:
		app.handleNewRepositoryGet(writer, request, settings, "", "", nil)
	case request.URL.Path == "/repositories/new-import" && (request.Method == http.MethodGet || request.Method == http.MethodPost):
		app.handleNewImport(writer, request, settings)
	case request.URL.Path == "/repositories" && request.Method == http.MethodPost:
		app.handleCreateRepository(writer, request, settings)
	case request.URL.Path == "/activity" && request.Method == http.MethodGet:
		app.handleActivity(writer, request, settings)
	case strings.HasPrefix(request.URL.Path, "/repositories/") && (request.Method == http.MethodGet || request.Method == http.MethodPost):
		app.handleRepositoryRoute(writer, request, settings)
	default:
		app.renderError(writer, request, http.StatusNotFound, webui.MsgErrNotFound, request.URL.Path)
	}
}

func (app *App) render(writer http.ResponseWriter, status int, page webui.Page) {
	var output bytes.Buffer
	if err := app.Renderer.Render(&output, page); err != nil {
		app.writePlainError(writer, http.StatusInternalServerError)
		return
	}
	writer.Header().Set("Content-Type", "text/html; charset=utf-8")
	writer.Header().Set("Cache-Control", "no-store")
	writer.WriteHeader(status)
	_, _ = writer.Write(output.Bytes())
}

func (app *App) renderError(writer http.ResponseWriter, request *http.Request, status int, code webui.MessageCode, detail string) {
	chrome, _ := app.chrome(writer, request, webui.SectionNone, "", "")
	app.render(writer, status, webui.ErrorPage{Chrome: chrome, Status: status, Code: code, Detail: detail, RetryURL: "/"})
}

func (app *App) writePlainError(writer http.ResponseWriter, status int) {
	writer.Header().Set("Content-Type", "text/plain; charset=utf-8")
	writer.Header().Set("Cache-Control", "no-store")
	writer.WriteHeader(status)
	_, _ = writer.Write([]byte(http.StatusText(status) + "\n"))
}

func (app *App) wakeChecks(repositoryID string) {
	if app.WakeChecks != nil {
		app.WakeChecks(repositoryID)
	}
}

func (app *App) now() time.Time {
	if app.Now != nil {
		return app.Now()
	}
	return time.Now()
}

func cloneWithPath(request *http.Request, path string) *http.Request {
	clone := request.Clone(request.Context())
	urlCopy := *request.URL
	urlCopy.Path = path
	clone.URL = &urlCopy
	return clone
}

func localNext(value, fallback string) string {
	if value == "" {
		return fallback
	}
	if !strings.HasPrefix(value, "/") || strings.HasPrefix(value, "//") || strings.Contains(value, "\\") {
		return fallback
	}
	// Browsers drop tab, CR and LF inside a URL, so "/\t/host" would become
	// "//host" and leave this server. Every control character is refused.
	for _, character := range value {
		if character < 0x20 || character == 0x7f {
			return fallback
		}
	}
	return value
}

func parseForm(writer http.ResponseWriter, request *http.Request) bool {
	if !requireFormOrigin(request) {
		http.Error(writer, "request origin is required", http.StatusForbidden)
		return false
	}
	request.Body = http.MaxBytesReader(writer, request.Body, 1<<20)
	if err := request.ParseForm(); err != nil {
		http.Error(writer, "invalid form", http.StatusBadRequest)
		return false
	}
	return true
}

func postValue(request *http.Request, name string) string {
	return request.PostForm.Get(name)
}

func formChecked(value string) bool {
	return value == "on" || value == "1"
}

func constantEqual(left, right string) bool {
	if len(left) != len(right) {
		return false
	}
	var different byte
	for index := range left {
		different |= left[index] ^ right[index]
	}
	return different == 0
}

func firstError(errs ...error) error {
	for _, err := range errs {
		if err != nil {
			return err
		}
	}
	return nil
}
