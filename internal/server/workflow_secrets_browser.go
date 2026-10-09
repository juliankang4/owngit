package server

import (
	"errors"
	"net/http"

	"owngit/internal/repository"
	"owngit/internal/state"
	"owngit/internal/webui"
)

func (app *App) handleWorkflowSecretsPage(writer http.ResponseWriter, request *http.Request, stored state.Repository, summary repository.Summary) {
	adminSession, ok := app.requireAdminPage(writer, request)
	if !ok {
		return
	}
	chrome, err := app.chrome(writer, request, webui.SectionRepository, stored.ID, adminSession.CSRF)
	if err != nil {
		app.answerUnavailable(writer, request, "page frame read", err)
		return
	}
	if request.Method == http.MethodGet {
		page := webui.WorkflowSecretsPage{Confirm: request.URL.Query().Get("remove")}
		app.renderWorkflowSecrets(writer, request, stored, summary, chrome, page, http.StatusOK)
		return
	}
	if request.Method != http.MethodPost {
		app.renderError(writer, request, http.StatusNotFound, webui.MsgErrNotFound, request.URL.Path)
		return
	}
	if !app.parseForm(writer, request) {
		return
	}
	if !constantEqual(adminSession.CSRF, postValue(request, "csrf")) {
		app.renderError(writer, request, http.StatusForbidden, webui.MsgErrCSRF, "")
		return
	}
	action, name := postValue(request, "action"), postValue(request, "name")
	pending := webui.WorkflowSecretsPage{PendingAction: action, PendingName: name}
	if action == webui.ActionSetSecret {
		pending.Name = name
	} else if action == webui.ActionRemoveSecret {
		pending.Confirm = name
	}
	refuse := func(notice webui.Notice, status int) {
		chrome.Notices = append(chrome.Notices, notice)
		app.renderWorkflowSecrets(writer, request, stored, summary, chrome, pending, status)
	}
	if action != webui.ActionSetSecret && action != webui.ActionRemoveSecret {
		refuse(webui.Error("", webui.MsgSettingsUnknownAct), http.StatusBadRequest)
		return
	}
	if _, err := app.confirmAdmin(writer, request, &chrome, false); err != nil {
		notice, status := adminPasswordNotice(request, err, "admin_password")
		refuse(notice, status)
		return
	}
	if err := state.ValidateWorkflowSecretName(name); err != nil {
		refuse(webui.Error("name", "wf.error.secret_name"), http.StatusUnprocessableEntity)
		return
	}
	notice := "workflow_secret_removed"
	if action == webui.ActionSetSecret {
		value := postValue(request, "value")
		if err := state.ValidateWorkflowSecretInput(name, value); err != nil {
			refuse(webui.Error("value", "wf.error.secret_value"), http.StatusUnprocessableEntity)
			return
		}
		_, err = app.Store.SetWorkflowSecret(request.Context(), stored.ID, name, value, state.Actor{Kind: "administrator"}, app.now())
		notice = "workflow_secret_saved"
	} else {
		err = app.Store.RemoveWorkflowSecret(request.Context(), stored.ID, name)
	}
	if err != nil {
		status := unavailable(request, "workflow secret change", err)
		if errors.Is(err, state.ErrRepositoryNotFound) {
			status = http.StatusNotFound
		}
		refuse(webui.Error("", "wf.error.secret_failed"), status)
		return
	}
	app.noticeRedirect(writer, request, workflowSecretsURL(stored.Address)+"?notice="+notice)
}

func (app *App) renderWorkflowSecrets(writer http.ResponseWriter, request *http.Request, stored state.Repository, summary repository.Summary, chrome webui.Chrome, page webui.WorkflowSecretsPage, status int) {
	base := app.baseRepositoryPage(request, chrome, stored, summary)
	page.Chrome, page.Repo, page.Tabs = chrome, base.Repo, repositoryTabs(base, webui.RepoTabChecks)
	page.SubmitURL, page.AutomaticChecksURL = workflowSecretsURL(stored.Address), configuredChecksURL(stored.Address)
	secrets, err := app.Store.ListWorkflowSecrets(request.Context(), stored.ID)
	if err != nil {
		page.Unavailable = true
		if status < http.StatusBadRequest {
			status = unavailable(request, "workflow secret list read", err)
		} else {
			logFailure(request, "workflow secret list read", err)
		}
	}
	for _, secret := range secrets {
		page.Secrets = append(page.Secrets, webui.WorkflowSecretRow{Name: secret.Name, UpdatedAt: secret.UpdatedAt.Local()})
	}
	credentials, err := app.Store.CheckRunnerCredentials(request.Context(), stored.ID)
	page.RunnerTokensMayExist = err != nil
	for _, credential := range credentials {
		page.RunnerTokensMayExist = page.RunnerTokensMayExist || credential.RevokedAt == nil
	}
	app.render(writer, request, status, page)
}
