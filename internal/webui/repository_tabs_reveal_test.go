package webui

import (
	"encoding/json"
	"os/exec"
	"strings"
	"testing"
)

// Guards the script that brings the current repository tab into view.
//
// At 320px the active tab sat past the right edge with scrollLeft 0, so the
// page gave no sign of where the reader was. A container's initial scroll
// position cannot be set in CSS, which is why this one part needs the script.
//
// These read the embedded script. The pixels are a browser check.

// tabScript returns the code that deals with the repository tab strip: the
// shared reveal helper and the block that wires it up. Slicing to these keeps
// the assertions off unrelated parts of the file, such as the activity graph's
// roving tab stop, which legitimately moves focus.
func tabScript(t *testing.T) string {
	t.Helper()
	script, err := assetFS.ReadFile("assets/owngit.js")
	if err != nil {
		t.Fatal(err)
	}
	source := string(script)

	helper := section(t, source, "var TAB_PAD", "function readCookie(")
	wiring := section(t, source, "var tabStrips = all('.rtabs')", "})();")
	return helper + "\n" + wiring
}

func section(t *testing.T, source, from, to string) string {
	t.Helper()
	start := strings.Index(source, from)
	if start < 0 {
		t.Fatalf("the tab strip code is missing: no %q", from)
	}
	end := strings.Index(source[start:], to)
	if end < 0 {
		t.Fatalf("the tab strip code starting at %q never ends", from)
	}
	return source[start : start+end]
}

func TestTheActiveTabIsRevealedWithinItsOwnStrip(t *testing.T) {
	reveal := tabScript(t)

	// Found by the attribute the template already sets, so no extra markup.
	if !strings.Contains(reveal, `.rtabs__btn[aria-current="page"]`) {
		t.Error("the active tab is not identified by its existing aria-current attribute")
	}
	// Only the strip moves.
	if !strings.Contains(reveal, "strip.scrollLeft") {
		t.Error("the strip's own scroll position is never adjusted")
	}
	// scrollIntoView would scroll every scrollable ancestor, moving the page.
	if strings.Contains(reveal, "scrollIntoView") {
		t.Error("scrollIntoView moves the document as well as the strip")
	}
	for _, moving := range []string{"window.scrollTo", "window.scrollBy", "document.documentElement.scrollTop"} {
		if strings.Contains(reveal, moving) {
			t.Errorf("the whole document is scrolled with %q", moving)
		}
	}
	// Focus belongs to the reader. The script listens for it and never moves
	// it, so the calls that would move it are what is banned.
	for _, stealing := range []string{".focus()", ".blur()", "autofocus", "focus({"} {
		if strings.Contains(reveal, stealing) {
			t.Errorf("the script changes focus with %q", stealing)
		}
	}
	// A strip that fits needs no adjustment at all.
	if !strings.Contains(reveal, "strip.scrollWidth <= strip.clientWidth") {
		t.Error("the script adjusts strips that already fit")
	}
}

func TestTabRevealAddsNoObserversOrDependencies(t *testing.T) {
	script, err := assetFS.ReadFile("assets/owngit.js")
	if err != nil {
		t.Fatal(err)
	}
	source := string(script)

	for _, heavy := range []string{"IntersectionObserver", "ResizeObserver", "MutationObserver"} {
		if strings.Contains(source, heavy) {
			t.Errorf("the script uses %s for work a measurement covers", heavy)
		}
	}
	for _, external := range []string{"import ", "require(", "fetch(", "XMLHttpRequest", "<script"} {
		if strings.Contains(source, external) {
			t.Errorf("the script pulls in something external with %q", external)
		}
	}
	// Resize is handled, since it changes how much of the strip fits.
	reveal := tabScript(t)
	if !strings.Contains(reveal, `addEventListener('resize'`) {
		t.Error("a resize can leave the active tab out of view")
	}
	// Coalesced rather than run on every resize event.
	if !strings.Contains(reveal, "requestAnimationFrame") {
		t.Error("resize handling is not coalesced")
	}
}

func TestTabsStillWorkWithoutTheScript(t *testing.T) {
	// The script improves ordinary links. Without it the reader still gets a
	// strip they can scroll by hand.
	r := newRenderer(t)
	for _, lang := range Langs() {
		out := render(t, r, tasksPage(fullChrome(lang), false))
		strip := out[strings.Index(out, `<nav class="rtabs"`):]
		strip = strip[:strings.Index(strip, "</nav>")]

		if !strings.Contains(strip, `aria-current="page"`) {
			t.Errorf("%s: no tab is marked current, so the script has nothing to reveal", lang)
		}
		// A real tab list, not an ARIA tab widget that needs the script to be
		// operable.
		for _, widget := range []string{`role="tab"`, `role="tablist"`, `role="tabpanel"`} {
			if strings.Contains(strip, widget) {
				t.Errorf("%s: the strip uses %s, which needs script-driven keyboard handling", lang, widget)
			}
		}
		for _, tab := range strings.Split(strip, "<a ")[1:] {
			if !strings.Contains(tab, "href=") || strings.Contains(tab, `href=""`) {
				t.Errorf("%s: a tab is not a plain link", lang)
			}
		}
	}
}

func TestOtherScrollRegionsAreNotTouched(t *testing.T) {
	// The activity graph and clone commands scroll sideways too, and the tab
	// script must leave them alone.
	reveal := tabScript(t)

	if !strings.Contains(reveal, "all('.rtabs')") {
		t.Error("the script does not select the repository tab strips specifically")
	}
	// The focus handler is bound to the strip, not to the document.
	if !strings.Contains(reveal, "strip.addEventListener('focusin'") {
		t.Error("focus handling is not scoped to the tab strip")
	}
	if strings.Contains(reveal, "document.addEventListener('focusin'") {
		t.Error("focus handling is bound to the document, which sees every element")
	}
	for _, other := range []string{".hm__scroll", ".clone__cmds", ".sidebar", ".codebox"} {
		if strings.Contains(reveal, other) {
			t.Errorf("the tab script reaches into %s", other)
		}
	}
	// It must not sweep every scrollable element on the page.
	for _, sweeping := range []string{"[style*=overflow]", "*'", `querySelectorAll('*')`} {
		if strings.Contains(reveal, sweeping) {
			t.Errorf("the script selects broadly with %q", sweeping)
		}
	}
}

// TestTabRevealGeometry runs the shipped revealTab against geometry measured in
// a real browser at 320px, including the two cases reported against the earlier
// build: the active tab hidden at load, and a focused tab left partly outside
// by native Tab.
//
// The static tests above check what the script says. This checks what it
// computes. It needs a JavaScript runtime and is skipped without one, so it
// adds no dependency; the browser remains what confirms the pixels.
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

func TestSwitchingLanguageInPlaceKeepsTheTabVisible(t *testing.T) {
	// Switching language rewrites every label, so the tabs change width and the
	// visible one can end up outside the strip. No resize event fires for an
	// in-place change, so the switch has to ask for the reveal itself.
	script, err := assetFS.ReadFile("assets/owngit.js")
	if err != nil {
		t.Fatal(err)
	}
	source := string(script)

	start := strings.Index(source, "function applyLanguage(")
	if start < 0 {
		t.Fatal("no in-place language switch")
	}
	end := strings.Index(source[start:], "\n  }\n")
	if end < 0 {
		t.Fatal("applyLanguage never ends")
	}
	apply := source[start : start+end]

	if !strings.Contains(apply, "revealTabsOfInterest()") {
		t.Error("an in-place language change can leave the tab of interest outside the strip")
	}
	// The same helper as every other caller, rather than a second copy.
	if strings.Count(source, "function revealTab(") != 1 {
		t.Error("revealTab is defined more than once")
	}
	if strings.Count(source, "function revealTabOfInterest(") != 1 {
		t.Error("revealTabOfInterest is defined more than once")
	}
	// The language switch must not start moving the page or focus either.
	for _, moving := range []string{"scrollIntoView", "window.scrollTo", ".focus()"} {
		if strings.Contains(apply, moving) {
			t.Errorf("the language switch moves the reader with %q", moving)
		}
	}
}

func TestTheRevealedTabFollowsWhereTheReaderIs(t *testing.T) {
	// Focus and the current page can disagree, for instance while tabbing
	// through the strip on another tab's page. Keeping the focused tab visible
	// is what matters then, including across a resize.
	reveal := tabScript(t)

	if !strings.Contains(reveal, "strip.contains(focused)") {
		t.Error("the script does not notice when focus is inside the strip")
	}
	if !strings.Contains(reveal, `.rtabs__btn[aria-current="page"]`) {
		t.Error("the current page's tab is no longer the fallback")
	}
	// Resize and language both go through the same chooser, so a resize while
	// focus is in the strip does not scroll the focused tab away.
	if strings.Count(reveal, "revealTabsOfInterest") < 2 {
		t.Error("resize does not reuse the shared chooser")
	}
	if strings.Contains(reveal, "document.body.contains") {
		t.Error("the script tests the document rather than the strip")
	}
}
