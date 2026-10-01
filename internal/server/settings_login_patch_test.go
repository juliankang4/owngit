package server

import (
	"encoding/json"
	"math"
	"net/http"
	"testing"

	"owngit/internal/state"
)

func TestInvalidLoginLimitPatchSavesNothing(t *testing.T) {
	fixture := newAPIFixture(t, false)
	server := serve(t, fixture.app.Handler())
	for _, test := range []struct {
		field   string
		value   any
		message string
	}{
		{"attempts", 0, "login_limits: the number of attempts is from 1 to 100."},
		{"window_seconds", 59, "login_limits: window_seconds is from 60 to 86400."},
		{"pause_seconds", int64(math.MaxInt64), "login_limits: pause_seconds is from 60 to 86400."},
	} {
		response := adminAPIRequest(t, http.MethodPatch, server.URL+"/api/v1/settings", map[string]any{
			"initial_branch": "trunk", "browse_limits": map[string]any{"raw_bytes": 11 << 20},
			"login_limits": map[string]any{test.field: test.value},
		}, "admin-password")
		var answer struct {
			Error struct{ Code, Message string } `json:"error"`
		}
		err := json.NewDecoder(response.Body).Decode(&answer)
		response.Body.Close()
		noErr(t, err)
		if response.StatusCode != http.StatusBadRequest || answer.Error.Code != "invalid_settings" || answer.Error.Message != test.message {
			t.Fatalf("%s: status=%d error=%+v", test.field, response.StatusCode, answer.Error)
		}
		if limits, err := fixture.store.LoginLimits(t.Context()); err != nil || limits != state.DefaultLoginLimits {
			t.Fatalf("refused login limits=%+v err=%v", limits, err)
		}
		if browse, err := fixture.store.BrowseLimits(t.Context()); err != nil || browse != state.DefaultBrowseLimits {
			t.Fatalf("refused browsing limits=%+v err=%v", browse, err)
		}
		if branch, err := fixture.store.InitialBranch(t.Context()); err != nil || branch != "main" {
			t.Fatalf("refused branch=%q err=%v", branch, err)
		}
	}
}
