package server

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"owngit/internal/logtext"
	"owngit/internal/markdown"
	"owngit/internal/repository"
	"owngit/internal/state"
	"owngit/internal/webui"
)

const maximumActivityCommits = 200_000

func (app *App) handleOverview(writer http.ResponseWriter, request *http.Request, settings state.Settings) {
	session, ok := app.requireGeneral(writer, request, settings)
	if !ok {
		return
	}
	// The first dashboard view after setup in this process shows "Setup
	// finished" once: after the terminal setup, or after the sign-in that
	// shared access asked for. The notice travels in the notice cookie.
	if notice := app.setupResult.Swap(nil); notice != nil {
		app.noticeRedirect(writer, request, "/?notice="+*notice, http.StatusSeeOther)
		return
	}
	chrome, err := app.chrome(writer, request, webui.SectionOverview, "", session.CSRF)
	if err != nil {
		app.answerUnavailable(writer, request, "page frame read", err)
		return
	}
	if app.resultNotice(writer, request) == removedNotice {
		chrome.Notices = app.removedNotices(writer, request, chrome.Viewer.AdminConfirmed)
	}
	repositories, err := app.visibleRepositories(request)
	if err != nil {
		app.renderError(writer, request, unavailable(request, "repository list read", err), webui.MsgErrUnavailable, "")
		return
	}
	ctx := request.Context()
	snapshots, snapshotErrs := app.refSnapshots(ctx, repositories)
	query := strings.ToLower(strings.TrimSpace(request.URL.Query().Get("q")))
	var summaries []webui.RepositorySummary
	// updated holds the time each listed row shows, for the sidebar below.
	updated := make(map[string]time.Time, len(repositories))
	listed := 0
	for index, stored := range repositories {
		// One repository that cannot be read never takes the page down. A
		// repository whose storage is unavailable is locked and prepared
		// again, as at startup; one whose Git data alone cannot be read is
		// marked here and stays available to Git. A repository deleted while
		// the page was built is left out.
		err := snapshotErrs[index]
		snapshot := snapshots[index]
		// A repository that another Git operation holds was not read for
		// this page: it shows its last listing (Stale), or, without one, is
		// listed as in use instead of holding up the page. It is listed only
		// if it still exists, and one found unreadable when it was last read
		// stays marked unreadable.
		busy := errors.Is(err, repository.ErrRepositoryInUse) && ctx.Err() == nil
		if busy || snapshot.Stale {
			if _, exists, lookupErr := app.Store.Repository(ctx, stored.ID); lookupErr == nil && !exists {
				continue
			}
		}
		if err != nil && !busy && !errors.Is(err, repository.ErrRepositoryPreparing) {
			if ctx.Err() != nil {
				app.renderError(writer, request, unavailable(request, "repository read", err), webui.MsgRepoUnreadable, stored.Name)
				return
			}
			if checked := app.Repositories.PrepareUnavailable(ctx, stored.ID); errors.Is(checked, repository.ErrRepositoryNotFound) {
				continue
			} else if checked != nil {
				err = checked
			}
		}
		unreadable := false
		switch {
		case busy:
			unreadable = app.unreadable.reported(stored.ID)
		case snapshot.Stale:
			// Not read now; the listing is from the last successful read.
		case err == nil || errors.Is(err, repository.ErrRepositoryPreparing):
			app.unreadable.recovered(stored.ID)
		default:
			app.unreadable.report(stored.ID, err)
			unreadable = true
		}
		repositories[listed], snapshots[listed], snapshotErrs[listed] = stored, snapshot, err
		listed++
		summary := app.repositorySummary(request, stored, snapshot)
		if err != nil {
			preparing := errors.Is(err, repository.ErrRepositoryPreparing)
			summary = webui.RepositorySummary{
				ID: stored.ID, Name: stored.Name, Description: stored.Description,
				URL: summary.URL, CloneURL: summary.CloneURL, CreatedAt: stored.CreatedAt,
				Preparing: preparing, Unreadable: unreadable, Busy: busy && !unreadable,
			}
		}
		updated[stored.ID] = summary.Updated()
		if query == "" || strings.Contains(strings.ToLower(stored.Name), query) || strings.Contains(strings.ToLower(stored.Description), query) {
			summaries = append(summaries, summary)
		}
	}
	// The sidebar was built before this page read the repositories, from
	// the snapshots it found then. It takes the times this page shows, so
	// the two lists are in the same order.
	for index := range chrome.Nav.Repositories {
		item := &chrome.Nav.Repositories[index]
		item.LastActivity = updated[item.ID]
	}
	webui.OrderNav(chrome.Nav.Repositories, chrome.Nav.Order, chrome.Lang)
	webui.OrderRepositories(summaries, chrome.Nav.Order, chrome.Lang)
	repositories, snapshots, snapshotErrs = repositories[:listed], snapshots[:listed], snapshotErrs[:listed]
	app.activity.forget(repositories)
	present := make([]string, len(repositories))
	for index, stored := range repositories {
		present[index] = stored.ID
	}
	app.Repositories.ForgetRefSnapshots(present)
	app.unreadable.forget(present)
	observation := app.observeActivity(ctx, repositories, activityKeysFrom(snapshots, snapshotErrs), app.activityLimit())
	recent := observation.entries
	sortActivityEntries(recent)
	if len(recent) > 8 {
		recent = recent[:8]
	}
	graph := buildActivityGraph(observation.counts, selectedYear(request, app.now().Year()), app.now(), len(repositories))
	observation.describe(&graph)
	app.render(writer, request, http.StatusOK, webui.OverviewPage{
		Chrome: chrome, Activity: graph, Repositories: summaries, Recent: recent,
		RecentMoreURL: "/activity", TotalCount: len(repositories),
		Release: app.releaseNotice(request, settings, chrome.Viewer.AdminConfirmed),
	})
}

// unreadableLog logs the cause once when a repository is first shown as
// unreadable, and again only after it was read successfully in between. The
// cause goes only to the server log; pages show a fixed notice.
//
// This differs on purpose from unavailable and logFailure, which log
// every failed read behind an unavailable answer or panel. The dashboard
// lists every repository on each view, so one damaged repository would
// otherwise repeat the same line on every visit to the dashboard, while its
// own repository page is one read that one person asked for.
type unreadableLog struct {
	mu     sync.Mutex
	logged map[string]bool
}

func (l *unreadableLog) report(id string, cause error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.logged[id] {
		return
	}
	if l.logged == nil {
		l.logged = make(map[string]bool)
	}
	l.logged[id] = true
	log.Printf("repository %q is shown as unreadable because its Git data could not be read: %s", id, logtext.Cause(cause))
}

// reported tells whether id was shown as unreadable when it was last read.
func (l *unreadableLog) reported(id string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.logged[id]
}

func (l *unreadableLog) recovered(id string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.logged, id)
}

// forget drops repositories that no longer exist.
func (l *unreadableLog) forget(present []string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for id := range l.logged {
		if !slices.Contains(present, id) {
			delete(l.logged, id)
		}
	}
}

func (app *App) handleNewRepositoryGet(writer http.ResponseWriter, request *http.Request, settings state.Settings, name, description string, notices []webui.Notice) {
	session, ok := app.requireGeneral(writer, request, settings)
	if !ok {
		return
	}
	chrome, err := app.chrome(writer, request, webui.SectionOverview, "", session.CSRF)
	if err != nil {
		app.answerUnavailable(writer, request, "page frame read", err)
		return
	}
	chrome.Notices = notices
	status := http.StatusOK
	if len(notices) != 0 {
		status = http.StatusUnprocessableEntity
	}
	app.render(writer, request, status, webui.NewRepositoryPage{
		Chrome: chrome, SubmitURL: "/repositories", Name: name, Description: description, NameRules: webui.MsgRepoNameRules,
	})
}

func (app *App) handleCreateRepository(writer http.ResponseWriter, request *http.Request, settings state.Settings) {
	if _, ok := app.requireGeneral(writer, request, settings); !ok {
		return
	}
	if !app.parseForm(writer, request) {
		return
	}
	if !app.requireCSRF(writer, request) {
		return
	}
	name := strings.TrimSpace(postValue(request, "name"))
	description := strings.TrimSpace(postValue(request, "description"))
	created, err := app.Repositories.Create(request.Context(), name, description)
	if err != nil {
		switch {
		case errors.Is(err, repository.ErrFailedCreationLimit):
			app.renderError(writer, request, unavailable(request, "repository creation", err), webui.MsgRepoCreationKept, strings.ToLower(name)+".git")
		case errors.Is(err, repository.ErrStorageInUse):
			logFailure(request, "repository creation", err)
			app.renderError(writer, request, http.StatusConflict, webui.MsgSetupStorageInUse, "")
		case errors.Is(err, repository.ErrImportInProgress):
			app.handleNewRepositoryGet(writer, request, settings, name, description, []webui.Notice{webui.Error("name", webui.MsgRepoNameBusy)})
		case errors.Is(err, repository.ErrNameTaken):
			app.handleNewRepositoryGet(writer, request, settings, name, description, []webui.Notice{webui.Error("name", webui.MsgRepoNameTaken)})
		case errors.Is(err, repository.ErrReservedName):
			app.handleNewRepositoryGet(writer, request, settings, name, description, []webui.Notice{webui.Error("name", webui.MsgRepoNameReserved)})
		case errors.Is(err, repository.ErrInvalidName):
			app.handleNewRepositoryGet(writer, request, settings, name, description, []webui.Notice{webui.Error("name", webui.MsgRepoNameInvalid)})
		case errors.Is(err, repository.ErrInvalidDescription):
			app.handleNewRepositoryGet(writer, request, settings, name, description, []webui.Notice{webui.Error("description", webui.MsgRepoDescriptionTooLong)})
		case errors.As(err, new(*state.PolicyError)):
			logFailure(request, "repository creation", err)
			app.renderError(writer, request, http.StatusConflict, webui.MsgBranchUnreadableCreate, "")
		default:
			app.renderError(writer, request, unavailable(request, "repository creation", err), webui.MsgRepoCreateFail, "")
		}
		return
	}
	app.noticeRedirect(writer, request, "/repositories/"+url.PathEscape(created.ID)+"?notice=repository_created", http.StatusSeeOther)
}

func (app *App) handleActivity(writer http.ResponseWriter, request *http.Request, settings state.Settings) {
	session, ok := app.requireGeneral(writer, request, settings)
	if !ok {
		return
	}
	chrome, err := app.chrome(writer, request, webui.SectionActivity, "", session.CSRF)
	if err != nil {
		app.answerUnavailable(writer, request, "page frame read", err)
		return
	}
	repositories, err := app.visibleRepositories(request)
	if err != nil {
		app.renderError(writer, request, unavailable(request, "repository list read", err), webui.MsgActivityUnavail, "")
		return
	}
	year := selectedYear(request, app.now().Year())
	// A day outside the chosen year, or not a day at all, lists the year.
	var day time.Time
	if parsed, err := time.ParseInLocation("2006-01-02", request.URL.Query().Get("date"), app.now().Location()); err == nil && parsed.Year() == year {
		day = parsed
	}
	listing := app.readActivity(request, repositories, year, day)
	app.render(writer, request, http.StatusOK, webui.ActivityPage{Chrome: chrome, Activity: listing.graph, Days: groupActivity(listing.entries), Truncated: listing.truncated, ListLimit: maximumActivityEntries})
}

func (app *App) handleRepositoryRoute(writer http.ResponseWriter, request *http.Request, settings state.Settings) {
	remainder := strings.TrimPrefix(request.URL.Path, "/repositories/")
	parts := strings.Split(remainder, "/")
	if len(parts) == 0 || parts[0] == "" {
		app.renderError(writer, request, http.StatusNotFound, webui.MsgRepoNotFound, "")
		return
	}
	var session state.Session
	var ok bool
	// The helper credential, execution policy, and runner token screens are
	// administrator only, and that is decided here by route rather than by
	// which controls a page would draw. Hiding a button is presentation; this
	// is the authorization.
	if len(parts) >= 2 && administratorRepositoryScreen(parts[1]) {
		session, ok = app.requireAdminPage(writer, request)
	} else {
		session, ok = app.requireGeneral(writer, request, settings)
	}
	if !ok {
		return
	}
	id, ok := app.answerRepositoryAddressPage(writer, request)
	if !ok {
		return
	}
	stored, exists, err := app.Store.Repository(request.Context(), id)
	if err != nil {
		// A record that could not be read says nothing about whether the
		// repository exists.
		app.renderError(writer, request, unavailable(request, "repository record read", err), webui.MsgErrUnavailable, "")
		return
	}
	if !exists {
		app.renderError(writer, request, http.StatusNotFound, webui.MsgRepoNotFound, id)
		return
	}
	chrome, err := app.chrome(writer, request, webui.SectionRepository, id, session.CSRF)
	if err != nil {
		app.answerUnavailable(writer, request, "page frame read", err)
		return
	}
	// Deleting does not need the Git data, so it is routed before that read:
	// a repository whose data cannot be read can still be removed.
	if len(parts) == 2 && parts[1] == "delete" {
		app.handleRepositoryDelete(writer, request, stored, chrome, session)
		return
	}
	// Share links are state records alone, so they are managed without a
	// Git read too.
	if len(parts) == 2 && parts[1] == "share-links" {
		app.handleShareLinks(writer, request, stored, chrome, session)
		return
	}
	// The credential screens read and change only the state database. While
	// the repository is being prepared they are routed before the Git read,
	// with an empty summary, so an administrator can still revoke a
	// credential, as the API allows.
	if len(parts) == 2 && app.Repositories.Preparing(id) {
		switch parts[1] {
		case "helper-credentials":
			app.handleHelperCredentials(writer, request, stored, repository.Summary{}, chrome)
			return
		case "runner-tokens":
			app.handleRunnerTokens(writer, request, stored, repository.Summary{}, chrome)
			return
		}
	}
	// One ref listing gives the summary every tab needs and the activity key
	// the overview's graph needs, where Summary alone would start two Git
	// processes and the graph a third.
	snapshot, err := app.Repositories.RefSnapshot(request.Context(), id)
	summary := snapshot.Summary
	if err != nil {
		app.renderRepositoryReadFailure(writer, request, app.baseRepositoryPage(request, chrome, stored, repository.Summary{}), err)
		return
	}
	if len(parts) == 2 && parts[1] == "pull-requests" {
		switch request.Method {
		case http.MethodGet:
			app.handlePullRequestsGet(writer, request, stored, summary, chrome)
		case http.MethodPost:
			app.handleCreatePullRequest(writer, request, stored, summary, chrome)
		}
		return
	}
	if len(parts) == 3 && parts[1] == "pull-requests" && parts[2] == "new" && request.Method == http.MethodGet {
		app.handleNewPullRequestGet(writer, request, stored, summary, chrome)
		return
	}
	if len(parts) >= 3 && parts[1] == "pull-requests" {
		if number, valid := parsePullRequestNumber(parts[2]); valid {
			switch {
			case len(parts) == 3 && request.Method == http.MethodGet:
				app.handlePullRequestGet(writer, request, stored, summary, chrome, number)
				return
			case len(parts) == 5 && parts[3] == "review" && parts[4] == "request" && request.Method == http.MethodPost:
				app.handlePullRequestAction(writer, request, stored, summary, chrome, number, "review_request")
				return
			case len(parts) == 5 && parts[3] == "review" && parts[4] == "skip" && request.Method == http.MethodPost:
				app.handlePullRequestAction(writer, request, stored, summary, chrome, number, "review_skip")
				return
			case len(parts) == 5 && parts[3] == "review" && parts[4] == "submit" && request.Method == http.MethodPost:
				app.handlePullRequestAction(writer, request, stored, summary, chrome, number, "review_submit")
				return
			case len(parts) == 4 && parts[3] == "edit" && request.Method == http.MethodPost:
				app.handlePullRequestAction(writer, request, stored, summary, chrome, number, "edit")
				return
			case len(parts) == 4 && parts[3] == "merge" && request.Method == http.MethodPost:
				app.handlePullRequestAction(writer, request, stored, summary, chrome, number, "merge")
				return
			case len(parts) == 4 && parts[3] == "mergeability" && request.Method == http.MethodPost:
				app.handlePullRequestMergeability(writer, request, stored, summary, chrome, number)
				return
			case len(parts) == 4 && (parts[3] == "close" || parts[3] == "reopen") && request.Method == http.MethodPost:
				app.handlePullRequestAction(writer, request, stored, summary, chrome, number, parts[3])
				return
			}
		}
	}
	if len(parts) == 2 && parts[1] == "tasks" && request.Method == http.MethodGet {
		app.handleTasksGet(writer, request, stored, summary, chrome)
		return
	}
	if len(parts) == 2 && parts[1] == "helper-credentials" {
		app.handleHelperCredentials(writer, request, stored, summary, chrome)
		return
	}
	if len(parts) == 2 && parts[1] == "configured-checks" {
		app.handleConfiguredChecks(writer, request, stored, summary, chrome)
		return
	}
	if len(parts) == 2 && parts[1] == "runner-tokens" {
		app.handleRunnerTokens(writer, request, stored, summary, chrome)
		return
	}
	if len(parts) == 2 && parts[1] == "settings" && request.Method == http.MethodGet {
		app.handleRepositorySettingsGet(writer, request, stored, summary, chrome)
		return
	}
	if len(parts) == 3 && parts[1] == "settings" && parts[2] == "default-branch" && request.Method == http.MethodPost {
		app.handleSetDefaultBranch(writer, request, stored, summary, chrome, session)
		return
	}
	if len(parts) == 3 && parts[1] == "settings" && parts[2] == "rename" && request.Method == http.MethodPost {
		app.handleRenameRepository(writer, request, stored, summary, chrome, session)
		return
	}
	if len(parts) == 3 && parts[1] == "settings" && parts[2] == "history" && request.Method == http.MethodPost {
		app.handleSaveHistory(writer, request, stored, summary, chrome, session)
		return
	}
	if len(parts) == 3 && parts[1] == "settings" && parts[2] == "ref-namespaces" && request.Method == http.MethodPost {
		app.handleSaveNamespaces(writer, request, stored, summary, chrome, session)
		return
	}
	if len(parts) == 2 && parts[1] == "import" {
		app.handleImportPage(writer, request, stored, summary, chrome)
		return
	}
	if len(parts) == 2 && parts[1] == "restore" && request.Method == http.MethodGet {
		app.handleRestoreGet(writer, request, stored, summary, chrome)
		return
	}
	if len(parts) == 3 && parts[1] == "restore" && parts[2] == "preview" && request.Method == http.MethodPost {
		app.handleRestorePreview(writer, request, stored, summary, chrome)
		return
	}
	if len(parts) == 2 && parts[1] == "restore" && request.Method == http.MethodPost {
		app.handleRestoreApply(writer, request, stored, summary, chrome)
		return
	}
	if len(parts) == 2 && parts[1] == "archive" && request.Method == http.MethodGet {
		app.handleArchive(writer, request, stored)
		return
	}
	if len(parts) == 2 && parts[1] == "raw" && (request.Method == http.MethodGet || request.Method == http.MethodHead) {
		app.handleRaw(writer, request, stored)
		return
	}
	if request.Method != http.MethodGet {
		app.renderError(writer, request, http.StatusNotFound, webui.MsgErrNotFound, request.URL.Path)
		return
	}
	app.serveCodePage(writer, request, app.baseRepositoryPage(request, chrome, stored, summary), snapshot, parts[1:])
}

// serveCodePage answers a GET of a repository's overview (tab empty), Code
// and Commits pages from snapshot, the repository's ref snapshot. base is
// the page before it is filled: a dashboard page (baseRepositoryPage) or a
// share link's (sharedRepositoryPage).
func (app *App) serveCodePage(writer http.ResponseWriter, request *http.Request, base webui.RepositoryPage, snapshot repository.RefSnapshot, tab []string) {
	summary := snapshot.Summary
	page := base
	requestedRef := request.URL.Query().Get("ref")
	status := http.StatusOK
	var err error
	switch {
	case len(tab) == 0:
		page.Tab = webui.RepoTabOverview
		err = app.fillRepositoryOverview(request, &page, summary, snapshot.ActivityKey, requestedRef)
		if requestedRef != "" && page.Ref.Missing {
			status = http.StatusNotFound
		}
	case len(tab) == 1 && tab[0] == "code":
		page.Tab = webui.RepoTabCode
		err = app.fillCode(request, &page, summary, requestedRef, request.URL.Query().Get("path"))
		// A path or a requested branch or tag that does not exist is not
		// found. The page keeps the repository and links back to it. A
		// default branch that has gone is the repository's state, not a bad
		// address, so it stays a normal page with its warning.
		if page.Code.NotFound || (requestedRef != "" && page.Ref.Missing) {
			status = http.StatusNotFound
		}
	case len(tab) == 1 && tab[0] == "commits":
		page.Tab = webui.RepoTabCommits
		err = app.fillCommits(request, &page, summary, requestedRef, "")
		if page.Commits.NotFound || (requestedRef != "" && page.Ref.Missing) {
			status = http.StatusNotFound
		}
	case len(tab) == 2 && tab[0] == "commits":
		page.Tab = webui.RepoTabCommits
		// A commit this repository does not have, including one that only
		// another repository has, is not found. A commit that exists opens
		// even when the address names a missing branch: the page then shows
		// it as a bare revision.
		err = app.fillCommits(request, &page, summary, requestedRef, tab[1])
		if page.Commits.NotFound {
			status = http.StatusNotFound
		}
	default:
		app.renderError(writer, request, http.StatusNotFound, webui.MsgErrNotFound, request.URL.Path)
		return
	}
	// A read that failed is explained instead of the page, before any 404:
	// it is never reported as a missing ref, commit or path. So is one that
	// ran out of time, since it can leave a side panel unread too.
	if err == nil {
		err = request.Context().Err()
	}
	if err != nil {
		if request.Context().Err() != nil && app.Repositories.InUse(page.Repo.ID) {
			err = repository.ErrRepositoryInUse
		}
		app.renderRepositoryReadFailure(writer, request, base, err)
		return
	}
	page.NotFound = status == http.StatusNotFound
	app.render(writer, request, status, page)
}

// renderRepositoryReadFailure answers a repository page whose Git data could
// not be read: not found when the repository was deleted meanwhile, and
// otherwise unavailable, with the reason when it is known.
func (app *App) renderRepositoryReadFailure(writer http.ResponseWriter, request *http.Request, page webui.RepositoryPage, cause error) {
	if errors.Is(cause, repository.ErrRepositoryNotFound) {
		app.renderError(writer, request, http.StatusNotFound, webui.MsgRepoNotFound, page.Repo.ID)
		return
	}
	page.Repo.Unreadable, page.Repo.Empty = true, false
	switch {
	case errors.Is(cause, repository.ErrRepositoryPreparing):
		// The notice is fixed; the cause is only in the server log.
		page.Repo.Preparing = true
		writer.Header().Set("Retry-After", "30")
	case errors.Is(cause, repository.ErrRepositoryInUse):
		page.Repo.UnreadableReason = webui.MsgRepoBusyInUse
		writer.Header().Set("Retry-After", "10")
	case errors.As(cause, new(*state.PolicyError)):
		// A saved setting stops the read; the page names it.
		logFailure(request, "repository read", cause)
		page.Repo.UnreadableReason = webui.MsgBrowseUnreadable
		app.render(writer, request, http.StatusConflict, page)
		return
	}
	app.render(writer, request, unavailable(request, "repository read", cause), page)
}

// administratorRepositoryScreen names the repository screens that require an
// administrator session before anything is read. They all read or change
// execution authority, so a general session must not reach them at all.
// The Import tab is not one of them: its status is readable by anyone who
// can read the repository, and handleImportPage requires the administrator
// session for every change and keeps administrator data out of other views.
func administratorRepositoryScreen(segment string) bool {
	switch segment {
	case "helper-credentials", "configured-checks", "runner-tokens", "settings", "delete", "share-links":
		return true
	default:
		return false
	}
}

func (app *App) baseRepositoryPage(request *http.Request, chrome webui.Chrome, stored state.Repository, summary repository.Summary) webui.RepositoryPage {
	base := "/repositories/" + url.PathEscape(stored.Address)
	clone := app.cloneURL(request, stored.Address)
	page := webui.RepositoryPage{
		Chrome:      chrome,
		Repo:        webui.RepositoryHeader{ID: stored.ID, Address: stored.Address, Name: stored.Name, Description: stored.Description, URL: base, CloneURL: clone, Empty: summary.Empty},
		OverviewURL: base, CodeURL: base + "/code", CommitsURL: base + "/commits",
		RestoreURL:      restoreURL(stored.Address, summary.DefaultOID, summary.DefaultBranch, ""),
		PullRequestsURL: base + "/pull-requests",
		TasksURL:        base + "/tasks",
		ImportsURL:      base + "/import",
	}
	// The administrator sections are offered to every viewer. Their routes
	// send a viewer without an administrator session to the login, which
	// returns to the section afterwards.
	page.SettingsURL = base + "/settings"
	page.DeleteURL = base + "/delete"
	return page
}

// ownerRestoreURL is restoreURL for the repository of page, or "" on a
// shared page, which leads to nothing an owner does.
func ownerRestoreURL(page *webui.RepositoryPage, sourceOID, target, filePath string) string {
	if page.Shared {
		return ""
	}
	return restoreURL(page.Repo.Address, sourceOID, target, filePath)
}

// ownerArchiveLinks is archiveLinks for the repository of page, or none on
// a shared page, which offers what a share link gives: pages, raw files
// and, for a clone link, Git.
func ownerArchiveLinks(page *webui.RepositoryPage, ref string) []webui.ArchiveLink {
	if page.Shared {
		return nil
	}
	return archiveLinks(page.Repo.Address, ref)
}

// selectRef resolves the requested ref, or the default branch, for a page
// and fills its ref picker. It reports whether the ref resolved; one that
// does not exist is marked Missing. A lookup that failed is returned as an
// error, since it tells nothing about the ref.
func (app *App) selectRef(request *http.Request, page *webui.RepositoryPage, summary repository.Summary, requested string) (string, bool, error) {
	selected := requested
	if selected == "" && summary.DefaultBranch != "" {
		selected = "refs/heads/" + summary.DefaultBranch
	}
	canonical, oid, resolved := "", "", false
	if selected != "" {
		var err error
		canonical, oid, err = app.Repositories.ResolveRef(request.Context(), page.Repo.ID, selected)
		if err != nil && !errors.Is(err, repository.ErrNotFound) {
			return "", false, err
		}
		resolved = err == nil
	}
	if resolved {
		selected = canonical
	}
	page.Ref.Name = displayRef(selected)
	page.Ref.Kind = refKind(selected)
	page.Ref.IsDefault = selected != "" && selected == "refs/heads/"+summary.DefaultBranch
	for _, branch := range summary.Branches {
		full := "refs/heads/" + branch.Name
		option := webui.RefOption{Name: branch.Name, URL: withRef(request.URL.Path, full), Selected: selected == full, IsDefault: branch.Name == summary.DefaultBranch}
		page.Ref.Branches = append(page.Ref.Branches, option)
	}
	for _, tag := range summary.Tags {
		full := "refs/tags/" + tag.Name
		page.Ref.Tags = append(page.Ref.Tags, webui.RefOption{Name: tag.Name, URL: withRef(request.URL.Path, full), Selected: selected == full})
	}
	// The tabs keep only a ref that resolves. A missing one would send each
	// tab to a not-found page, so they then open the repository's
	// default addresses; the picker still shows the missing name.
	if resolved {
		page.OverviewURL = withRef(page.OverviewURL, selected)
		page.CodeURL = withRef(page.CodeURL, selected)
		page.CommitsURL = withRef(page.CommitsURL, selected)
	}
	if summary.Empty {
		return "", false, nil
	}
	if !resolved {
		page.Ref.Missing = true
		return "", false, nil
	}
	page.Ref.Revision = oid
	page.Ref.ShortRevision = shortOID(oid)
	return canonical, true, nil
}

func displayRef(ref string) string {
	for _, prefix := range []string{"refs/heads/", "refs/tags/"} {
		if strings.HasPrefix(ref, prefix) {
			return strings.TrimPrefix(ref, prefix)
		}
	}
	return ref
}

func refKind(ref string) string {
	if strings.HasPrefix(ref, "refs/tags/") {
		return "tag"
	}
	return "branch"
}

// Bounds for the overview. Every figure the overview shows comes from one
// bounded read, so a repository with thousands of refs, commits, or pull
// requests costs the same page work as a small one.
const (
	overviewRecentCommits = 7
	overviewOpenPRBound   = 99
	overviewBranchRows    = 6
	overviewTagRows       = 5
	overviewRetainedRows  = 5
	overviewAllRefsQuery  = "refs"
	overviewAllRefsValue  = "all"
	overviewRefsFragment  = "#refs"
)

// fillRepositoryOverview fills the overview. Its body, the recent commits
// and the top folder of the selected ref, is required: a failed read of it
// is returned, and the page then says the repository cannot be read. Each
// side panel (languages, activity, open pull requests, the default branch
// check, ref tips and kept history) is read on its own, and one that cannot
// be read says so on the page. It is never shown as empty, zero or absent.
func (app *App) fillRepositoryOverview(request *http.Request, page *webui.RepositoryPage, summary repository.Summary, activityKey, requested string) error {
	selectedRef, resolved, err := app.selectRef(request, page, summary, requested)
	if err != nil {
		return err
	}
	if page.Ref.Missing {
		code := webui.MsgRepoRefMissing
		if requested == "" {
			code = webui.MsgRepoDefaultGone
		}
		page.Chrome.Notices = append(page.Chrome.Notices, webui.Notice{Kind: webui.NoticeWarning, Code: code})
	}
	if summary.DefaultOID != "" {
		page.Overview.DefaultBranch = summary.DefaultBranch
	}
	page.Overview.BranchCount = len(summary.Branches)
	page.Overview.TagCount = len(summary.Tags)
	if page.Repo.Empty {
		// HEAD names the branch the repository started on, as Settings
		// chose it when the repository was created.
		branch := summary.DefaultBranch
		if branch == "" {
			branch = "HEAD"
		}
		if !page.Shared {
			page.Overview.PushCommands = []string{
				"git remote add origin " + page.Repo.CloneURL,
				"git push -u origin " + branch,
			}
			page.Overview.Activity = emptyActivityGraph(selectedYear(request, app.now().Year()), app.now(), page.Repo.Name)
		}
		return nil
	}
	page.Overview.Languages = app.overviewLanguages(request, page.Repo.ID, summary.DefaultOID)
	if resolved {
		// One extra commit says whether older history exists without counting it.
		commits, err := app.Repositories.CommitsAt(request.Context(), page.Repo.ID, page.Ref.Revision, overviewRecentCommits+1)
		if err := app.noteUnreadableCommits(request, page, selectedRef, err); err != nil {
			return err
		}
		if len(commits) > 0 {
			if len(commits) > overviewRecentCommits {
				page.Overview.RecentMore = true
				commits = commits[:overviewRecentCommits]
			}
			for _, commit := range commits {
				page.Overview.Recent = append(page.Overview.Recent, app.commitSummary(page.Repo.URL, selectedRef, commit))
			}
			// An unreadable tip leaves the latest commit unknown, not its
			// parent.
			if commits[0].OID == page.Ref.Revision {
				page.Overview.Head = page.Overview.Recent[0]
			}
		}
		// The README answers what the repository is. It is found and
		// rendered exactly as the code view does for the top folder.
		entries, err := app.Repositories.TreeAt(request.Context(), page.Repo.ID, page.Ref.Revision, "")
		if err != nil {
			return err
		}
		page.Overview.Readme = app.folderReadme(request, page.Repo, selectedRef, "", entries)
	}
	// A shared page shows the code only: pull requests, checks, kept history
	// and the activity graph, which counts kept history, are the owner's.
	if !page.Shared {
		app.fillOverviewEvidence(request, page, summary)
	}

	showAll := request.URL.Query().Get(overviewAllRefsQuery) == overviewAllRefsValue
	branchTips, err := app.Repositories.RefTips(request.Context(), page.Repo.ID, summary.Branches)
	page.Overview.BranchTipsKnown = err == nil
	if err != nil {
		logFailure(request, "branch tip read", err)
	}
	tagTips, err := app.Repositories.RefTips(request.Context(), page.Repo.ID, summary.Tags)
	page.Overview.TagTipsKnown = err == nil
	if err != nil {
		logFailure(request, "tag tip read", err)
	}
	var branches, tags, retainedLines []webui.RefLine
	for _, branch := range summary.Branches {
		full := "refs/heads/" + branch.Name
		line := webui.RefLine{Name: branch.Name, Kind: "branch", IsDefault: branch.Name == summary.DefaultBranch, URL: withRef(page.Repo.URL, full)}
		if commit, ok := branchTips[branch.Name]; ok {
			line.Tip = app.commitSummary(page.Repo.URL, full, commit)
			line.RestoreURL = ownerRestoreURL(page, commit.OID, branch.Name, "")
		}
		branches = append(branches, line)
	}
	for _, tag := range summary.Tags {
		full := "refs/tags/" + tag.Name
		line := webui.RefLine{Name: tag.Name, Kind: "tag", URL: withRef(page.Repo.URL, full), Annotated: tag.Type == "tag"}
		if commit, ok := tagTips[tag.Name]; ok {
			line.Tip = app.commitSummary(page.Repo.URL, full, commit)
			line.RestoreURL = ownerRestoreURL(page, commit.OID, summary.DefaultBranch, "")
		}
		tags = append(tags, line)
	}
	var retained []repository.RetainedRef
	if !page.Shared {
		retained, err = app.Repositories.RetainedRefs(request.Context(), page.Repo.ID)
		page.Overview.RetainedKnown = err == nil
		if err != nil {
			logFailure(request, "kept history read", err)
		}
	}
	for _, ref := range retained {
		line := webui.RefLine{Name: shortOID(ref.OID), Kind: ref.Kind, Retained: true}
		if ref.CommitOID != "" {
			line.Tip = app.commitSummary(page.Repo.URL, "", ref.Commit)
			line.URL = line.Tip.URL
			line.RestoreURL = ownerRestoreURL(page, ref.CommitOID, recoveredTarget(ref.OID, summary.Branches), "")
		}
		retainedLines = append(retainedLines, line)
	}
	// Newest first, so a repository with many releases shows its recent ones
	// rather than the first names in alphabetical order.
	sortRefLinesNewestFirst(branches)
	sortRefLinesNewestFirst(tags)
	sortRefLinesNewestFirst(retainedLines)
	page.Overview.RetainedCount = len(retainedLines)
	hidden := false
	if !showAll {
		var cut bool
		branches, cut = firstRefLines(branches, overviewBranchRows)
		hidden = hidden || cut
		tags, cut = firstRefLines(tags, overviewTagRows)
		hidden = hidden || cut
		retainedLines, cut = firstRefLines(retainedLines, overviewRetainedRows)
		hidden = hidden || cut
	}
	page.Overview.Branches, page.Overview.Tags, page.Overview.RetainedRefs = branches, tags, retainedLines
	switch {
	case showAll:
		page.Overview.FewerRefsURL = overviewRefsURL(request, false)
	case hidden:
		page.Overview.AllRefsURL = overviewRefsURL(request, true)
	}

	if !page.Shared {
		page.Overview.Activity = app.repositoryActivityGraph(request, page, activityKey)
	}
	return nil
}

// fillOverviewEvidence reads the two summary figures that come from OwnGit's
// own records: how many pull requests are open, and the latest check on the
// default branch tip. Each is one bounded query. A failed read is reported as
// unreadable instead of as zero or as "no check".
func (app *App) fillOverviewEvidence(request *http.Request, page *webui.RepositoryPage, summary repository.Summary) {
	open, more, err := app.Store.OpenPullRequests(request.Context(), page.Repo.ID, overviewOpenPRBound)
	if err == nil {
		page.Overview.OpenPullRequests = len(open)
		page.Overview.OpenPullRequestsMore = more
		page.Overview.OpenPullRequestsKnown = true
	} else {
		logFailure(request, "open pull request count read", err)
	}
	if summary.DefaultOID == "" {
		return
	}
	page.Overview.DefaultCheckRev = summary.DefaultOID
	attempt, exists, err := app.Store.LatestCheckAttemptForRevision(request.Context(), page.Repo.ID, summary.DefaultOID)
	if err != nil {
		logFailure(request, "default branch check read", err)
		return
	}
	page.Overview.DefaultCheckKnown = true
	if exists {
		page.Overview.HasDefaultCheck = true
		page.Overview.DefaultCheck = app.browserAttemptRecord(request.Context(), attempt)
	}
}

// sortRefLinesNewestFirst orders rows by the tip date the row displays, so the
// list reads in the order it claims. A row without a tip goes last, and equal
// dates fall back to the name so the order is stable across reloads.
func sortRefLinesNewestFirst(lines []webui.RefLine) {
	sort.SliceStable(lines, func(i, j int) bool {
		left, right := lines[i], lines[j]
		if left.IsDefault != right.IsDefault {
			return left.IsDefault
		}
		if !left.Tip.AuthorDate.Equal(right.Tip.AuthorDate) {
			return left.Tip.AuthorDate.After(right.Tip.AuthorDate)
		}
		return left.Name < right.Name
	})
}

// firstRefLines keeps at most limit rows and reports whether any were cut.
func firstRefLines(lines []webui.RefLine, limit int) ([]webui.RefLine, bool) {
	if len(lines) <= limit {
		return lines, false
	}
	return lines[:limit], true
}

// overviewRefsURL is this overview with every ref listed, or with the short
// lists again. It keeps the selected ref and language, and lands on the ref
// column.
func overviewRefsURL(request *http.Request, all bool) string {
	values := request.URL.Query()
	values.Del(overviewAllRefsQuery)
	values.Del("notice")
	if all {
		values.Set(overviewAllRefsQuery, overviewAllRefsValue)
	}
	target := request.URL.Path
	if encoded := values.Encode(); encoded != "" {
		target += "?" + encoded
	}
	return target + overviewRefsFragment
}

func recoveredTarget(oid string, branches []repository.Ref) string {
	base := "recovered-" + shortOID(oid)
	used := make(map[string]bool, len(branches))
	for _, branch := range branches {
		used[strings.ToLower(branch.Name)] = true
	}
	if !used[strings.ToLower(base)] {
		return base
	}
	for suffix := 2; ; suffix++ {
		candidate := base + "-" + strconv.Itoa(suffix)
		if !used[strings.ToLower(candidate)] {
			return candidate
		}
	}
}

// fillCode fills the Code tab. A ref or path that does not exist is marked
// on the page; a read that failed is returned.
func (app *App) fillCode(request *http.Request, page *webui.RepositoryPage, summary repository.Summary, requested, requestedPath string) error {
	selectedRef, resolved, err := app.selectRef(request, page, summary, requested)
	if err != nil {
		return err
	}
	if !resolved {
		if requested == "" && page.Ref.Missing {
			page.Chrome.Notices = append(page.Chrome.Notices, webui.Notice{Kind: webui.NoticeWarning, Code: webui.MsgRepoDefaultGone})
		}
		return nil
	}
	// The ref was resolved once, above; every read below names its commit.
	commitOID := page.Ref.Revision
	lookup, err := app.Repositories.PathAt(request.Context(), page.Repo.ID, commitOID, requestedPath)
	if errors.Is(err, repository.ErrNotFound) {
		page.Code = webui.CodeView{Path: requestedPath, NotFound: true, Crumbs: codeCrumbs(page.Repo, selectedRef, requestedPath)}
		return nil
	}
	if err != nil {
		return err
	}
	if !lookup.Folder {
		limits, err := app.Store.BrowseLimits(request.Context())
		if err != nil {
			return err
		}
		blob, err := app.Repositories.BlobAt(request.Context(), page.Repo.ID, lookup.File, limits.FileBytes)
		if err != nil {
			return err
		}
		binary := blob.Binary || !utf8.Valid(blob.Content)
		target := summary.DefaultBranch
		if strings.HasPrefix(selectedRef, "refs/heads/") {
			target = strings.TrimPrefix(selectedRef, "refs/heads/")
		}
		parent := path.Dir(requestedPath)
		if parent == "." {
			parent = ""
		}
		file := &webui.FileView{
			Path: requestedPath, Size: int64(len(blob.Content)), Binary: binary, Truncated: blob.Truncated,
			RawURL:     rawURL(page.Repo.URL, selectedRef, requestedPath),
			RestoreURL: ownerRestoreURL(page, commitOID, target, requestedPath),
		}
		if lookup.File.Size >= 0 {
			file.Size = lookup.File.Size
		}
		if !binary {
			content := strings.ReplaceAll(string(blob.Content), "\r\n", "\n")
			file.Lines = strings.Split(strings.TrimSuffix(content, "\n"), "\n")
		}
		if !binary && markdown.IsDocument(requestedPath) {
			file.Document = true
			file.ShowSource = request.URL.Query().Get("view") == "source"
			file.PreviewURL = codeURL(page.Repo.URL, selectedRef, requestedPath)
			file.SourceURL = file.PreviewURL + "&view=source"
			// A cut-off document would render a broken ending, so only a
			// whole file is rendered. The source view is always offered.
			if blob.Truncated {
				file.NotRendered = webui.MsgCodeNotShown
			} else if !file.ShowSource {
				file.Rendered, file.NotRendered = app.renderMarkdown(request.Context(), page.Repo.URL, selectedRef, parent, blob.Content)
			}
		}
		view := webui.CodeView{Path: requestedPath, Dir: parent, Crumbs: codeCrumbs(page.Repo, selectedRef, requestedPath), File: file, Entries: treeViewEntries(page.Repo.URL, selectedRef, lookup.Entries)}
		// The drawer lists the file's folder, so "up" leaves that folder.
		if parent != "" {
			view.UpURL = codeURL(page.Repo.URL, selectedRef, path.Dir(parent))
		}
		file.RawTooLarge = file.Size > limits.RawBytes
		// A picture loads through the raw endpoint, so it is shown only
		// when that endpoint would serve it.
		if binary && !file.RawTooLarge {
			file.Image, file.ImageWidth, file.ImageHeight = inlineImage(requestedPath, blob.Content)
		}
		page.Code = view
		return nil
	}
	view := webui.CodeView{Path: requestedPath, Dir: requestedPath, Crumbs: codeCrumbs(page.Repo, selectedRef, requestedPath)}
	if requestedPath != "" {
		view.UpURL = codeURL(page.Repo.URL, selectedRef, path.Dir(requestedPath))
	}
	view.Entries = treeViewEntries(page.Repo.URL, selectedRef, lookup.Entries)
	view.Readme = app.folderReadme(request, page.Repo, selectedRef, requestedPath, lookup.Entries)
	page.Code = view
	// The whole branch or tag downloads from its top folder, where no folder
	// or file could be taken for what the archive holds.
	if requestedPath == "" {
		page.Downloads = ownerArchiveLinks(page, selectedRef)
	}
	return nil
}

// noteUnreadableCommits keeps a page that shows commits when some of them
// could not be read: the page shows the others, and one notice names each
// unreadable commit and links to its own page. Any other error is returned.
func (app *App) noteUnreadableCommits(request *http.Request, page *webui.RepositoryPage, ref string, err error) error {
	var unreadable *repository.UnreadableCommitError
	if !errors.As(err, &unreadable) {
		return err
	}
	// The repository is part of the cause, so the same commit in another
	// repository is logged on its own line rather than counted with this one.
	logFailure(request, "commit read", fmt.Errorf("repository %q: %w", page.Repo.ID, err))
	for _, oid := range unreadable.OIDs {
		named := func(notice webui.Notice) bool {
			return notice.Code == webui.MsgCommitUnreadable && notice.Detail == shortOID(oid)
		}
		if !slices.ContainsFunc(page.Chrome.Notices, named) {
			page.Chrome.Notices = append(page.Chrome.Notices, webui.Notice{
				Kind: webui.NoticeWarning, Code: webui.MsgCommitUnreadable, Detail: shortOID(oid), Link: commitURL(page.Repo.URL, ref, oid, ""),
			})
		}
	}
	return nil
}

// fillCommits fills the Commits tab: the list, or the commit openedOID. A
// ref, commit or path that does not exist is marked on the page, and so is a
// commit that could not be read; any other read that failed is returned.
func (app *App) fillCommits(request *http.Request, page *webui.RepositoryPage, summary repository.Summary, requested, openedOID string) error {
	selectedRef, resolved, err := app.selectRef(request, page, summary, requested)
	if err != nil {
		return err
	}
	if !resolved && requested == "" && page.Ref.Missing {
		page.Chrome.Notices = append(page.Chrome.Notices, webui.Notice{Kind: webui.NoticeWarning, Code: webui.MsgRepoDefaultGone})
	}
	if openedOID == "" {
		if !resolved {
			return nil
		}
		commits, err := app.Repositories.CommitsAt(request.Context(), page.Repo.ID, page.Ref.Revision, repository.CommitPageSize)
		if err := app.noteUnreadableCommits(request, page, selectedRef, err); err != nil {
			return err
		}
		page.Commits.Unreadable = err != nil
		for _, commit := range commits {
			page.Commits.List = append(page.Commits.List, app.commitSummary(page.Repo.URL, selectedRef, commit))
		}
		return nil
	}
	// One commit. The page names the selected branch or tag only when the
	// commit is part of its history. The list of commits is one link away
	// and is not read here.
	if resolved {
		reachable, err := app.Repositories.CommitReachableFrom(request.Context(), page.Repo.ID, page.Ref.Revision, openedOID)
		if errors.Is(err, repository.ErrNotFound) {
			page.Commits.NotFound = true
			return nil
		}
		if err != nil {
			return err
		}
		if !reachable {
			resolved = false
			selectedRef = ""
			page.OverviewURL = page.Repo.URL
			page.CodeURL = page.Repo.URL + "/code"
			page.CommitsURL = page.Repo.URL + "/commits"
		}
	}
	// A shared page reaches only commits in the history of its branch or
	// tag, never one that only kept history holds.
	if !resolved && page.Shared {
		page.Commits.NotFound = true
		return nil
	}
	if !resolved {
		page.Ref = webui.RefSelection{Name: shortOID(openedOID), Kind: "revision", Detached: true, Revision: openedOID, ShortRevision: shortOID(openedOID)}
	}
	commit, files, err := app.Repositories.CommitFiles(request.Context(), page.Repo.ID, openedOID)
	if errors.Is(err, repository.ErrNotFound) {
		page.Commits.NotFound = true
		return nil
	}
	if err != nil {
		if err := app.noteUnreadableCommits(request, page, selectedRef, err); err != nil {
			return err
		}
		page.Commits.Unreadable = true
		return nil
	}
	// A commit opens with every file's diff. An address naming one file, as
	// the note on a file left out of a large commit does, loads that file's
	// diff alone.
	requestedPath := request.URL.Query().Get("path")
	if requestedPath != "" {
		found := false
		for _, file := range files {
			if file.Path == requestedPath {
				found = true
				break
			}
		}
		if !found {
			page.Commits.NotFound = true
			return nil
		}
	}
	target := summary.DefaultBranch
	if strings.HasPrefix(selectedRef, "refs/heads/") {
		target = strings.TrimPrefix(selectedRef, "refs/heads/")
	}
	view := webui.CommitDetail{
		Commit: app.commitSummary(page.Repo.URL, selectedRef, commit), Body: commit.Body,
		CommitterName: commit.CommitterName, CommitterDate: commit.CommittedAt,
		SelectedPath: requestedPath,
		RestoreURL:   ownerRestoreURL(page, commit.OID, target, requestedPath),
	}
	if requestedPath != "" {
		view.AllFilesURL = commitURL(page.Repo.URL, selectedRef, openedOID, "")
	}
	for _, parentOID := range commit.Parents {
		view.Parents = append(view.Parents, webui.CommitSummary{OID: parentOID, ShortOID: shortOID(parentOID), URL: commitURL(page.Repo.URL, selectedRef, parentOID, "")})
	}
	page.Downloads = ownerArchiveLinks(page, commit.OID)
	if len(commit.Parents) > 1 {
		view.Unavailable = true
		view.UnavailableReason = webui.MsgCommitDiffMerge
		page.Commits.Detail = &view
		return nil
	}
	fileURL := func(filePath string) string { return commitURL(page.Repo.URL, selectedRef, openedOID, filePath) }
	limits, err := app.Store.BrowseLimits(request.Context())
	if err != nil {
		return err
	}
	if requestedPath != "" {
		patch, truncated, err := app.Repositories.CommitPatch(request.Context(), page.Repo.ID, openedOID, requestedPath, nil, limits.FilePatchBytes)
		if err != nil {
			return err
		}
		view.Truncated = truncated
		for _, file := range files {
			if file.Path != requestedPath {
				continue
			}
			item := diffFileItem(file, fileURL)
			item.Selected = true
			if !item.Binary {
				item.Hunks = parsePatch(patch)
			}
			view.Files = append(view.Files, item)
		}
		page.Commits.Detail = &view
		return nil
	}
	// A file with more changed lines than the page shows is left out of the
	// diff read, so it cannot use up the size limit of the files after it.
	deferred := map[string]bool{}
	var excluded []string
	for _, file := range files {
		if !file.Binary && file.Additions+file.Deletions > maximumCommitDiffLines && len(excluded) < maximumDeferredFiles {
			deferred[file.Path] = true
			excluded = append(excluded, file.Path)
		}
	}
	var patch string
	var truncated bool
	if len(excluded) < len(files) {
		patch, truncated, err = app.Repositories.CommitPatch(request.Context(), page.Repo.ID, openedOID, "", excluded, limits.CommitPatchBytes)
		if err != nil {
			return err
		}
	}
	view.Truncated = truncated
	view.Files, _ = diffFileItems(files, patch, truncated, deferred, fileURL, limits.CommitFileBytes)
	page.Commits.Detail = &view
	return nil
}

// Bounds for one commit or pull request diff. The sizes of the diff reads,
// and of one file's diff shown, are the owner's browsing limits
// (state.BrowseLimits). A page shows at most maximumCommitDiffLines diff
// lines, because each line becomes a table row and short lines would
// otherwise make a page many times larger than the diff. Files left out for
// any of these reasons are listed and marked as not loaded, with a link to
// their diff alone where the page has one. A commit leaves at most
// maximumDeferredFiles large files out of its diff read by name, which
// keeps the Git command line short.
const (
	maximumCommitDiffLines = 10000
	maximumDeferredFiles   = 100
)

// diffFileItem is the list row of one changed file, without its diff.
func diffFileItem(file repository.ChangedFile, fileURL func(string) string) webui.DiffFile {
	item := webui.DiffFile{
		Path: file.Path, OldPath: file.OldPath, Status: file.Status, Additions: file.Additions, Deletions: file.Deletions,
		Binary: file.Binary,
	}
	if fileURL != nil {
		item.URL = fileURL(file.Path)
	}
	return item
}

// diffFileItems pairs changed files with their parts of patch, a diff read
// without rename detection, within the page's limits. A file in deferred was
// left out of patch on purpose. When truncated, the patch stopped early, so
// a file with changed lines and no complete part is not loaded, and so is
// one whose part is larger than fileBytes. notLoaded reports whether any
// file's changes are missing from the page.
func diffFileItems(files []repository.ChangedFile, patch string, truncated bool, deferred map[string]bool, fileURL func(string) string, fileBytes int64) (items []webui.DiffFile, notLoaded bool) {
	sections := splitPatchByFile(patch, truncated)
	shownLines := 0
	for _, file := range files {
		item := diffFileItem(file, fileURL)
		if !item.Binary {
			section, ok := sections[file.Path]
			lines := strings.Count(section, "\n")
			switch {
			case ok && int64(len(section)) <= fileBytes && shownLines+lines <= maximumCommitDiffLines:
				item.Hunks = parsePatch(section)
				shownLines += lines
			case ok || deferred[file.Path] || truncated && file.Additions+file.Deletions > 0:
				item.NotLoaded = true
				notLoaded = true
			}
		}
		items = append(items, item)
	}
	return items, notLoaded
}

// splitPatchByFile splits a whole-commit patch read without rename detection
// into each file's part, keyed by the file's path. A path is read from the
// "diff --git" line itself, since only that line is present for every kind of
// change. A change of file type yields two parts with one path, which are
// joined. When truncated, the last part may be cut short, so it is left out.
func splitPatchByFile(patch string, truncated bool) map[string]string {
	sections := map[string]string{}
	var order []string
	var current string
	var body strings.Builder
	flush := func() {
		if current != "" {
			sections[current] += body.String()
		}
		body.Reset()
	}
	for _, line := range strings.SplitAfter(patch, "\n") {
		if strings.HasPrefix(line, "diff --git ") {
			flush()
			current = patchHeaderPath(strings.TrimSuffix(strings.TrimPrefix(line, "diff --git "), "\n"))
			if current != "" {
				order = append(order, current)
			}
			continue
		}
		body.WriteString(line)
	}
	flush()
	if truncated && len(order) != 0 {
		delete(sections, order[len(order)-1])
	}
	return sections
}

// patchHeaderPath reads the new path from the rest of a "diff --git" line
// written without rename detection, where both sides name the same path. Git
// quotes a path with unusual bytes in C style, on both sides alike, and
// strconv.Unquote reads that style. An unquoted pair is split at its middle,
// since both halves match.
func patchHeaderPath(rest string) string {
	if strings.HasPrefix(rest, "\"") {
		first, err := strconv.QuotedPrefix(rest)
		if err != nil {
			return ""
		}
		second, err := strconv.Unquote(strings.TrimPrefix(rest[len(first):], " "))
		if err != nil {
			return ""
		}
		return strings.TrimPrefix(second, "b/")
	}
	if len(rest) < 5 || (len(rest)-5)%2 != 0 {
		return ""
	}
	half := (len(rest) - 5) / 2
	if rest[:2] != "a/" || rest[2+half:2+half+3] != " b/" || rest[2:2+half] != rest[5+half:] {
		return ""
	}
	return rest[5+half:]
}

func (app *App) repositorySummary(request *http.Request, stored state.Repository, snapshot repository.RefSnapshot) webui.RepositorySummary {
	summary := snapshot.Summary
	result := webui.RepositorySummary{
		ID: stored.ID, Name: stored.Name, Description: stored.Description,
		URL: repositoryPath(stored.Address), CloneURL: app.cloneURL(request, stored.Address),
		CreatedAt: stored.CreatedAt, Empty: summary.Empty, DefaultBranch: summary.DefaultBranch,
		// An empty repository has no branch at all yet, which is its normal
		// first state rather than a default branch that went missing.
		DefaultBranchMissing: !summary.Empty && summary.DefaultBranch != "" && summary.DefaultOID == "",
		BranchCount:          len(summary.Branches), TagCount: len(summary.Tags), Counted: true,
	}
	switch {
	case snapshot.HeadFound:
		result.Head = app.commitSummary(repositoryPath(stored.Address), "refs/heads/"+summary.DefaultBranch, snapshot.Head)
	case snapshot.HeadErr != nil:
		result.HeadUnreadable = true
		logFailure(request, "latest commit read", fmt.Errorf("repository %q: %w", stored.ID, snapshot.HeadErr))
	}
	return result
}

func (app *App) commitSummary(base, ref string, commit repository.Commit) webui.CommitSummary {
	return webui.CommitSummary{
		OID: commit.OID, ShortOID: shortOID(commit.OID), Subject: commit.Subject,
		AuthorName: commit.AuthorName, AuthorDate: commit.AuthoredAt, URL: commitURL(base, ref, commit.OID, ""),
	}
}

type activityObservation struct {
	entries  []webui.ActivityEntry
	counts   map[string]int
	complete bool
	// counting is true while some repository is still being counted.
	counting bool
	// preparing is true when some repository was skipped because it is
	// still being prepared after startup.
	preparing bool
	// unreadable names the repositories skipped because their refs or their
	// history could not be read. The server log has each cause.
	unreadable []string
}

// observeActivity gathers one bounded, current-history-first observation per
// repository and reuses it for both the recent list and the annual graph. The
// page-wide budget is shared, so a truncated observation is honestly
// incomplete for both outputs. Observations come from activityCache, keyed by
// the refs in keys.
func (app *App) observeActivity(ctx context.Context, repositories []state.Repository, keys []string, maximum int) activityObservation {
	observation := activityObservation{counts: make(map[string]int), complete: true}
	ids := make([]string, len(repositories))
	for index, stored := range repositories {
		ids[index] = stored.ID
	}
	parts := app.activity.observe(ctx, app.Repositories, ids, keys, maximum, activityWait)
	for index, part := range parts {
		if part.err != nil {
			// It was not counted: it is being prepared, by design, or its
			// refs or history could not be read. It is left out, the page
			// names it, and the others are still shown.
			observation.complete = false
			if errors.Is(part.err, errRefsUnlisted) && app.Repositories.Preparing(ids[index]) {
				observation.preparing = true
			} else {
				observation.unreadable = append(observation.unreadable, repositories[index].Name)
			}
			continue
		}
		if part.incomplete || part.pending {
			observation.complete = false
		}
		observation.counting = observation.counting || part.pending
		observation.entries = append(observation.entries, app.activityEntries(repositories[index], part.records)...)
		for _, record := range part.records {
			observation.counts[record.AuthoredAt.Format("2006-01-02")]++
		}
	}
	return observation
}

// describe sets the graph's completeness and availability, and the
// repositories it left out as unreadable, from the observation. Counting in progress takes
// precedence over the limit reason.
func (observation activityObservation) describe(graph *webui.ActivityGraph) {
	graph.Complete = observation.complete
	graph.Unreadable = observation.unreadable
	unreadable := len(observation.unreadable) != 0
	switch {
	case observation.counting:
		graph.IncompleteReason = webui.MsgActivityCounting
	case observation.preparing && unreadable:
		graph.IncompleteReason = webui.MsgActivitySkipped
	case observation.preparing:
		graph.IncompleteReason = webui.MsgActivityPreparing
	case unreadable:
		graph.IncompleteReason = webui.MsgActivityUnreadable
	case !observation.complete:
		graph.IncompleteReason = webui.MsgActivityLimit
	}
	// With no repository read, there is no count to show, not a count of zero.
	if unreadable && len(observation.unreadable) == graph.RepositoryCount {
		graph.Available = false
		graph.UnavailableReason = webui.MsgActivityScanFail
	}
}

// overviewLanguageRows is how many languages the Languages panel names
// before folding the rest into Other.
const overviewLanguageRows = 6

// overviewLanguages builds the Languages panel from the default branch's
// current commit. A count that hit its bounds or failed shows a short note,
// never a share that could be wrong.
func (app *App) overviewLanguages(request *http.Request, id, commitOID string) webui.LanguageSummary {
	if commitOID == "" {
		return webui.LanguageSummary{Note: webui.MsgRepoLanguagesNone}
	}
	stats, err := app.Repositories.Languages(request.Context(), id, commitOID)
	switch {
	case errors.Is(err, repository.ErrRepositoryInUse):
		// Only this panel waits for the operation; the rest of the page is
		// answered from what could be read.
		return webui.LanguageSummary{Note: webui.MsgRepoLanguagesBusy}
	case err != nil:
		logFailure(request, "language count", err)
		return webui.LanguageSummary{Note: webui.MsgRepoLanguagesUnavailable}
	case stats.TooLarge:
		return webui.LanguageSummary{Note: webui.MsgRepoLanguagesTooLarge}
	case stats.TimedOut:
		return webui.LanguageSummary{Note: webui.MsgRepoLanguagesSlow}
	case len(stats.Shares) == 0:
		return webui.LanguageSummary{Note: webui.MsgRepoLanguagesNone, AttributesNote: languageAttributesNote(stats.Attributes)}
	}
	return webui.LanguageSummary{Rows: languageRows(stats.Shares), AttributesNote: languageAttributesNote(stats.Attributes)}
}

// languageAttributesNote says why .gitattributes language settings were not
// applied, or nothing when they were.
func languageAttributesNote(state repository.AttributeState) webui.MessageCode {
	switch state {
	case repository.AttributesUnsupported:
		return webui.MsgRepoLanguagesNoAttrs
	case repository.AttributesUnreadable:
		return webui.MsgRepoLanguagesAttrsFailed
	}
	return ""
}

// languageRows turns sizes, largest first, into the panel rows: the first
// overviewLanguageRows languages and then Other for the rest.
func languageRows(shares []repository.LanguageShare) []webui.LanguageRow {
	var total int64
	for _, share := range shares {
		total += share.Bytes
	}
	if total <= 0 {
		return nil
	}
	percent := func(size int64) float64 { return float64(size) * 100 / float64(total) }
	var rows []webui.LanguageRow
	var rest int64
	for index, share := range shares {
		if index >= overviewLanguageRows {
			rest += share.Bytes
			continue
		}
		value := percent(share.Bytes)
		rows = append(rows, webui.LanguageRow{Name: share.Name, Color: repository.LanguageColor(share.Name), Share: value, Percent: languagePercent(value)})
	}
	if rest > 0 {
		value := percent(rest)
		rows = append(rows, webui.LanguageRow{Name: "Other", Share: value, Percent: languagePercent(value), Other: true})
	}
	return rows
}

// languagePercent shows a share with one decimal. A share too small for
// that shows "<0.1%" rather than a misleading 0.0%.
func languagePercent(value float64) string {
	if value > 0 && value < 0.05 {
		return "<0.1%"
	}
	return strconv.FormatFloat(value, 'f', 1, 64) + "%"
}

// repositoryActivityGraph builds one repository's activity graph from the
// shared cache, with the repository's own full budget. key is the activity key
// of the ref snapshot the page was built from.
func (app *App) repositoryActivityGraph(request *http.Request, page *webui.RepositoryPage, key string) webui.ActivityGraph {
	year := selectedYear(request, app.now().Year())
	ctx := request.Context()
	name := page.Repo.Name
	part := app.activity.observe(ctx, app.Repositories, []string{page.Repo.ID}, []string{key}, app.activityLimit(), activityWait)[0]
	if part.err != nil {
		// A commit that could not be read is named at the top of the page.
		// The count logged any other cause.
		_ = app.noteUnreadableCommits(request, page, "", part.err)
		return webui.ActivityGraph{Year: year, Scope: name, UnavailableReason: webui.MsgActivityRepoFail}
	}
	counts := make(map[string]int)
	for _, record := range part.records {
		counts[record.AuthoredAt.Format("2006-01-02")]++
	}
	graph := buildActivityGraph(counts, year, app.now(), 1)
	graph.Scope = name
	graph.Complete = !part.incomplete && !part.pending
	switch {
	case part.pending:
		graph.IncompleteReason = webui.MsgActivityCountRepo
	case part.incomplete:
		graph.IncompleteReason = webui.MsgActivityLimit
	}
	return graph
}

func (app *App) activityLimit() int {
	if app.ActivityLimit > 0 {
		return app.ActivityLimit
	}
	return maximumActivityCommits
}

func (app *App) activityEntries(stored state.Repository, records []repository.ActivityRecord) []webui.ActivityEntry {
	entries := make([]webui.ActivityEntry, 0, len(records))
	for _, record := range records {
		ref := displayRef(record.Source)
		linkRef := record.Source
		if record.Retained || record.Uncertain {
			linkRef = ""
			if ref == "" {
				ref = "retained " + shortOID(record.OID)
			}
		}
		entries = append(entries, webui.ActivityEntry{
			RepositoryID: stored.ID, RepositoryName: stored.Name, RepositoryAddress: stored.Address, RepositoryURL: repositoryPath(stored.Address),
			Ref: ref, RefRetained: record.Retained,
			Commit: webui.CommitSummary{OID: record.OID, ShortOID: shortOID(record.OID), Subject: record.Subject, AuthorName: record.AuthorName, AuthorDate: record.AuthoredAt, URL: commitURL(repositoryPath(stored.Address), linkRef, record.OID, "")},
		})
	}
	return entries
}

func buildActivityGraph(counts map[string]int, year int, now time.Time, repositoryCount int) webui.ActivityGraph {
	graph := webui.ActivityGraph{Year: year, RepositoryCount: repositoryCount, Complete: true, Available: true}
	start := time.Date(year, time.January, 1, 0, 0, 0, 0, now.Location())
	end := start.AddDate(1, 0, 0)
	for day := start; day.Before(end); day = day.AddDate(0, 0, 1) {
		key := day.Format("2006-01-02")
		count := counts[key]
		graph.Days = append(graph.Days, webui.ActivityDay{Date: day, Count: count, Future: day.After(now), URL: "/activity?date=" + key + "&year=" + strconv.Itoa(year)})
		graph.Total += count
	}
	yearSet := map[int]struct{}{now.Year(): {}, year: {}}
	for day := range counts {
		if candidate, err := strconv.Atoi(strings.SplitN(day, "-", 2)[0]); err == nil {
			yearSet[candidate] = struct{}{}
		}
	}
	years := make([]int, 0, len(yearSet))
	for candidate := range yearSet {
		years = append(years, candidate)
	}
	sort.Sort(sort.Reverse(sort.IntSlice(years)))
	for _, candidate := range years {
		graph.Years = append(graph.Years, webui.ActivityYear{Year: candidate, URL: "/activity?year=" + strconv.Itoa(candidate)})
	}
	return graph
}

func emptyActivityGraph(year int, now time.Time, scope string) webui.ActivityGraph {
	graph := buildActivityGraph(nil, year, now, 1)
	graph.Scope = scope
	return graph
}

func selectedYear(request *http.Request, fallback int) int {
	year, err := strconv.Atoi(request.URL.Query().Get("year"))
	if err != nil || !activityYearInRange(year) {
		return fallback
	}
	return year
}

// activityYearInRange reports whether year is one an activity graph shows.
func activityYearInRange(year int) bool { return year >= 1970 && year <= 9999 }

func sortActivityEntries(entries []webui.ActivityEntry) {
	sort.SliceStable(entries, func(left, right int) bool {
		leftDay := entries[left].Commit.AuthorDate.Format("2006-01-02")
		rightDay := entries[right].Commit.AuthorDate.Format("2006-01-02")
		if leftDay != rightDay {
			return leftDay > rightDay
		}
		return entries[left].Commit.AuthorDate.After(entries[right].Commit.AuthorDate)
	})
}

func groupActivity(entries []webui.ActivityEntry) []webui.ActivityDayGroup {
	var groups []webui.ActivityDayGroup
	for _, entry := range entries {
		day := time.Date(entry.Commit.AuthorDate.Year(), entry.Commit.AuthorDate.Month(), entry.Commit.AuthorDate.Day(), 0, 0, 0, 0, entry.Commit.AuthorDate.Location())
		if len(groups) == 0 || groups[len(groups)-1].Date.Format("2006-01-02") != day.Format("2006-01-02") {
			groups = append(groups, webui.ActivityDayGroup{Date: day})
		}
		groups[len(groups)-1].Entries = append(groups[len(groups)-1].Entries, entry)
	}
	return groups
}

func withRef(base, ref string) string {
	return base + "?ref=" + url.QueryEscape(ref)
}

// The addresses of a repository's code pages start at base, the
// repository's own page: repositoryPath for the dashboard, or a share
// link's address.

func codeURL(base, ref, filePath string) string {
	values := url.Values{"ref": []string{ref}}
	if filePath != "" && filePath != "." {
		values.Set("path", filePath)
	}
	return base + "/code?" + values.Encode()
}

func commitURL(base, ref, oid, filePath string) string {
	values := make(url.Values)
	if ref != "" {
		values.Set("ref", ref)
	}
	if filePath != "" {
		values.Set("path", filePath)
	}
	result := base + "/commits/" + url.PathEscape(oid)
	if len(values) != 0 {
		result += "?" + values.Encode()
	}
	return result
}

func treeViewEntries(base, ref string, entries []repository.TreeEntry) []webui.TreeEntry {
	views := make([]webui.TreeEntry, 0, len(entries))
	for _, entry := range entries {
		kind := "file"
		switch {
		case entry.Type == "tree":
			kind = "dir"
		case entry.Type == "commit":
			kind = "submodule"
		case entry.Mode == "120000":
			kind = "symlink"
		}
		views = append(views, webui.TreeEntry{Name: entry.Name, Path: entry.Path, URL: codeURL(base, ref, entry.Path), Kind: kind, Size: entry.Size})
	}
	sort.SliceStable(views, func(left, right int) bool {
		leftDir := views[left].Kind == "dir"
		rightDir := views[right].Kind == "dir"
		if leftDir != rightDir {
			return leftDir
		}
		return strings.ToLower(views[left].Name) < strings.ToLower(views[right].Name)
	})
	return views
}

func codeCrumbs(repo webui.RepositoryHeader, ref, filePath string) []webui.Crumb {
	crumbs := []webui.Crumb{{Name: repo.Address, URL: codeURL(repo.URL, ref, ""), Current: filePath == ""}}
	if filePath == "" {
		return crumbs
	}
	parts := strings.Split(filePath, "/")
	for index, part := range parts {
		currentPath := strings.Join(parts[:index+1], "/")
		crumbs = append(crumbs, webui.Crumb{Name: part, URL: codeURL(repo.URL, ref, currentPath), Current: index == len(parts)-1})
	}
	return crumbs
}

func shortOID(oid string) string {
	if len(oid) > 10 {
		return oid[:10]
	}
	return oid
}

var hunkHeader = regexp.MustCompile(`^@@ -(\d+)(?:,(\d+))? \+(\d+)(?:,(\d+))? @@`)

func parsePatch(patch string) []webui.DiffHunk {
	var hunks []webui.DiffHunk
	var current *webui.DiffHunk
	oldLine, newLine := 0, 0
	for _, line := range strings.Split(strings.ReplaceAll(patch, "\r\n", "\n"), "\n") {
		match := hunkHeader.FindStringSubmatch(line)
		if match != nil {
			oldLine, _ = strconv.Atoi(match[1])
			newLine, _ = strconv.Atoi(match[3])
			hunks = append(hunks, webui.DiffHunk{Header: line})
			current = &hunks[len(hunks)-1]
			continue
		}
		if current == nil || line == "\\ No newline at end of file" || line == "" {
			continue
		}
		diffLine := webui.DiffLine{Kind: "context", Text: line}
		switch line[0] {
		case '+':
			diffLine.Kind, diffLine.NewLine, diffLine.Text = "add", newLine, line[1:]
			newLine++
		case '-':
			diffLine.Kind, diffLine.OldLine, diffLine.Text = "del", oldLine, line[1:]
			oldLine++
		case ' ':
			diffLine.OldLine, diffLine.NewLine, diffLine.Text = oldLine, newLine, line[1:]
			oldLine++
			newLine++
		default:
			continue
		}
		current.Lines = append(current.Lines, diffLine)
	}
	return hunks
}
