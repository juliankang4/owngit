package server

import (
	"context"
	"html"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	"owngit/internal/importsync"
	"owngit/internal/state"
	"owngit/internal/webui"
)

// The API sets and changes the refresh choices like the other options, and
// refuses an extra namespace outside the allowed ones.
func TestImportAPIChangesRefreshChoices(t *testing.T) {
	fixture := newAPIFixture(t, false)
	server := serve(t, fixture.app.Handler())
	base := server.URL + "/api/v1/repositories/project/import"
	saved := decodeImportSource(t, importAPIRequest(t, http.MethodPut, base, map[string]any{
		"url": "https://example.invalid/team/project.git", "mode": "standalone",
		"extra_ref_prefixes": []string{"refs/notes/"}, "overwrite_diverged": true,
	}, "admin-password"))
	if !slices.Equal(saved.Options.ExtraRefPrefixes, []string{"refs/notes/"}) || !saved.Options.OverwriteDiverged || saved.Options.FollowUpstreamDeletions {
		t.Fatalf("saved options = %+v", saved.Options)
	}
	changed := decodeImportSource(t, importAPIRequest(t, http.MethodPatch, base, map[string]any{"follow_upstream_deletions": true}, "admin-password"))
	if !changed.Options.OverwriteDiverged || !changed.Options.FollowUpstreamDeletions || !slices.Equal(changed.Options.ExtraRefPrefixes, []string{"refs/notes/"}) ||
		changed.AuthorityRevision != saved.AuthorityRevision+1 {
		t.Fatalf("changed source = %+v", changed)
	}
	cleared := decodeImportSource(t, importAPIRequest(t, http.MethodPatch, base, map[string]any{"extra_ref_prefixes": []string{}}, "admin-password"))
	if cleared.Options.ExtraRefPrefixes == nil || len(cleared.Options.ExtraRefPrefixes) != 0 || !cleared.Options.FollowUpstreamDeletions {
		t.Fatalf("cleared source = %+v", cleared)
	}
	for _, prefixes := range [][]string{{"refs/tags/"}, {"refs/owngit/"}, {"refs/notes"}, {"notes/"}} {
		response := importAPIRequest(t, http.MethodPatch, base, map[string]any{"extra_ref_prefixes": prefixes}, "admin-password")
		if response.StatusCode != http.StatusUnprocessableEntity || importAPICode(t, response) != importsync.CodeInvalidSource {
			t.Errorf("%v: status=%d", prefixes, response.StatusCode)
		}
	}
	// A new address starts with overwrite and deletions off, and keeps the
	// namespaces.
	moved := decodeImportSource(t, importAPIRequest(t, http.MethodPut, base, map[string]any{"url": "https://example.invalid/other/project.git", "mode": "standalone"}, "admin-password"))
	if moved.Options.OverwriteDiverged || moved.Options.FollowUpstreamDeletions {
		t.Fatalf("new address options = %+v", moved.Options)
	}
}

// seedImportObservations records two completed refreshes of the fixture's
// source: the first saw refs/heads/gone, the second main and feature at the
// fixture's base commit, so gone was deleted at the source and feature,
// which holds another commit locally, diverged.
func seedImportObservations(t *testing.T, fixture apiFixture) {
	t.Helper()
	ctx := context.Background()
	source, err := fixture.app.Imports.ConfigureSource(ctx, importsync.ConfigureInput{
		RepositoryID: "project", URL: "https://example.invalid/team/project.git", Mode: importsync.ModeStandalone,
	})
	noErr(t, err)
	apiRunGit(t, "", "--git-dir", fixture.remote, "update-ref", "refs/heads/gone", fixture.targetOID)
	now := time.Now().UTC().Truncate(time.Second)
	for index, refs := range []map[string]string{
		{"refs/heads/gone": fixture.targetOID},
		{"refs/heads/main": fixture.targetOID, "refs/heads/feature": fixture.targetOID, state.ImportHeadRef: fixture.targetOID},
	} {
		run := state.ImportRun{
			ID: strings.Repeat(string(rune('a'+index)), 32), RepositoryID: "project", SourceGeneration: source.SourceGeneration,
			AuthorityRevision: source.AuthorityRevision, Kind: state.ImportKindRefresh, Status: state.ImportRunPreparing, StartedAt: now, CreatedAt: now,
		}
		noErr(t, fixture.store.BeginImportRun(ctx, run))
		run.Status, run.FinishedAt = state.ImportRunComplete, now
		noErr(t, fixture.store.FinishImportRun(ctx, run))
		var observations []state.ImportObservation
		for name, oid := range refs {
			observation := state.ImportObservation{RepositoryID: "project", SourceGeneration: source.SourceGeneration, RefName: name, OID: oid, ObservedAt: now, RunID: run.ID}
			if name == state.ImportHeadRef {
				observation.SymrefTarget = "refs/heads/main"
			}
			observations = append(observations, observation)
		}
		noErr(t, fixture.store.RecordImportObservations(ctx, observations))
	}
}

// Before the refresh choices are saved, the Import tab lists the refs they
// would change now and whether kept history covers them. Saving the form
// stores them, and a refused list stays as typed on its field.
func TestImportTabShowsAndSavesRefreshChoices(t *testing.T) {
	fixture := newAPIFixture(t, false)
	seedImportObservations(t, fixture)
	server := serve(t, fixture.app.Handler())
	client, jar := newBrowserClient(t)
	csrf := browserAdminSessionFor(t, fixture, server.URL, jar, "refresh-admin")
	for _, lang := range []webui.Lang{webui.LangEN, webui.LangKO} {
		page := browserGET(t, client, server.URL+"/repositories/project/import?setup=1&lang="+string(lang))
		for _, want := range []string{
			webui.Text(lang, webui.MsgImportEffectsHeading), `<span class="mono" dir="auto">refs/heads/feature</span>`, `<span class="mono" dir="auto">refs/heads/gone</span>`,
			html.EscapeString(webui.Text(lang, webui.MsgImportEffectReplace)), webui.Text(lang, webui.MsgImportEffectNeedsOverwrite),
			webui.Text(lang, webui.MsgImportEffectDelete), webui.Text(lang, webui.MsgImportEffectNeedsFollow), webui.Text(lang, webui.MsgImportEffectKept),
			webui.Text(lang, webui.MsgImportRefreshWarning), webui.Text(lang, webui.MsgImportExtraRefsWarning),
			`name="overwrite_diverged" value="1" data-import-transport`, `name="extra_ref_prefixes"`,
		} {
			if !strings.Contains(page.body, want) {
				t.Errorf("%s import tab lacks %q", lang, want)
			}
		}
	}
	save := func(prefixes string) browserHTTPResult {
		return browserForm(t, client, server.URL+"/repositories/project/import", url.Values{
			"csrf": {csrf}, "action": {webui.ActionImportConfigure}, "admin_password": {"admin-password"},
			"url": {"https://example.invalid/team/project.git"}, "options_url": {"https://example.invalid/team/project.git"}, "mode": {"standalone"},
			"redirects": {"refuse"}, "extra_ref_prefixes": {prefixes}, "overwrite_diverged": {"1"}, "follow_upstream_deletions": {"1"},
		}, server.URL)
	}
	refused := save("refs/notes/\nrefs/heads/")
	if refused.status != http.StatusUnprocessableEntity || !strings.Contains(refused.body, webui.Text(webui.LangEN, webui.MsgImportExtraRefsInvalid)) ||
		!strings.Contains(refused.body, "refs/notes/\nrefs/heads/</textarea>") || !strings.Contains(refused.body, `<details class="ccadv" open>`) {
		t.Fatalf("refused list status=%d", refused.status)
	}
	if result := save("refs/notes/\nrefs/changes/"); result.status != http.StatusSeeOther {
		t.Fatalf("save status=%d body=%s", result.status, result.body)
	}
	source, _, err := fixture.store.ImportSource(context.Background(), "project")
	if err != nil || !source.OverwriteDiverged || !source.FollowUpstreamDeletions || !slices.Equal(source.ExtraRefPrefixes, []string{"refs/notes/", "refs/changes/"}) {
		t.Fatalf("stored source = %+v, %v", source, err)
	}
	page := browserGET(t, client, server.URL+"/repositories/project/import")
	for _, want := range []string{webui.Text(webui.LangEN, webui.MsgImportFactOverwrite), webui.Text(webui.LangEN, webui.MsgImportFactFollowDeletions), "refs/notes/, refs/changes/"} {
		if !strings.Contains(page.body, want) {
			t.Errorf("status strip lacks %q", want)
		}
	}
	anonymous, _ := newBrowserClient(t)
	if page := browserGET(t, anonymous, server.URL+"/repositories/project/import"); strings.Contains(page.body, webui.Text(webui.LangEN, webui.MsgImportEffectsHeading)) ||
		strings.Contains(page.body, webui.Text(webui.LangEN, webui.MsgImportFactOverwrite)) {
		t.Fatal("a viewer without the administrator password sees the refresh choices")
	}
}
