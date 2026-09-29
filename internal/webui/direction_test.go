package webui

import (
	"strings"
	"testing"
)

// Text from Git and repository users is rendered in an element of its own
// with dir="auto", which the browser isolates, so a direction control in it
// cannot reorder the states, commit IDs and labels beside it. The window
// title has no elements, so there the pull request title is set between
// FIRST STRONG ISOLATE and POP DIRECTIONAL ISOLATE without its controls.
func TestTextFromUsersIsIsolated(t *testing.T) {
	r := newRenderer(t)
	const control = "\u202e"
	page := pullRequestPage(fullChrome(LangEN), prFixtureFailing)
	page.Title = "Cap" + control + "delays"
	page.Source.Branch = "fix/" + control + "cursor"
	page.Target.Branch = "main" + control
	page.Review.ReviewerLabel = "codex" + control
	page.ReviewNotes[0].ReviewerLabel = "reader" + control
	page.Description = PullRequestText{Text: "plain" + control, NotRendered: MsgCodeNotShown}
	page.ReviewNotes[0].Note = PullRequestText{Text: "note" + control, HTML: "<p>note" + control + "</p>"}
	out := render(t, r, page)
	for _, want := range []string{
		`<span dir="auto">Cap` + control + `delays</span>`,
		`<span class="mono" dir="auto">fix/` + control + `cursor</span>`,
		`<span class="mono" dir="auto">main` + control + `</span>`,
		`<dd dir="auto">codex` + control + `</dd>`,
		`<span dir="auto">reader` + control + `</span>`,
		`<div class="prtext prtext--plain" dir="auto">plain` + control + `</div>`,
		`<article class="md prtext" dir="auto"><p>note` + control + `</p></article>`,
		"<title>#12 \u2068Capdelays\u2069",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the pull request page lacks %q", want)
		}
	}

	list := pullRequestsPage(fullChrome(LangEN), false)
	list.Items[0].Title = "Cap" + control
	list.Items[0].Source.Branch = "fix" + control
	out = render(t, r, list)
	for _, want := range []string{`<span class="prrow__t" dir="auto">Cap` + control + `</span>`, `<span class="mono" dir="auto">fix` + control + `</span>`} {
		if !strings.Contains(out, want) {
			t.Errorf("the pull request list lacks %q", want)
		}
	}
}
