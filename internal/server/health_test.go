package server

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// The liveness check answers before and after setup with an empty body,
// refuses other methods, and keeps the Host check: an unknown Host, as a
// DNS rebinding page would send, and a loopback name from another device are
// refused even before setup.
func TestHealthAnswersWithoutStateAndKeepsTheHostCheck(t *testing.T) {
	notSetUp, _, _ := newTestApp(t)
	setUp := newConfiguredApp(t)
	local := func(method, target string) *http.Request {
		request := httptest.NewRequest(method, target, nil)
		request.RemoteAddr = "127.0.0.1:50000"
		return request
	}
	for name, app := range map[string]*App{"before setup": notSetUp, "after setup": setUp} {
		handler := app.Handler()
		for _, method := range []string{http.MethodGet, http.MethodHead} {
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, local(method, "http://127.0.0.1:7654"+HealthPath))
			body, _ := io.ReadAll(recorder.Result().Body)
			if recorder.Code != http.StatusOK || len(body) != 0 {
				t.Errorf("%s: %s %s = %d %q, want 200 and no body", name, method, HealthPath, recorder.Code, body)
			}
			if recorder.Header().Get("Cache-Control") != "no-store" {
				t.Errorf("%s: %s %s Cache-Control = %q", name, method, HealthPath, recorder.Header().Get("Cache-Control"))
			}
		}
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, local(http.MethodPost, "http://127.0.0.1:7654"+HealthPath))
		if recorder.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s: POST %s = %d, want 405", name, HealthPath, recorder.Code)
		}
		recorder = httptest.NewRecorder()
		handler.ServeHTTP(recorder, local(http.MethodGet, "http://rebound.example:7654"+HealthPath))
		if recorder.Code != http.StatusMisdirectedRequest {
			t.Errorf("%s: GET %s by an unknown Host = %d, want 421", name, HealthPath, recorder.Code)
		}
		recorder = httptest.NewRecorder()
		remote := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:7654"+HealthPath, nil)
		remote.RemoteAddr = "192.0.2.10:50000"
		handler.ServeHTTP(recorder, remote)
		if recorder.Code != http.StatusMisdirectedRequest {
			t.Errorf("%s: GET %s by a loopback name from another device = %d, want 421", name, HealthPath, recorder.Code)
		}
	}
}
