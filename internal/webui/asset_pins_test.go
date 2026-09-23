package webui

import (
	"encoding/json"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// Stylesheet and script rules that earlier layout and behaviour fixes depend
// on. They read the shipped assets because the defects were in the rules; a
// browser at the real widths is what confirms the pixels. The script-security
// checks (setup fragment, preference cookie, stored secrets) stay separate in
// behaviour_test.go and webui_test.go.

// assetPin is one invariant over part of a shipped asset.
type assetPin struct {
	name  string
	src   func(t *testing.T) string
	has   []string
	lacks []string
	extra func(t *testing.T, src string)
}

func TestAssetPins(t *testing.T) {
	sheet := func(t *testing.T) string { return readSheet(t) }
	js := func(t *testing.T) string { return scriptSource(t) }
	rule := func(selector string) func(*testing.T) string {
		return func(t *testing.T) string { return cssRule(t, selector) }
	}
	// narrow is a rule's declarations inside the 620px media query.
	narrow := func(selector string) func(*testing.T) string {
		return func(t *testing.T) string {
			return declarationsOf(t, mediaBlock(t, readSheet(t), "@media (max-width: 620px)"), selector)
		}
	}
	sheetFrom := func(from string) func(*testing.T) string {
		return func(t *testing.T) string { return section(t, readSheet(t), from, "}") }
	}
	jsFrom := func(from, to string) func(*testing.T) string {
		return func(t *testing.T) string { return section(t, scriptSource(t), from, to) }
	}
	tabs := func(t *testing.T) string { return tabScript(t) }

	pins := []assetPin{
		// The 390px tab strip defect: "Pull requests" wrapped, moving the
		// active underline off its word. Labels stay whole on one line at one
		// size, and the strip scrolls sideways within itself.
		{name: "tab labels stay whole on one line", src: rule(".rtabs__btn"),
			has: []string{"white-space: nowrap", "flex: none", "font-size: 13px", "border-bottom: 2px solid transparent", "scroll-margin-inline"},
			// A negative margin would put the underline in clipped space.
			lacks: []string{"flex-shrink: 1", "flex: 1", "min-width: 0", "text-overflow: ellipsis", "overflow: hidden", "max-width", "margin-bottom: -"}},
		{name: "no override changes the tab size", src: sheet, extra: func(t *testing.T, css string) {
			for _, override := range regexp.MustCompile(`\.rtabs__btn[^{]*\{[^}]*\}`).FindAllString(css, -1) {
				if strings.Contains(override, "font-size") && !strings.Contains(override, "font-size: 13px") {
					t.Errorf("a rule sets a different tab font size: %s", strings.TrimSpace(override))
				}
			}
		}},
		{name: "the tab strip is one row that scrolls within itself", src: rule(".rtabs"),
			has:   []string{"overflow-x: auto", "overscroll-behavior-x: contain", "display: flex", "inset 0 -1px 0 var(--sep)", "scroll-padding-inline"},
			lacks: []string{"flex-wrap: wrap"}},
		{name: "the active tab keeps its underline", src: rule(`.rtabs__btn[aria-current="page"]`),
			has: []string{"border-bottom-color: var(--accent-line)"}},
		// The shared ring is outset, which the scrollport would clip.
		{name: "the tab focus ring is inset", src: rule(".rtabs__btn:focus-visible"),
			has: []string{"outline: 2px solid var(--focus)", "outline-offset: -2px"}},

		// The script that brings the current tab into view moves only the
		// strip, never the page or focus, and leaves other scroll regions
		// alone. scrollIntoView would scroll every ancestor.
		{name: "the tab reveal moves only its own strip", src: tabs,
			has: []string{`.rtabs__btn[aria-current="page"]`, "strip.scrollLeft", "strip.scrollWidth <= strip.clientWidth",
				`addEventListener('resize'`, "requestAnimationFrame", "all('.rtabs')", "strip.addEventListener('focusin'", "strip.contains(focused)"},
			lacks: []string{"scrollIntoView", "window.scrollTo", "window.scrollBy", "document.documentElement.scrollTop",
				".focus()", ".blur()", "autofocus", "focus({", "document.addEventListener('focusin'", "document.body.contains",
				".hm__scroll", ".clone__cmds", ".sidebar", ".codebox", "[style*=overflow]", "*'", `querySelectorAll('*')`},
			// Resize and language share one chooser, so a resize while focus
			// is in the strip does not scroll the focused tab away.
			extra: func(t *testing.T, src string) {
				if strings.Count(src, "revealTabsOfInterest") < 2 {
					t.Error("resize does not reuse the shared chooser")
				}
			}},
		{name: "the script adds no observers or external code", src: js,
			lacks: []string{"IntersectionObserver", "ResizeObserver", "MutationObserver", "import ", "require(", "fetch(", "XMLHttpRequest", "<script"}},
		// An in-place language change rewrites every label and fires no
		// resize, so the switch asks for the reveal itself.
		{name: "switching language keeps the tab visible", src: jsFrom("function applyLanguage(", "\n  }\n"),
			has:   []string{"revealTabsOfInterest()"},
			lacks: []string{"scrollIntoView", "window.scrollTo", ".focus()"}},
		{name: "the reveal helpers are defined once", src: js,
			extra: func(t *testing.T, src string) {
				for _, helper := range []string{"function revealTab(", "function revealTabOfInterest("} {
					if n := strings.Count(src, helper); n != 1 {
						t.Errorf("%s is defined %d times", helper, n)
					}
				}
			}},

		// The 390px toolbar defect: the language picker dropped onto its own
		// row. The controls travel as one group, and the narrow header wraps
		// in source order without hiding or shrinking labels.
		{name: "the header control group does not wrap internally", src: rule(".toolbar__controls"),
			has: []string{"flex-wrap: nowrap", "flex: none", "display: flex"}},
		{name: "narrow identity leads and can shrink", src: narrow(".toolbar__id"), has: []string{"order: 1", "min-width: 0"}},
		{name: "narrow search follows identity on a full row", src: narrow(".field"), has: []string{"order: 2", "flex: 1 0 100%"}},
		{name: "narrow controls follow search", src: narrow(".toolbar__controls"), has: []string{"order: 3"},
			lacks: []string{"display: none", "visibility: hidden", "font-size"}},
		{name: "narrow language picker is not hidden or shrunk", src: narrow(".lang"), lacks: []string{"display: none", "visibility: hidden", "font-size"}},
		{name: "narrow language labels are not hidden or shrunk", src: narrow(".lang__btn"), lacks: []string{"display: none", "visibility: hidden", "font-size"}},
		{name: "narrow version is not hidden or shrunk", src: narrow(".toolbar__ver"), lacks: []string{"display: none", "visibility: hidden", "font-size"}},
		{name: "no narrow rule positions a single control",
			src:   func(t *testing.T) string { return mediaBlock(t, readSheet(t), "@media (max-width: 620px)") },
			lacks: []string{".lang {", ".conn {", ".toolbar .field + .conn"}},

		// Wide content scrolls inside its own panel. The restore file list is
		// here because a page-long list would push the confirmation out of
		// reach.
		{name: "code and diff panels contain their overflow", src: sheetFrom(".codebox, .diffbox"), has: []string{"overflow"}},
		{name: "the activity graph contains its overflow", src: sheetFrom(".hm__scroll"), has: []string{"overflow"}},
		{name: "the restore path list contains its overflow", src: sheetFrom(".rpaths {"), has: []string{"overflow"}},
		// The restore source and target pair collapses on a phone, and its
		// narrow rule comes after the two-column default so it wins.
		{name: "the narrow layout exists and the restore pair collapses", src: sheet,
			has: []string{"@media (max-width: 620px)", "min-width: 0", ".restore__pair {", ".restore__pair { grid-template-columns: minmax(0, 1fr)"},
			extra: func(t *testing.T, css string) {
				if strings.Index(css, ".restore__pair { grid-template-columns: minmax(0, 1fr)") < strings.Index(css, ".restore__pair {") {
					t.Error("the narrow restore layout is overridden by the wide one")
				}
			}},
		{name: "motion and focus preferences are respected", src: sheet,
			has:   []string{"@media (prefers-reduced-motion: no-preference)", ":focus-visible"},
			lacks: []string{"outline: none", "outline: 0"}},
		// Dark is its own palette, and System yields to an explicit choice.
		{name: "System appearance is subordinate to a choice", src: sheet,
			has:   []string{"@media (prefers-color-scheme: dark)", ".theme-system {", ".theme-dark {"},
			lacks: []string{"filter: invert"}},
		{name: "the script keeps an explicit appearance over the system", src: js,
			has: []string{`var APPEARANCE_KEY = 'owngit_appearance';`, "prefers-color-scheme: dark", `currentAppearance() === 'system'`}},
		// All text comes from server-rendered data-en and data-ko, so the two
		// languages cannot drift from the Go catalog.
		{name: "the script translates from rendered text only", src: js,
			has: []string{`data-' + lang`}, lacks: []string{"저장소", "브랜치", "Repositories'", "Settings'"}},
		{name: "the stylesheet has no outbound dependency", src: sheet,
			lacks: []string{"http://", "https://", "//fonts.", "@import url(http"}},

		// A class rule such as `.fieldnote { display: flex }` beat the
		// browser's own [hidden] rule, so the welcome page showed "the code
		// is held" and "there is no setup code" at once.
		{name: "the hidden attribute is restated to win", src: rule("[hidden]"), has: []string{"display: none", "!important"}},
		{name: "the script shows exactly one welcome state", src: js,
			has: []string{"held.hidden = !haveToken", "missing.hidden = haveToken", "if (haveToken)"}},
		// Enter is left to the browser, since the day is a link.
		{name: "graph keyboard and readout stay wired together", src: js,
			has: []string{"graph.querySelector('[data-graph-readout]')", "announce(cell)"}, lacks: []string{"case 'Enter':"}},
		// The script dims the file list while the whole project is selected.
		// Clearing or disabling the inputs would submit a selection nobody made.
		{name: "the restore scope hint is presentation only", src: jsFrom("data-restore-files", "})();"),
			has: []string{"data-restore-dimmed"}, lacks: []string{"checked = false", "disabled", "remove()"}},

		// A retained badge was clipped at 1440px because the row truncated
		// its last child. Only the ref name shortens; the badge keeps its
		// width and one line, and is prose in the interface font rather than
		// the identifier font.
		{name: "only the ref name truncates", src: rule(".row__refname"), has: []string{"text-overflow: ellipsis", "overflow: hidden", "min-width: 0"}},
		{name: "the ref row keeps monospace and does not truncate", src: rule(".row__ref"),
			has: []string{"font-family: var(--mono)"}, lacks: []string{"text-overflow"}},
		{name: "the badge is readable prose on one line", src: rule(".pill"),
			has: []string{"white-space: nowrap", "font-family: var(--font)"}, lacks: []string{"var(--mono)"},
			extra: func(t *testing.T, pill string) {
				size := regexp.MustCompile(`font-size:\s*([0-9.]+)px`).FindStringSubmatch(pill)
				if size == nil {
					t.Fatalf(".pill has no font size: %q", pill)
				}
				if got, err := strconv.ParseFloat(size[1], 64); err != nil || got < 10 {
					t.Errorf(".pill text was shrunk to %spx, below a readable size", size[1])
				}
			}},
		{name: "the badge does not shrink inside the source column", src: sheet, extra: func(t *testing.T, css string) {
			if !regexp.MustCompile(`\.row__ref > \.pill\s*\{[^}]*flex:\s*none`).MatchString(css) &&
				!regexp.MustCompile(`\.row__ref > svg,\s*\n?\.row__ref > \.pill\s*\{[^}]*flex:\s*none`).MatchString(css) {
				t.Error("the status badge can shrink inside the source column")
			}
		}},
	}
	for _, pin := range pins {
		t.Run(pin.name, func(t *testing.T) {
			src := pin.src(t)
			for _, want := range pin.has {
				if !strings.Contains(src, want) {
					t.Errorf("missing %q", want)
				}
			}
			for _, bad := range pin.lacks {
				if strings.Contains(src, bad) {
					t.Errorf("contains %q", bad)
				}
			}
			if pin.extra != nil {
				pin.extra(t, src)
			}
		})
	}
}

// TestTabsStillWorkWithoutTheScript checks that the tab strip is a list of
// plain links: the script improves them and is never needed to use them.
func TestTabsStillWorkWithoutTheScript(t *testing.T) {
	r := newRenderer(t)
	for _, lang := range Langs() {
		for name, page := range map[string]Page{
			"pull requests": pullRequestsPage(fullChrome(lang), false),
			"tasks":         tasksPage(fullChrome(lang), false),
		} {
			out := render(t, r, page)
			strip := out[strings.Index(out, `<nav class="rtabs"`):]
			strip = strip[:strings.Index(strip, "</nav>")]

			tabs := strings.Split(strip, "<a ")[1:]
			if len(tabs) == 0 || !strings.Contains(strip, `class="rtabs__btn"`) {
				t.Fatalf("%s %s: no tabs rendered", lang, name)
			}
			for _, tab := range tabs {
				if !strings.Contains(tab, "href=") || strings.Contains(tab, `href=""`) {
					t.Errorf("%s %s: a tab is not a followable link: %s", lang, name, strings.TrimSpace(tab))
				}
			}
			if strings.Contains(strip, `tabindex="-1"`) {
				t.Errorf("%s %s: a tab was taken out of the keyboard order", lang, name)
			}
			if !strings.Contains(strip, `aria-current="page"`) {
				t.Errorf("%s %s: no tab is marked current, so the script has nothing to reveal", lang, name)
			}
			// A real tab list, not an ARIA tab widget that needs the script
			// to be operable.
			for _, widget := range []string{`role="tab"`, `role="tablist"`, `role="tabpanel"`} {
				if strings.Contains(strip, widget) {
					t.Errorf("%s %s: the strip uses %s, which needs script-driven keyboard handling", lang, name, widget)
				}
			}
		}
	}
}

// TestTabRevealGeometry runs the shipped revealTab against geometry measured in
// a real browser at 320px, including the two reported cases: the active tab
// hidden at load, and a focused tab left partly outside by native Tab.
//
// The pins above check what the script says; this checks what it computes. It
// needs a JavaScript runtime and is skipped without one, so it adds no
// dependency.
func TestTabRevealGeometry(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("no JavaScript runtime available to exercise the script")
	}

	out, err := exec.Command(node, "testdata/tabreveal.js", "assets/owngit.js").CombinedOutput()
	var cases []struct {
		Name         string  `json:"name"`
		OK           bool    `json:"ok"`
		ScrollLeft   float64 `json:"scrollLeft"`
		Left         float64 `json:"left"`
		Right        float64 `json:"right"`
		FullyVisible bool    `json:"fullyVisible"`
		Error        string  `json:"error"`
	}
	if jsonErr := json.Unmarshal(out, &cases); jsonErr != nil {
		t.Fatalf("the geometry harness produced no report: %v\n%s", err, out)
	}
	for _, c := range cases {
		if !c.OK {
			t.Errorf("%s: scrollLeft %.1f left %.1f right %.1f fullyVisible %v %s",
				c.Name, c.ScrollLeft, c.Left, c.Right, c.FullyVisible, c.Error)
		}
	}
	if err != nil && !t.Failed() {
		t.Fatalf("the geometry harness failed without reporting a case: %v\n%s", err, out)
	}
}

// readSheet returns the embedded stylesheet.
func readSheet(t *testing.T) string {
	t.Helper()
	sheet, err := assetFS.ReadFile("assets/owngit.css")
	noErr(t, err)
	return string(sheet)
}

// scriptSource returns the embedded script, the only place that touches
// browser storage and the address bar.
func scriptSource(t *testing.T) string {
	t.Helper()
	data, err := assetFS.ReadFile("assets/owngit.js")
	noErr(t, err)
	return string(data)
}

// cssRule returns the declarations of the first top-level rule for selector.
// It matches the selector as a whole rule head so ".rtabs" does not also find
// ".rtabs__btn".
func cssRule(t *testing.T, selector string) string {
	t.Helper()
	pattern := regexp.MustCompile(`(?m)^` + regexp.QuoteMeta(selector) + `\s*\{([^}]*)\}`)
	match := pattern.FindStringSubmatch(readSheet(t))
	if match == nil {
		t.Fatalf("no rule for %q in the stylesheet", selector)
	}
	return match[1]
}

// section returns source from the first from up to the next to.
func section(t *testing.T, source, from, to string) string {
	t.Helper()
	start := strings.Index(source, from)
	if start < 0 {
		t.Fatalf("no %q in the asset", from)
	}
	end := strings.Index(source[start:], to)
	if end < 0 {
		t.Fatalf("the part starting at %q never ends", from)
	}
	return source[start : start+end]
}

// tabScript returns the shared reveal helper and the block that wires it to
// the tab strips. Slicing to these keeps the pins off unrelated code such as
// the activity graph's roving tab stop, which legitimately moves focus.
func tabScript(t *testing.T) string {
	t.Helper()
	source := scriptSource(t)
	return section(t, source, "var TAB_PAD", "function readCookie(") + "\n" +
		section(t, source, "var tabStrips = all('.rtabs')", "})();")
}

// mediaBlock returns the body of one media query.
func mediaBlock(t *testing.T, sheet, query string) string {
	t.Helper()
	start := strings.Index(sheet, query)
	if start < 0 {
		t.Fatalf("no %s block in the stylesheet", query)
	}
	depth := 0
	for i := start; i < len(sheet); i++ {
		switch sheet[i] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return sheet[start : i+1]
			}
		}
	}
	t.Fatalf("the %s block never closes", query)
	return ""
}

// declarationsOf returns the declarations of one rule inside a block, joined
// when the rule appears more than once.
func declarationsOf(t *testing.T, block, selector string) string {
	t.Helper()
	var found []string
	rest := block
	for {
		at := strings.Index(rest, selector+" {")
		if at < 0 {
			break
		}
		// Reject a longer selector that merely starts with this one.
		if at > 0 {
			before := rest[at-1]
			if before != '\n' && before != ' ' && before != '}' {
				rest = rest[at+len(selector):]
				continue
			}
		}
		body := rest[at:]
		end := strings.Index(body, "}")
		if end < 0 {
			break
		}
		found = append(found, body[:end])
		rest = body[end:]
	}
	return strings.Join(found, "\n")
}
