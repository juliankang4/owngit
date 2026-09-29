package server

import (
	"errors"
	"net/http"
	"net/url"
	"time"

	"owngit/internal/auth"
	"owngit/internal/requestctx"
	"owngit/internal/state"
	"owngit/internal/webui"
)

// Administrator confirmation in the browser.
//
// Every administrator page passes requireAdminPage, and every administrator
// change confirmAdmin. Both follow adminAuthority, which alone decides what
// this browser may do as administrator under the saved choice
// (state.AdminConfirmation, "Ask for the administrator password" in
// Access):
//
//   - The administrator session (adminCookie) is this browser's
//     confirmation. It starts when the administrator password is typed, on
//     the administrator sign-in page or in a form, and lasts the chosen
//     window from then; opening pages never extends it. It ends when it
//     expires, with End, when this browser signs out, and when the
//     administrator password is changed or reset. A shorter choice
//     shortens the sessions browsers hold already.
//   - Every time: the session only opens the administrator pages, for
//     auth.Manager.AdminSessionLife. Each change asks for the password.
//   - Do not ask: whoever may use the dashboard may open the administrator
//     pages and make changes without the password.
//
// This is browser authority only. The administrator API never reads it
// (authorizeAdminAPI), and Do not ask does not apply to the API. CSRF,
// Host and Origin checks apply as before.

// adminAuthority is what this browser may do as administrator.
type adminAuthority struct {
	// choice is the saved choice, or Every time when the saved value is
	// one this build does not know (known false).
	choice state.AdminConfirmation
	known  bool
	// session is this browser's administrator session, when confirmed.
	session   state.Session
	confirmed bool
}

// remembers reports whether this browser's typed password still stands for
// a change.
func (a adminAuthority) remembers() bool { return a.confirmed && a.choice.Window() > 0 }

// checkOff reports whether Do not ask is on.
func (a adminAuthority) checkOff() bool { return a.choice == state.ConfirmNever }

// changesFreely reports whether a change needs no password now.
func (a adminAuthority) changesFreely() bool { return a.remembers() || a.checkOff() }

// adminAuthority reads the saved choice and this browser's administrator
// session.
func (app *App) adminAuthority(writer http.ResponseWriter, request *http.Request) (adminAuthority, error) {
	choice, known, err := app.Store.AdminConfirmation(request.Context())
	if err != nil {
		return adminAuthority{}, err
	}
	session, ok, err := app.Auth.ValidateSession(request.Context(), heldCookie(writer, request, adminCookie), "admin")
	session, ok, err = endedSessionKept(request, session, ok, err)
	if err != nil {
		return adminAuthority{}, err
	}
	return adminAuthority{choice: choice, known: known, session: session, confirmed: ok}, nil
}

// heldCookie is the value of the cookie named name that the browser holds
// once this response arrives: the one the response sets, when it sets one
// ("" when it clears it), otherwise the one the request sent.
func heldCookie(writer http.ResponseWriter, request *http.Request, name string) string {
	value, set := "", false
	for _, line := range writer.Header().Values("Set-Cookie") {
		if cookie, err := http.ParseSetCookie(line); err == nil && cookie.Name == name {
			value, set = cookie.Value, true
		}
	}
	if !set {
		if cookie, err := request.Cookie(name); err == nil {
			value = cookie.Value
		}
	}
	return value
}

// requireAdminPage lets the request open an administrator page, or answers
// it: with the administrator sign-in, or with the general sign-in when Do
// not ask is on and this browser has not signed in. It returns the session
// whose CSRF token the page's forms carry.
func (app *App) requireAdminPage(writer http.ResponseWriter, request *http.Request) (state.Session, bool) {
	writer.Header().Set("Cache-Control", "no-store")
	authority, err := app.adminAuthority(writer, request)
	if err != nil {
		app.answerUnavailable(writer, request, "administrator confirmation read", err)
		return state.Session{}, false
	}
	if authority.confirmed {
		return authority.session, true
	}
	if authority.checkOff() {
		settings, err := app.Store.Settings(request.Context())
		if err != nil {
			app.answerUnavailable(writer, request, "settings read", err)
			return state.Session{}, false
		}
		return app.requireGeneral(writer, request, settings)
	}
	http.Redirect(writer, request, "/admin/login?next="+url.QueryEscape(loginNext(request)), http.StatusSeeOther)
	return state.Session{}, false
}

// errAdminPasswordMissing refuses a change that needs the administrator
// password when none was typed. It is not a wrong password and does not
// count toward the limit.
var errAdminPasswordMissing = errors.New("administrator password missing")

// confirmAdmin decides whether this request may make an administrator
// change, from the "admin_password" field when the change needs it. always
// asks for the password even when this browser is remembered or Do not ask
// is on; it is for changing the administrator password and for turning Do
// not ask on.
//
// It returns the password and version when this request verified them, and
// an empty password when the change needed none. A verified password starts
// this browser's remembered confirmation under a remembering choice, unless
// always; chrome, when
// given, then shows it. An error is errAdminPasswordMissing, an
// authentication error of auth.Manager.VerifyCredential, or a failure to
// decide; adminPasswordNotice describes each.
type adminPasswordProof struct {
	password string
	version  int64
}

func (app *App) confirmAdmin(writer http.ResponseWriter, request *http.Request, chrome *webui.Chrome, always bool) (adminPasswordProof, error) {
	// A typed password confirms a change whatever the choice and this
	// browser's session are, so a failure to read them decides only a
	// change that typed none.
	authority, readErr := app.adminAuthority(writer, request)
	if readErr == nil && !always && authority.changesFreely() {
		return adminPasswordProof{}, nil
	}
	password := postValue(request, "admin_password")
	if password == "" {
		if readErr != nil {
			return adminPasswordProof{}, readErr
		}
		return adminPasswordProof{}, errAdminPasswordMissing
	}
	version, err := app.Auth.VerifyCredentialVersion(request.Context(), "admin", password, requestctx.Of(request).ClientAddress)
	if err != nil {
		return adminPasswordProof{}, err
	}
	if err := app.Auth.ConfirmCredentialVersion(request.Context(), "admin", version); err != nil {
		return adminPasswordProof{}, err
	}
	if readErr == nil && !always && authority.choice.Window() > 0 {
		if err := app.rememberAdmin(writer, request, chrome, version); err != nil {
			if errors.Is(err, auth.ErrInvalidCredentials) {
				return adminPasswordProof{}, err
			}
			// A session write failure need not block a change confirmed with
			// the current password, but an unreadable revision cannot confirm it.
			if checkErr := app.Auth.ConfirmCredentialVersion(request.Context(), "admin", version); checkErr != nil {
				return adminPasswordProof{}, checkErr
			}
			logFailure(request, "administrator confirmation start", err)
		}
	}
	return adminPasswordProof{password: password, version: version}, nil
}

// rememberAdmin starts this browser's remembered confirmation after this
// request verified the administrator password, in place of the
// administrator session the browser held, which may have been only a page
// session. chrome, when given, then shows it. The caller handles a failed
// insertion rather than treating it as a current administrator session.
func (app *App) rememberAdmin(writer http.ResponseWriter, request *http.Request, chrome *webui.Chrome, version int64) error {
	session, err := app.Auth.StartAdminSession(request.Context(), heldCookie(writer, request, adminCookie), version)
	if err != nil {
		return err
	}
	app.setCookie(writer, request, adminCookie, session.Token, session.Expires, true)
	if chrome != nil {
		if authority, err := app.adminAuthority(writer, request); err != nil {
			logFailure(request, "administrator confirmation read", err)
		} else {
			fillAdminViewer(chrome, authority)
		}
	}
	return nil
}

// fillAdminViewer puts this browser's administrator state into chrome:
// whether it may open the administrator pages, whether a change asks for
// the password, and whether Do not ask is on. It relies on
// chrome.Viewer.GeneralUnlocked and on the sidebar, which chrome sets
// first.
func fillAdminViewer(chrome *webui.Chrome, authority adminAuthority) {
	viewer := &chrome.Viewer
	viewer.AdminChoice = string(authority.choice)
	viewer.AdminChoiceUnknown = !authority.known
	viewer.AdminConfirmed = authority.confirmed || (authority.checkOff() && viewer.GeneralUnlocked)
	viewer.AdminRemembered = authority.remembers()
	viewer.AdminAsks = !authority.changesFreely()
	viewer.AdminCheckOff = authority.checkOff() && (viewer.GeneralUnlocked || authority.confirmed)
	viewer.AdminExpiresAt = time.Time{}
	chrome.Nav.AdminLogoutURL = ""
	if authority.confirmed {
		viewer.AdminExpiresAt = authority.session.Expires
		// End ends this browser's confirmation. The sidebar that offers it
		// exists only for a viewer who may see the dashboard, and while the
		// check is off the confirmation decides nothing, so it is not
		// offered then.
		if chrome.Nav.OverviewURL != "" && !authority.checkOff() {
			chrome.Nav.AdminLogoutURL = "/admin/logout"
		}
	}
}
