package server

import (
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"owngit/internal/githttp"
	"owngit/internal/repository"
	"owngit/internal/state"
	"owngit/internal/webui"
)

// maximumArchiveNameBytes keeps an archive's file name within common file
// system limits once the extension is added.
const maximumArchiveNameBytes = 200

// archiveTarget is a checked archive download.
type archiveTarget struct {
	commitOID string
	format    string
	// name is the archive's top folder and file name without extension.
	name string
}

// archiveURL is the browser address that downloads ref of a repository in
// format.
func archiveURL(repositoryID, ref, format string) string {
	values := url.Values{"ref": []string{ref}, "format": []string{format}}
	return "/repositories/" + url.PathEscape(repositoryID) + "/archive?" + values.Encode()
}

// archiveLinks offers ref as ZIP and tar.gz.
func archiveLinks(repositoryID, ref string) []webui.ArchiveLink {
	return []webui.ArchiveLink{
		{Label: "ZIP", URL: archiveURL(repositoryID, ref, githttp.ArchiveZip)},
		{Label: "tar.gz", URL: archiveURL(repositoryID, ref, githttp.ArchiveTarGz)},
	}
}

// resolveArchive checks the format and resolves the ref of an archive
// request: a branch or tag as the Code tab names it, a full commit ID, or the
// default branch when ref is empty. It returns 404 for an unknown format or
// ref, and 503 when the repository cannot be read now.
func (app *App) resolveArchive(request *http.Request, repositoryID string) (archiveTarget, int) {
	query := request.URL.Query()
	format := query.Get("format")
	if format != githttp.ArchiveZip && format != githttp.ArchiveTarGz {
		return archiveTarget{}, http.StatusNotFound
	}
	resolved, commitOID, err := app.Repositories.ResolveRevision(request.Context(), repositoryID, query.Get("ref"))
	switch {
	case errors.Is(err, repository.ErrRepositoryPreparing), errors.Is(err, repository.ErrRepositoryInUse):
		return archiveTarget{}, http.StatusServiceUnavailable
	case err != nil:
		return archiveTarget{}, http.StatusNotFound
	}
	return archiveTarget{commitOID: commitOID, format: format, name: archiveName(repositoryID, displayRef(resolved))}, http.StatusOK
}

// archiveName is REPOSITORY-REF with every character other than a letter, a
// digit, ".", "-" or "_" replaced by "-", so the name holds no path, and cut
// to maximumArchiveNameBytes.
func archiveName(repositoryID, ref string) string {
	name := strings.Map(func(character rune) rune {
		if unicode.IsLetter(character) || unicode.IsDigit(character) || strings.ContainsRune(".-_", character) {
			return character
		}
		return '-'
	}, repositoryID+"-"+ref)
	for len(name) > maximumArchiveNameBytes {
		_, size := utf8.DecodeLastRuneInString(name)
		name = name[:len(name)-size]
	}
	return name
}

// serveArchive streams the archive. When it fails before the first byte it
// writes nothing and returns the *githttp.ArchiveError to answer, after
// setting Retry-After when one applies.
func (app *App) serveArchive(writer http.ResponseWriter, request *http.Request, repositoryID string, target archiveTarget) *githttp.ArchiveError {
	extension := ".zip"
	if target.format == githttp.ArchiveTarGz {
		extension = ".tar.gz"
	}
	err := app.GitHTTP.ServeArchive(writer, request, repositoryID, target.commitOID, target.format, target.name, target.name+extension)
	if err == nil {
		return nil
	}
	var failure *githttp.ArchiveError
	if !errors.As(err, &failure) {
		failure = &githttp.ArchiveError{Status: http.StatusBadGateway, Message: "The archive could not be created."}
	}
	if failure.RetryAfter > 0 {
		writer.Header().Set("Retry-After", strconv.Itoa(int(failure.RetryAfter/time.Second)))
	}
	return failure
}

// archiveRoute reports whether request asks for an archive on the browser or
// API route. Those requests get the Git transfer time limit instead of the
// page limit.
func archiveRoute(request *http.Request) bool {
	if request == nil || request.Method != http.MethodGet {
		return false
	}
	for _, prefix := range []string{"/repositories/", "/api/v1/repositories/"} {
		if rest, ok := strings.CutPrefix(request.URL.Path, prefix); ok {
			id, found := strings.CutSuffix(rest, "/archive")
			return found && id != "" && !strings.Contains(id, "/")
		}
	}
	return false
}

// handleArchive answers the browser download of the Code tab and the commit
// page, with the read access of those pages.
func (app *App) handleArchive(writer http.ResponseWriter, request *http.Request, stored state.Repository) {
	if app.GitHTTP == nil {
		app.renderError(writer, request, http.StatusServiceUnavailable, webui.MsgErrUnavailable, "")
		return
	}
	target, status := app.resolveArchive(request, stored.ID)
	switch status {
	case http.StatusOK:
		if failure := app.serveArchive(writer, request, stored.ID, target); failure != nil {
			code := webui.MsgErrUnavailable
			if failure.Status == http.StatusNotFound {
				code = webui.MsgErrNotFound
			}
			app.renderError(writer, request, failure.Status, code, "")
		}
	case http.StatusServiceUnavailable:
		writer.Header().Set("Retry-After", "10")
		app.renderError(writer, request, status, webui.MsgErrUnavailable, "")
	default:
		app.renderError(writer, request, status, webui.MsgErrNotFound, request.URL.Path)
	}
}

// archiveQueryAllowed accepts the ref and format of an archive API request,
// each at most once.
func archiveQueryAllowed(request *http.Request, repositoryRoute bool, resource, remainder string) bool {
	if !repositoryRoute || resource != "archive" || remainder != "" || request.Method != http.MethodGet {
		return false
	}
	for key, values := range request.URL.Query() {
		if (key != "ref" && key != "format") || len(values) != 1 {
			return false
		}
	}
	return true
}

// handleArchiveAPI answers GET /api/v1/repositories/ID/archive?ref=REF&format=FORMAT
// for clients without a browser session, with general access like the pull
// request API. Failures before the archive starts are JSON errors.
func (app *App) handleArchiveAPI(writer http.ResponseWriter, request *http.Request, settings state.Settings, repositoryID, remainder string) {
	if remainder != "" {
		writeAPIError(writer, http.StatusNotFound, "not_found", "The API endpoint does not exist.", nil)
		return
	}
	if request.Method != http.MethodGet {
		writeAPIMethodError(writer, http.MethodGet)
		return
	}
	if !app.authorizeAPI(writer, request, settings) || app.refusePreparingAPI(writer, repositoryID) {
		return
	}
	if _, exists, err := app.visibleRepository(request, repositoryID); err != nil {
		writeAPIError(writer, http.StatusServiceUnavailable, "state_unavailable", "Repository metadata could not be read.", nil)
		return
	} else if !exists {
		writeAPIError(writer, http.StatusNotFound, "repository_not_found", "The repository does not exist.", nil)
		return
	}
	if app.GitHTTP == nil {
		writeAPIError(writer, http.StatusServiceUnavailable, "service_unavailable", "The Git service is unavailable.", nil)
		return
	}
	target, status := app.resolveArchive(request, repositoryID)
	switch status {
	case http.StatusOK:
		if failure := app.serveArchive(writer, request, repositoryID, target); failure != nil {
			code := "archive_failed"
			switch failure.Status {
			case http.StatusNotFound:
				code = "archive_not_found"
			case http.StatusServiceUnavailable:
				code = "repository_unavailable"
			}
			writeAPIError(writer, failure.Status, code, failure.Message, nil)
		}
	case http.StatusServiceUnavailable:
		writer.Header().Set("Retry-After", "10")
		writeAPIError(writer, status, "repository_unavailable", "The repository cannot be read now. Try again later.", nil)
	default:
		writeAPIError(writer, status, "archive_not_found", "The format must be zip or tar.gz, and the ref must name a branch, a tag, or a full commit ID of this repository.", nil)
	}
}
