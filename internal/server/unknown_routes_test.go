package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"owngit/internal/pullrequest"
	"owngit/internal/webui"
)

func createRouteFixturePullRequest(t *testing.T, fixture apiFixture) string {
	t.Helper()
	created, err := fixture.app.PullRequests.Create(context.Background(), pullrequest.CreateInput{
		Repository: "project", Title: "Route fixture", SourceBranch: "feature", TargetBranch: "main",
		SourceOID: fixture.sourceOID, TargetOID: fixture.targetOID, ReviewChoice: webui.ReviewChoiceRequest,
	})
	if err != nil {
		t.Fatal(err)
	}
	return strconv.FormatInt(created.Number, 10)
}

// An unknown resource misses for every method, both beside the repository
// resources and below a pull request, and changes no record. Browser forms
// are sent with a real administrator session and its token, so no guard can
// turn them away before the routing decision.
func TestUnknownRoutesAnswerNotFoundWithoutSideEffects(t *testing.T) {
	fixture := newAPIFixture(t, false)
	number := createRouteFixturePullRequest(t, fixture)
	server := httptest.NewServer(fixture.app.Handler())
	defer server.Close()
	client, jar := newBrowserClient(t)
	before, err := fixture.store.PullRequests(context.Background(), "project")
	if err != nil || len(before) != 1 {
		t.Fatalf("pull requests=%d err=%v", len(before), err)
	}
	for _, path := range []string{
		"/api/v1/repositories/project/unknown",
		"/api/v1/repositories/project/unknown/" + strings.Repeat("b", 32) + "/cancel",
		"/api/v1/repositories/project/pull-requests/" + number + "/unknown",
	} {
		for _, method := range []string{http.MethodGet, http.MethodPost, http.MethodPut, http.MethodDelete} {
			response := apiRequest(t, method, server.URL+path, map[string]any{}, "", server.URL)
			if status, code := response.StatusCode, apiErrorCode(t, response); status != http.StatusNotFound || code != "not_found" {
				t.Fatalf("%s %s status=%d code=%q", method, path, status, code)
			}
		}
	}

	if form := browserGET(t, client, server.URL+"/admin/login"); form.status != http.StatusOK {
		t.Fatalf("admin login form answered %d", form.status)
	}
	login := browserForm(t, client, server.URL+"/admin/login", url.Values{
		"csrf": {cookieValue(t, jar, server.URL, preauthCookie)}, "admin_password": {"admin-password"}, "next": {"/"},
	}, server.URL)
	if login.status != http.StatusSeeOther {
		t.Fatalf("admin login answered %d", login.status)
	}
	adminSession, ok, err := fixture.store.Session(context.Background(), cookieValue(t, jar, server.URL, adminCookie), "admin", time.Now())
	if err != nil || !ok {
		t.Fatalf("admin session ok=%v err=%v", ok, err)
	}
	for _, path := range []string{"/repositories/project/unknown", "/repositories/project/pull-requests/" + number + "/unknown"} {
		if result := browserGET(t, client, server.URL+path); result.status != http.StatusNotFound {
			t.Fatalf("GET %s answered %d", path, result.status)
		}
		result := browserForm(t, client, server.URL+path, url.Values{"csrf": {adminSession.CSRF}}, server.URL)
		if result.status != http.StatusNotFound {
			t.Fatalf("POST %s answered %d", path, result.status)
		}
	}

	after, err := fixture.store.PullRequests(context.Background(), "project")
	if err != nil || len(after) != 1 || !after[0].UpdatedAt.Equal(before[0].UpdatedAt) {
		t.Fatalf("unknown routes changed pull request state: count=%d err=%v", len(after), err)
	}
}

// Only the import history route accepts query parameters. The parser refuses
// every other query string before a route is chosen.
func TestAPIRefusesQueryParametersOutsideImportHistory(t *testing.T) {
	fixture := newAPIFixture(t, false)
	number := createRouteFixturePullRequest(t, fixture)
	server := httptest.NewServer(fixture.app.Handler())
	defer server.Close()

	for _, path := range []string{
		"/api/v1/repositories/project/pull-requests/" + number + "?cursor=2",
		"/api/v1/repositories/project/tasks?cursor=1",
	} {
		response := apiRequest(t, http.MethodGet, server.URL+path, nil, "", server.URL)
		if status, code := response.StatusCode, apiErrorCode(t, response); status != http.StatusBadRequest || code != "invalid_request" {
			t.Fatalf("%s answered status=%d code=%q", path, status, code)
		}
	}
	for _, path := range []string{
		"/api/v1/repositories/project/pull-requests",
		"/api/v1/repositories/project/pull-requests/" + number,
	} {
		response := apiRequest(t, http.MethodGet, server.URL+path, nil, "", server.URL)
		response.Body.Close()
		if response.StatusCode != http.StatusOK {
			t.Fatalf("route %s answered %d", path, response.StatusCode)
		}
	}
}

// Under shared-password access the guard runs before the route decision, so
// an unknown path answers a sign-in redirect or 401 rather than not_found.
func TestAccessGuardRunsBeforeTheRouteDecision(t *testing.T) {
	fixture := newAPIFixture(t, true)
	number := createRouteFixturePullRequest(t, fixture)
	server := httptest.NewServer(fixture.app.Handler())
	defer server.Close()
	client, jar := newBrowserClient(t)
	apiPath := "/api/v1/repositories/project/unknown"
	browserPath := "/repositories/project/pull-requests/" + number + "/unknown"

	response := apiRequest(t, http.MethodGet, server.URL+apiPath, nil, "", server.URL)
	if status, code := response.StatusCode, apiErrorCode(t, response); status != http.StatusUnauthorized || code != "authentication_required" {
		t.Fatalf("anonymous status=%d code=%q", status, code)
	}
	response = apiRequest(t, http.MethodGet, server.URL+apiPath, nil, "shared-password", server.URL)
	if status, code := response.StatusCode, apiErrorCode(t, response); status != http.StatusNotFound || code != "not_found" {
		t.Fatalf("authenticated status=%d code=%q", status, code)
	}

	redirected := browserGET(t, client, server.URL+browserPath)
	if redirected.status != http.StatusSeeOther || !strings.HasPrefix(redirected.header.Get("Location"), "/login?") {
		t.Fatalf("anonymous browser status=%d location=%q", redirected.status, redirected.header.Get("Location"))
	}
	if form := browserGET(t, client, server.URL+"/login"); form.status != http.StatusOK {
		t.Fatalf("login form answered %d", form.status)
	}
	login := browserForm(t, client, server.URL+"/login", url.Values{
		"csrf": {cookieValue(t, jar, server.URL, preauthCookie)}, "password": {"shared-password"}, "next": {"/"},
	}, server.URL)
	if login.status != http.StatusSeeOther {
		t.Fatalf("login answered %d", login.status)
	}
	if signedIn := browserGET(t, client, server.URL+browserPath); signedIn.status != http.StatusNotFound {
		t.Fatalf("signed-in browser status=%d", signedIn.status)
	}
	if ordinary := browserGET(t, client, server.URL+"/repositories/project/pull-requests/"+number); ordinary.status != http.StatusOK {
		t.Fatalf("ordinary page answered %d", ordinary.status)
	}
}
