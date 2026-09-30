package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// renameAPI renames repository name through the owner API and returns the
// status and the decoded answer.
func renameAPI(t *testing.T, server, name, newName, password string) (int, repositoryResponse, string) {
	t.Helper()
	response := adminAPIRequest(t, http.MethodPost, server+"/api/v1/repositories/"+name+"/rename", map[string]string{"name": newName}, password)
	defer response.Body.Close()
	var decoded struct {
		repositoryResponse
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	noErr(t, json.NewDecoder(response.Body).Decode(&decoded))
	return response.StatusCode, decoded.repositoryResponse, decoded.Error.Code
}

// After a rename, the repository answers at its new name on every route,
// its links and clone address use it, and the old address leads there: pages
// and the API redirect after the caller is accepted, and a clone made from
// the old address keeps fetching and pushing while Git says where it went.
// Once the alias expires the old address reaches nothing.
func TestRenamedRepositoryAnswersAtItsNewNameAndTheOldOneLeadsThere(t *testing.T) {
	fixture := newAPIFixture(t, false)
	server, client, jar := openBrowser(t, fixture)
	oldClone := filepath.Join(t.TempDir(), "old")
	apiRunGit(t, "", "clone", "-q", server.URL+"/git/project.git", oldClone)

	// The Settings tab offers the rename to an administrator, and the POST
	// needs the session's CSRF token.
	signInAdmin(t, fixture, server.URL, jar)
	settings := browserGET(t, client, server.URL+"/repositories/project/settings")
	if settings.status != http.StatusOK || !strings.Contains(settings.body, `action="/repositories/project/settings/rename"`) {
		t.Fatalf("settings status=%d, rename form missing", settings.status)
	}
	refused := browserForm(t, client, server.URL+"/repositories/project/settings/rename", url.Values{"name": {"Renamed"}, "csrf": {"wrong"}}, server.URL)
	if refused.status != http.StatusForbidden {
		t.Fatalf("rename without the CSRF token status=%d", refused.status)
	}
	taken := browserForm(t, client, server.URL+"/repositories/project/settings/rename", url.Values{"name": {"new"}, "csrf": {adminTestCSRF}}, server.URL)
	if taken.status != http.StatusUnprocessableEntity || !strings.Contains(taken.body, `value="new"`) {
		t.Fatalf("rename to a reserved name status=%d", taken.status)
	}
	done := browserForm(t, client, server.URL+"/repositories/project/settings/rename", url.Values{"name": {"Renamed"}, "csrf": {adminTestCSRF}}, server.URL)
	if done.status != http.StatusSeeOther || done.header.Get("Location") != "/repositories/renamed/settings?notice=repository_renamed" {
		t.Fatalf("rename status=%d location=%q", done.status, done.header.Get("Location"))
	}
	stored, _, err := fixture.store.Repository(context.Background(), "project")
	noErr(t, err)
	if stored.Name != "Renamed" || stored.Address != "renamed" {
		t.Fatalf("stored=%+v", stored)
	}

	page := browserGET(t, client, server.URL+"/repositories/renamed/settings?notice=repository_renamed")
	if page.status != http.StatusOK || !strings.Contains(page.body, `<span class="mono" dir="auto">project</span>`) {
		t.Fatalf("renamed settings status=%d, earlier address not listed", page.status)
	}
	overview := browserGET(t, client, server.URL+"/repositories/renamed")
	if overview.status != http.StatusOK || !strings.Contains(overview.body, "/git/renamed.git") {
		t.Fatalf("renamed overview status=%d, clone address not the new one", overview.status)
	}
	for _, body := range []string{page.body, overview.body} {
		if at := strings.Index(body, `/repositories/project`); at >= 0 {
			t.Fatalf("a link names the old address: %s", body[max(0, at-200):min(len(body), at+100)])
		}
	}
	moved := browserGET(t, client, server.URL+"/repositories/project/code?ref=main")
	if moved.status != http.StatusTemporaryRedirect || moved.header.Get("Location") != "/repositories/renamed/code?ref=main" {
		t.Fatalf("old page status=%d location=%q", moved.status, moved.header.Get("Location"))
	}

	// The API redirects too, and says where the repository is now.
	request, err := http.NewRequest(http.MethodGet, server.URL+"/api/v1/repositories/project", nil)
	noErr(t, err)
	response, err := client.Do(request)
	noErr(t, err)
	var answer struct {
		Error struct {
			Code    string            `json:"code"`
			Details map[string]string `json:"details"`
		} `json:"error"`
	}
	noErr(t, json.NewDecoder(response.Body).Decode(&answer))
	response.Body.Close()
	if response.StatusCode != http.StatusTemporaryRedirect || answer.Error.Code != "repository_moved" || answer.Error.Details["address"] != "renamed" ||
		response.Header.Get("Location") != "/api/v1/repositories/renamed" {
		t.Fatalf("old API address status=%d answer=%+v", response.StatusCode, answer)
	}
	response = apiRequest(t, http.MethodGet, server.URL+"/api/v1/repositories/renamed", nil, "", "")
	var shown repositoryResponse
	noErr(t, json.NewDecoder(response.Body).Decode(&shown))
	response.Body.Close()
	if shown.Repository.ID != "project" || shown.Repository.Address != "renamed" || !strings.HasSuffix(shown.Repository.CloneURL, "/git/renamed.git") ||
		len(shown.Repository.Aliases) != 1 || shown.Repository.Aliases[0].Name != "project" {
		t.Fatalf("shown=%+v", shown.Repository)
	}

	// Git: a new clone uses the new address, and the old clone keeps
	// fetching and pushing through the alias.
	apiRunGit(t, "", "clone", "-q", server.URL+"/git/renamed.git", filepath.Join(t.TempDir(), "new"))
	output, err := gitCombined(oldClone, "fetch", "origin")
	if err != nil || !strings.Contains(output, "redirecting to "+server.URL+"/git/renamed.git/") {
		t.Fatalf("fetch through the alias err=%v output:\n%s", err, output)
	}
	apiRunGit(t, oldClone, "commit", "--allow-empty", "-m", "through the alias")
	if output, err := gitCombined(oldClone, "push", "origin", "HEAD:refs/heads/main"); err != nil {
		t.Fatalf("push through the alias: %v\n%s", err, output)
	}
	if head := apiGitOutput(t, fixture.remote, "rev-parse", "refs/heads/main"); head != apiGitOutput(t, oldClone, "rev-parse", "HEAD") {
		t.Fatalf("pushed head %s did not reach the repository", head)
	}

	// An expired alias reaches nothing.
	noErr(t, fixture.store.Exec(context.Background(), `UPDATE repository_names SET alias_until=? WHERE name='project'`, time.Now().Add(-time.Second).Unix()))
	if gone := browserGET(t, client, server.URL+"/repositories/project"); gone.status != http.StatusNotFound {
		t.Fatalf("expired page status=%d", gone.status)
	}
	if status, _ := checkStatus(t, apiRequest(t, http.MethodGet, server.URL+"/api/v1/repositories/project/pull-requests", nil, "", "")); status != http.StatusNotFound {
		t.Fatalf("expired API status=%d", status)
	}
	if output, err := gitCombined(oldClone, "fetch", "origin"); err == nil {
		t.Fatalf("fetch through an expired alias succeeded:\n%s", output)
	}
	if status, _ := checkStatus(t, apiRequest(t, http.MethodGet, server.URL+"/api/v1/repositories/renamed/pull-requests", nil, "", "")); status != http.StatusOK {
		t.Fatalf("pull requests at the new address status=%d", status)
	}
}

// Renaming needs the administrator password; general access is refused.
// Without a credential, an old address answers only the credential
// challenge, so it tells nobody where the repository went.
func TestRenameIsForTheAdministratorAndAliasesAnswerOnlyAcceptedCallers(t *testing.T) {
	fixture := newAPIFixture(t, true)
	server := serve(t, fixture.app.Handler())
	response := sendJSON(t, http.MethodPost, server.URL+"/api/v1/repositories/project/rename", map[string]string{"name": "renamed"}, basicAuth("owngit", "shared-password"))
	if status, code := checkStatus(t, response); status != http.StatusUnauthorized || code != "admin_authentication_required" {
		t.Fatalf("rename with the general password status=%d code=%s", status, code)
	}
	if status, _, code := renameAPI(t, server.URL, "project", "renamed", "wrong-password"); status != http.StatusUnauthorized || code != "invalid_admin_credentials" {
		t.Fatalf("rename with a wrong password status=%d code=%s", status, code)
	}
	if status, _, code := renameAPI(t, server.URL, "project", "OTHER.git", "admin-password"); status != http.StatusUnprocessableEntity || code != "invalid_repository_name" {
		t.Fatalf("rename to an invalid name status=%d code=%s", status, code)
	}
	status, renamed, code := renameAPI(t, server.URL, "project", "Renamed", "admin-password")
	if status != http.StatusOK || renamed.Repository.Address != "renamed" || renamed.Repository.Name != "Renamed" || renamed.Repository.ID != "project" {
		t.Fatalf("rename status=%d code=%s repository=%+v", status, code, renamed.Repository)
	}

	client, _ := newBrowserClient(t)
	if page := browserGET(t, client, server.URL+"/repositories/project"); page.status != http.StatusSeeOther || !strings.HasPrefix(page.header.Get("Location"), "/login") {
		t.Fatalf("old page without a session status=%d location=%q", page.status, page.header.Get("Location"))
	}
	if status, code := checkStatus(t, apiRequest(t, http.MethodGet, server.URL+"/api/v1/repositories/project", nil, "", "")); status != http.StatusUnauthorized || code != "authentication_required" {
		t.Fatalf("old API address without a password status=%d code=%s", status, code)
	}
	response = apiRequest(t, http.MethodGet, server.URL+"/git/project.git/info/refs?service=git-upload-pack", nil, "", "")
	response.Body.Close()
	if response.StatusCode != http.StatusUnauthorized || response.Header.Get("Location") != "" {
		t.Fatalf("old Git address without a password status=%d", response.StatusCode)
	}
	request, err := http.NewRequest(http.MethodGet, server.URL+"/git/project.git/info/refs?service=git-upload-pack", nil)
	noErr(t, err)
	request.SetBasicAuth("owngit", "shared-password")
	response, err = client.Do(request)
	noErr(t, err)
	response.Body.Close()
	if response.StatusCode != http.StatusTemporaryRedirect || response.Header.Get("Location") != "/git/renamed.git/info/refs?service=git-upload-pack" {
		t.Fatalf("old Git address with the password status=%d location=%q", response.StatusCode, response.Header.Get("Location"))
	}
}
