package server

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"reflect"
	"strings"
	"testing"

	"owngit/internal/webui"
)

// A repository's extra ref namespaces are saved from its Settings tab and
// the API alike, refused when they overlap branches, tags, OwnGit's refs or
// each other in any letter case, and warned about when not empty. The
// repository read names them, and an unreadable list names its setting.
func TestExtraRefNamespacesAreSavedFromTheSettingsTabAndTheAPI(t *testing.T) {
	fixture := newAPIFixture(t, false)
	ctx := context.Background()
	server, client, jar := openBrowser(t, fixture)
	signInAdmin(t, fixture, server.URL, jar)
	saved := func() []string {
		t.Helper()
		prefixes, err := fixture.store.RepositoryExtraRefPrefixes(ctx, "project")
		noErr(t, err)
		return prefixes
	}
	page := browserGET(t, client, server.URL+"/repositories/project/settings")
	if !strings.Contains(page.body, `action="/repositories/project/settings/ref-namespaces"`) || !strings.Contains(page.body, enText(webui.MsgNamespacesUnkept)) {
		t.Fatalf("the Settings tab lacks the namespaces:\n%s", page.body)
	}
	target := server.URL + "/repositories/project/settings/ref-namespaces"
	for _, refused := range []string{"refs/Heads/", "refs/notes/\nrefs/Notes/", "refs/owngit/x/", "notes"} {
		result := browserForm(t, client, target, url.Values{"csrf": {adminTestCSRF}, "extra_ref_prefixes": {refused}}, server.URL)
		if result.status != http.StatusUnprocessableEntity || !strings.Contains(result.body, enText(webui.MsgNamespacesInvalid)) {
			t.Fatalf("%q: status=%d", refused, result.status)
		}
	}
	if got := saved(); got != nil {
		t.Fatalf("a refused save saved %q", got)
	}
	result := browserForm(t, client, target, url.Values{"csrf": {adminTestCSRF}, "extra_ref_prefixes": {"refs/notes/\r\nrefs/meta/\n"}}, server.URL)
	if result.status != http.StatusSeeOther || result.header.Get("Location") != "/repositories/project/settings?notice=namespaces_saved_unkept" {
		t.Fatalf("save status=%d location=%q", result.status, result.header.Get("Location"))
	}
	if got := saved(); !reflect.DeepEqual(got, []string{"refs/notes/", "refs/meta/"}) {
		t.Fatalf("saved %q", got)
	}
	page = browserGET(t, client, server.URL+"/repositories/project/settings?notice=namespaces_saved_unkept")
	if !strings.Contains(page.body, "refs/notes/\nrefs/meta/</textarea>") || !strings.Contains(page.body, enText(webui.MsgNamespacesSaved)) {
		t.Fatalf("the Settings tab after saving:\n%s", page.body)
	}

	status, answer, problem := repositorySettingsAPI(t, server.URL, "project", http.MethodPatch, map[string]any{"protect_default_branch": true})
	if status != http.StatusOK || !reflect.DeepEqual(*answer.Settings.ExtraRefPrefixes, []string{"refs/notes/", "refs/meta/"}) {
		t.Fatalf("PATCH of the protection status=%d answer=%+v problem=%s", status, answer, problem)
	}
	if status, _, problem := repositorySettingsAPI(t, server.URL, "project", http.MethodPatch, map[string]any{"extra_ref_prefixes": []string{"refs/tags/"}}); status != http.StatusBadRequest {
		t.Fatalf("PATCH overlapping tags status=%d problem=%s", status, problem)
	}
	status, answer, _ = repositorySettingsAPI(t, server.URL, "project", http.MethodPatch, map[string]any{"extra_ref_prefixes": []string{"refs/notes/"}})
	if status != http.StatusOK || len(answer.Warnings) != 1 || answer.Warnings[0] != enText(webui.MsgNamespacesUnkept) {
		t.Fatalf("PATCH notes status=%d answer=%+v", status, answer)
	}

	show := func() map[string]any {
		t.Helper()
		response := apiRequest(t, http.MethodGet, server.URL+"/api/v1/repositories/project", nil, "", "")
		defer response.Body.Close()
		body, err := io.ReadAll(response.Body)
		noErr(t, err)
		var decoded struct {
			Repository map[string]any `json:"repository"`
		}
		noErr(t, json.Unmarshal(body, &decoded))
		return decoded.Repository
	}
	if got := show()["push_ref_namespaces"]; !reflect.DeepEqual(got, []any{"refs/heads/", "refs/tags/", "refs/notes/"}) {
		t.Fatalf("repository read push_ref_namespaces=%v", got)
	}
	noErr(t, fixture.store.Exec(ctx, `UPDATE repository_policies SET extra_ref_prefixes='["refs/heads/"]' WHERE repository_id='project'`))
	if got := show(); got["push_ref_namespaces"] != nil || !strings.Contains(got["push_ref_namespaces_error"].(string), "--extra-ref-prefixes") {
		t.Fatalf("repository read with unreadable namespaces: %v", got)
	}
	if status, _, problem := repositorySettingsAPI(t, server.URL, "project", http.MethodGet, nil); status != http.StatusConflict || !strings.HasPrefix(problem, "setting_unreadable extra_ref_prefixes ") {
		t.Fatalf("GET with unreadable namespaces status=%d problem=%s", status, problem)
	}
	page = browserGET(t, client, server.URL+"/repositories/project/settings")
	if !strings.Contains(page.body, enText(webui.MsgNamespacesUnreadable)) {
		t.Fatalf("the Settings tab does not say the namespaces cannot be read:\n%s", page.body)
	}
}
