package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"owngit/internal/state"
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
