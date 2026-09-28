package server

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
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
	page, err := app.restorePage(request, stored, summary, chrome, selection, nil, false)
	if err != nil {
		app.renderRestorePageError(writer, request, chrome, stored, err)
		return
	}
	app.render(writer, request, http.StatusOK, page)
}

func (app *App) handleRestorePreview(writer http.ResponseWriter, request *http.Request, stored state.Repository, summary repository.Summary, chrome webui.Chrome) {
	if !parseForm(writer, request) {
		return
	}
	if !app.requireCSRF(writer, request) {
		return
	}
	selection := restoreFormRequest(request)
	preview, err := app.Repositories.PreviewRestore(request.Context(), stored.ID, selection)
	page, pageErr := app.restorePage(request, stored, summary, chrome, selection, previewOrNil(preview, err), true)
	if pageErr != nil {
		app.renderRestorePageError(writer, request, chrome, stored, pageErr)
		return
	}
	if err != nil {
		page.Chrome.Notices = append(page.Chrome.Notices, webui.Error(restoreField(err), restoreMessage(err)))
		page.Previewed = false
		page.CanApply = false
		app.render(writer, request, restoreStatus(request, "restore preview", err), page)
		return
	}
	if !preview.CanApply {
		page.Chrome.Notices = append(page.Chrome.Notices, webui.Info(webui.MsgRestoreNoChanges))
	} else {
		page.Chrome.Notices = append(page.Chrome.Notices, webui.Info(webui.MsgRestoreReady))
	}
	app.render(writer, request, http.StatusOK, page)
}

func (app *App) handleRestoreApply(writer http.ResponseWriter, request *http.Request, stored state.Repository, summary repository.Summary, chrome webui.Chrome) {
	if !parseForm(writer, request) {
		return
	}
	if !app.requireCSRF(writer, request) {
		return
	}
	selection := restoreFormRequest(request)
	selection.ExpectedHead = postValue(request, "expected_head")
	if postValue(request, "confirm") != "restore" {
		page, err := app.restorePage(request, stored, summary, chrome, selection, nil, true)
		if err != nil {
			app.renderRestorePageError(writer, request, chrome, stored, err)
			return
		}
		page.Chrome.Notices = append(page.Chrome.Notices, webui.Error("confirm", webui.MsgRestoreInvalid))
		app.render(writer, request, http.StatusUnprocessableEntity, page)
		return
	}
	result, err := app.Repositories.ApplyRestore(request.Context(), stored.ID, selection)
	if err == nil {
		location := commitURL(stored.ID, "refs/heads/"+selection.Target, result.CommitOID, "")
		separator := "?"
		if strings.Contains(location, "?") {
			separator = "&"
		}
		app.noticeRedirect(writer, request, location+separator+"notice=restore_success", http.StatusSeeOther)
		return
	}
	status := restoreStatus(request, "restore apply", err)

	current, previewErr := app.Repositories.PreviewRestore(request.Context(), stored.ID, repository.RestoreRequest{
		Source: selection.Source, Target: selection.Target, Mode: selection.Mode, Paths: selection.Paths,
	})
	page, pageErr := app.restorePage(request, stored, summary, chrome, selection, previewOrNil(current, previewErr), previewErr == nil)
	if pageErr != nil {
		// A restore that failed for a reason other than a refusal cannot
		// promise that the branch stayed as it was, and the reader must hear
		// that first. A refused restore changed nothing, so the page failure
		// is then the news.
		if restoreRefusal(err) == 0 {
			app.renderError(writer, request, status, restoreMessage(err), "")
			return
		}
		app.renderRestorePageError(writer, request, chrome, stored, pageErr)
		return
	}
	page.Chrome.Notices = append(page.Chrome.Notices, webui.Error(restoreField(err), restoreMessage(err)))
	page.CanApply = false
	app.render(writer, request, status, page)
}

// restorePage builds the restore page for selection. A source that names no
// commit of the repository is an invalid selection; a read that failed is
// returned as it is.
func (app *App) restorePage(request *http.Request, stored state.Repository, summary repository.Summary, chrome webui.Chrome, selection repository.RestoreRequest, selectedPreview *repository.RestorePreview, previewed bool) (webui.RestorePage, error) {
	sourceCommit, _, err := app.Repositories.CommitFiles(request.Context(), stored.ID, selection.Source)
	if errors.Is(err, repository.ErrNotFound) {
		return webui.RestorePage{}, fmt.Errorf("%w: %w", repository.ErrRestoreInvalid, err)
	}
	if err != nil {
		return webui.RestorePage{}, err
	}
	available, err := app.Repositories.PreviewRestore(request.Context(), stored.ID, repository.RestoreRequest{
		Source: selection.Source, Target: selection.Target, Mode: repository.RestoreAll,
	})
	if err != nil {
		return webui.RestorePage{}, err
	}
	if selectedPreview == nil {
		if selection.Mode == repository.RestoreAll {
			selectedPreview = &available
		} else if candidate, previewErr := app.Repositories.PreviewRestore(request.Context(), stored.ID, selection); previewErr == nil {
			selectedPreview = &candidate
		}
	}
	branchPreview := selectedPreview
	if branchPreview == nil {
		branchPreview = &available
	}
	base := "/repositories/" + url.PathEscape(stored.ID)
	page := webui.RestorePage{
		Chrome: chrome,
		Repo: webui.RepositoryHeader{
			ID: stored.ID, Name: stored.Name, Description: stored.Description, URL: base,
			CloneURL: app.cloneURL(request, stored.ID), Empty: summary.Empty,
		},
		Source: app.commitSummary(stored.ID, "", sourceCommit), TargetBranch: selection.Target,
		CreatesBranch: branchPreview.CreatesBranch, Mode: selection.Mode, Previewed: previewed,
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
	if selectedPreview != nil {
		page.ExpectedHead = selectedPreview.ExpectedHead
		page.CanApply = selectedPreview.CanApply
		page.DiffTruncated = selectedPreview.DiffTruncated
		if previewed {
			for _, change := range selectedPreview.Changes {
				item := webui.DiffFile{
					Path: change.Path, Status: change.Status, Additions: change.Additions,
					Deletions: change.Deletions, Binary: change.Binary, Selected: true,
				}
				if patch := selectedPreview.Patches[change.Path]; patch != "" {
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
		app.renderError(writer, request, status, restoreMessage(err), "")
		return
	}
	app.renderRepositoryReadFailure(writer, request, chrome, stored, err)
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

func restoreURL(repositoryID, sourceOID, target, filePath string) string {
	if sourceOID == "" || target == "" {
		return ""
	}
	values := url.Values{"source": {sourceOID}, "target": {target}}
	if filePath != "" {
		values.Set("mode", repository.RestoreFiles)
		values.Add("path", filePath)
	}
	return "/repositories/" + url.PathEscape(repositoryID) + "/restore?" + values.Encode()
}

func previewOrNil(preview repository.RestorePreview, err error) *repository.RestorePreview {
	if err != nil {
		return nil
	}
	return &preview
}

func restoreMessage(err error) webui.MessageCode {
	switch {
	case errors.Is(err, repository.ErrRestoreConflict):
		return webui.MsgRestoreConflict
	case errors.Is(err, repository.ErrRestoreNoChanges):
		return webui.MsgRestoreNoChanges
	case errors.Is(err, repository.ErrRestoreUnsupported):
		return webui.MsgRestoreUnsupported
	case errors.Is(err, repository.ErrRestoreInvalid):
		return webui.MsgRestoreInvalid
	default:
		return webui.MsgRestoreFailed
	}
}

// restoreStatus is the status that answers err, a failed restore step of
// request: a refusal's own status, and otherwise unavailable.
func restoreStatus(request *http.Request, step string, err error) int {
	if status := restoreRefusal(err); status != 0 {
		return status
	}
	return unavailable(request, step, err)
}

// restoreRefusal is the status of a selection the repository refused, and 0
// for any other failure, which is a read or write that could not be
// completed.
func restoreRefusal(err error) int {
	switch {
	case errors.Is(err, repository.ErrRestoreConflict):
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
	if errors.Is(err, repository.ErrRestoreUnsupported) {
		return "path"
	}
	return ""
}
