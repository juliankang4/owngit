package server

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"owngit/internal/checkapi"
	"owngit/internal/state"
	"owngit/internal/webui"
)

// settingsAPI sends a request to /api/v1/settings with the administrator
// password and decodes a successful answer.
func settingsAPI(t *testing.T, server string, method string, body any) (int, string, map[string]any) {
	t.Helper()
	response := adminAPIRequest(t, method, server+"/api/v1/settings", body, "admin-password")
	defer response.Body.Close()
	var decoded struct {
		OK       bool           `json:"ok"`
		Settings map[string]any `json:"settings"`
		Error    struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	noErr(t, json.NewDecoder(response.Body).Decode(&decoded))
	return response.StatusCode, decoded.Error.Code, decoded.Settings
}

// A chosen sign-in length applies to sign-ins after it is saved; a browser
// already signed in keeps its end.
func TestSessionLengthAppliesToLaterSignIns(t *testing.T) {
	fixture, server, clock := newConfirmationFixture(t, true, state.ConfirmEveryTime)
	ctx := context.Background()
	before := openConfirmationBrowser(t, server, true)
	requireSaved(t, "session length", before.post("/settings/access", url.Values{
		"action": {webui.ActionSaveSession}, "general_session": {"7d"}, "admin_password": {"admin-password"},
	}))
	after := openConfirmationBrowser(t, server, true)
	for _, check := range []struct {
		browser *confirmationBrowser
		life    time.Duration
	}{{before, 12 * time.Hour}, {after, 7 * 24 * time.Hour}} {
		session, ok, err := fixture.store.Session(ctx, check.browser.cookie(generalCookie), "general", clock.Now())
		if err != nil || !ok || !session.Expires.Equal(clock.Now().Add(check.life)) {
			t.Fatalf("session ok=%v err=%v ends %v, want %v", ok, err, session.Expires, clock.Now().Add(check.life))
		}
	}
	if page := after.get("/settings/access"); !strings.Contains(page.body, `name="general_session" data-saved="7d"`) {
		t.Fatalf("Access does not show the saved length:\n%s", page.body)
	}
}

// The owner API reads and changes the sign-in length with the
// administrator password, refuses an unknown length without saving
// anything, and asks for the password without it.
func TestSettingsAPIChangesTheSessionLength(t *testing.T) {
	fixture := newAPIFixture(t, true)
	server := serve(t, fixture.app.Handler())
	if status, _, settings := settingsAPI(t, server.URL, http.MethodGet, nil); status != http.StatusOK || settings["session"] != "12h" {
		t.Fatalf("GET status=%d settings=%v", status, settings)
	}
	if status, code, _ := settingsAPI(t, server.URL, http.MethodPatch, map[string]any{"session": "2d"}); status != http.StatusBadRequest || code != "invalid_settings" {
		t.Fatalf("unknown length status=%d code=%s", status, code)
	}
	if status, _, settings := settingsAPI(t, server.URL, http.MethodPatch, map[string]any{"session": "30d"}); status != http.StatusOK || settings["session"] != "30d" {
		t.Fatalf("PATCH status=%d settings=%v", status, settings)
	}
	if saved, err := fixture.store.GeneralSession(context.Background()); err != nil || saved != state.Session30Days {
		t.Fatalf("saved=%q err=%v", saved, err)
	}
	response := apiRequest(t, http.MethodPatch, server.URL+"/api/v1/settings", map[string]any{"session": "1h"}, "shared-password", "")
	if status, code := checkStatus(t, response); status != http.StatusUnauthorized || code != "admin_authentication_required" {
		t.Fatalf("shared password status=%d code=%s", status, code)
	}
}

// A saved length that cannot be read is an error where it applies, never
// the default: a sign-in fails and the API says to set it again. Access
// still opens and offers the default to save in its place.
func TestUnreadableSessionLengthIsAnErrorUntilSetAgain(t *testing.T) {
	fixture, server, _ := newConfirmationFixture(t, true, state.ConfirmEveryTime)
	noErr(t, fixture.store.Exec(context.Background(), `INSERT INTO metadata(key,value) VALUES('general_session_seconds','5')`))
	browser := openConfirmationBrowser(t, server, false)
	browser.get("/login")
	result := browserForm(t, browser.client, server.URL+"/login", url.Values{"csrf": {browser.cookie(preauthCookie)}, "password": {"shared-password"}, "next": {"/"}}, server.URL)
	if result.status != http.StatusConflict || browser.cookie(generalCookie) != "" || !strings.Contains(result.body, enText(webui.MsgSessionUnreadableSignIn)) {
		t.Fatalf("sign-in with an unreadable length status=%d:\n%s", result.status, result.body)
	}
	requireUnreadable(t, adminAPIRequest(t, http.MethodGet, server.URL+"/api/v1/settings", nil, "admin-password"), "session", "general_session_seconds")
	// A change to another setting is saved and answered as saved, naming
	// the one to set again.
	response := adminAPIRequest(t, http.MethodPatch, server.URL+"/api/v1/settings", map[string]any{"check_logs": "90d"}, "admin-password")
	var patched settingsResponse
	decodeCheckJSON(t, response, &patched)
	if response.StatusCode != http.StatusOK || patched.Settings.CheckLogs == nil || *patched.Settings.CheckLogs != "90d" || patched.Settings.Session != nil ||
		len(patched.Unreadable) != 1 || patched.Unreadable[0].Setting != "session" || !strings.Contains(patched.Unreadable[0].Message, "--session") {
		t.Fatalf("PATCH beside an unreadable setting status=%d answer=%+v", response.StatusCode, patched)
	}
	browser.adminSignIn()
	page := browser.get("/settings/access")
	if page.status != http.StatusOK || !strings.Contains(page.body, enText(webui.MsgPolicyUnreadable)) || !strings.Contains(page.body, `name="general_session" data-saved=""`) {
		t.Fatalf("Access status=%d:\n%s", page.status, page.body)
	}
	if status, _, settings := settingsAPI(t, server.URL, http.MethodPatch, map[string]any{"session": "12h"}); status != http.StatusOK || settings["session"] != "12h" {
		t.Fatalf("PATCH status=%d settings=%v", status, settings)
	}
}

// Repositories created after the initial branch is saved start on it, and
// the empty repository page says to push that branch. A repository created
// before keeps its branch, and a name OwnGit cannot use is refused.
func TestInitialBranchAppliesToLaterRepositories(t *testing.T) {
	fixture, server, _ := newConfirmationFixture(t, false, state.ConfirmEveryTime)
	ctx := context.Background()
	before, err := fixture.app.Repositories.Create(ctx, "before", "")
	noErr(t, err)
	browser := openConfirmationBrowser(t, server, false)
	for _, refused := range []string{"-main", "feature..x", "a b", "HEAD", "topic.lock", "브랜치"} {
		result := browser.post("/settings/repositories", url.Values{
			"action": {webui.ActionSaveInitialBranch}, "initial_branch": {refused}, "admin_password": {"admin-password"},
		})
		if result.status != http.StatusUnprocessableEntity || !strings.Contains(result.body, enText(webui.MsgInitialBranchInvalid)) {
			t.Fatalf("%q: status=%d", refused, result.status)
		}
	}
	requireSaved(t, "initial branch", browser.post("/settings/repositories", url.Values{
		"action": {webui.ActionSaveInitialBranch}, "initial_branch": {"release/trunk"}, "admin_password": {"admin-password"},
	}))
	after, err := fixture.app.Repositories.Create(ctx, "after", "")
	noErr(t, err)
	for _, check := range []struct{ id, branch string }{{before.ID, "main"}, {after.ID, "release/trunk"}} {
		path, err := fixture.app.Repositories.Path(check.id)
		noErr(t, err)
		if head := apiGitOutput(t, path, "symbolic-ref", "HEAD"); head != "refs/heads/"+check.branch {
			t.Fatalf("%s HEAD=%s, want %s", check.id, head, check.branch)
		}
	}
	if page := browser.get("/repositories/after"); !strings.Contains(page.body, "git push -u origin release/trunk") {
		t.Fatalf("the empty repository does not say to push its branch:\n%s", page.body)
	}
	if status, code, _ := settingsAPI(t, server.URL, http.MethodPatch, map[string]any{"initial_branch": "no spaces"}); status != http.StatusBadRequest || code != "invalid_settings" {
		t.Fatalf("API refusal status=%d code=%s", status, code)
	}
	if status, _, settings := settingsAPI(t, server.URL, http.MethodGet, nil); status != http.StatusOK || settings["initial_branch"] != "release/trunk" {
		t.Fatalf("GET status=%d settings=%v", status, settings)
	}
}

// The Git transfer limits are saved in the units they are typed in, and a
// value outside their bounds is refused with what was typed kept. The API
// changes one limit and keeps the other, and replaces an unreadable saved
// value only when it names both.
func TestTransferLimitsAreSavedWithTheirUnitsAndBounds(t *testing.T) {
	fixture, server, _ := newConfirmationFixture(t, false, state.ConfirmEveryTime)
	ctx := context.Background()
	browser := openConfirmationBrowser(t, server, false)
	save := func(size, sizeUnit, duration, durationUnit string) browserHTTPResult {
		return browser.post("/settings/repositories", url.Values{
			"action": {webui.ActionSaveTransfers}, "admin_password": {"admin-password"},
			"transfer_size": {size}, "transfer_size_unit": {sizeUnit}, "transfer_time": {duration}, "transfer_time_unit": {durationUnit},
		})
	}
	for _, refused := range [][4]string{{"65", "GB", "30", "min"}, {"4", "GB", "25", "h"}, {"0.5", "MB", "30", "min"}, {"four", "GB", "30", "min"}} {
		result := save(refused[0], refused[1], refused[2], refused[3])
		if result.status != http.StatusUnprocessableEntity || !strings.Contains(result.body, `value="`+refused[0]+`"`) {
			t.Fatalf("%v: status=%d", refused, result.status)
		}
	}
	requireSaved(t, "transfer limits", save("8", "GB", "2", "h"))
	if limits, err := fixture.store.GitTransferLimits(ctx); err != nil || limits != (state.GitTransferLimits{MaximumBytes: 8 << 30, Operation: 2 * time.Hour}) {
		t.Fatalf("saved %+v err=%v", limits, err)
	}
	page := browser.get("/settings/repositories")
	if !strings.Contains(page.body, `name="transfer_size" type="text"`) || !strings.Contains(page.body, `value="8" data-saved="8"`) || !strings.Contains(page.body, `value="2" data-saved="2"`) {
		t.Fatalf("Repositories does not show the saved limits:\n%s", page.body)
	}

	if status, _, settings := settingsAPI(t, server.URL, http.MethodPatch, map[string]any{"git_transfer": map[string]any{"maximum_bytes": 1 << 30}}); status != http.StatusOK ||
		settings["git_transfer"].(map[string]any)["operation_seconds"] != float64(7200) {
		t.Fatalf("PATCH one limit status=%d settings=%v", status, settings)
	}
	if status, code, _ := settingsAPI(t, server.URL, http.MethodPatch, map[string]any{"git_transfer": map[string]any{"operation_seconds": 30}}); status != http.StatusBadRequest || code != "invalid_settings" {
		t.Fatalf("PATCH below the bound status=%d code=%s", status, code)
	}
	noErr(t, fixture.store.Exec(ctx, `UPDATE metadata SET value='{"maximum_bytes":"many"}' WHERE key='git_transfer_limits'`))
	if status, code, _ := settingsAPI(t, server.URL, http.MethodPatch, map[string]any{"git_transfer": map[string]any{"operation_seconds": 600}}); status != http.StatusConflict || code != "setting_unreadable" {
		t.Fatalf("PATCH one limit over an unreadable value status=%d code=%s", status, code)
	}
	if status, _, _ := settingsAPI(t, server.URL, http.MethodPatch, map[string]any{"git_transfer": map[string]any{"maximum_bytes": 1 << 30, "operation_seconds": 600}}); status != http.StatusOK {
		t.Fatalf("PATCH both limits over an unreadable value status=%d", status)
	}
}

// Repositories shows any saved transfer limit exactly, whatever the API or
// the command line saved it in, so saving the form unchanged stores the
// same limits.
func TestTransferLimitsSurviveAnUnchangedSave(t *testing.T) {
	fixture, server, _ := newConfirmationFixture(t, false, state.ConfirmEveryTime)
	browser := openConfirmationBrowser(t, server, false)
	shown := func(body, id string) (amount, unit string) {
		t.Helper()
		input := regexp.MustCompile(`(?s)<input id="` + id + `"[^>]*?value="([^"]*)"`).FindStringSubmatch(body)
		menu := regexp.MustCompile(`(?s)<select id="` + id + `-unit".*?</select>`).FindString(body)
		selected := regexp.MustCompile(`<option value="([^"]*)" selected`).FindStringSubmatch(menu)
		if input == nil || selected == nil {
			t.Fatalf("%s is not shown with a unit:\n%s", id, body)
		}
		return input[1], selected[1]
	}
	for _, saved := range []state.GitTransferLimits{
		{MaximumBytes: 1<<20 + 1, Operation: 90 * time.Second},
		{MaximumBytes: 1536 << 10, Operation: 5400 * time.Second},
		{MaximumBytes: 64 << 30, Operation: 24 * time.Hour},
	} {
		if status, _, _ := settingsAPI(t, server.URL, http.MethodPatch, map[string]any{"git_transfer": map[string]any{
			"maximum_bytes": saved.MaximumBytes, "operation_seconds": int64(saved.Operation / time.Second),
		}}); status != http.StatusOK {
			t.Fatalf("PATCH %+v status=%d", saved, status)
		}
		page := browser.get("/settings/repositories")
		size, sizeUnit := shown(page.body, "transfer-size")
		duration, durationUnit := shown(page.body, "transfer-time")
		requireSaved(t, "unchanged transfer limits", browser.post("/settings/repositories", url.Values{
			"action": {webui.ActionSaveTransfers}, "admin_password": {"admin-password"},
			"transfer_size": {size}, "transfer_size_unit": {sizeUnit}, "transfer_time": {duration}, "transfer_time_unit": {durationUnit},
		}))
		if limits, err := fixture.store.GitTransferLimits(context.Background()); err != nil || limits != saved {
			t.Fatalf("shown as %s %s and %s %s, saved back as %+v (err=%v), want %+v", size, sizeUnit, duration, durationUnit, limits, err, saved)
		}
	}
}

// Storage & recovery saves how long raw check logs are kept, refuses a
// choice it does not offer, and the API reads and changes the same choice.
func TestRawLogRetentionIsSavedFromStorageAndTheAPI(t *testing.T) {
	fixture, server, _ := newConfirmationFixture(t, false, state.ConfirmEveryTime)
	ctx := context.Background()
	browser := openConfirmationBrowser(t, server, false)
	save := func(choice string) browserHTTPResult {
		return browser.post("/settings/storage", url.Values{"action": {webui.ActionSaveCheckLogs}, "check_logs": {choice}, "admin_password": {"admin-password"}})
	}
	if result := save("45d"); result.status != http.StatusBadRequest {
		t.Fatalf("an unknown choice: status=%d", result.status)
	}
	requireSaved(t, "raw log retention", save("indefinite"))
	if retention, err := fixture.store.CheckLogRetention(ctx); err != nil || retention != state.KeepCheckLogs {
		t.Fatalf("saved=%q err=%v", retention, err)
	}
	if page := browser.get("/settings/storage"); !strings.Contains(page.body, `name="check_logs" data-saved="indefinite"`) {
		t.Fatalf("Storage does not show the saved choice:\n%s", page.body)
	}
	if status, _, settings := settingsAPI(t, server.URL, http.MethodPatch, map[string]any{"check_logs": "90d"}); status != http.StatusOK || settings["check_logs"] != "90d" {
		t.Fatalf("PATCH status=%d settings=%v", status, settings)
	}
	if status, code, _ := settingsAPI(t, server.URL, http.MethodPatch, map[string]any{"check_logs": "forever"}); status != http.StatusBadRequest || code != "invalid_settings" {
		t.Fatalf("PATCH an unknown choice status=%d code=%s", status, code)
	}
}

// requireUnreadable checks the answer to an operation a saved setting
// stopped: 409 setting_unreadable naming the setting and how to set it
// again, without its metadata key or how it failed to parse.
func requireUnreadable(t *testing.T, response *http.Response, setting, key string) {
	t.Helper()
	defer response.Body.Close()
	var envelope struct {
		Error struct {
			Code    string            `json:"code"`
			Message string            `json:"message"`
			Details map[string]string `json:"details"`
		} `json:"error"`
	}
	noErr(t, json.NewDecoder(response.Body).Decode(&envelope))
	answer := envelope.Error
	if response.StatusCode != http.StatusConflict || answer.Code != "setting_unreadable" || answer.Details["setting"] != setting ||
		!strings.Contains(answer.Message, "owngit settings set") || strings.Contains(answer.Message, key) || strings.Contains(answer.Message, "json") {
		t.Fatalf("status=%d answer=%+v", response.StatusCode, answer)
	}
}

// Work a saved setting stops says which setting cannot be read and where
// to set it again: creating a repository without a usable initial branch,
// in the dashboard and the API, and a Git transfer or the settings API
// without usable transfer limits.
func TestUnreadableSettingsSayWhatToSetAgain(t *testing.T) {
	fixture, server, _ := newConfirmationFixture(t, false, state.ConfirmEveryTime)
	ctx := context.Background()
	browser := openConfirmationBrowser(t, server, false)
	noErr(t, fixture.store.Exec(ctx, `INSERT INTO metadata(key,value) VALUES('initial_branch','-main')`))
	page := browser.post("/repositories", url.Values{"name": {"later"}})
	if page.status != http.StatusConflict || !strings.Contains(page.body, enText(webui.MsgBranchUnreadableCreate)) {
		t.Fatalf("dashboard creation status=%d:\n%s", page.status, page.body)
	}
	requireUnreadable(t, apiRequest(t, http.MethodPost, server.URL+"/api/v1/repositories", map[string]any{"name": "later"}, "", ""), "initial_branch", "initial_branch")

	noErr(t, fixture.store.Exec(ctx, `UPDATE metadata SET key='git_transfer_limits',value='{"maximum_bytes":"big"}' WHERE key='initial_branch'`))
	requireUnreadable(t, adminAPIRequest(t, http.MethodGet, server.URL+"/api/v1/settings", nil, "admin-password"), "git_transfer", "git_transfer_limits")
	response, err := http.Get(server.URL + "/git/project.git/info/refs?service=git-upload-pack")
	noErr(t, err)
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	noErr(t, err)
	if response.StatusCode != http.StatusConflict || !strings.Contains(string(body), "--transfer-size") || strings.Contains(string(body), "git_transfer_limits") {
		t.Fatalf("Git transfer status=%d body=%s", response.StatusCode, body)
	}
}

// A raw log's expiry follows the retention saved now in every answer about
// it. Under Keep indefinitely the log reads without an expiry; while the
// retention cannot be read the log API names the setting, and the tasks
// page and attempt answers say nothing about a log they cannot judge.
func TestRawLogsFollowTheRetentionSavedNow(t *testing.T) {
	fixture := newAPIFixture(t, false)
	ctx := context.Background()
	base, token := helperAPI(t, fixture, "laptop", time.Now())
	server := strings.TrimSuffix(base, "/api/v1/repositories/project")
	taskID := createCheckTask(t, base, token, "Retention")
	var recorded checkapi.TaskResponse
	decodeCheckJSON(t, recordAttempt(t, base, taskID, token, attemptUploadBody(fixture.sourceOID, "clean", "failed")), &recorded)
	attempt := recorded.Attempt
	if attempt == nil || attempt.LogExpiresAt == nil {
		t.Fatalf("recorded attempt=%+v", attempt)
	}
	logURL := base + "/check-attempts/" + attempt.ID + "/log"
	latest := func() *checkapi.Attempt {
		t.Helper()
		var response checkapi.TaskResponse
		decodeCheckJSON(t, checkRequest(t, http.MethodGet, base+"/tasks/"+taskID, nil, token), &response)
		return response.Attempt
	}

	keep := state.KeepCheckLogs
	noErr(t, fixture.store.SavePolicies(ctx, state.PolicyChange{CheckLogs: &keep}))
	var kept checkapi.LogResponse
	decodeCheckJSON(t, checkRequest(t, http.MethodGet, logURL, nil, token), &kept)
	if !kept.OK || kept.Content == "" || kept.ExpiresAt != nil {
		t.Fatalf("log kept indefinitely=%+v", kept)
	}
	if got := latest(); got == nil || got.LogExpiresAt != nil {
		t.Fatalf("attempt kept indefinitely=%+v", got)
	}

	noErr(t, fixture.store.Exec(ctx, `UPDATE metadata SET value='45' WHERE key='check_log_retention_days'`))
	requireUnreadable(t, checkRequest(t, http.MethodGet, logURL, nil, token), "check_logs", "check_log_retention_days")
	if got := latest(); got == nil || got.LogID != attempt.ID || got.LogExpiresAt != nil {
		t.Fatalf("attempt under an unreadable retention=%+v", got)
	}
	client, _ := newBrowserClient(t)
	page := browserGET(t, client, server+tasksURL("project", taskID))
	if page.status != http.StatusOK || !strings.Contains(page.body, enText(webui.MsgCheckLogUnavailable)) {
		t.Fatalf("tasks page status=%d:\n%s", page.status, page.body)
	}
}

// The Settings menus offer exactly the choices the state accepts, in the
// same order, so no saved choice lacks its menu entry.
func TestPolicyMenusOfferEveryStateChoice(t *testing.T) {
	values := func(choices []webui.PolicyChoice) []string {
		names := make([]string, len(choices))
		for index, choice := range choices {
			names[index] = choice.Value
		}
		return names
	}
	if menu, stateChoices := values(webui.SessionChoices()), choiceList(state.GeneralSessions); strings.Join(menu, ", ") != stateChoices {
		t.Fatalf("session menu %v, state %s", menu, stateChoices)
	}
	if menu, stateChoices := values(webui.CheckLogChoices()), choiceList(state.CheckLogRetentions); strings.Join(menu, ", ") != stateChoices {
		t.Fatalf("raw log menu %v, state %s", menu, stateChoices)
	}
}
