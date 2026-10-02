package server

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"runtime"
	"strings"
	"testing"
	"time"
)

// repositoryShowAnswer is one GET /api/v1/repositories/ID answer: its status,
// the decoded body, the raw repository JSON object, and the raw body text.
type repositoryShowAnswer struct {
	status int
	answer repositoryResponse
	fields map[string]any
	body   string
}

func readRepositoryShow(t *testing.T, server *httptest.Server, id string) repositoryShowAnswer {
	t.Helper()
	response, err := http.Get(server.URL + "/api/v1/repositories/" + id)
	noErr(t, err)
	defer response.Body.Close()
	raw, err := io.ReadAll(response.Body)
	noErr(t, err)
	var answer repositoryResponse
	noErr(t, json.Unmarshal(raw, &answer))
	var decoded struct {
		Repository map[string]any `json:"repository"`
	}
	noErr(t, json.Unmarshal(raw, &decoded))
	return repositoryShowAnswer{status: response.StatusCode, answer: answer, fields: decoded.Repository, body: string(raw)}
}

// A ref read that failed is reported with the fixed read-error marker and
// logged, never as a repository without a default branch. Once Git works
// again the same read reports the branch, so no failure was kept.
func TestRepositoryShowMarksFailedBranchRead(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the failing Git wrapper is a Unix test fixture")
	}
	fixture := newAPIFixture(t, false)
	failure := failGitWhile(t, fixture.app, "for-each-ref")
	server := serve(t, fixture.app.Handler())

	before := readRepositoryShow(t, server, "project")
	if before.status != http.StatusOK || !before.answer.OK || before.answer.Repository.DefaultBranch != "main" || before.answer.Repository.DefaultBranchError != "" {
		t.Fatalf("positive control status=%d answer=%+v", before.status, before.answer)
	}
	// Releasing the write lock invalidates the cached ref snapshot, so the
	// next read runs the failing Git command instead of the cached answer.
	lock := fixture.app.Repositories.Locks.For("project")
	lock.Lock()
	lock.Unlock()
	noErr(t, os.WriteFile(failure, []byte("synthetic read failure\n"), 0o600))

	unreadable := readRepositoryShow(t, server, "project")
	const fixed = "OwnGit could not read the branches; see the server log."
	if unreadable.status != http.StatusOK || !unreadable.answer.OK {
		t.Fatalf("failed ref read status=%d answer=%+v", unreadable.status, unreadable.answer)
	}
	if unreadable.answer.Repository.DefaultBranch != "" || unreadable.answer.Repository.DefaultBranchError != fixed {
		t.Errorf("failed ref read default_branch=%q default_branch_error=%q, want %q",
			unreadable.answer.Repository.DefaultBranch, unreadable.answer.Repository.DefaultBranchError, fixed)
	}
	if _, present := unreadable.fields["default_branch"]; present {
		t.Error("the failed read still reported a default branch")
	}
	if got := unreadable.fields["default_branch_error"]; got != fixed {
		t.Errorf("default_branch_error on the wire=%v, want the fixed message", got)
	}
	for _, leak := range []string{"simulated", fixture.remote, fixture.work} {
		if strings.Contains(unreadable.body, leak) {
			t.Errorf("the failed read answer holds %q: %s", leak, unreadable.body)
		}
	}

	noErr(t, os.Rename(failure, failure+".retired"))
	recovered := readRepositoryShow(t, server, "project")
	if recovered.status != http.StatusOK || recovered.answer.Repository.DefaultBranch != "main" || recovered.answer.Repository.DefaultBranchError != "" {
		t.Errorf("recovery status=%d default_branch=%q default_branch_error=%q",
			recovered.status, recovered.answer.Repository.DefaultBranch, recovered.answer.Repository.DefaultBranchError)
	}
	if _, present := recovered.fields["default_branch_error"]; present {
		t.Error("the recovered read still reported a read error")
	}
}

// A repository whose folder is missing or unusable is still described,
// with that reason named instead of an absent default branch.
func TestRepositoryShowMarksMissingFolder(t *testing.T) {
	fixture := newAPIFixture(t, false)
	server := serve(t, fixture.app.Handler())
	before := readRepositoryShow(t, server, "project")
	if before.status != http.StatusOK || before.answer.Repository.DefaultBranch != "main" {
		t.Fatalf("positive control status=%d answer=%+v", before.status, before.answer)
	}
	folder, err := fixture.app.Repositories.Path("project")
	noErr(t, err)
	moved := folder + ".moved"
	noErr(t, os.Rename(folder, moved))

	missing := readRepositoryShow(t, server, "project")
	const fixed = "The repository folder is missing or unusable; see the server log."
	if missing.status != http.StatusOK || !missing.answer.OK {
		t.Fatalf("missing folder status=%d answer=%+v", missing.status, missing.answer)
	}
	if missing.answer.Repository.DefaultBranch != "" || missing.answer.Repository.DefaultBranchError != fixed {
		t.Errorf("missing folder default_branch=%q default_branch_error=%q, want %q",
			missing.answer.Repository.DefaultBranch, missing.answer.Repository.DefaultBranchError, fixed)
	}
	if _, present := missing.fields["default_branch"]; present {
		t.Error("the missing folder still reported a default branch")
	}
	if got := missing.fields["default_branch_error"]; got != fixed {
		t.Errorf("default_branch_error on the wire=%v, want the fixed message", got)
	}
	for _, leak := range []string{"no such file", folder} {
		if strings.Contains(missing.body, leak) {
			t.Errorf("the missing folder answer holds %q: %s", leak, missing.body)
		}
	}

	noErr(t, os.Rename(moved, folder))
	recovered := readRepositoryShow(t, server, "project")
	if recovered.status != http.StatusOK || recovered.answer.Repository.DefaultBranch != "main" || recovered.answer.Repository.DefaultBranchError != "" {
		t.Errorf("recovery status=%d default_branch=%q default_branch_error=%q",
			recovered.status, recovered.answer.Repository.DefaultBranch, recovered.answer.Repository.DefaultBranchError)
	}
}

// A repository held by another Git operation is still described and keeps the
// quiet fallback that leaves the default branch and the read-error marker out.
func TestRepositoryShowBusyLeavesNoError(t *testing.T) {
	fixture := newAPIFixture(t, false)
	lock := fixture.app.Repositories.Locks.For("project")
	lock.Lock()
	defer lock.Unlock()
	server := serve(t, fixture.app.Handler())
	start := time.Now()
	busy := readRepositoryShow(t, server, "project")
	elapsed := time.Since(start)
	t.Logf("the busy read waited %s", elapsed.Round(time.Millisecond))
	if busy.status != http.StatusOK || !busy.answer.OK {
		t.Fatalf("busy status=%d answer=%+v", busy.status, busy.answer)
	}
	if busy.answer.Repository.DefaultBranch != "" || busy.answer.Repository.DefaultBranchError != "" {
		t.Errorf("busy default_branch=%q default_branch_error=%q, want both empty",
			busy.answer.Repository.DefaultBranch, busy.answer.Repository.DefaultBranchError)
	}
	for _, field := range []string{"default_branch", "default_branch_error"} {
		if _, present := busy.fields[field]; present {
			t.Errorf("the busy read reported %s", field)
		}
	}
}
