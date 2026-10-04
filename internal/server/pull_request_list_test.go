package server

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"owngit/internal/webui"
)

// A repository with more than a thousand pull requests in every state lists
// instead of refusing: the API answers newest first with the continuation,
// the page shows the open ones with a link to the next page and to the other
// states, and a parameter that cannot be honored is a 400.
func TestPullRequestListPagesPastAThousandRequests(t *testing.T) {
	fixture := newAPIFixture(t, false)
	noErr(t, fixture.store.Exec(context.Background(), `WITH RECURSIVE n(i) AS (SELECT 1 UNION ALL SELECT i+1 FROM n WHERE i<1100)
		INSERT INTO pull_requests(repository_id,number,title,source_branch,target_branch,status,created_at,updated_at)
		SELECT 'project',i,'Request ' || i,'feature','main',CASE WHEN i%2=0 THEN 'closed' ELSE 'open' END,1,1 FROM n`))
	server, client, _ := openBrowser(t, fixture)
	list := server.URL + "/api/v1/repositories/project/pull-requests"

	var first struct {
		Items []struct {
			Number int64 `json:"number"`
		} `json:"pull_requests"`
		Next int64 `json:"next"`
	}
	if status := decodeAPI(t, apiRequest(t, http.MethodGet, list, nil, "", ""), &first); status != http.StatusOK ||
		len(first.Items) != 50 || first.Items[0].Number != 1100 || first.Next != 1051 {
		t.Fatalf("first page: status %d, %d items, newest %v, next %d; want 200, 50 items from 1100, next 1051", status, len(first.Items), first.Items, first.Next)
	}
	var last struct {
		Items []struct{ Number int64 } `json:"pull_requests"`
		Next  *int64                   `json:"next"`
	}
	if status := decodeAPI(t, apiRequest(t, http.MethodGet, list+"?state=open&limit=100&before=4", nil, "", ""), &last); status != http.StatusOK ||
		len(last.Items) != 2 || last.Items[0].Number != 3 || last.Next != nil {
		t.Fatalf("last open page: status %d, %+v, next %v; want 200, numbers 3 and 1, no next", status, last.Items, last.Next)
	}
	for _, query := range []string{"?state=draft", "?limit=0", "?limit=101", "?limit=x", "?before=0", "?before=x"} {
		if response := apiRequest(t, http.MethodGet, list+query, nil, "", ""); response.StatusCode != http.StatusBadRequest {
			t.Errorf("%s answered %d, want 400", query, response.StatusCode)
		}
	}

	page := browserGET(t, client, server.URL+"/repositories/project/pull-requests")
	if page.status != http.StatusOK || strings.Contains(page.body, "Pull request records could not be read") ||
		!strings.Contains(page.body, "Request 1099") || strings.Contains(page.body, "Request 1100") ||
		!strings.Contains(page.body, webui.Text(webui.LangEN, webui.MsgPRListMore)) ||
		!strings.Contains(page.body, "before=1001") || !strings.Contains(page.body, "?state=closed") {
		t.Errorf("open page: status %d, want 200 with the newest open request, a More link and the other states", page.status)
	}
	closed := browserGET(t, client, server.URL+"/repositories/project/pull-requests?state=closed&before=1100")
	if closed.status != http.StatusOK || !strings.Contains(closed.body, "Request 1098") || strings.Contains(closed.body, "Request 1099") {
		t.Errorf("closed page below 1100: status %d, want the closed requests only", closed.status)
	}
	if bad := browserGET(t, client, server.URL+"/repositories/project/pull-requests?state=draft"); bad.status != http.StatusBadRequest {
		t.Errorf("unknown state answered %d, want 400", bad.status)
	}
}
