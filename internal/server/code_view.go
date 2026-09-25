package server

import (
	"bytes"
	"context"
	"errors"
	"html/template"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
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

// inlineImageTypes are the raster formats the file view shows as a picture
// through the raw endpoint. SVG is left out on purpose: it can carry script
// and links, so it is offered only as a download (its text is shown as
// source like any other text file).
var inlineImageTypes = map[string]bool{
	"image/png":  true,
	"image/jpeg": true,
	"image/gif":  true,
	"image/webp": true,
}

// inlineImage reports whether filePath names a raster picture whose first
// bytes match its type, and its pixel size when it can be read from the
// header (zero otherwise). A file named .png that holds something else is
// treated as an ordinary binary file.
func inlineImage(filePath string, head []byte) (bool, int, int) {
	declared := rawTypes[strings.ToLower(path.Ext(filePath))]
	if !inlineImageTypes[declared] || http.DetectContentType(head) != declared {
		return false, 0, 0
	}
	if declared == "image/webp" {
		width, height := webpSize(head)
		return true, width, height
	}
	config, _, err := image.DecodeConfig(bytes.NewReader(head))
	if err != nil || config.Width <= 0 || config.Height <= 0 {
		return true, 0, 0
	}
	return true, config.Width, config.Height
}

// webpSize reads a WebP picture's pixel size from its first chunk header, so
// the page can reserve its space. The standard library has no WebP decoder;
// the three first-chunk forms are small fixed layouts (RFC 9649). An
// unrecognised header reports zero, and the picture is still shown.
func webpSize(head []byte) (int, int) {
	if len(head) < 30 || string(head[0:4]) != "RIFF" || string(head[8:12]) != "WEBP" {
		return 0, 0
	}
	le24 := func(b []byte) int { return int(b[0]) | int(b[1])<<8 | int(b[2])<<16 }
	switch string(head[12:16]) {
	case "VP8X":
		// Canvas width and height minus one, 24 bits each.
		return le24(head[24:27]) + 1, le24(head[27:30]) + 1
	case "VP8 ":
		// A key frame: 3-byte frame tag, start code 9d 01 2a, then 14-bit
		// width and height.
		if head[23] != 0x9d || head[24] != 0x01 || head[25] != 0x2a {
			return 0, 0
		}
		return (int(head[26]) | int(head[27])<<8) & 0x3fff, (int(head[28]) | int(head[29])<<8) & 0x3fff
	case "VP8L":
		// Signature 0x2f, then width and height minus one, 14 bits each.
		if head[20] != 0x2f {
			return 0, 0
		}
		bits := uint32(head[21]) | uint32(head[22])<<8 | uint32(head[23])<<16 | uint32(head[24])<<24
		return int(bits&0x3fff) + 1, int(bits>>14&0x3fff) + 1
	}
	return 0, 0
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
