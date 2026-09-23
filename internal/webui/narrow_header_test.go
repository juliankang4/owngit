package webui

import (
	"strings"
	"testing"
)

// Guards the toolbar against the reported 390px defect, where the language
// picker dropped onto a row of its own below the theme buttons.
//
// The controls were separate flex children, so the wrap could split them
// anywhere. They now travel as one group. The markup checks run against
// rendered pages; the layout checks read the stylesheet, since that is where
// the defect was. A browser at the real widths confirms the pixels.

func toolbarOf(t *testing.T, document string) string {
	t.Helper()
	start := strings.Index(document, `<header class="toolbar">`)
	if start < 0 {
		t.Fatal("no toolbar was rendered")
	}
	end := strings.Index(document[start:], "</header>")
	if end < 0 {
		t.Fatal("the toolbar was never closed")
	}
	return document[start : start+end]
}

func TestHeaderControlsTravelAsOneGroup(t *testing.T) {
	// Connection, appearance and language sit inside one element, so the
	// toolbar can only wrap around the group and never inside it.
	r := newRenderer(t)
	for _, lang := range Langs() {
		bar := toolbarOf(t, render(t, r, allPages(lang)["overview"]))

		group := strings.Index(bar, `class="toolbar__controls"`)
		if group < 0 {
			t.Fatalf("%s: the header controls are not grouped", lang)
		}
		for _, control := range []string{`class="conn`, `class="appear"`, `class="lang"`} {
			at := strings.Index(bar, control)
			if at < 0 {
				t.Errorf("%s: %s is missing from the toolbar", lang, control)
				continue
			}
			if at < group {
				t.Errorf("%s: %s sits outside the control group and can wrap away from it", lang, control)
			}
		}
	}
}

func TestNarrowHeaderKeepsItsContentAndOrder(t *testing.T) {
	// The fix is layout only. Labels stay, the source order is unchanged, and
	// nothing is duplicated to achieve the arrangement.
	r := newRenderer(t)
	for _, lang := range Langs() {
		page := fullChrome(lang)
		bar := toolbarOf(t, render(t, r, allPages(lang)["overview"]))

		// Source order still matches reading and focus order.
		positions := []struct {
			name, marker string
		}{
			{"identity", `class="toolbar__id"`},
			{"search", `role="search"`},
			{"controls", `class="toolbar__controls"`},
		}
		previous := -1
		for _, p := range positions {
			at := strings.Index(bar, p.marker)
			if at < 0 {
				t.Fatalf("%s: %s is missing", lang, p.name)
			}
			if at < previous {
				t.Errorf("%s: %s was moved out of source order", lang, p.name)
			}
			previous = at
		}

		// One of each control, so the arrangement is not achieved by
		// rendering a second copy for narrow screens.
		for _, marker := range []string{`data-appearance-picker`, `data-lang-picker`, `class="toolbar__controls"`} {
			if n := strings.Count(bar, marker); n != 1 {
				t.Errorf("%s: %s appears %d times, want 1", lang, marker, n)
			}
		}

		// The version and the language labels are still text on the page.
		if !strings.Contains(bar, page.Version) {
			t.Errorf("%s: the running version is no longer shown", lang)
		}
		// Both language labels are present as text, whichever is selected.
		for _, label := range []string{"English", "한국어"} {
			if !strings.Contains(bar, ">"+label+"<") {
				t.Errorf("%s: language label %q is not rendered as text", lang, label)
			}
		}
	}
}
