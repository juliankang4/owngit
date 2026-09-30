package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"owngit/internal/state"
	"owngit/internal/webui"
)

// The check ceilings are set in Settings, Repositories and with the
// settings API. A raised ceiling warns; a lowered one names the
// repositories whose policy is now above it, and their checks page says
// that no new check starts.
func TestCheckCeilingsAreSetAndNameThePoliciesAboveThem(t *testing.T) {
	fixture, server, _ := newConfirmationFixture(t, false, state.ConfirmEveryTime)
	ctx := context.Background()
	browser := openConfirmationBrowser(t, server, false)
	browser.adminSignIn()
	form := func(queue string) url.Values {
		return url.Values{
			"action": {webui.ActionSaveCheckCeilings}, "admin_password": {"admin-password"},
			"ceiling_timeout": {"48"}, "ceiling_timeout_unit": {"h"}, "ceiling_output": {"64"}, "ceiling_output_unit": {"MB"},
			"ceiling_queue": {queue}, "ceiling_active": {"100"}, "ceiling_cpu": {"64"}, "ceiling_cpu_unit": {"cores"},
			"ceiling_memory": {"64"}, "ceiling_memory_unit": {"GB"}, "ceiling_pids": {"4096"},
			"ceiling_scratch": {"16"}, "ceiling_scratch_unit": {"GB"}, "ceiling_source": {"4"}, "ceiling_source_unit": {"GB"},
		}
	}
	if refused := browser.post("/settings/repositories", form("10001")); refused.status != http.StatusUnprocessableEntity || !strings.Contains(refused.body, `value="10001"`) {
		t.Fatalf("a queue ceiling above its record bound: status=%d", refused.status)
	}
	saved := browser.post("/settings/repositories", form("5000"))
	requireSaved(t, "check ceilings", saved)
	want := state.DefaultCheckCeilings
	want.TimeoutMS, want.QueueLimit = 48*60*60*1000, 5000
	if ceilings, err := fixture.store.CheckCeilings(ctx); err != nil || ceilings != want {
		t.Fatalf("saved %+v, %v; want %+v", ceilings, err, want)
	}
	page := browser.get("/settings/repositories?notice=ceilings_looser")
	if !strings.Contains(page.body, `id="grp-ceilings"`) || !strings.Contains(page.body, enText(webui.MsgCeilingsWarning)) ||
		!strings.Contains(page.body, `value="5000" data-saved="5000"`) {
		t.Fatal("Repositories does not show the saved ceilings and the warning")
	}

	_, err := fixture.store.SetCheckPolicy(ctx, state.CheckPolicyInput{
		RepositoryID: "project", Executor: state.CheckExecutorExternalRunner, AllowedEvents: []string{"push"},
		MaxTimeoutMS: 600000, MaxOutputLimitBytes: 65536, QueueLimit: 3000, MaxActiveJobs: 1, MaxLeaseMS: 60000,
	}, fixture.app.now())
	noErr(t, err)

	response := adminAPIRequest(t, http.MethodPatch, server.URL+"/api/v1/settings", map[string]any{"check_ceilings": map[string]any{"queue_limit": 2000}}, "admin-password")
	defer response.Body.Close()
	var answer struct {
		Settings struct {
			CheckCeilings state.CheckCeilingFields `json:"check_ceilings"`
		} `json:"settings"`
		Warnings []string `json:"warnings"`
	}
	noErr(t, json.NewDecoder(response.Body).Decode(&answer))
	if response.StatusCode != http.StatusOK || *answer.Settings.CheckCeilings.QueueLimit != 2000 || *answer.Settings.CheckCeilings.TimeoutSeconds != 48*60*60 {
		t.Fatalf("PATCH one ceiling: status=%d settings=%+v", response.StatusCode, answer.Settings.CheckCeilings)
	}
	if len(answer.Warnings) != 2 || !strings.HasSuffix(answer.Warnings[1], ": project") {
		t.Fatalf("warnings %q, want the raised-ceiling warning and the repository above the ceilings", answer.Warnings)
	}
	if status, code, _ := settingsAPI(t, server.URL, http.MethodPatch, map[string]any{"check_ceilings": map[string]any{"active_jobs": 1001}}); status != http.StatusBadRequest || code != "invalid_settings" {
		t.Fatalf("PATCH above the record bound: status=%d code=%s", status, code)
	}

	if page := browser.get("/settings/repositories"); !strings.Contains(page.body, enText(webui.MsgCeilingsAbove)) || !strings.Contains(page.body, "project") {
		t.Fatal("Repositories does not name the repository above the ceilings")
	}
	checks := browser.get(configuredChecksURL("project"))
	if !strings.Contains(checks.body, enText(webui.MsgCCPolicyAboveCeilings)) || !strings.Contains(checks.body, "Allowed: 1 to 2,000") {
		t.Fatal("the checks page does not say the policy is above the ceilings, or states another range")
	}
}

// Unreadable ceilings stop policy saves with advice, never with the
// defaults.
func TestUnreadableCheckCeilingsStopPolicySaves(t *testing.T) {
	fixture, server, _ := newConfirmationFixture(t, false, state.ConfirmEveryTime)
	noErr(t, fixture.store.Exec(context.Background(), `INSERT INTO metadata(key,value) VALUES('check_ceilings','{"queue_limit":0}')`))
	browser := openConfirmationBrowser(t, server, false)
	browser.adminSignIn()
	if checks := browser.get(configuredChecksURL("project")); !strings.Contains(checks.body, enText(webui.MsgCCCeilingsUnreadable)) {
		t.Fatal("the checks page does not say the ceilings cannot be read")
	}
	response := adminAPIRequest(t, http.MethodPut, server.URL+"/api/v1/repositories/project/check-policy", map[string]any{
		"executor": state.CheckExecutorExternalRunner, "allowed_events": []string{"push"}, "max_timeout_ms": 600000,
		"max_output_limit_bytes": 65536, "queue_limit": 4, "max_active_jobs": 1, "max_lease_ms": 60000,
	}, "admin-password")
	defer response.Body.Close()
	var answer struct {
		Error struct {
			Code    string            `json:"code"`
			Details map[string]string `json:"details"`
		} `json:"error"`
	}
	noErr(t, json.NewDecoder(response.Body).Decode(&answer))
	if response.StatusCode != http.StatusConflict || answer.Error.Code != "setting_unreadable" {
		t.Fatalf("policy save with unreadable ceilings: status=%d answer=%+v", response.StatusCode, answer)
	}
}
