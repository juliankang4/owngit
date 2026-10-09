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
	csrf := auth.RandomToken(32)
	// The longest accepted password, made only of four-byte characters that
	// URL-encode to twelve bytes each, is the largest valid refresh form.
	longest := strings.Repeat("\U0001F512", auth.MaximumPasswordCharacters)
	noErr(t, auth.ValidatePassword(longest))
	largest := url.Values{
		"csrf": {csrf}, "action": {webui.ActionImportRefresh}, "admin_password": {longest},
	}.Encode()
	if len(largest) > maxRefreshFormBytes {
		t.Fatalf("largest valid refresh form is %d bytes, above the %d byte peek", len(largest), maxRefreshFormBytes)
	}
	const form = "application/x-www-form-urlencoded"

	t.Run("settings error transport", func(t *testing.T) {
		app := newConfiguredApp(t)
		readErr := errors.New("body unavailable")
		for _, test := range []struct {
			name, method, path, content, media string
			parsed                             url.Values
			declared                           bool
			err                                error
			plain                              bool
		}{
			{name: "download", content: "action=backup_download", plain: true},
			{name: "download without run", path: "/settings", content: "action=backup_download", plain: true},
			{name: "ordinary save with run", content: "action=save_initial_branch"},
			{name: "query action is not body action", path: "/settings/storage?run=synthetic&action=backup_download", content: "action=save_initial_branch"},
			{name: "get with run", method: http.MethodGet, content: "action=backup_download"},
			{name: "parsed download", parsed: url.Values{"action": {"backup_download"}}, plain: true},
			{name: "parsed ordinary", parsed: url.Values{"action": {"save_initial_branch"}}, content: "action=backup_download"},
			{name: "parsed empty", parsed: url.Values{}, content: "action=backup_download"},
			{name: "malformed", content: "action=backup_download&broken=%zz"},
			{name: "oversized chunked", content: "action=backup_download&pad=" + strings.Repeat("x", maxRefreshFormBytes)},
			{name: "oversized declared", content: "action=backup_download&pad=" + strings.Repeat("x", maxRefreshFormBytes), declared: true},
			{name: "not a form", content: "action=backup_download", media: "text/plain"},
			{name: "read error", content: "action=backup_download", err: readErr},
		} {
			t.Run(test.name, func(t *testing.T) {
				media := test.media
				if media == "" {
					media = form
				}
				body := &countingBody{reader: io.MultiReader(strings.NewReader(test.content), failedReader{test.err})}
				if test.err == nil {
					body.reader = strings.NewReader(test.content)
				}
				request := refreshRequest(body, -1, media)
				request.URL, _ = url.Parse("/settings/storage?run=synthetic")
				if test.path != "" {
					request.URL, _ = url.Parse(test.path)
				}
				if test.method != "" {
					request.Method = test.method
				}
				if test.declared {
					request.ContentLength = int64(len(test.content))
				}
				request.PostForm = test.parsed
				response := httptest.NewRecorder()
				app.answerUnavailable(response, request, "settings read", errors.New("unavailable"))
				if response.Code != http.StatusServiceUnavailable || strings.HasPrefix(response.Header().Get("Content-Type"), "text/plain") != test.plain {
					t.Fatalf("status=%d type=%s", response.Code, response.Header().Get("Content-Type"))
				}
				if body.read > maxRefreshFormBytes+1 || ((test.parsed != nil || test.declared || test.media != "" || test.method == http.MethodGet) && body.read != 0) {
					t.Fatalf("unexpected peek read: %d bytes", body.read)
				}
				content, err := io.ReadAll(request.Body)
				if string(content) != test.content || !errors.Is(err, test.err) {
					t.Fatalf("body or error was not replayed: %q %v", content, err)
				}
			})
		}
	})

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
