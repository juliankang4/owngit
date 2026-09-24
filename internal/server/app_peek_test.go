package server

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"owngit/internal/auth"
	"owngit/internal/webui"
)

// countingBody records how many bytes were read before authentication.
type countingBody struct {
	reader io.Reader
	read   int
}

func (body *countingBody) Read(p []byte) (int, error) {
	n, err := body.reader.Read(p)
	body.read += n
	return n, err
}

func (body *countingBody) Close() error { return nil }

func refreshRequest(body io.ReadCloser, contentLength int64, contentType string) *http.Request {
	request := httptest.NewRequest(http.MethodPost, "/repositories/project/import", nil)
	request.Body = body
	request.ContentLength = contentLength
	request.Header.Set("Content-Type", contentType)
	return request
}

func TestRefreshPeekReadsOnlyWhatTheFormNeeds(t *testing.T) {
	csrf, err := auth.RandomToken(32)
	noErr(t, err)
	// The longest accepted password, made only of bytes that URL-encode to
	// three characters, is the largest valid refresh form.
	largest := url.Values{
		"csrf": {csrf}, "action": {webui.ActionImportRefresh}, "admin_password": {strings.Repeat("%", 1024)},
	}.Encode()
	if len(largest) > maxRefreshFormBytes {
		t.Fatalf("largest valid refresh form is %d bytes, above the %d byte peek", len(largest), maxRefreshFormBytes)
	}
	const form = "application/x-www-form-urlencoded"

	t.Run("valid refresh form", func(t *testing.T) {
		body := &countingBody{reader: strings.NewReader(largest)}
		request := refreshRequest(body, -1, form)
		if !importRunRequest(request) {
			t.Fatal("largest valid refresh form did not get the import run timeout")
		}
		assertBody(t, request, largest)
	})

	t.Run("oversized chunked body", func(t *testing.T) {
		content := "action=" + webui.ActionImportRefresh + "&pad=" + strings.Repeat("x", 1<<20)
		body := &countingBody{reader: strings.NewReader(content)}
		request := refreshRequest(body, -1, form)
		if importRunRequest(request) {
			t.Fatal("oversized body got the import run timeout")
		}
		if body.read > maxRefreshFormBytes+1 {
			t.Fatalf("peek read %d bytes before authentication", body.read)
		}
		assertBody(t, request, content)
	})

	t.Run("declared oversized body", func(t *testing.T) {
		body := &countingBody{reader: strings.NewReader("action=" + webui.ActionImportRefresh)}
		request := refreshRequest(body, maxRefreshFormBytes+1, form)
		if importRunRequest(request) || body.read != 0 {
			t.Fatalf("declared oversized body was read: %d bytes", body.read)
		}
	})

	t.Run("not a URL-encoded form", func(t *testing.T) {
		body := &countingBody{reader: strings.NewReader("action=" + webui.ActionImportRefresh)}
		request := refreshRequest(body, -1, "text/plain")
		if importRunRequest(request) || body.read != 0 {
			t.Fatalf("non-form body was read: %d bytes", body.read)
		}
	})

	t.Run("read error stays visible", func(t *testing.T) {
		readErr := errors.New("connection reset")
		partial := io.MultiReader(strings.NewReader("action="+webui.ActionImportRefresh), failedReader{readErr})
		request := refreshRequest(io.NopCloser(partial), -1, form)
		if importRunRequest(request) {
			t.Fatal("failed body got the import run timeout")
		}
		content, err := io.ReadAll(request.Body)
		if !errors.Is(err, readErr) || string(content) != "action="+webui.ActionImportRefresh {
			t.Fatalf("handler body=%q err=%v, want the prefix and the read error", content, err)
		}
	})
}

func assertBody(t *testing.T, request *http.Request, want string) {
	t.Helper()
	content, err := io.ReadAll(request.Body)
	noErr(t, err)
	if string(content) != want {
		t.Fatalf("handler received %d body bytes, want %d", len(content), len(want))
	}
}
