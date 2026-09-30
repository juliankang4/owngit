package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"owngit/internal/importfetch"
	"owngit/internal/importsync"
	"owngit/internal/webui"
)

type importSourceResponse struct {
	URL               string                   `json:"url"`
	AuthorityRevision int64                    `json:"authority_revision"`
	Options           importsync.OptionsStatus `json:"options"`
}

func decodeImportSource(t *testing.T, response *http.Response) importSourceResponse {
	t.Helper()
	body := importAPIBody(t, response)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.StatusCode, body)
	}
	var source importSourceResponse
	noErr(t, json.Unmarshal([]byte(body), &source))
	return source
}

func TestImportOptionsAPIChangesOnlyWhatItNames(t *testing.T) {
	fixture := newAPIFixture(t, false)
	server := serve(t, fixture.app.Handler())
	base := server.URL + "/api/v1/repositories/project/import"
	plain := map[string]any{"url": "http://example.invalid/team/project.git", "mode": "standalone"}
	if refused := importAPIRequest(t, http.MethodPut, base, plain, "admin-password"); refused.StatusCode != http.StatusUnprocessableEntity || importAPICode(t, refused) != importsync.CodeInvalidSource {
		t.Fatalf("plain HTTP without consent status=%d", refused.StatusCode)
	}
	plain["allow_plain_http"] = true
	plain["limits"] = map[string]int64{"run_seconds": 7200}
	saved := decodeImportSource(t, importAPIRequest(t, http.MethodPut, base, plain, "admin-password"))
	if !saved.Options.AllowPlainHTTP || saved.Options.Limits.RunSeconds != 7200 || strings.Join(saved.Options.ChangedLimits, ",") != "run_seconds" {
		t.Fatalf("saved options = %+v", saved.Options)
	}

	if unauthenticated := importAPIRequest(t, http.MethodPatch, base, map[string]any{"allow_reserved_addresses": true}, ""); unauthenticated.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthenticated change status=%d", unauthenticated.StatusCode)
	}
	changed := decodeImportSource(t, importAPIRequest(t, http.MethodPatch, base, map[string]any{
		"redirects": "approved", "approved_redirect_origin": "HTTPS://Mirror.Example", "limits": map[string]int64{"pack_bytes": 1 << 30},
	}, "admin-password"))
	if changed.URL != saved.URL || !changed.Options.AllowPlainHTTP || changed.Options.Redirects != "approved" ||
		changed.Options.ApprovedRedirectOrigin != "https://mirror.example" || changed.Options.Limits.RunSeconds != 7200 ||
		changed.Options.Limits.PackBytes != 1<<30 || changed.AuthorityRevision != saved.AuthorityRevision+1 {
		t.Fatalf("changed source = %+v", changed)
	}
	for name, body := range map[string]map[string]any{
		"zero limit":      {"limits": map[string]int64{"refs": 0}},
		"unknown limit":   {"limits": map[string]int64{"packets": 1}},
		"incoherent time": {"limits": map[string]int64{"fetch_seconds": 3 * 3600}},
		"bad origin":      {"approved_redirect_origin": "https://mirror.example/path"},
		"unknown policy":  {"redirects": "follow"},
	} {
		if response := importAPIRequest(t, http.MethodPatch, base, body, "admin-password"); response.StatusCode != http.StatusUnprocessableEntity || importAPICode(t, response) != importsync.CodeInvalidSource {
			t.Errorf("%s: status=%d", name, response.StatusCode)
		}
	}
	if response := importAPIRequest(t, http.MethodPatch, base, map[string]any{"allow_everything": true}, "admin-password"); response.StatusCode != http.StatusBadRequest {
		t.Errorf("unknown field status=%d", response.StatusCode)
	}

	// Saving the same address keeps every option; a new address keeps only
	// the limits.
	same := decodeImportSource(t, importAPIRequest(t, http.MethodPut, base, map[string]any{"url": saved.URL, "mode": "coexistence"}, "admin-password"))
	if same.Options.Redirects != "approved" || !same.Options.AllowPlainHTTP {
		t.Fatalf("same address options = %+v", same.Options)
	}
	moved := decodeImportSource(t, importAPIRequest(t, http.MethodPut, base, map[string]any{"url": "https://example.invalid/other/project.git", "mode": "standalone"}, "admin-password"))
	if moved.Options.AllowPlainHTTP || moved.Options.Redirects != "refuse" || moved.Options.ApprovedRedirectOrigin != "" || moved.Options.Limits.PackBytes != 1<<30 {
		t.Fatalf("new address options = %+v", moved.Options)
	}
	status := importAPIBody(t, importAPIRequest(t, http.MethodGet, base, nil, "admin-password"))
	if !strings.Contains(status, `"options":{`) || !strings.Contains(status, `"changed_limits":["pack_bytes","run_seconds"]`) {
		t.Fatalf("status does not report the options: %s", status)
	}
}

// A run started over HTTP keeps its request open for the source's own run
// time, and no longer, so a longer run time is not cut short by the request.
func TestImportRunRequestFollowsTheSourceRunTime(t *testing.T) {
	fixture := newAPIFixture(t, false)
	var runDeadline time.Duration
	previous := fixture.app.Imports.Fetch
	fixture.app.Imports.Fetch = func(ctx context.Context, request importfetch.Request, consume importfetch.PackConsumer) (*importfetch.Result, error) {
		if deadline, ok := ctx.Deadline(); ok {
			runDeadline = time.Until(deadline)
		}
		return previous(ctx, request, consume)
	}
	var requestDeadline time.Duration
	fixture.app.requestObserver = func(request *http.Request) {
		if deadline, ok := request.Context().Deadline(); ok {
			requestDeadline = time.Until(deadline)
		}
	}
	server := serve(t, fixture.app.Handler())
	within := func(got, want time.Duration) bool { return got <= want && got > want-time.Minute }

	created := importAPIRequest(t, http.MethodPost, server.URL+"/api/v1/repositories/long/import/run", map[string]any{
		"name": "long", "url": "https://example.invalid/team/long.git", "limits": map[string]int64{"run_seconds": 3 * 3600},
	}, "admin-password")
	if body := importAPIBody(t, created); created.StatusCode != http.StatusOK {
		t.Fatalf("import status=%d body=%s", created.StatusCode, body)
	}
	if !within(runDeadline, 3*time.Hour) || !within(requestDeadline, 3*time.Hour+ImportResponseMargin) {
		t.Fatalf("new import: run deadline %s, request deadline %s", runDeadline, requestDeadline)
	}

	refreshed := importAPIRequest(t, http.MethodPost, server.URL+"/api/v1/repositories/long/import/run", map[string]any{}, "admin-password")
	if body := importAPIBody(t, refreshed); refreshed.StatusCode != http.StatusOK {
		t.Fatalf("refresh status=%d body=%s", refreshed.StatusCode, body)
	}
	if !within(runDeadline, 3*time.Hour) || !within(requestDeadline, 3*time.Hour+ImportResponseMargin) {
		t.Fatalf("refresh: run deadline %s, request deadline %s", runDeadline, requestDeadline)
	}

	// A source without its own run time keeps the server's.
	fixture.app.ImportRunTimeout = 20 * time.Minute
	decodeImportSource(t, importAPIRequest(t, http.MethodPatch, server.URL+"/api/v1/repositories/long/import", map[string]any{"limits": map[string]int64{"run_seconds": 3600}}, "admin-password"))
	refreshed = importAPIRequest(t, http.MethodPost, server.URL+"/api/v1/repositories/long/import/run", map[string]any{}, "admin-password")
	if body := importAPIBody(t, refreshed); refreshed.StatusCode != http.StatusOK {
		t.Fatalf("refresh status=%d body=%s", refreshed.StatusCode, body)
	}
	if !within(runDeadline, 20*time.Minute) || !within(requestDeadline, 20*time.Minute+ImportResponseMargin) {
		t.Fatalf("default run time: run deadline %s, request deadline %s", runDeadline, requestDeadline)
	}
}

// The new-import form saves the connection options and limits it carries,
// and the Import tab shows them to an administrator, summarized and in the
// source form, and to nobody else.
func TestImportFormsSaveAndShowTheOptions(t *testing.T) {
	fixture := newAPIFixture(t, false)
	var request importfetch.Request
	previous := fixture.app.Imports.Fetch
	fixture.app.Imports.Fetch = func(ctx context.Context, got importfetch.Request, consume importfetch.PackConsumer) (*importfetch.Result, error) {
		request = got
		return previous(ctx, got, consume)
	}
	server := serve(t, fixture.app.Handler())
	client, jar := newBrowserClient(t)
	csrf := browserAdminSessionFor(t, fixture, server.URL, jar, "options-admin")
	created := browserForm(t, client, server.URL+"/repositories/new-import", url.Values{
		"csrf": {csrf}, "admin_password": {"admin-password"}, "name": {"plain"}, "url": {"http://example.invalid/team/plain.git"},
		"mode": {"standalone"}, "credential_form": {"none"}, "allow_plain_http": {"1"}, "redirects": {"same_origin"},
		"allow_reserved_addresses": {"1"}, "refs": {"100000"}, "pack_bytes": {"32"}, "pack_bytes_unit": {"GB"},
		"run_seconds": {""}, "run_seconds_unit": {"min"},
	}, server.URL)
	if created.status != http.StatusSeeOther {
		t.Fatalf("new import status=%d body=%s", created.status, created.body)
	}
	if !request.AllowPlainHTTP || request.Redirects != "same_origin" || !request.AllowReservedAddresses ||
		request.Limits.Advertisement.MaxRefRecords != 100_000 || request.Limits.MaxPackBytes != 32<<30 || request.Limits.TotalTimeout != 30*time.Minute {
		t.Fatalf("transport request = %+v", request)
	}
	for _, lang := range []webui.Lang{webui.LangEN, webui.LangKO} {
		page := browserGET(t, client, server.URL+"/repositories/plain/import?setup=1&lang="+string(lang))
		for _, want := range []string{
			webui.Text(lang, webui.MsgImportFactPlainHTTP), webui.Text(lang, webui.MsgImportFactSameOrigin), webui.Text(lang, webui.MsgImportFactReserved),
			`name="allow_plain_http" value="1" checked`, `value="same_origin" checked`, `<details class="ccadv" open>`,
			`name="refs" type="text" inputmode="numeric" autocomplete="off"`, `value="100000"`, `value="32"`,
		} {
			if !strings.Contains(page.body, want) {
				t.Errorf("%s import tab lacks %q", lang, want)
			}
		}
	}
	anonymous, _ := newBrowserClient(t)
	page := browserGET(t, anonymous, server.URL+"/repositories/plain/import")
	if page.status != http.StatusOK || strings.Contains(page.body, webui.Text(webui.LangEN, webui.MsgImportFactPlainHTTP)) || strings.Contains(page.body, `name="allow_plain_http"`) {
		t.Fatalf("a viewer without the administrator password sees the options: status=%d", page.status)
	}
}
