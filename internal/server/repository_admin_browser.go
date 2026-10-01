package server

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

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
	renamedNotice        = "repository_renamed"
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
	// ID identifies the kept folder's original storage path.
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
	case errors.Is(err, repository.ErrBackupReading):
		return webui.MsgRepoBusyBackup, true
	case errors.Is(err, repository.ErrRepositoryBusy):
		return webui.MsgRepoBusy, true
	}
	return "", false
}

func repositorySettingsURL(address string) string {
	return "/repositories/" + url.PathEscape(address) + "/settings"
}

func repositoryDeleteURL(address string) string {
	return "/repositories/" + url.PathEscape(address) + "/delete"
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
	if !app.parseForm(writer, request) {
		return
	}
	if !constantEqual(session.CSRF, postValue(request, "csrf")) {
		app.renderError(writer, request, http.StatusForbidden, webui.MsgErrCSRF, "")
		return
	}
	branch := postValue(request, "branch")
	if _, err := app.confirmAdmin(writer, request, &chrome, false); err != nil {
		notice, status := adminPasswordNotice(request, err, "admin_password")
		chrome.Notices = append(chrome.Notices, notice)
		app.renderRepositorySettings(writer, request, stored, summary, chrome, branch, status)
		return
	}
	// Only an existing branch is offered, and only an existing branch is
	// accepted. The backend checks again under its own lock.
	if !hasBranch(summary, branch) {
		chrome.Notices = append(chrome.Notices, webui.Error("branch", webui.MsgRepoDefaultBranchUnknown))
		app.renderRepositorySettings(writer, request, stored, summary, chrome, branch, http.StatusUnprocessableEntity)
		return
	}
	if err := app.setDefaultBranch(request.Context(), stored.ID, branch); err != nil {
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
			chrome.Notices = append(chrome.Notices, webui.Error("", webui.MsgRepoDefaultBranchFailed))
			app.renderRepositorySettings(writer, request, stored, summary, chrome, branch, unavailable(request, "default branch change", err))
		}
		return
	}
	app.noticeRedirect(writer, request, repositorySettingsURL(stored.Address)+"?notice="+defaultBranchNotice, http.StatusSeeOther)
}

// setDefaultBranch makes branch, an existing branch, the default branch of
// repository id, for Settings and the owner API alike.
func (app *App) setDefaultBranch(ctx context.Context, id, branch string) error {
	// The change waits only briefly for the write lock, and a background
	// activity count may hold the read lock far longer. The count does not
	// depend on the default branch and is redone on a later page, so counting
	// pauses for the change rather than being reported as another Git
	// operation.
	// The pause ends in a deferred call, so a panic cannot leave it behind.
	defer app.activity.pause(id, true)()
	return app.Repositories.SetDefaultBranch(ctx, id, branch)
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
	app.renderRepositorySettingsPage(writer, request, stored, summary, chrome, selected, nil, nil, nil, status)
}

// renameForm is what the rename form sent, shown again with its notices
// after a refused rename.
type renameForm struct {
	name    string
	notices []webui.Notice
}

// historyForm is what the kept history and protection form sent, shown
// again with its notices after a refused save.
type historyForm struct {
	kept    state.KeptHistoryChoice
	protect bool
	notices []webui.Notice
}

// namespacesForm is what the extra ref namespaces form sent, shown again
// with its notices after a refused save.
type namespacesForm struct {
	text    string
	notices []webui.Notice
}

// renderRepositorySettingsPage renders the Settings tab. The kept history
// and protection form shows history when it was refused, and otherwise the
// saved choices; a saved row that cannot be read shows the defaults and
// says so. The extra ref namespaces form does the same with namespaces,
// and the rename form shows a refused name.
func (app *App) renderRepositorySettingsPage(writer http.ResponseWriter, request *http.Request, stored state.Repository, summary repository.Summary, chrome webui.Chrome, selected string, history *historyForm, namespaces *namespacesForm, rename *renameForm, status int) {
	base := app.baseRepositoryPage(request, chrome, stored, summary)
	self := repositorySettingsURL(stored.Address)
	page := webui.RepositorySettingsPage{
		Chrome: chrome, Repo: base.Repo, Tabs: repositoryTabs(base, webui.RepoTabSettings),
		SelfURL: self, DefaultBranchURL: self + "/default-branch", HistoryURL: self + "/history", NamespacesURL: self + "/ref-namespaces",
		RenameURL: self + "/rename", Address: stored.Address,
		DefaultBranch:        summary.DefaultBranch,
		DefaultBranchMissing: summary.DefaultOID == "" && len(summary.Branches) > 0,
		ConfiguredChecksURL:  configuredChecksURL(stored.Address),
		RunnerTokensURL:      runnerTokensURL(stored.Address),
		HelperCredentialsURL: baseHelperCredentialsURL(stored.Address),
		ShareLinksURL:        shareLinksURL(stored.Address),
		ImportURL:            base.ImportsURL,
		DeleteURL:            base.DeleteURL,
	}
	saved, err := app.Store.RepositoryRefPolicy(request.Context(), stored.ID)
	if errors.As(err, new(*state.PolicyError)) {
		logFailure(request, "repository settings read", err)
		saved, err, page.HistoryUnreadable = state.RepositoryRefPolicy{KeptHistory: state.KeptHistoryDefault}, nil, true
	}
	if err != nil {
		app.answerUnavailable(writer, request, "repository settings read", err)
		return
	}
	aliases, err := app.Store.RepositoryAliases(request.Context(), stored.ID, app.now())
	if err != nil {
		app.answerUnavailable(writer, request, "repository alias read", err)
		return
	}
	for _, alias := range aliases {
		page.Aliases = append(page.Aliases, webui.RepositoryAlias{Name: alias.Name, Until: *alias.AliasUntil})
	}
	if rename != nil {
		page.RenameName, page.RenameNotices = rename.name, rename.notices
	}
	page.KeptHistory, page.ProtectDefaultBranch = string(saved.KeptHistory), saved.ProtectDefaultBranch
	if history != nil {
		page.KeptHistory, page.ProtectDefaultBranch, page.HistoryNotices = string(history.kept), history.protect, history.notices
	}
	prefixes, err := app.Store.RepositoryExtraRefPrefixes(request.Context(), stored.ID)
	if errors.As(err, new(*state.PolicyError)) {
		logFailure(request, "repository settings read", err)
		err, page.NamespacesUnreadable = nil, true
	}
	if err != nil {
		app.answerUnavailable(writer, request, "repository settings read", err)
		return
	}
	page.Namespaces = strings.Join(prefixes, "\n")
	if namespaces != nil {
		page.Namespaces, page.NamespacesNotices = namespaces.text, namespaces.notices
	}
	serverKeeps, err := app.Store.KeptHistory(request.Context())
	switch {
	case errors.As(err, new(*state.PolicyError)):
		logFailure(request, "server kept history read", err)
	case err != nil:
		app.answerUnavailable(writer, request, "server kept history read", err)
		return
	default:
		page.ServerKeepsHistory = onOff(serverKeeps)
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
	app.render(writer, request, status, page)
}

// handleSaveHistory saves the repository's kept history choice and default
// branch protection. Both apply to pushes and imports that start after the
// save. The result notice warns about what turning either off allows.
func (app *App) handleSaveHistory(writer http.ResponseWriter, request *http.Request, stored state.Repository, summary repository.Summary, chrome webui.Chrome, session state.Session) {
	writer.Header().Set("Cache-Control", "no-store")
	if !app.parseForm(writer, request) {
		return
	}
	if !constantEqual(session.CSRF, postValue(request, "csrf")) {
		app.renderError(writer, request, http.StatusForbidden, webui.MsgErrCSRF, "")
		return
	}
	kept, valid := state.ParseKeptHistoryChoice(postValue(request, "kept_history"))
	form := &historyForm{kept: kept, protect: postValue(request, "protect_default_branch") == "on"}
	refuse := func(notice webui.Notice, status int) {
		if !valid {
			form.kept = state.KeptHistoryDefault
		}
		form.notices = append(form.notices, notice)
		app.renderRepositorySettingsPage(writer, request, stored, summary, chrome, "", form, nil, nil, status)
	}
	if _, err := app.confirmAdmin(writer, request, &chrome, false); err != nil {
		refuse(adminPasswordNotice(request, err, "admin_password"))
		return
	}
	if !valid {
		refuse(webui.Error("kept_history", webui.MsgSettingsUnknownAct), http.StatusBadRequest)
		return
	}
	_, warnings, err := app.saveRefPolicy(request.Context(), stored.ID, state.RepositoryRefPolicyChange{KeptHistory: &form.kept, ProtectDefaultBranch: &form.protect})
	if errors.As(err, new(*state.PolicyError)) {
		logFailure(request, "repository settings save", err)
		refuse(webui.Error("kept_history", webui.MsgRepoHistoryServerUnreadable), http.StatusConflict)
		return
	}
	if err != nil {
		refuse(webui.Error("", webui.MsgRepoHistoryFailed), unavailable(request, "repository settings save", err))
		return
	}
	keptOff, protectOff := slices.Contains(warnings, webui.MsgRepoHistoryKeptOff), slices.Contains(warnings, webui.MsgRepoHistoryProtectOff)
	notice := "history_saved"
	switch {
	case keptOff && protectOff:
		notice = "history_saved_both_off"
	case keptOff:
		notice = "history_saved_kept_off"
	case protectOff:
		notice = "history_saved_protect_off"
	}
	app.noticeRedirect(writer, request, repositorySettingsURL(stored.Address)+"?notice="+notice, http.StatusSeeOther)
}

// handleRenameRepository renames the repository and opens its Settings tab
// at the new address. The earlier address leads there for 90 days.
func (app *App) handleRenameRepository(writer http.ResponseWriter, request *http.Request, stored state.Repository, summary repository.Summary, chrome webui.Chrome, session state.Session) {
	writer.Header().Set("Cache-Control", "no-store")
	if !app.parseForm(writer, request) {
		return
	}
	if !constantEqual(session.CSRF, postValue(request, "csrf")) {
		app.renderError(writer, request, http.StatusForbidden, webui.MsgErrCSRF, "")
		return
	}
	form := &renameForm{name: postValue(request, "name")}
	refuse := func(notice webui.Notice, status int) {
		form.notices = append(form.notices, notice)
		app.renderRepositorySettingsPage(writer, request, stored, summary, chrome, "", nil, nil, form, status)
	}
	if _, err := app.confirmAdmin(writer, request, &chrome, false); err != nil {
		refuse(adminPasswordNotice(request, err, "admin_password"))
		return
	}
	renamed, err := app.renameRepository(request, stored.ID, form.name)
	switch {
	case err == nil:
		app.noticeRedirect(writer, request, repositorySettingsURL(renamed.Address)+"?notice="+renamedNotice, http.StatusSeeOther)
	case errors.Is(err, repository.ErrReservedName):
		refuse(webui.Error("name", webui.MsgRepoNameReserved), http.StatusUnprocessableEntity)
	case errors.Is(err, repository.ErrInvalidName):
		refuse(webui.Error("name", webui.MsgRepoNameInvalid), http.StatusUnprocessableEntity)
	case errors.Is(err, repository.ErrNameTaken):
		refuse(webui.Error("name", webui.MsgRepoRenameTaken), http.StatusConflict)
	case errors.Is(err, repository.ErrRepositoryBusy):
		code, _ := busyNotice(err)
		refuse(webui.Error("", code), http.StatusConflict)
	case errors.Is(err, repository.ErrRepositoryNotFound):
		app.renderError(writer, request, http.StatusNotFound, webui.MsgRepoNotFound, stored.Address)
	default:
		refuse(webui.Error("", webui.MsgRepoRenameFailed), unavailable(request, "repository rename", err))
	}
}

// handleSaveNamespaces saves the repository's extra ref namespaces, one per
// line. They apply to pushes that start after the save; a list that is not
// empty warns that their refs have no kept history.
func (app *App) handleSaveNamespaces(writer http.ResponseWriter, request *http.Request, stored state.Repository, summary repository.Summary, chrome webui.Chrome, session state.Session) {
	writer.Header().Set("Cache-Control", "no-store")
	if !app.parseForm(writer, request) {
		return
	}
	if !constantEqual(session.CSRF, postValue(request, "csrf")) {
		app.renderError(writer, request, http.StatusForbidden, webui.MsgErrCSRF, "")
		return
	}
	form := &namespacesForm{text: postValue(request, "extra_ref_prefixes")}
	refuse := func(notice webui.Notice, status int) {
		form.notices = append(form.notices, notice)
		app.renderRepositorySettingsPage(writer, request, stored, summary, chrome, "", nil, form, nil, status)
	}
	if _, err := app.confirmAdmin(writer, request, &chrome, false); err != nil {
		refuse(adminPasswordNotice(request, err, "admin_password"))
		return
	}
	prefixes := strings.Fields(form.text)
	if err := state.ValidateExtraRefPrefixes(prefixes); err != nil {
		refuse(webui.Error("extra_ref_prefixes", webui.MsgNamespacesInvalid), http.StatusUnprocessableEntity)
		return
	}
	_, _, err := app.saveRefPolicy(request.Context(), stored.ID, state.RepositoryRefPolicyChange{ExtraRefPrefixes: &prefixes})
	if errors.As(err, new(*state.PolicyError)) {
		logFailure(request, "repository settings save", err)
		refuse(webui.Error("extra_ref_prefixes", webui.MsgNamespacesChoicesUnreadable), http.StatusConflict)
		return
	}
	if err != nil {
		refuse(webui.Error("", webui.MsgRepoHistoryFailed), unavailable(request, "repository settings save", err))
		return
	}
	notice := "namespaces_saved"
	if len(prefixes) > 0 {
		notice = "namespaces_saved_unkept"
	}
	app.noticeRedirect(writer, request, repositorySettingsURL(stored.Address)+"?notice="+notice, http.StatusSeeOther)
}

// ---------------------------------------------------------------------------
// Delete
// ---------------------------------------------------------------------------

// handleRepositoryDelete serves the confirmation page and its POST. It runs
// before the repository's Git data is read, so a repository whose data can no
// longer be read can still be removed.
//
// Settings decide whether the name must be typed (deleteNameRule). While
// that choice cannot be read, the page says so and a deletion is refused.
func (app *App) handleRepositoryDelete(writer http.ResponseWriter, request *http.Request, stored state.Repository, chrome webui.Chrome, session state.Session) {
	writer.Header().Set("Cache-Control", "no-store")
	name, err := app.deleteNameRule(request)
	if err != nil {
		app.answerUnavailable(writer, request, "delete setting read", err)
		return
	}
	if request.Method == http.MethodGet {
		app.renderRepositoryDelete(writer, request, stored, chrome, "", name, http.StatusOK)
		return
	}
	if !app.parseForm(writer, request) {
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
	if name.Unreadable {
		chrome.Notices = append(chrome.Notices, webui.Error("", webui.MsgDeleteNameUnreadableDelete))
		app.renderRepositoryDelete(writer, request, stored, chrome, mode, name, http.StatusConflict)
		return
	}
	if !deleteNameConfirmed(name, stored.Name, postValue(request, "confirm_name")) {
		chrome.Notices = append(chrome.Notices, webui.Error("confirm_name", webui.MsgRepoDeleteNameMismatch))
	}
	if len(chrome.Notices) != 0 {
		app.renderRepositoryDelete(writer, request, stored, chrome, mode, name, http.StatusUnprocessableEntity)
		return
	}
	if _, err := app.confirmAdmin(writer, request, &chrome, false); err != nil {
		notice, status := adminPasswordNotice(request, err, "admin_password")
		chrome.Notices = append(chrome.Notices, notice)
		app.renderRepositoryDelete(writer, request, stored, chrome, mode, name, status)
		return
	}

	result, incomplete, err := app.deleteRepository(request.Context(), stored.ID, repository.DeleteMode(mode))
	if err != nil && !incomplete {
		code, status := app.deleteFailure(request, stored.ID, err)
		chrome.Notices = append(chrome.Notices, webui.Error("", code))
		app.renderRepositoryDelete(writer, request, stored, chrome, mode, name, status)
		return
	}
	// An incomplete deletion has already removed the repository from OwnGit,
	// so the dashboard reports it as removed, with the file cleanup still
	// owed, rather than as a failure to retry on a page that no longer exists.
	if incomplete {
		logFailure(request, "repository file removal", err)
	}
	app.setRemovedCookie(writer, request, removedResult{Name: stored.Name, ID: stored.ID, Mode: mode, Kept: result.KeptPath, Incomplete: incomplete})
	app.noticeRedirect(writer, request, "/?notice="+removedNotice, http.StatusSeeOther)
}

// deleteRepository deletes repository id in mode once the owner confirmed
// it, for the delete page and the owner API alike. incomplete is true, with
// the cause in err, when the repository is gone from OwnGit but its file
// step is still owed; the next start finishes it.
func (app *App) deleteRepository(ctx context.Context, id string, mode repository.DeleteMode) (result repository.DeleteResult, incomplete bool, err error) {
	// A background activity count holds the repository's read lock for as long
	// as its history walk takes, so counting pauses for the deletion, and the
	// pause forgets what was counted before. The pause ends in a deferred
	// call, so a panic cannot leave it behind.
	result, err = func() (repository.DeleteResult, error) {
		defer app.activity.pause(id, false)()
		// Once confirmed, the deletion must not stop half way because the
		// owner went away: the records go first, and a cancelled context
		// would leave the file step for the next start.
		return app.Repositories.Delete(context.WithoutCancel(ctx), id, mode)
	}()
	if errors.Is(err, repository.ErrDeleteIncomplete) {
		// Delete also reports an older unfinished deletion of this name that
		// it could not resume. A repository that still exists was not
		// removed, so that case is a failure, not a removal.
		_, exists, lookupErr := app.Store.Repository(context.WithoutCancel(ctx), id)
		incomplete = lookupErr == nil && !exists
	}
	return result, incomplete, err
}

// deleteFailure says why a deletion of repository id failed with err, and
// the status that answers it. The cause can name storage paths and the
// deletion token, so it goes to the server log and never into an answer.
func (app *App) deleteFailure(request *http.Request, id string, err error) (webui.MessageCode, int) {
	if busy, ok := busyNotice(err); ok {
		// While the repository is being prepared, no other Git operation
		// can reach it, so the holder is the preparation attempt.
		if busy == webui.MsgRepoBusyInUse && app.Repositories.Preparing(id) {
			busy = webui.MsgRepoBusyPreparing
		}
		return busy, http.StatusConflict
	}
	switch {
	case errors.Is(err, repository.ErrRepositoryNotFound):
		return webui.MsgRepoDeleteGone, http.StatusNotFound
	case errors.Is(err, repository.ErrDeletionRecordMismatch):
		return webui.MsgRepoDeleteFailed, internalError(request, "repository deletion", err)
	default:
		return webui.MsgRepoDeleteFailed, unavailable(request, "repository deletion", err)
	}
}

// deleteNameConfirmed reports whether typed confirms deleting the
// repository called name under rule.
func deleteNameConfirmed(rule webui.DeleteNameRule, name, typed string) bool {
	// Names never contain spaces, so trimming only forgives a pasted space.
	return !rule.Required || strings.TrimSpace(typed) == name
}

// deleteNameRule says whether a deletion asks for the typed name, as saved
// in Settings. A saved choice that cannot be read is logged and reported
// as Unreadable; err is a failure to read the state at all.
func (app *App) deleteNameRule(request *http.Request) (webui.DeleteNameRule, error) {
	required, err := app.Store.DeleteRequiresName(request.Context())
	if errors.As(err, new(*state.PolicyError)) {
		logFailure(request, "delete setting read", err)
		return webui.DeleteNameRule{Required: true, Unreadable: true}, nil
	}
	return webui.DeleteNameRule{Required: required}, err
}

func (app *App) renderRepositoryDelete(writer http.ResponseWriter, request *http.Request, stored state.Repository, chrome webui.Chrome, mode string, name webui.DeleteNameRule, status int) {
	base := app.baseRepositoryPage(request, chrome, stored, repository.Summary{})
	page := webui.RepositoryDeletePage{
		Chrome: chrome, Repo: base.Repo, Tabs: repositoryTabs(base, webui.RepoTabDelete),
		SelfURL: repositoryDeleteURL(stored.Address), SubmitURL: repositoryDeleteURL(stored.Address),
		Mode: mode, Name: name, CancelURL: repositorySettingsURL(stored.Address),
	}
	// The page is administrator only, and the administrator already sees the
	// storage path in the toolbar, so naming the folder adds nothing new.
	if gitPath, err := app.Repositories.Path(stored.ID); err == nil {
		page.GitPath = gitPath
		page.RemovedPath = filepath.Join(filepath.Dir(gitPath), removedFolderName)
	}
	app.render(writer, request, status, page)
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
			webui.Info(webui.MsgRepoRemovedKeptCommand).WithDetail(recoveryCommand(result.Kept, app.cloneURL(request, strings.ToLower(result.Name)))),
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
