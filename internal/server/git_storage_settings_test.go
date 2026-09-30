package server

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"owngit/internal/state"
	"owngit/internal/webui"
)

// The browsing limits, repository maintenance and unused object cleanup
// are read and changed through the settings API: a PATCH keeps the fields
// it does not name, refuses values out of bounds, and warns about choices
// that loosen a limit. A saved value that cannot be read is named and
// replaced only by a change that names every field of its group.
func TestGitStorageSettingsThroughTheAPI(t *testing.T) {
	fixture, server, _ := newConfirmationFixture(t, false, state.ConfirmEveryTime)
	ctx := context.Background()
	status, _, settings := settingsAPI(t, server.URL, http.MethodGet, nil)
	if status != http.StatusOK || settings["browse_limits"].(map[string]any)["raw_bytes"] != float64(10<<20) ||
		settings["maintenance"].(map[string]any)["enabled"] != true || settings["unused_object_cleanup"].(map[string]any)["enabled"] != false {
		t.Fatalf("GET status=%d settings=%v", status, settings)
	}
	for _, refused := range []map[string]any{
		{"browse_limits": map[string]any{"raw_bytes": 257 << 20}},
		{"browse_limits": map[string]any{"compare_seconds": 61}},
		{"browse_limits": map[string]any{}},
		{"maintenance": map[string]any{"window_start_hour": 4, "window_end_hour": 4}},
		{"maintenance": map[string]any{"pack_threshold": 1}},
		{"unused_object_cleanup": map[string]any{"grace_days": 1}},
		{"unused_object_cleanup": map[string]any{"grace_days": 366}},
	} {
		if status, code, _ := settingsAPI(t, server.URL, http.MethodPatch, refused); status != http.StatusBadRequest || code != "invalid_settings" {
			t.Fatalf("PATCH %v status=%d code=%s", refused, status, code)
		}
	}
	response := adminAPIRequest(t, http.MethodPatch, server.URL+"/api/v1/settings", map[string]any{
		"browse_limits":         map[string]any{"file_bytes": 4 << 20},
		"maintenance":           map[string]any{"window_start_hour": 22, "window_end_hour": 6},
		"unused_object_cleanup": map[string]any{"enabled": true},
	}, "admin-password")
	var answer settingsResponse
	decodeCheckJSON(t, response, &answer)
	if response.StatusCode != http.StatusOK || len(answer.Warnings) != 3 {
		t.Fatalf("PATCH status=%d warnings=%q", response.StatusCode, answer.Warnings)
	}
	for _, want := range []webui.MessageCode{webui.MsgBrowseWarning, webui.MsgMaintenanceWarning, webui.MsgCleanupWarning} {
		if !strings.Contains(strings.Join(answer.Warnings, "\n"), enText(want)) {
			t.Fatalf("warnings %q lack %s", answer.Warnings, want)
		}
	}
	browse, err := fixture.store.BrowseLimits(ctx)
	noErr(t, err)
	want := state.DefaultBrowseLimits
	want.FileBytes = 4 << 20
	if browse != want {
		t.Fatalf("browse limits %+v", browse)
	}
	if choices, err := fixture.store.Maintenance(ctx); err != nil || choices.WindowStart != 22 || choices.WindowEnd != 6 || choices.Idle != 5*time.Minute {
		t.Fatalf("maintenance %+v err=%v", choices, err)
	}
	if cleanup, err := fixture.store.UnusedObjectCleanup(ctx); err != nil || !cleanup.Enabled || cleanup.Grace != 14*24*time.Hour {
		t.Fatalf("cleanup %+v err=%v", cleanup, err)
	}

	noErr(t, fixture.store.Exec(ctx, `UPDATE metadata SET value='{"enabled":"yes"}' WHERE key='unused_object_cleanup'`))
	if status, code, _ := settingsAPI(t, server.URL, http.MethodGet, nil); status != http.StatusConflict || code != "setting_unreadable" {
		t.Fatalf("GET with unreadable cleanup status=%d code=%s", status, code)
	}
	if status, code, _ := settingsAPI(t, server.URL, http.MethodPatch, map[string]any{"unused_object_cleanup": map[string]any{"enabled": false}}); status != http.StatusConflict || code != "setting_unreadable" {
		t.Fatalf("PATCH part of an unreadable group status=%d code=%s", status, code)
	}
	if status, _, _ := settingsAPI(t, server.URL, http.MethodPatch, map[string]any{"unused_object_cleanup": map[string]any{"enabled": false, "grace_days": 30}}); status != http.StatusOK {
		t.Fatalf("PATCH every field of an unreadable group status=%d", status)
	}
}

// Settings saves each group from its tab, refuses a value out of bounds in
// the field that holds it, and says what a looser choice allows.
func TestGitStorageSettingsFromTheTabs(t *testing.T) {
	fixture, server, _ := newConfirmationFixture(t, false, state.ConfirmEveryTime)
	ctx := context.Background()
	browser := openConfirmationBrowser(t, server, false)
	browser.adminSignIn()
	browse := url.Values{
		"action": {webui.ActionSaveBrowseLimits}, "admin_password": {"admin-password"},
		"browse_raw": {"20"}, "browse_raw_unit": {"MB"}, "browse_file": {"2"}, "browse_file_unit": {"MB"},
		"browse_commit_patch": {"2"}, "browse_commit_patch_unit": {"MB"}, "browse_file_patch": {"8"}, "browse_file_patch_unit": {"MB"},
		"browse_commit_file": {"256"}, "browse_commit_file_unit": {"KB"}, "browse_compare": {"8"}, "browse_compare_unit": {"MB"},
		"browse_compare_time": {"20"}, "browse_compare_time_unit": {"s"},
	}
	refused := url.Values{}
	for key, value := range browse {
		refused[key] = value
	}
	refused.Set("browse_raw", "300")
	if result := browser.post("/settings/repositories", refused); result.status != http.StatusUnprocessableEntity || !strings.Contains(result.body, `value="300"`) {
		t.Fatalf("a raw limit over 256 MB: status=%d", result.status)
	}
	result := browser.post("/settings/repositories", browse)
	requireSaved(t, "browsing limits", result)
	if location := result.header.Get("Location"); location != "/settings/repositories?notice=browse_looser#grp-browse" {
		t.Fatalf("saved browsing limits went to %q", location)
	}
	if limits, err := fixture.store.BrowseLimits(ctx); err != nil || limits.RawBytes != 20<<20 {
		t.Fatalf("saved %+v err=%v", limits, err)
	}

	maintenance := url.Values{
		"action": {webui.ActionSaveMaintenance}, "admin_password": {"admin-password"},
		"maintenance_enabled": {"off"}, "maintenance_window_start": {"3"}, "maintenance_window_end": {"3"},
		"maintenance_idle": {"5"}, "maintenance_idle_unit": {"min"}, "maintenance_command": {"30"}, "maintenance_command_unit": {"min"},
		"maintenance_full_repack": {"2"}, "maintenance_full_repack_unit": {"h"}, "maintenance_pack_threshold": {"20"},
	}
	if result := browser.post("/settings/storage", maintenance); result.status != http.StatusUnprocessableEntity || !strings.Contains(result.body, enText(webui.MsgMaintenanceWindowSame)) {
		t.Fatalf("a window that starts where it ends: status=%d", result.status)
	}
	maintenance.Set("maintenance_window_end", "5")
	result = browser.post("/settings/storage", maintenance)
	requireSaved(t, "maintenance", result)
	if location := result.header.Get("Location"); location != "/settings/storage?notice=maintenance_looser#grp-maintenance" {
		t.Fatalf("saved maintenance went to %q", location)
	}
	if choices, err := fixture.store.Maintenance(ctx); err != nil || choices.Enabled {
		t.Fatalf("saved %+v err=%v", choices, err)
	}

	result = browser.post("/settings/storage", url.Values{"action": {webui.ActionSaveCleanup}, "admin_password": {"admin-password"}, "cleanup_enabled": {"on"}, "cleanup_grace": {"30"}})
	requireSaved(t, "cleanup", result)
	if location := result.header.Get("Location"); location != "/settings/storage?notice=cleanup_on#grp-cleanup" {
		t.Fatalf("saved cleanup went to %q", location)
	}
	page := browser.get("/settings/storage?notice=cleanup_on")
	for _, want := range []string{enText(webui.MsgCleanupWarning), `name="cleanup_grace" type="text" inputmode="numeric" autocomplete="off" spellcheck="false" required`, `value="30" data-saved="30"`} {
		if !strings.Contains(page.body, want) {
			t.Fatalf("Storage lacks %q:\n%s", want, page.body)
		}
	}
}

// A larger file view budget shows more of a file, a larger raw budget
// serves a larger download, and a result cut at the old budget is never
// served under the new one. Browsing limits that cannot be read stop the
// view and name the setting.
func TestBrowsingLimitsApplyToTheViews(t *testing.T) {
	app := newConfiguredApp(t)
	ctx := context.Background()
	seedRepository(t, app, "sizes", map[string]string{
		"large.txt": strings.Repeat("a line of text\n", (3<<20)/15),
		"big.bin":   strings.Repeat("\x00\x01", 6<<20),
	}, time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC))
	server := serve(t, app.Handler())
	client := &http.Client{}
	view := server.URL + "/repositories/sizes/code?ref=refs%2Fheads%2Fmain&path=large.txt"
	raw := server.URL + "/repositories/sizes/raw?ref=refs%2Fheads%2Fmain&path=big.bin"
	if body, _ := dashboardGET(t, client, view); !strings.Contains(body, enText(webui.MsgCodeTruncated)) {
		t.Fatal("a 3 MB file is shown whole under the 2 MB default")
	}
	if result := browserGET(t, client, raw); result.status != http.StatusForbidden {
		t.Fatalf("a 12 MB download under the 10 MB default: status=%d", result.status)
	}
	larger := state.DefaultBrowseLimits
	larger.FileBytes, larger.RawBytes = 4<<20, 16<<20
	noErr(t, app.Store.SavePolicies(ctx, state.PolicyChange{Browse: &larger}))
	if body, _ := dashboardGET(t, client, view); strings.Contains(body, enText(webui.MsgCodeTruncated)) {
		t.Fatal("the file view still shows the result cut at the old limit")
	}
	if result := browserGET(t, client, raw); result.status != http.StatusOK || len(result.body) != 12<<20 {
		t.Fatalf("a 12 MB download under a 16 MB limit: status=%d length=%d", result.status, len(result.body))
	}
	noErr(t, app.Store.Exec(ctx, `UPDATE metadata SET value='{"raw_bytes":1}' WHERE key='browse_limits'`))
	for _, target := range []string{view, raw} {
		if result := browserGET(t, client, target); result.status != http.StatusConflict || !strings.Contains(result.body, "--browse-") {
			t.Fatalf("%s with unreadable limits: status=%d", target, result.status)
		}
	}
}
