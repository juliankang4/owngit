package webui

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"mime"
	"net/http"
	"net/url"
	"path"
	"strings"
	"sync"
	"time"
)

// assetPrefix is where the caller must mount Assets().
const assetPrefix = "/assets/"

// fingerprints maps an embedded asset path to a short content hash. The
// rendered URL carries that hash, so a new build changes every asset URL and a
// browser cannot keep serving the previous stylesheet or script. Without this,
// fixed URLs plus a long cache lifetime would hide an update until the cache
// expired.
type fingerprints map[string]string

func buildFingerprints(files fs.FS) (fingerprints, error) {
	out := fingerprints{}
	err := fs.WalkDir(files, ".", func(name string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		data, err := fs.ReadFile(files, name)
		if err != nil {
			return err
		}
		sum := sha256.Sum256(data)
		out[name] = hex.EncodeToString(sum[:])[:12]
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("fingerprint webui assets: %w", err)
	}
	return out, nil
}

// url returns the public URL for an embedded asset, including its version.
// An unknown name still returns a usable path so a template never renders an
// empty src attribute.
func (f fingerprints) url(name string) string {
	name = strings.TrimPrefix(path.Clean("/"+name), "/")
	version, ok := f[name]
	if !ok {
		return assetPrefix + name
	}
	return assetPrefix + name + "?v=" + url.QueryEscape(version)
}

// assetHandler serves the embedded stylesheet, script, font, and logo.
//
// A request carrying the current version of the file may be cached for a long
// time, because that exact URL only ever names this exact content. A request
// without it, or with an old one, gets a short lifetime and must revalidate,
// so an outdated bookmark or a hand-typed URL cannot pin stale code.
//
// Every asset carries an ETag made from its content hash, so a request for an
// outdated or unversioned URL (the font, which the stylesheet names without a
// version) is answered 304 when nothing changed. Text assets are sent gzipped
// to clients that accept it; fonts are already compressed.
func assetHandler(files fs.FS, prints fingerprints) http.Handler {
	fileServer := http.FileServer(http.FS(files))
	var zipped sync.Map // asset name to its gzipped content
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.Method != http.MethodGet && req.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		name := strings.TrimPrefix(path.Clean("/"+req.URL.Path), "/")
		if name == "" || strings.HasSuffix(req.URL.Path, "/") {
			http.NotFound(w, req)
			return
		}
		if req.URL.Query().Get("v") == prints[name] && prints[name] != "" {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		} else {
			w.Header().Set("Cache-Control", "public, max-age=300, must-revalidate")
		}
		w.Header().Set("X-Content-Type-Options", "nosniff")
		etag := `"` + prints[name] + `"`
		if prints[name] != "" {
			w.Header().Set("ETag", etag)
		}
		if isTextAsset(name) && prints[name] != "" && AcceptsGzip(req.Header) {
			content, ok := zipped.Load(name)
			if !ok {
				plain, err := fs.ReadFile(files, name)
				if err != nil {
					http.NotFound(w, req)
					return
				}
				content, _ = zipped.LoadOrStore(name, Gzip(plain))
			}
			w.Header().Set("Content-Encoding", "gzip")
			w.Header().Set("Content-Type", mime.TypeByExtension(path.Ext(name)))
			w.Header().Set("ETag", `"`+prints[name]+`-gzip"`)
			w.Header().Add("Vary", "Accept-Encoding")
			http.ServeContent(w, req, name, time.Time{}, bytes.NewReader(content.([]byte)))
			return
		}
		if isTextAsset(name) {
			w.Header().Add("Vary", "Accept-Encoding")
		}
		fileServer.ServeHTTP(w, req)
	})
}

// isTextAsset reports whether name is an uncompressed text asset.
func isTextAsset(name string) bool {
	switch path.Ext(name) {
	case ".css", ".js", ".svg", ".txt":
		return true
	}
	return false
}
