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
// newer release, and this browser has not dismissed that version. The
// update command holds the program's path on this computer, which is
// administrator data like the repository folder, so only a confirmed
// administrator sees it; others are told where to get it.
func (app *App) releaseNotice(request *http.Request, settings state.Settings, admin bool) *webui.ReleaseNotice {
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
	notice := &webui.ReleaseNotice{
		Version: release.Version, Current: app.Version, NotesURL: release.NotesURL,
		GuideURL: releasecheck.UpdateGuideURL, DismissURL: releaseDismissPath,
	}
	if app.UpdateCommand != nil {
		command, start, restart := app.UpdateCommand(release.Version)
		switch {
		case command == "":
		case admin:
			notice.Command, notice.Start, notice.Restart = command, start, restart
		default:
			notice.CommandHidden = true
		}
	}
	return notice
}

// handleReleaseDismiss remembers in this browser that the notice for one
// version was dismissed. A later version has a different value, so its notice
// shows again.
func (app *App) handleReleaseDismiss(writer http.ResponseWriter, request *http.Request, settings state.Settings) {
	if _, ok := app.requireGeneral(writer, request, settings); !ok {
		return
	}
	if !app.parseForm(writer, request) {
		return
	}
	if !app.requireCSRF(writer, request) {
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
