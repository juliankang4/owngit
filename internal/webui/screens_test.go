package webui

import (
	"strings"
	"testing"
)

// screen is one rendered state and the claims it must and must not make. The
// row name states the claim; the fields list what the reader must or must not
// see.
type screen struct {
	name     string
	page     Page
	lang     Lang          // language of want and absent; English when empty
	want     []MessageCode // catalog text that must appear
	absent   []MessageCode // catalog text that must not appear
	markup   []string      // literal output that must appear
	noMarkup []string      // literal output that must not appear
	merge    mergeControl
	extra    func(t *testing.T, out string)
}

// mergeControl is the expected state of a pull request's merge form.
type mergeControl int

const (
	mergeUnchecked mergeControl = iota
	mergeOffered
	mergeRefused
)

func checkScreens(t *testing.T, screens ...screen) {
	t.Helper()
	r := newRenderer(t)
	for _, s := range screens {
		t.Run(s.name, func(t *testing.T) {
			lang := s.lang
			if lang == "" {
				lang = LangEN
			}
			out := render(t, r, s.page)
			for _, code := range s.want {
				if !strings.Contains(out, wantText(lang, code)) {
					t.Errorf("missing %s: %q", code, wantText(lang, code))
				}
			}
			for _, code := range s.absent {
				if strings.Contains(out, wantText(lang, code)) {
					t.Errorf("must not show %s: %q", code, wantText(lang, code))
				}
			}
			for _, text := range s.markup {
				if !strings.Contains(out, text) {
					t.Errorf("missing %q", text)
				}
			}
			for _, text := range s.noMarkup {
				if strings.Contains(out, text) {
					t.Errorf("must not contain %q", text)
				}
			}
			switch s.merge {
			case mergeOffered:
				if strings.Contains(formNamed(t, out, "/merge"), "disabled") {
					t.Error("the merge control is disabled")
				}
			case mergeRefused:
				if !strings.Contains(formNamed(t, out, "/merge"), "disabled") {
					t.Error("the merge control is offered")
				}
			}
			if s.extra != nil {
				s.extra(t, out)
			}
		})
	}
}

// with returns page after edit, so a row can state its variation inline.
func with[P any](page P, edit func(*P)) P {
	edit(&page)
	return page
}

// inOrder checks that every marker appears, in the given order.
func inOrder(markers ...string) func(*testing.T, string) {
	return func(t *testing.T, out string) {
		t.Helper()
		last := -1
		for _, marker := range markers {
			at := strings.Index(out, marker)
			if at < 0 {
				t.Fatalf("missing %q", marker)
			}
			if at < last {
				t.Fatalf("%q is out of order", marker)
			}
			last = at
		}
	}
}

// countIs checks how many times text appears.
func countIs(text string, want int) func(*testing.T, string) {
	return func(t *testing.T, out string) {
		t.Helper()
		if n := strings.Count(out, text); n != want {
			t.Errorf("%q appears %d times, want %d", text, n, want)
		}
	}
}

// allOf runs several extra checks on one row.
func allOf(checks ...func(*testing.T, string)) func(*testing.T, string) {
	return func(t *testing.T, out string) {
		t.Helper()
		for _, check := range checks {
			check(t, out)
		}
	}
}
