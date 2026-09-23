package webui

import (
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

func TestScriptClearsTheSetupFragmentAndNeverStoresIt(t *testing.T) {
	js := scriptSource(t)

	if !strings.Contains(js, "history.replaceState") {
		t.Error("the setup code is not removed from the address bar")
	}
	// The code must go into the form field and nowhere else.
	if !strings.Contains(js, "field.value = token") {
		t.Error("the setup code is not held in the form field")
	}
	for _, sink := range []string{"sessionStorage.setItem", "fetch(", "XMLHttpRequest", "console.log"} {
		if strings.Contains(js, sink) {
			t.Errorf("the script sends or records data through %q", sink)
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
