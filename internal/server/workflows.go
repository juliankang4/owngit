package server

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"owngit/internal/actions"
	"owngit/internal/checkapi"
	"owngit/internal/checkrun"
	"owngit/internal/repository"
	"owngit/internal/state"
	"owngit/internal/workflows"
)

func workflowQueryAllowed(request *http.Request, resource, remainder string) bool {
	if request.Method != http.MethodGet || remainder != "" {
		return false
	}
	allowed := map[string]bool{}
	switch resource {
	case "workflows":
		allowed["ref"], allowed["path"] = true, true
	case "workflow-runs":
		allowed["limit"] = true
	default:
		return false
	}
	for key, values := range request.URL.Query() {
		if !allowed[key] || len(values) != 1 {
			return false
		}
	}
	return true
}

func (app *App) handleWorkflowAPI(writer http.ResponseWriter, request *http.Request, settings state.Settings, id, resource, remainder string) {
	if !app.authorizeAPI(writer, request, settings) {
		return
	}
	if app.refusePreparingAPI(writer, request, id) {
		return
	}
	switch resource {
	case "workflows":
		app.handleWorkflows(writer, request, id, remainder)
	case "workflow-runs":
		app.handleWorkflowRuns(writer, request, id, remainder)
	default:
		writeAPIError(writer, 404, "not_found", "The API endpoint does not exist.", nil)
	}
}

func (app *App) handleWorkflows(writer http.ResponseWriter, request *http.Request, id, remainder string) {
	service := workflows.Service{Store: app.Store, Repositories: app.Repositories}
	if remainder == "" && request.Method == http.MethodGet {
		result, err := service.Discover(request.Context(), id, request.URL.Query().Get("ref"))
		if err != nil {
			writeWorkflowError(writer, request, err)
			return
		}
		if path := request.URL.Query().Get("path"); path != "" {
			for _, file := range result.Workflows {
				if file.Path == path {
					writeAPIJSON(writer, 200, struct {
						OK        bool           `json:"ok"`
						Ref       string         `json:"ref"`
						SourceOID string         `json:"source_oid"`
						Workflow  workflows.File `json:"workflow"`
					}{true, result.Ref, result.SourceOID, file})
					return
				}
			}
			writeAPIError(writer, 404, "workflow.not_found", "The workflow file does not exist at this branch.", nil)
			return
		}
		writeAPIJSON(writer, 200, result)
		return
	}
	if remainder == "dispatch" && request.Method == http.MethodPost {
		var input workflows.DispatchInput
		if !decodeAPIJSON(writer, request, &input) {
			return
		}
		if input.ExpectedOID != "" && !validOID(input.ExpectedOID) {
			writeAPIError(writer, 422, "workflow.invalid", "expected_oid must be a commit object ID.", nil)
			return
		}
		run, err := service.Dispatch(request.Context(), id, input, generalAccessActor)
		if err != nil {
			writeWorkflowError(writer, request, err)
			return
		}
		app.wakeChecks(id)
		app.writeWorkflowRun(writer, request, run, false)
		return
	}
	if remainder == "" {
		writeAPIMethodError(writer, http.MethodGet)
	} else if remainder == "dispatch" {
		writeAPIMethodError(writer, http.MethodPost)
	} else {
		writeAPIError(writer, 404, "not_found", "The API endpoint does not exist.", nil)
	}
}

type workflowSecretStatus struct {
	Name string `json:"name"`
	Set  bool   `json:"set"`
}
type workflowRunResponse struct {
	OK           bool                     `json:"ok"`
	Run          state.ActionsRunEvidence `json:"run"`
	Jobs         []workflowJobView        `json:"jobs"`
	Secrets      []workflowSecretStatus   `json:"secrets"`
	SecretsKnown bool                     `json:"secrets_known"`
	Deduplicated bool                     `json:"deduplicated,omitempty"`
}

func (app *App) writeWorkflowRun(writer http.ResponseWriter, request *http.Request, run state.ActionsRun, deduped bool) {
	evidence, err := app.Store.ActionsRunEvidence(request.Context(), run)
	if err != nil {
		writeWorkflowError(writer, request, err)
		return
	}
	result := workflowRunResponse{OK: true, Run: evidence, Jobs: []workflowJobView{}, Secrets: []workflowSecretStatus{}, Deduplicated: deduped}
	jobs, err := app.Store.ActionsRunJobs(request.Context(), run.RepositoryID, run.ID)
	if err != nil {
		writeWorkflowError(writer, request, err)
		return
	}
	for _, job := range jobs {
		item, err := app.workflowJobRecord(request, job)
		if err != nil {
			writeWorkflowError(writer, request, err)
			return
		}
		result.Jobs = append(result.Jobs, workflowJobSummary(item))
	}
	// Read metadata only. A damaged secrets file must not look like unset secrets.
	names, err := app.Store.ListWorkflowSecrets(request.Context(), run.RepositoryID)
	result.SecretsKnown = err == nil
	set := map[string]bool{}
	for _, name := range names {
		set[name.Name] = true
	}
	for _, name := range run.Facts.SecretNames {
		result.Secrets = append(result.Secrets, workflowSecretStatus{name, set[strings.ToUpper(name)]})
	}
	writeAPIJSON(writer, 200, result)
}

func (app *App) handleWorkflowRuns(writer http.ResponseWriter, request *http.Request, id, remainder string) {
	if remainder == "" {
		if request.Method != http.MethodGet {
			writeAPIMethodError(writer, http.MethodGet)
			return
		}
		limit := 50
		if value := request.URL.Query().Get("limit"); value != "" {
			parsed, err := strconv.Atoi(value)
			if err != nil || parsed < 1 || parsed > 999 {
				writeAPIError(writer, 400, "invalid_request", "limit must be from 1 to 999.", nil)
				return
			}
			limit = parsed
		}
		runs, err := app.Store.ActionsRuns(request.Context(), id, limit+1)
		if err != nil {
			writeWorkflowError(writer, request, err)
			return
		}
		more := len(runs) > limit
		if more {
			runs = runs[:limit]
		}
		items := []state.ActionsRunSummary{}
		for _, run := range runs {
			item, err := app.Store.ActionsRunSummary(request.Context(), run)
			if err != nil {
				writeWorkflowError(writer, request, err)
				return
			}
			items = append(items, item)
		}
		writeAPIJSON(writer, 200, struct {
			OK        bool                      `json:"ok"`
			Runs      []state.ActionsRunSummary `json:"runs"`
			Truncated bool                      `json:"truncated"`
		}{true, items, more})
		return
	}
	parts := strings.Split(remainder, "/")
	if len(parts) == 0 || !validAttemptID(parts[0]) {
		writeAPIError(writer, 404, "workflow.run_not_found", "The workflow run does not exist.", nil)
		return
	}
	run, found, err := app.Store.ActionsRun(request.Context(), id, parts[0])
	if err != nil {
		writeWorkflowError(writer, request, err)
		return
	}
	if !found {
		writeAPIError(writer, 404, "workflow.run_not_found", "The workflow run does not exist.", nil)
		return
	}
	if len(parts) == 1 {
		if request.Method != http.MethodGet {
			writeAPIMethodError(writer, http.MethodGet)
			return
		}
		app.writeWorkflowRun(writer, request, run, false)
		return
	}
	if len(parts) == 2 && (parts[1] == "cancel" || parts[1] == "rerun") {
		if request.Method != http.MethodPost {
			writeAPIMethodError(writer, http.MethodPost)
			return
		}
		if !decodeAPIJSON(writer, request, &struct{}{}) {
			return
		}
		deduped := false
		if parts[1] == "cancel" {
			run, err = app.Store.CancelActionsRun(request.Context(), id, run.ID, app.now())
		} else {
			coordinator := checkrun.Coordinator{Store: app.Store, Repositories: app.Repositories}
			run, deduped, err = coordinator.RerunActionsRun(request.Context(), id, run.ID)
		}
		if err != nil {
			writeWorkflowError(writer, request, err)
			return
		}
		app.wakeChecks(id)
		app.writeWorkflowRun(writer, request, run, deduped)
		return
	}
	if (len(parts) == 3 || len(parts) == 4 && parts[3] == "log") && parts[1] == "jobs" {
		if request.Method != http.MethodGet {
			writeAPIMethodError(writer, http.MethodGet)
			return
		}
		job, found, err := app.Store.CheckJob(request.Context(), id, parts[2])
		if err != nil {
			writeWorkflowError(writer, request, err)
			return
		}
		if !found || job.RunID != run.ID {
			writeAPIError(writer, 404, "workflow.job_not_found", "The job does not belong to this workflow run.", nil)
			return
		}
		if len(parts) == 3 {
			view, err := app.workflowJobRecord(request, job)
			if err != nil {
				writeWorkflowError(writer, request, err)
				return
			}
			writeAPIJSON(writer, 200, workflowJobResponse{OK: true, RunID: run.ID, workflowJobView: view})
			return
		}
		if job.AttemptID == "" {
			writeAPIError(writer, 404, "check_log_missing", "This job has no attempt log because nothing ran.", nil)
			return
		}
		attempt, err := app.jobAttempt(request.Context(), job)
		if err != nil {
			writeWorkflowError(writer, request, err)
			return
		}
		app.writeCheckAttemptLog(writer, request, attempt)
		return
	}
	writeAPIError(writer, 404, "not_found", "The API endpoint does not exist.", nil)
}

func writeWorkflowError(writer http.ResponseWriter, request *http.Request, err error) {
	var refusal *actions.Refusal
	var problem *workflows.Problem
	switch {
	case errors.As(err, &problem):
		writeAPIError(writer, problem.Status, problem.Code, problem.Message, problem.Details)
	case errors.As(err, &refusal):
		status := http.StatusConflict
		if refusal.Code == "workflow.dispatch_input" {
			status = http.StatusUnprocessableEntity
		}
		writeAPIError(writer, status, refusal.Code, refusal.Detail, refusal.Message)
	case errors.Is(err, repository.ErrPinnedRepositoryBusy):
		writeAPIError(writer, 503, "repository_busy", "The repository is in use. Retry this workflow request later.", nil)
	case errors.Is(err, state.ErrActionsWorkflowsOff), errors.Is(err, state.ErrCheckConsentRequired), errors.Is(err, state.ErrCheckPolicyMissing):
		writeAPIError(writer, 409, "workflow.off", "Workflows are off or need current administrator consent.", nil)
	case errors.Is(err, state.ErrCheckEventNotAllowed):
		writeAPIError(writer, 409, "workflow.event_off", "The check policy does not allow this event.", nil)
	case errors.Is(err, state.ErrInvalidActionsRun):
		writeAPIError(writer, 409, "workflow.invalid", "The workflow run cannot be admitted under the current policy.", nil)
	default:
		writeAPIError(writer, unavailable(request, "workflow state read or admission", err), "state_unavailable", "The workflow state could not be read or changed.", nil)
	}
}

type workflowSecretInfo struct {
	Name      string    `json:"name"`
	UpdatedAt time.Time `json:"updated_at"`
}

func (app *App) handleWorkflowSecrets(writer http.ResponseWriter, request *http.Request, id, name string) {
	if !app.authorizeAdminAPI(writer, request) {
		return
	}
	if name == "" && request.Method == http.MethodGet {
		infos, err := app.Store.ListWorkflowSecrets(request.Context(), id)
		if err != nil {
			writeWorkflowSecretError(writer, request, err)
			return
		}
		items := []workflowSecretInfo{}
		for _, info := range infos {
			items = append(items, workflowSecretInfo{info.Name, info.UpdatedAt})
		}
		writeAPIJSON(writer, 200, struct {
			OK      bool                 `json:"ok"`
			Secrets []workflowSecretInfo `json:"secrets"`
		}{true, items})
		return
	}
	if name != "" && !strings.Contains(name, "/") {
		if err := state.ValidateWorkflowSecretName(name); err != nil {
			writeAPIError(writer, 422, "workflow.secret_invalid", err.Error(), nil)
			return
		}
		switch request.Method {
		case http.MethodPut:
			var input struct {
				Value string `json:"value"`
			}
			if !decodeAPIJSONLimit(writer, request, &input, int64(state.MaxWorkflowSecretValueBytes*6+1024)) {
				return
			}
			if err := state.ValidateWorkflowSecretInput(name, input.Value); err != nil {
				writeAPIError(writer, 422, "workflow.secret_invalid", err.Error(), nil)
				return
			}
			info, err := app.Store.SetWorkflowSecret(request.Context(), id, name, input.Value, state.Actor{Kind: "administrator"}, app.now())
			if err != nil {
				writeWorkflowSecretError(writer, request, err)
				return
			}
			writeAPIJSON(writer, 200, struct {
				OK     bool               `json:"ok"`
				Secret workflowSecretInfo `json:"secret"`
			}{true, workflowSecretInfo{info.Name, info.UpdatedAt}})
		case http.MethodDelete:
			if !decodeAPIJSON(writer, request, &struct{}{}) {
				return
			}
			if err := app.Store.RemoveWorkflowSecret(request.Context(), id, name); err != nil {
				writeWorkflowSecretError(writer, request, err)
				return
			}
			writeAPIJSON(writer, 200, checkapi.OKResponse{OK: true})
		default:
			writeAPIMethodError(writer, http.MethodPut+", "+http.MethodDelete)
		}
		return
	}
	if name == "" {
		writeAPIMethodError(writer, http.MethodGet)
	} else {
		writeAPIError(writer, 404, "not_found", "The API endpoint does not exist.", nil)
	}
}

func writeWorkflowSecretError(writer http.ResponseWriter, request *http.Request, err error) {
	if errors.Is(err, state.ErrRepositoryNotFound) {
		writeAPIError(writer, 404, "repository_not_found", "The repository does not exist.", nil)
		return
	}
	writeAPIError(writer, unavailable(request, "workflow secret metadata or storage", err), "workflow.secret_unavailable", "The secret could not be saved, removed or listed. Check private storage and the repository secret limit.", nil)
}
