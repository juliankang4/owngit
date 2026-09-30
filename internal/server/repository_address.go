package server

import (
	"context"
	"net/http"
	"net/url"
	"strings"

	"owngit/internal/webui"
)

// Repository addresses. A page or API path names a repository by its
// address (see state.ResolveRepositoryName). serveHTTP resolves that name
// once and attaches the result to the request; the routes use the resolved
// ID. An alias, and a name that reaches no repository, are answered only
// after the route has accepted the caller's credential, so the answer never
// tells an unauthenticated caller about a repository: an alias redirects to
// the same request at the current address, and anything else is not found.

// repositoryAddress is what the repository name in a request path reached.
type repositoryAddress struct {
	// id is the repository ID the name reaches, or "" when it reaches none,
	// such as an expired alias.
	id string
	// current is where that repository answers now.
	current string
	// movedTo is the same request at the repository's current address when
	// the name is an alias, otherwise "".
	movedTo string
	// err is a lookup that failed; it says nothing about the name.
	err error
}

type repositoryAddressKey struct{}

// repositoryNameInPath returns the prefix and repository name of a page or
// API path below a repository, and the rest of the path after the name.
func repositoryNameInPath(path string) (prefix, name, rest string) {
	for _, prefix := range []string{"/api/v1/repositories/", "/api/v1/tasks/", "/repositories/"} {
		if remainder, ok := strings.CutPrefix(path, prefix); ok {
			name, rest, _ = strings.Cut(remainder, "/")
			if rest != "" || strings.HasSuffix(remainder, "/") {
				rest = "/" + rest
			}
			return prefix, name, rest
		}
	}
	return "", "", ""
}

// resolveRepositoryAddress resolves the repository name of a page or API
// path and attaches the result. Other requests are returned unchanged.
func (app *App) resolveRepositoryAddress(request *http.Request) *http.Request {
	prefix, name, rest := repositoryNameInPath(request.URL.Path)
	if name == "" {
		return request
	}
	var address repositoryAddress
	resolved, found, err := app.Store.ResolveRepositoryName(request.Context(), name, app.now())
	switch {
	case err != nil:
		address.err = err
	case !found:
	case resolved.Current != name:
		moved := *request.URL
		moved.Path, moved.RawPath = prefix+resolved.Current+rest, ""
		address.movedTo = moved.RequestURI()
		address.id, address.current = resolved.RepositoryID, resolved.Current
	default:
		address.id, address.current = resolved.RepositoryID, resolved.Current
	}
	return withRepositoryAddress(request, address)
}

func withRepositoryAddress(request *http.Request, address repositoryAddress) *http.Request {
	return request.WithContext(context.WithValue(request.Context(), repositoryAddressKey{}, address))
}

// firstImportDestination lets an import route name a repository that does
// not exist yet: a first import names the repository it is to create, and
// its cancel and credential routes name it until then. Any other route
// answers such a name as not found. The import service refuses a name that
// is already taken, including the ID of a renamed repository.
func firstImportDestination(request *http.Request) *http.Request {
	address, named := repositoryAddressOf(request)
	if !named || address.err != nil || address.id != "" {
		return request
	}
	_, name, _ := repositoryNameInPath(request.URL.Path)
	address.id, address.current = name, name
	return withRepositoryAddress(request, address)
}

// repositoryAddressOf returns what resolveRepositoryAddress attached. A
// request without one reaches no repository.
func repositoryAddressOf(request *http.Request) (repositoryAddress, bool) {
	address, ok := request.Context().Value(repositoryAddressKey{}).(repositoryAddress)
	return address, ok
}

// answerRepositoryAddressPage answers a page request, after its session is
// accepted, whose address reaches no current repository, and otherwise
// returns the repository ID.
func (app *App) answerRepositoryAddressPage(writer http.ResponseWriter, request *http.Request) (string, bool) {
	address, _ := repositoryAddressOf(request)
	switch {
	case address.err != nil:
		app.renderError(writer, request, unavailable(request, "repository record read", address.err), webui.MsgErrUnavailable, "")
	case address.movedTo != "":
		http.Redirect(writer, request, address.movedTo, http.StatusTemporaryRedirect)
	case address.id == "":
		_, name, _ := repositoryNameInPath(request.URL.Path)
		app.renderError(writer, request, http.StatusNotFound, webui.MsgRepoNotFound, name)
	default:
		return address.id, true
	}
	return "", false
}

// answerRepositoryAddressAPI answers an API request, after its credential is
// accepted, whose address reaches no current repository, and reports whether
// the request may continue. A request outside a repository continues.
//
// A runner or helper credential is bound to one repository by its ID, so
// with such a credential (boundCredential) an alias keeps working until it
// expires; the credential already proves which repository is meant.
func answerRepositoryAddressAPI(writer http.ResponseWriter, request *http.Request, boundCredential bool) bool {
	address, named := repositoryAddressOf(request)
	switch {
	case !named:
		return true
	case address.err != nil:
		writeAPIError(writer, unavailable(request, "repository record read", address.err), "state_unavailable", "OwnGit state is unavailable.", nil)
	case address.movedTo != "" && !boundCredential:
		// The API client does not follow redirects, so the answer also says
		// where the repository is now.
		current, _ := url.Parse(address.movedTo)
		_, name, _ := repositoryNameInPath(current.Path)
		writer.Header().Set("Location", address.movedTo)
		writeAPIError(writer, http.StatusTemporaryRedirect, "repository_moved", "The repository was renamed. Use its new name: "+name+".", map[string]string{"address": name})
	case address.id == "":
		writeAPIError(writer, http.StatusNotFound, "repository_not_found", "The repository does not exist.", nil)
	default:
		return true
	}
	return false
}

// repositoryAddressCurrent returns where the repository the request names
// answers now.
func repositoryAddressCurrent(request *http.Request) string {
	address, _ := repositoryAddressOf(request)
	return address.current
}
