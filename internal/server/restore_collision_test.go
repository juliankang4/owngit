package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"owngit/internal/webui"
)

func pushRestoreCollisionHistory(t *testing.T, app *App) (remote, source, target, safe string) {
	t.Helper()
	_, err := app.Repositories.Create(context.Background(), "restore-collision", "")
	noErr(t, err)
	remote, err = app.Repositories.Path("restore-collision")
	noErr(t, err)
	object := func(input string, arguments ...string) string {
		t.Helper()
		result, err := app.Repositories.Git.Run(context.Background(), remote, strings.NewReader(input), append([]string{"--git-dir", "."}, arguments...)...)
		noErr(t, err)
		return strings.TrimSpace(string(result.Stdout))
	}
	blob := object("unselected content\n", "hash-object", "-w", "--stdin")
	sourceTree := object("100644 blob "+blob+"\tnode\n", "mktree")
	childTree := object("100644 blob "+blob+"\tchild\n", "mktree")
	targetTree := object("040000 tree "+childTree+"\tnode\n100644 blob "+blob+"\tnode-neighbor\n", "mktree")
	safeTree := object("", "mktree")
	commitArguments := []string{"-c", "user.name=Restore Fixture", "-c", "user.email=restore@example.invalid", "commit-tree"}
	source = object("source\n", append(commitArguments, sourceTree)...)
	target = object("target\n", append(commitArguments, targetTree, "-p", source)...)
	safe = object("safe target\n", append(commitArguments, safeTree, "-p", source)...)
	apiRunGit(t, remote, "--git-dir", ".", "update-ref", "refs/heads/main", target)
	return remote, source, target, safe
}

func TestRestoreBrowserAndAPIRefuseUnselectedDescendantCollision(t *testing.T) {
	for _, surface := range []string{"browser", "API"} {
		t.Run(surface, func(t *testing.T) {
			app := newConfiguredApp(t)
			remote, source, target, safe := pushRestoreCollisionHistory(t, app)
			server := httptest.NewServer(app.Handler())
			defer server.Close()
			client, jar := newBrowserClient(t)
			if _, status := dashboardGET(t, client, server.URL+"/"); status != http.StatusOK {
				t.Fatalf("overview status=%d", status)
			}
			csrf := cookieValue(t, jar, server.URL, generalCookie)
			base := server.URL + "/repositories/restore-collision/restore"
			send := func(preview bool, expected string, wantStatus int, wantCode string) {
				t.Helper()
				if surface == "API" {
					endpoint := server.URL + "/api/v1/repositories/restore-collision/restore"
					input := map[string]any{"source_oid": source, "target_branch": "main", "mode": "files", "paths": []string{"node"}}
					if preview {
						endpoint += "/preview"
					} else {
						input["expected_head"] = expected
					}
					status, body := restoreAPI(t, http.MethodPost, endpoint, input, "")
					if status != wantStatus || (wantCode != "" && restoreErrorCode(body) != wantCode) {
						t.Fatalf("API preview=%v status=%d body=%v, want %d %s", preview, status, body, wantStatus, wantCode)
					}
					if wantCode == "restore_unsupported" && !strings.Contains(body["error"].(map[string]any)["message"].(string), "selection would discard unselected descendants") {
						t.Errorf("API did not explain the collision refusal: %v", body)
					}
					return
				}
				endpoint := base
				input := url.Values{"csrf": {csrf}, "source": {source}, "target": {"main"}, "mode": {"files"}, "path": {"node"}}
				if preview {
					endpoint += "/preview"
				} else {
					input.Set("expected_head", expected)
					input.Set("confirm", "restore")
				}
				body, status := restorePOST(t, client, endpoint, input, server.URL)
				message := ""
				switch wantCode {
				case "restore_unsupported":
					message = webui.Text(webui.LangEN, webui.MsgRestoreUnsupported)
				case "stale_revision":
					message = webui.Text(webui.LangEN, webui.MsgRestoreConflict)
				}
				if status != wantStatus || (message != "" && !strings.Contains(body, message)) {
					t.Fatalf("browser preview=%v status=%d reason=%v, want %d %s", preview, status, strings.Contains(body, message), wantStatus, wantCode)
				}
				if wantCode == "restore_unsupported" && (strings.Contains(body, `name="confirm"`) || !strings.Contains(body, `id="path-note"`) || !strings.Contains(body, `name="path" value="node" checked`)) {
					t.Error("refused browser restore must retain the selection and its reason without offering apply")
				}
			}
			send(true, "", http.StatusUnprocessableEntity, "restore_unsupported")
			send(false, target, http.StatusUnprocessableEntity, "restore_unsupported")
			apiRunGit(t, remote, "--git-dir", ".", "update-ref", "refs/heads/main", safe, target)
			send(true, "", http.StatusOK, "")
			apiRunGit(t, remote, "--git-dir", ".", "update-ref", "refs/heads/main", target, safe)
			send(false, safe, http.StatusConflict, "stale_revision")
			send(false, target, http.StatusUnprocessableEntity, "restore_unsupported")
			if got := apiGitOutput(t, remote, "--git-dir", ".", "rev-parse", "refs/heads/main"); got != target {
				t.Errorf("refused restore changed branch=%s, want %s", got, target)
			}
			if got := apiGitOutput(t, remote, "--git-dir", ".", "show", "main:node/child"); got != "unselected content" {
				t.Errorf("unselected descendant content=%q", got)
			}
		})
	}
}
