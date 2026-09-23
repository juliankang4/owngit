package server

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"owngit/internal/state"
)

// The Tasks page reads only the attempts it displays: the latest attempt per
// list row and the newest maximumBrowserTaskAttempts of an opened task. Older
// attempts are made unreadable on purpose, so a page that still loads every
// attempt of the repository fails instead of rendering.
func TestTasksPageReadsOnlyDisplayedAttempts(t *testing.T) {
	fixture := newAPIFixture(t, false)
	ctx := context.Background()
	long, err := fixture.store.CreateTask(ctx, "project", "Long history", time.Now().UTC().Add(-time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	other, err := fixture.store.CreateTask(ctx, "project", "Other task", time.Now().UTC().Add(-time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	attemptID := func(task, index int) string { return fmt.Sprintf("%012x%020x", task*1000+index, 0) }
	for index := 0; index <= maximumBrowserTaskAttempts; index++ {
		recordBrowserAttempt(t, fixture.store, long.ID, fixture.sourceOID, attemptID(1, index), state.WorktreeClean, "", true)
	}
	for index := 0; index < 2; index++ {
		recordBrowserAttempt(t, fixture.store, other.ID, fixture.sourceOID, attemptID(2, index), state.WorktreeClean, "", true)
	}
	for _, hidden := range []string{attemptID(1, 0), attemptID(2, 0)} {
		if err := fixture.store.Exec(ctx, `UPDATE check_results SET exit_code='unreadable' WHERE attempt_id=?`, hidden); err != nil {
			t.Fatal(err)
		}
	}

	server := httptest.NewServer(fixture.app.Handler())
	defer server.Close()
	client, _ := newBrowserClient(t)
	if result := browserGET(t, client, server.URL+"/repositories/project"); result.status != http.StatusOK {
		t.Fatalf("repository status=%d", result.status)
	}
	list := browserGET(t, client, server.URL+tasksURL("project", ""))
	if list.status != http.StatusOK || !strings.Contains(list.body, "Long history") || !strings.Contains(list.body, "Other task") {
		t.Fatalf("task list status=%d", list.status)
	}
	detail := browserGET(t, client, server.URL+tasksURL("project", long.ID))
	newest := shortOpaqueID(attemptID(1, maximumBrowserTaskAttempts))
	if detail.status != http.StatusOK || !strings.Contains(detail.body, "Only the most recent runs are shown.") ||
		!strings.Contains(detail.body, newest) || strings.Count(detail.body, "class=\"attempt\"") > maximumBrowserTaskAttempts {
		t.Fatalf("task detail status=%d newest=%v", detail.status, strings.Contains(detail.body, newest))
	}
	if missing := browserGET(t, client, server.URL+tasksURL("project", strings.Repeat("f", 32))); missing.status != http.StatusNotFound || !strings.Contains(missing.body, "Long history") {
		t.Fatalf("missing task status=%d", missing.status)
	}
}
