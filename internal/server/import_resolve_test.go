package server

import (
	"context"
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

func TestImportResolveAPIRequiresOwnerAndRecordsTheDecision(t *testing.T) {
	fixture := newImportAPIFixture(t)
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

func TestImportPageOffersOwnerResolutionInBothLanguages(t *testing.T) {
	fixture := newImportAPIFixture(t)
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
