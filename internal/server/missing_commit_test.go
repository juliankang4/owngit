package server

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"owngit/internal/state"
)

// A check record can name a commit that its repository no longer has, for
// example after a restore of a backup made once the commit was overwritten
// with kept history off. Its pages still show the record with the commit ID.
func TestCheckRecordOfAMissingCommitStillShows(t *testing.T) {
	fixture := newAPIFixture(t, false)
	ctx := context.Background()
	task, err := fixture.store.CreateTask(ctx, "project", "Overwritten work", time.Now().UTC().Add(-time.Minute))
	noErr(t, err)
	missing := strings.Repeat("e", 40)
	recordBrowserAttempt(t, fixture.store, task.ID, missing, strings.Repeat("a", 32), state.WorktreeClean, "", true)

	server := serve(t, fixture.app.Handler())
	client, _ := newBrowserClient(t)
	if result := browserGET(t, client, server.URL+"/repositories/project"); result.status != http.StatusOK {
		t.Fatalf("repository status=%d", result.status)
	}
	for _, page := range []string{tasksURL("project", ""), tasksURL("project", task.ID)} {
		result := browserGET(t, client, server.URL+page)
		if result.status != http.StatusOK || !strings.Contains(result.body, shortOID(missing)) {
			t.Fatalf("%s status=%d shows commit=%v", page, result.status, strings.Contains(result.body, shortOID(missing)))
		}
	}
}
