package server

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"net/http"
	"net/url"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"owngit/internal/state"
	"owngit/internal/webui"
)

// decodeAPI reads a JSON response into value and returns its status.
func decodeAPI(t *testing.T, response *http.Response, value any) int {
	t.Helper()
	defer response.Body.Close()
	noErrf(t, json.NewDecoder(response.Body).Decode(value), "decode API response")
	return response.StatusCode
}

// importCommits adds count commits on main to the bare repository at
// remote, one second apart from start, with subjects "commit 0000" and up.
func importCommits(t *testing.T, remote string, start time.Time, count int) {
	t.Helper()
	var stream strings.Builder
	for index := 1; index <= count; index++ {
		subject := fmt.Sprintf("commit %04d", index-1)
		when := strconv.FormatInt(start.Add(time.Duration(index)*time.Second).Unix(), 10)
		fmt.Fprintf(&stream, "commit refs/heads/main\nmark :%d\nauthor Synthetic <synthetic@example.invalid> %s +0000\ncommitter Synthetic <synthetic@example.invalid> %s +0000\ndata %d\n%s\n", index, when, when, len(subject), subject)
		if index > 1 {
			fmt.Fprintf(&stream, "from :%d\n", index-1)
		}
		stream.WriteString("\n")
	}
	command := exec.Command("git", "--git-dir", remote, "fast-import", "--quiet")
	command.Stdin = strings.NewReader(stream.String())
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("fast-import: %v\n%s", err, output)
	}
}

// completeActivity reads the activity API until every repository was
// counted.
func completeActivity(t *testing.T, target string) activityResponse {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		var activity activityResponse
		if status := decodeAPI(t, sendJSON(t, http.MethodGet, target, nil), &activity); status != http.StatusOK {
			t.Fatalf("GET %s status=%d", target, status)
		}
		if activity.IncompleteReason != "counting" || time.Now().After(deadline) {
			return activity
		}
		time.Sleep(200 * time.Millisecond)
	}
}

var activitySubject = regexp.MustCompile(`<span class="row__msg" dir="auto">([^<]*)</span>`)

func TestActivityPageAndAPIListTheSameNewestCommits(t *testing.T) {
	app := newConfiguredApp(t)
	_, err := app.Repositories.Create(context.Background(), "many", "")
	noErr(t, err)
	remote, err := app.Repositories.Path("many")
	noErr(t, err)
	year := time.Now().Year()
	start := time.Date(year, time.January, 2, 12, 0, 0, 0, time.UTC)
	importCommits(t, remote, start, maximumActivityEntries+1)
	server := serve(t, app.Handler())
	client, _ := newBrowserClient(t)

	for _, query := range []string{"year=" + strconv.Itoa(year), "date=" + start.Format("2006-01-02")} {
		activity := completeActivity(t, server.URL+activityAPIPath+"?"+query)
		if !activity.OK || !activity.Complete || activity.Total != maximumActivityEntries+1 || !activity.Truncated || len(activity.Entries) != maximumActivityEntries {
			t.Fatalf("%s: complete=%v total=%d truncated=%v entries=%d", query, activity.Complete, activity.Total, activity.Truncated, len(activity.Entries))
		}
		if len(activity.Days) != 1 || activity.Days[0].Count != maximumActivityEntries+1 || activity.Entries[0].Subject != "commit 1000" || activity.Entries[0].AuthorName != "Synthetic" {
			t.Fatalf("%s: days=%v first=%+v", query, activity.Days, activity.Entries[0])
		}
		page := browserGET(t, client, server.URL+"/activity?"+query)
		var listed []string
		for _, match := range activitySubject.FindAllStringSubmatch(page.body, -1) {
			listed = append(listed, match[1])
		}
		if page.status != http.StatusOK || len(listed) != len(activity.Entries) {
			t.Fatalf("%s: page status=%d rows=%d", query, page.status, len(listed))
		}
		for index, entry := range activity.Entries {
			if listed[index] != entry.Subject {
				t.Fatalf("%s: row %d is %q on the page and %q in the API", query, index, listed[index], entry.Subject)
			}
		}
		note := "Only the newest 1,000 commits are shown. Choose a date to see others."
		if strings.HasPrefix(query, "date=") {
			note = "Only the newest 1,000 commits of this day are shown."
		}
		if !strings.Contains(page.body, note) {
			t.Fatalf("%s: page does not say the list was cut", query)
		}
	}

	if status, code := checkStatus(t, sendJSON(t, http.MethodGet, server.URL+activityAPIPath+"?year=10000", nil)); status != http.StatusBadRequest || code != "invalid_request" {
		t.Fatalf("invalid year status=%d code=%s", status, code)
	}
	if status, code := checkStatus(t, sendJSON(t, http.MethodGet, server.URL+activityAPIPath+"?year=2020&date="+start.Format("2006-01-02"), nil)); status != http.StatusBadRequest || code != "invalid_request" {
		t.Fatalf("date outside year status=%d code=%s", status, code)
	}
}

// breakActivity adds a branch whose history cannot be walked, so counting
// the repository fails.
func breakActivity(t *testing.T, remote string) {
	t.Helper()
	treeOID := apiGitOutput(t, "", "--git-dir", remote, "rev-parse", "refs/heads/main^{tree}")
	commitObject := "tree " + treeOID + "\nparent " + strings.Repeat("1", 40) + "\nauthor Test <test@example.invalid> 1704067200 +0000\ncommitter Test <test@example.invalid> 1704067200 +0000\n\nbroken parent\n"
	command := exec.Command("git", "--git-dir", remote, "hash-object", "-t", "commit", "-w", "--stdin")
	command.Stdin = strings.NewReader(commitObject)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("create broken commit object: %v\n%s", err, output)
	}
	apiRunGit(t, "", "--git-dir", remote, "update-ref", "refs/heads/broken", strings.TrimSpace(string(output)))
}

func TestActivityAPIKeepsNoCommitsApartFromUnreadable(t *testing.T) {
	app := newConfiguredApp(t)
	server := serve(t, app.Handler())

	empty := completeActivity(t, server.URL+activityAPIPath)
	if !empty.OK || !empty.Complete || empty.Total != 0 || empty.RepositoryCount != 0 || len(empty.Unreadable) != 0 {
		t.Fatalf("no repositories: %+v", empty)
	}

	broken, _ := addActivityRepository(t, app, "broken", 1)
	breakActivity(t, broken)
	if status, code := checkStatus(t, sendJSON(t, http.MethodGet, server.URL+activityAPIPath, nil)); status != http.StatusServiceUnavailable || code != "activity_unavailable" {
		t.Fatalf("only an unreadable repository: status=%d code=%s", status, code)
	}

	addActivityRepository(t, app, "readable", 1)
	partial := completeActivity(t, server.URL+activityAPIPath)
	if !partial.OK || partial.Complete || partial.IncompleteReason != "unreadable" || len(partial.Unreadable) != 1 || partial.Unreadable[0] != "broken" ||
		partial.Total != 1 || len(partial.Entries) != 1 || partial.Entries[0].Repository != "readable" {
		t.Fatalf("one unreadable repository: %+v", partial)
	}
}

var taskTitle = regexp.MustCompile(`<span class="taskrow__t"(?: dir="auto")?>(?:<span class="ev__sub" dir="auto">[^<]*</span> <span dir="auto">)?([^<]*)<`)

func pageTaskTitles(body string) []string {
	var titles []string
	for _, match := range taskTitle.FindAllStringSubmatch(body, -1) {
		titles = append(titles, match[1])
	}
	return titles
}

func apiTaskTitles(tasks []taskViewJSON) []string {
	var titles []string
	for _, task := range tasks {
		titles = append(titles, task.Title)
	}
	return titles
}

func TestTaskViewsMatchTheTasksAndCodingToolsPages(t *testing.T) {
	fixture := newAPIFixture(t, false)
	ctx := context.Background()
	base := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
	var tasks []state.Task
	for index := 0; index <= maximumRecentTasks; index++ {
		task, err := fixture.store.CreateTask(ctx, "project", fmt.Sprintf("Task %02d", index), base.Add(time.Duration(index)*time.Minute))
		noErr(t, err)
		tasks = append(tasks, task)
	}
	// The oldest task gets the newest attempt, so the Tasks page lists it
	// first, while the recent list orders by update time.
	recordBrowserAttempt(t, fixture.store, tasks[0].ID, fixture.sourceOID, fmt.Sprintf("%032x", 1), state.WorktreeClean, "", true)
	server := serve(t, fixture.app.Handler())
	client, _ := newBrowserClient(t)

	var listed taskViewListResponse
	if status := decodeAPI(t, sendJSON(t, http.MethodGet, server.URL+taskViewAPIPath+"/project", nil), &listed); status != http.StatusOK || listed.Truncated {
		t.Fatalf("repository tasks status=%d truncated=%v", status, listed.Truncated)
	}
	page := browserGET(t, client, server.URL+tasksURL("project", ""))
	if want, got := apiTaskTitles(listed.Tasks), pageTaskTitles(page.body); page.status != http.StatusOK || strings.Join(want, ",") != strings.Join(got, ",") || want[0] != "Task 00" {
		t.Fatalf("Tasks page lists %v, the API %v", got, want)
	}
	if listed.Tasks[0].LatestAttempt == nil || listed.Tasks[0].LatestAttempt.Status != state.AttemptPassed || listed.Tasks[1].LatestAttempt != nil {
		t.Fatalf("latest attempts: %+v %+v", listed.Tasks[0].LatestAttempt, listed.Tasks[1].LatestAttempt)
	}

	var recent taskViewListResponse
	if status := decodeAPI(t, sendJSON(t, http.MethodGet, server.URL+taskViewAPIPath, nil), &recent); status != http.StatusOK || !recent.Truncated || len(recent.Tasks) != maximumRecentTasks {
		t.Fatalf("recent tasks status=%d truncated=%v count=%d", status, recent.Truncated, len(recent.Tasks))
	}
	coding := browserGET(t, client, server.URL+codingToolsPath)
	if want, got := apiTaskTitles(recent.Tasks), pageTaskTitles(coding.body); coding.status != http.StatusOK || strings.Join(want, ",") != strings.Join(got, ",") ||
		!strings.Contains(coding.body, "Only the 10 most recently updated tasks are shown.") {
		t.Fatalf("Coding tools page lists %v, the API %v", got, want)
	}

	var detail taskDetailResponse
	if status := decodeAPI(t, sendJSON(t, http.MethodGet, server.URL+taskViewAPIPath+"/project/"+tasks[0].ID, nil), &detail); status != http.StatusOK ||
		detail.Task.ID != tasks[0].ID || len(detail.Attempts) != 1 || detail.AttemptsTruncated {
		t.Fatalf("task detail status=%d %+v", status, detail)
	}
	for target, want := range map[string]string{
		"/project/" + strings.Repeat("f", 32): "task_not_found",
		"/missing":                            "repository_not_found",
		"/project/x/y":                        "not_found",
	} {
		if status, code := checkStatus(t, sendJSON(t, http.MethodGet, server.URL+taskViewAPIPath+target, nil)); status != http.StatusNotFound || code != want {
			t.Fatalf("%s status=%d code=%s", target, status, code)
		}
	}
}

func TestReadViewsNeedGeneralAccess(t *testing.T) {
	fixture := newAPIFixture(t, true)
	server := serve(t, fixture.app.Handler())
	for _, path := range []string{activityAPIPath, taskViewAPIPath, taskViewAPIPath + "/project"} {
		if status, code := checkStatus(t, sendJSON(t, http.MethodGet, server.URL+path, nil)); status != http.StatusUnauthorized || code != "authentication_required" {
			t.Fatalf("%s without the password: status=%d code=%s", path, status, code)
		}
		response := sendJSON(t, http.MethodGet, server.URL+path, nil, basicAuth("owngit", "shared-password"))
		response.Body.Close()
		if response.StatusCode != http.StatusOK {
			t.Fatalf("%s with the password: status=%d", path, response.StatusCode)
		}
	}
	client, _ := newBrowserClient(t)
	if page := browserGET(t, client, server.URL+codingToolsPath); page.status != http.StatusSeeOther {
		t.Fatalf("Coding tools page without the password: status=%d", page.status)
	}
}

func TestHelperCredentialLastUseShowsOnCodingToolsAndInTheAPI(t *testing.T) {
	fixture := newAPIFixture(t, false)
	credential, token, _, err := fixture.app.issueHelperCredential(context.Background(), "project", "laptop helper", "")
	noErr(t, err)
	server := serve(t, fixture.app.Handler())
	client, jar := newBrowserClient(t)

	// Without administrator confirmation the page neither reads nor shows
	// the credentials.
	page := browserGET(t, client, server.URL+codingToolsPath)
	if page.status != http.StatusOK || strings.Contains(page.body, "laptop helper") || !strings.Contains(page.body, "Helper credentials are shown to the administrator.") {
		t.Fatalf("Coding tools page for a general viewer: status=%d", page.status)
	}
	if status, _ := checkStatus(t, sendJSON(t, http.MethodGet, server.URL+allHelperCredentialsAPIPath, nil)); status != http.StatusUnauthorized {
		t.Fatalf("credential list without the administrator password: status=%d", status)
	}

	// read returns the last use the API reports, as text, and the page.
	read := func() (string, string) {
		var listed struct {
			Credentials []struct {
				ID         string     `json:"id"`
				Label      string     `json:"label"`
				LastUsedAt *time.Time `json:"last_used_at"`
			} `json:"credentials"`
		}
		if status := decodeAPI(t, adminAPIRequest(t, http.MethodGet, server.URL+allHelperCredentialsAPIPath, nil, "admin-password"), &listed); status != http.StatusOK ||
			len(listed.Credentials) != 1 || listed.Credentials[0].ID != credential.ID || listed.Credentials[0].Label != "laptop helper" {
			t.Fatalf("credential list status=%d %+v", status, listed)
		}
		used := "never"
		if listed.Credentials[0].LastUsedAt != nil {
			used = listed.Credentials[0].LastUsedAt.Format(time.RFC3339)
		}
		return used, browserGET(t, client, server.URL+codingToolsPath).body
	}

	signInAdmin(t, fixture, server.URL, jar)
	used, body := read()
	if used != "never" || !strings.Contains(body, "laptop helper") || !strings.Contains(body, "Never used") {
		t.Fatalf("before use: API last use %s, page shows the label %v", used, strings.Contains(body, "laptop helper"))
	}
	if strings.Contains(body, token) {
		t.Fatal("the Coding tools page shows a token")
	}

	response := sendJSON(t, http.MethodGet, server.URL+"/api/v1/repositories/project/tasks", nil, header("Authorization", "Bearer "+token))
	response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("helper use status=%d", response.StatusCode)
	}
	used, body = read()
	if used == "never" || strings.Contains(body, "Never used") {
		t.Fatalf("after use: API last use %s, page still says never used %v", used, strings.Contains(body, "Never used"))
	}
}

func TestCodingToolsCommandFitsTheServer(t *testing.T) {
	for _, test := range []struct {
		origin              string
		plainHTTP, password bool
		want                string
	}{
		{"https://owngit.example.test", false, false, "owngit mcp --server https://owngit.example.test"},
		{"http://127.0.0.1:7654", true, true, "owngit mcp --server http://127.0.0.1:7654 --accept-insecure-http --password-file PASSWORD_FILE"},
		{"http://[::1]:7654", true, false, "owngit mcp --server 'http://[::1]:7654' --accept-insecure-http"},
	} {
		if got := mcpCommandFor(test.origin, test.plainHTTP, test.password); got != test.want {
			t.Errorf("%s: %q, want %q", test.origin, got, test.want)
		}
	}
}

// taskMoreLink finds the "Show older tasks" link the Tasks page renders. The
// link is a plain anchor, so following it needs no script.
var taskMoreLink = regexp.MustCompile(`href="([^"]+)" rel="next"`)

func taskMoreURL(t *testing.T, body string) string {
	t.Helper()
	match := taskMoreLink.FindStringSubmatch(body)
	if match == nil {
		t.Fatalf("the Tasks page has no older-tasks link")
	}
	return html.UnescapeString(match[1])
}

// recordTaskAttemptAt records one completed attempt under the server time the
// task list order reads.
func recordTaskAttemptAt(t *testing.T, store *state.Store, sourceOID, taskID, attemptID string, at time.Time) {
	t.Helper()
	ctx := context.Background()
	attempt := state.CheckAttempt{
		ID: attemptID, TaskID: taskID, RepositoryID: "project", RevisionOID: sourceOID,
		WorktreeState: state.WorktreeClean, StartedAt: at, CreatedAt: at,
		Checks: []state.CheckDefinition{{Name: "test", Command: "go test ./..."}},
	}
	_, _, err := store.RegisterCheckAttempt(ctx, attempt)
	noErr(t, err)
	exitCode := 0
	_, _, err = store.CompleteCheckAttempt(ctx, state.CheckCompletion{
		AttemptID: attemptID, RepositoryID: "project", TaskID: taskID, FinishedAt: at.Add(time.Second),
		WorktreeState: state.WorktreeClean, Log: "synthetic test output",
		Results: []state.CheckResult{{
			Name: "test", Command: "go test ./...", Status: state.AttemptPassed,
			ExitCode: &exitCode, DurationMS: 50, OutputExcerpt: "ok",
		}},
	}, at)
	noErr(t, err)
}

// corruptAttemptResult makes one stored result unreadable.
func corruptAttemptResult(t *testing.T, store *state.Store, attemptID string) {
	t.Helper()
	noErr(t, store.Exec(context.Background(), `UPDATE check_results SET exit_code='unreadable' WHERE attempt_id=?`, attemptID))
}

// The repository tasks page and its API read one bounded page, newest first,
// and hand back a continuation for the older tasks.
func TestTaskListPagesReadAndContinueOnePage(t *testing.T) {
	fixture := newAPIFixture(t, false)
	ctx := context.Background()
	base := time.Now().UTC().Add(-2 * time.Hour).Truncate(time.Second)
	count := defaultTaskPageSize + 10
	for index := 0; index < count; index++ {
		_, err := fixture.store.CreateTask(ctx, "project", fmt.Sprintf("Task %02d", index), base.Add(time.Duration(index)*time.Minute))
		noErr(t, err)
	}
	server := serve(t, fixture.app.Handler())

	var first taskViewListResponse
	if status := decodeAPI(t, sendJSON(t, http.MethodGet, server.URL+taskViewAPIPath+"/project", nil), &first); status != http.StatusOK {
		t.Fatalf("first task page status=%d", status)
	}
	if len(first.Tasks) != defaultTaskPageSize || first.Next == "" || first.Tasks[0].Title != fmt.Sprintf("Task %02d", count-1) {
		t.Fatalf("first task page count=%d next=%q first=%q", len(first.Tasks), first.Next, first.Tasks[0].Title)
	}
	var rest taskViewListResponse
	target := server.URL + taskViewAPIPath + "/project?before=" + url.QueryEscape(first.Next)
	if status := decodeAPI(t, sendJSON(t, http.MethodGet, target, nil), &rest); status != http.StatusOK {
		t.Fatalf("continued task page status=%d", status)
	}
	if rest.Next != "" || len(rest.Tasks) != count-defaultTaskPageSize {
		t.Fatalf("last task page count=%d next=%q", len(rest.Tasks), rest.Next)
	}
	seen := make(map[string]bool, count)
	for _, view := range append(first.Tasks, rest.Tasks...) {
		if seen[view.ID] {
			t.Fatalf("task %s appeared on two pages", view.ID)
		}
		seen[view.ID] = true
	}
	if len(seen) != count {
		t.Fatalf("pages covered %d tasks, want %d", len(seen), count)
	}
	// The largest allowed page covers the repository in one answer.
	var whole taskViewListResponse
	if status := decodeAPI(t, sendJSON(t, http.MethodGet, server.URL+taskViewAPIPath+"/project?limit=100", nil), &whole); status != http.StatusOK || whole.Next != "" || len(whole.Tasks) != count {
		t.Fatalf("full page status=%d count=%d next=%q", status, len(whole.Tasks), whole.Next)
	}

	// The browser list is the same page with a plain link to the next one.
	client, _ := newBrowserClient(t)
	page := browserGET(t, client, server.URL+tasksURL("project", ""))
	listed := pageTaskTitles(page.body)
	if page.status != http.StatusOK || len(listed) != defaultTaskPageSize || listed[0] != fmt.Sprintf("Task %02d", count-1) {
		t.Fatalf("Tasks page status=%d rows=%d first=%q", page.status, len(listed), listed[0])
	}
	// A page of a longer list is not the count: only the whole list states
	// its size.
	if strings.Contains(page.body, fmt.Sprintf("%d items", defaultTaskPageSize)) {
		t.Fatalf("Tasks page states one page's size as the list count")
	}
	// The largest page is the whole list, so it states the repository's count.
	wholeList := browserGET(t, client, server.URL+tasksURL("project", "")+"?limit=100")
	if wholeList.status != http.StatusOK || !strings.Contains(wholeList.body, fmt.Sprintf("%d items", count)) {
		t.Fatalf("whole Tasks page status=%d, want the repository's count", wholeList.status)
	}
	// A continuation below every task, as after those tasks were removed,
	// says no older tasks remain rather than claiming the repository has none.
	stale := browserGET(t, client, server.URL+tasksURL("project", "")+"?before="+url.QueryEscape("0:0:00000000000000000000000000000000"))
	if stale.status != http.StatusOK || !strings.Contains(stale.body, enText(webui.MsgTasksEmptyOlder)) || strings.Contains(stale.body, enText(webui.MsgTasksEmpty)) {
		t.Fatalf("empty continued Tasks page status=%d, want the older-tasks wording", stale.status)
	}
	older := browserGET(t, client, server.URL+taskMoreURL(t, page.body))
	olderTitles := pageTaskTitles(older.body)
	if older.status != http.StatusOK || len(olderTitles) != count-defaultTaskPageSize || taskMoreLink.MatchString(older.body) {
		t.Fatalf("older page status=%d rows=%d", older.status, len(olderTitles))
	}
	covered := make(map[string]bool, count)
	for _, title := range append(listed, olderTitles...) {
		if covered[title] {
			t.Fatalf("task %q appeared on two pages", title)
		}
		covered[title] = true
	}
	if len(covered) != count {
		t.Fatalf("the Tasks page covered %d tasks, want %d", len(covered), count)
	}

	// A page parameter that cannot be read is refused, not guessed.
	for _, item := range []struct {
		query string
		code  string
	}{
		{"?limit=0", "invalid_list_limit"},
		{"?limit=101", "invalid_list_limit"},
		{"?limit=half", "invalid_list_limit"},
		{"?before=half", "invalid_list_before"},
		{"?before=1:2:", "invalid_list_before"},
		{"?limit=1&limit=2", "invalid_request"},
		{"?unknown=1", "invalid_request"},
	} {
		if status, code := checkStatus(t, sendJSON(t, http.MethodGet, server.URL+taskViewAPIPath+"/project"+item.query, nil)); status != http.StatusBadRequest || code != item.code {
			t.Fatalf("API %s status=%d code=%s", item.query, status, code)
		}
	}
	if bad := browserGET(t, client, server.URL+tasksURL("project", "")+"?before=half"); bad.status != http.StatusBadRequest {
		t.Fatalf("Tasks page with a bad continuation status=%d", bad.status)
	}
}

// The recent task list reads the tasks it shows only. An unreadable result of
// an older task outside the shown page no longer fails the list, while an
// unreadable result of a shown task still fails rather than shortening it.
func TestRecentTasksIgnoreUnreadableAttemptsOutsideTheShownPage(t *testing.T) {
	fixture := newAPIFixture(t, false)
	ctx := context.Background()
	base := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
	tasks := make([]state.Task, 0, maximumRecentTasks+1)
	for index := 0; index <= maximumRecentTasks; index++ {
		task, err := fixture.store.CreateTask(ctx, "project", fmt.Sprintf("Task %02d", index), base.Add(time.Duration(index)*time.Minute))
		noErr(t, err)
		tasks = append(tasks, task)
	}
	excluded := fmt.Sprintf("%032x", 1)
	recordTaskAttemptAt(t, fixture.store, fixture.sourceOID, tasks[0].ID, excluded, base)
	corruptAttemptResult(t, fixture.store, excluded)

	server := serve(t, fixture.app.Handler())
	client, _ := newBrowserClient(t)
	var recent taskViewListResponse
	if status := decodeAPI(t, sendJSON(t, http.MethodGet, server.URL+taskViewAPIPath, nil), &recent); status != http.StatusOK ||
		!recent.Truncated || len(recent.Tasks) != maximumRecentTasks {
		t.Fatalf("recent tasks status=%d count=%d truncated=%v", status, len(recent.Tasks), recent.Truncated)
	}
	for _, view := range recent.Tasks {
		if view.ID == tasks[0].ID {
			t.Fatalf("the unreadable older task was shown")
		}
	}
	if page := browserGET(t, client, server.URL+codingToolsPath); page.status != http.StatusOK || !strings.Contains(page.body, fmt.Sprintf("Task %02d", maximumRecentTasks)) {
		t.Fatalf("Coding tools with an unreadable older task status=%d", page.status)
	}

	// A result inside the shown page still fails the whole answer.
	shown := fmt.Sprintf("%032x", 2)
	recordTaskAttemptAt(t, fixture.store, fixture.sourceOID, tasks[maximumRecentTasks].ID, shown, base.Add(time.Duration(maximumRecentTasks+1)*time.Minute))
	corruptAttemptResult(t, fixture.store, shown)
	if status, code := checkStatus(t, sendJSON(t, http.MethodGet, server.URL+taskViewAPIPath, nil)); status != http.StatusServiceUnavailable || code != "state_unavailable" {
		t.Fatalf("unreadable shown task status=%d code=%s", status, code)
	}
	if page := browserGET(t, client, server.URL+codingToolsPath); page.status != http.StatusServiceUnavailable || strings.Contains(page.body, `class="taskrow"`) {
		t.Fatalf("Coding tools with an unreadable shown task status=%d", page.status)
	}
}
