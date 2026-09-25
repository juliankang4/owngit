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

func TestImportBrowserFormsWorkInBothLanguages(t *testing.T) {
	fixture := newImportAPIFixture(t)
	server := serve(t, fixture.app.Handler())
	client, jar := newBrowserClient(t)
	csrf := browserAdminSessionFor(t, fixture, server.URL, jar, "import-browser")
	// The source form is behind the explicit Set up import action.
	english := browserGET(t, client, server.URL+"/repositories/project/import?setup=1")
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
	server := serve(t, fixture.app.Handler())
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
	server := serve(t, fixture.app.Handler())
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
	server := serve(t, fixture.app.Handler())
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

// The Import tab shows its status to anyone who can read the repository,
// without the administrator password. The source address, credential state
// and run messages stay hidden, every change is a link with the lock mark to
// the password prompt, and a change posted without an administrator session
// goes to that prompt too.
func TestImportTabShowsStatusWithoutTheAdministratorPassword(t *testing.T) {
	fixture := newImportAPIFixture(t)
	const source = "https://git.example.invalid/private-team/project.git"
	if _, err := fixture.app.Imports.ConfigureSource(context.Background(), importsync.ConfigureInput{
		RepositoryID: "project", URL: source, Mode: importsync.ModeCoexistence,
	}); err != nil {
		t.Fatal(err)
	}
	server := serve(t, fixture.app.Handler())
	client, _ := newBrowserClient(t)
	for _, lang := range []webui.Lang{webui.LangEN, webui.LangKO} {
		page := browserGET(t, client, server.URL+"/repositories/project/import?lang="+string(lang))
		if page.status != http.StatusOK {
			t.Fatalf("%s import tab without the administrator status=%d location=%q", lang, page.status, page.header.Get("Location"))
		}
		for _, want := range []webui.MessageCode{webui.MsgImportModeCoexistence, webui.MsgImportAdminOnly, webui.MsgImportRefresh, webui.MsgImportChangeSettings, webui.MsgImportLastRun} {
			if !strings.Contains(page.body, webui.Text(lang, want)) {
				t.Errorf("%s import tab lacks %q", lang, webui.Text(lang, want))
			}
		}
		if strings.Contains(page.body, "git.example.invalid") || strings.Contains(page.body, `name="admin_password"`) || strings.Contains(page.body, `method="post" action="/repositories/project/import"`) {
			t.Errorf("%s import tab shows administrator data or a change form without the administrator", lang)
		}
		login := `href="/admin/login?next=` + url.QueryEscape("/repositories/project/import")
		if !strings.Contains(page.body, login) || !strings.Contains(page.body, `class="adminlock"`) {
			t.Errorf("%s import tab changes are not locked links to the password prompt", lang)
		}
		if !strings.Contains(page.body, `class="sb__item" href="/repositories/project/import" aria-current="page"`) {
			t.Errorf("%s import tab does not keep the repository sidebar", lang)
		}
	}
	posted := browserForm(t, client, server.URL+"/repositories/project/import", url.Values{
		"action": {webui.ActionImportRefresh}, "admin_password": {"admin-password"},
	}, server.URL)
	if posted.status != http.StatusSeeOther || !strings.HasPrefix(posted.header.Get("Location"), "/admin/login?next=") {
		t.Fatalf("a change without an administrator session status=%d location=%q", posted.status, posted.header.Get("Location"))
	}
}

// An unconfigured repository offers Set up import instead of an open source
// form. The form opens on that action, for an administrator only.
func TestImportFormIsBehindSetUpImport(t *testing.T) {
	fixture := newImportAPIFixture(t)
	server := serve(t, fixture.app.Handler())
	viewer, _ := newBrowserClient(t)
	page := browserGET(t, viewer, server.URL+"/repositories/project/import")
	setUp := `href="/admin/login?next=` + url.QueryEscape("/repositories/project/import?setup=1") + `"`
	if page.status != http.StatusOK || !strings.Contains(page.body, setUp) || strings.Contains(page.body, `name="url"`) {
		t.Fatalf("viewer import tab status=%d does not offer a locked Set up import", page.status)
	}
	admin, jar := newBrowserClient(t)
	browserAdminSessionFor(t, fixture, server.URL, jar, "setup-admin")
	page = browserGET(t, admin, server.URL+"/repositories/project/import")
	if !strings.Contains(page.body, `href="/repositories/project/import?setup=1"`) || strings.Contains(page.body, `name="url"`) {
		t.Fatal("administrator import tab opens the source form before Set up import")
	}
	page = browserGET(t, admin, server.URL+"/repositories/project/import?setup=1")
	if !strings.Contains(page.body, `name="url"`) || !strings.Contains(page.body, `value="import_configure"`) {
		t.Fatal("Set up import does not open the source form")
	}
}

// A refused import names the rule on the field it is about, in both
// languages, and marks that field invalid.
func TestImportFormsReportTheRuleOnTheField(t *testing.T) {
	fixture := newImportAPIFixture(t)
	server := serve(t, fixture.app.Handler())
	client, jar := newBrowserClient(t)
	csrf := browserAdminSessionFor(t, fixture, server.URL, jar, "field-admin")
	for _, test := range []struct {
		name   string
		target string
		values url.Values
		field  string
		code   webui.MessageCode
	}{
		{"new import over http", "/repositories/new-import", url.Values{"url": {"http://example.invalid/team/p.git"}, "credential_form": {"none"}}, "url", webui.MsgImportURLHTTPS},
		{"new import with user info", "/repositories/new-import", url.Values{"url": {"https://user:pw@example.invalid/p.git"}, "credential_form": {"none"}}, "url", webui.MsgImportURLUser},
		{"new import with a bad name", "/repositories/new-import", url.Values{"name": {"bad name"}, "url": {"https://example.invalid/p.git"}, "credential_form": {"none"}}, "name", webui.MsgRepoNameInvalid},
		{"new import token missing", "/repositories/new-import", url.Values{"url": {"https://example.invalid/p.git"}, "credential_form": {"bearer"}}, "token", webui.MsgImportTokenRequired},
		{"new import password missing", "/repositories/new-import", url.Values{"url": {"https://example.invalid/p.git"}, "credential_form": {"basic"}, "username": {"someone"}}, "password", webui.MsgImportBasicNeedsBoth},
		{"new import with a long description", "/repositories/new-import", url.Values{"description": {strings.Repeat("a", 501)}, "url": {"https://example.invalid/p.git"}, "credential_form": {"none"}}, "description", webui.MsgRepoDescriptionTooLong},
		{"new repository with a long description", "/repositories", url.Values{"name": {"long-description"}, "description": {strings.Repeat("설", 167)}}, "description", webui.MsgRepoDescriptionTooLong},
		{"source over http", "/repositories/project/import", url.Values{"action": {webui.ActionImportConfigure}, "url": {"http://example.invalid/team/p.git"}, "mode": {"standalone"}}, "url", webui.MsgImportURLHTTPS},
		{"source with a query", "/repositories/project/import", url.Values{"action": {webui.ActionImportConfigure}, "url": {"https://example.invalid/p.git?x=1"}, "mode": {"standalone"}}, "url", webui.MsgImportURLQuery},
	} {
		for _, lang := range []webui.Lang{webui.LangEN, webui.LangKO} {
			values := url.Values{"csrf": {csrf}, "admin_password": {"admin-password"}, "lang": {string(lang)}}
			for key, value := range test.values {
				values[key] = value
			}
			result := browserForm(t, client, server.URL+test.target+"?lang="+string(lang), values, server.URL)
			if result.status != http.StatusUnprocessableEntity {
				t.Errorf("%s (%s) status=%d", test.name, lang, result.status)
				continue
			}
			note := `<p class="fieldnote fieldnote--error" id="` + test.field + `-note">`
			at := strings.Index(result.body, note)
			if at < 0 || !strings.Contains(result.body[at:], webui.Text(lang, test.code)) {
				t.Errorf("%s (%s) does not name the rule on the %s field", test.name, lang, test.field)
			}
			if !strings.Contains(result.body, `aria-invalid="true" aria-describedby="`+test.field+`-note"`) {
				t.Errorf("%s (%s) does not mark the %s field invalid", test.name, lang, test.field)
			}
		}
	}
	if status, err := fixture.app.Imports.Status(context.Background(), "project"); err != nil || status.Configured {
		t.Fatalf("a refused source change configured the import: %+v %v", status, err)
	}
}

// A run's technical message can name the source host, so a viewer without
// an administrator session never receives it, while the explained failure
// class stays visible to everyone.
func TestImportTabKeepsRunMessagesFromViewers(t *testing.T) {
	fixture := newImportAPIFixture(t)
	fixture.app.Imports.Fetch = func(context.Context, importfetch.Request, importfetch.PackConsumer) (*importfetch.Result, error) {
		return nil, &importfetch.Error{Op: "connect", Kind: importfetch.ErrConnection}
	}
	if _, err := fixture.app.Imports.ConfigureSource(context.Background(), importsync.ConfigureInput{
		RepositoryID: "project", URL: "https://private-host.example.invalid/team/project.git", Mode: importsync.ModeStandalone,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.app.Imports.Refresh(context.Background(), "project", fixture.app.importRunLimits()); err == nil {
		t.Fatal("the refresh did not fail")
	}
	server := serve(t, fixture.app.Handler())
	admin, jar := newBrowserClient(t)
	browserAdminSessionFor(t, fixture, server.URL, jar, "message-admin")
	adminPage := browserGET(t, admin, server.URL+"/repositories/project/import")
	if !strings.Contains(adminPage.body, webui.Text(webui.LangEN, webui.MsgImportTechnicalDetails)) {
		t.Fatal("the administrator does not see the run's technical details, so this test would prove nothing")
	}
	viewer, _ := newBrowserClient(t)
	for _, lang := range []webui.Lang{webui.LangEN, webui.LangKO} {
		page := browserGET(t, viewer, server.URL+"/repositories/project/import?lang="+string(lang))
		if page.status != http.StatusOK || !strings.Contains(page.body, webui.Text(lang, webui.MsgImportErrorNetwork)) {
			t.Fatalf("%s viewer does not see the explained failure: status=%d", lang, page.status)
		}
		if strings.Contains(page.body, "private-host") || strings.Contains(page.body, "<details>") ||
			strings.Contains(page.body, webui.Text(lang, webui.MsgImportTechnicalDetails)) {
			t.Errorf("%s viewer receives the run's technical message or the source host", lang)
		}
	}
}
