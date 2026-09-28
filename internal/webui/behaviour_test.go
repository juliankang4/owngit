package webui

import (
	"regexp"
	"strings"
	"testing"
)

// The interface must work as plain server-rendered HTML. The script improves
// what a reload would otherwise cost; it is never the only way to do something.

func TestEveryScreenWorksWithoutScripting(t *testing.T) {
	r := newRenderer(t)
	// A control that only responds to a script would be dead without it.
	// inlineEventHandler catches any on* attribute in any case, quoted or
	// not, and ignores escaped code in page text.

	for _, lang := range Langs() {
		for name, page := range allPages(lang) {
			out := render(t, r, page)

			if loc := inlineEventHandler(out); loc != "" {
				t.Errorf("%s/%s: control depends on an inline handler (%s)", lang, name, loc)
			}
			if strings.Contains(out, `href="#"`) || strings.Contains(out, `href="javascript:`) {
				t.Errorf("%s/%s: a link goes nowhere without scripting", lang, name)
			}
			// Every form must name a real endpoint.
			for _, form := range strings.Split(out, "<form")[1:] {
				if end := strings.Index(form, ">"); end >= 0 {
					form = form[:end]
				}
				if !strings.Contains(form, "action=") {
					t.Errorf("%s/%s: a form has no action: <form%s>", lang, name, form)
				}
			}
		}
	}
}

func TestLanguageSwitchIsAPlainLinkAndAScriptHook(t *testing.T) {
	r := newRenderer(t)
	c := fullChrome(LangEN)
	c.CurrentURL = "/settings"
	out := render(t, r, SettingsPage{Chrome: c, SubmitURL: "/settings"})

	// Without scripting the link navigates and the backend answers in the
	// other language. With scripting the same element switches in place.
	if !strings.Contains(out, `class="lang__btn" href="/settings?lang=ko"`) {
		t.Error("the language control is not a working link")
	}
	if !strings.Contains(out, `data-lang-set="ko"`) {
		t.Error("the language control has no in-place switch hook")
	}
	if !strings.Contains(out, `data-lang-cookie="owngit_lang"`) {
		t.Error("the preference cookie name is not published to the script")
	}
	if !strings.Contains(scriptSource(t), `|| 'owngit_lang'`) {
		t.Error("the script fallback does not use the OwnGit language cookie")
	}
}

func TestRefPickerSubmitsWithoutScripting(t *testing.T) {
	r := newRenderer(t)
	out := render(t, r, repoPage(fullChrome(LangEN), RepoTabCode))

	idx := strings.Index(out, `<select id="ref-select"`)
	if idx < 0 {
		t.Fatal("no ref picker rendered")
	}
	form := out[strings.LastIndex(out[:idx], "<form"):]
	if end := strings.Index(form, "</form>"); end >= 0 {
		form = form[:end]
	}
	if !strings.Contains(form, `method="get"`) {
		t.Error("the ref picker is not a GET form")
	}
	if !strings.Contains(form, `type="submit"`) {
		t.Error("the ref picker has no submit control for a browser without scripting")
	}
	if !strings.Contains(form, "data-submit-on-change") {
		t.Error("the ref picker has no progressive enhancement hook")
	}
	// Changing branch must keep the current file path.
	if !strings.Contains(form, `name="path" value="internal/pagination/cursor.go"`) {
		t.Error("changing branch would lose the current path")
	}
}

func TestAppearanceDefaultsToSystem(t *testing.T) {
	r := newRenderer(t)
	out := render(t, r, OverviewPage{Chrome: fullChrome(LangEN), Activity: sampleGraph()})
	if !strings.Contains(out, `class="theme-system" data-appearance="system"`) {
		t.Error("the served document does not start in System appearance")
	}
	for _, choice := range []string{"light", "dark", "system"} {
		if !strings.Contains(out, `data-appearance-set="`+choice+`"`) {
			t.Errorf("the %s appearance option is missing", choice)
		}
	}
}

// The script is the only place that touches browser storage and the address
// bar, so its promises are checked against its actual source.

// The Settings group save is the other block of the script that makes
// requests. It sends only a form without a password field, only to this
// site, as a POST with this site's cookies, and then makes one GET of the
// address the server returned, again only on this site. Neither follows a
// redirect. It stores and logs nothing, and the only value it reads is the
// anti-forgery token of the page it fetched, which it copies into this
// page's forms.
func TestSettingsGroupSaveSendsOnlyWhatItMay(t *testing.T) {
	block := groupSaveSource(t)
	counts := map[string]int{
		"fetch(": 2, "method: 'POST'": 1, "method: 'GET'": 1, "credentials: 'same-origin'": 2, "redirect: 'manual'": 2,
		"new FormData(form)": 1, "target.origin === window.location.origin": 1, `'input[name="csrf"]'`: 2,
		"control.type === 'password'": 1, ".value": 2,
	}
	for part, want := range counts {
		if got := strings.Count(block, part); got != want {
			t.Errorf("the group save has %q %d times, want %d", part, got, want)
		}
	}
	// A form with a password field is refused before its data is built, and
	// both requests go only to an address on this site.
	send := section(t, block, "function send(", "function read(")
	if !strings.Contains(send, "if (holdsPassword(form) || !target) { return Promise.reject(") ||
		strings.Index(send, "holdsPassword(form)") > strings.Index(send, "new FormData(form)") {
		t.Error("the group save builds form data before refusing a form with a password field")
	}
	for _, sender := range []string{send, section(t, block, "function read(", "function copyToken(")} {
		if !strings.Contains(sender, "onThisSite(") || !strings.Contains(sender, "fetch(target.href") {
			t.Errorf("a request of the group save is not limited to this site:\n%s", sender)
		}
	}
	if strings.Contains(block, "\"password\"") {
		t.Error("the group save names a password field")
	}
	// The one value it reads is the token of the fetched page.
	copyToken := section(t, block, "function copyToken(", "return {")
	if strings.Count(copyToken, ".value") != 2 || !strings.Contains(copyToken, "field.value = token.value") {
		t.Error("the group save reads a value other than the fetched token")
	}
	// Beyond what no part of the script may use, the block uses no browser
	// storage at all, no cookie, log, address fragment or other site.
	if found := forbiddenSinksIn(block); len(found) > 0 {
		t.Errorf("the group save touches %q", found)
	}
	for _, leak := range []string{
		"Storage", "console.", "location.hash", "document.cookie", "http:", "https:", "://",
		"Observer", "import", "require(", "<script", "innerHTML", "eval(",
	} {
		if strings.Contains(strings.ReplaceAll(block, "'X-OwnGit-Group'", ""), leak) {
			t.Errorf("the group save touches %q", leak)
		}
	}
	// The rest of the script leaves sending to it: a form it does not send
	// is submitted by the browser.
	rest := scriptOutsideRequests(t)
	if !strings.Contains(rest, "!groupSave || !groupSave.eligible(form)) { return; }") {
		t.Error("the submit handler takes over a form the group save does not send")
	}
}

// Leaving Settings with unsaved changes makes no request of its own: it
// saves through saveGroup, asking it to stay on the page, and otherwise
// lets the browser send a group's form as a page. It never reads a
// password field's value, and asks the browser's own question on leaving
// only while a group holds a change.
func TestLeavingSettingsSendsNothingItself(t *testing.T) {
	block := section(t, scriptSource(t), "var leaveDialog = settingsPanel", "  if (settingsPanel) {\n    var editGroup")
	for _, banned := range []string{"fetch(", "FormData", "URLSearchParams", "groupSave.send", "groupSave.read", "innerHTML", "Storage", "document.cookie", "console."} {
		if strings.Contains(block, banned) {
			t.Errorf("the leave block uses %q", banned)
		}
	}
	if found := forbiddenSinksIn(block); len(found) > 0 {
		t.Errorf("the leave block uses %q", found)
	}
	// One caller of saveGroup, which never lets it send the page.
	if strings.Count(block, "saveGroup(") != 1 || !strings.Contains(block, "saveGroup(groupNamed(name), true)") {
		t.Error("the leave block calls saveGroup other than to stay on the page")
	}
	// The only values it touches: the address being left for, clearing
	// the dialog's password field, and the value of a choice.
	for part, want := range map[string]int{".value": 4, "to.value = leave.target": 1, "to.value = ''": 1,
		"querySelector('[data-leave-password]').value = ''": 1, "item.value === value": 1} {
		if got := strings.Count(block, part); got != want {
			t.Errorf("the leave block has %q %d times, want %d", part, got, want)
		}
	}
	// A page is sent only by the browser's own form submission.
	if strings.Count(block, "HTMLFormElement.prototype.submit.call(") != 2 || strings.Contains(block, ".requestSubmit(") {
		t.Error("the leave block sends a form some other way")
	}
	// The browser's question is registered only while a group holds a
	// change.
	sync := section(t, block, "function syncLeave(", "function isGuard(")
	if strings.Count(block, "addEventListener('beforeunload'") != 1 || !strings.Contains(sync, "if (dirty) {\n        window.addEventListener('beforeunload', askBeforeUnload);") ||
		!strings.Contains(sync, "window.removeEventListener('beforeunload', askBeforeUnload);") {
		t.Error("the browser's question on leaving is not tied to an unsaved change")
	}
	// History gets one entry of its own, added only while something is
	// unsaved, and taken back again.
	if strings.Count(block, "pushState(") != 1 || !strings.Contains(block, "window.history.back();") {
		t.Error("the leave block adds history entries other than its one guard")
	}
}

// No part of the script, the two blocks that make requests included, keeps
// data in the browser beyond the display preferences or sends it by any way
// other than their fetch calls.
func TestScriptUsesNoOtherStorageOrTransport(t *testing.T) {
	if found := forbiddenSinksIn(scriptSource(t)); len(found) > 0 {
		t.Errorf("the script uses %q", found)
	}
	if found := forbiddenSinksIn(scriptOutsideRequests(t)); len(found) > 0 {
		t.Errorf("the script outside the request blocks uses %q", found)
	}
}

func TestScriptClearsTheSetupFragmentAndNeverStoresIt(t *testing.T) {
	js := scriptSource(t)

	if !strings.Contains(js, "history.replaceState") {
		t.Error("the setup code is not removed from the address bar")
	}
	// The code must go into the form field and nowhere else.
	if !strings.Contains(js, "field.value = token") {
		t.Error("the setup code is not held in the form field")
	}
	// The browser approval watcher is the one place that makes a request: a
	// GET without a body for this browser's approval state. It is cut out
	// and checked on its own; the rest of the script, the setup fragment
	// handler included, must not send or record anything.
	watcher := section(t, js, "(function watchApproval()", "})();")
	rest := scriptOutsideRequests(t)
	for _, sink := range []string{"sessionStorage.setItem", "fetch(", "XMLHttpRequest", "console.log"} {
		if strings.Contains(rest, sink) {
			t.Errorf("the script sends or records data through %q", sink)
		}
	}
	if strings.Count(watcher, "fetch(") != 1 || !strings.Contains(watcher, "method: 'GET'") {
		t.Error("the approval watcher must make exactly one kind of request, a GET")
	}
	for _, leak := range []string{
		"body", "token", "csrf", "location.hash", "Storage", "sendBeacon", "XMLHttpRequest", "WebSocket", "EventSource",
		"console.", "Observer", "import", "require(", "<script", "postMessage", "document.cookie",
	} {
		if strings.Contains(watcher, leak) {
			t.Errorf("the approval watcher touches %q", leak)
		}
	}

	// Redemption must be a deliberate submission. The script fills the field
	// and stops; nothing inside the setup block submits the form.
	start := strings.Index(js, "function handleSetupFragment")
	if start < 0 {
		t.Fatal("the setup fragment handler is missing")
	}
	block := js[start:]
	if end := strings.Index(block, "})();"); end >= 0 {
		block = block[:end]
	}
	for _, auto := range []string{".submit()", "requestSubmit", ".click()"} {
		if strings.Contains(block, auto) {
			t.Errorf("the setup page redeems the code without the owner acting: %q", auto)
		}
	}
}

func TestScriptPreferenceCookieIsNotACredential(t *testing.T) {
	js := scriptSource(t)
	if !strings.Contains(js, "SameSite=Lax") {
		t.Error("the preference cookie has no SameSite attribute")
	}
	if !strings.Contains(js, "Path=/") {
		t.Error("the preference cookie is not scoped to the whole interface")
	}
	if !strings.Contains(js, "https:") || !strings.Contains(js, "Secure") {
		t.Error("the preference cookie is not marked Secure over HTTPS")
	}
	// A preference cookie must never be treated as proof of anything.
	for _, forbidden := range []string{"owngit_session", "admin", "token="} {
		if strings.Contains(js, `'`+forbidden) || strings.Contains(js, `"`+forbidden) {
			t.Errorf("the script handles %q, which is not a preference", forbidden)
		}
	}
}

func TestMissingURLsDoNotBecomeDeadLinks(t *testing.T) {
	// The backend cannot always address a retained ref or a path segment. A
	// link with an empty target would look clickable and do nothing, so those
	// entries render as plain text instead.
	r := newRenderer(t)
	page := repoPage(fullChrome(LangEN), RepoTabOverview)
	page.Overview.RetainedRefs = []RefLine{{Name: "7f2c1a0", Kind: "branch", Retained: true}}
	page.Overview.Branches = []RefLine{{Name: "main", Kind: "branch", IsDefault: true}}
	page.Overview.Tags = []RefLine{{Name: "v1.0", Kind: "tag"}}
	out := render(t, r, page)

	if strings.Contains(out, `href=""`) {
		t.Error("an entry without a target rendered as an empty link")
	}
	// The information itself must still be readable.
	for _, name := range []string{"7f2c1a0", "main", "v1.0"} {
		if !strings.Contains(out, name) {
			t.Errorf("%q disappeared when it had no URL", name)
		}
	}

	code := repoPage(fullChrome(LangEN), RepoTabCode)
	code.Code.Crumbs = []Crumb{{Name: "forge-cli"}, {Name: "internal", Current: true}}
	if out := render(t, r, code); strings.Contains(out, `href=""`) {
		t.Error("a breadcrumb without a target rendered as an empty link")
	}
}

// TestLanguageClickTranslatesRefGroupsAndImportWords drives the shipped script
// over the renderer's real markup: the ref picker's group labels and the import
// mode, run and ref words switch in place; an unknown token stays escaped data.
func TestLanguageClickTranslatesRefGroupsAndImportWords(t *testing.T) {
	r := newRenderer(t)
	c := fullChrome(LangEN)
	imp := allPages(LangEN)["import-admin"].(ImportPage)
	imp.Refs = []ImportRefRow{{Name: "main", State: "diverged"}, {Name: "odd", State: "<not & a state>"}}
	out := render(t, r, repoPage(c, RepoTabCode)) + render(t, r, imp)

	// The unknown token is data in both languages: escaped once, no carrier.
	if strings.Contains(out, "<not & a state>") || !strings.Contains(out, `&lt;not &amp; a state&gt;</span>`) {
		t.Fatal("an unknown import token is not shown as escaped text")
	}
	if strings.Contains(out, `data-en="&lt;not`) {
		t.Error("an unknown import token was offered for translation")
	}

	// Watch the real tags: both optgroups and the import words on their spans.
	tags := regexp.MustCompile(`<optgroup [^>]*>`).FindAllString(out, -1)
	for _, en := range []string{"Coexistence", "Refresh", "Failed", "Diverged", "Access token"} {
		tag := regexp.MustCompile(`<span data-en="` + en + `" data-ko="[^"]*">` + en).FindString(out)
		if tag == "" {
			t.Fatalf("%q is rendered without both languages", en)
		}
		tags = append(tags, tag)
	}
	_, lang, nodes := clickLanguage(t, out, c.CurrentURL, tags...)
	if lang != string(LangKO) || len(nodes) != len(tags) {
		t.Fatalf("the click left lang=%q with %d of %d watched elements", lang, len(nodes), len(tags))
	}
	if got := []string{nodes[0].Attrs["label"], nodes[1].Attrs["label"]}; got[0] != "브랜치" || got[1] != "태그" {
		t.Errorf("ref group labels after the click: %q", got)
	}
	want := []string{"공존", "새로고침", "실패", "원본과 다름", "액세스 토큰"}
	for i, node := range nodes[2:] {
		if node.Text != want[i] {
			t.Errorf("import word %d after the click = %q, want %q", i, node.Text, want[i])
		}
	}
}
