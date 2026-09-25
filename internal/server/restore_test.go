package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"owngit/internal/auth"
	"owngit/internal/gitexec"
	"owngit/internal/repository"
	"owngit/internal/webui"
)

func TestRestoreHTTPPreviewApplyAndStaleConflict(t *testing.T) {
	app := newConfiguredApp(t)
	if _, err := app.Repositories.Create(context.Background(), "restore-http", ""); err != nil {
		t.Fatal(err)
	}
	remote, _ := app.Repositories.Path("restore-http")
	work := filepath.Join(t.TempDir(), "work")
	apiRunGit(t, "", "init", "--initial-branch=main", work)
	apiRunGit(t, work, "config", "user.name", "Restore Author")
	apiRunGit(t, work, "config", "user.email", "restore@example.invalid")
	noErr(t, os.WriteFile(filepath.Join(work, "kept.txt"), []byte("source\n"), 0o600))
	apiRunGit(t, work, "add", ".")
	apiRunGit(t, work, "commit", "-m", "source")
	sourceOID := apiGitOutput(t, work, "rev-parse", "HEAD")
	noErr(t, os.WriteFile(filepath.Join(work, "kept.txt"), []byte("target\n"), 0o600))
	noErr(t, os.WriteFile(filepath.Join(work, "remove.txt"), []byte("target only\n"), 0o600))
	apiRunGit(t, work, "add", ".")
	apiRunGit(t, work, "commit", "-m", "target")
	targetOID := apiGitOutput(t, work, "rev-parse", "HEAD")
	apiRunGit(t, work, "push", remote, "HEAD:refs/heads/main")

	server := httptest.NewServer(app.Handler())
	defer server.Close()
	client, jar := newBrowserClient(t)
	commitPath := server.URL + "/repositories/restore-http/commits/" + sourceOID + "?" + url.Values{"ref": {"refs/heads/main"}}.Encode()
	commitBody, commitStatus := dashboardGET(t, client, commitPath)
	wholeTreeLink := `href="/repositories/restore-http/restore?source=` + sourceOID + `&amp;target=main"`
	if commitStatus != http.StatusOK || !strings.Contains(commitBody, wholeTreeLink) {
		t.Fatalf("commit restore link status=%d, want whole-tree link %s", commitStatus, wholeTreeLink)
	}
	fileBody, fileStatus := dashboardGET(t, client, commitPath+"&path=kept.txt")
	fileLink := `href="/repositories/restore-http/restore?mode=files&amp;path=kept.txt&amp;source=` + sourceOID + `&amp;target=main"`
	if fileStatus != http.StatusOK || !strings.Contains(fileBody, fileLink) {
		t.Fatalf("file restore link status=%d, want selected-file link %s", fileStatus, fileLink)
	}

	body, status := dashboardGET(t, client, server.URL+"/repositories/restore-http/restore?source="+sourceOID+"&target=main")
	hasSourceAndDeletion := strings.Contains(body, "remove.txt") && strings.Contains(body, sourceOID[:10])
	if status != http.StatusOK || !hasSourceAndDeletion {
		t.Fatalf("restore GET status=%d source_and_deletion_visible=%v", status, hasSourceAndDeletion)
	}
	explicitAllBody, explicitAllStatus := dashboardGET(t, client, server.URL+"/repositories/restore-http/restore?source="+sourceOID+"&target=main&mode=all&path=kept.txt")
	if explicitAllStatus != http.StatusOK || !strings.Contains(explicitAllBody, "remove.txt") {
		t.Fatalf("whole-tree restore GET with stale paths status=%d deletion_visible=%v", explicitAllStatus, strings.Contains(explicitAllBody, "remove.txt"))
	}
	csrf := cookieValue(t, jar, server.URL, generalCookie)
	selection := repository.RestoreRequest{Source: sourceOID, Target: "main", Mode: repository.RestoreFiles, Paths: []string{"kept.txt", "remove.txt"}}
	preview, err := app.Repositories.PreviewRestore(context.Background(), "restore-http", selection)
	noErr(t, err)
	previewBody, previewStatus := restorePOST(t, client, server.URL+"/repositories/restore-http/restore/preview", url.Values{
		"csrf": {csrf}, "source": {sourceOID}, "target": {"main"}, "mode": {"files"}, "path": {"kept.txt", "remove.txt"},
	}, server.URL)
	previewContentVisible := strings.Contains(previewBody, "Nothing has changed yet") && strings.Contains(previewBody, "remove.txt")
	if previewStatus != http.StatusOK || !previewContentVisible {
		t.Fatalf("restore preview status=%d expected_content_visible=%v", previewStatus, previewContentVisible)
	}
	_, applyStatus := restorePOST(t, client, server.URL+"/repositories/restore-http/restore", url.Values{
		"csrf": {csrf}, "source": {sourceOID}, "target": {"main"}, "mode": {"files"},
		"path": {"kept.txt", "remove.txt"}, "expected_head": {preview.ExpectedHead}, "confirm": {"restore"},
	}, server.URL)
	if applyStatus != http.StatusSeeOther {
		t.Fatalf("restore apply status=%d", applyStatus)
	}
	restoredOID := apiGitOutput(t, "", "--git-dir", remote, "rev-parse", "refs/heads/main")
	if restoredOID == sourceOID || restoredOID == targetOID {
		t.Fatalf("restore did not create a new commit: %s", restoredOID)
	}
	if got := apiGitOutput(t, "", "--git-dir", remote, "show", restoredOID+":kept.txt"); got != "source" {
		t.Fatalf("restored file=%q", got)
	}
	if _, err := gitCombined("", "--git-dir", remote, "cat-file", "-e", restoredOID+":remove.txt"); err == nil {
		t.Fatal("selected source deletion was not applied")
	}

	apiRunGit(t, "", "--git-dir", remote, "update-ref", "refs/heads/main", targetOID, restoredOID)
	ordinaryAllBody, ordinaryAllStatus := restorePOST(t, client, server.URL+"/repositories/restore-http/restore/preview", url.Values{
		"csrf": {csrf}, "source": {sourceOID}, "target": {"main"}, "mode": {"all"},
	}, server.URL)
	ordinaryDeletionVisible := strings.Contains(ordinaryAllBody, `class="restore__deletes"`) && strings.Contains(ordinaryAllBody, "remove.txt")
	if ordinaryAllStatus != http.StatusOK || !ordinaryDeletionVisible {
		t.Fatalf("ordinary whole-tree preview status=%d deletion_visible=%v", ordinaryAllStatus, ordinaryDeletionVisible)
	}
	allPreview, err := app.Repositories.PreviewRestore(context.Background(), "restore-http", repository.RestoreRequest{Source: sourceOID, Target: "main", Mode: repository.RestoreAll})
	noErr(t, err)
	leftoverBody, leftoverStatus := restorePOST(t, client, server.URL+"/repositories/restore-http/restore/preview", url.Values{
		"csrf": {csrf}, "source": {sourceOID}, "target": {"main"}, "mode": {"all"}, "path": {"kept.txt"},
	}, server.URL)
	leftoverDeletionVisible := strings.Contains(leftoverBody, `class="restore__deletes"`) && strings.Contains(leftoverBody, "remove.txt")
	if leftoverStatus != http.StatusOK || !leftoverDeletionVisible {
		t.Fatalf("whole-tree preview with stale paths status=%d deletion_visible=%v", leftoverStatus, leftoverDeletionVisible)
	}
	_, allApplyStatus := restorePOST(t, client, server.URL+"/repositories/restore-http/restore", url.Values{
		"csrf": {csrf}, "source": {sourceOID}, "target": {"main"}, "mode": {"all"}, "path": {"kept.txt"},
		"expected_head": {allPreview.ExpectedHead}, "confirm": {"restore"},
	}, server.URL)
	if allApplyStatus != http.StatusSeeOther {
		t.Fatalf("whole-tree apply with stale paths status=%d", allApplyStatus)
	}
	allRestoredOID := apiGitOutput(t, "", "--git-dir", remote, "rev-parse", "refs/heads/main")
	if got := apiGitOutput(t, "", "--git-dir", remote, "show", allRestoredOID+":kept.txt"); got != "source" {
		t.Fatalf("whole-tree restored file=%q", got)
	}
	if _, err := gitCombined("", "--git-dir", remote, "cat-file", "-e", allRestoredOID+":remove.txt"); err == nil {
		t.Fatal("whole-tree restore ignored source deletion when stale paths were submitted")
	}

	stale, err := app.Repositories.PreviewRestore(context.Background(), "restore-http", repository.RestoreRequest{Source: targetOID, Target: "main", Mode: repository.RestoreAll})
	noErr(t, err)
	apiRunGit(t, "", "--git-dir", remote, "update-ref", "refs/heads/main", targetOID, allRestoredOID)
	conflictBody, conflictStatus := restorePOST(t, client, server.URL+"/repositories/restore-http/restore", url.Values{
		"csrf": {csrf}, "source": {sourceOID}, "target": {"main"}, "mode": {"all"},
		"expected_head": {stale.ExpectedHead}, "confirm": {"restore"},
	}, server.URL)
	conflictNotice := "The branch changed after the preview. Preview again before restoring."
	if conflictStatus != http.StatusConflict || !strings.Contains(conflictBody, conflictNotice) {
		t.Fatalf("stale restore status=%d conflict_notice_visible=%v", conflictStatus, strings.Contains(conflictBody, conflictNotice))
	}
	// The refusal is a whole new document, so the browser would otherwise
	// start the reader at the top of it, away from the reason the restore was
	// blocked. The response has to hand the reader that message.
	conflictAlert := conflictBody[strings.LastIndex(conflictBody[:strings.Index(conflictBody, `id="target-note"`)], "<"):]
	conflictAlert = conflictAlert[:strings.Index(conflictAlert, ">")]
	for _, want := range []string{`role="alert"`, `tabindex="-1"`, "autofocus"} {
		if !strings.Contains(conflictAlert, want) {
			t.Errorf("the refused restore does not reach the reader, missing %s: %s", want, conflictAlert)
		}
	}
	if got := strings.Count(conflictBody, "autofocus"); got != 1 {
		t.Errorf("the refusal response has %d focus targets, want 1", got)
	}
	if got := apiGitOutput(t, "", "--git-dir", remote, "rev-parse", "refs/heads/main"); got != targetOID {
		t.Fatalf("stale restore changed branch to %s", got)
	}

	_, forbiddenStatus := restorePOST(t, client, server.URL+"/repositories/restore-http/restore/preview", url.Values{
		"source": {sourceOID}, "target": {"main"}, "mode": {"all"},
	}, server.URL)
	if forbiddenStatus != http.StatusForbidden {
		t.Fatalf("restore preview without CSRF status=%d", forbiddenStatus)
	}
}

func TestRestoreApplyReportsPageConstructionFailure(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the failing Git wrapper is a Unix test fixture")
	}
	app := newConfiguredApp(t)
	if _, err := app.Repositories.Create(context.Background(), "page-failure", ""); err != nil {
		t.Fatal(err)
	}
	remote, _ := app.Repositories.Path("page-failure")
	work := filepath.Join(t.TempDir(), "work")
	apiRunGit(t, "", "init", "--initial-branch=main", work)
	apiRunGit(t, work, "config", "user.name", "Restore Author")
	apiRunGit(t, work, "config", "user.email", "restore@example.invalid")
	noErr(t, os.WriteFile(filepath.Join(work, "file.txt"), []byte("source\n"), 0o600))
	apiRunGit(t, work, "add", ".")
	apiRunGit(t, work, "commit", "-m", "source")
	sourceOID := apiGitOutput(t, work, "rev-parse", "HEAD")
	noErr(t, os.WriteFile(filepath.Join(work, "file.txt"), []byte("target\n"), 0o600))
	apiRunGit(t, work, "commit", "-am", "target")
	apiRunGit(t, work, "push", remote, "HEAD:refs/heads/main")

	server := httptest.NewServer(app.Handler())
	defer server.Close()
	client, jar := newBrowserClient(t)
	if _, status := dashboardGET(t, client, server.URL+"/"); status != http.StatusOK {
		t.Fatalf("overview status=%d", status)
	}
	csrf := cookieValue(t, jar, server.URL, generalCookie)

	gitPath, err := exec.LookPath("git")
	noErr(t, err)
	wrapperPath := filepath.Join(t.TempDir(), "git-wrapper")
	// The page reads the source commit's metadata with the only log that
	// lists raw changes, so failing that read fails the page alone.
	wrapper := "#!/bin/sh\nfor arg in \"$@\"; do\n  if test \"$arg\" = --raw; then exit 97; fi\ndone\nexec " + serverShellQuote(gitPath) + " \"$@\"\n"
	noErr(t, os.WriteFile(wrapperPath, []byte(wrapper), 0o700))
	failing, err := gitexec.New(wrapperPath, filepath.Join(t.TempDir(), "runtime"))
	noErr(t, err)
	app.Repositories.Git = failing

	body, status := restorePOST(t, client, server.URL+"/repositories/page-failure/restore", url.Values{
		"csrf": {csrf}, "source": {sourceOID}, "target": {"main"}, "mode": {"all"},
		"expected_head": {strings.Repeat("0", len(sourceOID))}, "confirm": {"restore"},
	}, server.URL)
	if status != http.StatusUnprocessableEntity || !strings.Contains(body, "That selection cannot be restored") {
		t.Fatalf("page construction failure status=%d invalid_message=%v", status, strings.Contains(body, "That selection cannot be restored"))
	}
	if strings.Contains(body, "The branch changed after the preview") {
		t.Fatalf("page construction failure reported the earlier apply error")
	}
}

func serverShellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}

func TestRestorePageUsesPreviewBranchObservationInsteadOfStaleSummary(t *testing.T) {
	app := newConfiguredApp(t)
	stored, err := app.Repositories.Create(context.Background(), "branch-observation", "")
	noErr(t, err)
	remote, _ := app.Repositories.Path(stored.ID)
	work := filepath.Join(t.TempDir(), "work")
	apiRunGit(t, "", "init", "--initial-branch=main", work)
	apiRunGit(t, work, "config", "user.name", "Restore Author")
	apiRunGit(t, work, "config", "user.email", "restore@example.invalid")
	noErr(t, os.WriteFile(filepath.Join(work, "file.txt"), []byte("source\n"), 0o600))
	apiRunGit(t, work, "add", ".")
	apiRunGit(t, work, "commit", "-m", "source")
	sourceOID := apiGitOutput(t, work, "rev-parse", "HEAD")
	noErr(t, os.WriteFile(filepath.Join(work, "file.txt"), []byte("target\n"), 0o600))
	apiRunGit(t, work, "commit", "-am", "target")
	targetOID := apiGitOutput(t, work, "rev-parse", "HEAD")
	apiRunGit(t, work, "push", remote, "HEAD:refs/heads/main")

	request := httptest.NewRequest(http.MethodGet, "http://localhost/repositories/branch-observation/restore", nil)
	staleMissing := repository.Summary{DefaultBranch: "main"}
	existingPage, err := app.restorePage(request, stored, staleMissing, webui.Chrome{}, repository.RestoreRequest{
		Source: sourceOID, Target: "main", Mode: repository.RestoreAll,
	}, nil, false)
	noErr(t, err)
	if existingPage.CreatesBranch || existingPage.ExpectedHead != targetOID {
		t.Fatalf("existing target used stale missing summary: creates=%v expected=%s", existingPage.CreatesBranch, existingPage.ExpectedHead)
	}

	zero := strings.Repeat("0", len(sourceOID))
	staleExisting := repository.Summary{DefaultBranch: "main", Branches: []repository.Ref{{Name: "release", OID: targetOID, Type: "commit"}}}
	missingPage, err := app.restorePage(request, stored, staleExisting, webui.Chrome{}, repository.RestoreRequest{
		Source: sourceOID, Target: "release", Mode: repository.RestoreAll,
	}, nil, false)
	noErr(t, err)
	if !missingPage.CreatesBranch || missingPage.ExpectedHead != zero {
		t.Fatalf("missing target used stale existing summary: creates=%v expected=%s", missingPage.CreatesBranch, missingPage.ExpectedHead)
	}
}

func TestRestoreRequiresGeneralAccessButNotAdministratorSession(t *testing.T) {
	app, store, repositoryRoot := newTestApp(t)
	noErr(t, os.MkdirAll(repositoryRoot, 0o700))
	canonical, err := filepath.EvalSymlinks(repositoryRoot)
	noErr(t, err)
	accessHash, _ := auth.HashPassword("shared-password")
	adminHash, _ := auth.HashPassword("admin-password")
	noErr(t, store.CompleteSetup(context.Background(), canonical, "password", accessHash, adminHash, true))
	app.Repositories.SetRoot(canonical)
	if _, err := app.Repositories.Create(context.Background(), "protected-restore", ""); err != nil {
		t.Fatal(err)
	}
	remote, _ := app.Repositories.Path("protected-restore")
	work := filepath.Join(t.TempDir(), "work")
	apiRunGit(t, "", "init", "--initial-branch=main", work)
	apiRunGit(t, work, "config", "user.name", "Restore Author")
	apiRunGit(t, work, "config", "user.email", "restore@example.invalid")
	noErr(t, os.WriteFile(filepath.Join(work, "file.txt"), []byte("source\n"), 0o600))
	apiRunGit(t, work, "add", ".")
	apiRunGit(t, work, "commit", "-m", "source")
	sourceOID := apiGitOutput(t, work, "rev-parse", "HEAD")
	noErr(t, os.WriteFile(filepath.Join(work, "file.txt"), []byte("target\n"), 0o600))
	apiRunGit(t, work, "commit", "-am", "target")
	apiRunGit(t, work, "push", remote, "HEAD:refs/heads/main")

	server := httptest.NewServer(app.Handler())
	defer server.Close()
	client, jar := newBrowserClient(t)
	restoreURL := server.URL + "/repositories/protected-restore/restore?source=" + sourceOID + "&target=main"
	_, status := dashboardGET(t, client, restoreURL)
	if status != http.StatusSeeOther {
		t.Fatalf("unauthenticated protected restore status=%d, want login redirect", status)
	}
	_, status = restorePOST(t, client, server.URL+"/repositories/protected-restore/restore", url.Values{
		"source": {sourceOID}, "target": {"main"}, "mode": {"all"}, "confirm": {"restore"},
	}, server.URL)
	if status != http.StatusSeeOther {
		t.Fatalf("unauthenticated protected restore apply status=%d, want login redirect", status)
	}

	loginURL := server.URL + "/login?next=" + url.QueryEscape("/repositories/protected-restore/restore?source="+sourceOID+"&target=main")
	if _, status := dashboardGET(t, client, loginURL); status != http.StatusOK {
		t.Fatalf("general login page status=%d", status)
	}
	csrf := cookieValue(t, jar, server.URL, preauthCookie)
	response := request(t, client, http.MethodPost, server.URL+"/login", url.Values{
		"csrf": {csrf}, "password": {"shared-password"}, "next": {"/repositories/protected-restore/restore?source=" + sourceOID + "&target=main"},
	}, server.URL)
	if response.StatusCode != http.StatusSeeOther {
		t.Fatalf("general login status=%d", response.StatusCode)
	}
	parsed, _ := url.Parse(server.URL)
	for _, cookie := range jar.Cookies(parsed) {
		if cookie.Name == adminCookie {
			t.Fatal("general login unexpectedly created an administrator session")
		}
	}
	body, status := dashboardGET(t, client, restoreURL)
	if status != http.StatusOK || !strings.Contains(body, sourceOID[:10]) {
		t.Fatalf("general-only restore status=%d source_visible=%v", status, strings.Contains(body, sourceOID[:10]))
	}
	preview, err := app.Repositories.PreviewRestore(context.Background(), "protected-restore", repository.RestoreRequest{
		Source: sourceOID, Target: "main", Mode: repository.RestoreAll,
	})
	noErr(t, err)
	generalToken := cookieValue(t, jar, server.URL, generalCookie)
	generalSession, ok, err := store.Session(context.Background(), generalToken, "general", time.Now())
	if err != nil || !ok {
		t.Fatalf("general session lookup: ok=%v err=%v", ok, err)
	}
	_, status = restorePOST(t, client, server.URL+"/repositories/protected-restore/restore", url.Values{
		"csrf": {generalSession.CSRF}, "source": {sourceOID}, "target": {"main"}, "mode": {"all"},
		"expected_head": {preview.ExpectedHead}, "confirm": {"restore"},
	}, server.URL)
	if status != http.StatusSeeOther {
		t.Fatalf("general-only restore apply status=%d", status)
	}
}

func restorePOST(t *testing.T, client *http.Client, target string, values url.Values, origin string) (string, int) {
	t.Helper()
	result := browserForm(t, client, target, values, origin)
	return result.body, result.status
}
