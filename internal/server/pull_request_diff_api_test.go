package server

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"owngit/internal/pullrequest"
	"owngit/internal/repository"
)

func decodeDiff(t *testing.T, response *http.Response) pullrequest.Diff {
	t.Helper()
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		var envelope pullrequest.ErrorEnvelope
		_ = json.NewDecoder(response.Body).Decode(&envelope)
		t.Fatalf("diff status=%d error=%+v", response.StatusCode, envelope.Error)
	}
	var diff pullrequest.Diff
	noErr(t, json.NewDecoder(response.Body).Decode(&diff))
	if !diff.OK {
		t.Fatal("diff response reported ok=false")
	}
	return diff
}

func diffPaths(diff pullrequest.Diff) string {
	var paths []string
	for _, file := range diff.Files {
		paths = append(paths, file.Status+":"+file.Path)
	}
	return strings.Join(paths, ",")
}

func commitAndPush(t *testing.T, work, branch, name, content string) string {
	t.Helper()
	noErr(t, os.WriteFile(filepath.Join(work, name), []byte(content), 0o600))
	apiRunGit(t, work, "add", ".")
	apiRunGit(t, work, "commit", "-q", "-m", "change "+name)
	apiRunGit(t, work, "push", "-q", "origin", "HEAD:refs/heads/"+branch)
	return apiGitOutput(t, work, "rev-parse", "HEAD")
}

// The diff states the exact pair it read. By default that is the current
// pair; a pinned pair must be current or recorded, and a pinned pair the
// branches moved away from is still diffed, with the move reported.
func TestPullRequestDiffAPIPinsRevisions(t *testing.T) {
	fixture := newAPIFixture(t, false)
	server := serve(t, fixture.app.Handler())
	endpoint := server.URL + "/api/v1/repositories/project/pull-requests"
	created := decodeAPISuccess(t, apiRequest(t, http.MethodPost, endpoint, map[string]any{
		"title": "Feature", "source_branch": "feature", "target_branch": "main", "review": "request",
	}, "", ""))
	diffURL := endpoint + "/" + itoa(created.PullRequest.Number) + "/diff"
	pinned := func(source, target string) string {
		return diffURL + "?source_oid=" + source + "&target_oid=" + target
	}

	first := decodeDiff(t, apiRequest(t, http.MethodGet, diffURL, nil, "", ""))
	if first.Source.OID != fixture.sourceOID || first.Target.OID != fixture.targetOID || first.MergeBase != fixture.targetOID ||
		first.Source.Branch != "feature" || first.Target.Branch != "main" || first.State != "open" || first.Moved || first.Current != nil {
		t.Fatalf("first diff revisions: %+v", first)
	}
	if diffPaths(first) != "added:feature.txt" || first.Files[0].Additions != 1 || first.Truncated || first.Incomplete || first.Reason != "" ||
		!strings.HasPrefix(first.Patch, "diff --git a/feature.txt b/feature.txt\n") || !strings.Contains(first.Patch, "+feature\n") {
		t.Fatalf("first diff content: %+v", first)
	}

	// The source moves. The default follows it; the recorded pair stays
	// readable and says that it moved.
	movedSource := commitAndPush(t, fixture.work, "feature", "second.txt", "second\n")
	current := decodeDiff(t, apiRequest(t, http.MethodGet, diffURL, nil, "", ""))
	if current.Source.OID != movedSource || current.Moved || diffPaths(current) != "added:feature.txt,added:second.txt" {
		t.Fatalf("current diff: %+v", current)
	}
	old := decodeDiff(t, apiRequest(t, http.MethodGet, pinned(fixture.sourceOID, fixture.targetOID), nil, "", ""))
	if old.Source.OID != fixture.sourceOID || !old.Moved || old.Current == nil || old.Current.Source.OID != movedSource ||
		old.Current.Target.OID != fixture.targetOID || diffPaths(old) != "added:feature.txt" || strings.Contains(old.Patch, "second") {
		t.Fatalf("pinned recorded diff: %+v", old)
	}
	// The current pair may be pinned although nothing recorded it.
	if same := decodeDiff(t, apiRequest(t, http.MethodGet, pinned(movedSource, fixture.targetOID), nil, "", "")); same.Moved || same.Source.OID != movedSource {
		t.Fatalf("pinned current diff: %+v", same)
	}

	for _, refused := range []struct {
		target, code string
		status       int
	}{
		{pinned(fixture.targetOID, fixture.targetOID), "revision_not_recorded", http.StatusUnprocessableEntity},
		{pinned(fixture.targetOID, fixture.sourceOID), "revision_not_recorded", http.StatusUnprocessableEntity},
		{diffURL + "?source_oid=" + fixture.sourceOID, "invalid_revision", http.StatusUnprocessableEntity},
		{diffURL + "?target_oid=" + fixture.targetOID, "invalid_revision", http.StatusUnprocessableEntity},
		{pinned("HEAD", fixture.targetOID), "invalid_revision", http.StatusUnprocessableEntity},
		{pinned(strings.ToUpper(fixture.sourceOID), fixture.targetOID), "invalid_revision", http.StatusUnprocessableEntity},
		{pinned(fixture.sourceOID, fixture.targetOID) + "&source_oid=" + fixture.sourceOID, "invalid_request", http.StatusBadRequest},
		{diffURL + "?path=feature.txt", "invalid_request", http.StatusBadRequest},
		{endpoint + "/99/diff", "pull_request_not_found", http.StatusNotFound},
		{server.URL + "/api/v1/repositories/missing/pull-requests/1/diff", "repository_not_found", http.StatusNotFound},
	} {
		response := apiRequest(t, http.MethodGet, refused.target, nil, "", "")
		if response.StatusCode != refused.status || apiErrorCode(t, response) != refused.code {
			t.Errorf("%s: status=%d, want %d %s", refused.target, response.StatusCode, refused.status, refused.code)
		}
	}
	if response := apiRequest(t, http.MethodPost, diffURL, struct{}{}, "", ""); response.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("POST diff status=%d", response.StatusCode)
	}

	// Without the source branch there is no current pair, but a recorded
	// pair is kept and still readable.
	apiRunGit(t, fixture.work, "push", "-q", "origin", ":refs/heads/feature")
	if response := apiRequest(t, http.MethodGet, diffURL, nil, "", ""); response.StatusCode != http.StatusConflict || apiErrorCode(t, response) != "source_branch_missing" {
		t.Fatalf("diff without a source branch status=%d", response.StatusCode)
	}
	kept := decodeDiff(t, apiRequest(t, http.MethodGet, pinned(fixture.sourceOID, fixture.targetOID), nil, "", ""))
	if !kept.Moved || kept.Current.Source.Status != "missing" || kept.Current.Source.OID != "" || diffPaths(kept) != "added:feature.txt" {
		t.Fatalf("pinned diff without a source branch: %+v", kept)
	}

	// A merged pull request shows the pair it merged, whatever the branches
	// do afterwards.
	apiRunGit(t, fixture.work, "push", "-q", "origin", "HEAD:refs/heads/feature")
	merged := apiRequest(t, http.MethodPost, endpoint+"/"+itoa(created.PullRequest.Number)+"/merge", pullrequest.RevisionInput{
		SourceOID: movedSource, TargetOID: fixture.targetOID,
	}, "", "")
	if merged.StatusCode != http.StatusOK {
		t.Fatalf("merge status=%d error=%q", merged.StatusCode, apiErrorCode(t, merged))
	}
	commitAndPush(t, fixture.work, "feature", "third.txt", "third\n")
	after := decodeDiff(t, apiRequest(t, http.MethodGet, diffURL, nil, "", ""))
	if after.State != "merged" || after.Source.OID != movedSource || after.Target.OID != fixture.targetOID || after.Moved ||
		diffPaths(after) != "added:feature.txt,added:second.txt" {
		t.Fatalf("merged diff: %+v", after)
	}
}

// The diff is a pull request read: general access, never a browser cookie.
func TestPullRequestDiffAPIRequiresGeneralAccess(t *testing.T) {
	fixture := newAPIFixture(t, true)
	server := serve(t, fixture.app.Handler())
	endpoint := server.URL + "/api/v1/repositories/project/pull-requests"
	created := decodeAPISuccess(t, apiRequest(t, http.MethodPost, endpoint, map[string]any{
		"title": "Feature", "source_branch": "feature", "target_branch": "main", "review": "skip",
	}, "shared-password", ""))
	diffURL := endpoint + "/" + itoa(created.PullRequest.Number) + "/diff"
	for _, attempt := range []struct {
		password, code string
	}{
		{"", "authentication_required"}, {"wrong-password", "invalid_credentials"}, {"admin-password", "invalid_credentials"},
	} {
		response := apiRequest(t, http.MethodGet, diffURL, nil, attempt.password, "")
		if response.StatusCode != http.StatusUnauthorized || apiErrorCode(t, response) != attempt.code {
			t.Errorf("password %q: status=%d", attempt.password, response.StatusCode)
		}
	}
	// A refused caller learns nothing about which pull requests exist.
	if response := apiRequest(t, http.MethodGet, endpoint+"/99/diff", nil, "", ""); response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unknown pull request without a password status=%d", response.StatusCode)
	}
	if diff := decodeDiff(t, apiRequest(t, http.MethodGet, diffURL, nil, "shared-password", "")); diff.Source.OID != fixture.sourceOID {
		t.Fatalf("authorized diff: %+v", diff)
	}
}

// A diff that does not fit the response keeps whole files and says so.
func TestPullRequestDiffAPIFitsTheResponse(t *testing.T) {
	fixture := newAPIFixture(t, false)
	for _, name := range []string{"a.html", "b.html", "c.html"} {
		// Markup is escaped in JSON, so the encoded size is what counts.
		noErr(t, os.WriteFile(filepath.Join(fixture.work, name), []byte(strings.Repeat("<p>&amp;</p>\n", 40)), 0o600))
	}
	sourceOID := commitAndPush(t, fixture.work, "feature", "d.txt", "d\n")
	server := serve(t, fixture.app.Handler())
	endpoint := server.URL + "/api/v1/repositories/project/pull-requests"
	created := decodeAPISuccess(t, apiRequest(t, http.MethodPost, endpoint, map[string]any{
		"title": "Feature", "source_branch": "feature", "target_branch": "main", "review": "skip",
	}, "", ""))
	diffURL := endpoint + "/" + itoa(created.PullRequest.Number) + "/diff"
	full := decodeDiff(t, apiRequest(t, http.MethodGet, diffURL, nil, "", ""))
	if full.Truncated || strings.Count(full.Patch, "diff --git ") != 5 {
		t.Fatalf("full diff: %+v", full)
	}

	previous := maximumDiffResponse
	maximumDiffResponse = 3000
	t.Cleanup(func() { maximumDiffResponse = previous })
	response := apiRequest(t, http.MethodGet, diffURL, nil, "", "")
	body := readBody(t, response)
	if len(body) > 3000 {
		t.Fatalf("response has %d bytes, limit 3000", len(body))
	}
	var cut pullrequest.Diff
	noErr(t, json.Unmarshal(body, &cut))
	sections := strings.Count(cut.Patch, "diff --git ")
	if !cut.Truncated || cut.Incomplete || cut.Reason != "response_limit" || len(cut.Files) != 5 || sections == 0 || sections >= 5 ||
		!strings.HasPrefix(full.Patch, cut.Patch) || !strings.HasPrefix(full.Patch[len(cut.Patch):], "diff --git ") || cut.Source.OID != sourceOID {
		t.Fatalf("cut diff: sections=%d %+v", sections, cut)
	}
}

func readBody(t *testing.T, response *http.Response) []byte {
	t.Helper()
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	noErr(t, err)
	return body
}

// Git's own limits reach the response as flags and a reason, and the part of
// a cut patch that may be partial is left out.
func TestDiffFromComparisonReportsCuts(t *testing.T) {
	revisions := pullrequest.DiffRevisions{
		Repository: "project", Number: 1, State: "open",
		Source: pullrequest.Revision{Branch: "feature", OID: strings.Repeat("a", 40), Status: "commit"},
		Target: pullrequest.Revision{Branch: "main", OID: strings.Repeat("b", 40), Status: "commit"},
	}
	files := []repository.ChangedFile{{Path: "one.txt", Status: "added", Additions: 1}, {Path: "two.txt", Status: "modified", Additions: 2, Deletions: 1}}
	whole := "diff --git a/one.txt b/one.txt\nnew file mode 100644\n--- /dev/null\n+++ b/one.txt\n@@ -0,0 +1 @@\n+one\n"
	partial := "diff --git a/two.txt b/two.txt\nindex 1..2 100644\n--- a/two.txt\n+++ b/two.txt\n@@ -1 +1,2 @@\n-t"

	for _, test := range []struct {
		name       string
		comparison repository.Comparison
		patch      string
		truncated  bool
		incomplete bool
		reason     string
	}{
		{"complete", repository.Comparison{Bases: 1, Base: strings.Repeat("c", 40), Files: files, Patch: whole}, whole, false, false, ""},
		{"output limit", repository.Comparison{Bases: 1, Files: files, Patch: whole + partial, PatchTruncated: true}, whole, true, false, "output_limit"},
		{"time limit", repository.Comparison{Bases: 1, Files: files, Patch: whole + partial, PatchTruncated: true, TimedOut: true}, whole, true, false, "time_limit"},
		{"file list cut", repository.Comparison{Bases: 1, Files: files[:1], PatchTruncated: true, FilesTruncated: true, TimedOut: true}, "", true, true, "time_limit"},
		{"first file partial", repository.Comparison{Bases: 1, Files: files, Patch: partial, PatchTruncated: true}, "", true, false, "output_limit"},
	} {
		diff := diffFromComparison(revisions, test.comparison)
		if diff.Patch != test.patch || diff.Truncated != test.truncated || diff.Incomplete != test.incomplete || diff.Reason != test.reason ||
			len(diff.Files) != len(test.comparison.Files) || diff.Source.OID != revisions.Source.OID || diff.Target.OID != revisions.Target.OID {
			t.Errorf("%s: %+v", test.name, diff)
		}
	}
	for bases, reason := range map[int]string{0: "no_merge_base", 2: "multiple_merge_bases"} {
		diff := diffFromComparison(revisions, repository.Comparison{Bases: bases})
		if diff.Unavailable != reason || diff.MergeBase != "" || diff.Files == nil || len(diff.Files) != 0 || diff.Truncated {
			t.Errorf("%d merge bases: %+v", bases, diff)
		}
	}

	// A file list too large for the response is cut after the patch.
	many := make([]repository.ChangedFile, 200)
	for index := range many {
		many[index] = repository.ChangedFile{Path: strings.Repeat("x", 30) + itoa(int64(index)), Status: "added"}
	}
	diff := diffFromComparison(revisions, repository.Comparison{Bases: 1, Files: many, Patch: whole})
	fitDiff(diff, 4000)
	encoded, err := json.Marshal(diff)
	noErr(t, err)
	if len(encoded)+1 > 4000 || diff.Patch != "" || !diff.Truncated || !diff.Incomplete || diff.Reason != "response_limit" || len(diff.Files) == 0 || len(diff.Files) >= 200 {
		t.Fatalf("list cut to fit: files=%d bytes=%d %+v", len(diff.Files), len(encoded), diff.Reason)
	}
	// One more entry would not have fit.
	next := many[len(diff.Files)]
	diff.Files = append(diff.Files, pullrequest.DiffFile{Path: next.Path, Status: next.Status})
	if size := encodedSize(diff); size <= 4000 {
		t.Fatalf("the list kept %d entries although %d fit (%d bytes)", len(diff.Files)-1, len(diff.Files), size)
	}
}

// Fitting the response keeps every whole file that fits, not half of them.
func TestFitDiffKeepsEveryWholeFileThatFits(t *testing.T) {
	var sections []string
	var files []pullrequest.DiffFile
	for index := 0; index < 6; index++ {
		name := "f" + itoa(int64(index)) + ".html"
		// Markup is escaped to six bytes per character.
		sections = append(sections, "diff --git a/"+name+" b/"+name+"\n+"+strings.Repeat("<&>", 100+index*50)+"\n")
		files = append(files, pullrequest.DiffFile{Path: name, Status: "added", Additions: 1})
	}
	whole := strings.Join(sections, "")
	newDiff := func() *pullrequest.Diff {
		return &pullrequest.Diff{OK: true, Repository: "project", Number: 1, Files: append([]pullrequest.DiffFile(nil), files...), Patch: whole}
	}
	// With every file kept the diff is not cut and carries no reason, so the
	// last count is the uncut case checked below.
	for keep := 0; keep < len(sections)-1; keep++ {
		probe := newDiff()
		markCut(probe, false)
		probe.Patch = strings.Join(sections[:keep+1], "")
		// The largest limit that cannot hold keep+1 files.
		limit := encodedSize(probe) - 1
		diff := newDiff()
		fitDiff(diff, limit)
		if diff.Patch != strings.Join(sections[:keep], "") || !diff.Truncated || diff.Incomplete || diff.Reason != "response_limit" ||
			len(diff.Files) != len(files) || encodedSize(diff) > limit {
			t.Errorf("limit %d: kept %d sections, want %d; size=%d %+v", limit, strings.Count(diff.Patch, "diff --git "), keep, encodedSize(diff), diff.Reason)
		}
	}
	if diff := newDiff(); func() bool { fitDiff(diff, encodedSize(diff)); return diff.Truncated || diff.Patch != whole }() {
		t.Fatal("a diff that fits was cut")
	}
}
