package webui

import (
	"html/template"
	"strings"
	"testing"
)

// The backend decides what a policy may contain. These tests check that its
// answer survives the trip to the screen: named against a real control, in
// both languages, and never silently dropped.

func TestEveryRefusedFieldReachesAControl(t *testing.T) {
	// A field the table does not know would otherwise disappear. The mapping
	// has to cover every field the state package can refuse.
	fields := []string{
		PolicyFieldExecutor, PolicyFieldAllowedEvents,
		PolicyFieldMaxTimeoutMS, PolicyFieldMaxOutputLimitBytes,
		PolicyFieldQueueLimit, PolicyFieldMaxActiveJobs, PolicyFieldMaxLeaseMS,
		PolicyFieldContainerImage, PolicyFieldContainerRuntime, PolicyFieldContainerNetwork,
		PolicyFieldContainerCPUMillis, PolicyFieldContainerMemoryBytes,
		PolicyFieldContainerPIDs, PolicyFieldContainerScratchBytes,
		PolicyFieldSourceMaxEntries, PolicyFieldSourceMaxFileBytes, PolicyFieldSourceMaxTotalBytes,
		PolicyFieldSourceMaxPathDepth, PolicyFieldSourceMaxPathBytes,
		PolicyFieldSourceMaxNameBytes, PolicyFieldSourceMetadataLimit,
	}
	rules := []string{
		PolicyRuleRange, PolicyRuleRequired, PolicyRuleUnknown,
		PolicyRuleFormat, PolicyRuleNotApplicable, PolicyRuleDuplicate,
	}
	for _, field := range fields {
		for _, rule := range rules {
			control, code := PolicyFieldNotice(field, rule)
			if control == "" {
				t.Fatalf("%s refused by %s has no control to attach to", field, rule)
			}
			for _, lang := range Langs() {
				if Text(lang, code) == "" {
					t.Fatalf("%s refused by %s has no %s wording", field, rule, lang)
				}
			}
		}
	}
}

func TestAnUnknownRefusedFieldIsReportedNotDropped(t *testing.T) {
	// A field added to the backend and not yet mapped here must still reach
	// the operator, at form level, rather than vanish into a blank screen.
	control, code := PolicyFieldNotice("a_field_added_later", PolicyRuleRange)
	if control != "" {
		t.Fatalf("an unmapped field was attached to control %q", control)
	}
	if code != MsgCCPolicyRefused {
		t.Fatalf("an unmapped field produced %q, want the form-level refusal", code)
	}
}

func TestAMovingFloorStillShowsItsCeiling(t *testing.T) {
	// The source total's floor is another field, but its maximum is fixed. A
	// moving floor is not a reason to leave the ceiling enforced and unstated,
	// so the rendered text names the field it must exceed and still gives the
	// number it may not pass.
	ranges := map[string]FieldRange{
		"total": {MinLabel: MsgCCSrcFileBytes, Max: 4294967296, Known: true},
	}
	for _, lang := range Langs() {
		text := string(fieldRange(lang, ranges, "total"))
		if !strings.Contains(text, "4294967296") {
			t.Errorf("%s: a moving floor suppressed the ceiling: %q", lang, text)
		}
		if !strings.Contains(text, template.HTMLEscapeString(Text(lang, MsgCCSrcFileBytes))) {
			t.Errorf("%s: the floor does not name the field it depends on: %q", lang, text)
		}
	}

	// Its refusal names the relationship; every other range refusal points at
	// the range shown beside the field.
	_, total := PolicyFieldNotice(PolicyFieldSourceMaxTotalBytes, PolicyRuleRange)
	if total != MsgCCTotalBytesRange {
		t.Fatalf("the total refusal is %q, want the relationship message", total)
	}
	_, ordinary := PolicyFieldNotice(PolicyFieldSourceMaxFileBytes, PolicyRuleRange)
	if ordinary != MsgCCFieldRange {
		t.Fatalf("an ordinary range refusal is %q, want the shown-range message", ordinary)
	}
}

func TestAFieldWithNoPublishedRangeClaimsNone(t *testing.T) {
	// Silence is the honest answer when the backend publishes nothing for a
	// field. Printing a bound nothing enforces would be worse than silence.
	ranges := map[string]FieldRange{
		"known":       {Min: 1, Max: 100, Known: true},
		"not_bounded": {Known: false},
	}
	for _, lang := range Langs() {
		if text := string(fieldRange(lang, ranges, "known")); !strings.Contains(text, "100") {
			t.Errorf("%s: a published range was not rendered: %q", lang, text)
		}
		if text := fieldRange(lang, ranges, "not_bounded"); text != "" {
			t.Errorf("%s: an unbounded field claimed a range: %q", lang, text)
		}
		if text := fieldRange(lang, ranges, "absent"); text != "" {
			t.Errorf("%s: an absent field claimed a range: %q", lang, text)
		}
		if text := fieldRange(lang, nil, "known"); text != "" {
			t.Errorf("%s: a nil range table produced text: %q", lang, text)
		}
	}
}

func TestRefusalWordingDoesNotRestateABound(t *testing.T) {
	// The accepted range is printed beside each field from the backend's own
	// numbers. Repeating a number in the refusal would create a second copy
	// that a future bound change can leave disagreeing with the first.
	//
	// MsgCCTotalBytesRange is deliberately not in this list: it describes a
	// relationship to another field rather than a fixed bound, and it still
	// states no number.
	for _, code := range []MessageCode{
		MsgCCFieldRange, MsgCCFieldRequired, MsgCCFieldUnknown,
		MsgCCFieldFormat, MsgCCFieldNotApplicable, MsgCCFieldDuplicate,
		MsgCCTotalBytesRange,
	} {
		for _, lang := range Langs() {
			if text := Text(lang, code); strings.ContainsAny(text, "0123456789") {
				t.Errorf("%s/%s states a number in a refusal: %q", code, lang, text)
			}
		}
	}
}

func TestRefusalWordingSeparatesMissingFromMalformed(t *testing.T) {
	// "You left it empty" and "that is not an immutable reference" have
	// different fixes, so they do not share one sentence.
	_, missing := PolicyFieldNotice(PolicyFieldContainerImage, PolicyRuleRequired)
	_, malformed := PolicyFieldNotice(PolicyFieldContainerImage, PolicyRuleFormat)
	if missing == malformed {
		t.Fatal("an empty image and a mutable image share one message")
	}
	for _, lang := range Langs() {
		if Text(lang, missing) == Text(lang, malformed) {
			t.Fatalf("%s: the two image refusals read identically", lang)
		}
	}
}
