package server

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"owngit/internal/importfetch"
	"owngit/internal/importgit"
	"owngit/internal/importsync"
	"owngit/internal/webui"
)

// The existing-repository Import page can store or replace a CA-only source
// credential: it offers the None form, preselects the bound form, and never
// fills a secret field. None with a CA is accepted; None with a password is
// refused and changes nothing.
func TestImportPageStoresACAOnlyCredential(t *testing.T) {
	fixture := newImportAPIFixture(t)
	server := serve(t, fixture.app.Handler())
	client, jar := newBrowserClient(t)
	csrf := browserAdminSessionFor(t, fixture, server.URL, jar, "ca-form-admin")
	if _, err := fixture.app.Imports.ConfigureSource(context.Background(), importsync.ConfigureInput{
		RepositoryID: "project", URL: "https://example.invalid/team/project.git", Mode: importsync.ModeStandalone,
	}); err != nil {
		t.Fatal(err)
	}
	const caPEM = "synthetic-ca-only-pem"
	pageURL := server.URL + "/repositories/project/import"
	for _, lang := range []webui.Lang{webui.LangEN, webui.LangKO} {
		page := browserGET(t, client, pageURL+"?lang="+string(lang))
		options := credentialFormOptions(t, page.body)
		if page.status != http.StatusOK || !strings.Contains(options["none"], ">"+webui.Text(lang, webui.MsgImportCredentialNone)+"<") || strings.Contains(options["none"], "selected") {
			t.Fatalf("%s unbound credential form status=%d lacks an unselected None option: %v", lang, page.status, options)
		}
	}
	saved := browserForm(t, client, pageURL, url.Values{
		"csrf": {csrf}, "action": {webui.ActionImportCredentials}, "credential_form": {"none"},
		"ca_pem": {caPEM}, "admin_password": {"admin-password"},
	}, server.URL)
	if saved.status != http.StatusSeeOther {
		t.Fatalf("CA-only credential status=%d body=%s", saved.status, saved.body)
	}
	status, err := fixture.app.Imports.Status(context.Background(), "project")
	if err != nil || !status.CredentialBound || status.CredentialForm != "none" || !status.CAPresent {
		t.Fatalf("CA-only credential status=%+v err=%v", status, err)
	}
	for _, lang := range []webui.Lang{webui.LangEN, webui.LangKO} {
		page := browserGET(t, client, pageURL+"?lang="+string(lang))
		options := credentialFormOptions(t, page.body)
		if !strings.HasPrefix(options["none"], `<option value="none" selected>`) || strings.Contains(options["basic"]+options["bearer"], "selected") {
			t.Fatalf("%s page does not preselect only the bound None form: %v", lang, options)
		}
		if !strings.Contains(page.body, ">"+webui.Text(lang, webui.MsgImportCAPresent)+"<") || strings.Contains(page.body, caPEM) {
			t.Fatalf("%s page lacks the stored CA note or shows the CA", lang)
		}
	}
	refused := browserForm(t, client, pageURL, url.Values{
		"csrf": {csrf}, "action": {webui.ActionImportCredentials}, "credential_form": {"none"},
		"password": {"synthetic-password"}, "ca_pem": {caPEM + "-replacement"}, "admin_password": {"admin-password"},
	}, server.URL)
	if refused.status == http.StatusSeeOther || strings.Contains(refused.body, "synthetic-password") {
		t.Fatalf("None with a password status=%d", refused.status)
	}
	stored, exists, err := fixture.store.LoadImportCredentials(context.Background(), "project")
	if err != nil || !exists || string(stored.RootCAPEM) != caPEM || stored.Basic != nil || stored.BearerToken != "" {
		t.Fatalf("refused form changed the stored credential: exists=%v err=%v", exists, err)
	}
}

// credentialFormOptions returns each option of the existing-repository
// credential form select, keyed by its value.
func credentialFormOptions(t *testing.T, body string) map[string]string {
	t.Helper()
	start := strings.Index(body, `<select id="import-credential-form"`)
	if start < 0 {
		t.Fatal("credential form select is missing")
	}
	end := strings.Index(body[start:], "</select>")
	options := map[string]string{}
	for _, option := range strings.Split(body[start:start+end], "<option ")[1:] {
		value, _, _ := strings.Cut(strings.TrimPrefix(option, `value="`), `"`)
		options[value] = "<option " + strings.TrimSpace(option)
	}
	return options
}

// Save never clears. With a CA-only credential bound and None preselected,
// saving the form unchanged is refused in the reader's language and keeps the
// CA; the API refuses an empty store the same way. Only the explicit clear
// actions remove the credential.
func TestImportCredentialSaveNeverClears(t *testing.T) {
	fixture := newImportAPIFixture(t)
	server := serve(t, fixture.app.Handler())
	client, jar := newBrowserClient(t)
	csrf := browserAdminSessionFor(t, fixture, server.URL, jar, "save-admin")
	if _, err := fixture.app.Imports.ConfigureSource(context.Background(), importsync.ConfigureInput{
		RepositoryID: "project", URL: "https://example.invalid/team/project.git", Mode: importsync.ModeStandalone,
	}); err != nil {
		t.Fatal(err)
	}
	const caPEM = "synthetic-kept-ca-pem"
	pageURL := server.URL + "/repositories/project/import"
	store := func() {
		t.Helper()
		saved := browserForm(t, client, pageURL, url.Values{
			"csrf": {csrf}, "action": {webui.ActionImportCredentials}, "credential_form": {"none"},
			"ca_pem": {caPEM}, "admin_password": {"admin-password"},
		}, server.URL)
		if saved.status != http.StatusSeeOther {
			t.Fatalf("store CA status=%d", saved.status)
		}
	}
	caKept := func(after string) {
		t.Helper()
		stored, exists, err := fixture.store.LoadImportCredentials(context.Background(), "project")
		if err != nil || !exists || string(stored.RootCAPEM) != caPEM {
			t.Fatalf("%s removed the stored CA: exists=%v err=%v", after, exists, err)
		}
	}
	store()
	for _, lang := range []webui.Lang{webui.LangEN, webui.LangKO} {
		browserGET(t, client, pageURL+"?lang="+string(lang))
		// The browser submits the preselected None with every field empty.
		empty := browserForm(t, client, pageURL, url.Values{
			"csrf": {csrf}, "action": {webui.ActionImportCredentials}, "credential_form": {"none"},
			"username": {""}, "password": {""}, "token": {""}, "ca_pem": {""}, "admin_password": {"admin-password"},
		}, server.URL)
		if empty.status != http.StatusUnprocessableEntity || !strings.Contains(empty.body, ">"+webui.Text(lang, webui.MsgImportNothingToSave)+"<") {
			t.Fatalf("%s empty save status=%d lacks the nothing-saved notice", lang, empty.status)
		}
		if strings.Contains(empty.body, webui.Text(lang, webui.MsgImportCredentialsSaved)) {
			t.Fatalf("%s empty save claims the credential was saved", lang)
		}
		caKept(string(lang) + " empty save")
	}
	api := server.URL + "/api/v1/repositories/project/import/credentials"
	for _, body := range []map[string]any{{}, {"form": "none"}, {"form": "none", "ca_pem": ""}} {
		response := importAPIRequest(t, http.MethodPut, api, body, "admin-password", "", "")
		if response.StatusCode != http.StatusUnprocessableEntity {
			t.Fatalf("API empty store %v status=%d", body, response.StatusCode)
		}
		caKept("API empty store")
	}
	cleared := browserForm(t, client, pageURL, url.Values{
		"csrf": {csrf}, "action": {webui.ActionImportClearCredentials}, "admin_password": {"admin-password"},
	}, server.URL)
	if cleared.status != http.StatusSeeOther {
		t.Fatalf("clear credentials status=%d", cleared.status)
	}
	if _, exists, err := fixture.store.LoadImportCredentials(context.Background(), "project"); err != nil || exists {
		t.Fatalf("Clear credentials kept the credential: exists=%v err=%v", exists, err)
	}
	store()
	if response := importAPIRequest(t, http.MethodDelete, api, nil, "admin-password", "", ""); response.StatusCode != http.StatusOK {
		t.Fatalf("API clear status=%d", response.StatusCode)
	}
	if _, exists, err := fixture.store.LoadImportCredentials(context.Background(), "project"); err != nil || exists {
		t.Fatalf("API DELETE kept the credential: exists=%v err=%v", exists, err)
	}
}

// The new-import form keeps treating empty credential fields as "no
// credential": the import starts without one.
func TestNewImportWithEmptyNoneHasNoCredential(t *testing.T) {
	fixture := newImportAPIFixture(t)
	var request importfetch.Request
	fixture.app.Imports.Fetch = func(_ context.Context, got importfetch.Request, _ importfetch.PackConsumer) (*importfetch.Result, error) {
		request = got
		return &importfetch.Result{Advertisement: &importgit.Advertisement{Service: "git-upload-pack", ObjectFormat: importgit.FormatSHA1, Empty: true}}, nil
	}
	server := serve(t, fixture.app.Handler())
	client, jar := newBrowserClient(t)
	csrf := browserAdminSessionFor(t, fixture, server.URL, jar, "empty-new-admin")
	result := browserForm(t, client, server.URL+"/repositories/new-import", url.Values{
		"csrf": {csrf}, "name": {"plain"}, "url": {"https://example.invalid/team/plain.git"}, "mode": {"standalone"},
		"credential_form": {"none"}, "username": {""}, "password": {""}, "token": {""}, "ca_pem": {""}, "admin_password": {"admin-password"},
	}, server.URL)
	if result.status != http.StatusSeeOther || len(request.RootCAPEM) != 0 || request.Authentication != (importfetch.Authentication{}) {
		t.Fatalf("empty new import status=%d ca=%d authenticated=%v", result.status, len(request.RootCAPEM), request.Authentication != (importfetch.Authentication{}))
	}
}
