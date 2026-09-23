package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"owngit/internal/importfetch"
	"owngit/internal/importgit"
	"owngit/internal/importsync"
	"owngit/internal/repository"
)

func TestImportAPIRequiresOwnerAndRejectsCSRF(t *testing.T) {
	fixture := newImportAPIFixture(t)
	server := httptest.NewServer(fixture.app.Handler())
	t.Cleanup(server.Close)
	endpoint := server.URL + "/api/v1/repositories/project/import"
	unauthenticated := importAPIRequest(t, http.MethodPut, endpoint, map[string]any{"url": "https://example.invalid/team/project.git"}, "", "", "")
	if unauthenticated.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthenticated status=%d", unauthenticated.StatusCode)
	}
	settings, err := fixture.store.Settings(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := fixture.store.CreateSession(context.Background(), "import-admin", "admin", "import-csrf", settings.AdminSessionVersion, time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	wrong := importSessionRequest(t, http.MethodPut, endpoint, map[string]any{"url": "https://example.invalid/team/project.git"}, "wrong-csrf", "admin-password")
	if wrong.StatusCode != http.StatusForbidden || importAPICode(t, wrong) != "csrf_required" {
		t.Fatalf("csrf status=%d code=%s", wrong.StatusCode, importAPICode(t, wrong))
	}
	saved := importSessionRequest(t, http.MethodPut, endpoint, map[string]any{
		"url": "https://example.invalid/team/project.git", "mode": "standalone",
	}, "import-csrf", "admin-password")
	if saved.StatusCode != http.StatusOK || strings.Contains(importAPIBody(t, saved), "admin-password") {
		t.Fatalf("owner configure status=%d", saved.StatusCode)
	}
}

func TestImportAPICredentialResponseHasNoSecret(t *testing.T) {
	fixture := newImportAPIFixture(t)
	server := httptest.NewServer(fixture.app.Handler())
	t.Cleanup(server.Close)
	base := server.URL + "/api/v1/repositories/project/import"
	configured := importAPIRequest(t, http.MethodPut, base, map[string]any{
		"url": "https://example.invalid/team/project.git", "mode": "coexistence", "git_only_consent": true,
	}, "admin-password", "", "")
	if configured.StatusCode != http.StatusOK {
		t.Fatalf("configure status=%d body=%s", configured.StatusCode, importAPIBody(t, configured))
	}
	const token = "import-secret-token"
	const password = "import-secret-password"
	saved := importAPIRequest(t, http.MethodPut, base+"/credentials", map[string]any{
		"form": "bearer", "token": token, "ca_pem": "not-a-secret-ca",
	}, "admin-password", "", "")
	body := importAPIBody(t, saved)
	if saved.StatusCode != http.StatusOK || !strings.Contains(body, `"credential_form":"bearer"`) || !strings.Contains(body, `"credential_bound":true`) {
		t.Fatalf("credential response status=%d body=%s", saved.StatusCode, body)
	}
	if strings.Contains(body, token) || strings.Contains(body, password) || strings.Contains(body, "not-a-secret-ca") {
		t.Fatalf("credential response echoed a secret: %s", body)
	}
	status := importAPIRequest(t, http.MethodGet, base, nil, "admin-password", "", "")
	statusBody := importAPIBody(t, status)
	if strings.Contains(statusBody, token) || strings.Contains(statusBody, "not-a-secret-ca") {
		t.Fatalf("status echoed a secret: %s", statusBody)
	}
}

func TestImportAPIMapsNotConfiguredAndImportsNewRepository(t *testing.T) {
	fixture := newImportAPIFixture(t)
	server := httptest.NewServer(fixture.app.Handler())
	t.Cleanup(server.Close)
	base := server.URL + "/api/v1/repositories/project/import"
	refresh := importAPIRequest(t, http.MethodPost, base+"/run", map[string]any{}, "admin-password", "", "")
	if refresh.StatusCode != http.StatusNotFound || importAPICode(t, refresh) != importsync.CodeNotConfigured {
		t.Fatalf("refresh without source status=%d code=%s", refresh.StatusCode, importAPICode(t, refresh))
	}
	created := importAPIRequest(t, http.MethodPost, server.URL+"/api/v1/repositories/fresh/import/run", map[string]any{
		"name": "fresh", "url": "https://example.invalid/team/fresh.git", "mode": "standalone",
	}, "admin-password", "", "")
	if created.StatusCode != http.StatusOK || !strings.Contains(importAPIBody(t, created), `"status":"complete"`) {
		t.Fatalf("initial import status=%d body=%s", created.StatusCode, importAPIBody(t, created))
	}
	history := importAPIRequest(t, http.MethodGet, server.URL+"/api/v1/repositories/fresh/import/history?limit=1", nil, "admin-password", "", "")
	if history.StatusCode != http.StatusOK || !strings.Contains(importAPIBody(t, history), `"more"`) {
		t.Fatalf("history status=%d body=%s", history.StatusCode, importAPIBody(t, history))
	}
	rejected := importAPIRequest(t, http.MethodGet, server.URL+"/api/v1/repositories/fresh/import?cursor=1", nil, "admin-password", "", "")
	if rejected.StatusCode != http.StatusBadRequest {
		t.Fatalf("query on status status=%d", rejected.StatusCode)
	}
}

func TestImportRunRouteOutlivesOrdinaryDeadline(t *testing.T) {
	fixture := newImportAPIFixture(t)
	fixture.app.HTTPTimeout = 500 * time.Millisecond
	fixture.app.Imports.Fetch = func(ctx context.Context, _ importfetch.Request, _ importfetch.PackConsumer) (*importfetch.Result, error) {
		select {
		case <-time.After(2 * time.Second):
			return &importfetch.Result{Advertisement: &importgit.Advertisement{Service: "git-upload-pack", ObjectFormat: importgit.FormatSHA1, Empty: true}}, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	server := httptest.NewServer(fixture.app.Handler())
	t.Cleanup(server.Close)
	created := importAPIRequest(t, http.MethodPost, server.URL+"/api/v1/repositories/slow/import/run", map[string]any{
		"name": "slow", "url": "https://example.invalid/team/slow.git", "mode": "standalone",
	}, "admin-password", "", "")
	body := importAPIBody(t, created)
	if created.StatusCode != http.StatusOK || !strings.Contains(body, `"status":"complete"`) || strings.Contains(body, `"code":"cancelled"`) {
		t.Fatalf("slow import did not outlive the ordinary deadline: status=%d body=%s", created.StatusCode, body)
	}

	finished := make(chan struct{})
	fixture.app.requestObserver = func(request *http.Request) {
		if request.URL.Path == "/setup" {
			<-request.Context().Done()
			close(finished)
		}
	}
	started := time.Now()
	go func() {
		response, err := http.Get(server.URL + "/setup")
		if err == nil {
			response.Body.Close()
		}
	}()
	select {
	case <-finished:
		if elapsed := time.Since(started); elapsed > 2*time.Second {
			t.Fatalf("ordinary route kept the import deadline: %s", elapsed)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("ordinary route did not time out at the short deadline")
	}
}

func TestInitialImportSendsCredentialsWithoutEchoingThem(t *testing.T) {
	fixture := newImportAPIFixture(t)
	const token = "initial-secret-token"
	const caPEM = "initial-secret-ca"
	var gotToken string
	var gotCA string
	fixture.app.Imports.Fetch = func(_ context.Context, request importfetch.Request, _ importfetch.PackConsumer) (*importfetch.Result, error) {
		gotToken = request.Authentication.BearerToken
		gotCA = string(request.RootCAPEM)
		return &importfetch.Result{Advertisement: &importgit.Advertisement{Service: "git-upload-pack", ObjectFormat: importgit.FormatSHA1, Empty: true}}, nil
	}
	server := httptest.NewServer(fixture.app.Handler())
	t.Cleanup(server.Close)
	created := importAPIRequest(t, http.MethodPost, server.URL+"/api/v1/repositories/private/import/run", map[string]any{
		"name": "private", "url": "https://example.invalid/team/private.git", "mode": "standalone",
		"credential_form": "bearer", "token": token, "ca_pem": caPEM,
	}, "admin-password", "", "")
	body := importAPIBody(t, created)
	if created.StatusCode != http.StatusOK || gotToken != token || gotCA != caPEM {
		t.Fatalf("initial credential was not sent: status=%d token=%q ca=%q body=%s", created.StatusCode, gotToken, gotCA, body)
	}
	if strings.Contains(body, token) || strings.Contains(body, caPEM) {
		t.Fatalf("initial import echoed a secret: %s", body)
	}
	refused := importAPIRequest(t, http.MethodPost, server.URL+"/api/v1/repositories/project/import/run", map[string]any{
		"credential_form": "bearer", "token": token,
	}, "admin-password", "", "")
	if refused.StatusCode != http.StatusUnprocessableEntity || importAPICode(t, refused) != importsync.CodeInvalidSource {
		t.Fatalf("refresh accepted credentials: status=%d code=%s", refused.StatusCode, importAPICode(t, refused))
	}
}

func TestReservedRepositoryNamesStayOnForms(t *testing.T) {
	fixture := newImportAPIFixture(t)
	ctx := context.Background()
	for _, name := range []string{"new", "new-import", "New-Import"} {
		_, err := fixture.app.Repositories.Create(ctx, name, "")
		if !errors.Is(err, repository.ErrInvalidName) {
			t.Fatalf("create %s error=%v", name, err)
		}
		_, err = fixture.app.Imports.Import(ctx, importsync.ImportInput{Name: name, URL: "https://example.invalid/team/reserved.git"})
		if !errors.Is(err, repository.ErrInvalidName) {
			t.Fatalf("import %s error=%v", name, err)
		}
	}
	server := httptest.NewServer(fixture.app.Handler())
	t.Cleanup(server.Close)
	client, jar := newBrowserClient(t)
	_ = browserAdminSessionFor(t, fixture, server.URL, jar, "reserved-admin")
	form := browserGET(t, client, server.URL+"/repositories/new-import")
	if form.status != http.StatusOK || !strings.Contains(form.body, `name="url"`) {
		t.Fatalf("new-import form status=%d", form.status)
	}
}

func newImportAPIFixture(t *testing.T) apiFixture {
	t.Helper()
	fixture := newAPIFixture(t, false)
	fixture.app.Imports = &importsync.Service{
		Store: fixture.store, Repositories: fixture.app.Repositories,
		Fetch: func(context.Context, importfetch.Request, importfetch.PackConsumer) (*importfetch.Result, error) {
			return &importfetch.Result{Advertisement: &importgit.Advertisement{Service: "git-upload-pack", ObjectFormat: importgit.FormatSHA1, Empty: true}}, nil
		},
	}
	// The runtime lease keeps its root marker open. Windows cannot remove
	// the temporary directory until the service releases it.
	service := fixture.app.Imports
	t.Cleanup(func() { _ = service.Close() })
	return fixture
}

func importAPIRequest(t *testing.T, method, target string, value any, password, csrf, adminHeader string) *http.Response {
	t.Helper()
	var body io.Reader = bytes.NewReader(nil)
	if value != nil {
		encoded, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		body = bytes.NewReader(encoded)
	}
	request, err := http.NewRequest(method, target, body)
	if err != nil {
		t.Fatal(err)
	}
	if value != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if password != "" {
		request.SetBasicAuth("admin", password)
	}
	if csrf != "" {
		request.Header.Set(csrfHeader, csrf)
	}
	if adminHeader != "" {
		request.Header.Set(adminPasswordHeader, adminHeader)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	return response
}

func importSessionRequest(t *testing.T, method, target string, value any, csrf, password string) *http.Response {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	request, err := http.NewRequest(method, target, bytes.NewReader(encoded))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.AddCookie(&http.Cookie{Name: adminCookie, Value: "import-admin"})
	request.Header.Set(csrfHeader, csrf)
	request.Header.Set(adminPasswordHeader, password)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	return response
}

func importAPICode(t *testing.T, response *http.Response) string {
	t.Helper()
	defer response.Body.Close()
	var envelope struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.NewDecoder(response.Body).Decode(&envelope); err != nil {
		t.Fatal(err)
	}
	return envelope.Error.Code
}

func importAPIBody(t *testing.T, response *http.Response) string {
	t.Helper()
	defer response.Body.Close()
	content, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	return string(content)
}
