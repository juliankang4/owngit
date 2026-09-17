package webui

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func newAssetRequest(t *testing.T, target string) *http.Request {
	t.Helper()
	return httptest.NewRequest(http.MethodGet, target, nil)
}

func newRecorder() *httptest.ResponseRecorder { return httptest.NewRecorder() }

func TestAssetsServeTheEmbeddedFiles(t *testing.T) {
	r := newRenderer(t)
	for _, name := range []string{"owngit.css", "owngit.js", "logo.svg"} {
		rec := httptest.NewRecorder()
		r.Assets().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/"+name, nil))
		if rec.Code != http.StatusOK {
			t.Errorf("%s: status %d", name, rec.Code)
		}
		if rec.Body.Len() == 0 {
			t.Errorf("%s: empty response", name)
		}
		if rec.Header().Get("X-Content-Type-Options") != "nosniff" {
			t.Errorf("%s: content type is not pinned", name)
		}
	}
}

func TestAssetsRejectWritesAndDirectoryListings(t *testing.T) {
	r := newRenderer(t)

	rec := httptest.NewRecorder()
	r.Assets().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/owngit.css", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST to an asset returned %d", rec.Code)
	}

	for _, target := range []string{"/", "/fonts/"} {
		rec = httptest.NewRecorder()
		r.Assets().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, target, nil))
		if rec.Code != http.StatusNotFound {
			t.Errorf("listing %q returned %d, expected 404", target, rec.Code)
		}
	}
}

func TestAssetsDoNotEscapeTheEmbeddedTree(t *testing.T) {
	r := newRenderer(t)
	for _, target := range []string{
		"/../webui.go",
		"/..%2f..%2fgo.mod",
		"/fonts/../../webui.go",
	} {
		rec := httptest.NewRecorder()
		r.Assets().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, target, nil))
		if rec.Code == http.StatusOK && strings.Contains(rec.Body.String(), "package webui") {
			t.Errorf("%q served a file outside the asset tree", target)
		}
	}
}

func TestRenderWritesToAnOrdinaryWriter(t *testing.T) {
	// Render must not touch response headers or status: the caller owns those.
	r := newRenderer(t)
	rec := httptest.NewRecorder()
	if err := r.Render(rec.Body, OverviewPage{Chrome: fullChrome(LangEN), Activity: sampleGraph()}); err != nil {
		t.Fatal(err)
	}
	if len(rec.Header()) != 0 {
		t.Errorf("Render set response headers: %v", rec.Header())
	}
	if rec.Body.Len() == 0 {
		t.Fatal("Render produced no output")
	}
}
