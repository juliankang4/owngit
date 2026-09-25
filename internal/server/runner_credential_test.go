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

	"owngit/internal/state"
)

// A live runner token presented for another repository gets its own code and
// message, which name no repository, and its use is not recorded. An unknown
// token keeps the unknown-or-revoked answer.
func TestRunnerTokenForAnotherRepositoryIsRefusedDistinctly(t *testing.T) {
	ctx := context.Background()
	store, err := state.Open(ctx, filepath.Join(t.TempDir(), "state"))
	noErr(t, err)
	defer store.Close()
	now := time.Unix(1_900_000_000, 0).UTC()
	for _, id := range []string{"alpha-repo", "beta-repo"} {
		noErr(t, store.AddRepository(ctx, state.Repository{ID: id, Name: id + "-name", CreatedAt: now}))
		if _, err := store.SetCheckPolicy(ctx, state.CheckPolicyInput{
			RepositoryID: id, Executor: state.CheckExecutorExternalRunner,
			AllowedEvents: []string{"push"}, MaxTimeoutMS: 60_000, MaxOutputLimitBytes: 64 << 10,
			QueueLimit: 4, MaxActiveJobs: 1, MaxLeaseMS: 60_000,
		}, now); err != nil {
			t.Fatal(err)
		}
	}
	credential, token, created, err := store.IssueCheckRunnerToken(ctx, "alpha-repo", "runner", "", now.Add(time.Second))
	if err != nil || !created {
		t.Fatalf("issue runner created=%v err=%v", created, err)
	}
	app := &App{Store: store, Now: func() time.Time { return now.Add(time.Minute) }}
	claim := func(repositoryID, token string) (int, string, string) {
		t.Helper()
		request := httptest.NewRequest(http.MethodPost, "/api/v1/repositories/"+repositoryID+"/runner/claim", nil)
		request.Header.Set("Authorization", "Bearer "+token)
		response := httptest.NewRecorder()
		app.handleRunnerAPI(response, request, repositoryID, "claim")
		var envelope struct {
			Error struct{ Code, Message string } `json:"error"`
		}
		noErr(t, json.Unmarshal(response.Body.Bytes(), &envelope))
		if strings.Contains(response.Body.String(), "alpha") || strings.Contains(response.Body.String(), credential.ID) {
			t.Fatalf("refusal names the token's repository or credential: %s", response.Body.String())
		}
		return response.Code, envelope.Error.Code, envelope.Error.Message
	}

	for _, repositoryID := range []string{"beta-repo", "missing-repo"} {
		status, code, message := claim(repositoryID, token)
		if status != http.StatusForbidden || code != "runner_credential_repository_mismatch" || message != "This runner token belongs to another repository." {
			t.Fatalf("token for %s: status=%d code=%s message=%q", repositoryID, status, code, message)
		}
	}
	credentials, err := store.CheckRunnerCredentials(ctx, "alpha-repo")
	if err != nil || len(credentials) != 1 || credentials[0].LastUsedAt != nil {
		t.Fatalf("a refused request recorded use: %+v err=%v", credentials, err)
	}

	unknown := token[:len(token)-1] + "0"
	if unknown == token {
		unknown = token[:len(token)-1] + "1"
	}
	for _, attempt := range []struct{ repositoryID, token string }{{"alpha-repo", unknown}, {"beta-repo", unknown}, {"beta-repo", "not-a-runner-token"}} {
		status, code, message := claim(attempt.repositoryID, attempt.token)
		if status != http.StatusUnauthorized || code != "invalid_runner_credential" || message != "The runner token is unknown or revoked." {
			t.Fatalf("unknown token for %s: status=%d code=%s message=%q", attempt.repositoryID, status, code, message)
		}
	}
	if status, code, _ := claim("alpha-repo", token); status != http.StatusOK || code != "" {
		t.Fatalf("own repository: status=%d code=%s", status, code)
	}
	noErr(t, store.RevokeCheckRunnerToken(ctx, "alpha-repo", credential.ID, now.Add(2*time.Minute)))
	for _, repositoryID := range []string{"alpha-repo", "beta-repo"} {
		if status, code, _ := claim(repositoryID, token); status != http.StatusUnauthorized || code != "invalid_runner_credential" {
			t.Fatalf("revoked token for %s: status=%d code=%s", repositoryID, status, code)
		}
	}
}
