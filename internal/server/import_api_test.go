package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"owngit/internal/importfetch"
	"owngit/internal/importgit"
	"owngit/internal/importsync"
	"owngit/internal/repository"
	"owngit/internal/state"
)

func TestImportAPIRequiresOwnerAndRejectsCSRF(t *testing.T) {
	fixture := newImportAPIFixture(t)
	server := serve(t, fixture.app.Handler())
	endpoint := server.URL + "/api/v1/repositories/project/import"
	unauthenticated := importAPIRequest(t, http.MethodPut, endpoint, map[string]any{"url": "https://example.invalid/team/project.git"}, "", "", "")
	if unauthenticated.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthenticated status=%d", unauthenticated.StatusCode)
	}
	settings, err := fixture.store.Settings(context.Background())
	noErr(t, err)
	noErr(t, fixture.store.CreateSession(context.Background(), "import-admin", "admin", "import-csrf", settings.AdminSessionVersion, time.Now().Add(time.Hour)))
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
	server := serve(t, fixture.app.Handler())
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
	server := serve(t, fixture.app.Handler())
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

// An add request for a repository that already exists is refused. It must not
// silently refresh the stored source instead of importing the given one.
func TestImportAddOnAnExistingRepositoryIsRefused(t *testing.T) {
	fixture := newImportAPIFixture(t)
	server := serve(t, fixture.app.Handler())
	base := server.URL + "/api/v1/repositories/fresh/import"
	created := importAPIRequest(t, http.MethodPost, base+"/run", map[string]any{
		"name": "fresh", "url": "https://example.invalid/team/fresh.git", "mode": "coexistence",
	}, "admin-password", "", "")
	if created.StatusCode != http.StatusOK {
		t.Fatalf("initial import status=%d body=%s", created.StatusCode, importAPIBody(t, created))
	}
	for _, body := range []map[string]any{
		{"name": "fresh", "url": "https://example.invalid/team/other.git", "mode": "standalone"},
		{"url": "https://example.invalid/team/fresh.git"},
	} {
		again := importAPIRequest(t, http.MethodPost, base+"/run", body, "admin-password", "", "")
		if again.StatusCode != http.StatusConflict || importAPICode(t, again) != importsync.CodeRepositoryTaken {
			t.Fatalf("add on an existing repository status=%d", again.StatusCode)
		}
	}
	runs, _, err := fixture.app.Imports.History(context.Background(), "fresh", 10)
	if err != nil || len(runs) != 1 {
		t.Fatalf("a refused add started a run: runs=%d err=%v", len(runs), err)
	}
	status, err := fixture.app.Imports.Status(context.Background(), "fresh")
	if err != nil || status.URL != "https://example.invalid/team/fresh.git" || status.Mode != "coexistence" {
		t.Fatalf("a refused add changed the source: %+v err=%v", status, err)
	}
	// A plain refresh still runs.
	refreshed := importAPIRequest(t, http.MethodPost, base+"/run", map[string]any{}, "admin-password", "", "")
	if refreshed.StatusCode != http.StatusOK {
		t.Fatalf("refresh status=%d body=%s", refreshed.StatusCode, importAPIBody(t, refreshed))
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
	server := serve(t, fixture.app.Handler())
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
	server := serve(t, fixture.app.Handler())
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
		if !errors.Is(err, repository.ErrReservedName) || !errors.Is(err, repository.ErrInvalidName) {
			t.Fatalf("create %s error=%v", name, err)
		}
		_, err = fixture.app.Imports.Import(ctx, importsync.ImportInput{Name: name, URL: "https://example.invalid/team/reserved.git"})
		if !errors.Is(err, repository.ErrReservedName) || !errors.Is(err, repository.ErrInvalidName) {
			t.Fatalf("import %s error=%v", name, err)
		}
	}
	if _, err := fixture.app.Repositories.Create(ctx, "bad name", ""); !errors.Is(err, repository.ErrInvalidName) || errors.Is(err, repository.ErrReservedName) {
		t.Fatalf("invalid name reported as reserved: %v", err)
	}
	server := serve(t, fixture.app.Handler())
	client, jar := newBrowserClient(t)
	csrf := browserAdminSessionFor(t, fixture, server.URL, jar, "reserved-admin")
	form := browserGET(t, client, server.URL+"/repositories/new-import")
	if form.status != http.StatusOK || !strings.Contains(form.body, `name="url"`) {
		t.Fatalf("new-import form status=%d", form.status)
	}
	const reserved = "The names new and new-import are reserved."
	created := browserForm(t, client, server.URL+"/repositories", url.Values{"csrf": {csrf}, "name": {"new"}}, server.URL)
	if created.status != http.StatusUnprocessableEntity || !strings.Contains(created.body, reserved) {
		t.Fatalf("create form did not explain the reserved name: status=%d", created.status)
	}
	imported := browserForm(t, client, server.URL+"/repositories/new-import", url.Values{
		"csrf": {csrf}, "name": {"new-import"}, "url": {"https://example.invalid/team/reserved.git"},
		"mode": {"standalone"}, "credential_form": {"none"}, "admin_password": {"admin-password"},
	}, server.URL)
	if imported.status != http.StatusUnprocessableEntity || !strings.Contains(imported.body, reserved) {
		t.Fatalf("import form did not explain the reserved name: status=%d", imported.status)
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
	return sendJSON(t, method, target, value, basicAuth("admin", password), header(csrfHeader, csrf), header(adminPasswordHeader, adminHeader))
}

// importSessionRequest always has a body, CSRF token and password.
func importSessionRequest(t *testing.T, method, target string, value any, csrf, password string) *http.Response {
	t.Helper()
	return sendJSON(t, method, target, value, adminCookieValue("import-admin"), header(csrfHeader, csrf), header(adminPasswordHeader, password))
}
func importAPICode(t *testing.T, response *http.Response) string {
	t.Helper()
	defer response.Body.Close()
	var envelope struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	noErr(t, json.NewDecoder(response.Body).Decode(&envelope))
	return envelope.Error.Code
}

func importAPIBody(t *testing.T, response *http.Response) string {
	t.Helper()
	defer response.Body.Close()
	content, err := io.ReadAll(response.Body)
	noErr(t, err)
	return string(content)
}

// Clearing credentials works for a name whose first import never created the
// repository, and a name with nothing stored is still not found.
func TestImportCredentialsClearWorksWithoutARepository(t *testing.T) {
	fixture := newImportAPIFixture(t)
	server := serve(t, fixture.app.Handler())
	ctx := context.Background()
	source, err := fixture.store.ConfigureImportSource(ctx, state.ImportSourceInput{
		RepositoryID: "orphan", URL: "https://example.invalid/team/orphan.git", Mode: "standalone", Now: time.Now(),
	})
	noErr(t, err)
	_, err = fixture.store.SaveImportCredentials(ctx, state.ImportCredentials{
		RepositoryID: "orphan", URL: source.URL, SourceGeneration: source.SourceGeneration,
		ExpectedAuthorityRevision: source.AuthorityRevision, BearerToken: "orphan-token",
	}, time.Now())
	noErr(t, err)

	cleared := importAPIRequest(t, http.MethodDelete, server.URL+"/api/v1/repositories/orphan/import/credentials", nil, "admin-password", "", "")
	if body := importAPIBody(t, cleared); cleared.StatusCode != http.StatusOK || !strings.Contains(body, `"credential_form":"none"`) {
		t.Fatalf("clear without a repository status=%d body=%s", cleared.StatusCode, body)
	}
	if _, exists, err := fixture.store.LoadImportCredentials(ctx, "orphan"); err != nil || exists {
		t.Fatalf("the credential stayed exists=%v err=%v", exists, err)
	}
	if _, exists, err := fixture.store.ImportSource(ctx, "orphan"); err != nil || exists {
		t.Fatalf("the source stayed exists=%v err=%v", exists, err)
	}
	again := importAPIRequest(t, http.MethodDelete, server.URL+"/api/v1/repositories/orphan/import/credentials", nil, "admin-password", "", "")
	if again.StatusCode != http.StatusNotFound || importAPICode(t, again) != "repository_not_found" {
		t.Fatalf("clear with nothing stored status=%d", again.StatusCode)
	}
}
