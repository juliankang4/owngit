package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"owngit/internal/state"
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
	fixture.app.BaseURL = "https://owngit.example.test"
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
