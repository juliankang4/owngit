package server

import (
	"net/http"
	"testing"

	"owngit/internal/checkapi"
	"owngit/internal/state"
)

// Save and enable over the API turns checks on for exactly the submitted
// policy, and refuses when the stored policy is not the one named.
func TestSaveAndEnableAPIBindsToTheReviewedPolicy(t *testing.T) {
	_, server, _ := newConfirmationFixture(t, false, state.Confirm30Days)
	target := server.URL + "/api/v1/repositories/project/check-policy/save-and-enable"
	policy := checkapi.PolicyInput{
		Executor: state.CheckExecutorExternalRunner, AllowedEvents: []string{"push"},
		MaxTimeoutMS: 5_000, MaxOutputLimitBytes: 64 << 10, QueueLimit: 4, MaxActiveJobs: 1, MaxLeaseMS: 10_000,
	}
	var first checkapi.PolicyResponse
	decodeCheckJSON(t, adminAPIRequest(t, http.MethodPost, target, checkapi.SaveAndEnableInput{Policy: policy, Expected: &checkapi.ExpectedPolicy{}}, "admin-password"), &first)
	if first.Policy == nil || !first.Policy.ConsentActive || first.Policy.Version != 1 {
		t.Fatalf("first save and enable=%+v", first.Policy)
	}

	policy.QueueLimit = 6
	stale := checkapi.SaveAndEnableInput{Policy: policy, Expected: &checkapi.ExpectedPolicy{}}
	if status, code := checkStatus(t, adminAPIRequest(t, http.MethodPost, target, stale, "admin-password")); status != http.StatusConflict || code != "check_policy_stale" {
		t.Fatalf("stale save and enable status=%d code=%q", status, code)
	}
	invalid := checkapi.SaveAndEnableInput{Policy: checkapi.PolicyInput{Executor: state.CheckExecutorContainer}}
	if status, code := checkStatus(t, adminAPIRequest(t, http.MethodPost, target, invalid, "admin-password")); status != http.StatusUnprocessableEntity || code != "invalid_check_policy" {
		t.Fatalf("invalid save and enable status=%d code=%q", status, code)
	}

	current := checkapi.SaveAndEnableInput{Policy: policy, Expected: &checkapi.ExpectedPolicy{Version: first.Policy.Version, Digest: first.Policy.Digest}}
	var saved checkapi.PolicyResponse
	decodeCheckJSON(t, adminAPIRequest(t, http.MethodPost, target, current, "admin-password"), &saved)
	if saved.Policy == nil || !saved.Policy.ConsentActive || saved.Policy.QueueLimit != 6 || saved.Policy.Version != 2 {
		t.Fatalf("save and enable=%+v", saved.Policy)
	}
}
