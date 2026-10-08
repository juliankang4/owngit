package server

import (
	"errors"
	"net/http"
	"sort"
	"strings"
	"time"

	"owngit/internal/repository"
	"owngit/internal/state"
	"owngit/internal/webui"
)

// The owner API for kept history and restoring files:
//
//	GET  /api/v1/repositories/{id}/kept-history
//	POST /api/v1/repositories/{id}/restore/preview
//	POST /api/v1/repositories/{id}/restore
//
// They need general access, as the browser's restore pages do, and call the
// same repository operations. Applying names the expected_head its preview
// returned, so a restore is refused when the target branch moved after the
// preview. A restore never rewrites history: it adds one commit on top of
// the target branch, or creates a branch that does not exist.

type keptHistoryItem struct {
	// Kind is "branch" or "tag".
	Kind string `json:"kind"`
	// SourceRef is the branch or tag this history was kept from.
	SourceRef string `json:"source_ref"`
	// OID is the kept object: a commit, or an annotated tag.
	OID string `json:"oid"`
	// CommitOID is the commit to restore from. It is empty for a tag that
	// does not point to a commit, which cannot be restored.
	CommitOID  string     `json:"commit_oid,omitempty"`
	Subject    string     `json:"subject,omitempty"`
	AuthorName string     `json:"author_name,omitempty"`
	AuthoredAt *time.Time `json:"authored_at,omitempty"`
	// RestoreTarget is the new branch the dashboard offers for restoring
	// this history, one that does not exist yet.
	RestoreTarget string `json:"restore_target,omitempty"`
}

type keptHistoryResponse struct {
	OK          bool              `json:"ok"`
	Repository  string            `json:"repository"`
	KeptHistory []keptHistoryItem `json:"kept_history"`
}

type restorePreviewInput struct {
	SourceOID    string   `json:"source_oid"`
	TargetBranch string   `json:"target_branch"`
	Mode         string   `json:"mode"`
	Paths        []string `json:"paths,omitempty"`
}

type restoreApplyInput struct {
	restorePreviewInput
	ExpectedHead string `json:"expected_head"`
}

type restoreChange struct {
	Path    string `json:"path"`
	Status  string `json:"status"`
	OldMode string `json:"old_mode"`
	NewMode string `json:"new_mode"`
	// Additions and Deletions are omitted when the counts are unknown, which
	// is the case for a file this computer did not compare as text (see
	// TooLarge); a count of zero is sent.
	Additions *int `json:"additions,omitempty"`
	Deletions *int `json:"deletions,omitempty"`
	// Binary says the file's content is not text.
	Binary bool `json:"binary"`
	// TooLarge says this computer did not compare the file as text because
	// reading it would need more memory than it gives Git at once.
	TooLarge bool `json:"too_large,omitempty"`
}

type restorePreviewJSON struct {
	SourceOID    string   `json:"source_oid"`
	TargetBranch string   `json:"target_branch"`
	TargetRef    string   `json:"target_ref"`
	Mode         string   `json:"mode"`
	Paths        []string `json:"paths,omitempty"`
	// ExpectedHead is the target branch tip the preview was made against,
	// or zeros when the branch does not exist. Applying must name it.
	ExpectedHead string `json:"expected_head"`
	// CreatesBranch says applying creates target_ref at source_oid.
	// Otherwise applying adds one commit with result_tree whose parent is
	// expected_head.
	CreatesBranch bool            `json:"creates_branch"`
	ResultTree    string          `json:"result_tree"`
	CanApply      bool            `json:"can_apply"`
	Changes       []restoreChange `json:"changes"`
}

type restorePreviewResponse struct {
	OK         bool               `json:"ok"`
	Repository string             `json:"repository"`
	Preview    restorePreviewJSON `json:"preview"`
}

type restoreResultJSON struct {
	TargetRef    string `json:"target_ref"`
	PreviousHead string `json:"previous_head"`
	CommitOID    string `json:"commit_oid"`
	Created      bool   `json:"created"`
}

type restoreResultResponse struct {
	OK         bool              `json:"ok"`
	Repository string            `json:"repository"`
	Restore    restoreResultJSON `json:"restore"`
}

func (app *App) handleRestoreAPI(writer http.ResponseWriter, request *http.Request, settings state.Settings, repositoryID, resource, remainder string) {
	get := resource == "kept-history" && remainder == ""
	preview := resource == "restore" && remainder == "preview"
	apply := resource == "restore" && remainder == ""
	switch {
	case !get && !preview && !apply:
		writeAPIError(writer, http.StatusNotFound, "not_found", "The API endpoint does not exist.", nil)
		return
	case get && request.Method != http.MethodGet:
		writeAPIMethodError(writer, http.MethodGet)
		return
	case !get && request.Method != http.MethodPost:
		writeAPIMethodError(writer, http.MethodPost)
		return
	}
	if !app.authorizeAPI(writer, request, settings) || app.refusePreparingAPI(writer, request, repositoryID) {
		return
	}
	if _, exists, err := app.visibleRepository(request, repositoryID); err != nil {
		writeAPIError(writer, unavailable(request, "repository record read", err), "state_unavailable", "Repository metadata could not be read.", nil)
		return
	} else if !exists {
		writeAPIError(writer, http.StatusNotFound, "repository_not_found", "The repository does not exist.", nil)
		return
	}
	switch {
	case get:
		app.keptHistoryAPI(writer, request, repositoryID)
	case preview:
		var input restorePreviewInput
		if !decodeAPIJSON(writer, request, &input) {
			return
		}
		result, err := app.Repositories.PreviewRestore(request.Context(), repositoryID, input.request())
		if err != nil {
			writeRestoreAPIError(writer, request, restorePreviewStep, err)
			return
		}
		writeAPIJSON(writer, http.StatusOK, restorePreviewResponse{OK: true, Repository: repositoryID, Preview: restorePreviewView(input, result)})
	case apply:
		var input restoreApplyInput
		if !decodeAPIJSON(writer, request, &input) {
			return
		}
		if input.ExpectedHead == "" {
			writeAPIError(writer, http.StatusUnprocessableEntity, "invalid_restore", "expected_head is required: pass the expected_head of the preview you reviewed.", nil)
			return
		}
		selection := input.request()
		selection.ExpectedHead = input.ExpectedHead
		result, err := app.Repositories.ApplyRestore(request.Context(), repositoryID, selection)
		if err != nil {
			writeRestoreAPIError(writer, request, restoreApplyStep, err)
			return
		}
		writeAPIJSON(writer, http.StatusOK, restoreResultResponse{OK: true, Repository: repositoryID, Restore: restoreResultJSON{
			TargetRef: "refs/heads/" + input.TargetBranch, PreviousHead: input.ExpectedHead, CommitOID: result.CommitOID, Created: result.Created,
		}})
	}
}

func (input restorePreviewInput) request() repository.RestoreRequest {
	return repository.RestoreRequest{Source: input.SourceOID, Target: input.TargetBranch, Mode: input.Mode, Paths: input.Paths}
}

func restorePreviewView(input restorePreviewInput, preview repository.RestorePreview) restorePreviewJSON {
	view := restorePreviewJSON{
		SourceOID: preview.SourceOID, TargetBranch: input.TargetBranch, TargetRef: preview.TargetRef,
		Mode: input.Mode, Paths: input.Paths, ExpectedHead: preview.ExpectedHead, CreatesBranch: preview.CreatesBranch,
		ResultTree: preview.ResultTree, CanApply: preview.CanApply, Changes: make([]restoreChange, 0, len(preview.Changes)),
	}
	for _, change := range preview.Changes {
		view.Changes = append(view.Changes, restoreChange{
			Path: change.Path, Status: change.Status, OldMode: change.OldMode, NewMode: change.NewMode,
			Additions: lineCount(change.Additions, countsUnknown(change)), Deletions: lineCount(change.Deletions, countsUnknown(change)),
			Binary: change.Binary && !change.TextDiffUnavailable, TooLarge: change.TextDiffUnavailable,
		})
	}
	return view
}

// lineCount returns the changed line count of a file, or nil when the count is
// unknown. It is unknown when this computer did not compare the file as text,
// and when the file held no count record because the read left it out (see
// countsUnknown).
func lineCount(count int, unknown bool) *int {
	if unknown {
		return nil
	}
	return &count
}

// countsUnknown reports whether a file's line counts were never read. A file
// above the memory line is left out of the read, and so is every file of a
// change whose files above the line are more than one command line can leave
// out. A zero count of such a file is not a count of zero lines.
func countsUnknown(file repository.ChangedFile) bool {
	return file.TextDiffUnavailable || !file.CountsRead
}

// keptHistoryAPI lists the repository's kept history as the Overview does,
// newest first, with the branch the dashboard offers to restore each into.
func (app *App) keptHistoryAPI(writer http.ResponseWriter, request *http.Request, repositoryID string) {
	snapshot, err := app.Repositories.RefSnapshot(request.Context(), repositoryID)
	if err != nil {
		writeRestoreAPIError(writer, request, restoreStep{name: "kept history read"}, err)
		return
	}
	retained, err := app.Repositories.RetainedRefs(request.Context(), repositoryID)
	if err != nil {
		writeRestoreAPIError(writer, request, restoreStep{name: "kept history read"}, err)
		return
	}
	sort.SliceStable(retained, func(i, j int) bool {
		left, right := retained[i].Commit.AuthoredAt, retained[j].Commit.AuthoredAt
		if !left.Equal(right) {
			return left.After(right)
		}
		return retained[i].OID < retained[j].OID
	})
	items := make([]keptHistoryItem, 0, len(retained))
	for _, ref := range retained {
		item := keptHistoryItem{Kind: ref.Kind, SourceRef: ref.Source, OID: ref.OID, CommitOID: ref.CommitOID}
		if ref.CommitOID != "" {
			authored := ref.Commit.AuthoredAt
			item.Subject, item.AuthorName, item.AuthoredAt = ref.Commit.Subject, ref.Commit.AuthorName, &authored
			item.RestoreTarget = recoveredTarget(ref.OID, snapshot.Summary.Branches)
		}
		items = append(items, item)
	}
	writeAPIJSON(writer, http.StatusOK, keptHistoryResponse{OK: true, Repository: repositoryID, KeptHistory: items})
}

// writeRestoreAPIError answers err, a failed step, with the status the
// restore pages give it. A refusal changed nothing. Any other failure of an
// apply may have happened after the branch moved, so its answer says to read
// the branch before trying again.
func writeRestoreAPIError(writer http.ResponseWriter, request *http.Request, step restoreStep, err error) {
	switch {
	case errors.Is(err, repository.ErrRepositoryNotFound):
		writeAPIError(writer, http.StatusNotFound, "repository_not_found", "The repository does not exist.", nil)
	case errors.Is(err, repository.ErrRepositoryPreparing):
		writer.Header().Set("Retry-After", "30")
		writeAPIError(writer, unavailable(request, step.name, err), "repository_preparing", "The repository is being prepared. Try again later.", nil)
	case errors.Is(err, repository.ErrStorageChanged):
		logFailure(request, step.name, err)
		writeAPIError(writer, http.StatusConflict, "repository_storage_changed", webui.Text(webui.LangEN, webui.MsgStorageChanged), nil)
	case errors.Is(err, repository.ErrRestoreConflict):
		writeAPIError(writer, http.StatusConflict, "stale_revision", "The target branch changed after the preview. Nothing was restored; preview again before restoring.", nil)
	case errors.Is(err, repository.ErrRestoreNoChanges):
		writeAPIError(writer, http.StatusUnprocessableEntity, "restore_no_changes", "The target branch already has these files. Nothing was restored.", nil)
	case errors.Is(err, repository.ErrRestoreUnsupported):
		writeAPIError(writer, http.StatusUnprocessableEntity, "restore_unsupported", restoreRefusalText(err), nil)
	case errors.Is(err, repository.ErrRestoreInvalid):
		writeAPIError(writer, http.StatusUnprocessableEntity, "invalid_restore", restoreRefusalText(err), nil)
	case step == restoreApplyStep:
		writeAPIError(writer, unavailable(request, step.name, err), "restore_failed", "The restore could not be completed. Read the target branch before trying again.", nil)
	default:
		writeAPIError(writer, unavailable(request, step.name, err), "repository_unavailable", "The repository cannot be read now. Try again later.", nil)
	}
}

// restoreRefusalText is the reason the repository gave for refusing a
// restore selection, such as "source must be a full object ID".
func restoreRefusalText(err error) string {
	text := err.Error()
	if _, reason, found := strings.Cut(text, ": "); found {
		text = reason
	}
	return "The restore was refused: " + text + "."
}
