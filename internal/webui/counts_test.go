package webui

import (
	"regexp"
	"strings"
	"testing"
)

// Counted nouns
//
// Section headings count things: branches, tags, commits, lines. English
// plurals were previously guessed by appending "s", which produced "0 branchs"
// on every repository with no branches, and Korean fell back to a bare number
// for any noun the switch did not list, silently dropping the noun.
//
// The set of things this interface counts is small and fixed, so each one
// states its own forms and an unlisted noun is a test failure rather than a
// wrong word on screen.

// countedInTemplates are the kinds passed to biCount from the templates,
// listed here so the check fails loudly if one loses its stated forms.
func countedInTemplates() []string {
	return []string{"repository", "branch", "tag", "commit", "file", "line", "day", "item", "entry"}
}

func TestEnglishCountsUseRealPlurals(t *testing.T) {
	cases := []struct {
		kind           string
		zero, one, two string
	}{
		{"repository", "0 repositories", "1 repository", "2 repositories"},
		{"branch", "0 branches", "1 branch", "2 branches"},
		{"tag", "0 tags", "1 tag", "2 tags"},
		{"commit", "0 commits", "1 commit", "2 commits"},
		{"file", "0 files", "1 file", "2 files"},
		{"line", "0 lines", "1 line", "2 lines"},
		{"day", "0 days", "1 day", "2 days"},
		{"item", "0 items", "1 item", "2 items"},
		{"entry", "0 entries", "1 entry", "2 entries"},
	}
	for _, c := range cases {
		for n, want := range map[int]string{0: c.zero, 1: c.one, 2: c.two} {
			if got := formatCount(LangEN, c.kind, n); got != want {
				t.Errorf("formatCount(en, %q, %d) = %q, want %q", c.kind, n, got, want)
			}
		}
	}
}

func TestKoreanCountsKeepTheirNoun(t *testing.T) {
	// Korean has no plural agreement, but each noun has its own counter word
	// and none may be dropped.
	cases := map[string][3]string{
		"repository": {"저장소 0곳", "저장소 1곳", "저장소 2곳"},
		"branch":     {"브랜치 0개", "브랜치 1개", "브랜치 2개"},
		"tag":        {"태그 0개", "태그 1개", "태그 2개"},
		"commit":     {"커밋 0건", "커밋 1건", "커밋 2건"},
		"file":       {"파일 0개", "파일 1개", "파일 2개"},
		"line":       {"0줄", "1줄", "2줄"},
		"day":        {"0일", "1일", "2일"},
		"item":       {"0건", "1건", "2건"},
		"entry":      {"항목 0개", "항목 1개", "항목 2개"},
	}
	for kind, want := range cases {
		for n, expected := range want {
			if got := formatCount(LangKO, kind, n); got != expected {
				t.Errorf("formatCount(ko, %q, %d) = %q, want %q", kind, n, got, expected)
			}
		}
	}
}

func TestNoCountedNounIsGuessed(t *testing.T) {
	// A noun that is not listed would previously get "s" in English and lose
	// its noun entirely in Korean. Both are now reported.
	for _, kind := range countedInTemplates() {
		if _, ok := countedNoun(kind); !ok {
			t.Errorf("%q is counted by a template but has no stated forms", kind)
		}
	}
	if _, ok := countedNoun("sheep"); ok {
		t.Error("an unlisted noun was accepted")
	}
	// The fallback must not invent a word.
	if got := formatCount(LangEN, "sheep", 2); strings.Contains(got, "sheeps") {
		t.Errorf("an unlisted noun was inflected by guessing: %q", got)
	}
}

func TestEveryCountedNounInTemplatesIsDeclared(t *testing.T) {
	// Catches a future template that counts something new without stating its
	// forms, which is how "branchs" reached the screen.
	pattern := regexp.MustCompile(`biCount \$lang "([a-z]+)"`)
	found := map[string]bool{}

	entries, err := templateFS.ReadDir("templates")
	noErr(t, err)
	var read func(dir string)
	read = func(dir string) {
		items, err := templateFS.ReadDir(dir)
		noErr(t, err)
		for _, item := range items {
			path := dir + "/" + item.Name()
			if item.IsDir() {
				read(path)
				continue
			}
			data, err := templateFS.ReadFile(path)
			noErr(t, err)
			for _, m := range pattern.FindAllStringSubmatch(string(data), -1) {
				found[m[1]] = true
			}
		}
	}
	_ = entries
	read("templates")

	if len(found) == 0 {
		t.Fatal("no counted nouns found in the templates; the check is not looking at anything")
	}
	for kind := range found {
		if _, ok := countedNoun(kind); !ok {
			t.Errorf("a template counts %q but its forms are not stated", kind)
		}
	}
}

func TestEmptyRepositorySectionsReadCorrectly(t *testing.T) {
	// The reported symptom, at the place it appeared: a repository with no
	// branches and no tags.
	r := newRenderer(t)
	page := repoPage(fullChrome(LangEN), RepoTabOverview)
	page.Overview.Branches = nil
	page.Overview.Tags = nil
	page.Overview.RetainedRefs = nil

	out := render(t, r, page)
	for _, wrong := range []string{"branchs", "0 branchs", "tagss", "itemss"} {
		if strings.Contains(out, wrong) {
			t.Errorf("the overview shows %q", wrong)
		}
	}
	if !strings.Contains(out, "0 branches") {
		t.Error(`a repository with no branches does not read "0 branches"`)
	}

	// And with exactly one, the singular is used.
	page.Overview.Branches = []RefLine{{Name: "main", URL: "/x", Kind: "branch", IsDefault: true}}
	out = render(t, r, page)
	if !strings.Contains(out, "1 branch") || strings.Contains(out, "1 branches") {
		t.Error(`a repository with one branch does not read "1 branch"`)
	}
}

func TestCountsRenderInBothLanguagesOnTheSameElement(t *testing.T) {
	// Counted nouns are swapped by the language control like any other text,
	// so both forms must be present and correct.
	r := newRenderer(t)
	page := repoPage(fullChrome(LangEN), RepoTabOverview)
	page.Overview.Branches = nil
	out := render(t, r, page)

	if !strings.Contains(out, `data-en="0 branches"`) {
		t.Error("the English form of an empty branch count is missing or wrong")
	}
	if !strings.Contains(out, `data-ko="브랜치 0개"`) {
		t.Error("the Korean form of an empty branch count is missing or wrong")
	}
}
