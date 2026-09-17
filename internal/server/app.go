package server

import (
	"bytes"
	"context"
	"net/http"
	"strings"
	"time"

	"owngit/internal/auth"
	"owngit/internal/githttp"
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
	GitHTTP                 *githttp.Handler
	Renderer                *webui.Renderer
	Hosts                   *HostPolicy
	SuggestedRepositoryRoot string
	GitVersion              string
	HTTPBackendFound        bool
	HTTPTimeout             time.Duration
	Now                     func() time.Time
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

func (app *App) serveHTTP(writer http.ResponseWriter, request *http.Request) {
	if strings.HasPrefix(request.URL.Path, "/git/") {
		app.GitHTTP.ServeHTTP(writer, request)
		return
	}

	timeout := app.HTTPTimeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	deadline := time.Now().Add(timeout)
	requestContext, cancel := context.WithDeadline(request.Context(), deadline)
	defer cancel()
	request = request.WithContext(requestContext)
	controller := http.NewResponseController(writer)
	_ = controller.SetReadDeadline(deadline)
	_ = controller.SetWriteDeadline(deadline)
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
		app.writePlainError(writer, http.StatusServiceUnavailable)
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
	case request.URL.Path == "/repositories" && request.Method == http.MethodPost:
		app.handleCreateRepository(writer, request, settings)
	case request.URL.Path == "/activity" && request.Method == http.MethodGet:
		app.handleActivity(writer, request, settings)
	case strings.HasPrefix(request.URL.Path, "/repositories/") && request.Method == http.MethodGet:
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
	if !strings.HasPrefix(value, "/") || strings.HasPrefix(value, "//") || strings.ContainsAny(value, "\\\r\n") {
		return fallback
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
