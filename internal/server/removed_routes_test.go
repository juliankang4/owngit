package server

import (
	"context"
	"encoding/json"
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

// The built-in review feature is gone with its packages and routes. These tests
// pin the paths it used to own, so a later change cannot quietly revive a dead
// endpoint or drop the access guard that still sits in front of one.
const (
	removedProbeID   = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	removedRequestID = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
)

// removedAPIPaths lists every route the removed API dispatch answered: settings,
// credential, probes, stored requests, and the two pull request routes. Each one
// now falls through to the pull request parser and misses.
func removedAPIPaths(pullRequest string) []string {
	return []string{
		"/api/v1/repositories/project/direct-review/settings",
		"/api/v1/repositories/project/direct-review/credential",
		"/api/v1/repositories/project/direct-review/probes",
		"/api/v1/repositories/project/direct-review/probes/" + removedProbeID,
		"/api/v1/repositories/project/direct-review/probes/" + removedProbeID + "/cancel",
		"/api/v1/repositories/project/direct-reviews/" + removedRequestID,
		"/api/v1/repositories/project/direct-reviews/" + removedRequestID + "/cancel",
		"/api/v1/repositories/project/pull-requests/" + pullRequest + "/direct-reviews",
		"/api/v1/repositories/project/pull-requests/" + pullRequest + "/direct-review/confirmation",
	}
}

// removedBrowserRequests lists the removed screens and forms with the method
// each one actually answered: the settings screen and both record views were
// GETs, while saving settings, cancelling a probe or a request, and starting or
// cancelling a pull request review were form POSTs.
func removedBrowserRequests(pullRequest string) []struct{ method, path string } {
	return []struct{ method, path string }{
		{http.MethodGet, "/repositories/project/direct-review"},
		{http.MethodPost, "/repositories/project/direct-review"},
		{http.MethodGet, "/repositories/project/direct-review/probes/" + removedProbeID},
		{http.MethodPost, "/repositories/project/direct-review/probes/" + removedProbeID + "/cancel"},
		{http.MethodGet, "/repositories/project/direct-reviews/" + removedRequestID},
		{http.MethodPost, "/repositories/project/direct-reviews/" + removedRequestID + "/cancel"},
		{http.MethodPost, "/repositories/project/pull-requests/" + pullRequest + "/direct-review"},
		{http.MethodGet, "/repositories/project/pull-requests/" + pullRequest + "/direct-review/confirm"},
		{http.MethodPost, "/repositories/project/pull-requests/" + pullRequest + "/direct-review/cancel"},
	}
}

func createRouteFixturePullRequest(t *testing.T, fixture apiFixture) string {
	t.Helper()
	created, err := fixture.app.PullRequests.Create(context.Background(), pullrequest.CreateInput{
		Repository: "project", Title: "Removed route fixture", SourceBranch: "feature", TargetBranch: "main",
		SourceOID: fixture.sourceOID, TargetOID: fixture.targetOID, ReviewChoice: webui.ReviewChoiceRequest,
	})
	if err != nil {
		t.Fatal(err)
	}
	return strconv.FormatInt(created.Number, 10)
}

func TestRemovedReviewRoutesAreUnavailable(t *testing.T) {
	fixture := newAPIFixture(t, false)
	number := createRouteFixturePullRequest(t, fixture)
	server := httptest.NewServer(fixture.app.Handler())
	defer server.Close()
	client, jar := newBrowserClient(t)
	before, err := fixture.store.PullRequests(context.Background(), "project")
	if err != nil || len(before) != 1 {
		t.Fatalf("pull requests=%d err=%v", len(before), err)
	}

	// Every method misses on every removed path, because the whole resource
	// family left the dispatch rather than a handful of handlers.
	for _, path := range removedAPIPaths(number) {
		for _, method := range []string{http.MethodGet, http.MethodPost, http.MethodPut, http.MethodDelete} {
			t.Run(method+" "+path, func(t *testing.T) {
				response := apiRequest(t, method, server.URL+path, map[string]any{}, "", server.URL)
				defer response.Body.Close()
				var envelope pullrequest.ErrorEnvelope
				if err := json.NewDecoder(response.Body).Decode(&envelope); err != nil {
					t.Fatal(err)
				}
				if response.StatusCode != http.StatusNotFound || envelope.Error.Code != "not_found" {
					t.Fatalf("status=%d code=%q", response.StatusCode, envelope.Error.Code)
				}
			})
		}
	}

	// The removed forms are sent as form POSTs with a real administrator session
	// and its token, so a guard cannot turn the request away before the routing
	// decision, and a re-added route would have to answer for itself.
	// The removed screens were administrator only in the old dispatch.
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
	for _, removed := range removedBrowserRequests(number) {
		t.Run(removed.method+" "+removed.path, func(t *testing.T) {
			result := browserGET(t, client, server.URL+removed.path)
			if removed.method == http.MethodPost {
				result = browserForm(t, client, server.URL+removed.path, url.Values{
					"csrf": {adminSession.CSRF}, "request_id": {removedRequestID},
				}, server.URL)
			}
			if result.status != http.StatusNotFound {
				t.Fatalf("removed browser route answered %d", result.status)
			}
		})
	}

	// The pages that stayed must not point at a removed route any more.
	for _, path := range []string{
		"/repositories/project",
		"/repositories/project/code",
		"/repositories/project/pull-requests",
		"/repositories/project/pull-requests/" + number,
		"/repositories/project/tasks",
	} {
		result := browserGET(t, client, server.URL+path)
		if result.status != http.StatusOK || strings.Contains(result.body, "/direct-review") {
			t.Fatalf("ordinary page %s answered %d", path, result.status)
		}
	}

	// No removed route and no removed form may leave a record or touch one.
	after, err := fixture.store.PullRequests(context.Background(), "project")
	if err != nil || len(after) != 1 || !after[0].UpdatedAt.Equal(before[0].UpdatedAt) {
		t.Fatalf("removed routes changed pull request state: count=%d err=%v", len(after), err)
	}
}

// The pull request history route was the only API endpoint that accepted query
// parameters. The parser refuses every query string before a route is chosen,
// so the old exception is gone while the routes themselves still work.
func TestRemovedReviewQueryExceptionIsGone(t *testing.T) {
	fixture := newAPIFixture(t, false)
	number := createRouteFixturePullRequest(t, fixture)
	server := httptest.NewServer(fixture.app.Handler())
	defer server.Close()

	refused := []string{
		"/api/v1/repositories/project/pull-requests/" + number + "/direct-reviews?cursor=2",
		"/api/v1/repositories/project/pull-requests/" + number + "?cursor=2",
		"/api/v1/repositories/project/tasks?cursor=1",
	}
	for _, path := range refused {
		response := apiRequest(t, http.MethodGet, server.URL+path, nil, "", server.URL)
		defer response.Body.Close()
		var envelope pullrequest.ErrorEnvelope
		if err := json.NewDecoder(response.Body).Decode(&envelope); err != nil {
			t.Fatal(err)
		}
		if response.StatusCode != http.StatusBadRequest || envelope.Error.Code != "invalid_request" {
			t.Fatalf("%s answered status=%d code=%q", path, response.StatusCode, envelope.Error.Code)
		}
	}

	for _, path := range []string{
		"/api/v1/repositories/project/pull-requests",
		"/api/v1/repositories/project/pull-requests/" + number,
	} {
		response := apiRequest(t, http.MethodGet, server.URL+path, nil, "", server.URL)
		defer response.Body.Close()
		if response.StatusCode != http.StatusOK {
			t.Fatalf("route %s answered %d", path, response.StatusCode)
		}
	}
}

// Under shared-password access the guard runs before the route decision, so a
// removed path answers a sign-in redirect or 401 rather than not_found.
func TestRemovedReviewRoutesStillPassTheAccessGuard(t *testing.T) {
	fixture := newAPIFixture(t, true)
	number := createRouteFixturePullRequest(t, fixture)
	server := httptest.NewServer(fixture.app.Handler())
	defer server.Close()
	client, jar := newBrowserClient(t)
	apiPath := "/api/v1/repositories/project/direct-review/settings"
	browserPath := "/repositories/project/pull-requests/" + number + "/direct-review/confirm"

	response := apiRequest(t, http.MethodGet, server.URL+apiPath, nil, "", server.URL)
	defer response.Body.Close()
	var envelope pullrequest.ErrorEnvelope
	if err := json.NewDecoder(response.Body).Decode(&envelope); err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusUnauthorized || envelope.Error.Code != "authentication_required" {
		t.Fatalf("anonymous status=%d code=%q", response.StatusCode, envelope.Error.Code)
	}

	response = apiRequest(t, http.MethodGet, server.URL+apiPath, nil, "shared-password", server.URL)
	defer response.Body.Close()
	if err := json.NewDecoder(response.Body).Decode(&envelope); err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusNotFound || envelope.Error.Code != "not_found" {
		t.Fatalf("authenticated status=%d code=%q", response.StatusCode, envelope.Error.Code)
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

	// With a session the same path is reached again, and only then does the
	// missing route answer for itself.
	if signedIn := browserGET(t, client, server.URL+browserPath); signedIn.status != http.StatusNotFound {
		t.Fatalf("signed-in browser status=%d", signedIn.status)
	}
	if ordinary := browserGET(t, client, server.URL+"/repositories/project/pull-requests/"+number); ordinary.status != http.StatusOK {
		t.Fatalf("ordinary page answered %d", ordinary.status)
	}
}
