package server

import (
	"net/http"
	"time"

	"owngit/internal/releasecheck"
	"owngit/internal/state"
	"owngit/internal/webui"
)

const releaseDismissPath = "/release-notice/dismiss"

// releaseNotice returns the dashboard notice when the check may run, found a
// newer release, and this browser has not dismissed that version.
func (app *App) releaseNotice(request *http.Request, settings state.Settings) *webui.ReleaseNotice {
	if app.Releases == nil || !settings.UpdateCheck {
		return nil
	}
	release, newer := app.Releases.Newer()
	if !newer {
		return nil
	}
	if cookie, err := request.Cookie(releaseDismissCookie); err == nil && cookie.Value == release.Version {
		return nil
	}
	return &webui.ReleaseNotice{
		Version: release.Version, Current: app.Version, NotesURL: release.NotesURL,
		GuideURL: releasecheck.UpdateGuideURL, DismissURL: releaseDismissPath,
	}
}

// handleReleaseDismiss remembers in this browser that the notice for one
// version was dismissed. A later version has a different value, so its notice
// shows again.
func (app *App) handleReleaseDismiss(writer http.ResponseWriter, request *http.Request, settings state.Settings) {
	if _, ok := app.requireGeneral(writer, request, settings); !ok {
		return
	}
	if !parseForm(writer, request) {
		return
	}
	if !app.validCSRF(request, postValue(request, "csrf")) {
		app.renderError(writer, request, http.StatusForbidden, webui.MsgErrCSRF, "")
		return
	}
	version := postValue(request, "version")
	if !releasecheck.ValidVersion(version) {
		app.renderError(writer, request, http.StatusBadRequest, webui.MsgErrGeneric, "")
		return
	}
	app.setCookie(writer, request, releaseDismissCookie, version, app.now().Add(365*24*time.Hour), true)
	http.Redirect(writer, request, "/", http.StatusSeeOther)
}
