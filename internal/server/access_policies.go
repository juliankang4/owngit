package server

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"owngit/internal/state"
	"owngit/internal/webui"
)

// The access policies in Settings and the owner API: whether deleting a
// repository asks for its name, the login attempt limits, and whether a
// link from another site keeps the shared sign-in. The rules they change
// live where they apply: the delete page (repository_admin_browser.go), the
// password check (auth) and the general session cookie (setGeneralCookie).

// loginLimitsForm reads the login attempt limits a Settings form sent, or
// the notices that refuse them.
func loginLimitsForm(request *http.Request) (state.LoginLimits, []webui.Notice) {
	form := &limitsForm{request: request}
	attempts, err := strconv.Atoi(strings.TrimSpace(postValue(request, "login_attempts")))
	if err != nil || attempts < state.MinimumLoginAttempts || attempts > state.MaximumLoginAttempts {
		form.notices = append(form.notices, webui.Error("login_attempts", webui.MsgLoginLimitsInvalid))
	}
	limits := state.LoginLimits{
		Attempts: attempts,
		Window:   form.duration("login_window", state.MinimumLoginDuration, state.MaximumLoginDuration),
		Pause:    form.duration("login_pause", state.MinimumLoginDuration, state.MaximumLoginDuration),
	}
	return limits, form.notices
}

// loginLimitsJSON is the owner API's form of the login attempt limits. A
// PATCH may name some of them; the others keep their saved values.
type loginLimitsJSON = state.LoginLimitFields

func loginLimitsAPI(limits state.LoginLimits) *loginLimitsJSON {
	return &loginLimitsJSON{Attempts: &limits.Attempts, WindowSeconds: pointer(int64(limits.Window / time.Second)), PauseSeconds: pointer(int64(limits.Pause / time.Second))}
}

// setGeneralCookie gives this browser the general session cookie of a
// sign-in with the shared password, under the saved cross-site link choice.
// The administrator, setup and form token cookies are always Strict
// (setCookie).
func (app *App) setGeneralCookie(writer http.ResponseWriter, request *http.Request, token string, expires time.Time, links state.CrossSiteLinks) {
	sameSite := http.SameSiteStrictMode
	if links == state.CrossSiteLax {
		sameSite = http.SameSiteLaxMode
	}
	app.writeCookie(writer, request, cookieNameForScheme(request, generalCookie), token, expires, true, sameSite)
}

// renewGeneralCookie applies a newly saved cross-site link choice to this
// browser's shared sign-in at once, when it has one; other browsers get it
// at their next sign-in.
func (app *App) renewGeneralCookie(writer http.ResponseWriter, request *http.Request, links state.CrossSiteLinks) {
	session, ok, err := app.cookieSession(request, "general", generalCookie)
	if err != nil {
		logFailure(request, "session read", err)
	}
	if !ok {
		return
	}
	cookie, err := request.Cookie(cookieNameForScheme(request, generalCookie))
	if err == nil {
		app.setGeneralCookie(writer, request, cookie.Value, session.Expires, links)
	}
}
