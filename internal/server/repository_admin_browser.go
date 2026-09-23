package server

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"owngit/internal/auth"
	"owngit/internal/repository"
	"owngit/internal/state"
	"owngit/internal/webui"
)

// The repository Settings tab and the delete confirmation. Both routes are
// administrator only; handleRepositoryRoute checks the administrator session
// before either handler runs, and each POST also checks the form origin and
// the session's CSRF token.

// removedCookie carries the result of a deletion across the redirect to the
// dashboard, so the notice can name the repository and the kept folder
// without putting either in a URL that anyone could craft. It is short-lived,
// HttpOnly, and read once.
const (
	removedCookie        = "owngit_removed"
	removedCookieMaxAge  = 2 * time.Minute
	removedNotice        = "repository_removed"
	defaultBranchNotice  = "default_branch_saved"
	removedFolderName    = ".owngit-removed"
	maximumRemovedCookie = 3000
)

type removedResult struct {
	Name string `json:"n"`
	Mode string `json:"m"`
	Kept string `json:"k,omitempty"`
	// Incomplete means the records are gone but the file step is not done;
	// the next start finishes it.
	Incomplete bool `json:"i,omitempty"`
	// ID names the repository in the recovery command's push address.
	ID string `json:"d,omitempty"`
}

// busyNotice names why the repository is busy. Each reason also matches
// ErrRepositoryBusy, so an unnamed reason still gets the general sentence.
func busyNotice(err error) (webui.MessageCode, bool) {
	switch {
	case errors.Is(err, repository.ErrImportRunning):
		return webui.MsgRepoBusyImport, true
	case errors.Is(err, repository.ErrCheckRunning):
		return webui.MsgRepoBusyCheck, true
	case errors.Is(err, repository.ErrCheckCleanupPending):
		return webui.MsgRepoBusyCheckCleanup, true
	case errors.Is(err, repository.ErrRepositoryInUse):
		return webui.MsgRepoBusyInUse, true
	case errors.Is(err, repository.ErrRepositoryBusy):
		return webui.MsgRepoBusy, true
	}
	return "", false
}

func repositorySettingsURL(repositoryID string) string {
	return "/repositories/" + url.PathEscape(repositoryID) + "/settings"
}

func repositoryDeleteURL(repositoryID string) string {
	return "/repositories/" + url.PathEscape(repositoryID) + "/delete"
}

// ---------------------------------------------------------------------------
// Settings
// ---------------------------------------------------------------------------

func (app *App) handleRepositorySettingsGet(writer http.ResponseWriter, request *http.Request, stored state.Repository, summary repository.Summary, chrome webui.Chrome) {
	writer.Header().Set("Cache-Control", "no-store")
	app.renderRepositorySettings(writer, request, stored, summary, chrome, "", http.StatusOK)
}

func (app *App) handleSetDefaultBranch(writer http.ResponseWriter, request *http.Request, stored state.Repository, summary repository.Summary, chrome webui.Chrome, session state.Session) {
	writer.Header().Set("Cache-Control", "no-store")
	if !parseForm(writer, request) {
		return
	}
	if !constantEqual(session.CSRF, postValue(request, "csrf")) {
		app.renderError(writer, request, http.StatusForbidden, webui.MsgErrCSRF, "")
		return
	}
	branch := postValue(request, "branch")
	// Only an existing branch is offered, and only an existing branch is
	// accepted. The backend checks again under its own lock.
	if !hasBranch(summary, branch) {
		chrome.Notices = append(chrome.Notices, webui.Error("branch", webui.MsgRepoDefaultBranchUnknown))
		app.renderRepositorySettings(writer, request, stored, summary, chrome, branch, http.StatusUnprocessableEntity)
		return
	}
	// The change waits only briefly for the write lock, and a background
	// activity count may hold the read lock far longer. The count does not
	// depend on the default branch and is redone on the next page, so a
	// running one is stopped rather than reported as another Git operation.
	app.activity.dropIfCounting(stored.ID)
	if err := app.Repositories.SetDefaultBranch(request.Context(), stored.ID, branch); err != nil {
		switch {
		case errors.Is(err, repository.ErrBranchNotFound):
			chrome.Notices = append(chrome.Notices, webui.Error("branch", webui.MsgRepoDefaultBranchUnknown))
			app.renderRepositorySettings(writer, request, stored, summary, chrome, branch, http.StatusUnprocessableEntity)
		case errors.Is(err, repository.ErrRepositoryBusy):
			code, _ := busyNotice(err)
			chrome.Notices = append(chrome.Notices, webui.Error("", code))
			app.renderRepositorySettings(writer, request, stored, summary, chrome, branch, http.StatusConflict)
		case errors.Is(err, repository.ErrRepositoryNotFound):
			app.renderError(writer, request, http.StatusNotFound, webui.MsgRepoNotFound, stored.ID)
		default:
			// The cause can name host paths, so it goes to the server log.
			log.Printf("default branch of repository %s was not changed: %v", stored.ID, err)
			chrome.Notices = append(chrome.Notices, webui.Error("", webui.MsgRepoDefaultBranchFailed))
			app.renderRepositorySettings(writer, request, stored, summary, chrome, branch, http.StatusInternalServerError)
		}
		return
	}
	http.Redirect(writer, request, repositorySettingsURL(stored.ID)+"?notice="+defaultBranchNotice, http.StatusSeeOther)
}

func hasBranch(summary repository.Summary, name string) bool {
	if name == "" {
		return false
	}
	for _, branch := range summary.Branches {
		if branch.Name == name {
			return true
		}
	}
	return false
}

func (app *App) renderRepositorySettings(writer http.ResponseWriter, request *http.Request, stored state.Repository, summary repository.Summary, chrome webui.Chrome, selected string, status int) {
	base := app.baseRepositoryPage(request, chrome, stored, summary)
	self := repositorySettingsURL(stored.ID)
	page := webui.RepositorySettingsPage{
		Chrome: chrome, Repo: base.Repo, Tabs: repositoryTabs(base, webui.RepoTabSettings),
		SelfURL: self, DefaultBranchURL: self + "/default-branch",
		DefaultBranch:        summary.DefaultBranch,
		DefaultBranchMissing: summary.DefaultOID == "" && len(summary.Branches) > 0,
		ConfiguredChecksURL:  configuredChecksURL(stored.ID),
		RunnerTokensURL:      runnerTokensURL(stored.ID),
		HelperCredentialsURL: baseHelperCredentialsURL(stored.ID),
		ImportURL:            base.ImportsURL,
		DeleteURL:            base.DeleteURL,
	}
	if summary.DefaultOID != "" {
		page.Selected = summary.DefaultBranch
	}
	for _, branch := range summary.Branches {
		page.Branches = append(page.Branches, branch.Name)
		// A refused submission keeps the administrator's choice when it is
		// still offered. Anything else keeps the default, or no choice.
		if branch.Name == selected {
			page.Selected = selected
		}
	}
	app.render(writer, status, page)
}

// ---------------------------------------------------------------------------
// Delete
// ---------------------------------------------------------------------------

// handleRepositoryDelete serves the confirmation page and its POST. It runs
// before the repository's Git data is read, so a repository whose data can no
// longer be read can still be removed.
func (app *App) handleRepositoryDelete(writer http.ResponseWriter, request *http.Request, stored state.Repository, chrome webui.Chrome, session state.Session) {
	writer.Header().Set("Cache-Control", "no-store")
	if request.Method == http.MethodGet {
		app.renderRepositoryDelete(writer, request, stored, chrome, "", http.StatusOK)
		return
	}
	if !parseForm(writer, request) {
		return
	}
	if !constantEqual(session.CSRF, postValue(request, "csrf")) {
		app.renderError(writer, request, http.StatusForbidden, webui.MsgErrCSRF, "")
		return
	}
	mode := postValue(request, "mode")
	validMode := mode == webui.DeleteModeKeepFiles || mode == webui.DeleteModeDeleteFiles
	if !validMode {
		mode = ""
		chrome.Notices = append(chrome.Notices, webui.Error("mode", webui.MsgRepoDeleteModeRequired))
	}
	// Names never contain spaces, so trimming only forgives a pasted space.
	if strings.TrimSpace(postValue(request, "confirm_name")) != stored.Name {
		chrome.Notices = append(chrome.Notices, webui.Error("confirm_name", webui.MsgRepoDeleteNameMismatch))
	}
	if len(chrome.Notices) != 0 {
		app.renderRepositoryDelete(writer, request, stored, chrome, mode, http.StatusUnprocessableEntity)
		return
	}
	// The password is asked again even inside an administrator session, as
	// every other destructive administrator action does.
	if err := app.Auth.VerifyCredential(request.Context(), "admin", postValue(request, "admin_password"), request.RemoteAddr); err != nil {
		code, status := webui.MsgAdminFailed, http.StatusUnauthorized
		if errors.Is(err, auth.ErrRateLimited) {
			code, status = webui.MsgAdminLocked, http.StatusTooManyRequests
		}
		chrome.Notices = append(chrome.Notices, webui.Error("admin_password", code))
		app.renderRepositoryDelete(writer, request, stored, chrome, mode, status)
		return
	}

	// A background activity count holds the repository's read lock for as long
	// as its history walk takes, so it is stopped first, and whatever it left
	// in the cache is dropped once the repository is gone.
	app.activity.drop(stored.ID)
	// Once confirmed, the deletion must not stop half way because the
	// administrator closed the tab: the records go first, and a cancelled
	// context would leave the file step for the next start.
	result, err := app.Repositories.Delete(context.WithoutCancel(request.Context()), stored.ID, repository.DeleteMode(mode))
	// A page opened meanwhile may have started a new count, which failed or
	// is failing on the removed folder; drop it too. Without this the next
	// dashboard's forget would clear it, so this only frees it sooner.
	app.activity.drop(stored.ID)
	if err != nil && !errors.Is(err, repository.ErrRepositoryBusy) {
		// The cause can name storage paths and the deletion token, so it goes
		// to the server log, which the pages point the administrator to, and
		// never into a page.
		log.Printf("deletion of repository %s: %v", stored.ID, err)
	}
	incomplete := errors.Is(err, repository.ErrDeleteIncomplete)
	if incomplete {
		// Delete also reports an older unfinished deletion of this name that
		// it could not resume. A repository that still exists was not
		// removed, so that case is a failure, not a removal.
		if _, exists, lookupErr := app.Store.Repository(context.WithoutCancel(request.Context()), stored.ID); lookupErr != nil || exists {
			incomplete = false
		}
	}
	if err != nil && !incomplete {
		code, status := webui.MsgRepoDeleteFailed, http.StatusInternalServerError
		if busy, ok := busyNotice(err); ok {
			code, status = busy, http.StatusConflict
		} else if errors.Is(err, repository.ErrRepositoryNotFound) {
			code, status = webui.MsgRepoDeleteGone, http.StatusNotFound
		}
		chrome.Notices = append(chrome.Notices, webui.Error("", code))
		app.renderRepositoryDelete(writer, request, stored, chrome, mode, status)
		return
	}
	// An incomplete deletion has already removed the repository from OwnGit,
	// so the dashboard reports it as removed, with the file cleanup still
	// owed, rather than as a failure to retry on a page that no longer exists.
	app.setRemovedCookie(writer, request, removedResult{Name: stored.Name, ID: stored.ID, Mode: mode, Kept: result.KeptPath, Incomplete: incomplete})
	http.Redirect(writer, request, "/?notice="+removedNotice, http.StatusSeeOther)
}

func (app *App) renderRepositoryDelete(writer http.ResponseWriter, request *http.Request, stored state.Repository, chrome webui.Chrome, mode string, status int) {
	base := app.baseRepositoryPage(request, chrome, stored, repository.Summary{})
	page := webui.RepositoryDeletePage{
		Chrome: chrome, Repo: base.Repo, Tabs: repositoryTabs(base, webui.RepoTabDelete),
		SelfURL: repositoryDeleteURL(stored.ID), SubmitURL: repositoryDeleteURL(stored.ID),
		Mode: mode, CancelURL: repositorySettingsURL(stored.ID),
	}
	// The page is administrator only, and the administrator already sees the
	// storage path in the toolbar, so naming the folder adds nothing new.
	if gitPath, err := app.Repositories.Path(stored.ID); err == nil {
		page.GitPath = gitPath
		page.RemovedPath = filepath.Join(filepath.Dir(gitPath), removedFolderName)
	}
	app.render(writer, status, page)
}

func (app *App) setRemovedCookie(writer http.ResponseWriter, request *http.Request, result removedResult) {
	encoded, err := json.Marshal(result)
	if err != nil {
		return
	}
	value := base64.RawURLEncoding.EncodeToString(encoded)
	if len(value) > maximumRemovedCookie {
		// A path too long for a cookie still gets the generic notice.
		return
	}
	app.setCookie(writer, request, removedCookie, value, app.now().Add(removedCookieMaxAge), true)
}

// removedNotices reads and clears the deletion result for the dashboard. A
// missing or malformed cookie gives the generic notice, which claims nothing
// about a particular repository.
//
// The details name storage paths, so they are shown only to an administrator
// session; any other viewer gets the generic notice and the cookie is cleared.
func (app *App) removedNotices(writer http.ResponseWriter, request *http.Request, administrator bool) []webui.Notice {
	generic := []webui.Notice{webui.Success(webui.MsgRepoRemovedGeneric)}
	cookie, err := request.Cookie(removedCookie)
	if err != nil {
		return generic
	}
	app.clearCookie(writer, request, removedCookie, true)
	if !administrator {
		return generic
	}
	data, err := base64.RawURLEncoding.DecodeString(cookie.Value)
	if err != nil {
		return generic
	}
	var result removedResult
	if json.Unmarshal(data, &result) != nil || result.Name == "" {
		return generic
	}
	if result.Incomplete {
		return []webui.Notice{
			{Kind: webui.NoticeWarning, Code: webui.MsgRepoRemovedIncomplete, Detail: result.Name},
			webui.Info(webui.MsgRepoRemovedCleanupLater),
		}
	}
	switch result.Mode {
	case webui.DeleteModeKeepFiles:
		notices := []webui.Notice{webui.Success(webui.MsgRepoRemovedKept).WithDetail(result.Name)}
		if result.Kept == "" {
			return append(notices, webui.Info(webui.MsgRepoRemovedKeptRecovery))
		}
		notices = append(notices, webui.Info(webui.MsgRepoRemovedKeptAt).WithDetail(result.Kept))
		if !app.keptFolderShape(result.ID, result.Kept) {
			return append(notices, webui.Info(webui.MsgRepoRemovedKeptRecovery))
		}
		return append(notices,
			webui.Info(webui.MsgRepoRemovedKeptCommand).WithDetail(recoveryCommand(result.Kept, app.baseURL(request)+"/git/"+url.PathEscape(result.ID)+".git")),
			webui.Info(webui.MsgRepoRemovedKeptWhere))
	case webui.DeleteModeDeleteFiles:
		return []webui.Notice{webui.Success(webui.MsgRepoRemovedDeleted).WithDetail(result.Name)}
	}
	return generic
}

// keptFolderShape reports whether path is where the backend moves a kept
// repository: <storage root>/.owngit-removed/<id>-<UTC stamp>[-n].git. The
// cookie is not signed, so the recovery command is offered only for a path of
// exactly that shape; anything else gets the sentence without a command.
func (app *App) keptFolderShape(id, path string) bool {
	if repository.ValidateID(id) != nil || filepath.Clean(path) != path {
		return false
	}
	gitPath, err := app.Repositories.Path(id)
	if err != nil || filepath.Dir(path) != filepath.Join(filepath.Dir(gitPath), removedFolderName) {
		return false
	}
	name := filepath.Base(path)
	return strings.HasPrefix(name, id+"-") && keptStamp.MatchString(strings.TrimPrefix(name, id+"-"))
}

// keptStamp is the rest of a kept folder's name after "<id>-".
var keptStamp = regexp.MustCompile(`^[0-9]{8}T[0-9]{6}Z(-[0-9]+)?\.git$`)

// recoveryCommand is the push that restores a kept folder into a new, empty
// repository. OwnGit accepts only branches and tags, so it names those two
// ref spaces rather than using --mirror. The path comes from a cookie, so it
// is always quoted for a POSIX shell.
func recoveryCommand(keptPath, pushURL string) string {
	return "git --git-dir " + shellQuote(keptPath) + " push " + shellQuote(pushURL) +
		" 'refs/heads/*:refs/heads/*' 'refs/tags/*:refs/tags/*'"
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'"
}
