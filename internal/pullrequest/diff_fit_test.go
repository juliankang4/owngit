package pullrequest

import (
	"strconv"
	"strings"
	"testing"
)

// Fitting a diff keeps every whole file that fits, not half of them.
func TestFitKeepsEveryWholeFileThatFits(t *testing.T) {
	var sections []string
	var files []DiffFile
	for index := 0; index < 6; index++ {
		name := "f" + strconv.Itoa(index) + ".html"
		// Markup is escaped to six bytes per character.
		sections = append(sections, "diff --git a/"+name+" b/"+name+"\n+"+strings.Repeat("<&>", 100+index*50)+"\n")
		files = append(files, DiffFile{Path: name, Status: "added", Additions: 1})
	}
	whole := strings.Join(sections, "")
	newDiff := func() *Diff {
		return &Diff{OK: true, Repository: "project", Number: 1, Files: append([]DiffFile(nil), files...), Patch: whole}
	}
	// With every file kept the diff is not cut and carries no reason, so the
	// last count is the uncut case checked below.
	for keep := 0; keep < len(sections)-1; keep++ {
		probe := newDiff()
		probe.markCut(false)
		probe.Patch = strings.Join(sections[:keep+1], "")
		// The largest limit that cannot hold keep+1 files.
		limit := probe.EncodedSize() - 1
		diff := newDiff()
		diff.Fit(limit)
		if diff.Patch != strings.Join(sections[:keep], "") || !diff.Truncated || diff.Incomplete || diff.Reason != "response_limit" ||
			len(diff.Files) != len(files) || diff.EncodedSize() > limit {
			t.Errorf("limit %d: kept %d sections, want %d; size=%d %+v", limit, strings.Count(diff.Patch, "diff --git "), keep, diff.EncodedSize(), diff.Reason)
		}
	}
	if diff := newDiff(); func() bool { diff.Fit(diff.EncodedSize()); return diff.Truncated || diff.Patch != whole }() {
		t.Fatal("a diff that fits was cut")
	}
	// An earlier reason is kept.
	diff := newDiff()
	diff.Truncated, diff.Reason = true, "time_limit"
	diff.Fit(diff.EncodedSize() - 1)
	if diff.Reason != "time_limit" || !diff.Truncated {
		t.Fatalf("an earlier reason was replaced: %+v", diff.Reason)
	}
}
