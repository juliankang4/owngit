package server

import (
	"context"
	"errors"
	"html/template"
	"mime"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"

	"owngit/internal/markdown"
	"owngit/internal/repository"
	"owngit/internal/state"
	"owngit/internal/webui"
)

// maximumRawBytes bounds one raw file response. A larger file is refused
// rather than cut, since a cut download would look complete.
const maximumRawBytes = 10 << 20

// rawURL is the address that downloads one file at ref. Relative images in a
// rendered document load through it too.
func rawURL(repositoryID, ref, filePath string) string {
	values := url.Values{"ref": []string{ref}, "path": []string{filePath}}
	return "/repositories/" + url.PathEscape(repositoryID) + "/raw?" + values.Encode()
}

// renderMarkdown renders a repository document found in dir at ref. Links to
// other files open them in the code view at the same ref, and relative images
// load through the raw endpoint. When the document is not rendered, the
// returned code says why: it is too large or too complex, every render slot
// stayed busy, or this server cannot run its render helper.
func (app *App) renderMarkdown(ctx context.Context, repositoryID, ref, dir string, source []byte) (template.HTML, webui.MessageCode) {
	// The document is rendered in a child process, so its links are given as
	// address prefixes; each is completed with a query-escaped path.
	base := "/repositories/" + url.PathEscape(repositoryID)
	ref = url.QueryEscape(ref)
	rendered, err := markdown.Render(ctx, source, markdown.Links{
		Dir:  dir,
		File: base + "/code?ref=" + ref + "&path=",
		Raw:  base + "/raw?ref=" + ref + "&path=",
	})
	if errors.Is(err, markdown.ErrBusy) {
		return "", webui.MsgCodeBusy
	}
	if errors.Is(err, markdown.ErrUnavailable) {
		return "", webui.MsgCodeUnavailable
	}
	if err != nil {
		return "", webui.MsgCodeNotShown
	}
	// markdown.Render writes no raw HTML from the file and resolves every
	// address itself, which is what makes this conversion safe.
	return template.HTML(rendered), ""
}

// folderReadme renders the README of a folder listing, if it has one that is
// a Markdown file. A README that cannot be rendered is still named, with the
// reason and a link to open it. A failure to read it leaves it out.
func (app *App) folderReadme(request *http.Request, repositoryID, ref, dir string, entries []repository.TreeEntry) *webui.ReadmeView {
	var found *repository.TreeEntry
	for index := range entries {
		entry := &entries[index]
		if entry.Type != "blob" || entry.Mode == "120000" || !markdown.IsDocument(entry.Name) {
			continue
		}
		base := strings.ToLower(strings.TrimSuffix(entry.Name, path.Ext(entry.Name)))
		if base == "readme" && (found == nil || entry.Name < found.Name) {
			found = entry
		}
	}
	if found == nil {
		return nil
	}
	view := &webui.ReadmeView{Path: found.Path, URL: codeURL(repositoryID, ref, found.Path), Note: webui.MsgReadmeNotShown}
	if found.Size > markdown.MaxSource {
		return view
	}
	_, blob, err := app.Repositories.ReadBlob(request.Context(), repositoryID, ref, found.Path, markdown.MaxSource)
	if err != nil || blob.Binary {
		return nil
	}
	if blob.Truncated {
		return view
	}
	rendered, reason := app.renderMarkdown(request.Context(), repositoryID, ref, dir, blob.Content)
	switch reason {
	case "":
		view.Rendered, view.Note = rendered, ""
	case webui.MsgCodeBusy:
		view.Note = webui.MsgReadmeBusy
	case webui.MsgCodeUnavailable:
		view.Note = webui.MsgReadmeUnavailable
	}
	return view
}

// rawTypes are the only content types the raw endpoint names. Every other
// file is sent as bytes to download.
var rawTypes = map[string]string{
	".png":  "image/png",
	".jpg":  "image/jpeg",
	".jpeg": "image/jpeg",
	".gif":  "image/gif",
	".webp": "image/webp",
	".svg":  "image/svg+xml",
}

// handleRaw sends one file as a download. It serves the file view's download
// link and the images of rendered documents, under the same access rules as
// the file view, which the caller has already applied.
//
// Repository content must never run in OwnGit's origin. The response is
// therefore always an attachment, so opening its address downloads it instead
// of displaying it; it is never typed as HTML; and its content security
// policy sandboxes it and allows no script, in case a browser displays it
// anyway. An SVG picture still shows where a page embeds it as an image,
// because a browser runs no script in an image.
func (app *App) handleRaw(writer http.ResponseWriter, request *http.Request, stored state.Repository) {
	query := request.URL.Query()
	filePath := query.Get("path")
	_, blob, err := app.Repositories.ReadBlob(request.Context(), stored.ID, query.Get("ref"), filePath, maximumRawBytes)
	if err != nil {
		app.renderError(writer, request, http.StatusNotFound, webui.MsgErrNotFound, request.URL.Path)
		return
	}
	if blob.Truncated {
		// The file view already hides the link for such a file; this answers
		// an address typed or kept from before.
		app.renderError(writer, request, http.StatusForbidden, webui.MsgCodeRawTooLarge, "")
		return
	}
	contentType, ok := rawTypes[strings.ToLower(path.Ext(filePath))]
	if !ok {
		contentType = "application/octet-stream"
	}
	header := writer.Header()
	header.Set("Content-Type", contentType)
	disposition := mime.FormatMediaType("attachment", map[string]string{"filename": path.Base(filePath)})
	if disposition == "" {
		disposition = "attachment"
	}
	header.Set("Content-Disposition", disposition)
	header.Set("Content-Length", strconv.Itoa(len(blob.Content)))
	header.Set("X-Content-Type-Options", "nosniff")
	header.Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; sandbox")
	// The address names a branch or tag, which can move, and access can be
	// withdrawn, so a browser keeps the file briefly and for itself only.
	header.Set("Cache-Control", "private, max-age=60")
	writer.WriteHeader(http.StatusOK)
	// A HEAD request gets the same headers; the server drops the body.
	_, _ = writer.Write(blob.Content)
}
