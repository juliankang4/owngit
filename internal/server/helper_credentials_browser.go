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
	"owngit/internal/requestctx"
	"owngit/internal/state"
	"owngit/internal/webui"
)

func (app *App) handleHelperCredentials(writer http.ResponseWriter, request *http.Request, stored state.Repository, summary repository.Summary, chrome webui.Chrome) {
	writer.Header().Set("Cache-Control", "no-store")
	adminSession, ok := app.requireBrowserAdmin(writer, request)
	if !ok {
		return
	}
	var err error
	chrome, err = app.chrome(writer, request, webui.SectionRepository, stored.ID, adminSession.CSRF)
	if err != nil {
		app.writePlainError(writer, http.StatusServiceUnavailable)
		return
	}
	if request.Method == http.MethodGet {
		app.renderHelperCredentials(writer, request, stored, summary, chrome, "", "", nil, state.HelperCredential{}, "", http.StatusOK)
		return
	}

	// Every credential mutation, including an early refusal, is private and
	// must not be cached. No response path puts a token in a redirect URL.
	writer.Header().Set("Cache-Control", "no-store")
	if !parseForm(writer, request) {
		return
	}
	action := postValue(request, "action")
	credentialID := postValue(request, "credential_id")
	if !constantEqual(adminSession.CSRF, postValue(request, "csrf")) {
		app.renderError(writer, request, http.StatusForbidden, webui.MsgErrCSRF, "")
		return
	}
	if err := app.Auth.VerifyCredential(request.Context(), "admin", postValue(request, "admin_password"), requestctx.Of(request).ClientAddress); err != nil {
		code, status := webui.MsgAdminFailed, http.StatusUnauthorized
		if errors.Is(err, auth.ErrRateLimited) {
			code, status = webui.MsgAdminLocked, http.StatusTooManyRequests
		}
		app.renderHelperCredentials(writer, request, stored, summary, chrome, action, credentialID,
			[]webui.Notice{webui.Error("admin_password", code)}, state.HelperCredential{}, "", status)
		return
	}

	switch action {
	case webui.ActionIssueHelperCredential:
		label := strings.Trim(postValue(request, "label"), " \t")
		if !validBrowserCredentialLabel(label) {
			app.renderHelperCredentials(writer, request, stored, summary, chrome, action, "",
				[]webui.Notice{webui.Error("label", webui.MsgHelperLabelInvalid)}, state.HelperCredential{}, "", http.StatusUnprocessableEntity)
			return
		}
		credential, token, created, err := app.issueHelperCredential(request.Context(), stored.ID, label, "")
		if err != nil || !created || token == "" {
			app.renderHelperCredentials(writer, request, stored, summary, chrome, action, "",
				[]webui.Notice{webui.Error("", webui.MsgHelperFailed)}, state.HelperCredential{}, "", http.StatusServiceUnavailable)
			return
		}
		app.renderHelperCredentials(writer, request, stored, summary, chrome, "", "",
			[]webui.Notice{webui.Success(webui.MsgHelperIssued)}, credential, token, http.StatusOK)
	case webui.ActionRevokeHelperCredential:
		if !validAttemptID(credentialID) {
			app.renderHelperCredentials(writer, request, stored, summary, chrome, action, credentialID,
				[]webui.Notice{webui.Error("", webui.MsgHelperNotFound)}, state.HelperCredential{}, "", http.StatusConflict)
			return
		}
		if err := app.Store.RevokeHelperCredential(request.Context(), stored.ID, credentialID, app.now()); err != nil {
			app.renderHelperCredentials(writer, request, stored, summary, chrome, action, credentialID,
				[]webui.Notice{webui.Error("", webui.MsgHelperNotFound)}, state.HelperCredential{}, "", http.StatusConflict)
			return
		}
		app.noticeRedirect(writer, request, baseHelperCredentialsURL(stored.ID)+"?notice=helper_credential_revoked", http.StatusSeeOther)
	default:
		app.renderHelperCredentials(writer, request, stored, summary, chrome, action, credentialID,
			[]webui.Notice{webui.Error("", webui.MsgHelperFailed)}, state.HelperCredential{}, "", http.StatusBadRequest)
	}
}

func (app *App) renderHelperCredentials(writer http.ResponseWriter, request *http.Request, stored state.Repository, summary repository.Summary, chrome webui.Chrome, pendingAction, pendingCredentialID string, notices []webui.Notice, issued state.HelperCredential, issuedToken string, status int) {
	credentials, err := app.Store.HelperCredentials(request.Context(), stored.ID)
	if err != nil {
		app.renderError(writer, request, http.StatusServiceUnavailable, webui.MsgHelperFailed, "")
		return
	}
	basePage := app.baseRepositoryPage(request, chrome, stored, summary)
	self := baseHelperCredentialsURL(stored.ID)
	page := webui.HelperCredentialsPage{
		Chrome:              chrome,
		Repo:                basePage.Repo,
		Tabs:                repositoryTabs(basePage, webui.RepoTabChecks),
		SelfURL:             self,
		SubmitURL:           self,
		PendingAction:       pendingAction,
		PendingCredentialID: pendingCredentialID,
		Issued:              browserHelperCredential(issued),
		IssuedToken:         issuedToken,
	}
	if notices != nil {
		page.Chrome.Notices = notices
	}
	for _, credential := range credentials {
		page.Credentials = append(page.Credentials, browserHelperCredential(credential))
	}
	app.render(writer, status, page)
}

func (app *App) requireBrowserAdmin(writer http.ResponseWriter, request *http.Request) (state.Session, bool) {
	writer.Header().Set("Cache-Control", "no-store")
	if session, ok := app.cookieSession(request, "admin", adminCookie); ok {
		return session, true
	}
	http.Redirect(writer, request, "/admin/login?next="+url.QueryEscape(loginNext(request)), http.StatusSeeOther)
	return state.Session{}, false
}

func (app *App) issueHelperCredential(ctx context.Context, repositoryID, label, creationID string) (state.HelperCredential, string, bool, error) {
	token, err := auth.RandomToken(32)
	if err != nil {
		return state.HelperCredential{}, "", false, err
	}
	hash := sha256.Sum256([]byte(token))
	credential, created, err := app.Store.CreateHelperCredential(ctx, repositoryID, label, creationID, hash[:], app.now())
	if err != nil || !created {
		token = ""
	}
	return credential, token, created, err
}

func browserHelperCredential(credential state.HelperCredential) webui.HelperCredentialRow {
	row := webui.HelperCredentialRow{
		ID:        credential.ID,
		ShortID:   shortOpaqueID(credential.ID),
		Label:     credential.Label,
		CreatedAt: credential.CreatedAt,
		Revoked:   credential.RevokedAt != nil,
	}
	if credential.RevokedAt != nil {
		row.RevokedAt = *credential.RevokedAt
	}
	if credential.LastUsedAt != nil {
		row.LastUsedAt = *credential.LastUsedAt
	}
	return row
}

func validBrowserCredentialLabel(label string) bool {
	return label != "" && len(label) <= 100 && utf8.ValidString(label) && !strings.ContainsAny(label, "\x00\r\n")
}

func baseHelperCredentialsURL(repositoryID string) string {
	return "/repositories/" + url.PathEscape(repositoryID) + "/helper-credentials"
}
