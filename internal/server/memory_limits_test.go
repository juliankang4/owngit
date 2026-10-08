package server

import (
	"encoding/json"
	"testing"

	"owngit/internal/pullrequest"
	"owngit/internal/repository"
)

// A file this computer did not compare as text has unknown line counts, not
// zero of them, and the JSON keeps binary for a file whose content is not
// text. A client can then tell "not compared here" from "no changes" and from
// "binary file".
func TestChangedFileJSONReportsAnUnknownCountAsUnknown(t *testing.T) {
	revisions := pullrequest.DiffRevisions{Repository: "project", Number: 1, State: "open",
		Source: pullrequest.Revision{Branch: "feature", OID: "1111111111111111111111111111111111111111"},
		Target: pullrequest.Revision{Branch: "main", OID: "2222222222222222222222222222222222222222"}}
	diff := diffFromComparison(revisions, repository.Comparison{Bases: 1, Base: revisions.Target.OID, Files: []repository.ChangedFile{
		{Path: "big.txt", Status: "added", Binary: true, TextDiffUnavailable: true},
		{Path: "logo.png", Status: "added", Binary: true, CountsRead: true},
		{Path: "text.txt", Status: "modified", Additions: 1, CountsRead: true},
		{Path: "unread.txt", Status: "modified"},
	}})
	encoded, err := json.Marshal(diff)
	noErr(t, err)
	var answer struct {
		Files []map[string]any `json:"files"`
	}
	noErr(t, json.Unmarshal(encoded, &answer))
	if len(answer.Files) != 4 {
		t.Fatalf("the answer holds %d files: %s", len(answer.Files), encoded)
	}
	large := answer.Files[0]
	if large["too_large"] != true || large["binary"] != false {
		t.Errorf("a file above the memory line is not reported as too large: %v", large)
	}
	for _, key := range []string{"additions", "deletions"} {
		if _, present := large[key]; present {
			t.Errorf("the unknown count %q is sent as %v", key, large[key])
		}
	}
	image := answer.Files[1]
	if image["binary"] != true {
		t.Errorf("a binary file is not reported as binary: %v", image)
	}
	if _, present := image["too_large"]; present {
		t.Errorf("a binary file is reported as too large: %v", image)
	}
	text := answer.Files[2]
	if text["additions"] != float64(1) || text["deletions"] != float64(0) {
		t.Errorf("a known count is missing or wrong: %v", text)
	}
	// A file of the same comparison whose counts were never read, because the
	// read left it out, is not reported as a change of no lines either.
	unread := answer.Files[3]
	if _, present := unread["too_large"]; present {
		t.Errorf("a file below the memory line is reported as too large: %v", unread)
	}
	for _, key := range []string{"additions", "deletions"} {
		if _, present := unread[key]; present {
			t.Errorf("the unread count %q is sent as %v", key, unread[key])
		}
	}

	// A patch that was not read because too many files are above the memory
	// line says so, instead of naming a limit that was never reached.
	crowded := repository.Comparison{Bases: 1, Base: revisions.Target.OID, PatchTruncated: true, PatchTooLarge: true,
		Files: []repository.ChangedFile{{Path: "big.bin", Status: "A", TextDiffUnavailable: true}}}
	if other := diffFromComparison(revisions, crowded); other.Reason != "too_large" || !other.Truncated {
		t.Errorf("a patch left out for its files reports reason=%q truncated=%v", other.Reason, other.Truncated)
	}

	// The restore preview reports the same file the same way.
	preview := restorePreviewView(restorePreviewInput{Mode: "merge"}, repository.RestorePreview{Changes: []repository.ChangedFile{
		{Path: "big.txt", Status: "added", Binary: true, TextDiffUnavailable: true},
		{Path: "logo.png", Status: "added", Binary: true, Additions: 0, Deletions: 0, CountsRead: true},
		{Path: "unread.txt", Status: "modified"},
	}})
	encoded, err = json.Marshal(preview.Changes)
	noErr(t, err)
	var changes []map[string]any
	noErr(t, json.Unmarshal(encoded, &changes))
	if len(changes) != 3 {
		t.Fatalf("the preview holds %d changes: %s", len(changes), encoded)
	}
	if changes[0]["too_large"] != true || changes[0]["binary"] != false {
		t.Errorf("the preview does not mark the file above the memory line: %v", changes[0])
	}
	if _, present := changes[0]["additions"]; present {
		t.Errorf("the preview sends an unknown count as %v", changes[0]["additions"])
	}
	if changes[1]["binary"] != true || changes[1]["additions"] != float64(0) {
		t.Errorf("the preview does not keep a binary file's own counts: %v", changes[1])
	}
	if _, present := changes[2]["additions"]; present {
		t.Errorf("the preview sends an unread count as %v", changes[2]["additions"])
	}
}

// The commit page leaves every file above the memory line out of its diff
// read, whatever side of the change holds it: Git reads such a file whole
// while it writes its patch. Past the number of pathspecs one command line
// carries, the page reads no patch at all and offers each file on its own.
func TestCommitDiffLeavesFilesAboveTheMemoryLineOut(t *testing.T) {
	files := []repository.ChangedFile{
		{Path: "added.bin", Status: "A", Binary: true, TextDiffUnavailable: true},
		{Path: "modified.bin", Status: "M", Binary: true, TextDiffUnavailable: true},
		{Path: "deleted.bin", Status: "D", Binary: true, TextDiffUnavailable: true},
		{Path: "text.txt", Status: "M", Additions: 1, Deletions: 1, CountsRead: true},
	}
	excluded, deferred, readPatch := excludedFromDiff(files)
	if !readPatch {
		t.Fatal("the diff read was left out although most files can be compared")
	}
	want := map[string]bool{"added.bin": true, "modified.bin": true, "deleted.bin": true}
	if len(excluded) != len(want) {
		t.Fatalf("the read leaves out %v, want the three files above the line", excluded)
	}
	for _, path := range excluded {
		if !want[path] {
			t.Errorf("the read leaves out %s, which is below the line", path)
		}
	}
	if len(deferred) != 0 {
		t.Errorf("a file above the memory line is deferred instead of left out: %v", deferred)
	}

	// The page shows the counts of a file that was read and hides those of a
	// file that was not: a zero count of a file whose counts were never read
	// would claim that the file changed no lines.
	items, notLoaded := diffFileItems([]repository.ChangedFile{{Path: "unread.txt", Status: "M"}}, "", false, nil, nil, 1<<20)
	if len(items) != 1 || !items[0].CountsUnknown || !items[0].NotLoaded || !notLoaded {
		t.Errorf("a file whose counts were not read is shown as a change of no lines: %+v notLoaded=%v", items, notLoaded)
	}
	items, notLoaded = diffFileItems(files[3:], "", false, nil, nil, 1<<20)
	if len(items) != 1 || items[0].CountsUnknown || items[0].NotLoaded || notLoaded {
		t.Errorf("a file whose counts were read is not shown with them: %+v notLoaded=%v", items, notLoaded)
	}

	many := make([]repository.ChangedFile, 0, maximumExcludedFiles+1)
	for index := 0; index <= maximumExcludedFiles; index++ {
		many = append(many, repository.ChangedFile{Path: "big.bin", Status: "A", Binary: true, TextDiffUnavailable: true})
	}
	if excluded, _, readPatch := excludedFromDiff(many); readPatch || excluded != nil {
		t.Errorf("past the pathspec bound the page still reads a patch: %v", excluded)
	}
}
