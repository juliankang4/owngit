package webui

import (
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

	pins := []assetPin{
		// The sidebar replaced the repository tab strip. Below 900px the
		// script folds it behind one button; the fold rule lives only in the
		// narrow layout, so a wide window and a page without the script
		// always show the whole menu.
		{name: "the sidebar folds only in the narrow layout", src: func(t *testing.T) string {
			return mediaBlock(t, readSheet(t), "@media (max-width: 900px)")
		}, has: []string{".sidebar--folds:not(.is-open) .sidebar__inner { display: none; }", ".sb__toggle:not([hidden])"}},
		{name: "the menu button is hidden on a wide window", src: rule(".sb__toggle"), has: []string{"display: none"}},
		{name: "the current place is marked by more than colour", src: rule(`.sb__item[aria-current="page"]`),
			has: []string{"box-shadow: inset 3px 0 0", "font-weight: 600"}},
		{name: "the sidebar script only folds, filters and closes", src: jsFrom("var sidebar = document.querySelector('[data-sidebar]')", "/* File list drawer."),
			has:   []string{"sideToggle.hidden = false", "sidebar.classList.add('sidebar--folds')", "'Escape'", "sideToggle.focus()", "row.hidden = !hit", "sideFilter.hidden = false"},
			lacks: []string{"innerHTML", "textContent", ".value =", "scrollIntoView", "window.scrollTo"}},

		// A file and a diff flow with the page, and long lines scroll inside
		// them until the reader turns wrapping on.
		{name: "code and diff panels have no height of their own", src: sheetFrom(".codebox, .diffbox"),
			lacks: []string{"max-height", "overflow-y"}},
		{name: "lines do not wrap by default", src: sheet,
			has: []string{".codetable__t, .difftable__t { padding: 0 10px; white-space: pre;",
				`[data-wrap="1"] .codetable__t, [data-wrap="1"] .difftable__t { white-space: pre-wrap;`}},
		{name: "the wrap switch is remembered per browser and starts off", src: jsFrom("var WRAP_KEY", "/* Diff files fold"),
			has:   []string{"var WRAP_KEY = 'owngit_wrap';", "applyWrap(storedWrap === '1')", "button.hidden = false"},
			lacks: []string{"document.cookie", "writeCookie"}},
		{name: "diff files keep their markers and edge bars", src: sheet,
			has: []string{".dfile .difftable__s { width: 1ch; margin-right: 1ch; font-weight: 700;",
				".dfile .difftable__r.is-add .difftable__n:first-child { box-shadow: inset 3px 0 0 var(--ok); }",
				".dfile .difftable__r.is-del .difftable__n:first-child { box-shadow: inset 3px 0 0 var(--bad); }"}},
		{name: "a diff file header stays in view", src: rule(".dfile__h"), has: []string{"position: sticky", "top: 0"}},
		{name: "the script adds no observers or external code", src: js,
			lacks: []string{"IntersectionObserver", "ResizeObserver", "MutationObserver", "import ", "require(", "fetch(", "XMLHttpRequest", "<script"}},

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
// It matches the selector as a whole rule head so ".sb__item" does not also
// find ".sb__item--danger".
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
