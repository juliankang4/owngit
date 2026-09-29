package server

import (
	"context"
	"crypto/sha256"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"unicode/utf8"

	"owngit/internal/auth"
	"owngit/internal/repository"
	"owngit/internal/state"
	"owngit/internal/webui"
)

func (app *App) handleHelperCredentials(writer http.ResponseWriter, request *http.Request, stored state.Repository, summary repository.Summary, chrome webui.Chrome) {
	writer.Header().Set("Cache-Control", "no-store")
	adminSession, ok := app.requireAdminPage(writer, request)
	if !ok {
		return
	}
	var err error
	chrome, err = app.chrome(writer, request, webui.SectionRepository, stored.ID, adminSession.CSRF)
	if err != nil {
		app.answerUnavailable(writer, request, "page frame read", err)
		return
	}
	if request.Method == http.MethodGet {
		app.renderHelperCredentials(writer, request, stored, summary, chrome, "", "", "", nil, http.StatusOK)
		return
	}

	// Every credential mutation, including an early refusal, is private and
	// must not be cached. No response path puts a token in a redirect URL.
	writer.Header().Set("Cache-Control", "no-store")
	if !app.parseForm(writer, request) {
		return
	}
	action := postValue(request, "action")
	credentialID := postValue(request, "credential_id")
	// The label is not sensitive, so a refusal can hand it back rather than
	// making the operator type it again.
	label := strings.Trim(postValue(request, "label"), " \t")
	if !constantEqual(adminSession.CSRF, postValue(request, "csrf")) {
		app.renderError(writer, request, http.StatusForbidden, webui.MsgErrCSRF, "")
		return
	}
	if _, err := app.confirmAdmin(writer, request, &chrome, false); err != nil {
		notice, status := adminPasswordNotice(request, err, "admin_password")
		app.renderHelperCredentials(writer, request, stored, summary, chrome, action, credentialID, label,
			[]webui.Notice{notice}, status)
		return
	}

	switch action {
	case webui.ActionIssueHelperCredential:
		if !validBrowserCredentialLabel(label) {
			app.renderHelperCredentials(writer, request, stored, summary, chrome, action, "", label,
				[]webui.Notice{webui.Error("label", webui.MsgHelperLabelInvalid)}, http.StatusUnprocessableEntity)
			return
		}
		// The list is read before issuing. Once the token exists, this
		// response is its only delivery, so no later read may replace it.
		credentials, err := app.Store.HelperCredentials(request.Context(), stored.ID)
		if err != nil {
			app.renderError(writer, request, unavailable(request, "helper credential list read", err), webui.MsgHelperFailed, "")
			return
		}
		// Without a creation identity every success creates a credential and
		// its token, so an error is the only other outcome.
		credential, token, _, err := app.issueHelperCredential(request.Context(), stored.ID, label, "")
		if err != nil {
			app.renderHelperCredentials(writer, request, stored, summary, chrome, action, "", label,
				[]webui.Notice{webui.Error("", webui.MsgHelperFailed)}, unavailable(request, "helper credential issue", err))
			return
		}
		page := app.helperCredentialsPage(request, stored, summary, chrome, append(credentials, credential))
		page.Chrome.Notices = []webui.Notice{webui.Success(webui.MsgHelperIssued)}
		page.Issued, page.IssuedToken = browserHelperCredential(credential), token
		app.render(writer, request, http.StatusOK, page)
	case webui.ActionRevokeHelperCredential:
		if !validAttemptID(credentialID) {
			app.renderHelperCredentials(writer, request, stored, summary, chrome, action, credentialID, "",
				[]webui.Notice{webui.Error("", webui.MsgHelperNotFound)}, http.StatusConflict)
			return
		}
		if err := app.Store.RevokeHelperCredential(request.Context(), stored.ID, credentialID, app.now()); errors.Is(err, state.ErrHelperCredentialRevoked) {
			app.renderHelperCredentials(writer, request, stored, summary, chrome, action, credentialID, "",
				[]webui.Notice{webui.Error("", webui.MsgHelperNotFound)}, http.StatusConflict)
			return
		} else if err != nil {
			app.renderHelperCredentials(writer, request, stored, summary, chrome, action, credentialID, "",
				[]webui.Notice{webui.Error("", webui.MsgHelperFailed)}, unavailable(request, "helper credential revoke", err))
			return
		}
		app.noticeRedirect(writer, request, baseHelperCredentialsURL(stored.ID)+"?notice=helper_credential_revoked", http.StatusSeeOther)
	default:
		app.renderHelperCredentials(writer, request, stored, summary, chrome, action, credentialID, "",
			[]webui.Notice{webui.Error("", webui.MsgHelperFailed)}, http.StatusBadRequest)
	}
}

// renderHelperCredentials draws the screen with the current list, for a visit
// or a refusal. pendingLabel is the label the operator typed; it is put back
// into the form so a refusal does not make them type it again.
func (app *App) renderHelperCredentials(writer http.ResponseWriter, request *http.Request, stored state.Repository, summary repository.Summary, chrome webui.Chrome, pendingAction, pendingCredentialID, pendingLabel string, notices []webui.Notice, status int) {
	credentials, err := app.Store.HelperCredentials(request.Context(), stored.ID)
	if err != nil {
		app.renderError(writer, request, unavailable(request, "helper credential list read", err), webui.MsgHelperUnreadable, "")
		return
	}
	page := app.helperCredentialsPage(request, stored, summary, chrome, credentials)
	page.PendingAction, page.PendingCredentialID, page.PendingLabel = pendingAction, pendingCredentialID, pendingLabel
	if notices != nil {
		page.Chrome.Notices = notices
	}
	app.render(writer, request, status, page)
}

func (app *App) helperCredentialsPage(request *http.Request, stored state.Repository, summary repository.Summary, chrome webui.Chrome, credentials []state.HelperCredential) webui.HelperCredentialsPage {
	basePage := app.baseRepositoryPage(request, chrome, stored, summary)
	self := baseHelperCredentialsURL(stored.ID)
	page := webui.HelperCredentialsPage{
		Chrome:    chrome,
		Repo:      basePage.Repo,
		Tabs:      repositoryTabs(basePage, webui.RepoTabChecks),
		SelfURL:   self,
		SubmitURL: self,
	}
	for _, credential := range credentials {
		page.Credentials = append(page.Credentials, browserHelperCredential(credential))
	}
	return page
}

func (app *App) issueHelperCredential(ctx context.Context, repositoryID, label, creationID string) (state.HelperCredential, string, bool, error) {
	token := auth.RandomToken(32)
	hash := sha256.Sum256([]byte(token))
	credential, created, err := app.Store.CreateHelperCredential(ctx, repositoryID, label, creationID, hash[:], app.now())
	if err != nil || !created {
		token = ""
	}
	return credential, token, created, err
}

// browserHelperCredential builds one row. Every time is shown in the server's
// local time, as a row read from the state is; a credential just issued
// carries whatever location its producer gave it.
func browserHelperCredential(credential state.HelperCredential) webui.HelperCredentialRow {
	row := webui.HelperCredentialRow{
		ID:        credential.ID,
		ShortID:   shortOpaqueID(credential.ID),
		Label:     credential.Label,
		CreatedAt: credential.CreatedAt.Local(),
		Revoked:   credential.RevokedAt != nil,
	}
	if credential.RevokedAt != nil {
		row.RevokedAt = credential.RevokedAt.Local()
	}
	if credential.LastUsedAt != nil {
		row.LastUsedAt = credential.LastUsedAt.Local()
	}
	return row
}

func validBrowserCredentialLabel(label string) bool {
	return label != "" && len(label) <= 100 && utf8.ValidString(label) && !strings.ContainsAny(label, "\x00\r\n")
}

func baseHelperCredentialsURL(repositoryID string) string {
	return "/repositories/" + url.PathEscape(repositoryID) + "/helper-credentials"
}
