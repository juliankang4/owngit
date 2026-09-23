package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"owngit/internal/checkapi"
	"owngit/internal/state"
)

func TestConfiguredCheckPolicyReportsUnavailableRuntimeAndRunnerIsClosed(t *testing.T) {
	ctx := context.Background()
	store, err := state.Open(ctx, filepath.Join(t.TempDir(), "state"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Unix(1_900_000_000, 0).UTC()
	if err := store.AddRepository(ctx, state.Repository{ID: "project", Name: "Project", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SetCheckPolicy(ctx, state.CheckPolicyInput{
		RepositoryID: "project", Executor: state.CheckExecutorExternalRunner,
		AllowedEvents: []string{"push"}, MaxTimeoutMS: 60_000, MaxOutputLimitBytes: 64 << 10,
		QueueLimit: 4, MaxActiveJobs: 1, MaxLeaseMS: 60_000,
	}, now); err != nil {
		t.Fatal(err)
	}
	_, token, created, err := store.IssueCheckRunnerToken(ctx, "project", "runner", "", now.Add(time.Second))
	if err != nil || !created {
		t.Fatalf("issue runner created=%v err=%v", created, err)
	}
	app := &App{
		Store:                         store,
		CheckRuntimeUnavailableCode:   "workspace_unavailable",
		CheckRuntimeUnavailableReason: "Configured checks are unavailable. Repair the workspace and restart OwnGit.",
	}

	policyRequest := httptest.NewRequest(http.MethodGet, "/api/v1/repositories/project/check-policy", nil)
	policyResponse := httptest.NewRecorder()
	app.handleCheckPolicy(policyResponse, policyRequest, "project", "")
	if policyResponse.Code != http.StatusOK {
		t.Fatalf("policy status=%d body=%s", policyResponse.Code, policyResponse.Body.String())
	}
	var decoded checkapi.PolicyResponse
	if err := json.Unmarshal(policyResponse.Body.Bytes(), &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Policy == nil || decoded.Runtime.Available || decoded.Runtime.UnavailableCode != "workspace_unavailable" || !strings.Contains(decoded.Runtime.UnavailableReason, "restart OwnGit") {
		t.Fatalf("policy runtime=%+v policy=%+v", decoded.Runtime, decoded.Policy)
	}

	runnerRequest := httptest.NewRequest(http.MethodPost, "/api/v1/repositories/project/runner/claim", nil)
	runnerRequest.Header.Set("Authorization", "Bearer "+token)
	runnerResponse := httptest.NewRecorder()
	app.handleRunnerAPI(runnerResponse, runnerRequest, "project", "claim")
	if runnerResponse.Code != http.StatusServiceUnavailable || !strings.Contains(runnerResponse.Body.String(), "check_runtime_unavailable") || !strings.Contains(runnerResponse.Body.String(), "workspace_unavailable") {
		t.Fatalf("runner status=%d body=%s", runnerResponse.Code, runnerResponse.Body.String())
	}
}
