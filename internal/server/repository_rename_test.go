package server

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"owngit/internal/testfixture"
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
// and the API redirect after the caller is accepted, and Git at the old
// address keeps cloning, fetching and pushing while it tells the client where
// the repository is now. Once the alias expires the old address reaches
// nothing.
func TestRenamedRepositoryAnswersAtItsNewNameAndTheOldOneLeadsThere(t *testing.T) {
	fixture := newAPIFixture(t, false)
	server, client, jar := openBrowser(t, fixture)
	oldClone := filepath.Join(t.TempDir(), "old")
	apiRunGit(t, "", "clone", "-q", server.URL+"/git/project.git", oldClone)
	apiRunGit(t, oldClone, "config", "user.name", "API Test")
	apiRunGit(t, oldClone, "config", "user.email", "api-test@example.invalid")

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
	// fetching and pushing through the alias, which tells it where the
	// repository is now. A transfer that moves no data carries no message on
	// this protocol, so a commit comes first.
	newClone := filepath.Join(t.TempDir(), "new")
	apiRunGit(t, "", "clone", "-q", server.URL+"/git/renamed.git", newClone)
	apiRunGit(t, newClone, "config", "user.name", "API Test")
	apiRunGit(t, newClone, "config", "user.email", "api-test@example.invalid")
	noErr(t, os.WriteFile(filepath.Join(newClone, "second.txt"), []byte("second\n"), 0o600))
	apiRunGit(t, newClone, "add", ".")
	apiRunGit(t, newClone, "commit", "-m", "second")
	apiRunGit(t, newClone, "push", "origin", "HEAD:refs/heads/main")

	movedNotice := "remote: This repository moved to " + server.URL + "/git/renamed.git"
	output, err := gitCombined(oldClone, "fetch", "origin")
	if err != nil || !strings.Contains(output, movedNotice) {
		t.Fatalf("fetch through the alias err=%v output:\n%s", err, output)
	}
	apiRunGit(t, oldClone, "merge", "--ff-only", "origin/main")
	apiRunGit(t, oldClone, "commit", "--allow-empty", "-m", "through the alias")
	if output, err := gitCombined(oldClone, "push", "origin", "HEAD:refs/heads/main"); err != nil || !strings.Contains(output, movedNotice) {
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
	answer, readErr := io.ReadAll(response.Body)
	response.Body.Close()
	noErr(t, readErr)
	if response.StatusCode != http.StatusOK || response.Header.Get("Location") != "" {
		t.Fatalf("old Git address with the password status=%d location=%q", response.StatusCode, response.Header.Get("Location"))
	}
	if strings.Contains(string(answer), "renamed") {
		t.Fatalf("the old Git address named the repository's current address:\n%s", answer)
	}
}

// passwordGit runs Git against base with the shared password in a credential
// helper file, the way an owner's client holds it after the credential
// challenge. It returns Git's combined output.
func passwordGit(t *testing.T, base, directory string, arguments ...string) (string, error) {
	t.Helper()
	home := t.TempDir()
	emptyConfig := filepath.Join(home, "gitconfig")
	noErr(t, os.WriteFile(emptyConfig, nil, 0o600))
	credentials := filepath.Join(home, "credentials")
	stored, err := url.Parse(base)
	noErr(t, err)
	stored.User = url.UserPassword("owngit", "shared-password")
	noErr(t, os.WriteFile(credentials, []byte(stored.String()+"\n"), 0o600))
	command := exec.Command("git", append([]string{"-c", "credential.helper=", "-c", "credential.helper=store --file=" + filepath.ToSlash(credentials)}, arguments...)...)
	command.Dir = directory
	command.Env = testfixture.GitEnvironment(append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+emptyConfig, "HOME="+home))
	output, err := command.CombinedOutput()
	return string(output), err
}

// With the shared password, an existing clone keeps cloning, fetching and
// pushing at an earlier address while its alias lasts, and Git prints where
// the repository moved to. Git does not follow a redirect that arrives after
// its authentication round, so the earlier address is served directly. Before,
// every command there failed with HTTP 307 for a correct saved password.
func TestPasswordProtectedEarlierAddressServesClonesFetchesAndPushes(t *testing.T) {
	fixture := newAPIFixture(t, true)
	server := serve(t, fixture.app.Handler())
	if status, _, code := renameAPI(t, server.URL, "project", "renamed", "admin-password"); status != http.StatusOK || code != "" {
		t.Fatalf("rename status=%d code=%s", status, code)
	}
	// The address Git uses carries the shared password in its credential
	// helper, so no prompt is needed.
	earlier := server.URL + "/git/project.git"
	current := server.URL + "/git/renamed.git"
	movedNotice := "remote: This repository moved to " + server.URL + "/git/renamed.git"

	// A clone at the earlier address.
	earlierClone := filepath.Join(t.TempDir(), "earlier")
	output, err := passwordGit(t, server.URL, "", "clone", earlier, earlierClone)
	if err != nil || !strings.Contains(output, movedNotice) {
		t.Fatalf("clone at the earlier address err=%v output:\n%s", err, output)
	}
	apiRunGit(t, earlierClone, "config", "user.name", "Alias Test")
	apiRunGit(t, earlierClone, "config", "user.email", "alias-test@example.invalid")

	currentClone := filepath.Join(t.TempDir(), "current")
	if output, err := passwordGit(t, server.URL, "", "clone", "-q", current, currentClone); err != nil {
		t.Fatalf("clone at the current address err=%v output:\n%s", err, output)
	}
	apiRunGit(t, currentClone, "config", "user.name", "Alias Test")
	apiRunGit(t, currentClone, "config", "user.email", "alias-test@example.invalid")

	// A fetch that needs objects. Protocol version 0 acknowledges before the
	// pack and version 2 sends a section header before it; both carry the
	// message. A transfer that moves no data carries none.
	for _, protocol := range []string{"0", "2"} {
		noErr(t, os.WriteFile(filepath.Join(currentClone, "next-"+protocol+".txt"), []byte("next\n"), 0o600))
		apiRunGit(t, currentClone, "add", ".")
		apiRunGit(t, currentClone, "commit", "-m", "next for protocol "+protocol)
		if output, err := passwordGit(t, server.URL, currentClone, "push", "origin", "HEAD:refs/heads/main"); err != nil {
			t.Fatalf("push at the current address err=%v output:\n%s", err, output)
		}
		output, err := passwordGit(t, server.URL, earlierClone, "-c", "protocol.version="+protocol, "fetch", "origin")
		if err != nil || !strings.Contains(output, movedNotice) {
			t.Fatalf("protocol %s fetch at the earlier address err=%v output:\n%s", protocol, err, output)
		}
		if head, want := apiGitOutput(t, earlierClone, "rev-parse", "refs/remotes/origin/main"), apiGitOutput(t, fixture.remote, "rev-parse", "refs/heads/main"); head != want {
			t.Fatalf("protocol %s fetched %s, want %s", protocol, head, want)
		}
	}

	// A push through the earlier address reaches the repository.
	apiRunGit(t, earlierClone, "merge", "--ff-only", "origin/main")
	noErr(t, os.WriteFile(filepath.Join(earlierClone, "pushed.txt"), []byte("pushed\n"), 0o600))
	apiRunGit(t, earlierClone, "add", ".")
	apiRunGit(t, earlierClone, "commit", "-m", "through the earlier address")
	if output, err = passwordGit(t, server.URL, earlierClone, "push", "origin", "HEAD:refs/heads/main"); err != nil || !strings.Contains(output, movedNotice) {
		t.Fatalf("push at the earlier address err=%v output:\n%s", err, output)
	}
	if head, want := apiGitOutput(t, fixture.remote, "rev-parse", "refs/heads/main"), apiGitOutput(t, earlierClone, "rev-parse", "HEAD"); head != want {
		t.Fatalf("the pushed commit %s is at %s", want, head)
	}
}

// A protocol 2 fetch that the server answers with a section before the pack
// sends a four-byte delimiter between the two, as a shallow fetch does. The
// notice scan must step over that delimiter exactly, or it reads the pack as
// garbage, stops, and the clone loses the message. Before, a shallow clone at
// an earlier address fetched silently.
func TestShallowCloneAtAnEarlierAddressNamesTheCurrentOne(t *testing.T) {
	fixture := newAPIFixture(t, true)
	server := serve(t, fixture.app.Handler())
	if status, _, code := renameAPI(t, server.URL, "project", "renamed", "admin-password"); status != http.StatusOK || code != "" {
		t.Fatalf("rename status=%d code=%s", status, code)
	}
	movedNotice := "remote: This repository moved to " + server.URL + "/git/renamed.git"
	clone := filepath.Join(t.TempDir(), "shallow")
	output, err := passwordGit(t, server.URL, "", "-c", "protocol.version=2", "clone", "--depth=1", server.URL+"/git/project.git", clone)
	if err != nil || !strings.Contains(output, movedNotice) {
		t.Fatalf("shallow clone at the earlier address err=%v output:\n%s", err, output)
	}
	if shallow, err := os.Stat(filepath.Join(clone, ".git", "shallow")); err != nil {
		t.Fatalf("the clone is not shallow, so the server sent no shallow section: %v", err)
	} else if shallow.IsDir() {
		t.Fatalf("the shallow marker is a directory")
	}

	// A later fetch that deepens the clone asks for the same section.
	full := filepath.Join(t.TempDir(), "full")
	if output, err := passwordGit(t, server.URL, "", "-c", "protocol.version=2", "clone", "-q", server.URL+"/git/project.git", full); err != nil || !strings.Contains(output, movedNotice) {
		t.Fatalf("clone at the earlier address err=%v output:\n%s", err, output)
	}
	if output, err := passwordGit(t, server.URL, full, "-c", "protocol.version=2", "fetch", "--depth=1", "origin"); err != nil || !strings.Contains(output, movedNotice) {
		t.Fatalf("shallow fetch at the earlier address err=%v output:\n%s", err, output)
	}
}

// Runner and helper credentials reach their repository at its current name
// and at every unexpired alias. At an alias that has expired they get the
// same "another repository" refusal as at a name that reaches nothing, so a
// credential never learns whether a name exists.
func TestScopedCredentialsFollowAliasesUntilTheyExpire(t *testing.T) {
	fixture := newAPIFixture(t, false)
	base, helper := helperAPI(t, fixture, "helper", time.Now())
	origin := strings.TrimSuffix(base, "/api/v1/repositories/project")
	setRunnerPolicy(t, fixture.store)
	_, runner, _, err := fixture.store.IssueCheckRunnerToken(context.Background(), "project", "runner", "", time.Now())
	noErr(t, err)
	for _, name := range []string{"middle", "current"} {
		_, err = fixture.app.Repositories.Rename(context.Background(), "project", name, time.Now())
		noErr(t, err)
	}
	requests := []struct{ method, path, token, refusal string }{
		{http.MethodGet, "/tasks", helper, "helper_credential_scope"},
		{http.MethodPost, "/runner/claim", runner, "runner_credential_repository_mismatch"},
	}
	for _, name := range []string{"project", "middle", "current"} {
		for _, request := range requests {
			if status, code := checkStatus(t, checkRequest(t, request.method, origin+"/api/v1/repositories/"+name+request.path, nil, request.token)); status != http.StatusOK {
				t.Fatalf("%s at %s status=%d code=%s", request.path, name, status, code)
			}
		}
	}
	noErr(t, fixture.store.Exec(context.Background(), `UPDATE repository_names SET alias_until=? WHERE kind='alias'`, time.Now().Add(-time.Second).Unix()))
	for _, name := range []string{"project", "middle", "nosuchrepo"} {
		for _, request := range requests {
			if status, code := checkStatus(t, checkRequest(t, request.method, origin+"/api/v1/repositories/"+name+request.path, nil, request.token)); status != http.StatusForbidden || code != request.refusal {
				t.Fatalf("%s at expired %s status=%d code=%s", request.path, name, status, code)
			}
		}
	}
}

// An import route may name a repository that does not exist yet only by an
// unused name. At an expired alias, whether a renamed repository's first
// name or a later one, every import route is not found and changes nothing,
// and a first import at an unused name still starts.
func TestImportRoutesAtAnExpiredAliasAreNotFound(t *testing.T) {
	fixture := newAPIFixture(t, true)
	server := serve(t, fixture.app.Handler())
	for _, name := range []string{"middle", "current"} {
		_, err := fixture.app.Repositories.Rename(context.Background(), "project", name, time.Now())
		noErr(t, err)
	}
	noErr(t, fixture.store.Exec(context.Background(), `UPDATE repository_names SET alias_until=? WHERE kind='alias'`, time.Now().Add(-time.Second).Unix()))
	source := map[string]any{"url": "https://example.invalid/expired.git", "mode": "coexistence", "git_only_consent": true}
	for _, name := range []string{"project", "middle"} {
		for _, request := range []struct {
			method, path string
			body         any
		}{
			{http.MethodGet, "", nil},
			{http.MethodPut, "", source},
			{http.MethodPost, "/run", map[string]any{"name": name, "url": "https://example.invalid/expired.git", "mode": "standalone"}},
			{http.MethodPost, "/cancel", map[string]any{}},
			{http.MethodDelete, "/credentials", nil},
		} {
			response := importAPIRequest(t, request.method, server.URL+"/api/v1/repositories/"+name+"/import"+request.path, request.body, "admin-password")
			if body := importAPIBody(t, response); response.StatusCode != http.StatusNotFound || !strings.Contains(body, "repository_not_found") {
				t.Fatalf("%s %s/import%s status=%d body=%s", request.method, name, request.path, response.StatusCode, body)
			}
		}
	}
	for _, id := range []string{"project", "middle"} {
		if _, exists, err := fixture.store.ImportSource(context.Background(), id); err != nil || exists {
			t.Fatalf("an import source was saved for %q: exists=%v err=%v", id, exists, err)
		}
	}
	response := importAPIRequest(t, http.MethodPost, server.URL+"/api/v1/repositories/fresh/import/run", map[string]any{"name": "fresh", "url": "https://example.invalid/fresh.git", "mode": "standalone"}, "admin-password")
	if body := importAPIBody(t, response); response.StatusCode != http.StatusOK || !strings.Contains(body, `"status":"complete"`) {
		t.Fatalf("first import at an unused name status=%d body=%s", response.StatusCode, body)
	}
}

// The owner routes for the default branch and deletion find a renamed
// repository at its current address. Its earlier address, while the alias
// lasts, redirects there and changes nothing; once the alias has expired it
// is not found.
func TestOwnerRoutesFollowTheRepositoryAddress(t *testing.T) {
	fixture := newAPIFixture(t, false)
	server := serve(t, fixture.app.Handler())
	_, err := fixture.app.Repositories.Rename(context.Background(), "project", "renamed", time.Now())
	noErr(t, err)
	noRedirect := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	post := func(name, resource string, body map[string]string) (int, string, string) {
		t.Helper()
		encoded, err := json.Marshal(body)
		noErr(t, err)
		request, err := http.NewRequest(http.MethodPost, server.URL+"/api/v1/repositories/"+name+"/"+resource, bytes.NewReader(encoded))
		noErr(t, err)
		request.Header.Set("Content-Type", "application/json")
		request.SetBasicAuth("admin", "admin-password")
		response, err := noRedirect.Do(request)
		noErr(t, err)
		location := response.Header.Get("Location")
		status, code := checkStatus(t, response)
		return status, code, location
	}
	deletion := map[string]string{"mode": "keep_files", "confirm_name": "renamed"}
	for resource, body := range map[string]map[string]string{"default-branch": {"branch": "feature"}, "delete": deletion} {
		if status, code, location := post("project", resource, body); status != http.StatusTemporaryRedirect || code != "repository_moved" || location != "/api/v1/repositories/renamed/"+resource {
			t.Fatalf("%s at the earlier address status=%d code=%q location=%q", resource, status, code, location)
		}
	}
	if head := apiGitOutput(t, fixture.remote, "symbolic-ref", "HEAD"); head != "refs/heads/main" || !fixtureRepositoryExists(t, fixture, "project") {
		t.Fatalf("a redirected request changed the repository: default branch=%q", head)
	}
	if status, code, _ := post("renamed", "default-branch", map[string]string{"branch": "feature"}); status != http.StatusOK || code != "" {
		t.Fatalf("default branch at the current address status=%d code=%q", status, code)
	}
	if head := apiGitOutput(t, fixture.remote, "symbolic-ref", "HEAD"); head != "refs/heads/feature" {
		t.Fatalf("default branch=%q", head)
	}

	noErr(t, fixture.store.Exec(context.Background(), `UPDATE repository_names SET alias_until=? WHERE kind='alias'`, time.Now().Add(-time.Second).Unix()))
	for resource, body := range map[string]map[string]string{"default-branch": {"branch": "main"}, "delete": deletion} {
		if status, code, location := post("project", resource, body); status != http.StatusNotFound || code != "repository_not_found" || location != "" {
			t.Fatalf("%s at the expired address status=%d code=%q location=%q", resource, status, code, location)
		}
	}
	if !fixtureRepositoryExists(t, fixture, "project") {
		t.Fatal("a request at the expired address deleted the repository")
	}
	if status, code, _ := post("renamed", "delete", deletion); status != http.StatusOK || code != "" || fixtureRepositoryExists(t, fixture, "project") {
		t.Fatalf("deletion at the current address status=%d code=%q", status, code)
	}
}

// The reads across repositories name each repository's current address
// beside its ID, and the task view of one repository finds it by its
// address: an earlier name redirects while its alias lasts, and is not
// found after it expires.
func TestReadViewsFollowARenamedRepository(t *testing.T) {
	fixture := newAPIFixture(t, false)
	ctx := context.Background()
	task, err := fixture.store.CreateTask(ctx, "project", "Renamed task", time.Now().UTC())
	noErr(t, err)
	_, _, _, err = fixture.app.issueHelperCredential(ctx, "project", "laptop helper", "")
	noErr(t, err)
	_, err = fixture.app.Repositories.Rename(ctx, "project", "renamed", time.Now())
	noErr(t, err)
	server := serve(t, fixture.app.Handler())

	for _, target := range []string{taskViewAPIPath, taskViewAPIPath + "/renamed"} {
		var listed taskViewListResponse
		if status := decodeAPI(t, sendJSON(t, http.MethodGet, server.URL+target, nil), &listed); status != http.StatusOK ||
			len(listed.Tasks) != 1 || listed.Tasks[0].RepositoryID != "project" || listed.Tasks[0].RepositoryAddress != "renamed" {
			t.Fatalf("%s status=%d %+v", target, status, listed.Tasks)
		}
	}
	var detail taskDetailResponse
	if status := decodeAPI(t, sendJSON(t, http.MethodGet, server.URL+taskViewAPIPath+"/renamed/"+task.ID, nil), &detail); status != http.StatusOK || detail.Task.ID != task.ID {
		t.Fatalf("task detail at the current address status=%d", status)
	}
	noRedirect := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := noRedirect.Get(server.URL + taskViewAPIPath + "/project/" + task.ID)
	noErr(t, err)
	location := response.Header.Get("Location")
	if status, code := checkStatus(t, response); status != http.StatusTemporaryRedirect || code != "repository_moved" || location != taskViewAPIPath+"/renamed/"+task.ID {
		t.Fatalf("task detail at the earlier address status=%d code=%s location=%q", status, code, location)
	}

	var credentials struct {
		Credentials []struct {
			RepositoryID      string `json:"repository_id"`
			RepositoryAddress string `json:"repository_address"`
		} `json:"credentials"`
	}
	if status := decodeAPI(t, adminAPIRequest(t, http.MethodGet, server.URL+allHelperCredentialsAPIPath, nil, "admin-password"), &credentials); status != http.StatusOK ||
		len(credentials.Credentials) != 1 || credentials.Credentials[0].RepositoryID != "project" || credentials.Credentials[0].RepositoryAddress != "renamed" {
		t.Fatalf("credential list status=%d %+v", status, credentials)
	}
	activity := completeActivity(t, server.URL+activityAPIPath)
	if len(activity.Entries) == 0 {
		t.Fatal("no activity entries")
	}
	for _, entry := range activity.Entries {
		if entry.Repository != "project" || entry.RepositoryAddress != "renamed" {
			t.Fatalf("activity entry %+v", entry)
		}
	}
	client, jar := newBrowserClient(t)
	signInAdmin(t, fixture, server.URL, jar)
	coding := browserGET(t, client, server.URL+codingToolsPath)
	if coding.status != http.StatusOK || !strings.Contains(coding.body, tasksURL("renamed", task.ID)) || !strings.Contains(coding.body, baseHelperCredentialsURL("renamed")) ||
		strings.Contains(coding.body, "/repositories/project") {
		t.Fatalf("Coding tools page status=%d does not link the current address", coding.status)
	}

	noErr(t, fixture.store.Exec(ctx, `UPDATE repository_names SET alias_until=? WHERE kind='alias'`, time.Now().Add(-time.Second).Unix()))
	if status, code := checkStatus(t, sendJSON(t, http.MethodGet, server.URL+taskViewAPIPath+"/project", nil)); status != http.StatusNotFound || code != "repository_not_found" {
		t.Fatalf("tasks at the expired address status=%d code=%s", status, code)
	}
}
