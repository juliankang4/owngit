package webui

import (
	"regexp"
	"strings"
	"testing"
)

// Guards the repository tab strip against the 390px defect where "Pull
// requests" wrapped onto a second line, moving the active underline off the
// word it marks and making the row taller than its neighbours.
//
// These read the stylesheet, since the defect is in the rules. A browser check
// at the real widths is what confirms the pixels.

// cssRule returns the declarations of one rule in the embedded stylesheet.
func cssRule(t *testing.T, selector string) string {
	t.Helper()
	sheet, err := assetFS.ReadFile("assets/owngit.css")
	if err != nil {
		t.Fatal(err)
	}
	// Match the selector as a whole rule head so ".rtabs" does not also find
	// ".rtabs__btn".
	pattern := regexp.MustCompile(`(?m)^` + regexp.QuoteMeta(selector) + `\s*\{([^}]*)\}`)
	match := pattern.FindSubmatch(sheet)
	if match == nil {
		t.Fatalf("no rule for %q in the stylesheet", selector)
	}
	return string(match[1])
}

func TestRepositoryTabLabelsStayOnOneLine(t *testing.T) {
	// A tab label names a destination, so it is shown whole on one line.
	button := cssRule(t, ".rtabs__btn")

	if !strings.Contains(button, "white-space: nowrap") {
		t.Error("tab labels may wrap, which is the reported 390px defect")
	}
	// A shrinking tab wraps the text inside it, so the width comes from the
	// label.
	if !strings.Contains(button, "flex: none") {
		t.Error("tabs can shrink below their label width, which forces a wrap")
	}
	for _, shrinking := range []string{"flex-shrink: 1", "flex: 1", "min-width: 0"} {
		if strings.Contains(button, shrinking) {
			t.Errorf("tabs declare %q, which lets the label wrap again", shrinking)
		}
	}
}

func TestRepositoryTabsAreNeitherTruncatedNorShrunk(t *testing.T) {
	// Hiding or shrinking labels would trade one unreadable strip for another.
	button := cssRule(t, ".rtabs__btn")

	for _, hiding := range []string{"text-overflow: ellipsis", "overflow: hidden", "max-width"} {
		if strings.Contains(button, hiding) {
			t.Errorf("tab labels are truncated with %q instead of being shown whole", hiding)
		}
	}
	// One size across the strip, including in narrow-viewport overrides.
	if !strings.Contains(button, "font-size: 13px") {
		t.Error("the tab font size changed; the strip is meant to keep one size")
	}
	sheet, err := assetFS.ReadFile("assets/owngit.css")
	if err != nil {
		t.Fatal(err)
	}
	for _, override := range regexp.MustCompile(`\.rtabs__btn[^{]*\{[^}]*\}`).FindAllString(string(sheet), -1) {
		if strings.Contains(override, "font-size") && !strings.Contains(override, "font-size: 13px") {
			t.Errorf("a rule sets a different tab font size: %s", strings.TrimSpace(override))
		}
	}
}

func TestRepositoryTabsOverflowSidewaysWithinTheStrip(t *testing.T) {
	// Labels that do not fit are reached by scrolling the strip, and that
	// scrolling stays inside it.
	strip := cssRule(t, ".rtabs")

	if !strings.Contains(strip, "overflow-x: auto") {
		t.Error("the tab strip does not scroll sideways, so wide labels have nowhere to go")
	}
	if !strings.Contains(strip, "overscroll-behavior-x: contain") {
		t.Error("scrolling past the end of the strip escapes to the page behind it")
	}
	if !strings.Contains(strip, "display: flex") {
		t.Error("the strip is no longer a single row")
	}
	// Wrapping the row would reintroduce the tall strip from the other end.
	if strings.Contains(strip, "flex-wrap: wrap") {
		t.Error("the strip wraps onto a second row")
	}
}

func TestRepositoryTabDecorationSurvivesTheScrollport(t *testing.T) {
	// A scrollport clips vertically too, so anything painted outside a tab's
	// own box would be cut off.
	strip := cssRule(t, ".rtabs")
	button := cssRule(t, ".rtabs__btn")
	active := cssRule(t, `.rtabs__btn[aria-current="page"]`)

	// The active tab still marks itself.
	if !strings.Contains(active, "border-bottom-color: var(--accent-line)") {
		t.Error("the active tab lost its underline")
	}
	if !strings.Contains(button, "border-bottom: 2px solid transparent") {
		t.Error("tabs no longer reserve space for the active underline")
	}
	// A negative margin would put the underline in clipped space, so the strip
	// draws its own line instead.
	if strings.Contains(button, "margin-bottom: -") {
		t.Error("the active underline reaches into clipped space with a negative margin")
	}
	if !strings.Contains(strip, "inset 0 -1px 0 var(--sep)") {
		t.Error("the line under the strip is gone")
	}
}

func TestRepositoryTabFocusRingIsVisibleInsideTheScrollport(t *testing.T) {
	// The shared ring is outset, which the scrollport clips, and keyboard
	// users still need to see which tab has focus.
	focus := cssRule(t, ".rtabs__btn:focus-visible")

	if !strings.Contains(focus, "outline: 2px solid var(--focus)") {
		t.Error("the tab focus ring does not match the shared ring")
	}
	if !strings.Contains(focus, "outline-offset: -2px") {
		t.Error("the focus ring is drawn outside the tab, where the scrollport clips it")
	}

	// Tabbing to the tab at either end must bring it fully into view.
	strip := cssRule(t, ".rtabs")
	button := cssRule(t, ".rtabs__btn")
	if !strings.Contains(strip, "scroll-padding-inline") {
		t.Error("a tab scrolled into view can sit flush against the edge of the strip")
	}
	if !strings.Contains(button, "scroll-margin-inline") {
		t.Error("a focused tab is scrolled into view without room around it")
	}
}

func TestRepositoryTabsRemainReachableWithoutScripting(t *testing.T) {
	// Presentation only: every tab is still a plain link.
	r := newRenderer(t)
	for _, lang := range Langs() {
		out := render(t, r, pullRequestsPage(fullChrome(lang), false))
		strip := out[strings.Index(out, `<nav class="rtabs"`):]
		strip = strip[:strings.Index(strip, "</nav>")]

		if n := strings.Count(strip, `class="rtabs__btn"`) + strings.Count(strip, `class="rtabs__btn" `); n == 0 {
			t.Fatalf("%s: no tabs rendered", lang)
		}
		for _, tab := range strings.Split(strip, "<a ")[1:] {
			if !strings.Contains(tab, "href=") || strings.Contains(tab, `href=""`) {
				t.Errorf("%s: a tab is not a followable link: %s", lang, strings.TrimSpace(tab))
			}
		}
		if strings.Contains(strip, "tabindex=\"-1\"") {
			t.Errorf("%s: a tab was taken out of the keyboard order", lang)
		}
	}
}
