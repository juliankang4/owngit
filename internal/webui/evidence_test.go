package webui

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

// These screens exist to report evidence about code, which makes their failure
// mode specific: a state the reader misreads as a pass is worse than no screen
// at all. The tests below are about that, and about the security properties of
// the credential screen.

// formNamed returns the markup of the POST form whose action contains the
// given fragment, so a test can assert on one form rather than the page.
func formNamed(t *testing.T, out, action string) string {
	t.Helper()
	for _, form := range strings.Split(out, "<form")[1:] {
		if end := strings.Index(form, "</form>"); end >= 0 {
			form = form[:end]
		}
		if strings.Contains(form, action) && strings.Contains(form, `method="post"`) {
			return form
		}
	}
	t.Fatalf("no POST form with action containing %q", action)
	return ""
}

func TestKoreanEvidenceUsesFamiliarDeveloperTerms(t *testing.T) {
	expected := map[MessageCode]string{
		MsgEvidenceAdvisory: "테스트, 린트, 빌드 같은 자동 체크와 코드 리뷰는 참고 정보이며 병합을 막지 않습니다.",
		MsgCheckTitle:       "체크",
		MsgCheckRevision:    "대상 커밋",
		MsgReviewTitle:      "리뷰",
		MsgTasksIntro:       "체크 에이전트가 실행한 명령과 각 결과가 어느 커밋에 해당하는지 보여 줍니다.",
		MsgHelperTitle:      "체크 에이전트 토큰",
		MsgHelperIntro:      "체크 에이전트가 결과를 보고할 때 쓰는 전용 토큰입니다. 저장소 접근 권한이나 관리자 비밀번호와는 별개입니다.",
		MsgHelperTokenLabel: "토큰",
	}
	for code, want := range expected {
		if got := Text(LangKO, code); got != want {
			t.Errorf("%s Korean text=%q, want %q", code, got, want)
		}
	}

	r := newRenderer(t)
	var rendered strings.Builder
	rendered.WriteString(render(t, r, pullRequestPage(fullChrome(LangKO), prFixtureFailing)))
	rendered.WriteString(render(t, r, tasksPage(fullChrome(LangKO), false)))
	rendered.WriteString(render(t, r, helperPage(fullChrome(LangKO), true)))
	out := rendered.String()
	for _, code := range []MessageCode{
		MsgEvidenceAdvisory, MsgCheckTitle, MsgReviewTitle, MsgTasksIntro,
		MsgHelperTitle, MsgHelperIntro, MsgHelperTokenLabel,
	} {
		if want := wantText(LangKO, code); !strings.Contains(out, want) {
			t.Errorf("rendered Korean screens do not contain %s text %q", code, want)
		}
	}

	for code, entry := range evidenceCatalog {
		for _, stale := range []string{"검사", "도우미", "자격 증명", "비밀 값", "이름표", "리비전"} {
			if strings.Contains(entry.ko, stale) {
				t.Errorf("%s Korean text still contains %q: %q", code, stale, entry.ko)
			}
		}
	}
}

func TestNoProtectionValueEverClaimsASandbox(t *testing.T) {
	// OwnGit runs checks in the developer's own environment. There is no
	// sandbox, so no protection value may be worded as one, including when the
	// backend sends the literal string.
	r := newRenderer(t)
	var all strings.Builder
	for _, lang := range Langs() {
		for _, page := range allPages(lang) {
			all.WriteString(render(t, r, page))
		}
	}
	out := all.String()
	for _, claim := range []string{"sandbox", "Sandbox", "격리된 환경", "안전한 환경"} {
		// "It is not a sandbox" is the one permitted use, so check the words
		// that would constitute a claim instead.
		if strings.Contains(out, claim) && !strings.Contains(out, "not a sandbox") {
			t.Errorf("the interface claims a sandbox (%q)", claim)
		}
	}
	// A backend value of "sandboxed" is not recognised and produces no note at
	// all, rather than a protection claim.
	if protectionNote("sandboxed") != "" {
		t.Error(`a protection value of "sandboxed" produced a note`)
	}
}

func TestLogDispositionIsFourDistinctAnswers(t *testing.T) {
	// The raw log is disposable and the durable record outlives it. "Not
	// recorded", "expired", "could not be read" and "not stated" are different
	// facts, and collapsing them would let a missing log imply a missing run.
	seen := map[MessageCode]string{}
	for _, status := range []string{LogAvailable, LogExpired, LogNotRecorded, LogUnavailable, LogUnknown, "shredded"} {
		note := logNote(status)
		if prior, ok := seen[note]; ok && prior != LogUnknown && status != "shredded" {
			t.Errorf("log statuses %q and %q produce the same sentence", prior, status)
		}
		seen[note] = status
	}
	// An unrecognised value states nothing rather than implying availability.
	if logNote("shredded") != MsgCheckLogUnknown {
		t.Error("an unrecognised log status does not fall back to a non-claim")
	}
}

func TestNoUserFacingIdempotencyBadge(t *testing.T) {
	// Attempt identity is a backend property proven by backend tests. The
	// interface may show an identifier, but it must not award a badge that
	// asserts the property to the reader.
	r := newRenderer(t)
	var all strings.Builder
	for _, lang := range Langs() {
		for _, page := range allPages(lang) {
			all.WriteString(render(t, r, page))
		}
	}
	out := all.String()
	for _, claim := range []string{"idempot", "Idempot", "멱등"} {
		if strings.Contains(out, claim) {
			t.Errorf("the interface asserts an idempotency property (%q)", claim)
		}
	}
	// The identifier itself is still available, which is what a reader
	// actually needs to correlate a run.
	if !strings.Contains(out, "att_9f31c0d4") {
		t.Error("no attempt identifier is shown anywhere")
	}
}

func TestLegacyDecisionRequiredReadsAsNoReview(t *testing.T) {
	// An older record means this revision has no review. It is not an
	// instruction and not a mandatory step.
	if got := reviewState(ReviewDecisionRequired).Code; got != MsgReviewStateNone {
		t.Errorf("decision_required renders as %q, want the no-review state", got)
	}
	if reviewState(ReviewDecisionRequired) != reviewState(ReviewNotRequested) {
		t.Error("a legacy record and an absent record read differently")
	}
}

// create: the submitted commits describe the selected branches

func TestCreateSubmitsTheCommitsObservedForTheSelectedBranches(t *testing.T) {
	// The defect this prevents: picking a second branch pair while the page
	// still carries the first pair's commit ids, which without scripting would
	// create a pull request for code nobody looked at. The selection is a
	// server round trip, so the names and ids always describe one observation.
	r := newRenderer(t)
	page := newPullRequestPage(fullChrome(LangEN), true)
	out := render(t, r, page)

	form := formNamed(t, out, page.SubmitURL)
	for _, want := range []string{
		`name="source_branch" value="` + page.Source.Branch + `"`,
		`name="target_branch" value="` + page.Target.Branch + `"`,
		`name="source_oid" value="` + page.Source.OID + `"`,
		`name="target_oid" value="` + page.Target.OID + `"`,
	} {
		if !strings.Contains(form, want) {
			t.Errorf("the create form does not submit %s", want)
		}
	}
	// Exactly one pair is submitted, so no other branch's tip can travel with
	// the request.
	if n := strings.Count(form, `name="source_oid"`); n != 1 {
		t.Errorf("the create form submits %d source commits, want 1", n)
	}
	if !strings.Contains(out, wantText(LangEN, MsgPRNewObservedTips)) {
		t.Error("the screen does not explain that a moved branch fails instead of being used")
	}
}

func TestChangingBranchesReturnsToAFollowableSelection(t *testing.T) {
	r := newRenderer(t)
	page := newPullRequestPage(fullChrome(LangEN), true)
	back := pullRequestSelectionURL(page)

	if !strings.Contains(back, "source=fix%2Fcursor") || !strings.Contains(back, "target=main") {
		t.Errorf("the selection address does not carry the chosen branches: %s", back)
	}
	// It carries no commit ids: returning here reads the tips again rather
	// than reusing a pair that may already be out of date.
	if strings.Contains(back, "oid") || strings.Contains(back, page.Source.OID) {
		t.Errorf("the selection address carries an observed commit: %s", back)
	}
	if out := render(t, r, page); !strings.Contains(out, `href="`+strings.ReplaceAll(back, "&", "&amp;")+`"`) {
		t.Error("the change-branches control does not lead to the selection address")
	}
}

func TestPullRequestActionsResubmitTheRevisionOnScreen(t *testing.T) {
	// Every action carries the tips this page was rendered from, so a branch
	// that moved is refused rather than acted on behind the reader's back.
	r := newRenderer(t)
	page := pullRequestPage(fullChrome(LangEN), prFixtureFailing)
	out := render(t, r, page)

	for _, action := range []string{"/review/request", "/review/skip", "/merge"} {
		form := formNamed(t, out, action)
		if !strings.Contains(form, `name="source_oid" value="`+page.Source.OID+`"`) {
			t.Errorf("%s does not resubmit the source commit on screen", action)
		}
		if !strings.Contains(form, `name="target_oid" value="`+page.Target.OID+`"`) {
			t.Errorf("%s does not resubmit the target commit on screen", action)
		}
		if !strings.Contains(form, `name="csrf"`) {
			t.Errorf("%s has no CSRF token", action)
		}
	}
}

// helper credentials

func TestIssuingAndRevokingEachCollectTheAdminPassword(t *testing.T) {
	// A remembered administrator session reads the list. Minting or destroying
	// a credential is a security change and re-verifies the password each
	// time, the same rule the settings screen follows.
	r := newRenderer(t)
	out := render(t, r, helperPage(fullChrome(LangEN), false))

	forms := 0
	for _, form := range strings.Split(out, `method="post"`)[1:] {
		if end := strings.Index(form, "</form>"); end >= 0 {
			form = form[:end]
		}
		forms++
		if !strings.Contains(form, `name="admin_password"`) {
			t.Errorf("a credential form does not re-verify the administrator password: %.160s", form)
		}
		if !strings.Contains(form, `name="csrf"`) {
			t.Errorf("a credential form has no CSRF token: %.160s", form)
		}
	}
	// One issue form plus one revoke form per active credential.
	if forms < 3 {
		t.Errorf("rendered %d credential forms, want an issue form and one per active credential", forms)
	}
	if !strings.Contains(out, wantText(LangEN, MsgHelperPasswordEachTime)) {
		t.Error("the screen does not say the password is required for each operation")
	}
}

func TestHelperCredentialLabelShowsTheStoredUTF8ByteBoundary(t *testing.T) {
	r := newRenderer(t)
	expected := map[Lang]string{
		LangEN: "Enter a single-line label between 1 and 100 UTF-8 bytes.",
		LangKO: "한 줄 이름을 UTF-8 기준 100바이트 이내로 입력하세요. 한글만 쓰면 최대 33자입니다.",
	}
	for _, lang := range Langs() {
		page := helperPage(fullChrome(lang), false)
		page.PendingAction = ActionIssueHelperCredential
		page.Chrome.Notices = []Notice{Error("label", MsgHelperLabelInvalid)}
		out := render(t, r, page)
		tag := regexp.MustCompile(`<input id="helper-label"[^>]*>`).FindString(out)
		for _, attribute := range []string{`name="label"`, `type="text"`, `required`, `maxlength="100"`, `autocomplete="off"`} {
			if !strings.Contains(tag, attribute) {
				t.Errorf("%s label input lost %s: %s", lang, attribute, tag)
			}
		}
		if strings.Contains(tag, `maxlength="200"`) {
			t.Errorf("%s label input still promises 200 characters", lang)
		}
		if got := wantText(lang, MsgHelperLabelInvalid); got != expected[lang] {
			t.Errorf("%s invalid-label text=%q, want %q", lang, got, expected[lang])
		}
		if !strings.Contains(out, expected[lang]) {
			t.Errorf("%s invalid-label text was not rendered", lang)
		}
		if !strings.Contains(out, wantText(lang, MsgHelperLabelHelp)) {
			t.Errorf("%s label help intent disappeared", lang)
		}
	}
}

func TestEveryAdminPasswordFieldOnTheCredentialScreenIsUnique(t *testing.T) {
	// Several forms collect the same field name. Duplicate ids would point
	// every aria-describedby at the first one, announcing the wrong form's
	// error.
	r := newRenderer(t)
	out := render(t, r, helperPage(fullChrome(LangEN), true))

	ids := regexp.MustCompile(`id="([^"]+)"`).FindAllStringSubmatch(out, -1)
	seen := map[string]bool{}
	for _, m := range ids {
		if seen[m[1]] {
			t.Errorf("duplicate id %q on the credential screen", m[1])
		}
		seen[m[1]] = true
	}
}

func TestIssuedTokenIsShownOnceAndNeverPersisted(t *testing.T) {
	// The secret exists in this one response. It must never reach a URL, a
	// link, browser storage, or the page's own scripting, because each of
	// those outlives the response and is readable afterwards.
	r := newRenderer(t)
	out := render(t, r, helperPage(fullChrome(LangEN), true))

	if n := strings.Count(out, helperTestToken); n != 1 {
		t.Fatalf("the token appears %d times, want exactly once", n)
	}
	// Not in any address: a query string or href would put it in history and
	// in the referrer of the next request.
	for _, m := range regexp.MustCompile(`(?:href|src|action|formaction)="([^"]*)"`).FindAllStringSubmatch(out, -1) {
		if strings.Contains(m[1], helperTestToken) {
			t.Errorf("the token reached an address: %s", m[1])
		}
	}
	// Not submitted back to the server by any form.
	if strings.Contains(out, `value="`+helperTestToken+`"`) {
		t.Error("the token is carried in a form value")
	}
	// Not handed to scripting for storage.
	for _, api := range []string{"localStorage", "sessionStorage", "indexedDB", "document.cookie"} {
		if strings.Contains(out, api) {
			t.Errorf("the credential screen references %s", api)
		}
	}
	if !strings.Contains(out, wantText(LangEN, MsgHelperTokenOnce)) {
		t.Error("the screen does not warn that this is the only time the secret is shown")
	}
}

func TestCredentialScreenLanguageLinkIsAFollowableGET(t *testing.T) {
	// The issuing response is a POST result. A language link built from the
	// request URL would be a GET at a route that refuses GET, and following it
	// would lose the screen.
	page := helperPage(fullChrome(LangEN), true)
	page.Chrome.CurrentURL = "/repositories/r1/helper-credentials"
	if got := canonicalURL(page, page.Chrome); got != page.SelfURL {
		t.Errorf("language links use %q, want the screen's own address %q", got, page.SelfURL)
	}

	pr := pullRequestPage(fullChrome(LangEN), prFixtureFailing)
	pr.Chrome.CurrentURL = "/repositories/r1/pull-requests/12/merge"
	if got := canonicalURL(pr, pr.Chrome); got != pr.SelfURL {
		t.Errorf("a refused merge sends language links to %q, want %q", got, pr.SelfURL)
	}
}

func TestNoServerSecretReachesTheEvidenceScreens(t *testing.T) {
	// General repository sessions read task results without a helper
	// credential, and no page may embed a bearer token or server secret.
	r := newRenderer(t)
	var all strings.Builder
	for _, lang := range Langs() {
		for name, page := range allPages(lang) {
			if strings.HasPrefix(name, "helper-credentials") {
				continue
			}
			all.WriteString(render(t, r, page))
		}
	}
	out := all.String()
	for _, secret := range []string{helperTestToken, "Bearer ", "Authorization:", "X-Owngit-Admin-Password"} {
		if strings.Contains(out, secret) {
			t.Errorf("a credential or header value (%q) reached an ordinary screen", secret)
		}
	}
}

// version

func TestRunningVersionComesFromTheBackendOnly(t *testing.T) {
	// The version on screen is whatever the backend passed from the binary's
	// own version source. This package must contain no version literal of its
	// own, because a stale copy here would be a confident lie.
	r := newRenderer(t)
	c := fullChrome(LangEN)
	c.Version = "9.9.9-test"
	if out := render(t, r, OverviewPage{Chrome: c, Activity: sampleGraph()}); !strings.Contains(out, "9.9.9-test") {
		t.Error("the version the backend supplied is not shown")
	}

	// With nothing supplied, nothing is claimed.
	c.Version = ""
	out := render(t, r, OverviewPage{Chrome: c, Activity: sampleGraph()})
	if strings.Contains(out, "toolbar__ver") {
		t.Error("an absent version renders a version element anyway")
	}
	if regexp.MustCompile(`\b\d+\.\d+\.\d+\b`).MatchString(out) {
		t.Error("a version number appeared without the backend supplying one")
	}
}

func TestVersionIsVisibleOnEveryDashboardScreen(t *testing.T) {
	// fullChrome carries a version, so every screen built on it proves the
	// answer to "which OwnGit is this" is reachable without a terminal.
	r := newRenderer(t)
	for _, lang := range Langs() {
		for name, page := range allPages(lang) {
			chrome, ok := chromeOf(page)
			if !ok || chrome.Version == "" {
				continue // setup and sign-in render without the dashboard chrome
			}
			if out := render(t, r, page); !strings.Contains(out, chrome.Version) {
				t.Errorf("%s/%s: the running version is not visible", lang, name)
			}
		}
	}
}

func TestPackageContainsNoVersionLiteral(t *testing.T) {
	// A version written here would drift from the binary's own version and
	// state it with total confidence. There is one source, and it is the
	// backend's.
	number := regexp.MustCompile(`\b\d+\.\d+\.\d+\b`)
	paths, err := filepath.Glob("*.go")
	noErr(t, err)
	templates, err := filepath.Glob("templates/*.html")
	noErr(t, err)
	pages, err := filepath.Glob("templates/pages/*.html")
	noErr(t, err)
	for _, path := range append(append(paths, templates...), pages...) {
		if strings.HasSuffix(path, "_test.go") {
			continue // fixtures deliberately carry a synthetic version
		}
		data, err := os.ReadFile(path)
		noErr(t, err)
		for _, line := range strings.Split(string(data), "\n") {
			// Example text in comments names real Git and font versions, which
			// are not claims about OwnGit.
			if strings.Contains(line, "//") || strings.Contains(line, "{{/*") {
				continue
			}
			if hit := number.FindString(line); hit != "" {
				t.Errorf("%s: version-like literal %q in package source", path, hit)
			}
		}
	}
}

func TestBackendLogDispositionMapsOntoTheRenderedVocabulary(t *testing.T) {
	// The wire says found, expired, or missing. Anything else must degrade to
	// a non-claim rather than implying the log can still be read.
	for wire, want := range map[string]string{
		"found":             LogAvailable,
		"expired":           LogExpired,
		"missing":           LogNotRecorded,
		"":                  LogUnknown,
		"some_future_state": LogUnknown,
	} {
		if got := LogStatusOf(wire); got != want {
			t.Errorf("LogStatusOf(%q) = %q, want %q", wire, got, want)
		}
	}
	// An unmapped value never reads as an available log.
	if logNote(LogStatusOf("some_future_state")) != MsgCheckLogUnknown {
		t.Error("an unmapped log disposition does not render as a non-claim")
	}
}

func TestNoPageRequiresAnInlineScriptOrStyleException(t *testing.T) {
	// Inline handlers and inline style attributes both need a CSP exception
	// the real server does not grant, so anything relying on one is dead in
	// the product even though it works in a test fixture.
	//
	// This checks every attribute rather than a list of event names. The
	// earlier list omitted onfocus, and a token field shipped using it.
	r := newRenderer(t)
	javascriptURL := regexp.MustCompile(`(?i)(?:href|src|action|formaction)\s*=\s*["']?\s*javascript:`)

	for _, lang := range Langs() {
		for name, page := range allPages(lang) {
			out := render(t, r, page)
			if hit := inlineEventHandler(out); hit != "" {
				t.Errorf("%s/%s: inline event handler %q needs a CSP exception", lang, name, hit)
			}
			if hit := javascriptURL.FindString(out); hit != "" {
				t.Errorf("%s/%s: javascript: URL %q", lang, name, strings.TrimSpace(hit))
			}
			// An inline <script> or <style> block would need the same
			// exception, so the page carries neither.
			if strings.Contains(out, "<script>") {
				t.Errorf("%s/%s: inline script block", lang, name)
			}
			if strings.Contains(out, "<style") {
				t.Errorf("%s/%s: inline style block", lang, name)
			}
		}
	}
}

func TestTokenSelectionIsHandledByTheExternalScript(t *testing.T) {
	// The convenience survives the handler's removal: the script hooks the
	// same field, and the field is selectable by hand without scripting.
	r := newRenderer(t)
	out := render(t, r, helperPage(fullChrome(LangEN), true))

	if !strings.Contains(out, "data-select-on-focus") {
		t.Fatal("the token field carries no hook for the external script")
	}
	script, err := assetFS.ReadFile("assets/owngit.js")
	noErr(t, err)
	if !strings.Contains(string(script), "data-select-on-focus") {
		t.Error("the external script does not implement the selection hook")
	}
	// Without scripting the value is still readable and selectable, so the
	// screen never depends on the hook.
	if !strings.Contains(out, helperTestToken) {
		t.Error("the token is not present as readable text")
	}
	if strings.Contains(out, "disabled") {
		t.Error("the token field is disabled, which would prevent manual selection")
	}
}

// htmlTag matches one element's opening tag, and inlineEventAttribute matches
// an on* attribute name inside it.
//
// Scanning tags rather than the whole document is what keeps escaped code in
// page content from being reported: a diff line reading "el.onfocus = fn" is
// text a reader sees, not a handler the browser runs. The name match is case
// insensitive because HTML attribute names are, and it stops at the equals
// sign so an unquoted value matches too.
var (
	htmlTag              = regexp.MustCompile(`<[a-zA-Z][^<>]*>`)
	inlineEventAttribute = regexp.MustCompile(`(?i)[\s"\']on[a-z]+\s*=`)
)

// inlineEventHandler returns the first inline event attribute in the document,
// or the empty string when there is none.
func inlineEventHandler(document string) string {
	for _, tag := range htmlTag.FindAllString(document, -1) {
		if match := inlineEventAttribute.FindString(tag); match != "" {
			return strings.TrimSpace(match)
		}
	}
	return ""
}

func TestInlineHandlerDetectionIgnoresCaseQuotingAndEscapedText(t *testing.T) {
	// The earlier pattern matched lowercase quoted attributes only, so an
	// uppercase or unquoted handler would have shipped unnoticed. It must
	// still leave escaped code in page text alone, since that is content a
	// reader sees rather than something the browser runs.
	caught := []string{
		`<textarea onfocus="this.select()">`,
		`<textarea ONFOCUS="this.select()">`,
		`<textarea onFocus="this.select()">`,
		`<textarea onfocus=this.select()>`,
		`<button onclick = "go()">`,
		`<form onsubmit='return false'>`,
		`<img src="x" ONERROR=alert(1)>`,
	}
	for _, markup := range caught {
		if inlineEventHandler(markup) == "" {
			t.Errorf("an inline handler went undetected: %s", markup)
		}
	}

	ignored := []string{
		`<pre>el.onfocus = function () {};</pre>`,
		`<pre>&lt;div onclick=&quot;go()&quot;&gt;</pre>`,
		`<span class="mono">form.onsubmit</span>`,
		`<input data-on="x">`,
		`<a href="/search?on=1">link</a>`,
		`<label for="button-only">x</label>`,
	}
	for _, markup := range ignored {
		if hit := inlineEventHandler(markup); hit != "" {
			t.Errorf("harmless markup reported as a handler (%q): %s", hit, markup)
		}
	}
}

func TestTheCleanupReasonIsShownSeparatelyAndEscaped(t *testing.T) {
	// The reason is backend text about a process, so it belongs beside the
	// output rather than inside the box a reader takes for command output.
	// Like every recorded string, it is escaped.
	r := newRenderer(t)
	out := render(t, r, uncleanTasksPage(fullChrome(LangEN)))

	if !strings.Contains(out, "killpg 40211: operation not permitted") {
		t.Fatal("the recorded cleanup reason is not shown")
	}
	if strings.Contains(out, `killpg 40211: operation not permitted <&"'>`) {
		t.Error("the cleanup reason was emitted as raw markup")
	}
	if !strings.Contains(out, "&lt;&amp;&#34;&#39;&gt;") {
		t.Error("the cleanup reason is not HTML escaped")
	}
	// It sits outside the output box, so it cannot be read as something the
	// check printed.
	for _, box := range strings.Split(out, `<pre class="result__out">`)[1:] {
		printed := box[:strings.Index(box, "</pre>")]
		if strings.Contains(printed, "killpg") {
			t.Error("the cleanup reason was placed inside the command output box")
		}
	}
}

// attemptHeads returns the head block of each rendered attempt, which is where
// the attempt's own status chip lives.
func attemptHeads(t *testing.T, document string) []string {
	t.Helper()
	var heads []string
	for _, part := range strings.Split(document, `<div class="attempt__head">`)[1:] {
		end := strings.Index(part, "</div>")
		if end < 0 {
			t.Fatal("an attempt head was never closed")
		}
		heads = append(heads, part[:end])
	}
	if len(heads) == 0 {
		t.Fatal("no attempts were rendered")
	}
	return heads
}

// resultNamed returns the rendered block for one check inside an attempt.
func resultNamed(t *testing.T, document, name string) string {
	t.Helper()
	for _, part := range strings.Split(document, `<div class="result">`)[1:] {
		if end := strings.Index(part, `<div class="result">`); end >= 0 {
			part = part[:end]
		}
		if strings.Contains(part, `<span class="result__n">`+name+`</span>`) {
			return part
		}
	}
	t.Fatalf("no rendered result named %q", name)
	return ""
}

func TestTheUncleanStatusIsANonSuccessLabelInBothLanguages(t *testing.T) {
	// A label reading "Passed, cleanup failed" would undo the rule the rest of
	// this file enforces, so the words themselves are checked rather than the
	// constant's current value.
	english := catalog[MsgCheckStateUnclean].en
	korean := catalog[MsgCheckStateUnclean].ko

	if english != "Cleanup failed" {
		t.Errorf("English unclean status is %q, want a non-success label", english)
	}
	if korean != "정리 실패" {
		t.Errorf("Korean unclean status is %q, want a non-success label", korean)
	}

	// No wording that asserts the checks succeeded.
	for _, banned := range []string{"Passed", "passed", "Pass", "Success", "succeeded", "OK"} {
		if strings.Contains(english, banned) {
			t.Errorf("English unclean status claims success with %q: %q", banned, english)
		}
	}
	for _, banned := range []string{"통과", "성공", "정상"} {
		if strings.Contains(korean, banned) {
			t.Errorf("Korean unclean status claims success with %q: %q", banned, korean)
		}
	}
}

func TestCleanupWordingClaimsNoMoreThanTheRecordEstablishes(t *testing.T) {
	// The record says one thing: OwnGit did not confirm the processes it
	// started had stopped. It does not establish that they are still running,
	// that work continues, or that the machine was restored.
	warning := catalog[MsgCheckCleanupFailed]

	for _, overreach := range []string{
		"still running", "is running", "still doing work", "restored", "clean machine",
	} {
		if strings.Contains(strings.ToLower(warning.en), overreach) {
			t.Errorf("English cleanup warning asserts %q: %q", overreach, warning.en)
		}
	}
	for _, overreach := range []string{"복원", "정상으로", "실행 중입니다"} {
		if strings.Contains(warning.ko, overreach) {
			t.Errorf("Korean cleanup warning asserts %q: %q", overreach, warning.ko)
		}
	}

	// It is scoped to processes this run owned, not to the machine at large.
	if !strings.Contains(warning.en, "this run started") {
		t.Errorf("English cleanup warning is not scoped to owned processes: %q", warning.en)
	}
	if !strings.Contains(warning.ko, "이 실행이 시작한") {
		t.Errorf("Korean cleanup warning is not scoped to owned processes: %q", warning.ko)
	}
}

// TestTheSecondaryActionsAreOneCompactDisclosure checks the record-keeping
// controls.
//
// They were a second expanded card that restated the advisory point and the
// merge explanation the summary already makes. Folding them keeps the real
// forms, their tokens and the revision they bind to.
func TestTheSecondaryActionsAreOneCompactDisclosure(t *testing.T) {
	r := newRenderer(t)
	page := pullRequestPage(fullChrome(LangEN), prFixtureFailing)
	out := render(t, r, page)

	title := wantText(LangEN, MsgPRActionsTitle)
	position := strings.Index(out, title)
	if position < 0 {
		t.Fatal("the secondary actions disappeared")
	}
	// It is a disclosure summary, not a card heading.
	if !strings.Contains(out[max(0, position-120):position], "<summary>") {
		t.Error("the secondary actions are not behind a disclosure")
	}

	// The real forms survive, with their token and the revision pair each
	// action is bound to.
	for _, want := range []string{
		`name="csrf"`,
		`name="source_oid" value="` + sourceRevision().OID,
		`name="target_oid" value="` + targetRevision().OID,
		wantText(LangEN, MsgPRRequestReview),
		wantText(LangEN, MsgPRSkipReview),
		// Recording a request is not asking a model to review.
		wantText(LangEN, MsgReviewRequestIntent),
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the disclosure lost %q", want)
		}
	}

	// Merge stays primary and outside the disclosure, with its explanation.
	merge := strings.Index(out, `class="btn btn--primary"`)
	if merge < 0 {
		t.Fatal("the merge control is no longer primary")
	}
	if merge > position {
		t.Error("the merge control moved below the secondary actions")
	}
	if !strings.Contains(out, wantText(LangEN, MsgPRMergeHelp)) {
		t.Error("the merge explanation disappeared with the second card")
	}
}

func TestAutomaticRunsDoNotBorrowTheManualWording(t *testing.T) {
	// A manual helper run happens in the operator's own environment with their
	// permissions, reported by their own repository credential. A server-owned
	// automatic job is a different machine and a different authority. Saying
	// the manual sentence over an automatic job names the wrong one.
	manualProtection := wantText(LangEN, MsgCheckProtectionInherited)
	manualProvenance := wantText(LangEN, MsgCheckProvenanceHelper)

	for _, tc := range []struct {
		name        string
		protection  string
		provenance  string
		wantMessage MessageCode
		wantOrigin  MessageCode
	}{
		{"host job", ProtectionAutomaticHost, ProvenanceAutomaticJob,
			MsgCheckProtectionAutoHost, MsgCheckProvenanceAutomatic},
		{"container job", ProtectionAutomaticContainer, ProvenanceAutomaticJob,
			MsgCheckProtectionAutoContainer, MsgCheckProvenanceAutomatic},
		{"external runner", ProtectionRunnerReported, ProvenanceRunnerClaimed,
			MsgCheckProtectionRunner, MsgCheckProvenanceRunner},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := protectionNote(tc.protection); got != tc.wantMessage {
				t.Errorf("protection note = %q, want %q", got, tc.wantMessage)
			}
			if got := provenanceNote(tc.provenance); got != tc.wantOrigin {
				t.Errorf("provenance note = %q, want %q", got, tc.wantOrigin)
			}
			for _, lang := range Langs() {
				protection := Text(lang, tc.wantMessage)
				origin := Text(lang, tc.wantOrigin)
				if protection == "" || origin == "" {
					t.Fatalf("%s: an automatic run has no wording of its own", lang)
				}
				if protection == Text(lang, MsgCheckProtectionInherited) {
					t.Errorf("%s: the automatic run reuses the manual environment sentence", lang)
				}
				if origin == Text(lang, MsgCheckProvenanceHelper) {
					t.Errorf("%s: the automatic run reuses the manual credential sentence", lang)
				}
			}
		})
	}

	// The manual wording still exists for the run it actually describes.
	if protectionNote(ProtectionInherited) != MsgCheckProtectionInherited {
		t.Error("the manual environment sentence was lost")
	}
	if provenanceNote(ProvenanceAuthenticatedHelper) != MsgCheckProvenanceHelper {
		t.Error("the manual credential sentence was lost")
	}

	// The configured-check job detail shows automatic jobs, so neither manual
	// sentence may appear on it.
	r := newRenderer(t)
	out := render(t, r, allPages(LangEN)["configured-checks-job"])
	if strings.Contains(out, manualProtection) {
		t.Error("the automatic job detail says the run used the operator's own environment")
	}
	if strings.Contains(out, manualProvenance) {
		t.Error("the automatic job detail credits the manual check helper")
	}
	if !strings.Contains(out, wantText(LangEN, MsgCheckProtectionAutoHost)) {
		t.Error("the automatic job detail does not say where it actually ran")
	}
	if !strings.Contains(out, wantText(LangEN, MsgCheckProvenanceAutomatic)) {
		t.Error("the automatic job detail does not say who ran it")
	}
	// The new wording is still not a sandbox claim.
	if !strings.Contains(Text(LangEN, MsgCheckProtectionAutoHost), "not a sandbox") {
		t.Error("the automatic host wording stopped saying it is not a sandbox")
	}
}

// prEN is the failing pull request fixture in English after edit.
func prEN(edit func(*PullRequestPage)) PullRequestPage {
	return with(pullRequestPage(fullChrome(LangEN), prFixtureFailing), edit)
}

func unchanged[P any](*P) {}

// TestMergeStaysAdvisory covers the product rule these screens honour: check
// and review results are information, never a gate. Only the backend's own
// refusal disables merging, and the reasons it shows are Git and repository
// state.
func TestMergeStaysAdvisory(t *testing.T) {
	var screens []screen
	for _, status := range []string{CheckFailed, CheckError, CheckCancelled, CheckIncomplete, CheckUnavailable, CheckStale, CheckAbsent} {
		screens = append(screens, screen{name: "check " + status + " is stated and does not block",
			page: prEN(func(p *PullRequestPage) { p.Checks.Status = status }),
			want: []MessageCode{checkState(status).Code}, merge: mergeOffered})
	}
	for _, status := range []string{ReviewPending, ReviewChanges, ReviewUnavailable, ReviewPartial, ReviewNotRequested, ReviewSkipped} {
		screens = append(screens, screen{name: "review " + status + " does not block",
			page: prEN(func(p *PullRequestPage) {
				p.Review = ReviewEvidence{Status: status, BoundToCurrentRevision: true, SubmittedAt: testNow}
			}),
			merge: mergeOffered})
	}
	// The stricter eligibility rule must not become a back door for advisory
	// results: every failing and pending combination still merges.
	for _, check := range []string{CheckFailed, CheckError, CheckCancelled, CheckIncomplete, CheckUnavailable, CheckStale, CheckAbsent, CheckPending} {
		for _, review := range []string{ReviewPending, ReviewChanges, ReviewUnavailable, ReviewPartial, ReviewNotRequested} {
			screens = append(screens, screen{name: "eligible with check " + check + " and review " + review,
				page: prEN(func(p *PullRequestPage) {
					p.Merge = MergeAvailability{Eligible: true}
					p.Checks.Status = check
					p.Checks.ReadFailure = nil
					p.Review = ReviewEvidence{Status: review, BoundToCurrentRevision: true, SubmittedAt: testNow}
				}),
				absent: []MessageCode{MsgMergeUnexplained}, merge: mergeOffered})
		}
	}
	screens = append(screens,
		screen{name: "an unreadable check record does not block",
			page: prEN(func(p *PullRequestPage) {
				p.Merge = MergeAvailability{Eligible: true}
				p.Checks.ReadFailure = &EvidenceReadFailure{Code: ReadFailureCheckEvidence, Message: MsgCheckRecordUnreadable}
			}),
			merge: mergeOffered},
		screen{name: "the advisory contract is stated beside the evidence", page: prEN(unchanged),
			want: []MessageCode{MsgEvidenceAdvisory, MsgPRMergeDespite, MsgReviewRequestIntent}},
		screen{name: "a negative review leaves the control enabled",
			page: prEN(func(p *PullRequestPage) {
				p.Review = ReviewEvidence{Status: ReviewChanges, BoundToCurrentRevision: true, SubmittedAt: testNow}
			}),
			want: []MessageCode{MsgPRMergeDespite}, noMarkup: []string{"disabled"}},
		// A reason this interface has no words for is shown generically with
		// the backend's own code, and the failing check stays information
		// rather than a listed reason.
		screen{name: "a backend refusal disables merging and explains itself",
			page:   pullRequestPage(fullChrome(LangEN), prFixtureBlocked),
			want:   []MessageCode{MsgPRMergeRefused, MsgMergeBlockedOther, MsgEvidenceAdvisory},
			absent: []MessageCode{MsgPRMergeDespite}, markup: []string{"some_new_backend_reason"},
			merge: mergeRefused,
			extra: func(t *testing.T, out string) {
				blockers := out[strings.Index(out, `class="blockers"`):]
				if end := strings.Index(blockers, "</ul>"); end >= 0 {
					blockers = blockers[:end]
				}
				for _, advisory := range []MessageCode{MsgCheckStateFailed, MsgCheckStateAbsent, MsgReviewStateNone, MsgReviewStatePending} {
					if strings.Contains(blockers, wantText(LangEN, advisory)) {
						t.Errorf("%q is listed as a merge blocker, but it is advisory", advisory)
					}
				}
			}},
		screen{name: "a merged request offers no further actions and keeps its record",
			page:     pullRequestPage(fullChrome(LangEN), prFixtureMerged),
			want:     []MessageCode{MsgEvidenceAdvisory},
			absent:   []MessageCode{MsgPRMergeDespite},
			markup:   []string{"c07f4ab", "refs/owngit/merges/12", "merge-commit"},
			noMarkup: []string{`action="/repositories/r1/pull-requests/12/merge"`}},
		// A decision that says eligible while listing a reason against it is
		// not a licence to offer the action.
		screen{name: "an eligible decision with a stated blocker is refused",
			page: prEN(func(p *PullRequestPage) {
				p.Merge = MergeAvailability{Eligible: true, Blockers: []MergeBlocker{{Code: "merge_conflict", Detail: "internal/retry/backoff.go"}}}
			}),
			want: []MessageCode{MsgMergeBlockedConflict}, merge: mergeRefused},
		screen{name: "an eligible decision with no blockers is offered",
			page:  prEN(func(p *PullRequestPage) { p.Merge = MergeAvailability{Eligible: true} }),
			merge: mergeOffered},
		screen{name: "a refusal without a reason says so",
			page:     prEN(func(p *PullRequestPage) { p.Merge = MergeAvailability{Eligible: false} }),
			want:     []MessageCode{MsgMergeUnexplained},
			noMarkup: []string{`<ul class="blockers">`}, merge: mergeRefused},
		screen{name: "every unknown blocker is displayed",
			page: prEN(func(p *PullRequestPage) {
				p.Merge = MergeAvailability{Blockers: []MergeBlocker{
					{Code: "future_reason_one"}, {Code: "merge_conflict"}, {Code: "future_reason_two", Detail: "extra context"},
				}}
			}),
			markup: []string{"future_reason_one", "future_reason_two", "extra context"},
			extra:  countIs(`class="blocker"`, 3)},
		// A blocker list that explains something drops only the empty entries.
		screen{name: "a mixed blocker list shows only the reasons it has",
			page: prEN(func(p *PullRequestPage) {
				p.Merge = MergeAvailability{Blockers: []MergeBlocker{{}, {Code: "merge_conflict"}, {Detail: "detail with no code"}, {}}}
			}),
			absent: []MessageCode{MsgMergeUnexplained}, markup: []string{"detail with no code"},
			merge: mergeRefused, extra: countIs(`class="blocker"`, 2)},
		screen{name: "a cleanup failure does not block",
			page:   with(uncleanPullRequestPage(fullChrome(LangEN)), func(p *PullRequestPage) { p.Merge = MergeAvailability{Eligible: true} }),
			want:   []MessageCode{MsgEvidenceAdvisory},
			absent: []MessageCode{MsgMergeUnexplained}, merge: mergeOffered},
	)
	// A blocker with nothing in it still refuses, without an empty bullet
	// under a heading that promises a reason.
	for _, eligible := range []bool{true, false} {
		screens = append(screens, screen{name: "empty blockers refuse without an invented explanation",
			page: prEN(func(p *PullRequestPage) {
				p.Merge = MergeAvailability{Eligible: eligible, Blockers: []MergeBlocker{{}, {}}}
			}),
			want: []MessageCode{MsgMergeUnexplained}, noMarkup: []string{`class="blocker"`}, merge: mergeRefused})
	}
	checkScreens(t, screens...)
}

// TestCheckEvidenceNeverOverclaims covers the failure mode specific to these
// screens: a state the reader misreads as a pass, or as a result about the
// code on screen, is worse than no screen at all.
func TestCheckEvidenceNeverOverclaims(t *testing.T) {
	testedClean := func(p *PullRequestPage) {
		p.Checks.Status, p.Checks.WorktreeState, p.Checks.TestedCommit = CheckPassed, WorktreeClean, true
	}
	unreadable := &EvidenceReadFailure{Code: ReadFailureCheckEvidence, Message: MsgCheckRecordUnreadable}
	var screens []screen
	for _, tree := range []string{WorktreeDirty, WorktreeUnknown} {
		screens = append(screens,
			screen{name: tree + " worktree is not a tested commit",
				page: prEN(func(p *PullRequestPage) {
					p.Checks.Status, p.Checks.WorktreeState, p.Checks.TestedCommit = CheckPassed, tree, false
				}),
				want: []MessageCode{MsgCheckNotTested}, absent: []MessageCode{MsgCheckTestedCommit}},
			screen{name: tree + " check labels its revision as the target, not tested", lang: LangKO,
				page: with(pullRequestPage(fullChrome(LangKO), prFixtureFailing), func(p *PullRequestPage) {
					p.Checks.Status, p.Checks.WorktreeState, p.Checks.TestedCommit = CheckPassed, tree, false
					p.Review = ReviewEvidence{}
				}),
				markup: []string{"대상 커밋", "7f2c1a0"}, noMarkup: []string{"테스트한 커밋"}})
	}
	// A set TestedCommit flag does not override the recorded working state.
	for _, tree := range []string{WorktreeDirty, WorktreeUnknown, ""} {
		screens = append(screens, screen{name: "flagged " + tree + " worktree is still unproven",
			page: prEN(func(p *PullRequestPage) {
				p.Checks.Status, p.Checks.TestedCommit, p.Checks.WorktreeState = CheckPassed, true, tree
			}),
			want: []MessageCode{MsgCheckNotTested}, absent: []MessageCode{MsgCheckTestedCommit}})
	}
	// Staleness recorded in the status, the flag, or both.
	for _, c := range []struct {
		name   string
		status string
		flag   bool
	}{{"status only", CheckStale, false}, {"flag only", CheckPassed, true}, {"both", CheckStale, true}} {
		screens = append(screens, screen{name: "stale by " + c.name + " never looks current",
			page: prEN(func(p *PullRequestPage) {
				p.Checks.Status, p.Checks.Stale, p.Checks.TestedCommit, p.Checks.WorktreeState = c.status, c.flag, true, WorktreeClean
			}),
			want:   []MessageCode{MsgCheckStateStale, MsgCheckStaleDetail},
			absent: []MessageCode{MsgCheckTestedCommit, MsgCheckStatePassed}})
	}
	for _, failure := range []EvidenceReadFailure{
		{Code: ReadFailureCheckConfiguration, Message: MsgCheckConfigurationUnreadable},
		{Code: ReadFailureCheckEvidence, Message: MsgCheckRecordUnreadable},
	} {
		for _, lang := range Langs() {
			screens = append(screens, screen{name: string(lang) + " " + failure.Code + " read failure invents no absent evidence",
				page: with(pullRequestPage(fullChrome(lang), prFixtureFailing), func(p *PullRequestPage) {
					p.Checks = CheckEvidence{Status: CheckAbsent, Advisory: true, ReadFailure: &failure}
				}),
				markup:   []string{bilingual(failure.Message, lang)},
				noMarkup: []string{wantText(LangEN, MsgCheckNotConfigured), wantText(LangKO, MsgCheckNotConfigured), bilingual(MsgCheckStateUnavailable, lang)}})
		}
	}
	for _, lang := range Langs() {
		screens = append(screens, screen{name: string(lang) + " review read failure is not a reviewer failure or a gate",
			page: with(pullRequestPage(fullChrome(lang), prFixtureFailing), func(p *PullRequestPage) {
				p.Merge = MergeAvailability{Eligible: true}
				p.Review = ReviewEvidence{ReadFailure: &EvidenceReadFailure{Code: ReadFailureReviewEvidence, Message: MsgReviewRecordUnreadable}}
			}),
			markup: []string{bilingual(MsgReviewRecordUnreadable, lang)}, noMarkup: []string{bilingual(MsgReviewStateUnavailable, lang)},
			merge: mergeOffered})
	}
	screens = append(screens,
		screen{name: "a clean tree on this revision may claim a tested commit", page: prEN(testedClean),
			want: []MessageCode{MsgCheckTestedCommit}},
		screen{name: "unknown statuses never pass",
			page:   pullRequestPage(fullChrome(LangEN), prFixtureUnknown),
			absent: []MessageCode{MsgCheckStatePassed, MsgReviewStateApproved},
			extra: func(t *testing.T, out string) {
				if n := strings.Count(out, wantText(LangEN, MsgEvidenceUnknownState)); n < 2 {
					t.Errorf("unrecognised statuses are stated %d times, want both the check and the review", n)
				}
			}},
		screen{name: "a pending review labels its revision as requested, not tested", lang: LangKO,
			page: with(pullRequestPage(fullChrome(LangKO), prFixtureFailing), func(p *PullRequestPage) {
				p.Checks = CheckEvidence{Status: CheckAbsent, Configured: true, Advisory: true}
				p.Review = ReviewEvidence{Status: ReviewPending, SourceOID: "7f2c1a0bb", ShortSourceOID: "7f2c1a0",
					BoundToCurrentRevision: true, Provenance: ReviewFromRequest, SubmittedAt: testNow}
			}),
			markup: []string{`>요청한 커밋</span></dt><dd class="mono">7f2c1a0</dd>`}, noMarkup: []string{"테스트한 커밋", ">대상 커밋</span></dt>"}},
		// QA-043: only a given review names the revision it tested. A request
		// and a skip keep their revision under a label that says what it is.
		screen{name: "a pending review in English is requested for its revision",
			page: with(pullRequestPage(fullChrome(LangEN), prFixtureFailing), func(p *PullRequestPage) {
				p.Checks = CheckEvidence{Status: CheckAbsent, Configured: true, Advisory: true}
				p.Review = ReviewEvidence{Status: ReviewPending, SourceOID: "7f2c1a0bb", ShortSourceOID: "7f2c1a0",
					BoundToCurrentRevision: true, Provenance: ReviewFromRequest, SubmittedAt: testNow}
			}),
			markup:   []string{`>Requested for revision</span></dt><dd class="mono">7f2c1a0</dd>`},
			noMarkup: []string{">Tested revision</span></dt>"}},
		screen{name: "a skipped review names the revision it skipped",
			page: with(pullRequestPage(fullChrome(LangEN), prFixtureFailing), func(p *PullRequestPage) {
				p.Checks = CheckEvidence{Status: CheckAbsent, Configured: true, Advisory: true}
				p.Review = ReviewEvidence{Status: ReviewSkipped, SourceOID: "7f2c1a0bb", ShortSourceOID: "7f2c1a0",
					BoundToCurrentRevision: true, Provenance: ReviewFromSkip, SubmittedAt: testNow}
			}),
			markup:   []string{`>Skipped for revision</span></dt><dd class="mono">7f2c1a0</dd>`},
			noMarkup: []string{">Tested revision</span></dt>"}},
		screen{name: "a given review names its tested revision",
			page: with(pullRequestPage(fullChrome(LangEN), prFixtureFailing), func(p *PullRequestPage) {
				p.Checks = CheckEvidence{Status: CheckAbsent, Configured: true, Advisory: true}
				p.Review = ReviewEvidence{Status: ReviewApproved, SourceOID: "7f2c1a0bb", ShortSourceOID: "7f2c1a0",
					BoundToCurrentRevision: true, Provenance: ReviewFromExternalTool, ReviewerLabel: "reviewer-one", SubmittedAt: testNow}
			}),
			markup: []string{`>Tested revision</span></dt><dd class="mono">7f2c1a0</dd>`},
			absent: []MessageCode{MsgReviewRevisionRequested, MsgReviewRevisionSkipped}},
		screen{name: "a stale result names its revision and never looks current",
			page: prEN(func(p *PullRequestPage) {
				p.Checks.Status, p.Checks.Stale, p.Checks.TestedCommit, p.Checks.RevisionShortOID = CheckStale, true, true, "5d0aa13"
			}),
			want:   []MessageCode{MsgCheckStateStale, MsgCheckStaleDetail},
			absent: []MessageCode{MsgCheckStatePassed, MsgCheckTestedCommit}, markup: []string{"5d0aa13"}},
		screen{name: "a stale pass is stale",
			page: prEN(func(p *PullRequestPage) {
				p.Checks.Status, p.Checks.Stale, p.Checks.TestedCommit, p.Checks.WorktreeState = CheckPassed, true, true, WorktreeClean
			}),
			want: []MessageCode{MsgCheckStateStale}, absent: []MessageCode{MsgCheckStatePassed, MsgCheckTestedCommit}},
		screen{name: "an unconfigured repository is not a pass",
			page: prEN(func(p *PullRequestPage) {
				p.Checks = CheckEvidence{Status: CheckAbsent, Configured: false, Advisory: true}
			}),
			want:   []MessageCode{MsgCheckStateAbsent, MsgCheckNotConfigured},
			absent: []MessageCode{MsgCheckStatePassed}},
		screen{name: "unreadable pull request records are not an empty list",
			page: pullRequestsPage(fullChrome(LangEN), true),
			want: []MessageCode{MsgPRListUnavailable}, absent: []MessageCode{MsgPRListEmpty}},
		screen{name: "unreadable task records are not an empty list",
			page: with(tasksPage(fullChrome(LangEN), false), func(p *TasksPage) {
				p.Unavailable, p.UnavailableReason, p.Tasks = true, MsgPRFailed, nil
			}),
			want: []MessageCode{MsgTasksUnavail}},
		screen{name: "a failed comparison is not shown as no changes",
			page: prEN(func(p *PullRequestPage) { p.Changes, p.ChangesUnavailable = nil, true }),
			want: []MessageCode{MsgPRChangesUnavail}, absent: []MessageCode{MsgPRChangesNone}},
		// An expired log does not erase the recorded result.
		screen{name: "an expired log keeps the durable result",
			page: prEN(func(p *PullRequestPage) { p.Checks.LogStatus, p.Checks.LogExpiresAt = LogExpired, time.Time{} }),
			want: []MessageCode{MsgCheckLogExpired, MsgCheckStateFailed}},
		// The backend registers an attempt before executing it. That record
		// has no outcome and nothing here observes whether it still runs.
		screen{name: "a registered attempt is not a result",
			page: prEN(func(p *PullRequestPage) {
				p.Checks = CheckEvidence{Status: CheckPending, Configured: true, Advisory: true,
					RevisionShortOID: "7f2c1a0", RegisteredAt: testNow.Add(-2 * time.Minute), WorktreeState: WorktreeClean}
			}),
			want:   []MessageCode{MsgCheckStatePending, MsgCheckPendingDetail},
			absent: []MessageCode{MsgCheckStatePassed, MsgCheckStateFailed, MsgCheckTestedCommit}, merge: mergeOffered},
		screen{name: "an unreadable check record is its own state",
			page: prEN(func(p *PullRequestPage) {
				p.Checks = CheckEvidence{Status: CheckAbsent, Configured: true, Advisory: true, ReadFailure: unreadable}
			}),
			want: []MessageCode{MsgCheckRecordUnreadable}, absent: []MessageCode{MsgCheckStatePassed}},
		screen{name: "an unreadable record does not borrow the outcome stored beside it",
			page: prEN(func(p *PullRequestPage) {
				p.Checks = CheckEvidence{Status: CheckPassed, Configured: true, Advisory: true, ReadFailure: unreadable,
					TestedCommit: true, WorktreeState: WorktreeClean}
			}),
			want: []MessageCode{MsgCheckRecordUnreadable}, absent: []MessageCode{MsgCheckStatePassed, MsgCheckTestedCommit}},
		screen{name: "an unavailable environment is not an unreadable record",
			page: prEN(func(p *PullRequestPage) { p.Checks.Status, p.Checks.ReadFailure = CheckUnavailable, nil }),
			want: []MessageCode{MsgCheckStateUnavailable}, absent: []MessageCode{MsgCheckRecordUnreadable}},
		screen{name: "quoted external review evidence is not translated", lang: LangKO,
			page: with(pullRequestPage(fullChrome(LangKO), prFixtureFailing), func(p *PullRequestPage) {
				p.Review = ReviewEvidence{Status: ReviewUnavailable, BoundToCurrentRevision: true,
					Provenance: ReviewFromExternalTool, Detail: "provider returned 503", SubmittedAt: testNow}
			}),
			markup: []string{"provider returned 503"}},
		// The chip is what a reader takes at a glance, so an approval of a
		// commit that is no longer the tip must not render as one.
		screen{name: "an approval for another revision is not an approval chip", page: prEN(unchanged),
			want:   []MessageCode{MsgReviewStateNone, MsgReviewOtherRevision},
			absent: []MessageCode{MsgReviewStateApproved}, markup: []string{"codex", "5d0aa13"}},
		screen{name: "an approval for this revision is an approval",
			page: prEN(func(p *PullRequestPage) { p.Review.BoundToCurrentRevision = true }),
			want: []MessageCode{MsgReviewStateApproved}},
		screen{name: "the restore screen keeps its future-tense wording",
			page: restorePage(fullChrome(LangEN), true), want: []MessageCode{MsgRestoreStatusModified}},
	)
	// A pull request already contains its change, so the list does not reuse
	// the restore screen's "will be changed" wording.
	for _, lang := range Langs() {
		screens = append(screens, screen{name: string(lang) + " pull request changes are not future plans", lang: lang,
			page:   pullRequestPage(fullChrome(lang), prFixtureFailing),
			want:   []MessageCode{MsgPRChangeModified},
			absent: []MessageCode{MsgRestoreStatusAdded, MsgRestoreStatusModified, MsgRestoreStatusDeleted}})
	}
	checkScreens(t, screens...)
}

// bilingual is the element text for code in lang, carrying both languages for
// in-place switching.
func bilingual(code MessageCode, lang Lang) string {
	return `data-en="` + wantText(LangEN, code) + `" data-ko="` + wantText(LangKO, code) + `">` + wantText(lang, code) + `</span>`
}

func TestTaskScreensStateWhatTheRecordHolds(t *testing.T) {
	detail := tasksPage(fullChrome(LangEN), true)
	pending := AttemptRecord{
		ID: "att_pending01", ShortID: "att_pending01", Status: CheckPending,
		RevisionShortOID: "7f2c1a0", WorktreeState: WorktreeClean, StartedAt: testNow.Add(-1 * time.Minute),
	}
	if pending.TestedCommit() {
		t.Error("a pending attempt reports a tested commit from its clean tree alone")
	}
	firstCheck := func(done bool) TasksPage {
		return with(tasksPage(fullChrome(LangEN), true), func(p *TasksPage) {
			p.Detail.Task.Status, p.Detail.Task.CyclesUsed, p.Detail.Task.InitialCheckDone = TaskActive, 0, done
		})
	}
	unordered := uncleanTasksPage(fullChrome(LangEN))
	for i := range unordered.Detail.Attempts {
		unordered.Detail.Attempts[i].Sequence = 0
	}
	checkScreens(t,
		screen{name: "a task with no run shows the absence", page: tasksPage(fullChrome(LangEN), false),
			want: []MessageCode{MsgCheckStateAbsent}},
		// An exhausted budget stops automatic correction; it does not restrict
		// Git, remove work or block a merge. A round is reserved before it runs
		// and a manual rerun does not count, or the number reads as a failure
		// counter.
		screen{name: "an exhausted budget is not a lockout and rounds are explained", page: detail,
			want: []MessageCode{MsgTaskExhausted, MsgTaskBudgetHelp, MsgTaskRoundsHelp, MsgTaskManualRerun}},
		screen{name: "shortened output is marked", page: detail, want: []MessageCode{MsgCheckOutputCut}},
		screen{name: "zero rounds before the first check is unmeasured", page: firstCheck(false),
			want: []MessageCode{MsgTaskInitialNone}},
		screen{name: "a measured task does not claim it never was", page: firstCheck(true),
			absent: []MessageCode{MsgTaskInitialNone}},
		// A zero duration would read as an instant run.
		screen{name: "a pending attempt states no duration or outcome",
			page: with(tasksPage(fullChrome(LangEN), true), func(p *TasksPage) { p.Detail.Attempts = []AttemptRecord{pending} }),
			want: []MessageCode{MsgCheckStatePending}, absent: []MessageCode{MsgAttemptDuration}},
		// The sequence is registration order across the repository, not a
		// count of this task's runs: the fixture has 47 and 46 on a task with
		// three cycles used.
		screen{name: "attempt sequence is repository order", page: uncleanTasksPage(fullChrome(LangEN)),
			want:   []MessageCode{MsgAttemptOrder, MsgAttemptOrderGap},
			markup: []string{`<span class="mono">47</span>`, `<span class="mono">46</span>`}},
		screen{name: "an unstated order renders no label", page: unordered, absent: []MessageCode{MsgAttemptOrder}},
	)
}

// TestACleanupFailureNeverRendersAPass covers runs whose checks passed while
// OwnGit could not confirm the processes they started had stopped.
func TestACleanupFailureNeverRendersAPass(t *testing.T) {
	unclean := uncleanPullRequestPage(fullChrome(LangEN))
	screens := []screen{
		screen{name: "check evidence names the cleanup failure", page: unclean,
			want:   []MessageCode{MsgCheckStateUnclean, MsgCheckCleanupFailed, MsgCheckNotTested},
			absent: []MessageCode{MsgCheckStatePassed, MsgCheckTestedCommit}},
		// The rule applies where the failure was recorded: from a result row
		// and from the aggregate alone, while a line that did clean up keeps
		// its passing status.
		screen{name: "attempts name the cleanup failure", page: uncleanTasksPage(fullChrome(LangEN)),
			want: []MessageCode{MsgCheckStatePassed}, absent: []MessageCode{MsgCheckTestedCommit},
			extra: func(t *testing.T, out string) {
				for i, head := range attemptHeads(t, out) {
					if strings.Contains(head, wantText(LangEN, MsgCheckStatePassed)) {
						t.Errorf("attempt %d with a cleanup failure renders as a plain pass", i)
					}
					if !strings.Contains(head, wantText(LangEN, MsgCheckStateUnclean)) {
						t.Errorf("attempt %d does not name the cleanup failure in its status", i)
					}
				}
				if n := strings.Count(out, wantText(LangEN, MsgCheckCleanupFailed)); n < 2 {
					t.Errorf("%d attempts warned about cleanup, want both the row-detailed and aggregate-only ones", n)
				}
			}},
		// Withholding "passed" must not hide what the command returned. The
		// row is scoped because another row also exited zero.
		screen{name: "the actual exit code survives", page: uncleanTasksPage(fullChrome(LangEN)),
			extra: func(t *testing.T, out string) {
				row := resultNamed(t, out, "test")
				for _, want := range []string{wantText(LangEN, MsgAttemptExitCode), `<span class="mono">0</span>`,
					"ok  owngit/internal/retry", wantText(LangEN, MsgCheckCleanupRow)} {
					if !strings.Contains(row, want) {
						t.Errorf("the cleanup-failed row lost %q", want)
					}
				}
			}},
		screen{name: "stale evidence keeps its stale status and the cleanup warning",
			page: with(unclean, func(p *PullRequestPage) { p.Checks.Stale = true }),
			want: []MessageCode{MsgCheckStateStale, MsgCheckStaleDetail, MsgCheckCleanupFailed}},
		screen{name: "an unreadable record keeps its own wording",
			page: with(unclean, func(p *PullRequestPage) {
				p.Checks.ReadFailure = &EvidenceReadFailure{Code: ReadFailureCheckEvidence, Message: MsgCheckRecordUnreadable}
			}),
			want: []MessageCode{MsgCheckRecordUnreadable}, absent: []MessageCode{MsgCheckStatePassed}},
		screen{name: "a run that did clean up still passes",
			page:   with(unclean, func(p *PullRequestPage) { p.Checks.CleanupFailed = false }),
			want:   []MessageCode{MsgCheckStatePassed, MsgCheckTestedCommit},
			absent: []MessageCode{MsgCheckCleanupFailed}},
	}
	for _, lang := range Langs() {
		screens = append(screens,
			screen{name: string(lang) + " unclean status replaces the pass", lang: lang,
				page: uncleanPullRequestPage(fullChrome(lang)),
				want: []MessageCode{MsgCheckStateUnclean}, absent: []MessageCode{MsgCheckStatePassed}},
			screen{name: string(lang) + " the cleanup reason is labelled", lang: lang,
				page: uncleanTasksPage(fullChrome(lang)), want: []MessageCode{MsgCheckCleanupRow}})
	}
	checkScreens(t, screens...)
}

func TestCredentialScreenStates(t *testing.T) {
	later := helperPage(fullChrome(LangEN), false)
	checkScreens(t,
		// A later GET shows the credential without its secret, which is what
		// makes the one-time warning true.
		screen{name: "returning to the screen shows no token", page: later,
			absent: []MessageCode{MsgHelperTokenTitle}, markup: []string{"dev-mini"}, noMarkup: []string{helperTestToken}},
		// A revoked credential stays listed as the record of its access, and
		// "never used" is stated because that is how an unused one is noticed.
		screen{name: "revoked credentials stay visible as a record", page: later,
			want: []MessageCode{MsgHelperRevoked, MsgHelperNeverUsed}, markup: []string{"old-ci"},
			extra: func(t *testing.T, out string) {
				revoked := out[strings.Index(out, "old-ci"):]
				if end := strings.Index(revoked, `class="cred`); end > 0 {
					revoked = revoked[:end]
				}
				if strings.Contains(revoked, wantText(LangEN, MsgHelperRevoke)+"</button>") {
					t.Error("a revoked credential still offers a revoke control")
				}
			}},
		// The constants are the contract a handler compares against.
		screen{name: "forms submit the documented action values", page: later,
			markup: []string{
				`name="action" value="` + ActionIssueHelperCredential + `"`,
				`name="action" value="` + ActionRevokeHelperCredential + `"`,
			},
			extra: func(t *testing.T, out string) {
				for _, m := range regexp.MustCompile(`name="action" value="([^"]*)"`).FindAllStringSubmatch(out, -1) {
					if m[1] != ActionIssueHelperCredential && m[1] != ActionRevokeHelperCredential {
						t.Errorf("a form submits the undocumented action %q", m[1])
					}
				}
			}},
		screen{name: "only an active credential with an identifier can be revoked",
			page: with(later, func(p *HelperCredentialsPage) {
				p.Credentials = []HelperCredentialRow{
					{ID: "hc1", Label: "dev-mini", CreatedAt: testNow},
					{ID: "hc0", Label: "old-ci", CreatedAt: testNow, RevokedAt: testNow, Revoked: true},
					{Label: "unidentified", CreatedAt: testNow},
				}
			}),
			markup:   []string{`name="credential_id" value="hc1"`},
			noMarkup: []string{`name="credential_id" value="hc0"`, `name="credential_id" value=""`}},
		// Every row submits the same action, so only the credential id says
		// which row failed. Notice elements are counted, not text, because
		// each string is carried twice for language switching.
		screen{name: "a refused revocation is announced on its own row",
			page: with(helperPage(fullChrome(LangEN), false), func(p *HelperCredentialsPage) {
				p.PendingAction, p.PendingCredentialID = ActionRevokeHelperCredential, "hc2"
				p.Chrome.Notices = []Notice{Error("admin_password", MsgAdminFailed)}
			}),
			markup: []string{`id="hc2-admin_password-note"`},
			extra: func(t *testing.T, out string) {
				countIs(`class="fieldnote fieldnote--error"`, 1)(t, out)
				row := out[strings.Index(out, `value="hc2"`):]
				if end := strings.Index(row, "</form>"); end >= 0 {
					row = row[:end]
				}
				if !strings.Contains(row, `aria-invalid="true"`) {
					t.Error("the failing row's password field is not marked invalid")
				}
			}},
	)
}

func TestCreateAndSummaryScreenStates(t *testing.T) {
	// A result about an earlier revision, or a record that could not be read,
	// is placed before the disclosure so it is read without opening anything.
	relevance := func(code MessageCode) func(*testing.T, string) {
		return func(t *testing.T, out string) {
			at := strings.Index(out, wantText(LangEN, code))
			if at < 0 {
				t.Fatalf("the screen does not say %q", wantText(LangEN, code))
			}
			if disclosure := strings.Index(out, "<details"); disclosure >= 0 && at > disclosure {
				t.Error("the relevance label was folded behind the disclosure")
			}
		}
	}
	checkScreens(t,
		screen{name: "no create form before the branch tips are read", page: newPullRequestPage(fullChrome(LangEN), false),
			absent: []MessageCode{MsgPRNewSubmit}, noMarkup: []string{`name="source_oid"`}},
		screen{name: "non-commit branch tips offer no create form",
			page: with(newPullRequestPage(fullChrome(LangEN), true), func(p *NewPullRequestPage) {
				p.Source = RevisionState{Branch: "fix/cursor", Status: RevisionMissing}
			}),
			want: []MessageCode{MsgPRNewNoCommit, MsgPRBranchGone}, absent: []MessageCode{MsgPRNewSubmit}},
		screen{name: "the Korean list shows source to target direction", lang: LangKO,
			page: with(pullRequestsPage(fullChrome(LangKO), false), func(p *PullRequestsPage) {
				p.Items = p.Items[:1]
				p.Items[0].Source.Branch, p.Items[0].Target.Branch = "feature", "main"
			}),
			extra: inOrder(">feature</span>", `data-en="into" data-ko="→">→</span>`, ">main</span>")},
		// Korean places the particle after the target, so a phrase between the
		// branch names read as nonsense; the arrow reads correctly in both.
		screen{name: "the Korean direction line reads correctly", lang: LangKO,
			page: with(pullRequestPage(fullChrome(LangKO), prFixtureFailing), func(p *PullRequestPage) {
				p.Source.Branch, p.Target.Branch = "feature", "main"
			}),
			noMarkup: []string{"에 합칩니다"},
			extra:    inOrder(">feature</span>", `data-en="into" data-ko="→">→</span>`, ">main</span>")},
		// A check run and a submitted opinion answer different questions, so a
		// missing review stays visible beside passing checks.
		screen{name: "every evidence kind keeps its own summary row",
			page: prEN(func(p *PullRequestPage) { p.Review = ReviewEvidence{} }),
			want: []MessageCode{MsgCheckTitle, MsgReviewTitle, MsgReviewStateNone}},
		screen{name: "an empty row does not state the absence twice",
			page: prEN(func(p *PullRequestPage) {
				p.Checks = CheckEvidence{Configured: true, Advisory: true}
				p.Review = ReviewEvidence{}
			}),
			want: []MessageCode{MsgCheckStateAbsent, MsgReviewStateNone}, absent: []MessageCode{MsgRelevanceNone}},
		screen{name: "current revision relevance", page: prEN(unchanged), extra: relevance(MsgRelevanceCurrent)},
		screen{name: "stale check relevance",
			page:  prEN(func(p *PullRequestPage) { p.Checks.Status, p.Checks.Stale = CheckPassed, true }),
			extra: relevance(MsgRelevancePrior)},
		screen{name: "unreadable check relevance",
			page: prEN(func(p *PullRequestPage) {
				p.Checks.ReadFailure = &EvidenceReadFailure{Code: "io_error", Message: MsgCheckRecordUnreadable}
			}),
			extra: relevance(MsgRelevanceUnknown)},
		screen{name: "unplaceable check relevance",
			page: prEN(func(p *PullRequestPage) {
				p.Checks = CheckEvidence{Configured: true, Advisory: true,
					ReadFailure: &EvidenceReadFailure{Code: ReadFailureCheckEvidence, Message: MsgCheckRecordUnreadable}}
			}),
			extra: relevance(MsgRelevanceUnknown)},
		screen{name: "review of another revision relevance",
			page: prEN(func(p *PullRequestPage) {
				p.Review = ReviewEvidence{Status: ReviewApproved, ShortSourceOID: "5d0aa13", SubmittedAt: testNow}
			}),
			extra: relevance(MsgRelevancePrior)},
		screen{name: "external review of an earlier commit relevance",
			page: prEN(func(p *PullRequestPage) {
				p.Review = ReviewEvidence{Status: ReviewApproved, SourceOID: "5d0aa1399", ShortSourceOID: "5d0aa13",
					Provenance: ReviewFromExternalTool, SubmittedAt: testNow.AddDate(0, 0, -1)}
			}),
			extra: relevance(MsgRelevancePrior)},
	)
}
