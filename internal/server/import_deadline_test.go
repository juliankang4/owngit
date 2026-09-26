package server

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"testing"
	"time"

	"owngit/internal/importfetch"
	"owngit/internal/importsync"
)

// A run that reaches its own deadline still records its outcome and returns
// that outcome to the client, because the request outlives the run. The run
// deadline must pass while the source is fetched, after the run is recorded,
// so it leaves admission a margin that only a hang uses up; a deadline during
// admission is TestImportDeadlineDuringAdmissionReachesTheClientAsTheLimit.
func TestImportRunDeadlineResultReachesTheClient(t *testing.T) {
	fixture := newImportAPIFixture(t)
	fixture.app.ImportRunTimeout = 5 * time.Second
	fixture.app.Imports.Fetch = func(ctx context.Context, _ importfetch.Request, _ importfetch.PackConsumer) (*importfetch.Result, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	server := serve(t, fixture.app.Handler())
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
	noErrf(t, err, "run response could not be read after %s", time.Since(started))
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

// A run deadline that passes during admission, before the run is recorded,
// reaches the client as the time limit, never as 500 or 503. Deadlines from
// 1 ms to 30 ms end the run at different admission steps.
func TestImportDeadlineDuringAdmissionReachesTheClientAsTheLimit(t *testing.T) {
	fixture := newImportAPIFixture(t)
	fixture.app.Imports.Fetch = func(ctx context.Context, _ importfetch.Request, _ importfetch.PackConsumer) (*importfetch.Result, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	server := serve(t, fixture.app.Handler())
	for _, timeout := range []time.Duration{time.Millisecond, 3 * time.Millisecond, 10 * time.Millisecond, 30 * time.Millisecond} {
		for round := 0; round < 3; round++ {
			fixture.app.ImportRunTimeout = timeout
			before, _, err := fixture.store.ImportRuns(context.Background(), "slow", 100)
			noErr(t, err)
			response := importAPIRequest(t, http.MethodPost, server.URL+"/api/v1/repositories/slow/import/run", map[string]any{
				"name": "slow", "url": "https://example.invalid/team/slow.git", "mode": "standalone",
			}, "admin-password", "", "")
			content, err := io.ReadAll(response.Body)
			response.Body.Close()
			noErr(t, err)
			var envelope struct {
				OK    bool `json:"ok"`
				Error struct {
					Code string `json:"code"`
				} `json:"error"`
			}
			if err := json.Unmarshal(content, &envelope); err != nil || envelope.OK || envelope.Error.Code != importsync.CodeLimit ||
				response.StatusCode == http.StatusInternalServerError || response.StatusCode == http.StatusServiceUnavailable {
				t.Errorf("%s deadline, round %d: status=%d body=%s err=%v", timeout, round, response.StatusCode, content, err)
				continue
			}
			after, _, err := fixture.store.ImportRuns(context.Background(), "slow", 100)
			noErr(t, err)
			if len(after) != len(before) && (len(after) != len(before)+1 || after[0].ErrorClass != importsync.CodeLimit) {
				t.Errorf("%s deadline, round %d: runs %d before, %d after, last %+v", timeout, round, len(before), len(after), after[0])
			}
		}
	}
}
