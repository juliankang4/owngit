package server

import (
	"context"
	"fmt"
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
type loginLimitsJSON struct {
	Attempts      *int   `json:"attempts,omitempty"`
	WindowSeconds *int64 `json:"window_seconds,omitempty"`
	PauseSeconds  *int64 `json:"pause_seconds,omitempty"`
}

func loginLimitsAPI(limits state.LoginLimits) *loginLimitsJSON {
	return &loginLimitsJSON{Attempts: &limits.Attempts, WindowSeconds: pointer(int64(limits.Window / time.Second)), PauseSeconds: pointer(int64(limits.Pause / time.Second))}
}

// changedLoginLimits applies a PATCH of the login attempt limits to the
// saved ones. problem says why the result is refused; err is a saved value
// that cannot be read, which the owner replaces by naming all three.
func (app *App) changedLoginLimits(ctx context.Context, change loginLimitsJSON) (limits state.LoginLimits, problem string, err error) {
	if change.Attempts == nil && change.WindowSeconds == nil && change.PauseSeconds == nil {
		return limits, "login_limits must name attempts, window_seconds, pause_seconds or several of them.", nil
	}
	if change.Attempts == nil || change.WindowSeconds == nil || change.PauseSeconds == nil {
		if limits, err = app.Store.LoginLimits(ctx); err != nil {
			return limits, "", err
		}
	}
	if change.Attempts != nil {
		limits.Attempts = *change.Attempts
	}
	for _, field := range []struct {
		name    string
		seconds *int64
		target  *time.Duration
	}{{"window_seconds", change.WindowSeconds, &limits.Window}, {"pause_seconds", change.PauseSeconds, &limits.Pause}} {
		if field.seconds == nil {
			continue
		}
		if *field.target, err = state.LoginSeconds(*field.seconds); err != nil {
			return limits, fmt.Sprintf("login_limits: %s is from %d to %d.", field.name, int64(state.MinimumLoginDuration/time.Second), int64(state.MaximumLoginDuration/time.Second)), nil
		}
	}
	if err := limits.Validate(); err != nil {
		return limits, "login_limits: " + err.Error() + ".", nil
	}
	return limits, "", nil
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
	app.writeCookie(writer, request, generalCookie, token, expires, true, sameSite)
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
	cookie, err := request.Cookie(generalCookie)
	if err == nil {
		app.setGeneralCookie(writer, request, cookie.Value, session.Expires, links)
	}
}
