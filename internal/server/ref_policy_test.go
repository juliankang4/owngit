package server

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"owngit/internal/state"
	"owngit/internal/webui"
)

// repositorySettingsAPI sends a request to a repository's settings with the
// administrator password and decodes the answer.
func repositorySettingsAPI(t *testing.T, server, repository, method string, body any) (int, repositorySettingsResponse, string) {
	t.Helper()
	response := adminAPIRequest(t, method, server+"/api/v1/repositories/"+repository+"/settings", body, "admin-password")
	var decoded struct {
		repositorySettingsResponse
		Error struct {
			Code    string            `json:"code"`
			Message string            `json:"message"`
			Details map[string]string `json:"details"`
		} `json:"error"`
	}
	decodeCheckJSON(t, response, &decoded)
	return response.StatusCode, decoded.repositorySettingsResponse, decoded.Error.Code + " " + decoded.Error.Details["setting"] + " " + decoded.Error.Message
}

// The server-wide kept history is saved from Settings, Repositories, and the
// settings API alike. Saving Do not keep warns what stops being kept, and a
// saved value that cannot be read is an error until it is set again.
func TestServerKeptHistoryIsSavedFromSettingsAndTheAPI(t *testing.T) {
	fixture, server, _ := newConfirmationFixture(t, true, state.ConfirmEveryTime)
	ctx := context.Background()
	browser := openConfirmationBrowser(t, server, true)
	browser.adminSignIn()
	page := browser.get("/settings/repositories")
	if !strings.Contains(page.body, `name="kept_history" data-saved="on"`) || !strings.Contains(page.body, enText(webui.MsgKeptHistoryOffWarning)) {
		t.Fatalf("Repositories does not offer kept history:\n%s", page.body)
	}
	if result := browser.post("/settings/repositories", url.Values{"action": {webui.ActionSaveKeptHistory}, "kept_history": {"sometimes"}, "admin_password": {"admin-password"}}); result.status != http.StatusBadRequest {
		t.Fatalf("an unknown choice: status=%d", result.status)
	}
	result := browser.post("/settings/repositories", url.Values{"action": {webui.ActionSaveKeptHistory}, "kept_history": {"off"}, "admin_password": {"admin-password"}})
	requireSaved(t, "kept history off", result)
	if location := result.header.Get("Location"); location != "/settings/repositories?notice=kept_history_off#grp-history" {
		t.Fatalf("saved Do not keep went to %q", location)
	}
	if keep, err := fixture.store.KeptHistory(ctx); err != nil || keep {
		t.Fatalf("saved keep=%v err=%v", keep, err)
	}
	page = browser.get("/settings/repositories?notice=kept_history_off")
	if !strings.Contains(page.body, `name="kept_history" data-saved="off"`) || !strings.Contains(page.body, enText(webui.MsgKeptHistorySavedOff)) {
		t.Fatalf("Repositories after Do not keep:\n%s", page.body)
	}

	if status, _, settings := settingsAPI(t, server.URL, http.MethodGet, nil); status != http.StatusOK || settings["kept_history"] != "off" {
		t.Fatalf("GET status=%d settings=%v", status, settings)
	}
	if status, code, _ := settingsAPI(t, server.URL, http.MethodPatch, map[string]any{"kept_history": "maybe"}); status != http.StatusBadRequest || code != "invalid_settings" {
		t.Fatalf("an unknown choice: status=%d code=%s", status, code)
	}
	var answer settingsResponse
	response := adminAPIRequest(t, http.MethodPatch, server.URL+"/api/v1/settings", map[string]any{"kept_history": "on"}, "admin-password")
	decodeCheckJSON(t, response, &answer)
	if response.StatusCode != http.StatusOK || *answer.Settings.KeptHistory != "on" || len(answer.Warnings) != 0 {
		t.Fatalf("PATCH on status=%d answer=%+v", response.StatusCode, answer)
	}
	response = adminAPIRequest(t, http.MethodPatch, server.URL+"/api/v1/settings", map[string]any{"kept_history": "off"}, "admin-password")
	decodeCheckJSON(t, response, &answer)
	if response.StatusCode != http.StatusOK || *answer.Settings.KeptHistory != "off" || len(answer.Warnings) != 1 || answer.Warnings[0] != webui.Text(webui.LangEN, webui.MsgKeptHistorySavedOff) {
		t.Fatalf("PATCH off status=%d answer=%+v", response.StatusCode, answer)
	}

	noErr(t, fixture.store.Exec(ctx, `UPDATE metadata SET value='maybe' WHERE key='retain_history'`))
	requireUnreadable(t, adminAPIRequest(t, http.MethodGet, server.URL+"/api/v1/settings", nil, "admin-password"), "kept_history", "retain_history")
	page = browser.get("/settings/repositories")
	if !strings.Contains(page.body, enText(webui.MsgPolicyUnreadable)) || !strings.Contains(page.body, `name="kept_history" data-saved=""`) {
		t.Fatalf("Repositories with an unreadable choice:\n%s", page.body)
	}
}

// A repository's own kept history choice and default branch protection are
// saved from its Settings tab and the owner API through the same operation:
// both warn when a change turns one off, and both refuse a choice that is
// not offered without changing anything.
func TestRepositoryHistoryChoicesAreSavedFromTheSettingsTabAndTheAPI(t *testing.T) {
	fixture := newAPIFixture(t, false)
	ctx := context.Background()
	server, client, jar := openBrowser(t, fixture)
	signInAdmin(t, fixture, server.URL, jar)
	saved := func() state.RepositoryRefPolicy {
		t.Helper()
		policy, err := fixture.store.RepositoryRefPolicy(ctx, "project")
		noErr(t, err)
		return policy
	}

	page := browserGET(t, client, server.URL+"/repositories/project/settings")
	for _, want := range []string{`action="/repositories/project/settings/history"`, `<option value="default" data-en="Follow the server setting (now Keep)"`, `name="protect_default_branch" value="on" aria-describedby="rset-protect-help">`} {
		if !strings.Contains(page.body, want) {
			t.Fatalf("the Settings tab lacks %q:\n%s", want, page.body)
		}
	}
	target := server.URL + "/repositories/project/settings/history"
	if result := browserForm(t, client, target, url.Values{"csrf": {"wrong"}, "kept_history": {"off"}}, server.URL); result.status != http.StatusForbidden {
		t.Fatalf("wrong csrf status=%d", result.status)
	}
	if result := browserForm(t, client, target, url.Values{"csrf": {adminTestCSRF}, "kept_history": {"sometimes"}, "protect_default_branch": {"on"}}, server.URL); result.status != http.StatusBadRequest {
		t.Fatalf("an unknown choice: status=%d", result.status)
	}
	if got := saved(); got != (state.RepositoryRefPolicy{KeptHistory: state.KeptHistoryDefault}) {
		t.Fatalf("a refused save changed the choices: %+v", got)
	}
	for _, step := range []struct {
		form   url.Values
		notice string
		want   state.RepositoryRefPolicy
	}{
		{url.Values{"kept_history": {"off"}, "protect_default_branch": {"on"}}, "history_saved_kept_off", state.RepositoryRefPolicy{KeptHistory: state.KeptHistoryOff, ProtectDefaultBranch: true}},
		{url.Values{"kept_history": {"default"}}, "history_saved_protect_off", state.RepositoryRefPolicy{KeptHistory: state.KeptHistoryDefault}},
		{url.Values{"kept_history": {"on"}, "protect_default_branch": {"on"}}, "history_saved", state.RepositoryRefPolicy{KeptHistory: state.KeptHistoryOn, ProtectDefaultBranch: true}},
		{url.Values{"kept_history": {"off"}}, "history_saved_both_off", state.RepositoryRefPolicy{KeptHistory: state.KeptHistoryOff}},
	} {
		step.form.Set("csrf", adminTestCSRF)
		result := browserForm(t, client, target, step.form, server.URL)
		if result.status != http.StatusSeeOther || result.header.Get("Location") != "/repositories/project/settings?notice="+step.notice {
			t.Fatalf("%v: status=%d location=%q", step.form, result.status, result.header.Get("Location"))
		}
		if got := saved(); got != step.want {
			t.Fatalf("%v saved %+v, want %+v", step.form, got, step.want)
		}
	}
	page = browserGET(t, client, server.URL+"/repositories/project/settings?notice=history_saved_both_off")
	for _, want := range []webui.MessageCode{webui.MsgRepoHistorySaved, webui.MsgRepoHistoryKeptOff, webui.MsgRepoHistoryProtectOff} {
		if !strings.Contains(page.body, enText(want)) {
			t.Fatalf("the saved notice lacks %s", want)
		}
	}

	status, answer, problem := repositorySettingsAPI(t, server.URL, "project", http.MethodGet, nil)
	if status != http.StatusOK || *answer.Settings.KeptHistory != "off" || answer.Settings.KeptHistoryNow != "off" || *answer.Settings.ProtectDefaultBranch {
		t.Fatalf("GET status=%d answer=%+v %s", status, answer, problem)
	}
	status, answer, problem = repositorySettingsAPI(t, server.URL, "project", http.MethodPatch, map[string]any{"kept_history": "default", "protect_default_branch": true})
	if status != http.StatusOK || *answer.Settings.KeptHistory != "default" || answer.Settings.KeptHistoryNow != "on" || !*answer.Settings.ProtectDefaultBranch || len(answer.Warnings) != 0 {
		t.Fatalf("PATCH status=%d answer=%+v %s", status, answer, problem)
	}
	status, answer, problem = repositorySettingsAPI(t, server.URL, "project", http.MethodPatch, map[string]any{"protect_default_branch": false})
	if status != http.StatusOK || *answer.Settings.KeptHistory != "default" || len(answer.Warnings) != 1 || answer.Warnings[0] != webui.Text(webui.LangEN, webui.MsgRepoHistoryProtectOff) {
		t.Fatalf("PATCH protection off status=%d answer=%+v %s", status, answer, problem)
	}
	for _, refused := range []struct {
		repository string
		body       any
		status     int
	}{
		{"project", map[string]any{}, http.StatusBadRequest},
		{"project", map[string]any{"kept_history": "sometimes"}, http.StatusBadRequest},
		{"project", map[string]any{"kept_history_now": "off"}, http.StatusBadRequest},
		{"missing", map[string]any{"kept_history": "off"}, http.StatusNotFound},
	} {
		if status, _, problem := repositorySettingsAPI(t, server.URL, refused.repository, http.MethodPatch, refused.body); status != refused.status {
			t.Fatalf("%s %v: status=%d %s", refused.repository, refused.body, status, problem)
		}
	}
	response := apiRequest(t, http.MethodGet, server.URL+"/api/v1/repositories/project/settings", nil, "shared-password", "")
	if status, code := checkStatus(t, response); status != http.StatusUnauthorized || code != "admin_authentication_required" {
		t.Fatalf("shared password status=%d code=%s", status, code)
	}

	// A row that cannot be read is an error in the API and a warning on
	// the tab; a change that names both choices replaces it.
	noErr(t, fixture.store.Exec(ctx, `PRAGMA ignore_check_constraints=ON; UPDATE repository_policies SET protect_default_branch=9 WHERE repository_id='project'; PRAGMA ignore_check_constraints=OFF`))
	if status, _, problem := repositorySettingsAPI(t, server.URL, "project", http.MethodGet, nil); status != http.StatusConflict || !strings.HasPrefix(problem, "setting_unreadable repository_policy ") || !strings.Contains(problem, "owngit repo settings set") {
		t.Fatalf("GET of an unreadable row: status=%d %s", status, problem)
	}
	if status, _, problem := repositorySettingsAPI(t, server.URL, "project", http.MethodPatch, map[string]any{"kept_history": "on"}); status != http.StatusConflict {
		t.Fatalf("PATCH of one choice over an unreadable row: status=%d %s", status, problem)
	}
	page = browserGET(t, client, server.URL+"/repositories/project/settings")
	if page.status != http.StatusOK || !strings.Contains(page.body, enText(webui.MsgRepoHistoryUnreadable)) {
		t.Fatalf("the Settings tab with an unreadable row: status=%d", page.status)
	}
	if status, answer, problem := repositorySettingsAPI(t, server.URL, "project", http.MethodPatch, map[string]any{"kept_history": "on", "protect_default_branch": true}); status != http.StatusOK || !*answer.Settings.ProtectDefaultBranch {
		t.Fatalf("PATCH of both choices over an unreadable row: status=%d %+v %s", status, answer, problem)
	}
}
