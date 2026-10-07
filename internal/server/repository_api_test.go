package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"owngit/internal/pullrequest"
	"owngit/internal/recovery"
	"owngit/internal/state"
	"owngit/internal/webui"
)

func decodeRepositoryResponse(t *testing.T, response *http.Response, want int) repositoryResponse {
	t.Helper()
	defer response.Body.Close()
	if response.StatusCode != want {
		t.Fatalf("status=%d, want %d", response.StatusCode, want)
	}
	var decoded repositoryResponse
	noErr(t, json.NewDecoder(response.Body).Decode(&decoded))
	if !decoded.OK {
		t.Fatal("repository response reported ok=false")
	}
	return decoded
}

func decodeRepositoryList(t *testing.T, response *http.Response) repositoryListResponse {
	t.Helper()
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("list status=%d", response.StatusCode)
	}
	var decoded repositoryListResponse
	noErr(t, json.NewDecoder(response.Body).Decode(&decoded))
	if !decoded.OK {
		t.Fatal("repository list reported ok=false")
	}
	return decoded
}

func TestRepositoryAPIListsShowsAndCreatesRepositories(t *testing.T) {
	fixture := newAPIFixture(t, false)
	server := serve(t, fixture.app.Handler())
	collection := server.URL + "/api/v1/repositories"

	listed := decodeRepositoryList(t, apiRequest(t, http.MethodGet, collection, nil, "", ""))
	if listed.Truncated || len(listed.Repositories) != 1 {
		t.Fatalf("list=%+v", listed)
	}
	project := listed.Repositories[0]
	if project.ID != "project" || project.Name != "project" || project.Description != "API fixture" ||
		project.CloneURL != server.URL+"/git/project.git" || project.CreatedAt.IsZero() || project.DefaultBranch != "" {
		t.Fatalf("listed repository=%+v", project)
	}

	shown := decodeRepositoryResponse(t, apiRequest(t, http.MethodGet, collection+"/project", nil, "", ""), http.StatusOK)
	if shown.Repository.ID != "project" || shown.Repository.DefaultBranch != "main" || shown.Repository.CloneURL != project.CloneURL {
		t.Fatalf("shown repository=%+v", shown.Repository)
	}
	if response := apiRequest(t, http.MethodGet, collection+"/missing", nil, "", ""); response.StatusCode != http.StatusNotFound || apiErrorCode(t, response) != "repository_not_found" {
		t.Fatalf("missing repository status=%d", response.StatusCode)
	}

	created := decodeRepositoryResponse(t, apiRequest(t, http.MethodPost, collection, map[string]string{
		"name": "  Second.Repo ", "description": " made by the API ",
	}, "", ""), http.StatusCreated)
	if created.Repository.ID != "second.repo" || created.Repository.Name != "Second.Repo" || created.Repository.Description != "made by the API" ||
		created.Repository.CloneURL != server.URL+"/git/second.repo.git" {
		t.Fatalf("created repository=%+v", created.Repository)
	}
	if _, err := fixture.app.Repositories.Path("second.repo"); err != nil {
		t.Fatalf("created repository has no storage: %v", err)
	}
	// An empty repository has no default branch to report yet.
	if shown := decodeRepositoryResponse(t, apiRequest(t, http.MethodGet, collection+"/second.repo", nil, "", ""), http.StatusOK); shown.Repository.ID != "second.repo" {
		t.Fatalf("shown new repository=%+v", shown.Repository)
	}
	listed = decodeRepositoryList(t, apiRequest(t, http.MethodGet, collection, nil, "", ""))
	if len(listed.Repositories) != 2 || !listed.Repositories[1].CreatedAt.Equal(created.Repository.CreatedAt) {
		t.Fatalf("list after create=%+v, created at %v", listed, created.Repository.CreatedAt)
	}

	for _, test := range []struct {
		name   string
		input  map[string]string
		status int
		code   string
	}{
		{"duplicate name", map[string]string{"name": "PROJECT"}, http.StatusConflict, "repository_exists"},
		{"reserved name", map[string]string{"name": "new"}, http.StatusUnprocessableEntity, "reserved_repository_name"},
		{"invalid name", map[string]string{"name": "bad/name"}, http.StatusUnprocessableEntity, "invalid_repository_name"},
		{"git suffix", map[string]string{"name": "mirror.git"}, http.StatusUnprocessableEntity, "invalid_repository_name"},
		{"empty name", map[string]string{"name": " "}, http.StatusUnprocessableEntity, "invalid_repository_name"},
		{"long description", map[string]string{"name": "described", "description": strings.Repeat("d", 501)}, http.StatusUnprocessableEntity, "invalid_repository_description"},
	} {
		response := apiRequest(t, http.MethodPost, collection, test.input, "", "")
		if response.StatusCode != test.status || apiErrorCode(t, response) != test.code {
			t.Errorf("%s: status=%d, want %d %s", test.name, response.StatusCode, test.status, test.code)
		}
	}
	// A Windows device name meets the character rules, so the refusal names
	// device names too.
	for _, name := range []string{"CON", "aux.txt", "lpt1"} {
		response := apiRequest(t, http.MethodPost, collection, map[string]string{"name": name}, "", "")
		var envelope pullrequest.ErrorEnvelope
		noErr(t, json.NewDecoder(response.Body).Decode(&envelope))
		response.Body.Close()
		if response.StatusCode != http.StatusUnprocessableEntity || envelope.Error.Code != "invalid_repository_name" || !strings.Contains(envelope.Error.Message, "Windows device name") {
			t.Errorf("%s: status=%d error=%+v", name, response.StatusCode, envelope.Error)
		}
	}
	if response := apiRequest(t, http.MethodPost, collection, map[string]any{"name": "extra", "admin": true}, "", ""); response.StatusCode != http.StatusBadRequest || apiErrorCode(t, response) != "invalid_json" {
		t.Fatalf("unknown field status=%d", response.StatusCode)
	}
}

func TestRepositoryAPIKeepsGeneralAccessAndCrossSiteRules(t *testing.T) {
	fixture := newAPIFixture(t, true)
	server := serve(t, fixture.app.Handler())
	collection := server.URL + "/api/v1/repositories"

	for _, path := range []string{collection, collection + "/project"} {
		if response := apiRequest(t, http.MethodGet, path, nil, "", ""); response.StatusCode != http.StatusUnauthorized || apiErrorCode(t, response) != "authentication_required" {
			t.Fatalf("%s without password status=%d", path, response.StatusCode)
		}
		if response := apiRequest(t, http.MethodGet, path, nil, "wrong-password", ""); response.StatusCode != http.StatusUnauthorized || apiErrorCode(t, response) != "invalid_credentials" {
			t.Fatalf("%s with a wrong password status=%d", path, response.StatusCode)
		}
	}
	// Authorization comes before existence, so a caller without the password
	// learns nothing about which repositories exist.
	if response := apiRequest(t, http.MethodGet, collection+"/missing", nil, "", ""); response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("missing repository without password status=%d", response.StatusCode)
	}
	if response := apiRequest(t, http.MethodPost, collection, map[string]string{"name": "blocked"}, "", ""); response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("create without password status=%d", response.StatusCode)
	}
	if listed := decodeRepositoryList(t, apiRequest(t, http.MethodGet, collection, nil, "shared-password", "")); len(listed.Repositories) != 1 {
		t.Fatalf("list with password=%+v", listed)
	}

	foreign := apiRequest(t, http.MethodPost, collection, map[string]string{"name": "foreign"}, "shared-password", "https://foreign.example")
	if foreign.StatusCode != http.StatusForbidden || apiErrorCode(t, foreign) != "origin_mismatch" {
		t.Fatalf("foreign Origin status=%d", foreign.StatusCode)
	}
	form, err := http.NewRequest(http.MethodPost, collection, strings.NewReader("name=form"))
	noErr(t, err)
	form.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	form.SetBasicAuth("owngit", "shared-password")
	formResponse, err := http.DefaultClient.Do(form)
	noErr(t, err)
	if formResponse.StatusCode != http.StatusUnsupportedMediaType || apiErrorCode(t, formResponse) != "json_required" {
		t.Fatalf("form create status=%d", formResponse.StatusCode)
	}
	for _, test := range []struct{ method, path string }{
		{http.MethodDelete, collection + "/project"}, {http.MethodPost, collection + "/project"},
		{http.MethodPut, collection}, {http.MethodDelete, collection},
	} {
		response := apiRequest(t, test.method, test.path, nil, "shared-password", "")
		if response.StatusCode != http.StatusMethodNotAllowed || apiErrorCode(t, response) != "method_not_allowed" {
			t.Errorf("%s %s status=%d", test.method, test.path, response.StatusCode)
		}
	}
	if response := apiRequest(t, http.MethodGet, collection+"?limit=1", nil, "shared-password", ""); response.StatusCode != http.StatusBadRequest {
		t.Fatalf("query parameters status=%d", response.StatusCode)
	}
	if exists := fixtureRepositoryExists(t, fixture, "foreign") || fixtureRepositoryExists(t, fixture, "form") || fixtureRepositoryExists(t, fixture, "blocked"); exists {
		t.Fatal("a refused request created a repository")
	}
	created := decodeRepositoryResponse(t, apiRequest(t, http.MethodPost, collection, map[string]string{"name": "allowed"}, "shared-password", server.URL), http.StatusCreated)
	if created.Repository.ID != "allowed" {
		t.Fatalf("created repository=%+v", created.Repository)
	}
}

func TestRepositoryAPICreationFailureCanRetryAndPreservesUnknownFolders(t *testing.T) {
	fixture := newAPIFixture(t, false)
	server := serve(t, fixture.app.Handler())
	collection := server.URL + "/api/v1/repositories"
	ctx := context.Background()
	noErr(t, fixture.store.Exec(ctx, `CREATE TRIGGER refuse_creation BEFORE INSERT ON repositories BEGIN SELECT RAISE(ABORT,'recording refused'); END`))
	response := apiRequest(t, http.MethodPost, collection, map[string]string{"name": "fresh"}, "", "")
	if response.StatusCode != http.StatusServiceUnavailable || apiErrorCode(t, response) != "repository_create_failed" {
		t.Fatalf("recording failure status=%d", response.StatusCode)
	}
	noErr(t, fixture.store.Exec(ctx, `DROP TRIGGER refuse_creation`))
	decodeRepositoryResponse(t, apiRequest(t, http.MethodPost, collection, map[string]string{"name": "fresh"}, "", ""), http.StatusCreated)
	unknown := filepath.Join(fixture.app.Repositories.RepositoryRoot(), "unknown.git")
	noErr(t, os.Mkdir(unknown, 0o700))
	noErr(t, os.WriteFile(filepath.Join(unknown, "keep"), []byte("unrelated data"), 0o600))
	response = apiRequest(t, http.MethodPost, collection, map[string]string{"name": "unknown"}, "", "")
	var envelope pullrequest.ErrorEnvelope
	noErr(t, json.NewDecoder(response.Body).Decode(&envelope))
	response.Body.Close()
	if response.StatusCode != http.StatusConflict || envelope.Error.Code != "repository_exists" || !strings.Contains(envelope.Error.Message, "has not adopted or removed") || !strings.Contains(envelope.Error.Message, "Choose another name") {
		t.Fatalf("unknown folder status=%d error=%+v", response.StatusCode, envelope.Error)
	}
	if data, err := os.ReadFile(filepath.Join(unknown, "keep")); err != nil || string(data) != "unrelated data" {
		t.Fatalf("unknown folder changed: %q %v", data, err)
	}
	decodeRepositoryResponse(t, apiRequest(t, http.MethodPost, collection, map[string]string{"name": "another"}, "", ""), http.StatusCreated)
}

func TestFailedCreationPreservationIsNotARepositoryOrImportIssue(t *testing.T) {
	fixture := newAPIFixture(t, false)
	server := serve(t, fixture.app.Handler())
	collection := server.URL + "/api/v1/repositories"
	ctx := context.Background()
	serverLog := captureServerLog(t)
	noErr(t, fixture.store.Exec(ctx, `CREATE TRIGGER refuse_creation BEFORE INSERT ON repositories BEGIN SELECT RAISE(ABORT,'recording refused'); END`))
	response := apiRequest(t, http.MethodPost, collection, map[string]string{"name": "fresh"}, "", "")
	var failure pullrequest.ErrorEnvelope
	noErr(t, json.NewDecoder(response.Body).Decode(&failure))
	response.Body.Close()
	wantFailure := webui.Text(webui.LangEN, webui.MsgRepoCreateFail)
	if response.StatusCode != http.StatusServiceUnavailable || failure.Error.Code != "repository_create_failed" || failure.Error.Message != wantFailure || !strings.Contains(failure.Error.Message, "owngit doctor") || !strings.Contains(failure.Error.Message, "server log") {
		t.Fatalf("failed create status=%d error=%+v", response.StatusCode, failure.Error)
	}
	noErr(t, fixture.store.Exec(ctx, `DROP TRIGGER refuse_creation`))
	keptRoot := filepath.Join(fixture.app.Repositories.RepositoryRoot(), ".owngit-failed-create")
	kept, err := os.ReadDir(keptRoot)
	noErr(t, err)
	if len(kept) != 1 || !kept[0].IsDir() {
		t.Fatalf("preserved folders=%v", kept)
	}
	// Causes are quoted in the log, including doubled Windows separators.
	loggedRoot := strconv.Quote(keptRoot)
	if !strings.Contains(serverLog.String(), loggedRoot[1:len(loggedRoot)-1]) || !strings.Contains(serverLog.String(), "may be removed") {
		t.Fatal("log does not name the removable empty preservation folder")
	}
	listed := decodeRepositoryList(t, apiRequest(t, http.MethodGet, collection, nil, "", ""))
	if len(listed.Repositories) != 1 || listed.Repositories[0].ID != "project" {
		t.Fatalf("preserved folder appeared in repository listing: %+v", listed)
	}
	if taken, err := fixture.store.RepositoryNameInUse(ctx, "fresh", time.Now()); err != nil || taken {
		t.Fatalf("preserved creation blocked its name: taken=%v err=%v", taken, err)
	}
	noErr(t, fixture.app.Imports.Reconcile(ctx))
	initials, err := fixture.store.ImportInitialDestinationsPage(ctx, "", 100)
	noErr(t, err)
	issues, err := fixture.store.ImportStagingsPage(ctx, "", 101)
	more := len(issues) > 100
	if err != nil || len(initials) != 0 || len(issues) != 0 || more {
		t.Fatalf("preservation became an import issue: initials=%v issues=%v more=%v err=%v", initials, issues, more, err)
	}
	// Exercise the real import publication path with the fixture's empty
	// fetch provider. It can reuse the failed name without touching the folder.
	imported := importAPIRequest(t, http.MethodPost, server.URL+"/api/v1/repositories/fresh/import/run", map[string]any{
		"name": "fresh", "url": "https://example.invalid/team/fresh.git", "mode": "standalone",
	}, "admin-password")
	if imported.StatusCode != http.StatusOK || !strings.Contains(importAPIBody(t, imported), `"status":"complete"`) {
		t.Fatalf("same-name import status=%d", imported.StatusCode)
	}
	backup := filepath.Join(t.TempDir(), "backup")
	_, err = recovery.CreateWithReport(ctx, fixture.store, fixture.app.Repositories, backup)
	noErr(t, err)
	manifestFile, err := os.Open(filepath.Join(backup, "manifest.json"))
	noErr(t, err)
	defer manifestFile.Close()
	var manifest struct {
		Repositories []struct {
			ID string `json:"id"`
		} `json:"repositories"`
	}
	noErr(t, json.NewDecoder(manifestFile).Decode(&manifest))
	if len(manifest.Repositories) != 2 || manifest.Repositories[0].ID != "fresh" || manifest.Repositories[1].ID != "project" {
		t.Fatalf("backup repository IDs=%+v", manifest.Repositories)
	}
	if _, err := os.Stat(filepath.Join(keptRoot, kept[0].Name(), "config")); err != nil {
		t.Fatalf("imports or backup removed the preserved tree: %v", err)
	}
}

func TestCreationPreservationLimitNamesOnlyTheRequestedFolder(t *testing.T) {
	fixture := newAPIFixture(t, false)
	server := serve(t, fixture.app.Handler())
	collection := server.URL + "/api/v1/repositories"
	ctx := context.Background()
	noErr(t, fixture.store.Exec(ctx, `CREATE TRIGGER refuse_creation BEFORE INSERT ON repositories BEGIN SELECT RAISE(ABORT,'recording refused'); END`))
	defer fixture.store.Exec(ctx, `DROP TRIGGER refuse_creation`)
	for attempt := 0; attempt < 9; attempt++ {
		response := apiRequest(t, http.MethodPost, collection, map[string]string{"name": "Kept"}, "", "")
		var envelope pullrequest.ErrorEnvelope
		noErr(t, json.NewDecoder(response.Body).Decode(&envelope))
		response.Body.Close()
		if response.StatusCode != http.StatusServiceUnavailable {
			t.Fatalf("attempt %d status=%d", attempt, response.StatusCode)
		}
		if attempt == 8 && (envelope.Error.Code != "repository_create_kept" || !strings.Contains(envelope.Error.Message, "kept.git") || !strings.Contains(envelope.Error.Message, "move it aside")) {
			t.Fatalf("full preservation response=%+v", envelope.Error)
		}
		if strings.Contains(envelope.Error.Message, fixture.app.Repositories.RepositoryRoot()) || strings.Contains(envelope.Error.Message, ".owngit-failed-create") {
			t.Fatal("creation response exposed a private or hidden preservation path")
		}
	}
	for _, lang := range []webui.Lang{webui.LangEN, webui.LangKO} {
		client, jar := newBrowserClient(t)
		browserGET(t, client, server.URL+"/repositories/new?lang="+string(lang))
		name := "browser-" + string(lang)
		response := browserForm(t, client, server.URL+"/repositories", url.Values{
			"csrf": {cookieValue(t, jar, server.URL, generalCookie)}, "name": {name},
		}, server.URL)
		if response.status != http.StatusServiceUnavailable || !strings.Contains(response.body, webui.Text(lang, webui.MsgRepoCreationKept)) || !strings.Contains(response.body, name+".git") {
			t.Fatalf("kept browser response language=%s status=%d", lang, response.status)
		}
		if strings.Contains(response.body, fixture.app.Repositories.RepositoryRoot()) || strings.Contains(response.body, ".owngit-failed-create") {
			t.Fatal("browser response exposed a private or hidden preservation path")
		}
	}
}

func lazyStorageCollision(t *testing.T) (*App, *App, string) {
	t.Helper()
	first, _, root := newTestApp(t)
	second, _, _ := newTestApp(t)
	for _, app := range []*App{first, second} {
		if feedback, err := app.CompleteSetup(context.Background(), setupAnswers(root), true); err != nil || len(feedback.Problems) != 0 || len(feedback.Warnings) != 0 {
			t.Fatalf("empty-root setup feedback=%+v err=%v", feedback, err)
		}
	}
	_, err := first.Repositories.Create(context.Background(), "owner", "")
	noErr(t, err)
	return first, second, root
}

func TestLazyStorageClaimRefusalExplainsTheOwnerInAPI(t *testing.T) {
	_, second, root := lazyStorageCollision(t)
	server := serve(t, second.Handler())
	before, err := os.ReadDir(root)
	noErr(t, err)
	head, err := os.ReadFile(filepath.Join(root, "owner.git", "HEAD"))
	noErr(t, err)
	response := apiRequest(t, http.MethodPost, server.URL+"/api/v1/repositories", map[string]string{"name": "blocked"}, "", "")
	var envelope pullrequest.ErrorEnvelope
	noErr(t, json.NewDecoder(response.Body).Decode(&envelope))
	response.Body.Close()
	if response.StatusCode != http.StatusConflict || envelope.Error.Code != "repository_storage_in_use" || envelope.Error.Message != webui.Text(webui.LangEN, webui.MsgSetupStorageInUse) {
		t.Errorf("storage refusal status=%d error=%+v", response.StatusCode, envelope.Error)
	}
	after, err := os.ReadDir(root)
	noErr(t, err)
	if len(after) != len(before) {
		t.Fatal("refused creation wrote into the owner's root")
	}
	if current, err := os.ReadFile(filepath.Join(root, "owner.git", "HEAD")); err != nil || string(current) != string(head) {
		t.Fatalf("owner's HEAD changed: %q %v", current, err)
	}
}

func TestLazyStorageClaimRefusalExplainsTheOwnerInBrowser(t *testing.T) {
	_, second, root := lazyStorageCollision(t)
	server := serve(t, second.Handler())
	for _, lang := range []webui.Lang{webui.LangEN, webui.LangKO} {
		client, jar := newBrowserClient(t)
		browserGET(t, client, server.URL+"/repositories/new?lang="+string(lang))
		response := browserForm(t, client, server.URL+"/repositories", url.Values{
			"csrf": {cookieValue(t, jar, server.URL, generalCookie)}, "name": {"blocked"},
		}, server.URL)
		if response.status != http.StatusConflict || !strings.Contains(response.body, webui.Text(lang, webui.MsgSetupStorageInUse)) {
			t.Errorf("storage refusal language=%s status=%d body=%s", lang, response.status, response.body)
		}
	}
	if _, err := os.Lstat(filepath.Join(root, "blocked.git")); !os.IsNotExist(err) {
		t.Fatalf("refused browser creation published a directory: %v", err)
	}
}

func fixtureRepositoryExists(t *testing.T, fixture apiFixture, id string) bool {
	t.Helper()
	_, exists, err := fixture.app.Store.Repository(context.Background(), id)
	noErr(t, err)
	return exists
}

// The clone address comes from the shared clone-URL helper, so a configured
// base URL replaces the request's Host, and a long list stops at the bound
// and says so.
func TestRepositoryAPIUsesTheCloneAddressAndBoundsTheList(t *testing.T) {
	fixture := newAPIFixture(t, false)
	fixture.app.Network = NewLiveNetwork(LiveNetworkConfig{BaseURL: "https://owngit.example.test", Hosts: fixture.app.Hosts})
	server := serve(t, fixture.app.Handler())
	ctx := context.Background()
	for index := 0; index < maximumListedRepositories; index++ {
		id := fmt.Sprintf("extra-%04d", index)
		noErr(t, fixture.app.Store.AddRepository(ctx, state.Repository{ID: id, Name: id, CreatedAt: time.Now()}))
	}
	listed := decodeRepositoryList(t, apiRequest(t, http.MethodGet, server.URL+"/api/v1/repositories", nil, "", ""))
	if !listed.Truncated || len(listed.Repositories) != maximumListedRepositories {
		t.Fatalf("list truncated=%v length=%d", listed.Truncated, len(listed.Repositories))
	}
	if first := listed.Repositories[0]; first.CloneURL != "https://owngit.example.test/git/"+first.ID+".git" {
		t.Fatalf("clone URL=%q", first.CloneURL)
	}
}
