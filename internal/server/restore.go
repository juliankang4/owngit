package server

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"owngit/internal/repository"
	"owngit/internal/state"
	"owngit/internal/webui"
)

func (app *App) handleRestoreGet(writer http.ResponseWriter, request *http.Request, stored state.Repository, summary repository.Summary, chrome webui.Chrome) {
	source := request.URL.Query().Get("source")
	if source == "" {
		source = summary.DefaultOID
		if source == "" && len(summary.Branches) != 0 {
			source = summary.Branches[0].OID
		}
	}
	target := restoreTarget(request.URL.Query().Get("target"), summary)
	requestedMode := request.URL.Query().Get("mode")
	mode := requestedMode
	if mode != repository.RestoreFiles {
		mode = repository.RestoreAll
	}
	paths := restorePaths(requestedMode, request.URL.Query()["path"])
	selection := repository.RestoreRequest{Source: source, Target: target, Mode: mode, Paths: paths}
	app.renderRestorePreview(writer, request, stored, summary, chrome, selection, false)
}

func (app *App) handleRestorePreview(writer http.ResponseWriter, request *http.Request, stored state.Repository, summary repository.Summary, chrome webui.Chrome) {
	if !app.parseForm(writer, request) {
		return
	}
	if !app.requireCSRF(writer, request) {
		return
	}
	app.renderRestorePreview(writer, request, stored, summary, chrome, restoreFormRequest(request), true)
}

// renderRestorePreview answers with the restore page for selection and its
// preview, or with the reason the preview failed. previewed shows the
// changes and whether they can be applied, as the answer to a preview
// request.
func (app *App) renderRestorePreview(writer http.ResponseWriter, request *http.Request, stored state.Repository, summary repository.Summary, chrome webui.Chrome, selection repository.RestoreRequest, previewed bool) {
	preview, err := app.Repositories.PreviewRestore(request.Context(), stored.ID, selection)
	page, pageErr := app.restorePage(request, stored, summary, chrome, selection, preview, err, previewed)
	if pageErr != nil {
		app.renderRestorePageError(writer, request, chrome, stored, pageErr)
		return
	}
	status := http.StatusOK
	switch {
	case err != nil:
		status = addRestoreFailure(&page, request, restorePreviewStep, err)
	case !previewed:
	case !preview.CanApply:
		page.Chrome.Notices = append(page.Chrome.Notices, webui.Info(webui.MsgRestoreNoChanges))
	default:
		page.Chrome.Notices = append(page.Chrome.Notices, webui.Info(webui.MsgRestoreReady))
	}
	app.render(writer, request, status, page)
}

func (app *App) handleRestoreApply(writer http.ResponseWriter, request *http.Request, stored state.Repository, summary repository.Summary, chrome webui.Chrome) {
	if !app.parseForm(writer, request) {
		return
	}
	if !app.requireCSRF(writer, request) {
		return
	}
	selection := restoreFormRequest(request)
	selection.ExpectedHead = postValue(request, "expected_head")
	if postValue(request, "confirm") != "restore" {
		preview, previewErr := app.Repositories.PreviewRestore(request.Context(), stored.ID, selection)
		page, err := app.restorePage(request, stored, summary, chrome, selection, preview, previewErr, true)
		if err != nil {
			app.renderRestorePageError(writer, request, chrome, stored, err)
			return
		}
		page.Chrome.Notices = append(page.Chrome.Notices, webui.Error("confirm", webui.MsgRestoreInvalid))
		if previewErr != nil {
			// The missing confirmation answers the request; the failed preview
			// is shown, and logged, beside it.
			addRestoreFailure(&page, request, restorePreviewStep, previewErr)
		}
		app.render(writer, request, http.StatusUnprocessableEntity, page)
		return
	}
	result, err := app.Repositories.ApplyRestore(request.Context(), stored.ID, selection)
	if err == nil {
		location := commitURL(repositoryPath(stored.Address), "refs/heads/"+selection.Target, result.CommitOID, "")
		separator := "?"
		if strings.Contains(location, "?") {
			separator = "&"
		}
		app.noticeRedirect(writer, request, location+separator+"notice=restore_success")
		return
	}

	// The page shows the branch as it is now, so it is previewed again rather
	// than from the observation the failed apply was checked against.
	current, previewErr := app.Repositories.PreviewRestore(request.Context(), stored.ID, repository.RestoreRequest{
		Source: selection.Source, Target: selection.Target, Mode: selection.Mode, Paths: selection.Paths,
	})
	page, pageErr := app.restorePage(request, stored, summary, chrome, selection, current, previewErr, true)
	if pageErr != nil {
		// A restore that failed for a reason other than a refusal cannot
		// promise that the branch stayed as it was, and the reader must hear
		// that first. A refused restore changed nothing, so the page failure
		// is then the news.
		if restoreRefusal(err) == 0 {
			app.renderError(writer, request, restoreStatus(request, restoreApplyStep, err), restoreMessage(restoreApplyStep, err), "")
			return
		}
		app.renderRestorePageError(writer, request, chrome, stored, pageErr)
		return
	}
	status := addRestoreFailure(&page, request, restoreApplyStep, err)
	if previewErr != nil {
		// The failed apply answers the request; the failed preview is shown,
		// and logged, beside it.
		addRestoreFailure(&page, request, restorePreviewStep, previewErr)
	}
	app.render(writer, request, status, page)
}

// restorePage builds the restore page for selection from preview, its
// preview, which failed with previewErr unless that is nil. A source that
// names no commit of the repository is an invalid selection; a read that
// failed is returned as it is. A failed preview of selected files leaves the
// page without a preview, and the caller says why.
func (app *App) restorePage(request *http.Request, stored state.Repository, summary repository.Summary, chrome webui.Chrome, selection repository.RestoreRequest, preview repository.RestorePreview, previewErr error, previewed bool) (webui.RestorePage, error) {
	sourceCommit, _, err := app.Repositories.CommitFiles(request.Context(), stored.ID, selection.Source)
	if errors.Is(err, repository.ErrNotFound) {
		return webui.RestorePage{}, fmt.Errorf("%w: %w", repository.ErrRestoreInvalid, err)
	}
	if err != nil {
		return webui.RestorePage{}, err
	}
	// The paths offered are the whole-tree preview's changes. A whole-tree
	// selection is that preview, so its list, expected head and changes come
	// from one observation of the branch, and the page cannot be built
	// without it.
	available := preview
	if selection.Mode == repository.RestoreAll {
		if previewErr != nil {
			return webui.RestorePage{}, previewErr
		}
	} else {
		available, err = app.Repositories.PreviewRestore(request.Context(), stored.ID, repository.RestoreRequest{
			Source: selection.Source, Target: selection.Target, Mode: repository.RestoreAll,
		})
		if err != nil {
			return webui.RestorePage{}, err
		}
	}
	branch := available
	if previewErr == nil {
		branch = preview
	}
	base := "/repositories/" + url.PathEscape(stored.Address)
	page := webui.RestorePage{
		Chrome: chrome,
		Repo: webui.RepositoryHeader{
			ID: stored.ID, Address: stored.Address, Name: stored.Name, Description: stored.Description, URL: base,
			CloneURL: app.cloneURL(request, stored.Address), Empty: summary.Empty,
		},
		Source: app.commitSummary(repositoryPath(stored.Address), "", sourceCommit), TargetBranch: selection.Target,
		CreatesBranch: branch.CreatesBranch, Mode: selection.Mode, Previewed: previewed && previewErr == nil,
		PreviewURL: base + "/restore/preview", ApplyURL: base + "/restore", CancelURL: base,
		// No section is current: restoring is reached from several of them.
		Tabs: repositoryTabs(app.baseRepositoryPage(request, chrome, stored, summary), ""),
	}
	for _, branch := range summary.Branches {
		page.Branches = append(page.Branches, webui.RefOption{Name: branch.Name, Selected: branch.Name == selection.Target})
	}
	selectedPaths := make(map[string]bool, len(selection.Paths))
	for _, filePath := range selection.Paths {
		selectedPaths[filePath] = true
	}
	for _, change := range available.Changes {
		page.Paths = append(page.Paths, webui.RestorePath{Path: change.Path, Status: change.Status, Selected: selectedPaths[change.Path]})
	}
	if previewErr == nil {
		page.ExpectedHead = preview.ExpectedHead
		page.CanApply = preview.CanApply
		page.DiffTruncated = preview.DiffTruncated
		if previewed {
			for _, change := range preview.Changes {
				item := webui.DiffFile{
					Path: change.Path, Status: change.Status, Additions: change.Additions,
					Deletions: change.Deletions, Binary: change.Binary, TextDiffUnavailable: change.TextDiffUnavailable, Selected: true,
				}
				if patch := preview.Patches[change.Path]; patch != "" {
					item.Hunks = parsePatch(patch)
				}
				page.Changes = append(page.Changes, item)
			}
		}
	}
	return page, nil
}

// renderRestorePageError answers a restore page that could not be built. A
// selection the repository refuses keeps its own message and status. Any
// other failure is a read that could not tell, so the page says the
// repository cannot be read now, as the other repository pages do.
func (app *App) renderRestorePageError(writer http.ResponseWriter, request *http.Request, chrome webui.Chrome, stored state.Repository, err error) {
	if status := restoreRefusal(err); status != 0 {
		app.renderError(writer, request, status, restoreMessage(restorePreviewStep, err), "")
		return
	}
	app.renderRepositoryReadFailure(writer, request, app.baseRepositoryPage(request, chrome, stored, repository.Summary{}), err)
}

// restoreStep is a step of restoring: its name in the server log, and the
// message for a failure of it that is not a refusal.
type restoreStep struct {
	name   string
	failed webui.MessageCode
}

var (
	restorePreviewStep = restoreStep{"restore preview", webui.MsgRestorePreviewFailed}
	restoreApplyStep   = restoreStep{"restore apply", webui.MsgRestoreFailed}
)

// addRestoreFailure shows err, a failed step of request, on page and returns
// the status that answers it. A notice the page already shows is not
// repeated: the preview after a refused apply usually refuses for the same
// reason.
func addRestoreFailure(page *webui.RestorePage, request *http.Request, step restoreStep, err error) int {
	notice := webui.Error(restoreField(err), restoreMessage(step, err))
	if !slices.Contains(page.Chrome.Notices, notice) {
		page.Chrome.Notices = append(page.Chrome.Notices, notice)
	}
	page.CanApply = false
	return restoreStatus(request, step, err)
}

func restoreFormRequest(request *http.Request) repository.RestoreRequest {
	mode := postValue(request, "mode")
	return repository.RestoreRequest{
		Source: postValue(request, "source"), Target: postValue(request, "target"),
		Mode: mode, Paths: restorePaths(mode, request.PostForm["path"]),
	}
}

func restorePaths(mode string, paths []string) []string {
	if mode == repository.RestoreAll {
		return nil
	}
	return append([]string(nil), paths...)
}

func restoreTarget(requested string, summary repository.Summary) string {
	requested = strings.TrimPrefix(requested, "refs/heads/")
	if requested != "" {
		return requested
	}
	if summary.DefaultBranch != "" {
		return summary.DefaultBranch
	}
	if len(summary.Branches) != 0 {
		return summary.Branches[0].Name
	}
	return "main"
}

func restoreURL(address, sourceOID, target, filePath string) string {
	if sourceOID == "" || target == "" {
		return ""
	}
	values := url.Values{"source": {sourceOID}, "target": {target}}
	if filePath != "" {
		values.Set("mode", repository.RestoreFiles)
		values.Add("path", filePath)
	}
	return "/repositories/" + url.PathEscape(address) + "/restore?" + values.Encode()
}

// restoreMessage is the message for err, a failed step: a refusal's own
// message, and otherwise the step's failure message. An empty file selection
// wraps ErrRestoreInvalid, so it is checked first: the commit and the branch
// are fine, and the reader needs to hear what to do instead.
func restoreMessage(step restoreStep, err error) webui.MessageCode {
	switch {
	case errors.Is(err, repository.ErrStorageChanged):
		return webui.MsgStorageChanged
	case errors.Is(err, repository.ErrRestoreFilesNone):
		return webui.MsgRestoreFilesNone
	case errors.Is(err, repository.ErrRestoreConflict):
		return webui.MsgRestoreConflict
	case errors.Is(err, repository.ErrRestoreNoChanges):
		return webui.MsgRestoreNoChanges
	case errors.Is(err, repository.ErrRestoreUnsupported):
		return webui.MsgRestoreUnsupported
	case errors.Is(err, repository.ErrRestoreInvalid):
		return webui.MsgRestoreInvalid
	default:
		return step.failed
	}
}

// restoreStatus is the status that answers err, a failed restore step of
// request: a refusal's own status, and otherwise unavailable.
func restoreStatus(request *http.Request, step restoreStep, err error) int {
	if status := restoreRefusal(err); status != 0 {
		return status
	}
	return unavailable(request, step.name, err)
}

// restoreRefusal is the status of a selection the repository refused, and 0
// for any other failure, which is a read or write that could not be
// completed.
func restoreRefusal(err error) int {
	switch {
	case errors.Is(err, repository.ErrRestoreConflict), errors.Is(err, repository.ErrStorageChanged):
		return http.StatusConflict
	case errors.Is(err, repository.ErrRestoreInvalid), errors.Is(err, repository.ErrRestoreUnsupported), errors.Is(err, repository.ErrRestoreNoChanges):
		return http.StatusUnprocessableEntity
	}
	return 0
}

func restoreField(err error) string {
	if errors.Is(err, repository.ErrRestoreConflict) {
		return "target"
	}
	if errors.Is(err, repository.ErrRestoreUnsupported) || errors.Is(err, repository.ErrRestoreFilesNone) {
		return "path"
	}
	return ""
}
