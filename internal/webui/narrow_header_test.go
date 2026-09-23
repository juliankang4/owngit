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

func TestHeaderControlGroupDoesNotWrapInternally(t *testing.T) {
	group := cssRule(t, ".toolbar__controls")

	if !strings.Contains(group, "flex-wrap: nowrap") {
		t.Error("the control group can wrap inside itself, which is the reported defect")
	}
	if !strings.Contains(group, "flex: none") {
		t.Error("the control group can shrink, which squeezes its controls")
	}
	if !strings.Contains(group, "display: flex") {
		t.Error("the control group is no longer a row")
	}
}

func TestNarrowHeaderWrapsInSourceOrder(t *testing.T) {
	// Visual order follows the DOM. Search fills its row.
	sheet := readSheet(t)
	narrow := mediaBlock(t, sheet, "@media (max-width: 620px)")

	for _, want := range []struct {
		rule, decl, why string
	}{
		{".toolbar__id", "order: 1", "identity does not lead the header"},
		{".field", "order: 2", "search does not follow identity"},
		{".toolbar__controls", "order: 3", "controls do not follow search"},
		{".field", "flex: 1 0 100%", "search does not take the full width"},
		{".toolbar__id", "min-width: 0", "a long storage path cannot shrink"},
	} {
		if !strings.Contains(declarationsOf(t, narrow, want.rule), want.decl) {
			t.Errorf("%s (%s missing from %s)", want.why, want.decl, want.rule)
		}
	}

	// The old rules moved the language picker on its own. Nothing in the
	// narrow block may position a single control again.
	for _, stale := range []string{".lang {", ".conn {", ".toolbar .field + .conn"} {
		if strings.Contains(narrow, stale) {
			t.Errorf("the narrow header still positions %s individually", stale)
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

func TestNarrowHeaderDoesNotHideOrShrinkLabels(t *testing.T) {
	// Fitting the header by hiding language labels or setting them smaller
	// would trade one unreadable header for another.
	sheet := readSheet(t)
	narrow := mediaBlock(t, sheet, "@media (max-width: 620px)")

	for _, rule := range []string{".lang", ".lang__btn", ".toolbar__ver", ".toolbar__controls"} {
		decls := declarationsOf(t, narrow, rule)
		for _, banned := range []string{"display: none", "visibility: hidden", "font-size"} {
			if strings.Contains(decls, banned) {
				t.Errorf("the narrow header applies %q to %s", banned, rule)
			}
		}
	}

	// Storage stays an administrator disclosure driven by the backend, not
	// something the stylesheet reveals or hides at a width.
	r := newRenderer(t)
	hidden := allPages(LangEN)["overview"].(OverviewPage)
	hidden.Chrome.Storage.Visible = false
	if bar := toolbarOf(t, render(t, r, hidden)); strings.Contains(bar, "toolbar__host") {
		t.Error("the storage location renders for a visitor it is hidden from")
	}
}

// readSheet returns the embedded stylesheet.
func readSheet(t *testing.T) string {
	t.Helper()
	sheet, err := assetFS.ReadFile("assets/owngit.css")
	if err != nil {
		t.Fatal(err)
	}
	return string(sheet)
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
