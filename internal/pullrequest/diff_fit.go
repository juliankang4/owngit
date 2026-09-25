package pullrequest

import (
	"encoding/json"
	"math"
	"strings"
)

// Fit cuts diff until EncodedSize fits in limit bytes. It keeps as many whole
// files of the patch as fit, counted from the start, and only when the patch
// is gone as many entries of the file list as fit. A cut sets Truncated (and
// Incomplete when files are left out of the list) with the reason
// response_limit, unless an earlier cut already gave a reason. JSON escapes
// each character on its own and file sections begin after an ASCII newline,
// so the encoded size of a prefix is the sum of the encoded sizes of its
// sections.
func (diff *Diff) Fit(limit int) {
	if diff.EncodedSize() <= limit {
		return
	}
	diff.markCut(false)
	patch := diff.Patch
	diff.Patch = ""
	if used := diff.EncodedSize(); used <= limit {
		kept := 0
		for _, section := range patchSections(patch) {
			encoded, _ := json.Marshal(section)
			if used += len(encoded) - 2; used > limit {
				break
			}
			kept += len(section)
		}
		diff.Patch = patch[:kept]
		return
	}
	diff.markCut(true)
	files := diff.Files
	diff.Files = []DiffFile{}
	used, kept := diff.EncodedSize(), 0
	for index, file := range files {
		encoded, _ := json.Marshal(file)
		size := len(encoded)
		if index > 0 {
			size++ // the comma between entries
		}
		if used += size; used > limit {
			break
		}
		kept++
	}
	diff.Files = files[:kept]
}

// EncodedSize is the length of diff as the API writes it: the JSON with HTML
// escaping and a trailing newline.
func (diff *Diff) EncodedSize() int {
	encoded, err := json.Marshal(diff)
	if err != nil {
		return math.MaxInt
	}
	return len(encoded) + 1
}

// patchSections splits a patch at the start of each file's "diff --git " line.
func patchSections(patch string) []string {
	var sections []string
	for patch != "" {
		next := strings.Index(patch[1:], "\ndiff --git ")
		if next < 0 {
			return append(sections, patch)
		}
		sections = append(sections, patch[:next+2])
		patch = patch[next+2:]
	}
	return sections
}

func (diff *Diff) markCut(files bool) {
	diff.Truncated = true
	diff.Incomplete = diff.Incomplete || files
	if diff.Reason == "" {
		diff.Reason = "response_limit"
	}
}
