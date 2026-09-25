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
// credential: it offers the no-sign-in form, named for what a save with it
// does, explains that a save keeps what is not entered and that Clear
// credentials removes it, offers that clear action, preselects the bound
// form, and never fills a secret field. None with a CA is accepted. A
// password sent with None comes from a field the page hides for that form
// (a browser without scripting still submits it), so it is ignored: only the
// CA changes, and the password is neither stored nor shown.
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
		if page.status != http.StatusOK || !strings.Contains(options["none"], ">"+webui.Text(lang, webui.MsgImportCredentialKeep)+"<") || strings.Contains(options["none"], "selected") {
			t.Fatalf("%s unbound credential form status=%d lacks an unselected None option: %v", lang, page.status, options)
		}
		if !strings.Contains(page.body, webui.Text(lang, webui.MsgImportCredentialSaveHelp)) || !strings.Contains(page.body, `value="`+webui.ActionImportClearCredentials+`"`) {
			t.Fatalf("%s credential form does not explain what a save keeps or offer Clear credentials", lang)
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
	stale := browserForm(t, client, pageURL, url.Values{
		"csrf": {csrf}, "action": {webui.ActionImportCredentials}, "credential_form": {"none"},
		"password": {"synthetic-password"}, "ca_pem": {caPEM + "-replacement"}, "admin_password": {"admin-password"},
	}, server.URL)
	if stale.status != http.StatusSeeOther || strings.Contains(stale.body, "synthetic-password") {
		t.Fatalf("None with a hidden password field status=%d", stale.status)
	}
	stored, exists, err := fixture.store.LoadImportCredentials(context.Background(), "project")
	if err != nil || !exists || string(stored.RootCAPEM) != caPEM+"-replacement" || stored.Basic != nil || stored.BearerToken != "" {
		t.Fatalf("None with a hidden password stored more than the CA: exists=%v err=%v basic=%v", exists, err, stored.Basic != nil)
	}
	if page := browserGET(t, client, pageURL); strings.Contains(page.body, "synthetic-password") {
		t.Fatal("the ignored password is shown on the page")
	}
}

// Without scripting, a field of another credential form is still submitted:
// here a token typed before switching to Basic. The browser forms read only
// the chosen form's fields, so the save succeeds with Basic alone, and the
// token is neither stored nor shown, on the Import tab and on a new import.
func TestBrowserCredentialFormsIgnoreHiddenFields(t *testing.T) {
	fixture := newImportAPIFixture(t)
	var sent importfetch.Request
	fixture.app.Imports.Fetch = func(_ context.Context, request importfetch.Request, _ importfetch.PackConsumer) (*importfetch.Result, error) {
		sent = request
		return &importfetch.Result{Advertisement: &importgit.Advertisement{Service: "git-upload-pack", ObjectFormat: importgit.FormatSHA1, Empty: true}}, nil
	}
	server := serve(t, fixture.app.Handler())
	client, jar := newBrowserClient(t)
	csrf := browserAdminSessionFor(t, fixture, server.URL, jar, "stale-field-admin")
	if _, err := fixture.app.Imports.ConfigureSource(context.Background(), importsync.ConfigureInput{
		RepositoryID: "project", URL: "https://example.invalid/team/project.git", Mode: importsync.ModeStandalone,
	}); err != nil {
		t.Fatal(err)
	}
	const token = "synthetic-stale-token"
	saved := browserForm(t, client, server.URL+"/repositories/project/import", url.Values{
		"csrf": {csrf}, "action": {webui.ActionImportCredentials}, "credential_form": {"basic"},
		"username": {"someone"}, "password": {"synthetic-password"}, "token": {token}, "admin_password": {"admin-password"},
	}, server.URL)
	if saved.status != http.StatusSeeOther {
		t.Fatalf("Basic with a hidden token field status=%d body=%s", saved.status, saved.body)
	}
	stored, exists, err := fixture.store.LoadImportCredentials(context.Background(), "project")
	if err != nil || !exists || stored.Basic == nil || stored.Basic.Username != "someone" || stored.BearerToken != "" {
		t.Fatalf("Basic with a hidden token stored %+v exists=%v err=%v", stored.Basic, exists, err)
	}
	if page := browserGET(t, client, server.URL+"/repositories/project/import"); strings.Contains(page.body, token) || strings.Contains(page.body, "synthetic-password") {
		t.Fatal("a secret is shown on the Import tab")
	}

	created := browserForm(t, client, server.URL+"/repositories/new-import", url.Values{
		"csrf": {csrf}, "name": {"stale"}, "url": {"https://example.invalid/team/stale.git"}, "mode": {"standalone"},
		"credential_form": {"basic"}, "username": {"someone"}, "password": {"synthetic-password"}, "token": {token},
		"admin_password": {"admin-password"},
	}, server.URL)
	if created.status != http.StatusSeeOther || strings.Contains(created.body, token) {
		t.Fatalf("new import with a hidden token field status=%d body=%s", created.status, created.body)
	}
	if sent.Authentication.BearerToken != "" || sent.Authentication.Basic == nil || sent.Authentication.Basic.Username != "someone" {
		t.Fatalf("new import sent a token or no Basic credential")
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

// With a stored Basic credential, a save with "No new sign-in (CA only)", a
// new CA and a stray password (a hidden field a browser without scripting
// still sends) keeps the stored sign-in, replaces only the CA, and neither
// stores nor shows the stray password.
func TestImportTabCAOnlySaveKeepsStoredBasic(t *testing.T) {
	fixture := newImportAPIFixture(t)
	server := serve(t, fixture.app.Handler())
	client, jar := newBrowserClient(t)
	csrf := browserAdminSessionFor(t, fixture, server.URL, jar, "keep-basic-admin")
	ctx := context.Background()
	if _, err := fixture.app.Imports.ConfigureSource(ctx, importsync.ConfigureInput{
		RepositoryID: "project", URL: "https://example.invalid/team/project.git", Mode: importsync.ModeStandalone,
	}); err != nil {
		t.Fatal(err)
	}
	if err := fixture.app.Imports.SetCredentials(ctx, "project", &importsync.Credentials{
		Username: "someone", Password: "synthetic-stored-password", RootCAPEM: []byte("synthetic-old-ca"),
	}); err != nil {
		t.Fatal(err)
	}
	saved := browserForm(t, client, server.URL+"/repositories/project/import", url.Values{
		"csrf": {csrf}, "action": {webui.ActionImportCredentials}, "credential_form": {"none"},
		"password": {"synthetic-stray-password"}, "ca_pem": {"synthetic-new-ca"}, "admin_password": {"admin-password"},
	}, server.URL)
	if saved.status != http.StatusSeeOther {
		t.Fatalf("CA-only save over Basic status=%d", saved.status)
	}
	stored, exists, err := fixture.store.LoadImportCredentials(ctx, "project")
	if err != nil || !exists || stored.Basic == nil || stored.Basic.Username != "someone" ||
		stored.Basic.Password != "synthetic-stored-password" || stored.BearerToken != "" || string(stored.RootCAPEM) != "synthetic-new-ca" {
		t.Fatalf("CA-only save over Basic changed the sign-in or kept the old CA: exists=%v err=%v", exists, err)
	}
	if page := browserGET(t, client, server.URL+"/repositories/project/import"); strings.Contains(page.body, "synthetic-stray-password") || strings.Contains(page.body, "synthetic-stored-password") {
		t.Fatal("a password is shown on the Import tab")
	}
}

// A credential form the page never offers comes only from a crafted request.
// Both browser forms answer it as a bad request with the note on the
// credential form field, and nothing is stored or started.
func TestBrowserCredentialFormsRefuseAnUnknownForm(t *testing.T) {
	fixture := newImportAPIFixture(t)
	fetched := false
	fixture.app.Imports.Fetch = func(context.Context, importfetch.Request, importfetch.PackConsumer) (*importfetch.Result, error) {
		fetched = true
		return nil, &importfetch.Error{Op: "connect", Kind: importfetch.ErrConnection}
	}
	server := serve(t, fixture.app.Handler())
	client, jar := newBrowserClient(t)
	csrf := browserAdminSessionFor(t, fixture, server.URL, jar, "unknown-form-admin")
	if _, err := fixture.app.Imports.ConfigureSource(context.Background(), importsync.ConfigureInput{
		RepositoryID: "project", URL: "https://example.invalid/team/project.git", Mode: importsync.ModeStandalone,
	}); err != nil {
		t.Fatal(err)
	}
	for _, lang := range []webui.Lang{webui.LangEN, webui.LangKO} {
		note := webui.Text(lang, webui.MsgImportCredentialFormUnknown)
		tab := browserForm(t, client, server.URL+"/repositories/project/import?lang="+string(lang), url.Values{
			"csrf": {csrf}, "action": {webui.ActionImportCredentials}, "credential_form": {"weird"},
			"password": {"synthetic-password"}, "admin_password": {"admin-password"},
		}, server.URL)
		if tab.status != http.StatusBadRequest || !strings.Contains(tab.body, note) || strings.Contains(tab.body, "synthetic-password") {
			t.Fatalf("%s Import tab with an unknown form status=%d", lang, tab.status)
		}
		created := browserForm(t, client, server.URL+"/repositories/new-import?lang="+string(lang), url.Values{
			"csrf": {csrf}, "name": {"weird-" + string(lang)}, "url": {"https://example.invalid/team/weird.git"}, "mode": {"standalone"},
			"credential_form": {"weird"}, "admin_password": {"admin-password"},
		}, server.URL)
		if created.status != http.StatusBadRequest || !strings.Contains(created.body, note) {
			t.Fatalf("%s new import with an unknown form status=%d", lang, created.status)
		}
	}
	if _, exists, err := fixture.store.LoadImportCredentials(context.Background(), "project"); err != nil || exists {
		t.Fatalf("an unknown form stored a credential: exists=%v err=%v", exists, err)
	}
	if fetched {
		t.Fatal("an unknown form started an import")
	}
}
