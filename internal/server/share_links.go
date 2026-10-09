package server

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"owngit/internal/auth"
	"owngit/internal/repository"
	"owngit/internal/state"
	"owngit/internal/webui"
)

// The administrator's side of share links: the repository's Share links
// screen and the owner API (/api/v1/repositories/{name}/share-links). Both
// create links through createShareLink and describe them the same way.

// shareLinkInput is what creating a share link takes.
type shareLinkInput struct {
	label, scope string
	// days is how long the link works; 0 keeps it until it is revoked.
	days     int
	password string
}

// maximumShareDays bounds how far ahead a link's expiry can be set.
const maximumShareDays = 3650

var (
	errShareLabel  = errors.New("share link label is not valid")
	errShareChoice = errors.New("share link scope or expiry is not valid")
)

// createShareLink creates a link for repository id and returns it with its
// secret, which exists only in this answer. A refused input is
// errShareLabel, errShareChoice, or the auth password rule's error.
func (app *App) createShareLink(ctx context.Context, id string, input shareLinkInput) (state.ShareLink, string, error) {
	label := strings.TrimSpace(input.label)
	if !state.ValidShareLabel(label) {
		return state.ShareLink{}, "", errShareLabel
	}
	if (input.scope != state.ShareBrowse && input.scope != state.ShareClone) || input.days < 0 || input.days > maximumShareDays {
		return state.ShareLink{}, "", errShareChoice
	}
	link := state.ShareLink{
		RepositoryID: id, Scope: input.scope, Label: label,
		CreatedBy: state.Actor{Kind: state.ActorAdministrator}, CreatedAt: app.now(),
	}
	if input.days > 0 {
		expires := link.CreatedAt.Add(time.Duration(input.days) * 24 * time.Hour)
		link.ExpiresAt = &expires
	}
	if input.password != "" {
		hash, err := app.Auth.HashPassword(ctx, input.password)
		if err != nil {
			return state.ShareLink{}, "", err
		}
		link.PasswordHash = hash
	}
	secret := auth.RandomToken(32)
	link, err := app.Store.CreateShareLink(ctx, link, shareSecretHash(secret))
	if err != nil {
		return state.ShareLink{}, "", err
	}
	return link, secret, nil
}

// shareWarnings say what a link's choices allow: no expiry, cloning, no
// extra password. They are the owner's choices to make; OwnGit says what
// each means and does not refuse it.
func shareWarnings(link state.ShareLink) []webui.MessageCode {
	var warnings []webui.MessageCode
	if link.ExpiresAt == nil {
		warnings = append(warnings, webui.MsgShareWarnNever)
	}
	if link.Scope == state.ShareClone {
		warnings = append(warnings, webui.MsgShareWarnClone)
	}
	if link.PasswordHash == "" {
		warnings = append(warnings, webui.MsgShareWarnNoPassword)
	}
	return warnings
}

// shareState names what a link does at now: "active", "expired" or
// "revoked".
func (app *App) shareState(link state.ShareLink) string {
	switch {
	case link.RevokedAt != nil:
		return "revoked"
	case !link.Active(app.now()):
		return "expired"
	}
	return "active"
}

func shareLinksURL(address string) string {
	return repositoryPath(address) + "/share-links"
}

// handleShareLinks answers the Share links screen, which the route opens
// for an administrator only. Creating and revoking ask for the
// administrator confirmation as every repository change does.
func (app *App) handleShareLinks(writer http.ResponseWriter, request *http.Request, stored state.Repository, chrome webui.Chrome, session state.Session) {
	writer.Header().Set("Cache-Control", "no-store")
	base := app.baseRepositoryPage(request, chrome, stored, repository.Summary{})
	page := webui.ShareLinksPage{
		Chrome: chrome, Repo: base.Repo, Tabs: repositoryTabs(base, webui.RepoTabSettings),
		SubmitURL: shareLinksURL(stored.Address),
		Form:      webui.ShareLinkForm{Scope: state.ShareBrowse, Expiry: webui.ShareExpiryDefault},
	}
	status := http.StatusOK
	if request.Method == http.MethodPost {
		if !app.parseForm(writer, request) {
			return
		}
		if !constantEqual(session.CSRF, postValue(request, "csrf")) {
			app.renderError(writer, request, http.StatusForbidden, webui.MsgErrCSRF, "")
			return
		}
		var done bool
		switch postValue(request, "action") {
		case webui.ActionCreateShareLink:
			status = app.createShareLinkForm(writer, request, stored, &page)
		case webui.ActionRevokeShareLink:
			status, done = app.revokeShareLinkForm(writer, request, stored, &page)
		default:
			app.renderError(writer, request, http.StatusBadRequest, webui.MsgSettingsUnknownAct, "")
			return
		}
		if done {
			return
		}
	}
	links, err := app.Store.ShareLinks(request.Context(), stored.ID)
	if err != nil {
		app.renderError(writer, request, unavailable(request, "share link list read", err), webui.MsgShareUnreadable, "")
		return
	}
	for _, link := range links {
		page.Links = append(page.Links, app.shareLinkRow(link))
	}
	app.render(writer, request, status, page)
}

// createShareLinkForm creates a link from the form and puts it, or the
// refusal, on page. The form's password is never shown again.
func (app *App) createShareLinkForm(writer http.ResponseWriter, request *http.Request, stored state.Repository, page *webui.ShareLinksPage) int {
	page.Form = webui.ShareLinkForm{Label: postValue(request, "label"), Scope: postValue(request, "scope"), Expiry: postValue(request, "expiry")}
	if _, err := app.confirmAdmin(writer, request, &page.Chrome, false); err != nil {
		notice, status := adminPasswordNotice(request, err, "admin_password")
		page.FormNotices = append(page.FormNotices, notice)
		return status
	}
	input := shareLinkInput{label: page.Form.Label, scope: page.Form.Scope, password: postValue(request, "share_password"), days: shareFormDays(page.Form.Expiry)}
	link, secret, err := app.createShareLink(request.Context(), stored.ID, input)
	switch {
	case err == nil:
	case errors.Is(err, errShareLabel):
		page.FormNotices = append(page.FormNotices, webui.Error("label", webui.MsgShareLabelInvalid))
		return http.StatusUnprocessableEntity
	case errors.Is(err, errShareChoice):
		page.Form.Scope, page.Form.Expiry = state.ShareBrowse, webui.ShareExpiryDefault
		page.FormNotices = append(page.FormNotices, webui.Error("", webui.MsgShareChoiceInvalid))
		return http.StatusUnprocessableEntity
	case errors.Is(err, auth.ErrPasswordTooShort), errors.Is(err, auth.ErrPasswordTooLong):
		page.FormNotices = append(page.FormNotices, webui.Error("share_password", webui.MsgSharePasswordRule))
		return http.StatusUnprocessableEntity
	default:
		page.FormNotices = append(page.FormNotices, webui.Error("", webui.MsgShareFailed))
		return unavailable(request, "share link creation", err)
	}
	created := &webui.CreatedShareLink{Label: link.Label, URL: app.serverOrigin(request) + sharePrefix + secret, Warnings: shareWarnings(link)}
	if link.Scope == state.ShareClone {
		created.CloneURL, created.CloneHelp = app.shareCloneURL(request, link)
	}
	created.PublicURL, created.PublicCloneURL = app.publicShareURLs(link, secret)
	page.Created = created
	page.Form = webui.ShareLinkForm{Scope: state.ShareBrowse, Expiry: webui.ShareExpiryDefault}
	page.Chrome.Notices = append(page.Chrome.Notices, webui.Success(webui.MsgShareCreatedNotice))
	return http.StatusOK
}

// shareFormDays reads the create form's expiry: a number of days that
// webui.ShareExpiryChoices offers, or 0 for webui.ShareExpiryNever.
// Anything else is -1, which createShareLink refuses.
func shareFormDays(choice string) int {
	if choice == webui.ShareExpiryNever {
		return 0
	}
	for _, offered := range webui.ShareExpiryChoices {
		if days, err := strconv.Atoi(offered); err == nil && offered == choice {
			return days
		}
	}
	return -1
}

// revokeShareLinkForm revokes the form's link and returns to the screen,
// or puts the refusal on the link's row.
func (app *App) revokeShareLinkForm(writer http.ResponseWriter, request *http.Request, stored state.Repository, page *webui.ShareLinksPage) (int, bool) {
	page.RevokeID = postValue(request, "link_id")
	if _, err := app.confirmAdmin(writer, request, &page.Chrome, false); err != nil {
		notice, status := adminPasswordNotice(request, err, "admin_password")
		page.RevokeNotices = append(page.RevokeNotices, notice)
		return status, false
	}
	_, err := app.Store.RevokeShareLink(request.Context(), stored.ID, page.RevokeID, app.now())
	switch {
	case err == nil:
		app.noticeRedirect(writer, request, shareLinksURL(stored.Address)+"?notice=share_link_revoked")
		return 0, true
	case errors.Is(err, state.ErrShareLinkNotFound):
		page.RevokeNotices = append(page.RevokeNotices, webui.Error("", webui.MsgShareNotActive))
		return http.StatusConflict, false
	}
	page.RevokeNotices = append(page.RevokeNotices, webui.Error("", webui.MsgShareFailed))
	return unavailable(request, "share link revoke", err), false
}

// shareLinkRow is one link on the screen. Times are shown in the server's
// local time.
func (app *App) shareLinkRow(link state.ShareLink) webui.ShareLinkRow {
	row := webui.ShareLinkRow{
		ID: link.ID, ShortID: shortOpaqueID(link.ID), Label: link.Label, Scope: link.Scope, HasPassword: link.PasswordHash != "",
		CreatedAt: link.CreatedAt.Local(), State: app.shareState(link),
	}
	if link.ExpiresAt != nil {
		row.ExpiresAt = link.ExpiresAt.Local()
	}
	if link.RevokedAt != nil {
		row.RevokedAt = link.RevokedAt.Local()
	}
	if link.LastUsedAt != nil {
		row.LastUsedAt = link.LastUsedAt.Local()
	}
	return row
}

// shareLinkJSON is the owner API's view of a link. It never holds the
// secret or the password.
type shareLinkJSON struct {
	ID          string     `json:"id"`
	Label       string     `json:"label"`
	Scope       string     `json:"scope"`
	HasPassword bool       `json:"has_password"`
	State       string     `json:"state"`
	VisitorPath string     `json:"visitor_path"`
	CreatedAt   time.Time  `json:"created_at"`
	ExpiresAt   *time.Time `json:"expires_at"`
	RevokedAt   *time.Time `json:"revoked_at,omitempty"`
	LastUsedAt  *time.Time `json:"last_used_at"`
}

func (app *App) shareLinkJSON(link state.ShareLink) shareLinkJSON {
	return shareLinkJSON{
		ID: link.ID, Label: link.Label, Scope: link.Scope, HasPassword: link.PasswordHash != "", State: app.shareState(link),
		VisitorPath: shareBase(link.ID), CreatedAt: link.CreatedAt, ExpiresAt: link.ExpiresAt, RevokedAt: link.RevokedAt, LastUsedAt: link.LastUsedAt,
	}
}

// handleShareLinksAPI answers GET and POST /api/v1/repositories/{name}/
// share-links and POST .../share-links/{id}/revoke with the administrator
// password.
func (app *App) handleShareLinksAPI(writer http.ResponseWriter, request *http.Request, id, remainder string) {
	writer.Header().Set("Cache-Control", "no-store")
	linkID, revoke := strings.CutSuffix(remainder, "/revoke")
	switch {
	case remainder == "" && request.Method != http.MethodGet && request.Method != http.MethodPost:
		writeAPIMethodError(writer, "GET, POST")
		return
	case revoke && validAttemptID(linkID) && request.Method != http.MethodPost:
		writeAPIMethodError(writer, http.MethodPost)
		return
	case remainder != "" && !(revoke && validAttemptID(linkID)):
		writeAPIError(writer, http.StatusNotFound, "not_found", "The API endpoint does not exist.", nil)
		return
	}
	if !app.authorizeAdminAPI(writer, request) || !app.importRepositoryExists(writer, request, id) {
		return
	}
	switch {
	case revoke:
		if !decodeAPIAction(writer, request) {
			return
		}
		link, err := app.Store.RevokeShareLink(request.Context(), id, linkID, app.now())
		switch {
		case errors.Is(err, state.ErrShareLinkNotFound):
			writeAPIError(writer, http.StatusNotFound, "share_link_not_active", webui.Text(webui.LangEN, webui.MsgShareNotActive), nil)
		case err != nil:
			writeAPIError(writer, unavailable(request, "share link revoke", err), "state_unavailable", "The share link could not be revoked. Try again later.", nil)
		default:
			writeAPIJSON(writer, http.StatusOK, struct {
				OK        bool          `json:"ok"`
				ShareLink shareLinkJSON `json:"share_link"`
			}{true, app.shareLinkJSON(link)})
		}
	case request.Method == http.MethodPost:
		app.createShareLinkAPI(writer, request, id)
	default:
		links, err := app.Store.ShareLinks(request.Context(), id)
		if err != nil {
			writeAPIError(writer, unavailable(request, "share link list read", err), "state_unavailable", "OwnGit state is unavailable.", nil)
			return
		}
		response := struct {
			OK         bool            `json:"ok"`
			Repository string          `json:"repository"`
			ShareLinks []shareLinkJSON `json:"share_links"`
		}{OK: true, Repository: id, ShareLinks: []shareLinkJSON{}}
		for _, link := range links {
			response.ShareLinks = append(response.ShareLinks, app.shareLinkJSON(link))
		}
		writeAPIJSON(writer, http.StatusOK, response)
	}
}

// createShareLinkAPI creates a link from {"label", "scope", "expires_in_days"
// or "until_revoked", "password"}. Without either expiry field the link
// expires after 30 days, as on the screen.
func (app *App) createShareLinkAPI(writer http.ResponseWriter, request *http.Request, id string) {
	var input struct {
		Label         string `json:"label"`
		Scope         string `json:"scope"`
		ExpiresInDays *int   `json:"expires_in_days"`
		UntilRevoked  bool   `json:"until_revoked"`
		Password      string `json:"password"`
	}
	if !decodeAPIJSON(writer, request, &input) {
		return
	}
	if input.Scope == "" {
		input.Scope = state.ShareBrowse
	}
	days, _ := strconv.Atoi(webui.ShareExpiryDefault)
	switch {
	case input.UntilRevoked && input.ExpiresInDays != nil:
		writeAPIError(writer, http.StatusBadRequest, "invalid_share_link", "Name expires_in_days or until_revoked, not both.", nil)
		return
	case input.UntilRevoked:
		days = 0
	case input.ExpiresInDays != nil:
		if *input.ExpiresInDays < 1 {
			writeAPIError(writer, http.StatusBadRequest, "invalid_share_link", "expires_in_days must be from 1 to "+strconv.Itoa(maximumShareDays)+"; for a link without an expiry use until_revoked.", nil)
			return
		}
		days = *input.ExpiresInDays
	}
	link, secret, err := app.createShareLink(request.Context(), id, shareLinkInput{label: input.Label, scope: input.Scope, days: days, password: input.Password})
	switch {
	case errors.Is(err, errShareLabel):
		writeAPIError(writer, http.StatusBadRequest, "invalid_share_link", webui.Text(webui.LangEN, webui.MsgShareLabelInvalid), nil)
		return
	case errors.Is(err, errShareChoice):
		writeAPIError(writer, http.StatusBadRequest, "invalid_share_link", "scope must be browse or clone, and expires_in_days from 1 to "+strconv.Itoa(maximumShareDays)+".", nil)
		return
	case errors.Is(err, auth.ErrPasswordTooShort), errors.Is(err, auth.ErrPasswordTooLong):
		writeAPIError(writer, http.StatusBadRequest, "invalid_share_link", webui.Text(webui.LangEN, webui.MsgSharePasswordRule), nil)
		return
	case err != nil:
		writeAPIError(writer, unavailable(request, "share link creation", err), "state_unavailable", "The share link could not be created. Try again later.", nil)
		return
	}
	response := struct {
		OK        bool          `json:"ok"`
		ShareLink shareLinkJSON `json:"share_link"`
		// URL holds the link's secret. This answer is the only place it
		// appears.
		URL         string `json:"url"`
		CloneURL    string `json:"clone_url,omitempty"`
		CloneSignIn string `json:"clone_sign_in,omitempty"`
		// PublicURL and PublicCloneURL are the same on the public share
		// address, when the running server has one.
		PublicURL      string   `json:"public_url,omitempty"`
		PublicCloneURL string   `json:"public_clone_url,omitempty"`
		Warnings       []string `json:"warnings,omitempty"`
	}{OK: true, ShareLink: app.shareLinkJSON(link), URL: app.serverOrigin(request) + sharePrefix + secret}
	response.PublicURL, response.PublicCloneURL = app.publicShareURLs(link, secret)
	if link.Scope == state.ShareClone {
		var help webui.MessageCode
		response.CloneURL, help = app.shareCloneURL(request, link)
		response.CloneSignIn = webui.Text(webui.LangEN, help)
	}
	for _, warning := range shareWarnings(link) {
		response.Warnings = append(response.Warnings, webui.Text(webui.LangEN, warning))
	}
	writeAPIJSON(writer, http.StatusCreated, response)
}
