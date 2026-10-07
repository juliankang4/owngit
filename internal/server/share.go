package server

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"owngit/internal/auth"
	"owngit/internal/githttp"
	"owngit/internal/repository"
	"owngit/internal/requestctx"
	"owngit/internal/state"
	"owngit/internal/webui"
)

// Share links let someone without an account read one repository. The
// addresses:
//
//   - /share/{secret} is the link handed out. It sets a cookie for the link
//     and sends the browser on to /share/{id}, so the secret leaves the
//     address bar at once and no later page, log line or Referer carries it.
//   - /share/{id} and the pages below it are the repository's overview,
//     Code and Commits pages and its raw files, for the browser holding the
//     cookie. A link with an extra password asks for it first.
//   - /share/{id}.git is the Git address of a clone link, fetch only. Git
//     signs in as shareGitLink describes.
//
// The ID names the link, not the repository, so a rename keeps every link
// working. An unknown, expired or revoked link answers as not found; Git
// gets the 401 of any failed sign-in.

const (
	sharePrefix = "/share/"
	shareCookie = "owngit_share"
)

type shareSecretKey struct{}

// withoutShareSecret moves the secret of a /share/{secret} request out of
// its path into its context, before anything can log, echo or redirect the
// path: the path becomes sharePrefix alone. Other requests are unchanged.
func withoutShareSecret(request *http.Request) *http.Request {
	secret, found := strings.CutPrefix(request.URL.Path, sharePrefix)
	if !found || secret == "" || strings.Contains(secret, "/") || validAttemptID(secret) {
		return request
	}
	request = request.WithContext(context.WithValue(request.Context(), shareSecretKey{}, secret))
	hidden := *request.URL
	hidden.Path, hidden.RawPath = sharePrefix, ""
	request.URL = &hidden
	request.RequestURI = hidden.RequestURI()
	return request
}

// shareGitRoute returns the link ID and the Git path after it of a share
// link's Git request, /share/{id}.git/{suffix}.
func shareGitRoute(path string) (string, string, bool) {
	rest, found := strings.CutPrefix(path, sharePrefix)
	id, suffix, found2 := strings.Cut(rest, ".git/")
	return id, suffix, found && found2 && validAttemptID(id)
}

// sharePage returns the link ID and the page below it ("" for the
// overview) of a share page address, /share/{id} or /share/{id}/{page}.
func sharePage(path string) (string, string, bool) {
	rest, found := strings.CutPrefix(path, sharePrefix)
	id, page, _ := strings.Cut(rest, "/")
	return id, page, found && validAttemptID(id)
}

func shareBase(id string) string { return sharePrefix + id }

func shareSecretHash(secret string) []byte {
	hash := sha256.Sum256([]byte(secret))
	return hash[:]
}

// activeShare returns the active link whose secret is secret when its ID is
// id.
func (app *App) activeShare(ctx context.Context, id, secret string) (state.ShareLink, bool, error) {
	link, found, err := app.Store.ActiveShareLink(ctx, shareSecretHash(secret), app.now())
	return link, found && link.ID == id, err
}

// shareProof proves in a link's cookie that its browser gave the link's
// extra password. It is keyed by the stored password hash, so it proves
// nothing for another link or another password.
func shareProof(link state.ShareLink, secret string) string {
	mac := hmac.New(sha256.New, []byte(link.PasswordHash))
	mac.Write([]byte(secret))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func (app *App) setShareCookie(writer http.ResponseWriter, request *http.Request, id, value string) {
	// Lax, so the link opens from a message or another site; HttpOnly and
	// limited to the link's own pages.
	http.SetCookie(writer, &http.Cookie{
		Name: cookieNameForScheme(request, shareCookie), Value: value, Path: shareBase(id),
		HttpOnly: true, Secure: requestctx.Of(request).Secure(), SameSite: http.SameSiteLaxMode,
	})
}

// serveShareGit serves the Git requests of a share link: a clone or fetch
// of a clone link's repository. Pushes are refused.
func (app *App) serveShareGit(writer http.ResponseWriter, request *http.Request, id, suffix string) {
	settings, err := app.Store.Settings(request.Context())
	if err != nil {
		logFailure(request, "settings read", err)
		githttp.RefuseCredentials(writer, err)
		return
	}
	if admissionWithdrawn(request, settings) {
		githttp.RefuseCredentials(writer, nil)
		return
	}
	user, password, _ := request.BasicAuth()
	link, err := app.shareGitLink(request, id, user, password)
	if err != nil {
		if !errors.Is(err, auth.ErrInvalidCredentials) && !errors.Is(err, auth.ErrRateLimited) && !errors.As(err, new(*state.PolicyError)) {
			logFailure(request, "share link check", err)
		}
		githttp.RefuseCredentials(writer, err)
		return
	}
	if link.Scope != state.ShareClone {
		http.Error(writer, "this share link opens the repository in a browser only; it cannot be cloned", http.StatusForbidden)
		return
	}
	if err := app.Store.NoteShareLinkUse(request.Context(), link.ID, app.now()); err != nil {
		logFailure(request, "share link use record", err)
		githttp.RefuseCredentials(writer, err)
		return
	}
	app.GitHTTP.ServeRead(writer, request, link.RepositoryID, suffix)
}

// shareGitLink is the one rule for how Git signs in with a share link: a
// link without an extra password takes its secret as the password,
// whatever the user name; a link with one takes the secret as the user
// name and the extra password as the password. Anything else is
// auth.ErrInvalidCredentials.
func (app *App) shareGitLink(request *http.Request, id, user, password string) (state.ShareLink, error) {
	ctx := request.Context()
	link, found, err := app.activeShare(ctx, id, password)
	if err != nil {
		return state.ShareLink{}, err
	}
	if found && link.PasswordHash == "" {
		return link, nil
	}
	link, found, err = app.activeShare(ctx, id, user)
	if err != nil {
		return state.ShareLink{}, err
	}
	if !found || link.PasswordHash == "" {
		return state.ShareLink{}, auth.ErrInvalidCredentials
	}
	if err := app.Auth.VerifySharePassword(ctx, link.PasswordHash, password, requestctx.Of(request).ClientAddress); err != nil {
		return state.ShareLink{}, err
	}
	return link, nil
}

// serveSharePage answers the browser addresses of share links.
func (app *App) serveSharePage(writer http.ResponseWriter, request *http.Request) {
	if request.URL.Path == sharePrefix {
		app.openShare(writer, request)
		return
	}
	id, page, ok := sharePage(request.URL.Path)
	if !ok {
		app.renderError(writer, request, http.StatusNotFound, webui.MsgShareLinkNotFound, "")
		return
	}
	var secret, proof string
	cookie, legacy := upgradeCookie(request, shareCookie)
	if cookie != nil {
		secret, proof, _ = strings.Cut(cookie.Value, ".")
	}
	link, found, err := app.activeShare(request.Context(), id, secret)
	if err != nil {
		app.renderError(writer, request, unavailable(request, "share link read", err), webui.MsgErrUnavailable, "")
		return
	}
	if !found {
		app.renderError(writer, request, http.StatusNotFound, webui.MsgShareLinkNotFound, "")
		return
	}
	if link.PasswordHash != "" && !hmac.Equal([]byte(proof), []byte(shareProof(link, secret))) {
		app.askSharePassword(writer, request, link, secret)
		return
	}
	if legacy {
		app.setShareCookie(writer, request, id, cookie.Value)
	}
	if request.Method == http.MethodPost {
		// A password sent again after it was accepted.
		http.Redirect(writer, request, request.URL.RequestURI(), http.StatusSeeOther)
		return
	}
	if err := app.Store.NoteShareLinkUse(request.Context(), link.ID, app.now()); err != nil {
		app.renderError(writer, request, unavailable(request, "share link use record", err), webui.MsgErrUnavailable, "")
		return
	}
	app.serveSharedRepository(writer, request, link, page)
}

// openShare answers the link handed out, /share/{secret}.
func (app *App) openShare(writer http.ResponseWriter, request *http.Request) {
	writer.Header().Set("Cache-Control", "no-store")
	secret, _ := request.Context().Value(shareSecretKey{}).(string)
	link, found, err := app.Store.ActiveShareLink(request.Context(), shareSecretHash(secret), app.now())
	if err != nil {
		app.renderError(writer, request, unavailable(request, "share link read", err), webui.MsgErrUnavailable, "")
		return
	}
	if !found || (request.Method != http.MethodGet && request.Method != http.MethodHead) {
		app.renderError(writer, request, http.StatusNotFound, webui.MsgShareLinkNotFound, "")
		return
	}
	app.setShareCookie(writer, request, link.ID, secret)
	http.Redirect(writer, request, shareBase(link.ID), http.StatusSeeOther)
}

// askSharePassword shows the password form of link, or checks the password
// it sent. The form's page sends a Referer within this server only, since
// under no-referrer a browser sends its form without an Origin, and it is
// the one page whose address a Referer could carry: /share/{id} and the
// page asked for, never the secret.
func (app *App) askSharePassword(writer http.ResponseWriter, request *http.Request, link state.ShareLink, secret string) {
	// The one exception to setSecurityHeaders' no-referrer under /share/.
	writer.Header().Set("Referrer-Policy", "same-origin")
	page := webui.SharePasswordPage{Chrome: app.shareChrome(writer, request, ""), SubmitURL: request.URL.RequestURI()}
	status := http.StatusForbidden
	if request.Method == http.MethodPost {
		if !app.parseForm(writer, request) {
			return
		}
		err := app.Auth.VerifySharePassword(request.Context(), link.PasswordHash, postValue(request, "share_password"), requestctx.Of(request).ClientAddress)
		var policyErr *state.PolicyError
		switch {
		case err == nil:
			app.setShareCookie(writer, request, link.ID, secret+"."+shareProof(link, secret))
			http.Redirect(writer, request, request.URL.RequestURI(), http.StatusSeeOther)
			return
		case errors.Is(err, auth.ErrInvalidCredentials):
			page.Wrong = true
		case errors.Is(err, auth.ErrRateLimited):
			page.Locked, status = true, http.StatusTooManyRequests
			seconds := auth.RetryAfter(err)
			page.RetryAfter = app.now().Add(time.Duration(seconds) * time.Second)
			writer.Header().Set("Retry-After", strconv.Itoa(seconds))
		case errors.As(err, &policyErr):
			logFailure(request, "share link password check", err)
			app.renderError(writer, request, http.StatusConflict, webui.MsgLoginLimitsUnreadable, "")
			return
		default:
			app.renderError(writer, request, unavailable(request, "share link password check", err), webui.MsgErrUnavailable, "")
			return
		}
	}
	app.render(writer, request, status, page)
}

// serveSharedRepository answers page, a page below a share link, with the
// link's repository.
func (app *App) serveSharedRepository(writer http.ResponseWriter, request *http.Request, link state.ShareLink, page string) {
	stored, exists, err := app.Store.Repository(request.Context(), link.RepositoryID)
	if err != nil {
		app.renderError(writer, request, unavailable(request, "repository record read", err), webui.MsgErrUnavailable, "")
		return
	}
	if !exists {
		app.renderError(writer, request, http.StatusNotFound, webui.MsgShareLinkNotFound, "")
		return
	}
	// A request for a repository postpones its maintenance until it ends.
	app.Repositories.NoteRepositoryUse(stored.ID)
	defer app.Repositories.NoteRepositoryUse(stored.ID)
	if page == "raw" && (request.Method == http.MethodGet || request.Method == http.MethodHead) {
		app.handleRaw(writer, request, stored)
		return
	}
	if request.Method != http.MethodGet {
		app.renderError(writer, request, http.StatusNotFound, webui.MsgErrNotFound, request.URL.Path)
		return
	}
	var tab []string
	if page != "" {
		tab = strings.Split(page, "/")
	}
	address, err := parsePageAddress(request, tab)
	if err != nil {
		app.renderError(writer, request, http.StatusBadRequest, webui.MsgErrNotFound, "")
		return
	}
	chrome := app.shareChrome(writer, request, shareBase(link.ID))
	snapshot, err := app.Repositories.RefSnapshot(request.Context(), stored.ID)
	base := app.sharedRepositoryPage(request, chrome, stored, snapshot.Summary, link)
	if err != nil {
		app.renderRepositoryReadFailure(writer, request, base, err)
		return
	}
	app.serveCodePage(writer, request, base, snapshot, tab, address)
}

// sharedRepositoryPage is baseRepositoryPage for a share link's pages: they
// link only to one another, and a clone link shows its Git address.
func (app *App) sharedRepositoryPage(request *http.Request, chrome webui.Chrome, stored state.Repository, summary repository.Summary, link state.ShareLink) webui.RepositoryPage {
	base := shareBase(link.ID)
	page := webui.RepositoryPage{
		Chrome: chrome, Shared: true,
		Repo:        webui.RepositoryHeader{ID: stored.ID, Address: stored.Address, Name: stored.Name, Description: stored.Description, URL: base, Empty: summary.Empty},
		OverviewURL: base, CodeURL: base + "/code", CommitsURL: base + "/commits",
	}
	if link.Scope == state.ShareClone {
		page.Repo.CloneURL, page.CloneHelp = app.shareCloneURL(request, link)
	}
	return page
}

// shareCloneURL is the Git address of a clone link and how Git signs in
// there (see shareGitLink).
func (app *App) shareCloneURL(request *http.Request, link state.ShareLink) (string, webui.MessageCode) {
	help := webui.MsgShareCloneHelp
	if link.PasswordHash != "" {
		help = webui.MsgShareCloneHelpPassword
	}
	return app.shareOrigin(request) + shareBase(link.ID) + ".git", help
}

// shareChrome is the frame of a share link's pages: no dashboard, no
// session, and the OwnGit mark leads to home, the share's first page, or
// nowhere but this page when home is "".
func (app *App) shareChrome(writer http.ResponseWriter, request *http.Request, home string) webui.Chrome {
	info := requestctx.Of(request)
	if home == "" {
		home = request.URL.RequestURI()
	}
	return webui.Chrome{
		Lang: app.language(writer, request), Appearance: app.appearance(writer, request), Now: app.now(),
		CurrentURL: request.URL.RequestURI(), HomeURL: home,
		Connection: webui.Connection{
			Encrypted: info.Secure(), Proxy: info.Secure() && info.Proxied, Tailscale: app.throughTailscale(request), Tailnet: app.throughTailnet(request),
			Host: info.Host,
		},
	}
}

// renderShareError is renderError on a share address: the page stays
// inside the share and names nothing else on this server.
func (app *App) renderShareError(writer http.ResponseWriter, request *http.Request, status int, code webui.MessageCode, detail string) {
	home, retry := "", ""
	if id, _, ok := sharePage(request.URL.Path); ok {
		home = shareBase(id)
		retry = home
	}
	if status == http.StatusNotFound && code == webui.MsgShareLinkNotFound {
		retry = ""
	}
	app.render(writer, request, status, webui.ErrorPage{Chrome: app.shareChrome(writer, request, home), Status: status, Code: code, Detail: detail, RetryURL: retry})
}
