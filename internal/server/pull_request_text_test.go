package server

import (
	"context"
	"encoding/json"
	"html"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"owngit/internal/checkapi"
	"owngit/internal/pullrequest"
	"owngit/internal/state"
)

// decodeAPIObject reads a JSON response as a generic object, to see which
// fields it carries.
func decodeAPIObject(t *testing.T, response *http.Response) map[string]any {
	t.Helper()
	defer response.Body.Close()
	content, err := io.ReadAll(response.Body)
	noErr(t, err)
	var object map[string]any
	noErr(t, json.Unmarshal(content, &object))
	return object
}

// renderedText is the first rendered description or note in a page.
func renderedText(t *testing.T, page string) string {
	t.Helper()
	const open = `<article class="md prtext" dir="auto">`
	start := strings.Index(page, open)
	if start < 0 {
		t.Fatalf("the page has no rendered text:\n%s", page)
	}
	end := strings.Index(page[start:], "</article>")
	return page[start : start+end]
}

// Pull request text through the API: a description at the 64 KiB limit fits
// a request even when JSON escapes every byte; an edit names the revision it
// read and a stale one is refused with the current revision; a review note is
// kept with its revisions; each change records general access; and no request
// can name its own actor.
func TestPullRequestTextThroughTheAPI(t *testing.T) {
	fixture := newAPIFixture(t, false)
	server := serve(t, fixture.app.Handler())
	endpoint := server.URL + "/api/v1/repositories/project/pull-requests"
	body := strings.Repeat("<", state.MaximumPullRequestTextBytes)

	claimed := apiRequest(t, http.MethodPost, endpoint, map[string]any{
		"title": "Claimed", "source_branch": "feature", "target_branch": "main", "actor": map[string]string{"kind": "administrator"},
	}, "", "")
	if claimed.StatusCode != http.StatusBadRequest || apiErrorCode(t, claimed) != "invalid_json" {
		t.Fatalf("a request that named its actor status=%d", claimed.StatusCode)
	}
	tooLong := apiRequest(t, http.MethodPost, endpoint, map[string]any{
		"title": "Too long", "source_branch": "feature", "target_branch": "main", "body": body + "<",
	}, "", "")
	if tooLong.StatusCode != http.StatusUnprocessableEntity || apiErrorCode(t, tooLong) != "invalid_body" {
		t.Fatalf("oversized description status=%d", tooLong.StatusCode)
	}

	createdResponse := apiRequest(t, http.MethodPost, endpoint, map[string]any{
		"title": "Described", "source_branch": "feature", "target_branch": "main", "body": body,
	}, "", "")
	if createdResponse.StatusCode != http.StatusOK {
		t.Fatalf("create status=%d error=%q", createdResponse.StatusCode, apiErrorCode(t, createdResponse))
	}
	created := decodeAPISuccess(t, createdResponse)
	if created.PullRequest.Body == nil || *created.PullRequest.Body != body || created.PullRequest.CreatedBy == nil || created.PullRequest.CreatedBy.Kind != state.ActorAccess {
		t.Fatalf("created pull request body=%d bytes created by %v", len(*created.PullRequest.Body), created.PullRequest.CreatedBy)
	}
	number := itoa(created.PullRequest.Number)

	edit := func(revision int64, title string) *http.Response {
		return apiRequest(t, http.MethodPost, endpoint+"/"+number+"/edit", map[string]any{"edit_revision": revision, "title": title}, "", "")
	}
	editedResponse := edit(0, "Edited")
	if editedResponse.StatusCode != http.StatusOK {
		t.Fatalf("edit status=%d error=%q", editedResponse.StatusCode, apiErrorCode(t, editedResponse))
	}
	edited := decodeAPISuccess(t, editedResponse).PullRequest
	if edited.Title != "Edited" || *edited.Body != body || edited.EditRevision != 1 || edited.EditedBy == nil || edited.EditedBy.Kind != state.ActorAccess {
		t.Fatalf("edited pull request=%+v", edited)
	}
	stale := decodeAPIObject(t, edit(0, "Overwrite"))
	details, _ := stale["error"].(map[string]any)
	if details["code"] != "stale_edit" || details["details"].(map[string]any)["current_edit_revision"] != float64(1) {
		t.Fatalf("stale edit answer=%v", stale)
	}

	// Every recorded review names the access that recorded it, with or
	// without a note: a request, a skip and a decision.
	revisions := pullrequest.RevisionInput{SourceOID: fixture.sourceOID, TargetOID: fixture.targetOID}
	for _, step := range []struct {
		path  string
		input any
	}{
		{"/review/request", revisions},
		{"/review/skip", revisions},
		{"/review/submit", pullrequest.ReviewSubmitInput{SourceOID: fixture.sourceOID, TargetOID: fixture.targetOID, Decision: state.ReviewChangesRequested, ReviewerLabel: "tool: api"}},
	} {
		marked := decodeAPISuccess(t, apiRequest(t, http.MethodPost, endpoint+"/"+number+step.path, step.input, "", "")).PullRequest
		if marked.Review.Actor == nil || marked.Review.Actor.Kind != state.ActorAccess || len(marked.ReviewNotes) != 0 {
			t.Fatalf("%s review=%+v notes=%+v", step.path, marked.Review, marked.ReviewNotes)
		}
	}
	reviewed := decodeAPISuccess(t, apiRequest(t, http.MethodPost, endpoint+"/"+number+"/review/submit", pullrequest.ReviewSubmitInput{
		SourceOID: fixture.sourceOID, TargetOID: fixture.targetOID, Decision: state.ReviewApproved, ReviewerLabel: "tool: api", Note: "Checked **both** commits.",
	}, "", "")).PullRequest
	if len(reviewed.ReviewNotes) != 1 || reviewed.ReviewNotes[0].Note != "Checked **both** commits." || !reviewed.ReviewNotes[0].Current ||
		reviewed.ReviewNotes[0].Actor == nil || reviewed.ReviewNotes[0].Actor.Kind != state.ActorAccess {
		t.Fatalf("review notes=%+v", reviewed.ReviewNotes)
	}

	list := decodeAPIObject(t, apiRequest(t, http.MethodGet, endpoint, nil, "", ""))
	item := list["pull_requests"].([]any)[0].(map[string]any)
	if _, hasBody := item["body"]; hasBody || item["edit_revision"] != float64(1) {
		t.Fatalf("list item=%v", item)
	}
	shown := decodeAPIObject(t, apiRequest(t, http.MethodGet, endpoint+"/"+number, nil, "", ""))["pull_request"].(map[string]any)
	if shown["body"] != body || shown["created_by"].(map[string]any)["kind"] != state.ActorAccess {
		t.Fatalf("show carried body=%v created_by=%v", shown["body"] == body, shown["created_by"])
	}
}

// The browser renders a description and a review note as Markdown with no
// raw HTML and no image, keeps what was typed when an edit is refused because
// someone else edited first, and records a review with its note.
func TestBrowserPullRequestTextRendersSafelyAndKeepsDrafts(t *testing.T) {
	fixture := newAPIFixture(t, false)
	server, client, jar := openBrowser(t, fixture)
	csrf := cookieValue(t, jar, server.URL, generalCookie)
	description := "**Bold** point\n\n<script>alert(1)</script>\n\n![outside](https://images.example/a.png) ![inside](picture.png)\n"
	created := browserForm(t, client, server.URL+"/repositories/project/pull-requests", url.Values{
		"csrf": {csrf}, "title": {"Described"}, "body": {description}, "source_branch": {"feature"}, "target_branch": {"main"},
		"source_oid": {fixture.sourceOID}, "target_oid": {fixture.targetOID},
	}, server.URL)
	if created.status != http.StatusSeeOther {
		t.Fatalf("create status=%d", created.status)
	}
	page := server.URL + "/repositories/project/pull-requests/1"
	detail := browserGET(t, client, page)
	rendered := renderedText(t, detail.body)
	if detail.status != http.StatusOK || !strings.Contains(rendered, "<strong>Bold</strong>") || strings.Contains(rendered, "<script") ||
		strings.Contains(rendered, "<img") || !strings.Contains(rendered, `href="https://images.example/a.png"`) {
		t.Fatalf("rendered description status=%d:\n%s", detail.status, rendered)
	}

	editURL := page + "/edit"
	first := browserForm(t, client, editURL, url.Values{"csrf": {csrf}, "edit_revision": {"0"}, "title": {"Described"}, "body": {"Theirs"}}, server.URL)
	if first.status != http.StatusSeeOther || first.header.Get("Location") != "/repositories/project/pull-requests/1?notice=pull_request_edited" {
		t.Fatalf("edit status=%d location=%q", first.status, first.header.Get("Location"))
	}
	stale := browserForm(t, client, editURL, url.Values{"csrf": {csrf}, "edit_revision": {"0"}, "title": {"Mine"}, "body": {"My draft"}}, server.URL)
	if stale.status != http.StatusConflict || !strings.Contains(stale.body, "Someone edited this pull request") ||
		!strings.Contains(stale.body, "My draft</textarea>") || !strings.Contains(stale.body, `value="Mine"`) ||
		!strings.Contains(stale.body, `name="edit_revision" value="1"`) || !strings.Contains(stale.body, "<p>Theirs</p>") {
		t.Fatalf("stale edit status=%d:\n%s", stale.status, stale.body)
	}
	record, _, err := fixture.store.PullRequest(context.Background(), "project", 1)
	noErr(t, err)
	if record.Body != "Theirs" || record.Title != "Described" || record.EditedBy.Kind != state.ActorAccess {
		t.Fatalf("a stale browser edit changed the record: %+v", record)
	}

	missingLabel := browserForm(t, client, page+"/review/submit", url.Values{
		"csrf": {csrf}, "source_oid": {fixture.sourceOID}, "target_oid": {fixture.targetOID}, "decision": {"approved"}, "note": {"Keep me"},
	}, server.URL)
	if missingLabel.status != http.StatusUnprocessableEntity || !strings.Contains(missingLabel.body, "Keep me</textarea>") {
		t.Fatalf("review without a label status=%d", missingLabel.status)
	}
	recorded := browserForm(t, client, page+"/review/submit", url.Values{
		"csrf": {csrf}, "source_oid": {fixture.sourceOID}, "target_oid": {fixture.targetOID}, "decision": {"changes_requested"},
		"reviewer_label": {"Owner"}, "note": {"Please <em>rename</em> `x`."},
	}, server.URL)
	if recorded.status != http.StatusSeeOther || recorded.header.Get("Location") != "/repositories/project/pull-requests/1?notice=review_recorded" {
		t.Fatalf("review status=%d location=%q", recorded.status, recorded.header.Get("Location"))
	}
	reviewed := browserGET(t, client, page)
	if !strings.Contains(reviewed.body, "Review notes") || !strings.Contains(reviewed.body, "<code>x</code>") ||
		strings.Contains(reviewed.body, "<em>rename</em>") || strings.Contains(reviewed.body, "About earlier commits") {
		t.Fatalf("review note page:\n%s", reviewed.body)
	}

	noErr(t, os.WriteFile(fixture.work+"/feature.txt", []byte("moved\n"), 0o600))
	apiRunGit(t, fixture.work, "commit", "-am", "move feature")
	apiRunGit(t, fixture.work, "push", "origin", "HEAD:refs/heads/feature")
	moved := browserGET(t, client, page)
	if !strings.Contains(moved.body, "About earlier commits") || !strings.Contains(html.UnescapeString(moved.body), "Owner") {
		t.Fatalf("a moved branch hid the review note:\n%s", moved.body)
	}

	// A review sent from the page loaded before the branch moved records
	// nothing. The form comes back bound to the new commits with the note and
	// name kept but no result chosen, so approving what was never seen takes
	// a new choice, and the notice says so.
	movedSource := strings.TrimSpace(apiGitOutput(t, fixture.work, "rev-parse", "HEAD"))
	refused := browserForm(t, client, page+"/review/submit", url.Values{
		"csrf": {csrf}, "source_oid": {fixture.sourceOID}, "target_oid": {fixture.targetOID}, "decision": {"approved"},
		"reviewer_label": {"Second reader"}, "note": {"Reviewed the first commit only."},
	}, server.URL)
	form := refused.body[strings.Index(refused.body, `class="form form--wide prreview"`):]
	form = form[:strings.Index(form, "</form>")]
	if refused.status != http.StatusConflict || !strings.Contains(refused.body, "A branch moved before this review was recorded") ||
		!strings.Contains(form, "Reviewed the first commit only.</textarea>") || !strings.Contains(form, `value="Second reader"`) ||
		strings.Contains(form, " checked") || !strings.Contains(form, `name="source_oid" value="`+movedSource+`"`) {
		t.Fatalf("review after a moved branch status=%d:\n%s", refused.status, form)
	}
	notes, _, err := fixture.store.PullRequestReviewNotes(context.Background(), "project", 1, 10)
	noErr(t, err)
	if len(notes) != 1 {
		t.Fatalf("a refused review recorded a note: %+v", notes)
	}
}

// A helper credential that submits a check result still shows that use as its
// last use on its own page, and the pull request's check result still names
// the credential. Pull request changes record general access and keep no
// second copy of a credential's use.
func TestHelperCredentialUseStaysVisibleBesidePullRequestProvenance(t *testing.T) {
	ctx := context.Background()
	fixture := newAPIFixture(t, false)
	server, client, jar := openBrowser(t, fixture)
	credential, token, created, err := fixture.app.issueHelperCredential(ctx, "project", "laptop agent", "")
	noErr(t, err)
	if !created {
		t.Fatal("the helper credential was not created")
	}
	settings, err := fixture.store.Settings(ctx)
	noErr(t, err)
	noErr(t, fixture.store.CreateSession(ctx, "admin-session", "admin", "admin-csrf", settings.AdminSessionVersion, time.Now().Add(time.Hour)))
	parsedServer, _ := url.Parse(server.URL)
	jar.SetCookies(parsedServer, []*http.Cookie{{Name: adminCookie, Value: "admin-session", Path: "/"}})
	helperPage := server.URL + baseHelperCredentialsURL("project")
	if page := browserGET(t, client, helperPage); page.status != http.StatusOK || !strings.Contains(page.body, "Never used") {
		t.Fatalf("unused helper page status=%d", page.status)
	}

	endpoint := server.URL + "/api/v1/repositories/project"
	pullRequest := decodeAPISuccess(t, apiRequest(t, http.MethodPost, endpoint+"/pull-requests", map[string]any{
		"title": "Checked", "source_branch": "feature", "target_branch": "main", "body": "Described",
	}, "", "")).PullRequest
	bearer := header("Authorization", "Bearer "+token)
	taskResponse := sendJSON(t, http.MethodPost, endpoint+"/tasks", checkapi.CreateTaskInput{Title: "Check"}, bearer)
	var task checkapi.TaskResponse
	noErr(t, json.NewDecoder(taskResponse.Body).Decode(&task))
	taskResponse.Body.Close()
	registered := sendJSON(t, http.MethodPost, endpoint+"/tasks/"+task.Task.ID+"/attempts", checkapi.AttemptRegistration{
		AttemptID: strings.Repeat("7", 32), RevisionOID: fixture.sourceOID, WorktreeState: state.WorktreeClean, StartedAt: time.Now().UTC(),
		Checks: []checkapi.CheckDefinition{{Name: "test", Command: "go test ./..."}},
	}, bearer)
	registered.Body.Close()
	exitCode := 0
	completed := sendJSON(t, http.MethodPost, endpoint+"/tasks/"+task.Task.ID+"/attempts/"+strings.Repeat("7", 32)+"/complete", checkapi.AttemptCompletion{
		Results:    []checkapi.Result{{Name: "test", Command: "go test ./...", Status: state.AttemptPassed, ExitCode: &exitCode}},
		FinishedAt: time.Now().UTC(), WorktreeState: state.WorktreeClean,
	}, bearer)
	completed.Body.Close()
	if registered.StatusCode != http.StatusOK || completed.StatusCode != http.StatusOK {
		t.Fatalf("attempt registration status=%d completion status=%d", registered.StatusCode, completed.StatusCode)
	}

	shown := decodeAPISuccess(t, apiRequest(t, http.MethodGet, endpoint+"/pull-requests/"+itoa(pullRequest.Number), nil, "", "")).PullRequest
	if shown.Checks.CredentialID != credential.ID || shown.CreatedBy == nil || *shown.CreatedBy != generalAccessActor {
		t.Fatalf("check credential=%q created by %v", shown.Checks.CredentialID, shown.CreatedBy)
	}
	detail := browserGET(t, client, server.URL+"/repositories/project/pull-requests/"+itoa(pullRequest.Number))
	if !strings.Contains(detail.body, "Reported by the check helper using its own repository credential.") {
		t.Fatal("the pull request page lost the helper provenance of its check result")
	}
	used := browserGET(t, client, helperPage)
	if used.status != http.StatusOK || strings.Contains(used.body, "Never used") || !strings.Contains(used.body, "laptop agent") {
		t.Fatalf("used helper page status=%d still says never used=%v", used.status, strings.Contains(used.body, "Never used"))
	}
	if count, err := fixture.store.TableRowCount(ctx, "helper_credentials"); err != nil || count != 1 {
		t.Fatalf("helper credential rows=%d err=%v", count, err)
	}
}

// A page form larger than OwnGit reads is answered with a page that says so,
// and that what was entered cannot be shown again, instead of a bare error.
// Nothing changes.
func TestBrowserFormOverTheLimitSaysSo(t *testing.T) {
	fixture := newAPIFixture(t, false)
	server, client, jar := openBrowser(t, fixture)
	csrf := cookieValue(t, jar, server.URL, generalCookie)
	created := browserForm(t, client, server.URL+"/repositories/project/pull-requests", url.Values{
		"csrf": {csrf}, "title": {"Described"}, "body": {"Kept"}, "source_branch": {"feature"}, "target_branch": {"main"},
		"source_oid": {fixture.sourceOID}, "target_oid": {fixture.targetOID},
	}, server.URL)
	if created.status != http.StatusSeeOther {
		t.Fatalf("create status=%d", created.status)
	}
	oversized := browserForm(t, client, server.URL+"/repositories/project/pull-requests/1/edit", url.Values{
		"csrf": {csrf}, "edit_revision": {"0"}, "title": {"Described"}, "body": {strings.Repeat("x", maximumForm)},
	}, server.URL)
	if oversized.status != http.StatusRequestEntityTooLarge || !strings.Contains(oversized.body, "This form is larger than 1 MiB") ||
		!strings.Contains(oversized.body, "cannot be shown here again") {
		t.Fatalf("oversized form status=%d:\n%s", oversized.status, oversized.body)
	}
	record, _, err := fixture.store.PullRequest(context.Background(), "project", 1)
	noErr(t, err)
	if record.Body != "Kept" || record.EditRevision != 0 {
		t.Fatalf("an oversized form changed the record: %+v", record)
	}
}

// directionControls are the Unicode direction controls, the ones a title or
// note could use to reorder what is shown around it.
const directionControls = "\u061c\u200e\u200f\u202a\u202b\u202c\u202d\u202e\u2066\u2067\u2068\u2069"

// Direction controls in pull request text reach API clients as \uXXXX
// escapes, in a result and in a list, so they cannot reorder the fields
// after them where the JSON is shown; decoded, the text is unchanged.
func TestAPIEscapesDirectionControls(t *testing.T) {
	fixture := newAPIFixture(t, false)
	server := serve(t, fixture.app.Handler())
	endpoint := server.URL + "/api/v1/repositories/project/pull-requests"
	title := "Fix " + directionControls + " done"
	created := apiRequest(t, http.MethodPost, endpoint, map[string]any{
		"title": title, "body": "a\u202eb", "source_branch": "feature", "target_branch": "main",
	}, "", "")
	for name, response := range map[string]*http.Response{
		"create": created,
		"list":   apiRequest(t, http.MethodGet, endpoint, nil, "", ""),
	} {
		content, err := io.ReadAll(response.Body)
		response.Body.Close()
		noErr(t, err)
		if response.StatusCode != http.StatusOK || strings.ContainsAny(string(content), directionControls) || !strings.Contains(string(content), `Fix \u061c\u200e`) {
			t.Fatalf("%s status=%d: %s", name, response.StatusCode, content)
		}
		var decoded struct {
			PullRequest  *pullrequest.View  `json:"pull_request"`
			PullRequests []pullrequest.View `json:"pull_requests"`
		}
		noErr(t, json.Unmarshal(content, &decoded))
		if decoded.PullRequest == nil && len(decoded.PullRequests) == 1 {
			decoded.PullRequest = &decoded.PullRequests[0]
		}
		if decoded.PullRequest == nil || decoded.PullRequest.Title != title {
			t.Fatalf("%s decoded to %+v", name, decoded)
		}
	}
}
