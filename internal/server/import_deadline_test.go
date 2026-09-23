package server

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"owngit/internal/importfetch"
	"owngit/internal/importsync"
)

// A run that reaches its own deadline still records its outcome and returns
// that outcome to the client, because the request outlives the run.
func TestImportRunDeadlineResultReachesTheClient(t *testing.T) {
	fixture := newImportAPIFixture(t)
	fixture.app.ImportRunTimeout = time.Second
	fixture.app.Imports.Fetch = func(ctx context.Context, _ importfetch.Request, _ importfetch.PackConsumer) (*importfetch.Result, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	server := httptest.NewServer(fixture.app.Handler())
	t.Cleanup(server.Close)
	var observed time.Duration
	fixture.app.requestObserver = func(request *http.Request) {
		if deadline, ok := request.Context().Deadline(); ok {
			observed = time.Until(deadline)
		}
	}
	started := time.Now()
	response := importAPIRequest(t, http.MethodPost, server.URL+"/api/v1/repositories/slow/import/run", map[string]any{
		"name": "slow", "url": "https://example.invalid/team/slow.git", "mode": "standalone",
	}, "admin-password", "", "")
	content, err := io.ReadAll(response.Body)
	response.Body.Close()
	if err != nil {
		t.Fatalf("run response could not be read after %s: %v", time.Since(started), err)
	}
	var envelope struct {
		OK    bool `json:"ok"`
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(content, &envelope); err != nil || envelope.OK || envelope.Error.Code != importsync.CodeLimit {
		t.Fatalf("deadline response status=%d body=%s err=%v", response.StatusCode, content, err)
	}
	if observed < ImportResponseMargin {
		t.Fatalf("import request deadline %s does not outlive the run deadline", observed)
	}
	runs, _, err := fixture.store.ImportRuns(context.Background(), "slow", 1)
	if err != nil || len(runs) != 1 || runs[0].ErrorClass != importsync.CodeLimit {
		t.Fatalf("recorded runs=%+v err=%v", runs, err)
	}
}
