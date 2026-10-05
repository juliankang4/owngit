package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"

	"owngit/internal/state"
)

func runReadCommandJSON(t *testing.T, command func([]string) error, arguments []string, result any) {
	t.Helper()
	output, err := captureStdout(func() error { return command(arguments) })
	noErr(t, err)
	if err := json.Unmarshal([]byte(output), result); err != nil {
		t.Fatalf("decode output: %v\n%s", err, output)
	}
}

// The tasks flags reach the server as its paging parameters, and the result,
// including the continuation, is printed as the server sent it. The paging
// flags need a repository and cannot be given with --task.
func TestTasksCommandPagesOneRepository(t *testing.T) {
	const body = `{"ok":true,"tasks":[],"next":"1:2:0123456789abcdef0123456789abcdef"}`
	var query string
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		query = request.URL.RawQuery
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(body))
	}))
	defer server.Close()
	before := "1:2:0123456789abcdef0123456789abcdef"
	output, err := captureStdout(func() error {
		return tasksCommand([]string{"--server", server.URL, "--accept-insecure-http", "--repository", "project", "--limit", "1", "--before", before})
	})
	noErr(t, err)
	if query != "before="+url.QueryEscape(before)+"&limit=1" || output != body+"\n" {
		t.Fatalf("query %q, output %q; want the paging parameters and the server bytes", query, output)
	}
	for _, arguments := range [][]string{
		{"--server", server.URL, "--accept-insecure-http", "--limit", "1"},
		{"--server", server.URL, "--accept-insecure-http", "--before", before},
		{"--server", server.URL, "--accept-insecure-http", "--repository", "project", "--task", "0123", "--limit", "1"},
	} {
		if code := commandErrorCode(tasksCommand(arguments)); code != "invalid_arguments" {
			t.Errorf("%v: code=%q", arguments, code)
		}
	}
}

func TestActivityTasksAndCredentialListCommands(t *testing.T) {
	serverURL, passwordFile := startRepositoryCLIServer(t, "shared-password")
	remote := []string{"--server", serverURL, "--accept-insecure-http", "--password-file", passwordFile}
	noErr(t, repoCommand(append([]string{"create", "--name", "tools"}, remote...)))

	var activity struct {
		OK              bool  `json:"ok"`
		RepositoryCount int   `json:"repository_count"`
		Total           int   `json:"total"`
		Entries         []any `json:"entries"`
		Truncated       bool  `json:"truncated"`
	}
	runReadCommandJSON(t, activityCommand, append([]string{"--year", "2026"}, remote...), &activity)
	if !activity.OK || activity.RepositoryCount != 1 || activity.Total != 0 || len(activity.Entries) != 0 || activity.Truncated {
		t.Fatalf("activity=%+v", activity)
	}
	if code := commandErrorCode(activityCommand(append([]string{"--date", "2026-13-01"}, remote...))); code != "invalid_request" {
		t.Fatalf("activity with an invalid date: code=%q", code)
	}
	if code := commandErrorCode(activityCommand([]string{"--server", serverURL, "--accept-insecure-http"})); code != "authentication_required" {
		t.Fatalf("activity without the password: code=%q", code)
	}

	var tasks struct {
		OK    bool  `json:"ok"`
		Tasks []any `json:"tasks"`
	}
	for _, arguments := range [][]string{remote, append([]string{"--repository", "tools"}, remote...)} {
		runReadCommandJSON(t, tasksCommand, arguments, &tasks)
		if !tasks.OK || tasks.Tasks == nil || len(tasks.Tasks) != 0 {
			t.Fatalf("tasks %v: %+v", arguments, tasks)
		}
	}
	if code := commandErrorCode(tasksCommand(append([]string{"--task", "0123"}, remote...))); code != "invalid_arguments" {
		t.Fatalf("tasks --task without --repository: code=%q", code)
	}
	if code := commandErrorCode(tasksCommand(append([]string{"--repository", "tools", "--task", "0123"}, remote...))); code != "task_not_found" {
		t.Fatalf("missing task: code=%q", code)
	}

	adminPasswordFile := filepath.Join(t.TempDir(), "admin-password")
	noErr(t, os.WriteFile(adminPasswordFile, []byte("admin-password\n"), 0o600))
	noErr(t, state.ProtectPrivatePath(adminPasswordFile, false))
	admin := []string{"--server", serverURL, "--accept-insecure-http", "--password-file", adminPasswordFile}
	noErr(t, helperCredentialCommand(append([]string{"create", "--repository", "tools", "--label", "laptop", "--output", filepath.Join(t.TempDir(), "token")}, admin...)))
	var credentials struct {
		OK          bool `json:"ok"`
		Credentials []struct {
			RepositoryID string `json:"repository_id"`
			Label        string `json:"label"`
		} `json:"credentials"`
	}
	runReadCommandJSON(t, helperCredentialCommand, append([]string{"list"}, admin...), &credentials)
	if !credentials.OK || len(credentials.Credentials) != 1 || credentials.Credentials[0].RepositoryID != "tools" || credentials.Credentials[0].Label != "laptop" {
		t.Fatalf("every repository's credentials: %+v", credentials)
	}
}
