package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"owngit/internal/releasecheck"
	"owngit/internal/state"
)

func TestSettingsPartialGroupsPreserveUnnamedFieldsAndRequireCompleteRecovery(t *testing.T) {
	for _, test := range []struct {
		group, key, first, second string
		firstValue, secondValue   any
		complete                  any
	}{
		{"browse_limits", "browse_limits", "raw_bytes", "file_bytes", 11 << 20, 3 << 20, state.DefaultBrowseLimits.Fields()},
		{"git_transfer", "git_transfer_limits", "maximum_bytes", "operation_seconds", 2 << 30, 1200, state.DefaultGitTransferLimits.Fields()},
		{"maintenance", "maintenance", "window_start_hour", "pack_threshold", 22, 30, state.DefaultMaintenance.Fields()},
		{"unused_object_cleanup", "unused_object_cleanup", "enabled", "grace_days", true, 30, state.DefaultUnusedObjectCleanup.Fields()},
		{"check_ceilings", "check_ceilings", "output_bytes", "queue_limit", 2 << 20, 12, state.DefaultCheckCeilings.Fields()},
		{"login_limits", "login_limits", "attempts", "pause_seconds", 2, 1200, loginLimitsAPI(state.DefaultLoginLimits)},
	} {
		t.Run(test.group, func(t *testing.T) {
			fixture := newAPIFixture(t, false)
			server := serve(t, fixture.app.Handler())
			patch := func(body any) (int, string, map[string]any) {
				return settingsAPI(t, server.URL, http.MethodPatch, body)
			}
			for _, body := range []map[string]any{
				{test.group: test.complete},
				{test.group: map[string]any{test.first: test.firstValue}},
				{test.group: map[string]any{test.second: test.secondValue}},
			} {
				status, code, _ := patch(body)
				if status != http.StatusOK {
					t.Fatalf("PATCH %v: %d %s", body, status, code)
				}
			}
			_, _, settings := settingsAPI(t, server.URL, http.MethodGet, nil)
			got := settings[test.group].(map[string]any)
			wantJSON, err := json.Marshal(map[string]any{test.first: test.firstValue, test.second: test.secondValue})
			noErr(t, err)
			var want map[string]any
			noErr(t, json.Unmarshal(wantJSON, &want))
			for field, value := range want {
				if got[field] != value {
					t.Fatalf("%s=%v want=%v", field, got[field], value)
				}
			}
			status, code, _ := patch(map[string]any{"initial_branch": "trunk", test.group: map[string]any{}})
			if status != http.StatusBadRequest || code != "invalid_settings" {
				t.Fatalf("empty group: %d %s", status, code)
			}
			if branch, err := fixture.store.InitialBranch(t.Context()); err != nil || branch != "main" {
				t.Fatalf("refused change saved branch=%q err=%v", branch, err)
			}
			noErr(t, fixture.store.Exec(t.Context(), `UPDATE metadata SET value='null' WHERE key=?`, test.key))
			status, code, _ = patch(map[string]any{"initial_branch": "trunk", test.group: map[string]any{test.first: test.firstValue}})
			if status != http.StatusConflict || code != "setting_unreadable" {
				t.Fatalf("partial recovery: %d %s", status, code)
			}
			if branch, err := fixture.store.InitialBranch(t.Context()); err != nil || branch != "main" {
				t.Fatalf("unreadable group saved branch=%q err=%v", branch, err)
			}
			status, code, _ = patch(map[string]any{test.group: test.complete})
			if status != http.StatusOK {
				t.Fatalf("complete recovery: %d %s", status, code)
			}
		})
	}
}

func TestSettingsConcurrentSameFieldAndDisjointGroups(t *testing.T) {
	fixture := newAPIFixture(t, false)
	server := serve(t, fixture.app.Handler())
	for _, bodies := range [][]map[string]any{
		{{"browse_limits": map[string]any{"raw_bytes": 11 << 20}}, {"browse_limits": map[string]any{"raw_bytes": 12 << 20}}},
		{{"browse_limits": map[string]any{"file_bytes": 3 << 20}}, {"git_transfer": map[string]any{"operation_seconds": 1200}}},
	} {
		start := make(chan struct{})
		type result struct {
			status int
			err    error
		}
		done := make(chan result, 2)
		client := &http.Client{Timeout: 10 * time.Second}
		for _, body := range bodies {
			encoded, err := json.Marshal(body)
			noErr(t, err)
			request, err := http.NewRequestWithContext(t.Context(), http.MethodPatch, server.URL+"/api/v1/settings", bytes.NewReader(encoded))
			noErr(t, err)
			request.Header.Set("Content-Type", "application/json")
			request.SetBasicAuth("admin", "admin-password")
			go func() {
				<-start
				response, err := client.Do(request)
				outcome := result{err: err}
				if err == nil {
					outcome.status = response.StatusCode
					response.Body.Close()
				}
				done <- outcome
			}()
		}
		close(start)
		for range bodies {
			if outcome := <-done; outcome.err != nil || outcome.status != http.StatusOK {
				t.Fatalf("concurrent PATCH status=%d err=%v", outcome.status, outcome.err)
			}
		}
	}
	browse, err := fixture.store.BrowseLimits(t.Context())
	noErr(t, err)
	transfer, err := fixture.store.GitTransferLimits(t.Context())
	noErr(t, err)
	if (browse.RawBytes != 11<<20 && browse.RawBytes != 12<<20) || browse.FileBytes != 3<<20 || transfer.Operation != 20*time.Minute {
		t.Fatalf("browse=%+v transfer=%+v", browse, transfer)
	}
	// An ordered final save wins on the same field, with other fields kept.
	status, code, _ := settingsAPI(t, server.URL, http.MethodPatch, map[string]any{"browse_limits": map[string]any{"raw_bytes": 13 << 20}})
	if status != http.StatusOK {
		t.Fatalf("last save: %d %s", status, code)
	}
	browse, err = fixture.store.BrowseLimits(t.Context())
	noErr(t, err)
	if browse.RawBytes != 13<<20 || browse.FileBytes != 3<<20 {
		t.Fatalf("last save=%+v", browse)
	}
}

func TestSettingsStorageRefusalIsNotSuccess(t *testing.T) {
	fixture := newAPIFixture(t, false)
	server := serve(t, fixture.app.Handler())
	noErr(t, fixture.store.Exec(t.Context(), `CREATE TRIGGER refuse_browse BEFORE INSERT ON metadata WHEN NEW.key='browse_limits' BEGIN SELECT RAISE(ABORT, 'save refused'); END`))
	body := map[string]any{"initial_branch": "trunk", "browse_limits": map[string]any{"raw_bytes": 11 << 20}}
	status, code, _ := settingsAPI(t, server.URL, http.MethodPatch, body)
	if status != http.StatusServiceUnavailable || code != "state_unavailable" {
		t.Fatalf("failed save: %d %s", status, code)
	}
	if branch, err := fixture.store.InitialBranch(t.Context()); err != nil || branch != "main" {
		t.Fatalf("failed save wrote branch=%q err=%v", branch, err)
	}
	if browse, err := fixture.store.BrowseLimits(t.Context()); err != nil || browse != state.DefaultBrowseLimits {
		t.Fatalf("failed save wrote browse=%+v err=%v", browse, err)
	}
	noErr(t, fixture.store.Exec(t.Context(), `DROP TRIGGER refuse_browse`))
	status, code, _ = settingsAPI(t, server.URL, http.MethodPatch, body)
	if status != http.StatusOK {
		t.Fatalf("valid retry: %d %s", status, code)
	}
}

func TestPolicySaveWakesReleaseCheckOnlyAfterCommit(t *testing.T) {
	app, store, _ := newTestApp(t)
	off := false
	noErr(t, store.SavePolicies(t.Context(), state.PolicyChange{UpdateCheck: &off}))
	checks := make(chan state.Settings, 4)
	app.Releases = &releasecheck.Checker{
		InitialDelay: time.Hour, Interval: time.Hour,
		Enabled: func(ctx context.Context) (bool, error) {
			settings, err := store.Settings(ctx)
			if err == nil {
				checks <- settings
			}
			return false, err
		},
	}
	ctx, cancel := context.WithCancel(t.Context())
	finished := make(chan struct{})
	go func() { defer close(finished); app.Releases.Run(ctx) }()
	defer func() { cancel(); <-finished }()
	quiet := func() {
		t.Helper()
		select {
		case settings := <-checks:
			t.Fatalf("unexpected release check after save: %+v", settings)
		case <-time.After(100 * time.Millisecond):
		}
	}
	checked := func() {
		t.Helper()
		select {
		case settings := <-checks:
			if !settings.UpdateCheck {
				t.Fatalf("release checker ran before the enabled save committed: %+v", settings)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("committed save did not wake the release checker")
		}
		quiet()
	}
	on, badRaw := true, int64(0)
	if _, err := app.patchPolicies(t.Context(), state.PolicyChange{UpdateCheck: &on}, state.PolicyFields{Browse: &state.BrowseFields{RawBytes: &badRaw}}); err == nil {
		t.Fatal("invalid partial save succeeded")
	}
	quiet()
	bad := state.DefaultBrowseLimits
	bad.RawBytes = badRaw
	if err := app.savePolicies(t.Context(), state.PolicyChange{UpdateCheck: &on, Browse: &bad}); err == nil {
		t.Fatal("invalid complete save succeeded")
	}
	quiet()
	noErr(t, store.Exec(t.Context(), `CREATE TRIGGER refuse_update_check BEFORE INSERT ON metadata WHEN NEW.key='update_check' BEGIN SELECT RAISE(ABORT, 'save refused'); END`))
	if err := app.savePolicies(t.Context(), state.PolicyChange{UpdateCheck: &on}); err == nil {
		t.Fatal("storage refusal became a successful save")
	}
	quiet()
	raw := int64(11 << 20)
	if _, err := app.patchPolicies(t.Context(), state.PolicyChange{UpdateCheck: &on}, state.PolicyFields{Browse: &state.BrowseFields{RawBytes: &raw}}); err == nil {
		t.Fatal("partial storage refusal became a successful save")
	}
	quiet()
	noErr(t, store.Exec(t.Context(), `DROP TRIGGER refuse_update_check`))
	_, err := app.patchPolicies(t.Context(), state.PolicyChange{UpdateCheck: &on}, state.PolicyFields{Browse: &state.BrowseFields{RawBytes: &raw}})
	noErr(t, err)
	checked()
	noErr(t, app.savePolicies(t.Context(), state.PolicyChange{UpdateCheck: &on}))
	checked()
	noErr(t, app.savePolicies(t.Context(), state.PolicyChange{UpdateCheck: &off}))
	quiet()
}
