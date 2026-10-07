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
func archiveURL(address, ref, format string) string {
	values := url.Values{"ref": []string{ref}, "format": []string{format}}
	return "/repositories/" + url.PathEscape(address) + "/archive?" + values.Encode()
}

// archiveLinks offers ref as ZIP and tar.gz.
func archiveLinks(address, ref string) []webui.ArchiveLink {
	return []webui.ArchiveLink{
		{Label: "ZIP", URL: archiveURL(address, ref, githttp.ArchiveZip)},
		{Label: "tar.gz", URL: archiveURL(address, ref, githttp.ArchiveTarGz)},
	}
}

// resolveArchive checks the format and resolves the ref of an archive
// request: a branch or tag as the Code tab names it, a full commit ID, or the
// default branch when ref is empty. It returns 404 for an unknown format or
// ref, and unavailable, with the cause logged, when the repository cannot be
// read now.
func (app *App) resolveArchive(request *http.Request, repositoryID string) (archiveTarget, int) {
	query := request.URL.Query()
	format := query.Get("format")
	if format != githttp.ArchiveZip && format != githttp.ArchiveTarGz {
		return archiveTarget{}, http.StatusNotFound
	}
	resolved, commitOID, err := app.Repositories.ResolveRevision(request.Context(), repositoryID, query.Get("ref"))
	if err != nil {
		if downloadNotFound(err) {
			return archiveTarget{}, http.StatusNotFound
		}
		return archiveTarget{}, unavailable(request, "archive ref read", err)
	}
	return archiveTarget{commitOID: commitOID, format: format, name: archiveName(repositoryID, displayRef(resolved))}, http.StatusOK
}

// downloadNotFound reports whether the failed Git read of a download
// established that the ref, commit or file does not exist, or that the
// repository itself is gone. Any other failure could not tell, as when the
// repository is busy, being prepared, or its storage failed, and is answered
// as unavailable.
func downloadNotFound(err error) bool {
	return errors.Is(err, repository.ErrNotFound) || errors.Is(err, repository.ErrRepositoryNotFound)
}

// archiveRefusalMessage returns the message code and the untranslated detail
// for an archive refusal. The English message of the error stays with the API
// route; a page states the same reason in the language of its reader, and
// shows the file and the sizes a memory refusal names as data.
func archiveRefusalMessage(failure *githttp.ArchiveError) (webui.MessageCode, string) {
	switch failure.Reason {
	case githttp.ArchiveRefusalRepeatedPath:
		return webui.MsgArchiveRepeatedPath, ""
	case githttp.ArchiveRefusalManyFiles:
		return webui.MsgArchiveManyFiles, ""
	case githttp.ArchiveRefusalDeepChain:
		return webui.MsgArchiveDeepChain, ""
	case githttp.ArchiveRefusalMemory:
		return webui.MsgArchiveMemory, failure.Detail
	}
	return webui.MsgErrRefused, failure.Message
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
	// The browser guard authorized the reader before this handler.
	request = app.beginOperation(writer, request)
	target, status := app.resolveArchive(request, stored.ID)
	switch status {
	case http.StatusOK:
		if failure := app.serveArchive(writer, request, stored.ID, target); failure != nil {
			code := webui.MsgErrUnavailable
			detail := ""
			switch failure.Status {
			case http.StatusNotFound:
				code = webui.MsgErrNotFound
			case http.StatusConflict:
				// A refusal says what happened and what the owner can do
				// instead, in the language of the reader, with the file
				// and the sizes it names shown as data.
				code, detail = archiveRefusalMessage(failure)
			}
			app.renderError(writer, request, failure.Status, code, detail)
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
	request = app.beginOperation(writer, request)
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
			if failure.Reason != "" {
				// The message stays English for clients, and the code names
				// the reason a page explains in the reader's language.
				code = "archive_" + string(failure.Reason)
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
