package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"owngit/internal/pullrequest"
)

// The API answers for the current pair, says stale for a pair that moved,
// and only answers GET.
func TestPullRequestMergeabilityAPI(t *testing.T) {
	fixture := newAPIFixture(t, false)
	created, err := fixture.app.PullRequests.Create(context.Background(), pullrequest.CreateInput{Repository: "project", Title: "Feature", SourceBranch: "feature", TargetBranch: "main"})
	noErr(t, err)
	server := serve(t, fixture.app.Handler())
	endpoint := server.URL + "/api/v1/repositories/project/pull-requests/" + itoa(created.Number) + "/mergeability"

	clean := getMergeability(t, endpoint, http.StatusOK)
	if clean.Status != pullrequest.MergeabilityClean || clean.Method != "fast_forward" || clean.Source.OID != fixture.sourceOID || clean.Target.OID != fixture.targetOID {
		t.Fatalf("clean answer %+v", clean)
	}
	pinned := endpoint + "?" + url.Values{"source_oid": {fixture.sourceOID}, "target_oid": {fixture.targetOID}}.Encode()
	if same := getMergeability(t, pinned, http.StatusOK); same.Status != pullrequest.MergeabilityClean {
		t.Fatalf("answer for the current pair %+v", same)
	}

	divergeWithConflict(t, fixture)
	if stale := getMergeability(t, pinned, http.StatusOK); stale.Status != pullrequest.MergeabilityStale || stale.Source.OID == fixture.sourceOID {
		t.Fatalf("answer for a pair that moved %+v", stale)
	}
	conflict := getMergeability(t, endpoint, http.StatusOK)
	if conflict.Status != pullrequest.MergeabilityConflict || strings.Join(conflict.ConflictPaths, ",") != "file.txt" {
		t.Fatalf("conflict answer %+v", conflict)
	}

	if response := apiRequest(t, http.MethodPost, endpoint, pullrequest.RevisionInput{}, "", server.URL); response.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("POST status=%d", response.StatusCode)
	}
	if response := apiRequest(t, http.MethodGet, endpoint+"?other=1", nil, "", ""); response.StatusCode != http.StatusBadRequest {
		t.Fatalf("unknown query status=%d", response.StatusCode)
	}
	half := endpoint + "?" + url.Values{"source_oid": {fixture.sourceOID}}.Encode()
	if response := apiRequest(t, http.MethodGet, half, nil, "", ""); response.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("half a pair status=%d", response.StatusCode)
	}
}

// The page offers Check mergeability, shows the answer for the commits it
// shows, and holds the merge control when the answer is a conflict.
func TestBrowserMergeabilityShowsTheAnswerOnThePage(t *testing.T) {
	fixture := newAPIFixture(t, false)
	created, err := fixture.app.PullRequests.Create(context.Background(), pullrequest.CreateInput{Repository: "project", Title: "Feature", SourceBranch: "feature", TargetBranch: "main"})
	noErr(t, err)
	server, client, jar := openBrowser(t, fixture)
	csrf := cookieValue(t, jar, server.URL, generalCookie)
	page := server.URL + pullRequestURL("project", created.Number)
	check := func(sourceOID, targetOID string) browserHTTPResult {
		t.Helper()
		result := browserForm(t, client, page+"/mergeability", url.Values{"csrf": {csrf}, "source_oid": {sourceOID}, "target_oid": {targetOID}}, server.URL)
		if result.status != http.StatusOK || !strings.Contains(result.body, `id="mergeability"`) {
			t.Fatalf("check status=%d", result.status)
		}
		return result
	}

	opened := browserGET(t, client, page)
	if opened.status != http.StatusOK || !strings.Contains(opened.body, `action="/repositories/project/pull-requests/1/mergeability#mergeability"`) ||
		strings.Contains(opened.body, `id="mergeability"`) {
		t.Fatalf("page before asking status=%d", opened.status)
	}
	if clean := check(fixture.sourceOID, fixture.targetOID); !strings.Contains(clean.body, "Can merge: the target branch moves forward") || mergeButtonDisabled(t, clean.body) {
		t.Fatal("clean answer is not shown with the merge control offered")
	}
	if refused := browserForm(t, client, page+"/mergeability", url.Values{"source_oid": {fixture.sourceOID}, "target_oid": {fixture.targetOID}}, server.URL); refused.status != http.StatusForbidden {
		t.Fatalf("check without csrf status=%d", refused.status)
	}

	divergeWithConflict(t, fixture)
	if stale := check(fixture.sourceOID, fixture.targetOID); !strings.Contains(stale.body, "A branch moved after this page was opened") {
		t.Fatal("stale answer is not shown")
	}
	source := apiGitOutput(t, "", "--git-dir", fixture.remote, "rev-parse", "refs/heads/feature")
	target := apiGitOutput(t, "", "--git-dir", fixture.remote, "rev-parse", "refs/heads/main")
	conflict := check(source, target)
	if !strings.Contains(conflict.body, "These files conflict.") || !strings.Contains(conflict.body, ">file.txt</li>") || !mergeButtonDisabled(t, conflict.body) {
		t.Fatal("conflict answer is not shown with the merge control held")
	}
	if reopened := browserGET(t, client, page); strings.Contains(reopened.body, `id="mergeability"`) || mergeButtonDisabled(t, reopened.body) {
		t.Fatal("the answer outlived the page that asked")
	}
}

// divergeWithConflict changes file.txt differently on both branches.
func divergeWithConflict(t *testing.T, fixture apiFixture) {
	t.Helper()
	for _, branch := range []string{"feature", "main"} {
		apiRunGit(t, fixture.work, "checkout", "-q", branch)
		noErr(t, os.WriteFile(filepath.Join(fixture.work, "file.txt"), []byte(branch+"\n"), 0o600))
		apiRunGit(t, fixture.work, "commit", "-qam", branch+" side")
		apiRunGit(t, fixture.work, "push", "-q", "origin", "HEAD:refs/heads/"+branch)
	}
}

func getMergeability(t *testing.T, target string, status int) pullrequest.Mergeability {
	t.Helper()
	response := apiRequest(t, http.MethodGet, target, nil, "", "")
	defer response.Body.Close()
	var answer pullrequest.Mergeability
	noErr(t, json.NewDecoder(response.Body).Decode(&answer))
	if response.StatusCode != status || !answer.OK {
		t.Fatalf("GET %s status=%d answer=%+v", target, response.StatusCode, answer)
	}
	return answer
}

// mergeButtonDisabled reports whether the merge form's button is disabled.
func mergeButtonDisabled(t *testing.T, body string) bool {
	t.Helper()
	_, form, found := strings.Cut(body, `action="/repositories/project/pull-requests/1/merge"`)
	if !found {
		t.Fatal("the page has no merge form")
	}
	button, _, _ := strings.Cut(form[strings.Index(form, "<button"):], ">")
	return strings.Contains(button, "disabled")
}
