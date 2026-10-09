package server

import (
	"bytes"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"owngit/internal/pullrequest"
	"owngit/internal/repository"
	"owngit/internal/webui"
)

func TestUnsupportedExactNamespaceDoesNotAliasBranch(t *testing.T) {
	fixture := newAPIFixture(t, false)
	apiRunGit(t, fixture.work, "push", "origin", fixture.sourceOID+":refs/heads/refs/notes/topic")
	server, client, _ := openBrowser(t, fixture)
	if _, _, err := fixture.app.Repositories.ResolveRef(t.Context(), "project", "refs/notes/topic"); !errors.Is(err, repository.ErrNotFound) {
		t.Fatalf("unsupported exact namespace resolved: %v", err)
	}
	for _, ref := range []string{"refs/notes/topic", "refs/heads/refs/notes/topic"} {
		want := http.StatusNotFound
		if ref == "refs/heads/refs/notes/topic" {
			want = http.StatusOK
		}
		for _, endpoint := range []string{"/code", "/raw", "/archive", "/commits"} {
			query := url.Values{"ref": {ref}}
			if endpoint == "/raw" {
				query.Set("path", "feature.txt")
			}
			if endpoint == "/archive" {
				query.Set("format", "zip")
			}
			page := browserGET(t, client, server.URL+"/repositories/project"+endpoint+"?"+query.Encode())
			if page.status != want {
				t.Fatalf("%s ref=%s status=%d, want %d", endpoint, ref, page.status, want)
			}
		}
	}
}

func TestInvalidExactCompareQueriesKeepTheOppositeChoice(t *testing.T) {
	fixture := newAPIFixture(t, false)
	server, client, _ := openBrowser(t, fixture)
	for _, lang := range []string{"en", "ko"} {
		for _, field := range []string{"source", "target"} {
			for _, value := range []string{"", "not-a-full-ref", "refs/tags/main", "refs/heads/../main", "refs/heads/" + strings.Repeat("a", 256)} {
				query := url.Values{"source_ref": {"refs/heads/feature"}, "target_ref": {"refs/heads/main"}, "lang": {lang}}
				query.Set(field+"_ref", value)
				page := browserGET(t, client, server.URL+"/repositories/project/pull-requests/new?"+query.Encode())
				if page.status != http.StatusUnprocessableEntity {
					t.Fatalf("%s invalid %s=%q status=%d", lang, field, value, page.status)
				}
				selectField := func(id string) string {
					start := strings.Index(page.body, `<select id="pr-`+id+`"`)
					if start < 0 {
						t.Fatalf("missing %s select", id)
					}
					body := page.body[start:]
					return body[:strings.Index(body, "</select>")]
				}
				invalid := selectField(field)
				if !strings.Contains(invalid, `aria-invalid="true"`) || !strings.Contains(invalid, `<option value="" selected disabled>`) {
					t.Fatalf("invalid %s chose an existing branch", field)
				}
				opposite, ref := "target", "refs/heads/main"
				if field == "target" {
					opposite, ref = "source", "refs/heads/feature"
				}
				valid := selectField(opposite)
				if strings.Contains(valid, `aria-invalid="true"`) || !strings.Contains(valid, `<option value="`+ref+`" selected>`) {
					t.Fatal("valid opposite choice was lost")
				}
				if strings.Contains(page.body, `name="source_oid"`) {
					t.Fatal("invalid comparison offered a create form")
				}
			}
		}
	}
	for _, query := range []url.Values{
		{"source_ref": {"not-full"}, "target_ref": {"refs/tags/main"}},
		{"source": {""}, "target": {""}},
	} {
		page := browserGET(t, client, server.URL+"/repositories/project/pull-requests/new?"+query.Encode())
		if page.status != http.StatusUnprocessableEntity || strings.Count(page.body, `<option value="" selected disabled>`) != 2 {
			t.Fatal("supplied invalid or empty pair fell back to defaults")
		}
	}
}

func TestPullRequestAdviceValuesAreAcceptedByAPI(t *testing.T) {
	for _, field := range []string{"source_branch", "target_branch"} {
		t.Run(field, func(t *testing.T) {
			fixture := newAPIFixture(t, false)
			first := "refs/heads/refs/owngit/topic"
			second := "refs/heads/refs/heads/refs/owngit/topic"
			apiRunGit(t, fixture.work, "push", "origin", fixture.targetOID+":"+first)
			apiRunGit(t, fixture.work, "push", "origin", fixture.sourceOID+":"+second)
			server := serve(t, fixture.app.Handler())
			endpoint := server.URL + "/api/v1/repositories/project/pull-requests"
			input := map[string]string{"title": "Accepted advice", "source_branch": "feature", "target_branch": "main"}
			input[field] = first
			response := apiRequest(t, http.MethodPost, endpoint, input, "", "")
			if response.StatusCode != http.StatusUnprocessableEntity {
				t.Fatalf("ambiguity status=%d", response.StatusCode)
			}
			problem := decodeAPIObject(t, response)["error"].(map[string]any)
			_, err := fixture.app.PullRequests.Create(t.Context(), pullrequest.CreateInput{
				Repository: "project", Title: input["title"], SourceBranch: input["source_branch"], TargetBranch: input["target_branch"],
			})
			var ambiguous *repository.AmbiguousBranchError
			if !errors.As(err, &ambiguous) || problem["code"] != "ambiguous_branch" || problem["message"] != pullrequest.AsProblem(err).Message {
				t.Fatalf("API and shared advice disagree: %v / %v", problem, err)
			}
			for index, value := range ambiguous.Values {
				if value == "" {
					continue
				}
				input[field] = value
				response = apiRequest(t, http.MethodPost, endpoint, input, "", "")
				if response.StatusCode != http.StatusOK {
					t.Fatalf("offered value %q is refused: status=%d", value, response.StatusCode)
				}
				answer := decodeAPIObject(t, response)["pull_request"].(map[string]any)
				revision := "source"
				if field == "target_branch" {
					revision = "target"
				}
				if answer[revision].(map[string]any)["branch"] != strings.TrimPrefix(ambiguous.Refs[index], "refs/heads/") {
					t.Fatal("accepted PR advice selected a different branch")
				}
			}
		})
	}
}

func TestDefaultBranchAdviceRespectsCallerEligibility(t *testing.T) {
	fixture := newAPIFixture(t, false)
	first := "refs/heads/refs/heads/HEAD"
	second := "refs/heads/refs/heads/refs/heads/HEAD"
	apiRunGit(t, fixture.work, "push", "origin", fixture.targetOID+":"+first)
	apiRunGit(t, fixture.work, "push", "origin", fixture.sourceOID+":"+second)
	server := serve(t, fixture.app.Handler())
	endpoint := server.URL + "/api/v1/repositories/project/default-branch"
	response := adminAPIRequest(t, http.MethodPost, endpoint, map[string]string{"branch": first}, "admin-password")
	if response.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("ambiguity status=%d", response.StatusCode)
	}
	problem := decodeAPIObject(t, response)["error"].(map[string]any)
	_, err := fixture.app.Repositories.SetDefaultBranchInput(t.Context(), "project", first, false)
	var ambiguous *repository.AmbiguousBranchError
	if !errors.As(err, &ambiguous) || problem["code"] != "ambiguous_branch" || problem["message"] != ambiguous.Error() {
		t.Fatalf("API and shared advice disagree: %v / %v", problem, err)
	}
	if len(ambiguous.Values) != 2 || ambiguous.Values[0] != "refs/heads/HEAD" || ambiguous.Values[1] == "" {
		t.Fatalf("advice omits a legal HEAD operand: %v", ambiguous.Values)
	}
	for index, value := range ambiguous.Values {
		response = adminAPIRequest(t, http.MethodPost, endpoint, map[string]string{"branch": value}, "admin-password")
		if response.StatusCode != http.StatusOK {
			t.Fatalf("offered value %q is refused: status=%d", value, response.StatusCode)
		}
		answer := decodeAPIObject(t, response)
		if answer["default_branch"] != strings.TrimPrefix(ambiguous.Refs[index], "refs/heads/") || apiGitOutput(t, fixture.remote, "symbolic-ref", "HEAD") != ambiguous.Refs[index] {
			t.Fatal("accepted advice selected a different branch")
		}
	}
	full, err := fixture.app.Repositories.SetDefaultBranchInput(t.Context(), "project", first, true)
	noErr(t, err)
	if full != first || apiGitOutput(t, fixture.remote, "symbolic-ref", "HEAD") != first {
		t.Fatal("exact browser control could not select the legal literal branch")
	}
}

func TestDefaultBranchRefCollision(t *testing.T) {
	fixture := newAPIFixture(t, false)
	shortOID := apiGitOutput(t, fixture.remote, "rev-parse", "refs/heads/main")
	longOID := apiGitOutput(t, fixture.remote, "rev-parse", "refs/heads/feature")
	apiRunGit(t, fixture.work, "push", "origin", shortOID+":refs/heads/x")
	apiRunGit(t, fixture.work, "push", "origin", longOID+":refs/heads/refs/heads/x")
	server, client, jar := openBrowser(t, fixture)
	signInAdmin(t, fixture, server.URL, jar)
	target := server.URL + "/api/v1/repositories/project/default-branch"
	response := adminAPIRequest(t, http.MethodPost, target, map[string]string{"branch": "refs/heads/x"}, "admin-password")
	if response.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("ambiguous branch: status=%d, want 422", response.StatusCode)
	}
	problem := decodeAPIObject(t, response)["error"].(map[string]any)
	message, _ := problem["message"].(string)
	if problem["code"] != "ambiguous_branch" || !strings.Contains(message, "refs/heads/x: x") || !strings.Contains(message, "refs/heads/refs/heads/x: refs/heads/refs/heads/x") {
		t.Fatalf("ambiguity did not explain both choices: %v", problem)
	}
	if got := apiGitOutput(t, fixture.remote, "symbolic-ref", "HEAD"); got != "refs/heads/main" {
		t.Fatalf("refused selection changed HEAD: %s", got)
	}
	answer := decodeAPIObject(t, adminAPIRequest(t, http.MethodPost, target, map[string]string{"branch": "refs/heads/refs/heads/x"}, "admin-password"))
	if answer["default_branch"] != "refs/heads/x" {
		t.Fatalf("selected branch=%v", answer)
	}
	assertClone := func(label, full, oid string) {
		t.Helper()
		if got := apiGitOutput(t, fixture.remote, "symbolic-ref", "HEAD"); got != full {
			t.Fatalf("%s HEAD=%s, want %s", label, got, full)
		}
		clone := filepath.Join(t.TempDir(), "clone")
		apiRunGit(t, "", "clone", server.URL+"/git/project.git", clone)
		if got := apiGitOutput(t, clone, "symbolic-ref", "HEAD"); got != full {
			t.Fatalf("%s clone branch=%s, want %s", label, got, full)
		}
		if got := apiGitOutput(t, clone, "rev-parse", "HEAD"); got != oid {
			t.Fatalf("%s clone commit=%s, want %s", label, got, oid)
		}
		apiRunGit(t, clone, "fetch")
	}
	assertClone("API", "refs/heads/refs/heads/x", longOID)
	settings := server.URL + "/repositories/project/settings"
	page := browserGET(t, client, settings)
	if page.status != http.StatusOK || !strings.Contains(page.body, `name="branch_ref"`) || !strings.Contains(page.body, `value="refs/heads/refs/heads/x" selected`) {
		t.Fatalf("settings does not carry the exact selected ref: status=%d", page.status)
	}
	result := browserForm(t, client, settings+"/default-branch", url.Values{"csrf": {adminTestCSRF}, "branch_ref": {"refs/heads/x"}}, server.URL)
	if result.status != http.StatusSeeOther {
		t.Fatalf("exact settings save status=%d", result.status)
	}
	assertClone("short Settings", "refs/heads/x", shortOID)
	result = browserForm(t, client, settings+"/default-branch", url.Values{"csrf": {adminTestCSRF}, "branch_ref": {"refs/heads/refs/heads/x"}}, server.URL)
	if result.status != http.StatusSeeOther {
		t.Fatalf("long settings save status=%d", result.status)
	}
	assertClone("long Settings", "refs/heads/refs/heads/x", longOID)
	for _, lang := range []string{"en", "ko"} {
		result = browserForm(t, client, settings+"/default-branch", url.Values{"csrf": {adminTestCSRF}, "branch": {"refs/heads/x"}, "lang": {lang}}, server.URL)
		if result.status != http.StatusUnprocessableEntity || !strings.Contains(result.body, "refs/heads/x: x") || !strings.Contains(result.body, "refs/heads/refs/heads/x: refs/heads/refs/heads/x") {
			t.Fatalf("%s browser ambiguity status=%d", lang, result.status)
		}
	}
}

func TestSharedSelectionPinsUncachedSnapshot(t *testing.T) {
	if testing.Short() {
		t.Skip("lists 12000 refs through real Git")
	}
	if runtime.GOOS == "windows" {
		t.Skip("the tracing wrapper is a POSIX-shell fixture")
	}
	fixture := newAPIFixture(t, false)
	apiRunGit(t, fixture.work, "push", "origin", fixture.targetOID+":refs/heads/x")
	apiRunGit(t, fixture.work, "push", "origin", fixture.sourceOID+":refs/heads/refs/heads/x")
	var refs strings.Builder
	for index := 0; index < 12_000; index++ {
		fmt.Fprintf(&refs, "create refs/heads/%s-%06d %s\n", strings.Repeat("a", 220), index, fixture.targetOID)
	}
	_, err := fixture.app.Repositories.Git.Run(t.Context(), fixture.remote, strings.NewReader(refs.String()), "--git-dir", ".", "update-ref", "--stdin")
	noErr(t, err)
	trace := traceGitCommands(t, fixture.app)
	snapshot, err := fixture.app.Repositories.RefSnapshot(t.Context(), "project")
	noErr(t, err)
	_, err = fixture.app.Repositories.RefSnapshot(t.Context(), "project")
	noErr(t, err)
	commands, err := os.ReadFile(trace)
	noErr(t, err)
	if got := bytes.Count(commands, []byte("\x00for-each-ref\x00")); got != 2 {
		t.Fatalf("oversized fixture remained cached: listings=%d, want 2", got)
	}
	stored, _, err := fixture.store.Repository(t.Context(), "project")
	noErr(t, err)
	server := serve(t, fixture.app.Handler())
	apiRunGit(t, fixture.work, "push", server.URL+"/git/project.git", ":refs/heads/refs/heads/x")
	before, err := os.ReadFile(trace)
	noErr(t, err)
	request := httptest.NewRequest(http.MethodGet, "/repositories/project/code", nil)
	page := fixture.app.baseRepositoryPage(request, webui.Chrome{}, stored, snapshot.Summary)
	full, resolved, err := fixture.app.selectRef(request, &page, snapshot, "refs/heads/refs/heads/x")
	if err != nil || !resolved || full != "refs/heads/refs/heads/x" || page.Ref.Revision != fixture.sourceOID {
		t.Fatalf("uncached deleted selection = %s %v %+v %v", full, resolved, page.Ref, err)
	}
	after, err := os.ReadFile(trace)
	noErr(t, err)
	if !bytes.Equal(before, after) {
		t.Fatal("selection started another Git process instead of using the supplied snapshot")
	}
}

func TestBrowsePagesUseExactSnapshotIdentity(t *testing.T) {
	fixture := newAPIFixture(t, false)
	apiRunGit(t, fixture.work, "push", "origin", fixture.targetOID+":refs/heads/x")
	apiRunGit(t, fixture.work, "push", "origin", fixture.sourceOID+":refs/heads/refs/heads/x")
	snapshot, err := fixture.app.Repositories.RefSnapshot(t.Context(), "project")
	noErr(t, err)
	stored, _, err := fixture.store.Repository(t.Context(), "project")
	noErr(t, err)
	server := serve(t, fixture.app.Handler())
	apiRunGit(t, fixture.work, "push", server.URL+"/git/project.git", ":refs/heads/refs/heads/x")
	for _, selected := range []struct{ ref, oid string }{
		{"refs/heads/x", fixture.targetOID},
		{"refs/heads/refs/heads/x", fixture.sourceOID},
	} {
		for _, tab := range [][]string{nil, {"code"}, {"commits"}} {
			request := httptest.NewRequest(http.MethodGet, "/repositories/project?"+url.Values{"ref": {selected.ref}}.Encode(), nil)
			page := fixture.app.baseRepositoryPage(request, webui.Chrome{}, stored, snapshot.Summary)
			full, resolved, err := fixture.app.selectRef(request, &page, snapshot, selected.ref)
			if err != nil || !resolved || full != selected.ref || page.Ref.Revision != selected.oid {
				t.Fatalf("snapshot selection %s = %s %v %+v %v", selected.ref, full, resolved, page.Ref, err)
			}
			writer := httptest.NewRecorder()
			page = fixture.app.baseRepositoryPage(request, webui.Chrome{}, stored, snapshot.Summary)
			fixture.app.serveCodePage(writer, request, page, snapshot, tab, pageAddress{Ref: selected.ref})
			if writer.Code != http.StatusOK {
				t.Fatalf("in-flight %v selection %s status=%d", tab, selected.ref, writer.Code)
			}
		}
	}
	fresh, err := fixture.app.Repositories.RefSnapshot(t.Context(), "project")
	noErr(t, err)
	for _, tab := range [][]string{nil, {"code"}, {"commits"}} {
		request := httptest.NewRequest(http.MethodGet, "/repositories/project", nil)
		page := fixture.app.baseRepositoryPage(request, webui.Chrome{}, stored, fresh.Summary)
		writer := httptest.NewRecorder()
		fixture.app.serveCodePage(writer, request, page, fresh, tab, pageAddress{Ref: "refs/heads/refs/heads/x"})
		if writer.Code != http.StatusNotFound {
			t.Fatalf("fresh deleted %v status=%d", tab, writer.Code)
		}
	}
}

func TestDefaultBranchRefusalKeepsExactChoice(t *testing.T) {
	fixture := newAPIFixture(t, false)
	apiRunGit(t, fixture.work, "push", "origin", fixture.targetOID+":refs/heads/a")
	apiRunGit(t, fixture.work, "push", "origin", fixture.sourceOID+":refs/heads/refs/heads/a")
	server, client, jar := openBrowser(t, fixture)
	if status, _ := checkStatus(t, adminAPIRequest(t, http.MethodPut, server.URL+"/api/v1/settings/admin-confirmation", map[string]string{"admin_confirmation": "every"}, "admin-password")); status != http.StatusOK {
		t.Fatalf("confirmation setup status=%d", status)
	}
	signInAdmin(t, fixture, server.URL, jar)
	settings := server.URL + "/repositories/project/settings"
	result := browserForm(t, client, settings+"/default-branch", url.Values{
		"csrf": {adminTestCSRF}, "branch_ref": {"refs/heads/a"}, "admin_password": {"wrong-password"},
	}, server.URL)
	if result.status != http.StatusUnauthorized || !strings.Contains(result.body, `<option value="refs/heads/a" selected>a</option>`) || strings.Contains(result.body, `<option value="refs/heads/refs/heads/a" selected>`) {
		t.Fatalf("refused exact choice was reinterpreted: status=%d", result.status)
	}
	if got := apiGitOutput(t, fixture.remote, "symbolic-ref", "HEAD"); got != "refs/heads/main" {
		t.Fatalf("refused choice changed HEAD=%s", got)
	}
}

func TestSettingsDefaultBranchExactIdentity(t *testing.T) {
	fixture := newAPIFixture(t, false)
	apiRunGit(t, fixture.work, "push", "origin", fixture.targetOID+":refs/heads/x")
	apiRunGit(t, fixture.work, "push", "origin", fixture.sourceOID+":refs/heads/refs/heads/x", fixture.sourceOID+":refs/heads/HEAD", fixture.sourceOID+":refs/heads/-dash")
	server, client, jar := openBrowser(t, fixture)
	signInAdmin(t, fixture, server.URL, jar)
	settings := server.URL + "/repositories/project/settings"
	for _, name := range []string{"refs/heads/x", "HEAD", "-dash"} {
		full := "refs/heads/" + name
		page := browserGET(t, client, settings)
		if !strings.Contains(page.body, `<option value="`+full+`">`+name+`</option>`) {
			t.Fatalf("Settings did not offer %q", name)
		}
		result := browserForm(t, client, settings+"/default-branch", url.Values{"csrf": {adminTestCSRF}, "branch_ref": {full}}, server.URL)
		if result.status != http.StatusSeeOther {
			t.Fatalf("Settings save %q status=%d", name, result.status)
		}
		clone := filepath.Join(t.TempDir(), "clone")
		apiRunGit(t, "", "clone", server.URL+"/git/project.git", clone)
		if head, oid := apiGitOutput(t, clone, "symbolic-ref", "HEAD"), apiGitOutput(t, clone, "rev-parse", "HEAD"); head != full || oid != fixture.sourceOID {
			t.Fatalf("Settings selected %s, clone got %s at %s instead of %s", full, head, oid, fixture.sourceOID)
		}
	}
}

func TestPullRequestBrowserUsesExactRefPair(t *testing.T) {
	fixture := newAPIFixture(t, false)
	apiRunGit(t, fixture.work, "push", "origin", fixture.targetOID+":refs/heads/x")
	apiRunGit(t, fixture.work, "push", "origin", fixture.sourceOID+":refs/heads/refs/heads/x")
	server, client, _ := openBrowser(t, fixture)
	base := server.URL + "/repositories/project/pull-requests"
	for index, pair := range []struct{ source, target, sourceOID, targetOID string }{
		{"refs/heads/refs/heads/x", "refs/heads/x", fixture.sourceOID, fixture.targetOID},
		{"refs/heads/x", "refs/heads/refs/heads/x", fixture.targetOID, fixture.sourceOID},
	} {
		query := url.Values{"source_ref": {pair.source}, "target_ref": {pair.target}}
		page := browserGET(t, client, base+"/new?"+query.Encode())
		if page.status != http.StatusOK || !strings.Contains(page.body, `name="source_ref" value="`+pair.source+`"`) || !strings.Contains(page.body, `name="target_ref" value="`+pair.target+`"`) {
			t.Fatalf("comparison lost exact branch pair: status=%d", page.status)
		}
		result := browserForm(t, client, base, url.Values{
			"csrf": {pageCSRF(t, page.body)}, "title": {"Exact branches"},
			"source_ref": {pair.source}, "target_ref": {pair.target}, "source_oid": {pair.sourceOID}, "target_oid": {pair.targetOID},
		}, server.URL)
		if result.status != http.StatusSeeOther {
			t.Fatalf("exact pair creation status=%d", result.status)
		}
		view, err := fixture.app.PullRequests.Show(t.Context(), "project", int64(index+1))
		noErr(t, err)
		if view.Source.Branch != strings.TrimPrefix(pair.source, "refs/heads/") || view.Target.Branch != strings.TrimPrefix(pair.target, "refs/heads/") || view.Source.OID != pair.sourceOID || view.Target.OID != pair.targetOID {
			t.Fatalf("stored pair=%+v / %+v", view.Source, view.Target)
		}
	}
	missing := browserForm(t, client, base, url.Values{
		"csrf": {pageCSRF(t, browserGET(t, client, base+"/new?source_ref=refs%2Fheads%2Fx&target_ref=refs%2Fheads%2Fmain").body)}, "title": {"Missing exact source"},
		"source_ref": {"refs/heads/missing"}, "target_ref": {"refs/heads/x"},
		"source_branch": {"refs/heads/x"}, "target_branch": {"main"},
		"source_oid": {fixture.sourceOID}, "target_oid": {fixture.targetOID},
	}, server.URL)
	if missing.status != http.StatusConflict {
		t.Fatalf("missing exact source fell back or changed status=%d", missing.status)
	}
	for _, field := range []string{"source_branch", "target_branch"} {
		input := map[string]string{"title": "Ambiguous pair", "source_branch": "x", "target_branch": "main"}
		input[field] = "refs/heads/x"
		response := apiRequest(t, http.MethodPost, server.URL+"/api/v1/repositories/project/pull-requests", input, "", "")
		if status, code := checkStatus(t, response); status != http.StatusUnprocessableEntity || code != "ambiguous_branch" {
			t.Fatalf("%s API status=%d code=%s", field, status, code)
		}
	}
}
