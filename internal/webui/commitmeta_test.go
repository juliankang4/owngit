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

func TestSameNameDifferentTimeIsShown(t *testing.T) {
	// The originally reported case: one person amends their own commit, so
	// only the date moves. Comparing names alone would hide it.
	committed := authored.Add(168 * time.Hour)
	detail := &CommitDetail{
		Commit: authoredBy("Dana"), Body: "Ties are broken by record id.",
		CommitterName: "Dana", CommitterDate: committed,
	}

	for _, lang := range Langs() {
		line, shown := committerLine(t, lang, detail)
		if !shown {
			t.Errorf("%s: the committer is hidden; the author date %s is the only date shown, "+
				"but it was committed %s",
				lang, authored.Format(time.RFC3339), committed.Format(time.RFC3339))
			continue
		}
		if !strings.Contains(line, "2026") {
			t.Errorf("%s: the committer is mentioned without its date: %q", lang, line)
		}
	}
}

func TestEarlierCommitterDateIsShownWithoutClaimingOrder(t *testing.T) {
	// A committer date may precede the author date; Git does not prevent it.
	// The difference is still worth showing, but nothing may imply the commit
	// was recorded "later".
	detail := &CommitDetail{
		Commit:        authoredBy("Dana"),
		CommitterName: "Dana", CommitterDate: authored.Add(-72 * time.Hour),
	}

	for _, lang := range Langs() {
		line, shown := committerLine(t, lang, detail)
		if !shown {
			t.Errorf("%s: an earlier committer date is hidden", lang)
			continue
		}
		if !strings.Contains(line, "2026") {
			t.Errorf("%s: the earlier committer date is not shown: %q", lang, line)
		}
		for _, claim := range []string{"later", "after", "\ub098\uc911\uc5d0", "\uc774\ud6c4"} {
			if strings.Contains(line, claim) {
				t.Errorf("%s: an earlier committer date is described as %q: %q", lang, claim, line)
			}
		}
	}
}

func TestDifferentNameAtTheSameInstantIsShown(t *testing.T) {
	// Applying a patch records a different committer at the same instant. The
	// names differ, the times do not, and nothing about time may be implied.
	detail := &CommitDetail{
		Commit:        authoredBy("Dana"),
		CommitterName: "Patch Applier", CommitterDate: authored,
	}

	for _, lang := range Langs() {
		line, shown := committerLine(t, lang, detail)
		if !shown {
			t.Errorf("%s: a different committer at the same instant is hidden", lang)
			continue
		}
		if !strings.Contains(line, "Patch Applier") {
			t.Errorf("%s: the committer is not named: %q", lang, line)
		}
		for _, claim := range []string{"later", "\ub098\uc911\uc5d0"} {
			if strings.Contains(line, claim) {
				t.Errorf("%s: a same-instant difference is described as %q: %q", lang, claim, line)
			}
		}
	}
}

func TestDifferentNameAndTimeIsStillShown(t *testing.T) {
	// The case that worked before the fix must keep working.
	detail := &CommitDetail{
		Commit:        authoredBy("Dana"),
		CommitterName: "Rebase Bot", CommitterDate: authored.Add(72 * time.Hour),
	}

	for _, lang := range Langs() {
		line, shown := committerLine(t, lang, detail)
		if !shown {
			t.Fatalf("%s: a commit committed by another person is no longer shown", lang)
		}
		if !strings.Contains(line, "Rebase Bot") {
			t.Errorf("%s: the committer is not named: %q", lang, line)
		}
	}
}

func TestIdenticalCommitterStaysQuiet(t *testing.T) {
	// Most commits are authored and committed by the same person at the same
	// instant. Repeating that would be noise on every commit.
	detail := &CommitDetail{Commit: authoredBy("Dana"), CommitterName: "Dana", CommitterDate: authored}

	for _, lang := range Langs() {
		if line, shown := committerLine(t, lang, detail); shown {
			t.Errorf("%s: an identical committer is shown anyway: %q", lang, line)
		}
	}
}

func TestSameInstantInAnotherZoneIsNotADifference(t *testing.T) {
	// The same moment recorded with a different UTC offset is one instant.
	// Comparing wall-clock fields instead of instants would flag every commit
	// made outside the server's zone.
	seoul := time.FixedZone("KST", 9*60*60)
	detail := &CommitDetail{
		Commit:        authoredBy("Dana"),
		CommitterName: "Dana", CommitterDate: authored.In(seoul),
	}

	if line, shown := committerLine(t, LangEN, detail); shown {
		t.Errorf("a time-zone difference was reported as a difference: %q", line)
	}
}

func TestMissingCommitterIsNotShown(t *testing.T) {
	// The backend leaves the committer empty when it has nothing to report.
	// An empty name with a zero date must not produce a line naming nobody.
	detail := &CommitDetail{Commit: authoredBy("Dana")}

	for _, lang := range Langs() {
		if line, shown := committerLine(t, lang, detail); shown {
			t.Errorf("%s: a commit with no committer information shows a committer line: %q", lang, line)
		}
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
