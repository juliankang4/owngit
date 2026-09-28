package server

import (
	"context"
	"encoding/json"
	"html"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"owngit/internal/importsync"
	"owngit/internal/state"
	"owngit/internal/webui"
)

// recordUnresolvedPublication stores a synthetic unresolved refresh intent for
// the fixture repository, as a crash between the ref and HEAD writes leaves.
func recordUnresolvedPublication(t *testing.T, fixture apiFixture) string {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC()
	source, err := fixture.store.ConfigureImportSource(ctx, state.ImportSourceInput{
		RepositoryID: "project", URL: "https://example.invalid/team/project.git", Mode: state.ImportModeStandalone, Now: now,
	})
	noErr(t, err)
	run := state.ImportRun{
		ID: strings.Repeat("1", 32), RepositoryID: "project", SourceGeneration: source.SourceGeneration, AuthorityRevision: source.AuthorityRevision,
		Kind: state.ImportKindRefresh, Status: state.ImportRunPublishing, StartedAt: now, CreatedAt: now,
	}
	noErr(t, fixture.store.BeginImportRun(ctx, run))
	intent := state.ImportIntent{
		ID: strings.Repeat("2", 32), RepositoryID: "project", RunID: run.ID,
		SourceGeneration: source.SourceGeneration, AuthorityRevision: source.AuthorityRevision, Status: state.ImportIntentPlanning,
		Expected: map[string]string{"refs/heads/main": fixture.targetOID, state.ImportHeadRef: "symbolic refs/heads/main " + fixture.targetOID},
		Desired:  map[string]string{"refs/heads/main": fixture.sourceOID, state.ImportHeadRef: "symbolic refs/heads/main " + fixture.sourceOID},
		Observed: map[string]string{"refs/heads/main": fixture.sourceOID, state.ImportHeadRef: "symbolic refs/heads/main " + fixture.sourceOID},
		Retained: map[string]string{}, CreatedAt: now, UpdatedAt: now,
	}
	noErr(t, fixture.store.CreateImportIntent(ctx, intent))
	noErr(t, fixture.store.UpdateImportIntent(ctx, intent.ID, state.ImportIntentUnresolved, "", "", "synthetic mixed outcome", now))
	run.Status = state.ImportRunUnresolved
	run.ErrorClass = importsync.CodeUnresolved
	run.FinishedAt = now
	noErr(t, fixture.store.FinishImportRun(ctx, run))
	return intent.ID
}

// failImportStatusAfter makes every later import status read of repositoryID
// fail once the SQLite trigger event happens. The trigger runs inside that
// write's own transaction, so the write still commits and the failure starts
// exactly there, without depending on timing. What the status read trips over
// is an unreadable schedule row.
func failImportStatusAfter(t *testing.T, store *state.Store, event, repositoryID string) {
	t.Helper()
	noErr(t, store.Exec(context.Background(), `CREATE TRIGGER fail_import_status AFTER `+event+` BEGIN
		INSERT OR IGNORE INTO import_schedules(repository_id,enabled,interval_seconds,last_started_at,created_at,updated_at)
		VALUES ('`+repositoryID+`',0,60,'unreadable',0,0);
		END`))
}

// ownerResolution is the trigger event of a committed owner resolution.
const ownerResolution = `UPDATE OF status ON import_publication_intents WHEN NEW.status='` + state.ImportIntentOwnerResolved + `'`

// decodeImportFields keeps each top-level field of a JSON response as it was
// sent, so a null status is distinguishable from a missing one.
func decodeImportFields(t *testing.T, response *http.Response) map[string]json.RawMessage {
	t.Helper()
	defer response.Body.Close()
	var fields map[string]json.RawMessage
	noErr(t, json.NewDecoder(response.Body).Decode(&fields))
	return fields
}

// statusUnavailable reports whether a successful mutation response says that
// the current status could not be read, with a safe reason, and leaves each
// observed field null instead of guessing it.
func statusUnavailable(t *testing.T, fields map[string]json.RawMessage, observed ...string) bool {
	t.Helper()
	for _, name := range observed {
		if string(fields[name]) != "null" {
			return false
		}
	}
	var reason struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	}
	if json.Unmarshal(fields["status_error"], &reason) != nil {
		return false
	}
	return reason.Code == importsync.CodeStateUnavailable && reason.Message == "import schedule could not be read"
}

func TestImportResolveAPIRequiresOwnerAndRecordsTheDecision(t *testing.T) {
	fixture := newAPIFixture(t, false)
	intentID := recordUnresolvedPublication(t, fixture)
	server := serve(t, fixture.app.Handler())
	target := server.URL + "/api/v1/repositories/project/import/resolve"

	if response := importAPIRequest(t, http.MethodPost, target, map[string]any{}, "", "", ""); response.StatusCode != http.StatusUnauthorized {
		response.Body.Close()
		t.Fatalf("anonymous resolve status=%d", response.StatusCode)
	} else {
		response.Body.Close()
	}
	settings, err := fixture.store.Settings(context.Background())
	noErr(t, err)
	noErr(t, fixture.store.CreateSession(context.Background(), "import-admin", "admin", "import-csrf", settings.AdminSessionVersion, time.Now().Add(time.Hour)))
	if response := importSessionRequest(t, http.MethodPost, target, map[string]any{}, "wrong-csrf", "admin-password"); response.StatusCode != http.StatusForbidden {
		response.Body.Close()
		t.Fatalf("wrong CSRF resolve status=%d", response.StatusCode)
	} else {
		response.Body.Close()
	}
	if intent, _, _ := fixture.store.ImportIntent(context.Background(), intentID); intent.Status != state.ImportIntentUnresolved {
		t.Fatalf("refused request changed the intent: %s", intent.Status)
	}

	response := importAPIRequest(t, http.MethodPost, target, map[string]any{}, "admin-password", "", "")
	body := importAPIBody(t, response)
	if response.StatusCode != http.StatusOK || !strings.Contains(body, intentID) || !strings.Contains(body, `"unresolved_intents":0`) {
		t.Fatalf("resolve status=%d body=%s", response.StatusCode, body)
	}
	intent, _, err := fixture.store.ImportIntent(context.Background(), intentID)
	if err != nil || intent.Status != state.ImportIntentOwnerResolved || !strings.Contains(intent.ReceiptJSON, fixture.targetOID) {
		t.Fatalf("resolved intent=%+v err=%v", intent, err)
	}
	again := importAPIRequest(t, http.MethodPost, target, map[string]any{}, "admin-password", "", "")
	if code := importAPICode(t, again); again.StatusCode != http.StatusConflict || code != importsync.CodeNothingToResolve {
		t.Fatalf("second resolve status=%d code=%s", again.StatusCode, code)
	}
}

// A resolution that committed stays acknowledged when the status read after
// it fails. The response names the resolved intents and says the status is
// unavailable instead of describing an unconfigured import, and nothing in
// Git changes.
func TestImportResolveAPIKeepsTheResolutionWhenStatusIsUnavailable(t *testing.T) {
	fixture := newAPIFixture(t, false)
	intentID := recordUnresolvedPublication(t, fixture)
	refs := func() string {
		return apiGitOutput(t, fixture.remote, "for-each-ref", "--format=%(refname) %(objectname)") + "\n" + apiGitOutput(t, fixture.remote, "symbolic-ref", "HEAD")
	}
	before := refs()
	failImportStatusAfter(t, fixture.store, ownerResolution, "project")
	server := serve(t, fixture.app.Handler())
	target := server.URL + "/api/v1/repositories/project/import/resolve"

	response := importAPIRequest(t, http.MethodPost, target, map[string]any{}, "admin-password", "", "")
	fields := decodeImportFields(t, response)
	var resolved []string
	noErr(t, json.Unmarshal(fields["resolved"], &resolved))
	if response.StatusCode != http.StatusOK || string(fields["ok"]) != "true" || len(resolved) != 1 || resolved[0] != intentID || !statusUnavailable(t, fields, "status") {
		t.Fatalf("resolve status=%d fields=%s", response.StatusCode, fields)
	}
	intent, _, err := fixture.store.ImportIntent(context.Background(), intentID)
	if err != nil || intent.Status != state.ImportIntentOwnerResolved || !strings.Contains(intent.ReceiptJSON, fixture.targetOID) {
		t.Fatalf("resolved intent=%+v err=%v", intent, err)
	}
	if after := refs(); after != before {
		t.Fatalf("resolution changed Git refs\nbefore:\n%s\nafter:\n%s", before, after)
	}
	again := importAPIRequest(t, http.MethodPost, target, map[string]any{}, "admin-password", "", "")
	if code := importAPICode(t, again); again.StatusCode != http.StatusConflict || code != importsync.CodeNothingToResolve {
		t.Fatalf("second resolve status=%d code=%s", again.StatusCode, code)
	}
}

// The browser keeps its success notice for a committed resolution when the
// status read fails afterwards. The page then says that the status could not
// be read, in either language, and offers no second resolution.
func TestImportPageConfirmsResolutionWhenStatusIsUnavailable(t *testing.T) {
	fixture := newAPIFixture(t, false)
	intentID := recordUnresolvedPublication(t, fixture)
	failImportStatusAfter(t, fixture.store, ownerResolution, "project")
	server := serve(t, fixture.app.Handler())
	client, jar := newBrowserClient(t)
	csrf := browserAdminSessionFor(t, fixture, server.URL, jar, "resolve-admin")
	resolved := browserForm(t, client, server.URL+"/repositories/project/import", url.Values{
		"csrf": {csrf}, "action": {webui.ActionImportResolve}, "admin_password": {"admin-password"},
	}, server.URL)
	if resolved.status != http.StatusSeeOther || !strings.Contains(resolved.header.Get("Location"), "notice=import_resolved") {
		t.Fatalf("resolve form status=%d location=%q body=%s", resolved.status, resolved.header.Get("Location"), resolved.body)
	}
	if intent, _, _ := fixture.store.ImportIntent(context.Background(), intentID); intent.Status != state.ImportIntentOwnerResolved {
		t.Fatalf("browser resolution left intent %s", intent.Status)
	}
	page := browserGET(t, client, server.URL+resolved.header.Get("Location"))
	if page.status != http.StatusServiceUnavailable || !strings.Contains(page.body, webui.Text(webui.LangEN, webui.MsgImportResolved)) ||
		strings.Contains(page.body, `value="import_resolve"`) {
		t.Fatalf("page after resolution status=%d body=%s", page.status, page.body)
	}
	for _, lang := range []webui.Lang{webui.LangEN, webui.LangKO} {
		if lang == webui.LangKO {
			page = browserGET(t, client, server.URL+"/repositories/project/import?lang=ko")
		}
		if page.status != http.StatusServiceUnavailable || !strings.Contains(page.body, shownText(lang, webui.MsgImportStatusUnreadable)) {
			t.Fatalf("%s page with an unreadable status status=%d body=%s", lang, page.status, page.body)
		}
	}
}

// shownText is a message as a page shows it in lang, not as the other
// language's switch attribute.
func shownText(lang webui.Lang, code webui.MessageCode) string {
	return ">" + html.EscapeString(webui.Text(lang, code)) + "</span>"
}

func TestImportPageOffersOwnerResolutionInBothLanguages(t *testing.T) {
	fixture := newAPIFixture(t, false)
	askEveryTime(t, fixture.app)
	intentID := recordUnresolvedPublication(t, fixture)
	server := serve(t, fixture.app.Handler())
	client, jar := newBrowserClient(t)
	csrf := browserAdminSessionFor(t, fixture, server.URL, jar, "resolve-admin")
	for _, lang := range []webui.Lang{webui.LangEN, webui.LangKO} {
		page := browserGET(t, client, server.URL+"/repositories/project/import?lang="+string(lang))
		if page.status != http.StatusOK || !strings.Contains(page.body, webui.Text(lang, webui.MsgImportResolve)) || !strings.Contains(page.body, `value="import_resolve"`) {
			t.Fatalf("%s import page status=%d body=%s", lang, page.status, page.body)
		}
	}
	wrong := browserForm(t, client, server.URL+"/repositories/project/import", url.Values{
		"csrf": {csrf}, "action": {webui.ActionImportResolve}, "admin_password": {"wrong-password"},
	}, server.URL)
	if wrong.status != http.StatusUnauthorized {
		t.Fatalf("wrong password resolve status=%d", wrong.status)
	}
	resolved := browserForm(t, client, server.URL+"/repositories/project/import", url.Values{
		"csrf": {csrf}, "action": {webui.ActionImportResolve}, "admin_password": {"admin-password"},
	}, server.URL)
	if resolved.status != http.StatusSeeOther || !strings.Contains(resolved.header.Get("Location"), "notice=import_resolved") {
		t.Fatalf("resolve form status=%d location=%q body=%s", resolved.status, resolved.header.Get("Location"), resolved.body)
	}
	if intent, _, _ := fixture.store.ImportIntent(context.Background(), intentID); intent.Status != state.ImportIntentOwnerResolved {
		t.Fatalf("browser resolution left intent %s", intent.Status)
	}
	page := browserGET(t, client, server.URL+"/repositories/project/import?lang=ko")
	if strings.Contains(page.body, `value="import_resolve"`) {
		t.Fatal("resolved repository still offers resolution")
	}
}
