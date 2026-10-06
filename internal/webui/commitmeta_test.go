package webui

import (
	"strings"
	"testing"
	"time"
)

// When a commit detail shows the committer
//
// Git records two identities on every commit: the author and the committer.
// Both are ordinary metadata that a client may set to any value. A rebase or
// `git commit --amend` typically keeps the original author date and writes a
// new committer date, and usually the same person does both, so the names
// match while the dates differ.
//
// Two things follow. Comparing only names hides that common case, leaving one
// date on screen that reads as when the work landed. But a difference is only
// a difference: it does not prove an amend, a rebase, or that anything
// happened later. A committer date can precede the author date, and two names
// can share one instant. So the interface states the committer as a fact and
// leaves the meaning to the reader.

func commitDetailPage(lang Lang, detail *CommitDetail) RepositoryPage {
	page := repoPage(fullChrome(lang), RepoTabCommits)
	page.Commits.Detail = detail
	return page
}

// committerLine returns the rendered committer line, if the page shows one.
// It locates the element rather than the first occurrence of the label text,
// which also appears inside the bilingual data attributes of other elements.
func committerLine(t *testing.T, lang Lang, detail *CommitDetail) (string, bool) {
	t.Helper()
	out := render(t, newRenderer(t), commitDetailPage(lang, detail))

	idx := strings.Index(out, `class="cdetail__meta cdetail__committer"`)
	if idx < 0 {
		return "", false
	}
	line := out[idx:]
	if end := strings.Index(line, "</p>"); end >= 0 {
		line = line[:end]
	}
	// The label must be the one this line is for.
	if !strings.Contains(line, wantText(lang, MsgCommitCommitter)) {
		t.Fatalf("the committer line does not carry its %s label: %q", lang, line)
	}
	return line, true
}

var authored = time.Date(2026, 3, 2, 9, 15, 0, 0, time.UTC)

func authoredBy(name string) CommitSummary {
	return CommitSummary{
		OID: "a41c9e2ff", ShortOID: "a41c9e2",
		Subject: "Use stable key in pagination cursor", AuthorName: name, AuthorDate: authored,
		URL: "/repositories/r1/commits/a41c9e2ff",
	}
}

// Each row is one way the committer can relate to the author. A difference is
// a fact to state, never proof of an amend or of an order of events.
func TestCommitterLineShowsADifferenceAndStaysQuietOtherwise(t *testing.T) {
	seoul := time.FixedZone("KST", 9*60*60)
	rows := []struct {
		name       string
		committer  string
		at         time.Time
		shown      bool
		has, lacks []string
	}{
		{"same name, later time: only the date moved", "Dana", authored.Add(168 * time.Hour), true, []string{"2026"}, nil},
		{"earlier time is shown without claiming order", "Dana", authored.Add(-72 * time.Hour), true, []string{"2026"},
			[]string{"later", "after", "\ub098\uc911\uc5d0", "\uc774\ud6c4"}},
		{"another name at the same instant", "Patch Applier", authored, true, []string{"Patch Applier"}, []string{"later", "\ub098\uc911\uc5d0"}},
		{"another name and time", "Rebase Bot", authored.Add(72 * time.Hour), true, []string{"Rebase Bot"}, nil},
		{"identical committer stays quiet", "Dana", authored, false, nil, nil},
		{"the same instant in another zone is no difference", "Dana", authored.In(seoul), false, nil, nil},
		{"a missing committer is not shown", "", time.Time{}, false, nil, nil},
	}
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			detail := &CommitDetail{Commit: authoredBy("Dana"), CommitterName: row.committer, CommitterDate: row.at}
			for _, lang := range Langs() {
				line, shown := committerLine(t, lang, detail)
				if shown != row.shown {
					t.Fatalf("%s: committer line shown=%v, want %v: %q", lang, shown, row.shown, line)
				}
				for _, want := range row.has {
					if !strings.Contains(line, want) {
						t.Errorf("%s: the committer line lacks %q: %q", lang, want, line)
					}
				}
				for _, claim := range row.lacks {
					if strings.Contains(line, claim) {
						t.Errorf("%s: the committer line claims %q: %q", lang, claim, line)
					}
				}
			}
		})
	}
}

func TestCommitterLabelClaimsOnlyWhatGitRecords(t *testing.T) {
	// The label names a Git field. It must not assert an amend, a rebase, or
	// an order of events, because differing metadata proves none of those.
	for _, lang := range Langs() {
		label := Text(lang, MsgCommitCommitter)
		for _, claim := range []string{
			"amend", "rebase", "rewritten", "later", "after", "modified",
			"\uc218\uc815", "\ub9ac\ubca0\uc774\uc2a4", "\ub098\uc911\uc5d0", "\uc7ac\uc791\uc131", "\ubcc0\uacbd\ub428",
		} {
			if strings.Contains(strings.ToLower(label), strings.ToLower(claim)) {
				t.Errorf("%s: the committer label asserts %q: %q", lang, claim, label)
			}
		}
		if strings.TrimSpace(label) == "" {
			t.Errorf("%s: the committer line has no label", lang)
		}
	}
}
