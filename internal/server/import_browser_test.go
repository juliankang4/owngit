package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"owngit/internal/importfetch"
	"owngit/internal/importgit"
	"owngit/internal/importsync"
	"owngit/internal/webui"
)

func TestImportBrowserFormsWorkInBothLanguages(t *testing.T) {
	fixture := newImportAPIFixture(t)
	server := httptest.NewServer(fixture.app.Handler())
	t.Cleanup(server.Close)
	client, jar := newBrowserClient(t)
	csrf := browserAdminSessionFor(t, fixture, server.URL, jar, "import-browser")
	english := browserGET(t, client, server.URL+"/repositories/project/import")
	if english.status != http.StatusOK || !strings.Contains(english.body, webui.Text(webui.LangEN, webui.MsgImportTitle)) || !strings.Contains(english.body, `name="csrf" value="`+csrf+`"`) {
		t.Fatalf("english import page status=%d body=%s", english.status, english.body)
	}
	if !strings.Contains(english.body, `name="url"`) || !strings.Contains(english.body, `name="admin_password"`) || !strings.Contains(english.body, `method="post"`) {
		t.Fatal("import form is missing a no-script field")
	}
	korean := browserGET(t, client, server.URL+"/repositories/project/import?lang=ko")
	if korean.status != http.StatusOK || !strings.Contains(korean.body, webui.Text(webui.LangKO, webui.MsgImportTitle)) {
		t.Fatalf("korean import page status=%d", korean.status)
	}
	reloaded := browserGET(t, client, server.URL+"/repositories/project/import")
	if reloaded.status != http.StatusOK || !strings.Contains(reloaded.body, webui.Text(webui.LangKO, webui.MsgImportIntro)) {
		t.Fatal("language cookie did not survive reload")
	}
	if !strings.Contains(reloaded.body, "lang=en") {
		t.Fatal("language switch back to English is missing")
	}
	wrong := browserForm(t, client, server.URL+"/repositories/project/import", url.Values{
		"csrf": {"wrong"}, "action": {webui.ActionImportConfigure}, "url": {"https://example.invalid/team/project.git"},
		"mode": {"standalone"}, "admin_password": {"admin-password"},
	}, server.URL)
	if wrong.status != http.StatusForbidden {
		t.Fatalf("form csrf status=%d", wrong.status)
	}
	saved := browserForm(t, client, server.URL+"/repositories/project/import", url.Values{
		"csrf": {csrf}, "action": {webui.ActionImportConfigure}, "url": {"https://example.invalid/team/project.git"},
		"mode": {"standalone"}, "admin_password": {"admin-password"},
	}, server.URL)
	if saved.status != http.StatusSeeOther {
		t.Fatalf("configure form status=%d body=%s", saved.status, saved.body)
	}
	listed := browserGET(t, client, server.URL+"/repositories/new-import")
	if listed.status != http.StatusOK || !strings.Contains(listed.body, `name="name"`) || !strings.Contains(listed.body, `name="url"`) {
		t.Fatalf("new import form status=%d", listed.status)
	}
}

func TestNewImportKeepsCAWhenCredentialFormIsNone(t *testing.T) {
	fixture := newImportAPIFixture(t)
	const caPEM = "browser-secret-ca"
	var gotCA string
	fixture.app.Imports.Fetch = func(_ context.Context, request importfetch.Request, _ importfetch.PackConsumer) (*importfetch.Result, error) {
		gotCA = string(request.RootCAPEM)
		return &importfetch.Result{Advertisement: &importgit.Advertisement{Service: "git-upload-pack", ObjectFormat: importgit.FormatSHA1, Empty: true}}, nil
	}
	server := httptest.NewServer(fixture.app.Handler())
	t.Cleanup(server.Close)
	client, jar := newBrowserClient(t)
	csrf := browserAdminSessionFor(t, fixture, server.URL, jar, "ca-admin")
	result := browserForm(t, client, server.URL+"/repositories/new-import", url.Values{
		"csrf": {csrf}, "name": {"caonly"}, "url": {"https://example.invalid/team/caonly.git"},
		"mode": {"standalone"}, "credential_form": {"none"}, "ca_pem": {caPEM}, "admin_password": {"admin-password"},
	}, server.URL)
	if result.status != http.StatusSeeOther || gotCA != caPEM || strings.Contains(result.body, caPEM) {
		t.Fatalf("CA-only import status=%d ca=%q body=%s", result.status, gotCA, result.body)
	}
}

func TestImportPageShowsPasswordAndFailureCauses(t *testing.T) {
	fixture := newImportAPIFixture(t)
	fixture.app.Imports.Fetch = func(context.Context, importfetch.Request, importfetch.PackConsumer) (*importfetch.Result, error) {
		return nil, &importfetch.Error{Op: "connect", Kind: importfetch.ErrConnection}
	}
	server := httptest.NewServer(fixture.app.Handler())
	t.Cleanup(server.Close)
	client, jar := newBrowserClient(t)
	csrf := browserAdminSessionFor(t, fixture, server.URL, jar, "notice-admin")
	if _, err := fixture.app.Imports.ConfigureSource(context.Background(), importsync.ConfigureInput{
		RepositoryID: "project", URL: "https://example.invalid/team/project.git", Mode: importsync.ModeStandalone,
	}); err != nil {
		t.Fatal(err)
	}
	wrong := browserForm(t, client, server.URL+"/repositories/project/import", url.Values{
		"csrf": {csrf}, "action": {webui.ActionImportRefresh}, "admin_password": {"wrong-password"},
	}, server.URL)
	if wrong.status != http.StatusUnauthorized || !strings.Contains(wrong.body, webui.Text(webui.LangEN, webui.MsgAdminFailed)) {
		t.Fatalf("wrong password status=%d body=%s", wrong.status, wrong.body)
	}
	failed := browserForm(t, client, server.URL+"/repositories/project/import", url.Values{
		"csrf": {csrf}, "action": {webui.ActionImportRefresh}, "admin_password": {"admin-password"},
	}, server.URL)
	if failed.status != http.StatusBadGateway || !strings.Contains(failed.body, webui.Text(webui.LangEN, webui.MsgImportFailed)) || !strings.Contains(failed.body, webui.Text(webui.LangEN, webui.MsgImportErrorNetwork)) {
		t.Fatalf("failed refresh status=%d body=%s", failed.status, failed.body)
	}
	// Korean pages explain the recorded class in Korean. The raw internal
	// message stays behind the technical details disclosure.
	korean := browserGET(t, client, server.URL+"/repositories/project/import?lang=ko")
	explanation := webui.Text(webui.LangKO, webui.MsgImportErrorNetwork)
	details := strings.Index(korean.body, "<details>")
	if korean.status != http.StatusOK || !strings.Contains(korean.body, explanation) || details < 0 {
		t.Fatalf("korean last run status=%d body=%s", korean.status, korean.body)
	}
	if raw := strings.Index(korean.body, "import network"); raw >= 0 && raw < details {
		t.Fatalf("raw internal error text appears outside technical details: %s", korean.body)
	}
	if strings.Count(korean.body, ">"+explanation+"<") != 1 {
		t.Fatalf("failure explanation is repeated: %s", korean.body)
	}
}

// A cancelled refresh reports the cancellation, not a finished refresh, and
// a cancelled initial import never redirects to a repository that was not
// created.
func TestCancelledImportsDoNotReportSuccess(t *testing.T) {
	fixture := newImportAPIFixture(t)
	cancelDuringFetch := func(repositoryID string) func(context.Context, importfetch.Request, importfetch.PackConsumer) (*importfetch.Result, error) {
		return func(ctx context.Context, _ importfetch.Request, _ importfetch.PackConsumer) (*importfetch.Result, error) {
			if _, err := fixture.app.Imports.Cancel(context.Background(), repositoryID); err != nil {
				t.Errorf("cancel %s: %v", repositoryID, err)
			}
			<-ctx.Done()
			return nil, ctx.Err()
		}
	}
	server := httptest.NewServer(fixture.app.Handler())
	t.Cleanup(server.Close)
	client, jar := newBrowserClient(t)
	csrf := browserAdminSessionFor(t, fixture, server.URL, jar, "cancel-admin")
	if _, err := fixture.app.Imports.ConfigureSource(context.Background(), importsync.ConfigureInput{
		RepositoryID: "project", URL: "https://example.invalid/team/project.git", Mode: importsync.ModeStandalone,
	}); err != nil {
		t.Fatal(err)
	}

	fixture.app.Imports.Fetch = cancelDuringFetch("project")
	refreshed := browserForm(t, client, server.URL+"/repositories/project/import", url.Values{
		"csrf": {csrf}, "action": {webui.ActionImportRefresh}, "admin_password": {"admin-password"},
	}, server.URL)
	location := refreshed.header.Get("Location")
	if refreshed.status != http.StatusSeeOther || !strings.Contains(location, "notice=import_run_cancelled") {
		t.Fatalf("cancelled refresh status=%d location=%q", refreshed.status, location)
	}
	shown := browserGET(t, client, server.URL+location)
	if !strings.Contains(shown.body, webui.Text(webui.LangEN, webui.MsgImportRunCancelled)) || strings.Contains(shown.body, webui.Text(webui.LangEN, webui.MsgImportRefreshed)) {
		t.Fatalf("cancelled refresh page body=%s", shown.body)
	}

	fixture.app.Imports.Fetch = cancelDuringFetch("fresh")
	created := browserForm(t, client, server.URL+"/repositories/new-import?lang=ko", url.Values{
		"csrf": {csrf}, "name": {"fresh"}, "url": {"https://example.invalid/team/fresh.git"},
		"mode": {"standalone"}, "admin_password": {"admin-password"},
	}, server.URL)
	if created.status == http.StatusSeeOther || !strings.Contains(created.body, webui.Text(webui.LangKO, webui.MsgImportCancelledNoRepo)) || strings.Contains(created.body, webui.Text(webui.LangKO, webui.MsgImportStarted)) {
		t.Fatalf("cancelled initial import status=%d location=%q body=%s", created.status, created.header.Get("Location"), created.body)
	}
	if _, exists, err := fixture.store.Repository(context.Background(), "fresh"); err != nil || exists {
		t.Fatalf("cancelled initial import repository exists=%v err=%v", exists, err)
	}
}
