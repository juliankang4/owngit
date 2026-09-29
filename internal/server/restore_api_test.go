package server

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"owngit/internal/repository"
)

// restoreHistory is a repository whose main branch was force-pushed, so its
// earlier tip is kept history.
type restoreHistory struct {
	remote string
	// first has kept.txt, gone.txt and link, a symbolic link to kept.txt.
	// kept is first's child with kept.txt changed, gone.txt and link
	// removed and extra.txt added; a force push replaced it with current,
	// first's child with kept.txt changed.
	first, kept, current string
}

// pushRestoreHistory creates repository name in format and pushes the
// history of restoreHistory to it. The same commits come out for the same
// format, so two repositories can be compared.
func pushRestoreHistory(t *testing.T, app *App, name, format string) restoreHistory {
	t.Helper()
	_, err := app.Repositories.CreateWithOptions(context.Background(), name, "", repository.CreateOptions{ObjectFormat: format})
	noErr(t, err)
	remote, err := app.Repositories.Path(name)
	noErr(t, err)
	work := filepath.Join(t.TempDir(), "work")
	apiRunGit(t, "", "init", "--object-format="+format, "--initial-branch=main", work)
	apiRunGit(t, work, "config", "user.name", "Restore Author")
	apiRunGit(t, work, "config", "user.email", "restore@example.invalid")
	commit := func(message string) string {
		apiRunGit(t, work, "-c", "commit.gpgsign=false", "commit", "-q", "-m", message)
		return apiGitOutput(t, work, "rev-parse", "HEAD")
	}
	write := func(name, content string) {
		noErr(t, os.WriteFile(filepath.Join(work, name), []byte(content), 0o600))
		apiRunGit(t, work, "add", name)
	}
	t.Setenv("GIT_COMMITTER_DATE", "2024-01-01T00:00:00Z")
	t.Setenv("GIT_AUTHOR_DATE", "2024-01-01T00:00:00Z")
	write("kept.txt", "first\n")
	write("gone.txt", "gone\n")
	// The link is written to the index only, so the fixture is the same
	// where the file system has no symbolic links.
	target := filepath.Join(t.TempDir(), "link-target")
	noErr(t, os.WriteFile(target, []byte("kept.txt"), 0o600))
	blob := apiGitOutput(t, work, "hash-object", "-w", target)
	apiRunGit(t, work, "update-index", "--add", "--cacheinfo", "120000,"+blob+",link")
	history := restoreHistory{remote: remote, first: commit("first")}
	write("kept.txt", "kept\n")
	write("extra.txt", "extra\n")
	apiRunGit(t, work, "rm", "-q", "--cached", "gone.txt", "link")
	history.kept = commit("kept")
	apiRunGit(t, work, "push", "-q", remote, "HEAD:refs/heads/main")
	apiRunGit(t, work, "reset", "-q", "--soft", history.first)
	apiRunGit(t, work, "read-tree", history.first)
	t.Setenv("GIT_COMMITTER_DATE", "2024-01-02T00:00:00Z")
	t.Setenv("GIT_AUTHOR_DATE", "2024-01-02T00:00:00Z")
	write("kept.txt", "current\n")
	history.current = commit("current")
	apiRunGit(t, work, "push", "-q", "--force", remote, "HEAD:refs/heads/main")
	return history
}

// restoreAPI sends a restore API request and returns the status and the
// decoded body.
func restoreAPI(t *testing.T, method, target string, body any, password string) (int, map[string]any) {
	t.Helper()
	response := sendJSON(t, method, target, body, basicAuth("owngit", password))
	defer response.Body.Close()
	content, err := io.ReadAll(response.Body)
	noErr(t, err)
	var decoded map[string]any
	if err := json.Unmarshal(content, &decoded); err != nil {
		t.Fatalf("%s %s: %v\n%s", method, target, err, content)
	}
	return response.StatusCode, decoded
}

func keys(value any) string {
	object, _ := value.(map[string]any)
	names := make([]string, 0, len(object))
	for name := range object {
		names = append(names, name)
	}
	sort.Strings(names)
	return strings.Join(names, " ")
}

func restoreErrorCode(body map[string]any) string {
	problem, _ := body["error"].(map[string]any)
	code, _ := problem["code"].(string)
	return code
}

// The API lists kept history, previews a restore with every changed path
// and its modes, applies it only at the previewed branch tip, and creates a
// branch for kept history, in SHA-1 and SHA-256 repositories.
func TestRestoreAPIListsPreviewsAndAppliesKeptHistory(t *testing.T) {
	for _, format := range []string{repository.ObjectFormatSHA1, repository.ObjectFormatSHA256} {
		t.Run(format, func(t *testing.T) {
			app := newConfiguredApp(t)
			history := pushRestoreHistory(t, app, "project", format)
			server := httptest.NewServer(app.Handler())
			defer server.Close()
			base := server.URL + "/api/v1/repositories/project"
			zero := strings.Repeat("0", len(history.kept))

			status, listed := restoreAPI(t, http.MethodGet, base+"/kept-history", nil, "")
			items, _ := listed["kept_history"].([]any)
			if status != http.StatusOK || keys(listed) != "kept_history ok repository" || len(items) != 1 {
				t.Fatalf("kept history status=%d body=%v", status, listed)
			}
			item := items[0].(map[string]any)
			if keys(item) != "author_name authored_at commit_oid kind oid restore_target source_ref subject" ||
				item["kind"] != "branch" || item["source_ref"] != "refs/heads/main" || item["oid"] != history.kept ||
				item["commit_oid"] != history.kept || item["subject"] != "kept" || item["restore_target"] != "recovered-"+history.kept[:10] {
				t.Fatalf("kept history item %v", item)
			}

			selection := map[string]any{"source_oid": history.kept, "target_branch": "main", "mode": "all"}
			status, previewed := restoreAPI(t, http.MethodPost, base+"/restore/preview", selection, "")
			preview, _ := previewed["preview"].(map[string]any)
			if status != http.StatusOK || keys(previewed) != "ok preview repository" ||
				keys(preview) != "can_apply changes creates_branch expected_head mode result_tree source_oid target_branch target_ref" {
				t.Fatalf("preview status=%d body=%v", status, previewed)
			}
			if preview["expected_head"] != history.current || preview["creates_branch"] != false || preview["can_apply"] != true || preview["target_ref"] != "refs/heads/main" {
				t.Fatalf("preview %v", preview)
			}
			type change struct{ path, status, oldMode, newMode string }
			var changes []change
			for _, entry := range preview["changes"].([]any) {
				fields := entry.(map[string]any)
				if keys(fields) != "additions binary deletions new_mode old_mode path status" {
					t.Fatalf("change fields %v", fields)
				}
				changes = append(changes, change{fields["path"].(string), fields["status"].(string), fields["old_mode"].(string), fields["new_mode"].(string)})
			}
			want := []change{
				{"extra.txt", "added", "000000", "100644"},
				{"gone.txt", "deleted", "100644", "000000"},
				{"kept.txt", "modified", "100644", "100644"},
				{"link", "deleted", "120000", "000000"},
			}
			if !reflect.DeepEqual(changes, want) {
				t.Fatalf("changes %v, want %v", changes, want)
			}

			// A preview made against an earlier tip is not consent to this one.
			stale := map[string]any{"source_oid": history.kept, "target_branch": "main", "mode": "all", "expected_head": history.kept}
			if status, body := restoreAPI(t, http.MethodPost, base+"/restore", stale, ""); status != http.StatusConflict || restoreErrorCode(body) != "stale_revision" {
				t.Fatalf("stale apply status=%d body=%v", status, body)
			}
			missing := map[string]any{"source_oid": history.kept, "target_branch": "main", "mode": "all"}
			if status, body := restoreAPI(t, http.MethodPost, base+"/restore", missing, ""); status != http.StatusUnprocessableEntity || restoreErrorCode(body) != "invalid_restore" {
				t.Fatalf("apply without expected_head status=%d body=%v", status, body)
			}
			if got := apiGitOutput(t, "", "--git-dir", history.remote, "rev-parse", "refs/heads/main"); got != history.current {
				t.Fatalf("refused applies moved main to %s", got)
			}

			apply := map[string]any{"source_oid": history.kept, "target_branch": "main", "mode": "all", "expected_head": history.current}
			status, applied := restoreAPI(t, http.MethodPost, base+"/restore", apply, "")
			result, _ := applied["restore"].(map[string]any)
			if status != http.StatusOK || keys(applied) != "ok repository restore" || keys(result) != "commit_oid created previous_head target_ref" ||
				result["created"] != false || result["previous_head"] != history.current || result["target_ref"] != "refs/heads/main" {
				t.Fatalf("apply status=%d body=%v", status, applied)
			}
			restored := result["commit_oid"].(string)
			if got := apiGitOutput(t, "", "--git-dir", history.remote, "rev-parse", "refs/heads/main"); got != restored {
				t.Fatalf("main is %s, want the restore commit %s", got, restored)
			}
			if parent := apiGitOutput(t, "", "--git-dir", history.remote, "rev-parse", restored+"^"); parent != history.current {
				t.Fatalf("restore commit parent %s, want the previewed tip %s", parent, history.current)
			}
			if tree := apiGitOutput(t, "", "--git-dir", history.remote, "rev-parse", restored+"^{tree}"); tree != preview["result_tree"] {
				t.Fatalf("restore tree %s, want the previewed %v", tree, preview["result_tree"])
			}
			// Applying the same preview again finds the branch moved.
			if status, body := restoreAPI(t, http.MethodPost, base+"/restore", apply, ""); status != http.StatusConflict || restoreErrorCode(body) != "stale_revision" {
				t.Fatalf("repeated apply status=%d body=%v", status, body)
			}

			// Restoring one file brings the symbolic link back as Git data.
			link := map[string]any{"source_oid": history.first, "target_branch": "main", "mode": "files", "paths": []string{"link"}}
			status, previewed = restoreAPI(t, http.MethodPost, base+"/restore/preview", link, "")
			preview, _ = previewed["preview"].(map[string]any)
			linkChanges, _ := preview["changes"].([]any)
			if status != http.StatusOK || len(linkChanges) != 1 || keys(preview) != "can_apply changes creates_branch expected_head mode paths result_tree source_oid target_branch target_ref" {
				t.Fatalf("link preview status=%d body=%v", status, previewed)
			}
			if fields := linkChanges[0].(map[string]any); fields["path"] != "link" || fields["status"] != "added" || fields["new_mode"] != "120000" {
				t.Fatalf("link change %v", fields)
			}
			link["expected_head"] = restored
			if status, body := restoreAPI(t, http.MethodPost, base+"/restore", link, ""); status != http.StatusOK {
				t.Fatalf("link apply status=%d body=%v", status, body)
			}
			if entry := apiGitOutput(t, "", "--git-dir", history.remote, "ls-tree", "refs/heads/main", "link"); !strings.HasPrefix(entry, "120000 blob ") {
				t.Fatalf("restored link entry %q", entry)
			}

			// Kept history restores into the new branch the list offers.
			branch := map[string]any{"source_oid": history.kept, "target_branch": item["restore_target"], "mode": "all"}
			status, previewed = restoreAPI(t, http.MethodPost, base+"/restore/preview", branch, "")
			preview, _ = previewed["preview"].(map[string]any)
			if status != http.StatusOK || preview["creates_branch"] != true || preview["expected_head"] != zero {
				t.Fatalf("new branch preview status=%d body=%v", status, previewed)
			}
			branch["expected_head"] = zero
			status, applied = restoreAPI(t, http.MethodPost, base+"/restore", branch, "")
			result, _ = applied["restore"].(map[string]any)
			if status != http.StatusOK || result["created"] != true || result["commit_oid"] != history.kept {
				t.Fatalf("new branch apply status=%d body=%v", status, applied)
			}
			if got := apiGitOutput(t, "", "--git-dir", history.remote, "rev-parse", "refs/heads/"+item["restore_target"].(string)); got != history.kept {
				t.Fatalf("created branch at %s, want %s", got, history.kept)
			}

			if status, body := restoreAPI(t, http.MethodPost, base+"/restore/preview", map[string]any{"source_oid": history.kept[:12], "target_branch": "main", "mode": "all"}, ""); status != http.StatusUnprocessableEntity || restoreErrorCode(body) != "invalid_restore" {
				t.Fatalf("short source status=%d body=%v", status, body)
			}
			if status, body := restoreAPI(t, http.MethodPost, base+"/restore/preview", map[string]any{"source_oid": history.kept, "target_branch": "main", "mode": "all", "expected_head": history.kept}, ""); status != http.StatusBadRequest || restoreErrorCode(body) != "invalid_json" {
				t.Fatalf("preview with expected_head status=%d body=%v", status, body)
			}
			if status, body := restoreAPI(t, http.MethodGet, base+"/restore/preview", nil, ""); status != http.StatusMethodNotAllowed || restoreErrorCode(body) != "method_not_allowed" {
				t.Fatalf("GET preview status=%d body=%v", status, body)
			}
			if status, body := restoreAPI(t, http.MethodGet, server.URL+"/api/v1/repositories/absent/kept-history", nil, ""); status != http.StatusNotFound || restoreErrorCode(body) != "repository_not_found" {
				t.Fatalf("absent repository status=%d body=%v", status, body)
			}
		})
	}
}

// With a shared password, restoring needs it, as the restore pages need
// general access; the administrator password is not needed.
func TestRestoreAPIUsesGeneralAccess(t *testing.T) {
	fixture := newAPIFixture(t, true)
	server := httptest.NewServer(fixture.app.Handler())
	defer server.Close()
	base := server.URL + "/api/v1/repositories/project"
	selection := map[string]any{"source_oid": fixture.sourceOID, "target_branch": "main", "mode": "all"}
	if status, _ := restoreAPI(t, http.MethodGet, base+"/kept-history", nil, ""); status != http.StatusUnauthorized {
		t.Fatalf("kept history without a password: status=%d", status)
	}
	// A wrong password counts toward the sign-in limit, so only one is tried.
	for _, password := range []string{"", "admin-password"} {
		if status, body := restoreAPI(t, http.MethodPost, base+"/restore/preview", selection, password); status != http.StatusUnauthorized {
			t.Fatalf("preview with %q: status=%d body=%v", password, status, body)
		}
	}
	status, previewed := restoreAPI(t, http.MethodPost, base+"/restore/preview", selection, "shared-password")
	preview, _ := previewed["preview"].(map[string]any)
	if status != http.StatusOK || preview["expected_head"] != fixture.targetOID {
		t.Fatalf("preview status=%d body=%v", status, previewed)
	}
	selection["expected_head"] = fixture.targetOID
	if status, body := restoreAPI(t, http.MethodPost, base+"/restore", selection, "shared-password"); status != http.StatusOK {
		t.Fatalf("apply status=%d body=%v", status, body)
	}
}

// The browser's restore form and the API make the same commit from the same
// selection: the same tree, parent and message.
func TestRestoreBrowserAndAPIMakeTheSameCommit(t *testing.T) {
	app := newConfiguredApp(t)
	browserHistory := pushRestoreHistory(t, app, "browser", repository.ObjectFormatSHA1)
	apiHistory := pushRestoreHistory(t, app, "api", repository.ObjectFormatSHA1)
	if browserHistory.current != apiHistory.current {
		t.Fatalf("fixtures differ: %s and %s", browserHistory.current, apiHistory.current)
	}
	server := httptest.NewServer(app.Handler())
	defer server.Close()

	client, jar := newBrowserClient(t)
	if _, status := dashboardGET(t, client, server.URL+"/repositories/browser/restore?source="+browserHistory.kept+"&target=main"); status != http.StatusOK {
		t.Fatalf("restore page status=%d", status)
	}
	_, status := restorePOST(t, client, server.URL+"/repositories/browser/restore", url.Values{
		"csrf": {cookieValue(t, jar, server.URL, generalCookie)}, "source": {browserHistory.kept}, "target": {"main"}, "mode": {"files"},
		"path": {"kept.txt", "link"}, "expected_head": {browserHistory.current}, "confirm": {"restore"},
	}, server.URL)
	if status != http.StatusSeeOther {
		t.Fatalf("browser restore status=%d", status)
	}
	apply := map[string]any{"source_oid": apiHistory.kept, "target_branch": "main", "mode": "files", "paths": []string{"kept.txt", "link"}, "expected_head": apiHistory.current}
	if status, body := restoreAPI(t, http.MethodPost, server.URL+"/api/v1/repositories/api/restore", apply, ""); status != http.StatusOK {
		t.Fatalf("API restore status=%d body=%v", status, body)
	}
	describe := func(remote string) string {
		return apiGitOutput(t, "", "--git-dir", remote, "log", "-1", "--format=%T %P %an <%ae> %s", "refs/heads/main")
	}
	browserCommit, apiCommit := describe(browserHistory.remote), describe(apiHistory.remote)
	if browserCommit != apiCommit || !strings.Contains(browserCommit, " "+browserHistory.current+" ") {
		t.Fatalf("browser restore %q, API restore %q", browserCommit, apiCommit)
	}
}
