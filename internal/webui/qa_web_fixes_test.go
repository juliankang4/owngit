package webui

import (
	"strings"
	"testing"
)

// Regressions for the pre-release web QA findings and minor observations.

// A merge record names its method in words, in both languages, and only a
// real merge commit is labelled as one.
func TestMergeRecordNamesTheMethod(t *testing.T) {
	r := newRenderer(t)
	for _, test := range []struct {
		mode        string
		method      MessageCode
		commitLabel MessageCode
	}{
		{MergeModeFastForward, MsgPRMergedFastForward, MsgPRMergedTarget},
		{MergeModeCommit, MsgPRMergedMergeCommit, MsgPRMergedCommit},
		{MergeModeUpToDate, MsgPRMergedUpToDate, MsgPRMergedTarget},
	} {
		for _, lang := range Langs() {
			page := pullRequestPage(fullChrome(lang), prFixtureMerged)
			page.Merged.Mode = test.mode
			out := render(t, r, page)
			if !strings.Contains(out, Text(lang, test.method)) || !strings.Contains(out, Text(lang, test.commitLabel)) {
				t.Errorf("%s %s: the merge record does not name the method and commit", test.mode, lang)
			}
			if strings.Contains(out, `<dd class="mono">`+test.mode+`</dd>`) {
				t.Errorf("%s %s: the raw mode value is shown", test.mode, lang)
			}
			if note := Text(lang, MsgPRMergedUpToDateFor); strings.Contains(out, note) != (test.mode == MergeModeUpToDate) {
				t.Errorf("%s %s: the no-new-commit note is shown only for up_to_date", test.mode, lang)
			}
		}
	}
	// A mode this interface has no words for still reaches the reader.
	out := render(t, r, pullRequestPage(fullChrome(LangEN), prFixtureMerged))
	if !strings.Contains(out, `<dd class="mono">merge-commit</dd>`) {
		t.Error("an unknown merge mode disappeared")
	}
}

// A requested or skipped review has no reviewer, so nothing is said about
// one.
func TestReviewerStatementsNeedAReviewer(t *testing.T) {
	for _, status := range []string{ReviewPending, ReviewSkipped} {
		evidence := ReviewEvidence{Status: status, BoundToCurrentRevision: true, Provenance: ReviewFromRequest}
		for _, note := range reviewNotes(evidence) {
			if note == MsgReviewNotIndependent || note == MsgReviewNoChecksRun {
				t.Errorf("%s: note %q speaks about a reviewer who does not exist", status, note)
			}
		}
		if summary := prReviewSummary(evidence); summary == MsgReviewNotIndependent || summary == MsgReviewNoChecksRun {
			t.Errorf("%s: summary %q speaks about a reviewer who does not exist", status, summary)
		}
	}
	submitted := ReviewEvidence{Status: ReviewApproved, BoundToCurrentRevision: true, Provenance: ReviewFromExternalTool}
	if prReviewSummary(submitted) != MsgReviewNotIndependent || len(reviewNotes(submitted)) != 2 {
		t.Error("a submitted review lost its qualifications")
	}
}

func TestNoticeLinkRendersAsALink(t *testing.T) {
	r := newRenderer(t)
	chrome := fullChrome(LangEN)
	chrome.Notices = []Notice{Error("", MsgPRAlreadyOpen).WithLink("#3", "/repositories/r1/pull-requests/3")}
	out := render(t, r, OverviewPage{Chrome: chrome, Activity: sampleGraph()})
	if !strings.Contains(out, Text(LangEN, MsgPRAlreadyOpen)+`</span> <a class="mono" href="/repositories/r1/pull-requests/3">#3</a>`) {
		t.Error("the notice does not link to the open pull request")
	}
}

// QA-015: the appearance controls are links the backend understands, and the
// saved choice is rendered by the server.
func TestAppearanceLinksWorkWithoutScripting(t *testing.T) {
	r := newRenderer(t)
	chrome := fullChrome(LangEN)
	chrome.Appearance = AppearanceDark
	chrome.CurrentURL = "/repositories/r1/code?ref=main&appearance=dark"
	out := render(t, r, OverviewPage{Chrome: chrome, Activity: sampleGraph()})
	if !strings.Contains(out, `class="theme-dark" data-appearance="dark"`) {
		t.Error("the saved appearance is not rendered")
	}
	for _, choice := range []string{"light", "dark", "system"} {
		link := elementAt(t, out, `data-appearance-set="`+choice+`"`)
		if !strings.HasPrefix(link, "<a ") || !strings.Contains(link, `href="/repositories/r1/code?appearance=`+choice+`&amp;ref=main"`) {
			t.Errorf("the %s control is not a link that keeps the screen: %s", choice, link)
		}
		if strings.Contains(link, `aria-current="true"`) != (choice == "dark") {
			t.Errorf("the %s control marks the wrong choice: %s", choice, link)
		}
	}
	if strings.Contains(out, "lang=ko&amp;appearance") || strings.Contains(out, "appearance=dark&amp;lang") {
		t.Error("the language links repeat the appearance parameter")
	}
}
