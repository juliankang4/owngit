package server

import (
	"context"
	"net/http"
	"slices"
	"strings"

	"owngit/internal/requestctx"
	"owngit/internal/state"
)

// The public share address is a second listener that answers share links
// and nothing else, so an owner can put it on the Internet (with Tailscale
// Funnel or a reverse proxy) while OwnGit itself stays private. Its one
// router is publicShareRoute: whatever a request carries, a path it does
// not name is not found, the same way for every such path. The private
// listener is unchanged and keeps refusing Funnel requests.

type publicShareKey struct{}

// publicAssets are the files the share pages load: the stylesheet, its
// font, the script and the logo.
var publicAssets = []string{"/assets/owngit.css", "/assets/fonts/PretendardVariable.woff2", "/assets/owngit.js", "/assets/logo.svg"}

type publicRoute int

const (
	publicNone publicRoute = iota
	publicAsset
	publicShareGit
	publicSharePage
)

// publicShareRoute is the only router of the public share address. Share
// pages open with /share/{secret}, which withoutShareSecret has already
// turned into /share/.
func publicShareRoute(path string) publicRoute {
	if slices.Contains(publicAssets, path) {
		return publicAsset
	}
	if _, _, ok := shareGitRoute(path); ok {
		return publicShareGit
	}
	if _, _, ok := sharePage(path); ok || path == sharePrefix {
		return publicSharePage
	}
	return publicNone
}

// PublicShareHandler serves the public share address.
func (app *App) PublicShareHandler() http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		request = withoutShareSecret(request)
		request = request.WithContext(context.WithValue(request.Context(), publicShareKey{}, true))
		app.Network.Resolver().Middleware(http.HandlerFunc(app.servePublicShare)).ServeHTTP(writer, request)
	})
}

func (app *App) servePublicShare(writer http.ResponseWriter, request *http.Request) {
	setSecurityHeaders(writer, request)
	route := publicShareRoute(request.URL.Path)
	if route == publicNone {
		http.NotFound(writer, request)
		return
	}
	if origin := request.Header.Get("Origin"); origin != "" && !sameOrigin(request, origin) {
		http.Error(writer, "origin does not match this server", http.StatusForbidden)
		return
	}
	if route == publicShareGit {
		id, suffix, _ := shareGitRoute(request.URL.Path)
		app.serveShareGit(writer, request, id, suffix)
		return
	}
	pageTimeout, pageReserve := app.pageTimeout()
	operationTimeout, operationReserve := app.requestTimeout(request)
	request, deadlines, cancel := startDeadlines(writer, request, pageTimeout, pageReserve, operationTimeout, operationReserve)
	defer cancel()
	defer deadlines.finish()
	if route == publicAsset {
		app.Renderer.Assets().ServeHTTP(writer, cloneWithPath(request, strings.TrimPrefix(request.URL.Path, "/assets")))
		return
	}
	app.serveSharePage(writer, request)
}

// publicShareURLs are a new link's address and, for a clone link, its Git
// address on the public share address, or none while it is off.
func (app *App) publicShareURLs(link state.ShareLink, secret string) (string, string) {
	if app.PublicShareURL == "" {
		return "", ""
	}
	cloneURL := ""
	if link.Scope == state.ShareClone {
		cloneURL = app.PublicShareURL + shareBase(link.ID) + ".git"
	}
	return app.PublicShareURL + sharePrefix + secret, cloneURL
}

// shareOrigin is the origin of a share link's addresses shown on its pages:
// the public share address for a request that came through it, otherwise
// serverOrigin.
func (app *App) shareOrigin(request *http.Request) string {
	if public, _ := request.Context().Value(publicShareKey{}).(bool); !public {
		return app.serverOrigin(request)
	}
	if app.PublicShareURL != "" {
		return app.PublicShareURL
	}
	return requestctx.Of(request).Origin()
}
