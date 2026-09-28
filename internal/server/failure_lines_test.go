package server

import (
	"net/http"
	"strings"
	"testing"
)

// While OwnGit state cannot be read every request fails the same way. The
// failure is logged once with the request that met it first; the requests
// after it, whatever their method and path, are counted instead of each
// adding a line, and the count is logged.
func TestRepeatedFailureIsLoggedOnce(t *testing.T) {
	fixture := newAPIFixture(t, false)
	server := serve(t, fixture.app.Handler())
	restore := hideTable(t, fixture.store, "metadata")
	defer restore()
	serverLog := captureServerLog(t)
	for _, target := range []struct{ method, path string }{
		{http.MethodGet, "/"}, {http.MethodGet, "/settings"}, {http.MethodPost, "/login"},
		{http.MethodGet, "/repositories/" + strings.Repeat("x", 300)}, {http.MethodGet, "/api/v1/repositories"},
	} {
		request, err := http.NewRequest(target.method, server.URL+target.path, nil)
		noErr(t, err)
		response, err := http.DefaultClient.Do(request)
		noErr(t, err)
		response.Body.Close()
		if response.StatusCode != http.StatusServiceUnavailable {
			t.Fatalf("%s %s status=%d", target.method, target.path, response.StatusCode)
		}
	}
	lines := loggedFailures(serverLog, 0)
	if len(lines) != 2 || !strings.Contains(lines[0], " GET /: settings read could not be completed: ") ||
		!strings.Contains(lines[1], " settings read could not be completed 4 more times ") || strings.Contains(lines[1], "GET /") {
		t.Fatalf("five failed requests logged:\n%s", strings.Join(lines, "\n"))
	}
}

// A request's method is cut in its log line as its path is, so a request
// cannot make one line as long as its method.
func TestLongMethodIsCutInTheLog(t *testing.T) {
	fixture := newAPIFixture(t, false)
	server := serve(t, fixture.app.Handler())
	restore := hideTable(t, fixture.store, "metadata")
	defer restore()
	serverLog := captureServerLog(t)
	method := strings.Repeat("M", 32768)
	request, err := http.NewRequest(method, server.URL+"/", nil)
	noErr(t, err)
	response, err := http.DefaultClient.Do(request)
	noErr(t, err)
	response.Body.Close()
	lines := loggedFailures(serverLog, 0)
	if len(lines) != 1 || len(lines[0]) > 1024 || !strings.Contains(lines[0], "...(cut, 32768 bytes) /: settings read could not be completed") {
		t.Fatalf("status=%d, %d lines, first %d bytes", response.StatusCode, len(lines), len(strings.Join(lines, "")))
	}
}
