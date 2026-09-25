package server

import (
	"errors"
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
		app.renderError(writer, request, restoreStatus(err), restoreMessage(err), "")
		return
	}
	app.render(writer, http.StatusOK, page)
}

func (app *App) handleRestorePreview(writer http.ResponseWriter, request *http.Request, stored state.Repository, summary repository.Summary, chrome webui.Chrome) {
	if !parseForm(writer, request) {
		return
	}
	if !app.validCSRF(request, postValue(request, "csrf")) {
		app.renderError(writer, request, http.StatusForbidden, webui.MsgErrCSRF, "")
		return
	}
	selection := restoreFormRequest(request)
	preview, err := app.Repositories.PreviewRestore(request.Context(), stored.ID, selection)
	page, pageErr := app.restorePage(request, stored, summary, chrome, selection, previewOrNil(preview, err), true)
	if pageErr != nil {
		app.renderError(writer, request, restoreStatus(pageErr), restoreMessage(pageErr), "")
		return
	}
	if err != nil {
		page.Chrome.Notices = append(page.Chrome.Notices, webui.Error(restoreField(err), restoreMessage(err)))
		page.Previewed = false
		page.CanApply = false
		app.render(writer, restoreStatus(err), page)
		return
	}
	if !preview.CanApply {
		page.Chrome.Notices = append(page.Chrome.Notices, webui.Info(webui.MsgRestoreNoChanges))
	} else {
		page.Chrome.Notices = append(page.Chrome.Notices, webui.Info(webui.MsgRestoreReady))
	}
	app.render(writer, http.StatusOK, page)
}

func (app *App) handleRestoreApply(writer http.ResponseWriter, request *http.Request, stored state.Repository, summary repository.Summary, chrome webui.Chrome) {
	if !parseForm(writer, request) {
		return
	}
	if !app.validCSRF(request, postValue(request, "csrf")) {
		app.renderError(writer, request, http.StatusForbidden, webui.MsgErrCSRF, "")
		return
	}
	selection := restoreFormRequest(request)
	selection.ExpectedHead = postValue(request, "expected_head")
	if postValue(request, "confirm") != "restore" {
		page, err := app.restorePage(request, stored, summary, chrome, selection, nil, true)
		if err != nil {
			app.renderError(writer, request, restoreStatus(err), restoreMessage(err), "")
			return
		}
		page.Chrome.Notices = append(page.Chrome.Notices, webui.Error("confirm", webui.MsgRestoreInvalid))
		app.render(writer, http.StatusUnprocessableEntity, page)
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

	current, previewErr := app.Repositories.PreviewRestore(request.Context(), stored.ID, repository.RestoreRequest{
		Source: selection.Source, Target: selection.Target, Mode: selection.Mode, Paths: selection.Paths,
	})
	page, pageErr := app.restorePage(request, stored, summary, chrome, selection, previewOrNil(current, previewErr), previewErr == nil)
	if pageErr != nil {
		app.renderError(writer, request, restoreStatus(pageErr), restoreMessage(pageErr), "")
		return
	}
	page.Chrome.Notices = append(page.Chrome.Notices, webui.Error(restoreField(err), restoreMessage(err)))
	page.CanApply = false
	app.render(writer, restoreStatus(err), page)
}

func (app *App) restorePage(request *http.Request, stored state.Repository, summary repository.Summary, chrome webui.Chrome, selection repository.RestoreRequest, selectedPreview *repository.RestorePreview, previewed bool) (webui.RestorePage, error) {
	sourceCommit, _, err := app.Repositories.CommitFiles(request.Context(), stored.ID, selection.Source)
	if err != nil {
		return webui.RestorePage{}, repository.ErrRestoreInvalid
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
			CloneURL: app.baseURL(request) + "/git/" + url.PathEscape(stored.ID) + ".git", Empty: summary.Empty,
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

func restoreStatus(err error) int {
	switch {
	case errors.Is(err, repository.ErrRestoreConflict):
		return http.StatusConflict
	case errors.Is(err, repository.ErrRestoreInvalid), errors.Is(err, repository.ErrRestoreUnsupported), errors.Is(err, repository.ErrRestoreNoChanges):
		return http.StatusUnprocessableEntity
	default:
		return http.StatusServiceUnavailable
	}
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
