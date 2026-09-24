package webui

import (
	"net/url"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// Regressions for defects an independent review found in the running
// interface. Each one was visible to a real owner, not merely wrong in source.

func bareChrome(lang Lang) Chrome {
	return Chrome{Lang: lang, Now: testNow, CurrentURL: "/setup", CSRF: "csrf-token-value"}
}

// elementAt returns the opening tag containing the given marker.
func elementAt(t *testing.T, out, marker string) string {
	t.Helper()
	idx := strings.Index(out, marker)
	if idx < 0 {
		t.Fatalf("%q not found in the rendered page", marker)
	}
	tag := out[strings.LastIndex(out[:idx], "<"):]
	if end := strings.Index(tag, ">"); end >= 0 {
		tag = tag[:end]
	}
	return tag
}

// 1. The welcome page shows exactly one setup-code state
//
// The [hidden] rule and the script that toggles the states are pinned in
// asset_pins_test.go.

func TestWelcomePageClaimsNothingAboutACodeItCannotSee(t *testing.T) {
	// The one-time code lives in the URL fragment, which a browser never
	// sends. The server cannot know whether one is present, so it must render
	// neither claim as visible.
	r := newRenderer(t)
	for _, lang := range Langs() {
		out := render(t, r, SetupPage{
			Chrome: bareChrome(lang), Stage: SetupWelcome,
			RedeemURL: "/setup/redeem", SubmitURL: "/setup",
		})

		for marker, what := range map[string]string{
			"data-redeem-held":    "the held-code message",
			"data-redeem-missing": "the missing-code message",
		} {
			if tag := elementAt(t, out, marker); !strings.Contains(tag, "hidden") {
				t.Errorf("%s: %s is visible before any script could check: %s", lang, what, tag)
			}
		}

		// The button must not invite a click that cannot work yet.
		if tag := elementAt(t, out, "data-redeem-start"); !strings.Contains(tag, "disabled") {
			t.Errorf("%s: setup can be started before a code is in hand: %s", lang, tag)
		}

		// A browser without scripting must be told why nothing works.
		if !strings.Contains(out, "<noscript>") {
			t.Errorf("%s: a browser without scripting gets no explanation", lang)
		}
		if !strings.Contains(out, wantText(lang, MsgSetupNeedsScript)) {
			t.Errorf("%s: the noscript message does not explain the reason", lang)
		}
	}
}

// 2. A settings error belongs to the form that was submitted
//
// Every settings form collects "admin_password". A wrong password on the
// enable, disable, or acknowledge form was rendered against the admin-change
// form, which is collapsed, so the reader saw no reason for the failure.

func settingsWithFailure(action, field string, code MessageCode, lang Lang, mode AccessMode) SettingsPage {
	c := fullChrome(lang)
	c.Notices = []Notice{Error(field, code)}
	c.Connection = Connection{Encrypted: false, Host: "owngit.example"}
	return SettingsPage{Chrome: c, SubmitURL: "/settings", AccessMode: mode, PendingAction: action}
}

// formFor returns the markup of the form carrying the given action value.
func formFor(t *testing.T, out, action string) string {
	t.Helper()
	marker := `name="action" value="` + action + `"`
	idx := strings.Index(out, marker)
	if idx < 0 {
		t.Fatalf("no form found for action %q", action)
	}
	form := out[strings.LastIndex(out[:idx], "<form"):]
	if end := strings.Index(form, "</form>"); end >= 0 {
		form = form[:end]
	}
	return form
}

var settingsActions = []struct {
	action string
	mode   AccessMode
}{
	{ActionEnableAccessPassword, AccessOpen},
	{ActionDisableAccessPassword, AccessPassword},
	{ActionChangeAccessPassword, AccessPassword},
	{ActionChangeAdminPassword, AccessOpen},
	{ActionAcknowledgeInsecure, AccessOpen},
	{ActionSetUpdateCheck, AccessOpen},
}

func TestWrongAdminPasswordIsShownInTheFormThatWasSubmitted(t *testing.T) {
	r := newRenderer(t)
	for _, tc := range settingsActions {
		for _, lang := range Langs() {
			out := render(t, r, settingsWithFailure(tc.action, "admin_password", MsgAdminFailed, lang, tc.mode))
			form := formFor(t, out, tc.action)

			if !strings.Contains(form, wantText(lang, MsgAdminFailed)) {
				t.Errorf("%s/%s: the rejected password is not reported in the form that was submitted",
					tc.action, lang)
			}
			wantID := noteID(tc.action, "admin_password")
			if !strings.Contains(form, `id="`+wantID+`"`) {
				t.Errorf("%s/%s: the message has no id unique to this form", tc.action, lang)
			}
			if !strings.Contains(form, `aria-describedby="`+wantID+`"`) {
				t.Errorf("%s/%s: the input does not point at its own message", tc.action, lang)
			}

			// No other form may claim the same failure.
			for _, other := range settingsActions {
				if other.action == tc.action || !strings.Contains(out, `value="`+other.action+`"`) {
					continue
				}
				if strings.Contains(formFor(t, out, other.action), wantText(lang, MsgAdminFailed)) {
					t.Errorf("%s/%s: the failure also appears in the unrelated %q form",
						tc.action, lang, other.action)
				}
			}
		}
	}
}

func TestEveryNoteIDOnSettingsIsUnique(t *testing.T) {
	// Two inputs sharing one id would make aria-describedby point at whichever
	// element came first, so a reader would hear another form's message.
	r := newRenderer(t)
	idPattern := regexp.MustCompile(`id="([^"]+)"`)

	for _, tc := range settingsActions {
		out := render(t, r, settingsWithFailure(tc.action, "admin_password", MsgAdminFailed, LangEN, tc.mode))
		seen := map[string]bool{}
		for _, m := range idPattern.FindAllStringSubmatch(out, -1) {
			if seen[m[1]] {
				t.Errorf("action %s: id %q appears more than once", tc.action, m[1])
			}
			seen[m[1]] = true
		}
	}
}

func TestFailedSettingsFormIsOpenWithoutScripting(t *testing.T) {
	// The forms are collapsed by default. If the failed one is not expanded by
	// the server, a reader without scripting sees a rejected submission and no
	// visible reason.
	r := newRenderer(t)
	for _, tc := range settingsActions {
		if tc.action == ActionAcknowledgeInsecure {
			continue // always expanded while the connection is unacknowledged
		}
		out := render(t, r, settingsWithFailure(tc.action, "admin_password", MsgAdminFailed, LangEN, tc.mode))
		tag := elementAt(t, out, `data-disclosure="`+tc.action+`"`)
		if !strings.Contains(tag, " open") {
			t.Errorf("%s: the failed form stays collapsed without scripting: %s", tc.action, tag)
		}
	}

	// A form that did not fail must stay closed, or every form would be open
	// on every visit.
	out := render(t, r, SettingsPage{Chrome: fullChrome(LangEN), SubmitURL: "/settings", AccessMode: AccessOpen})
	tag := elementAt(t, out, `data-disclosure="`+ActionChangeAdminPassword+`"`)
	if strings.Contains(tag, " open") {
		t.Errorf("a form with no error is expanded anyway: %s", tag)
	}
}

func TestUnknownActionIsReportedAtPageLevel(t *testing.T) {
	// The backend reports an unknown action against the hidden "action" field.
	// No input shows it, so it has to appear above the page or it is lost.
	r := newRenderer(t)
	for _, lang := range Langs() {
		c := fullChrome(lang)
		c.Notices = []Notice{Error("action", MsgSettingsUnknownAct)}
		out := render(t, r, SettingsPage{Chrome: c, SubmitURL: "/settings", AccessMode: AccessOpen})

		if !strings.Contains(out, wantText(lang, MsgSettingsUnknownAct)) {
			t.Errorf("%s: an unknown action is never reported to the reader", lang)
			continue
		}
		notices := out[strings.Index(out, `class="notices"`):]
		if end := strings.Index(notices, "</div>"); end >= 0 {
			notices = notices[:end]
		}
		if !strings.Contains(notices, wantText(lang, MsgSettingsUnknownAct)) {
			t.Errorf("%s: the unknown-action message is not among the page notices", lang)
		}
	}
}

// 3. The activity graph must reach the real filtered list
//
// The behaviour scope sat on an inner element while the readout lived outside
// it, so the script's lookup returned null and nothing was ever announced. The
// cells also dropped the URL the backend supplies, so no day could be opened.

func graphWithDayLinks() ActivityGraph {
	graph := sampleGraph()
	for i := range graph.Days {
		graph.Days[i].URL = "/activity?date=" + graph.Days[i].Date.Format("2006-01-02")
	}
	return graph
}

func TestGraphReadoutIsInsideTheBehaviourScope(t *testing.T) {
	// The script looks the readout up inside the element marked data-graph.
	// With the readout outside it, the lookup returns null and the live region
	// is never updated, silently.
	r := newRenderer(t)
	out := render(t, r, OverviewPage{Chrome: fullChrome(LangEN), Activity: graphWithDayLinks()})

	start := strings.Index(out, "data-graph ")
	if start < 0 {
		t.Fatal("the graph has no behaviour scope")
	}
	scopeStart := strings.LastIndex(out[:start], "<div")
	depth, i := 0, scopeStart
	for i < len(out) {
		switch {
		case strings.HasPrefix(out[i:], "<div"):
			depth++
		case strings.HasPrefix(out[i:], "</div>"):
			depth--
		}
		if depth == 0 && i > scopeStart {
			break
		}
		i++
	}
	scope := out[scopeStart:i]

	if !strings.Contains(scope, "data-graph-readout") {
		t.Error("the readout is outside the behaviour scope, so the script cannot find it")
	}
	if !strings.Contains(scope, "data-day") {
		t.Error("the day cells are outside the behaviour scope")
	}
}

// 4. A missing ref must not be blamed on the default branch
//
// Ref.Missing is also true when a reader asks for a branch that never existed.
// Labelling every such case "the default branch is gone" was wrong, and the
// backend already sends a notice naming the real reason, so it was duplicated.

func TestBackendReasonForAMissingRefIsShownOnce(t *testing.T) {
	// When the default branch really is gone the backend says so. It must
	// appear, and only once.
	r := newRenderer(t)
	c := fullChrome(LangEN)
	c.Notices = []Notice{{Kind: NoticeWarning, Code: MsgRepoDefaultGone}}
	page := repoPage(c, RepoTabOverview)
	page.Ref.Missing = true

	out := render(t, r, page)
	// Count elements, not raw text: each bilingual element repeats its English
	// wording once in a data-en attribute and once as visible text.
	if n := strings.Count(out, `data-en="`+wantText(LangEN, MsgRepoDefaultGone)+`"`); n != 1 {
		t.Errorf("the reason for the missing branch appears in %d elements, want one", n)
	}
}

// 5. A ref picker must preserve which ref was chosen
//
// A branch and a tag can share one short name. Submitting the display name
// leaves the backend to guess, and it may resolve to the other ref. The
// backend puts the unambiguous ref in each option's URL, so the option submits
// that value while still showing the short name.

func collidingRefPage(tab RepoTab) RepositoryPage {
	page := repoPage(fullChrome(LangEN), tab)
	page.Ref = RefSelection{
		Name: "release", Kind: "branch", Revision: "a41c9e2ff", ShortRevision: "a41c9e2",
		Branches: []RefOption{
			{Name: "main", URL: "/repositories/r1/code?ref=refs%2Fheads%2Fmain"},
			{Name: "release", URL: "/repositories/r1/code?ref=refs%2Fheads%2Frelease", Selected: true},
		},
		Tags: []RefOption{
			{Name: "release", URL: "/repositories/r1/code?ref=refs%2Ftags%2Frelease"},
		},
	}
	return page
}

// optionValues returns every option value inside the ref picker, in order.
func optionValues(t *testing.T, out string) []string {
	t.Helper()
	idx := strings.Index(out, `<select id="ref-select"`)
	if idx < 0 {
		t.Fatal("no ref picker rendered")
	}
	picker := out[idx:]
	if end := strings.Index(picker, "</select>"); end >= 0 {
		picker = picker[:end]
	}
	var values []string
	for _, m := range regexp.MustCompile(`<option value="([^"]*)"`).FindAllStringSubmatch(picker, -1) {
		values = append(values, m[1])
	}
	return values
}

func TestRefPickerSubmitsTheCanonicalRefNotTheDisplayName(t *testing.T) {
	r := newRenderer(t)
	out := render(t, r, collidingRefPage(RepoTabCode))

	values := optionValues(t, out)
	if len(values) != 3 {
		t.Fatalf("expected three options, got %d: %v", len(values), values)
	}
	want := []string{"refs/heads/main", "refs/heads/release", "refs/tags/release"}
	for i, expected := range want {
		if values[i] != expected {
			t.Errorf("option %d submits %q, want %q", i, values[i], expected)
		}
	}

	// The two same-named refs must remain distinguishable when submitted.
	if values[1] == values[2] {
		t.Errorf("a branch and a tag named %q submit the same value %q, so one cannot be chosen",
			"release", values[1])
	}

	// The reader still sees the short name.
	if strings.Count(out, ">release</option>") != 2 {
		t.Error("the picker no longer shows the short name a reader recognises")
	}
}

func TestRefPickerValueMatchesTheLinkItWouldFollow(t *testing.T) {
	// The value a form submits and the URL the same option points at must
	// request the same ref, or the picker and the rest of the page disagree.
	for _, option := range collidingRefPage(RepoTabCode).Ref.Branches {
		got := refValue(option)
		if !strings.Contains(option.URL, "ref="+strings.ReplaceAll(got, "/", "%2F")) {
			t.Errorf("option %q submits %q, which its URL %q would not request",
				option.Name, got, option.URL)
		}
	}
}

func TestRefValueFallsBackToTheNameOnlyWhenItMust(t *testing.T) {
	// Older callers and display-only fixtures have no URL, or a URL without a
	// ref. Those still submit the display name rather than nothing.
	cases := []struct {
		option RefOption
		want   string
	}{
		{RefOption{Name: "main"}, "main"},
		{RefOption{Name: "main", URL: "/repositories/r1/code"}, "main"},
		{RefOption{Name: "main", URL: "://bad url"}, "main"},
		{RefOption{Name: "release", URL: "/x?ref=refs%2Ftags%2Frelease"}, "refs/tags/release"},
		{RefOption{Name: "release", URL: "/x?ref=release"}, "release"},
		{RefOption{Name: "fix/cursor", URL: "/x?ref=refs%2Fheads%2Ffix%2Fcursor"}, "refs/heads/fix/cursor"},
	}
	for _, tc := range cases {
		if got := refValue(tc.option); got != tc.want {
			t.Errorf("refValue(%+v) = %q, want %q", tc.option, got, tc.want)
		}
	}
}

func TestRefPickerWorksWithoutScriptingForCollidingNames(t *testing.T) {
	// Without scripting the reader presses the submit button, so the canonical
	// value has to travel in the plain GET form.
	r := newRenderer(t)
	out := render(t, r, collidingRefPage(RepoTabCode))

	idx := strings.Index(out, `<select id="ref-select"`)
	form := out[strings.LastIndex(out[:idx], "<form"):]
	if end := strings.Index(form, "</form>"); end >= 0 {
		form = form[:end]
	}
	if !strings.Contains(form, `method="get"`) {
		t.Error("the ref picker is not a plain GET form")
	}
	if !strings.Contains(form, `type="submit"`) {
		t.Error("there is no submit control for a browser without scripting")
	}
	if !strings.Contains(form, `value="refs/tags/release"`) {
		t.Error("the tag cannot be chosen without scripting")
	}
}

func TestWelcomeIntroDoesNotClaimTheReaderUsedTheLink(t *testing.T) {
	// The static introduction is rendered before anything is checked. The
	// server cannot see the URL fragment, so a plain GET /setup looks
	// identical to an owner arriving by the one-time link. Telling every
	// visitor "you opened the one-time setup link" states something the
	// server does not know, and contradicts the missing-code notice shown
	// right below it.
	for _, lang := range Langs() {
		intro := Text(lang, MsgSetupWelcomeBody)

		for _, claim := range []string{
			"You opened", "you opened", "you arrived", "your link",
			"들어왔습니다", "여셨습니다", "접속하셨습니다",
		} {
			if strings.Contains(intro, claim) {
				t.Errorf("%s: the introduction asserts how the reader arrived (%q): %q", lang, claim, intro)
			}
		}

		// It must still explain what the link is and that it is single-use,
		// which is the reason the owner must not share it.
		for _, part := range map[Lang][]string{
			LangEN: {"one-time setup link", "cannot be reused"},
			LangKO: {"1회용 설치 링크", "다시 쓸 수 없습니다"},
		}[lang] {
			if !strings.Contains(intro, part) {
				t.Errorf("%s: the introduction no longer explains %q: %q", lang, part, intro)
			}
		}
	}

	// On a plain visit the page shows the neutral introduction and nothing
	// that presumes a code is present.
	r := newRenderer(t)
	for _, lang := range Langs() {
		out := render(t, r, SetupPage{
			Chrome: bareChrome(lang), Stage: SetupWelcome,
			RedeemURL: "/setup/redeem", SubmitURL: "/setup",
		})
		if !strings.Contains(out, wantText(lang, MsgSetupWelcomeBody)) {
			t.Errorf("%s: the introduction is not rendered", lang)
		}
	}
}

func TestRefPickerLabelCoversBranchesAndTags(t *testing.T) {
	// The picker lists branches and tags in one control, and when it is closed
	// the optgroup that would reveal the kind is hidden, so only the chosen
	// short name shows. Labelling it "Branch" therefore misnames a selected
	// tag, and a short name shared by both gives the reader nothing to go on.
	r := newRenderer(t)

	for _, lang := range Langs() {
		page := collidingRefPage(RepoTabCode)
		page.Chrome = fullChrome(lang)
		// Select the tag, the case the old label got wrong.
		page.Ref.Kind = "tag"
		page.Ref.Branches[1].Selected = false
		page.Ref.Tags[0].Selected = true

		out := render(t, r, page)

		idx := strings.Index(out, `class="refbar__label"`)
		if idx < 0 {
			t.Fatalf("%s: the picker has no label", lang)
		}
		label := out[idx:]
		if end := strings.Index(label, "</label>"); end >= 0 {
			label = label[:end]
		}

		if !strings.Contains(label, wantText(lang, MsgRefLabel)) {
			t.Errorf("%s: the picker label does not cover both kinds: %q", lang, label)
		}
		// It must be a label for the select, or it names nothing.
		if !strings.Contains(label, `for="ref-select"`) {
			t.Errorf("%s: the label is not attached to the picker", lang)
		}
	}

	// The default-branch pill keeps its own wording: that one really is a
	// branch, so the two must not be merged into one message.
	if Text(LangEN, MsgBranchLabel) != "Branch" {
		t.Errorf("the default-branch pill wording changed: %q", Text(LangEN, MsgBranchLabel))
	}
	if Text(LangKO, MsgBranchLabel) != "브랜치" {
		t.Errorf("the Korean default-branch pill wording changed: %q", Text(LangKO, MsgBranchLabel))
	}
	if Text(LangEN, MsgRefLabel) == Text(LangEN, MsgBranchLabel) {
		t.Error("the picker label and the default-branch pill share one message again")
	}

	// The pill still renders on a default branch.
	page := repoPage(fullChrome(LangEN), RepoTabCode)
	page.Ref.IsDefault = true
	if out := render(t, r, page); !strings.Contains(out, `pill--default`) {
		t.Error("the default-branch pill is no longer rendered")
	}
}

// A missing ref must still be the selected option
//
// When the requested ref does not resolve, it is in none of the picker lists.
// With no option marked selected, the browser selects the first one instead.
// A real repository showed what that costs: the default branch "main" was
// deleted and only a tag named "main" remained, so the picker displayed "main"
// as though it were being shown, while the page reported the ref as missing.
//
// Worse, the reader could not leave. Choosing the sole tag that the browser had
// already auto-selected fires no change event, so the script never submits, and
// the no-script button was hidden. The one available ref was unreachable
// through the control meant to reach it.
//
// The requested ref is therefore offered as an explicit selected option,
// carrying the exact value that was asked for rather than its short name: the
// deleted "refs/heads/main" is a different value from the surviving
// "refs/tags/main", so submitting it unchanged asks for the same missing ref
// instead of quietly resolving to the tag.

// missingRefPage is the reported fixture: the default branch main was deleted
// and only a tag of the same name survives.
func missingRefPage(requested string) RepositoryPage {
	page := repoPage(fullChrome(LangEN), RepoTabCode)
	page.Ref = RefSelection{
		Name: "main", Kind: "branch", Missing: true,
		Tags: []RefOption{{Name: "main", URL: "/repositories/r1/code?ref=refs%2Ftags%2Fmain"}},
	}
	page.CodeURL = "/repositories/r1/code?ref=" + url.QueryEscape(requested)
	return page
}

// selectedOptions returns the value of every option marked selected.
func selectedOptions(t *testing.T, out string) []string {
	t.Helper()
	idx := strings.Index(out, `<select id="ref-select"`)
	if idx < 0 {
		t.Fatal("no ref picker rendered")
	}
	picker := out[idx:]
	if end := strings.Index(picker, "</select>"); end >= 0 {
		picker = picker[:end]
	}
	var values []string
	for _, m := range regexp.MustCompile(`<option value="([^"]*)"[^>]*\sselected`).FindAllStringSubmatch(picker, -1) {
		values = append(values, m[1])
	}
	return values
}

func TestMissingRefIsTheSelectedOptionNotTheFirstAvailableOne(t *testing.T) {
	r := newRenderer(t)

	for _, tc := range []struct {
		name      string
		requested string
	}{
		// The default branch was deleted; the backend asks for it by its full
		// name, so the picker must submit that and not the same-named tag.
		{"implicitly missing default branch", "refs/heads/main"},
		// An address typed by hand. The exact text must survive a round trip.
		{"explicitly requested unknown ref", "does-not-exist"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			page := missingRefPage(tc.requested)
			out := render(t, r, page)

			selected := selectedOptions(t, out)
			if len(selected) != 1 {
				t.Fatalf("the picker has %d selected options, want exactly 1: %q",
					len(selected), optionValues(t, out))
			}
			if selected[0] != tc.requested {
				t.Errorf("the picker submits %q, want the ref actually requested, %q",
					selected[0], tc.requested)
			}
			// The surviving tag must remain a different choice, or selecting
			// it fires no change and the reader cannot move.
			if selected[0] == "refs/tags/main" {
				t.Error("the missing ref resolved to the same-named tag")
			}
			values := optionValues(t, out)
			if len(values) < 2 {
				t.Fatalf("the picker offers no alternative to the missing ref: %q", values)
			}
			if values[0] == selected[0] {
				t.Error("the missing option is first, so the available ref cannot be chosen by moving away from it")
			}

			// The reader still sees a friendly name and the warning.
			if !strings.Contains(out, `>main</option>`) {
				t.Error("the option does not show the short name")
			}
			if !strings.Contains(out, "pill--missing") {
				t.Error("the missing warning is gone")
			}
		})
	}
}

func TestMissingRefPickerWorksWithoutScripting(t *testing.T) {
	// With no script the reader uses the native control and the submit button.
	// Both must carry real values: the form submits whatever option is chosen,
	// so each one has to be the ref the backend can act on.
	r := newRenderer(t)
	out := render(t, r, missingRefPage("refs/heads/main"))

	form := elementAt(t, out, `class="refbar__pick"`)
	if !strings.Contains(form, `method="get"`) {
		t.Error("the picker does not submit without scripting")
	}
	if !strings.Contains(out, `class="btn refbar__go"`) {
		t.Error("the no-script submit button is gone")
	}

	for _, v := range optionValues(t, out) {
		if v == "" {
			t.Error("an option submits an empty ref")
		}
		if v == "main" {
			t.Error("an option submits the bare short name, which may resolve to the wrong ref")
		}
	}

	// An unknown ref keeps its exact text through the round trip.
	out = render(t, r, missingRefPage("does-not-exist"))
	if got := selectedOptions(t, out); len(got) != 1 || got[0] != "does-not-exist" {
		t.Errorf("the requested ref was rewritten: %q", got)
	}
}

func TestUnknownRefKeepsAvailableBranchesReachable(t *testing.T) {
	// The other common case: the reader only ever pushed "master", so the
	// default "main" is absent. The branch that does exist must be a distinct,
	// selectable option.
	r := newRenderer(t)
	page := repoPage(fullChrome(LangEN), RepoTabCode)
	page.Ref = RefSelection{
		Name: "main", Kind: "branch", Missing: true,
		Branches: []RefOption{{Name: "master", URL: "/repositories/r1/code?ref=refs%2Fheads%2Fmaster"}},
	}
	page.CodeURL = "/repositories/r1/code?ref=refs%2Fheads%2Fmain"

	out := render(t, r, page)

	selected := selectedOptions(t, out)
	if len(selected) != 1 || selected[0] != "refs/heads/main" {
		t.Fatalf("selected options = %q, want only the missing refs/heads/main", selected)
	}
	values := optionValues(t, out)
	if !slices.Contains(values, "refs/heads/master") {
		t.Errorf("the branch that exists is not offered: %q", values)
	}
	// Choosing master is a real change away from the current selection.
	if values[0] == selected[0] {
		t.Error("the missing ref is the first option, so moving to master needs no change event")
	}
}

func TestResolvedRefStillHasExactlyOneSelectedOption(t *testing.T) {
	// The new explicit option must not double up when the ref does resolve.
	r := newRenderer(t)
	out := render(t, r, collidingRefPage(RepoTabCode))

	if got := selectedOptions(t, out); len(got) != 1 || got[0] != "refs/heads/release" {
		t.Errorf("selected options = %q, want only refs/heads/release", got)
	}
}

func TestMissingRefIsStatedOnceOnTheCodeTab(t *testing.T) {
	// The refbar warning and the code panel were printing the same generic
	// sentence, one directly under the other. Saying it twice adds nothing and
	// pushes the backend's specific explanation further away.
	r := newRenderer(t)

	for _, lang := range Langs() {
		page := missingRefPage("refs/heads/main")
		page.Chrome = fullChrome(lang)
		out := render(t, r, page)

		// Each bilingual element carries the text in a data attribute and in
		// its body, so elements are counted, not raw substrings.
		marker := `data-en="` + wantText(LangEN, MsgRepoRefMissing) + `"`
		if n := strings.Count(out, marker); n != 1 {
			t.Errorf("%s: the generic missing text appears in %d elements, want 1", lang, n)
		}
		// The authoritative warning stays.
		if !strings.Contains(out, "pill--missing") {
			t.Errorf("%s: the missing warning was removed along with the duplicate", lang)
		}
	}

	// A backend page notice about the deleted default branch is still shown.
	page := missingRefPage("refs/heads/main")
	page.Chrome.Notices = []Notice{{Kind: NoticeWarning, Code: MsgRepoDefaultGone}}
	if out := render(t, r, page); !strings.Contains(out, wantText(LangEN, MsgRepoDefaultGone)) {
		t.Error("the backend's default-branch warning is not rendered")
	}
}

func TestLongRefNameStaysAvailableInFull(t *testing.T) {
	// Shortening is visual only. The full name stays in the markup and in the
	// title, so it is never the only copy that was cut.
	r := newRenderer(t)

	for _, lang := range Langs() {
		c := fullChrome(lang)
		page := ActivityPage{
			Chrome: c,
			Days: []ActivityDayGroup{{
				Date: testNow,
				Entries: []ActivityEntry{{
					RepositoryID: "r1", RepositoryName: "hello-owngit",
					Ref: "feature/docs", RefRetained: true,
					Commit: CommitSummary{
						ShortOID: "a41c9e2", Subject: "Document the feature branch",
						AuthorName: "Dana", AuthorDate: testNow,
						URL: "/repositories/r1/commits/a41c9e2ff",
					},
				}},
			}},
			Activity: ActivityGraph{Year: 2026, Available: true, Complete: true, Total: 1, RepositoryCount: 1},
		}

		out := render(t, r, page)

		ref := out[strings.Index(out, `class="row__ref"`):]
		if end := strings.Index(ref, "</span>\n  <span class=\"row__msg\""); end >= 0 {
			ref = ref[:end]
		}
		if !strings.Contains(ref, `class="row__refname"`) {
			t.Errorf("%s: the ref name has no element of its own, so it cannot shorten separately: %q", lang, ref)
		}
		if !strings.Contains(out, `title="feature/docs"`) {
			t.Errorf("%s: the complete ref name is not available when shortened", lang)
		}
		if !strings.Contains(out, "feature/docs") {
			t.Errorf("%s: the ref name is not rendered", lang)
		}
		// The badge is still there, in full, as text rather than an image.
		if !strings.Contains(out, wantText(lang, MsgRepoRetainTitle)) {
			t.Errorf("%s: the retained badge text is missing", lang)
		}
	}
}

// A badge states a status; it cannot hold an instruction
//
// The repository list rendered the whole default-branch warning inside a
// .pill, which is a single non-wrapping line of fixed width. "The default
// branch no longer exists. Choose another branch, or push one with this name
// again." cannot fit, so it was cut off mid-sentence, exactly like the retained
// badge before it.
//
// The row now names the status. The repository's own pages keep the full
// explanation, including what to do, because that is where a reader acts on it.

func TestOverviewDefaultBranchBadgeIsShortEnoughToRead(t *testing.T) {
	r := newRenderer(t)

	for _, lang := range Langs() {
		out := render(t, r, OverviewPage{
			Chrome: fullChrome(lang), TotalCount: 1, Activity: sampleGraph(),
			Repositories: []RepositorySummary{{
				ID: "r1", Name: "forge-cli", URL: "/repositories/r1",
				DefaultBranchMissing: true,
			}},
		})

		// The status is still reported.
		short := wantText(lang, MsgRepoDefaultGoneShort)
		if !strings.Contains(out, short) {
			t.Errorf("%s: the row does not report the missing default branch", lang)
		}
		// The badge holds the short status, not the instruction.
		badge := out[strings.Index(out, `class="pill pill--missing"`):]
		if end := strings.Index(badge, "</span></span>"); end >= 0 {
			badge = badge[:end]
		}
		if strings.Contains(badge, wantText(lang, MsgRepoDefaultGone)) {
			t.Errorf("%s: the badge still holds the full sentence, which cannot fit on one line: %q", lang, badge)
		}
		// A character count is not proof that the text fits: the same string
		// measured 155.078px in the monospace stack and 123.406px in the
		// interface font, against 151px of room. So the width is estimated
		// from the font actually used.
		if w := badgeWidth(Text(lang, MsgRepoDefaultGoneShort)); w > rowBadgeRoom {
			t.Errorf("%s: the badge needs about %.1fpx but has %.1fpx: %q",
				lang, w, rowBadgeRoom, Text(lang, MsgRepoDefaultGoneShort))
		}
		// It must still say which branch kind is missing, not just "missing".
		for _, part := range map[Lang][]string{
			LangEN: {"Default branch"},
			LangKO: {"기본 브랜치"},
		}[lang] {
			if !strings.Contains(Text(lang, MsgRepoDefaultGoneShort), part) {
				t.Errorf("%s: the badge does not say what is missing: %q", lang, Text(lang, MsgRepoDefaultGoneShort))
			}
		}
	}
}

// Room for a badge in a repository row, and a rough width for its text.
//
// The numbers come from measuring the built page: .row__ref is 170px wide and
// the branch icon with its gap takes 19px, leaving 151px. Advances are
// per-character averages at 10.5px, taken from the same measurement: the
// interface font is proportional, and CJK glyphs are close to square.
const (
	rowBadgeRoom = 151.0
	pillPadding  = 16.0 // .pill padding: 1px 8px
	uiAdvance    = 5.26 // Pretendard, Latin, 10.5px
	cjkAdvance   = 9.8  // Pretendard, Hangul, 10.5px
)

func badgeWidth(text string) float64 {
	width := pillPadding
	for _, r := range text {
		if r > 0x2E80 {
			width += cjkAdvance
			continue
		}
		width += uiAdvance
	}
	return width
}

func TestRepositoryReviewScreenStates(t *testing.T) {
	// The full default-branch warning still tells the reader what to do, and
	// the row keeps a distinct short wording.
	for lang, parts := range map[Lang][]string{
		LangEN: {"Choose another branch", "push one with this name again"},
		LangKO: {"다른 브랜치를 고르거나", "다시 푸시하세요"},
	} {
		for _, part := range parts {
			if !strings.Contains(Text(lang, MsgRepoDefaultGone), part) {
				t.Errorf("%s: the full warning no longer says %q", lang, part)
			}
		}
		if Text(lang, MsgRepoDefaultGoneShort) == Text(lang, MsgRepoDefaultGone) {
			t.Errorf("%s: the row and the page share one wording again", lang)
		}
	}
	dayLinks := func(address bool) OverviewPage {
		graph := graphWithDayLinks()
		if !address {
			for i := range graph.Days {
				graph.Days[i].URL = ""
			}
		}
		return OverviewPage{Chrome: fullChrome(LangEN), Activity: graph}
	}
	missing := func(tab RepoTab) RepositoryPage {
		return with(repoPage(fullChrome(LangEN), tab), func(p *RepositoryPage) { p.Ref.Missing = true })
	}
	screens := []screen{
		// A day is the link itself, so a click and Enter do the same thing
		// without scripting; a day the backend cannot address is no control.
		screen{name: "each activity day links to its filtered list", page: dayLinks(true),
			markup: []string{`href="/activity?date=`},
			extra: func(t *testing.T, out string) {
				if tag := elementAt(t, out, "data-day"); !strings.HasPrefix(tag, "<a ") || !strings.Contains(tag, "href=") {
					t.Errorf("a day is not a link with a destination: %s", tag)
				}
			}},
		screen{name: "days without an address are not fake controls", page: dayLinks(false),
			noMarkup: []string{`href=""`},
			extra: func(t *testing.T, out string) {
				if tag := elementAt(t, out, "data-day"); strings.HasPrefix(tag, "<a ") {
					t.Errorf("a day with no address is still a link: %s", tag)
				}
			}},
		// The code and commits tabs say the ref does not resolve without
		// naming a cause they cannot know.
		screen{name: "a missing ref on the code tab is not blamed on the default branch", page: missing(RepoTabCode),
			want: []MessageCode{MsgRepoRefMissing}, absent: []MessageCode{MsgRepoDefaultGone}},
		screen{name: "a missing ref on the commits tab is not blamed on the default branch", page: missing(RepoTabCommits),
			want: []MessageCode{MsgRepoRefMissing}, absent: []MessageCode{MsgRepoDefaultGone}},
		screen{name: "a missing path explains itself when the ref resolves",
			page: with(repoPage(fullChrome(LangEN), RepoTabCode), func(p *RepositoryPage) {
				p.Code.NotFound, p.Code.Path = true, "cmd/missing"
			}),
			want: []MessageCode{MsgCodePathMissing}},
		// The repository list uses the same column as the activity rows.
		screen{name: "the overview branch name shortens separately",
			page: OverviewPage{Chrome: fullChrome(LangEN), Activity: sampleGraph(), Repositories: []RepositorySummary{{
				Name: "hello-owngit", URL: "/repositories/r1", DefaultBranch: "feature/a-rather-long-branch-name",
			}}},
			markup: []string{`class="row__refname"`, `title="feature/a-rather-long-branch-name"`}},
	}
	for _, lang := range Langs() {
		screens = append(screens,
			// Ref.Missing is also true for a branch that never existed, and
			// the backend's notice names the real reason.
			screen{name: string(lang) + " an unknown branch is not blamed on the default branch", lang: lang,
				page: with(repoPage(fullChrome(lang), RepoTabOverview), func(p *RepositoryPage) {
					p.Chrome.Notices = []Notice{{Kind: NoticeWarning, Code: MsgRepoRefMissing}}
					p.Ref.Missing, p.Ref.Name = true, "no-such-branch"
				}),
				want: []MessageCode{MsgRepoRefMissing}, absent: []MessageCode{MsgRepoDefaultGone}},
			// Shortening the row must not remove the explanation from where a
			// reader acts on it.
			screen{name: string(lang) + " repository pages keep the full default branch explanation", lang: lang,
				page: with(repoPage(fullChrome(lang), RepoTabCode), func(p *RepositoryPage) {
					p.Chrome.Notices = []Notice{{Kind: NoticeWarning, Code: MsgRepoDefaultGone}}
				}),
				want: []MessageCode{MsgRepoDefaultGone}})
	}
	checkScreens(t, screens...)
}
