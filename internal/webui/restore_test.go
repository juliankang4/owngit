package webui

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// Restoring is the one screen in this package whose form leads to a write in a
// repository. These checks cover what the reader must be able to see and do
// before that write, and what the page must never imply on its own: the
// backend makes every authorization and validation decision, and the renderer
// shows only what it was given.

// formAt returns the form whose opening tag contains the given action.
func formAt(t *testing.T, out, action string) string {
	t.Helper()
	marker := `action="` + action + `"`
	idx := strings.Index(out, marker)
	if idx < 0 {
		t.Fatalf("no form posting to %s", action)
	}
	form := out[strings.LastIndex(out[:idx], "<form"):]
	if end := strings.Index(form, "</form>"); end >= 0 {
		form = form[:end]
	}
	return form
}

// ---------------------------------------------------------------------------
// the submitted contract
// ---------------------------------------------------------------------------

func TestRestoreSubmitsTheAgreedSelectionFields(t *testing.T) {
	r := newRenderer(t)
	out := render(t, r, restorePage(fullChrome(LangEN), false))

	preview := formAt(t, out, "/repositories/r1/restore/preview")
	if !strings.Contains(preview, `method="post"`) {
		t.Error("the preview request is not a POST")
	}
	for _, field := range []string{
		`name="csrf"`,
		`name="source" value="7f2c1a0bb"`,
		`name="target"`,
		`name="mode" value="all"`,
		`name="mode" value="files"`,
		`name="path" value="internal/retry/backoff.go"`,
	} {
		if !strings.Contains(preview, field) {
			t.Errorf("the preview form does not submit %s", field)
		}
	}
	// Nothing may be applied from the selection form itself.
	if strings.Contains(preview, `name="confirm"`) {
		t.Error("the selection form carries a confirmation, so previewing could write")
	}
	if strings.Contains(preview, `action="/repositories/r1/restore"`) {
		t.Error("the selection form posts to the apply endpoint")
	}
}

func TestRestoreApplyResubmitsExactlyWhatWasPreviewed(t *testing.T) {
	r := newRenderer(t)
	page := restorePage(fullChrome(LangEN), true)
	out := render(t, r, page)

	apply := formAt(t, out, "/repositories/r1/restore")
	for _, field := range []string{
		`name="csrf"`,
		`name="source" value="7f2c1a0bb"`,
		`name="target" value="main"`,
		`name="mode" value="files"`,
		`name="expected_head" value="a41c9e2ff"`,
		`name="confirm" value="restore"`,
	} {
		if !strings.Contains(apply, field) {
			t.Errorf("the apply form does not submit %s", field)
		}
	}

	// Only the reviewed paths are resubmitted. An unticked path must not
	// arrive with the confirmed request.
	for _, picked := range []string{"internal/retry/backoff.go", "internal/retry/limits.go"} {
		if !strings.Contains(apply, `name="path" value="`+picked+`"`) {
			t.Errorf("the apply form dropped the reviewed path %q", picked)
		}
	}
	if strings.Contains(apply, `name="path" value="internal/retry/legacy.go"`) {
		t.Error("the apply form submits a path the reader did not select")
	}
}

func TestRestoreWholeProjectDoesNotSubmitAPathList(t *testing.T) {
	// In whole-project mode the paths are not the selection; the commit is.
	// Sending a list anyway would let a stale tick narrow a restore the reader
	// asked to be complete.
	r := newRenderer(t)
	page := restorePage(fullChrome(LangEN), true)
	page.Mode = RestoreModeAll
	out := render(t, r, page)

	apply := formAt(t, out, "/repositories/r1/restore")
	if !strings.Contains(apply, `name="mode" value="all"`) {
		t.Fatal("the apply form lost the whole-project mode")
	}
	if strings.Contains(apply, `name="path"`) {
		t.Error("a whole-project restore submits a path list")
	}
}

func TestRestoreExpectedHeadIsCarriedForAnAbsentBranch(t *testing.T) {
	// Recreating a deleted branch is a legitimate restore. The backend sends
	// the zero OID as the observed tip, and the page must submit that value
	// rather than an empty field, so "the branch was absent" and "the field
	// was not filled in" stay different requests.
	const zero = "0000000000000000000000000000000000000000"
	r := newRenderer(t)
	page := restorePage(fullChrome(LangEN), true)
	page.TargetBranch = "recovered/main"
	page.ExpectedHead = zero
	out := render(t, r, page)

	apply := formAt(t, out, "/repositories/r1/restore")
	if !strings.Contains(apply, `name="expected_head" value="`+zero+`"`) {
		t.Error("the observed absent tip was not submitted")
	}
	if !strings.Contains(apply, `name="target" value="recovered/main"`) {
		t.Error("the recreated branch is not the submitted target")
	}
}

// ---------------------------------------------------------------------------
// two deliberate steps
// ---------------------------------------------------------------------------

func TestRestoreHasNoApplyControlBeforeAPreview(t *testing.T) {
	r := newRenderer(t)
	out := render(t, r, restorePage(fullChrome(LangEN), false))

	if strings.Contains(out, `action="/repositories/r1/restore"`) {
		t.Error("the apply endpoint is reachable before anything was previewed")
	}
	if strings.Contains(out, `name="confirm"`) {
		t.Error("a confirmation control exists before there is a result to confirm")
	}
	if strings.Contains(out, wantText(LangEN, MsgRestoreConfirmLabel)) {
		t.Error("a restore confirmation is offered before the changes were shown")
	}
	if strings.Contains(out, wantText(LangEN, MsgRestorePreviewTitle)) {
		t.Error("a changes section is shown before anything was previewed")
	}
	if !strings.Contains(out, wantText(LangEN, MsgRestorePreviewSubmit)) {
		t.Error("the first step does not offer a preview")
	}
}

func TestRestorePreviewOffersNoControlThatCanContradictIt(t *testing.T) {
	// Regression. The selection form used to stay editable beside the preview
	// while the apply form carried the previewed values in hidden fields. A
	// reader could switch the branch picker to release, see release selected,
	// confirm, and have the write go to main. The editable controls are absent
	// once there is something to confirm, so the two cannot disagree.
	r := newRenderer(t)
	out := render(t, r, restorePage(fullChrome(LangEN), true))

	for _, control := range []string{
		`name="target"`,
		`name="mode"`,
		`name="path"`,
		`name="source"`,
	} {
		// The apply form still submits these, but only as hidden fields the
		// reader cannot change.
		for _, editable := range []string{
			`type="text" ` + control,
			`type="radio" ` + control,
			`type="checkbox" ` + control,
		} {
			if strings.Contains(out, editable) {
				t.Errorf("a previewed page still offers an editable %s", control)
			}
		}
	}
	// The typed target control and its suggestions belong to step one only.
	for _, gone := range []string{`id="restore-target"`, "<datalist", "<select"} {
		if strings.Contains(out, gone) {
			t.Errorf("a previewed page still renders %s", gone)
		}
	}
	// Nothing may post to the preview route from here either, or the reader
	// would have two live forms describing different selections.
	if strings.Contains(out, `action="/repositories/r1/restore/preview"`) {
		t.Error("the selection form is still live beside the preview")
	}
	// Apart from the toolbar's search field, the apply form is the only one.
	body := out[strings.Index(out, `id="main"`):]
	if got := strings.Count(body, "<form"); got != 1 {
		t.Errorf("a previewed page has %d forms in its body, want only the apply form", got)
	}
}

func TestRestoreChangingTheSelectionReturnsToStepOne(t *testing.T) {
	// Changing the choice has to be possible without scripting, and it must
	// land on a page that requires a fresh preview rather than one that can
	// apply the old result.
	r := newRenderer(t)
	page := restorePage(fullChrome(LangEN), true)
	out := render(t, r, page)

	if !strings.Contains(out, wantText(LangEN, MsgRestoreChangeChoice)) {
		t.Fatal("a previewed page offers no way back to the selection")
	}
	back := restoreSelectionURL(page)
	// Attribute values are escaped on the way out, so compare against the
	// rendered form rather than the raw URL.
	if !strings.Contains(out, `href="`+attrEscape(back)+`"`) {
		t.Fatalf("the change link does not point at the selection: %q", back)
	}
	// It is a GET on the restore page itself, not the POST-only preview route.
	if strings.Contains(back, "/restore/preview") {
		t.Error("the change link points at a route that only accepts POST")
	}
	if !strings.HasPrefix(back, "/repositories/r1/restore?") {
		t.Errorf("the change link is not the canonical restore address: %q", back)
	}
	// The choices survive the trip. Paths are percent-encoded in the query.
	for _, keep := range []string{"source=7f2c1a0bb", "target=main", "mode=files"} {
		if !strings.Contains(back, keep) {
			t.Errorf("the change link drops %q", keep)
		}
	}
	for _, keep := range []string{"backoff.go", "limits.go"} {
		if !strings.Contains(back, keep) {
			t.Errorf("the change link drops the chosen file %q", keep)
		}
	}
	// A path separator must be encoded, not left to be read as part of the
	// address.
	if strings.Contains(back, "path=internal/") {
		t.Error("a chosen path is not encoded in the change link")
	}
	if strings.Contains(back, "legacy.go") {
		t.Error("the change link carries a file the reader did not choose")
	}
	// Returning there must not carry the preview, so nothing can be applied
	// without looking at the changes again.
	if strings.Contains(back, "expected_head") || strings.Contains(back, "confirm") {
		t.Error("the change link carries enough state to skip the preview")
	}
}

func TestRestoreNeedsAnExplicitConfirmationTick(t *testing.T) {
	// One click must not restore. The reader ticks a checkbox that states what
	// will happen, and the backend requires the same value again.
	r := newRenderer(t)
	out := render(t, r, restorePage(fullChrome(LangEN), true))

	apply := formAt(t, out, "/repositories/r1/restore")
	idx := strings.Index(apply, `name="confirm"`)
	if idx < 0 {
		t.Fatal("the apply form has no confirmation control")
	}
	field := apply[strings.LastIndex(apply[:idx], "<input"):]
	if end := strings.Index(field, ">"); end >= 0 {
		field = field[:end]
	}
	if !strings.Contains(field, `type="checkbox"`) {
		t.Error("the confirmation is not a control the reader has to set")
	}
	if !strings.Contains(field, "required") {
		t.Error("the form can be submitted without the confirmation")
	}
	if !strings.Contains(apply, wantText(LangEN, MsgRestoreConfirmLabel)) {
		t.Error("the confirmation does not say what is being confirmed")
	}
	if !strings.Contains(apply, wantText(LangEN, MsgRestoreConfirmHelp)) {
		t.Error("the confirmation does not say that existing history is kept")
	}
}

func TestRestoreCannotBeAppliedWhenTheBackendSaysSo(t *testing.T) {
	// CanApply is the backend's decision. A disabled control is a courtesy,
	// not the protection: the reason must also be readable.
	r := newRenderer(t)
	c := fullChrome(LangEN)
	c.Notices = []Notice{{Kind: NoticeWarning, Code: MsgRestorePreviewStale}}
	page := restorePage(c, true)
	page.CanApply = false
	out := render(t, r, page)

	apply := formAt(t, out, "/repositories/r1/restore")
	if strings.Count(apply, "disabled") < 2 {
		t.Error("the confirmation and the restore button are not both disabled")
	}
	if !strings.Contains(out, wantText(LangEN, MsgRestorePreviewStale)) {
		t.Error("the page does not say why restoring is unavailable")
	}
}

// ---------------------------------------------------------------------------
// deletions are never inferred
// ---------------------------------------------------------------------------

func TestRestoreSelectionListsDeletionsAsTheirOwnRows(t *testing.T) {
	r := newRenderer(t)
	out := render(t, r, restorePage(fullChrome(LangEN), false))

	if !strings.Contains(out, "internal/retry/legacy.go") {
		t.Fatal("a path that restoring would delete is missing from the list")
	}
	if !strings.Contains(out, wantText(LangEN, MsgRestoreStatusDeleted)) {
		t.Error("a deletion is not labelled as a deletion in the selection list")
	}
	for _, code := range []MessageCode{MsgRestoreStatusAdded, MsgRestoreStatusModified} {
		if !strings.Contains(out, wantText(LangEN, code)) {
			t.Errorf("the selection list does not state the %q outcome", code)
		}
	}
}

func TestRestorePreviewNamesDeletedFilesSeparately(t *testing.T) {
	r := newRenderer(t)
	out := render(t, r, restorePage(fullChrome(LangEN), true))

	if !strings.Contains(out, wantText(LangEN, MsgRestoreDeletesLabel)) {
		t.Fatal("the preview does not gather the deletions")
	}
	deletes := out[strings.Index(out, `class="restore__deletes"`):]
	if end := strings.Index(deletes, "</div>"); end >= 0 {
		deletes = deletes[:end]
	}
	if !strings.Contains(deletes, "internal/retry/legacy.go") {
		t.Error("the deleted file is not named in the deletion list")
	}
	if strings.Contains(deletes, "internal/retry/limits.go") {
		t.Error("an added file was listed as a deletion")
	}
}

func TestRestorePreviewShowsEveryChangedPathIncludingUntouchedText(t *testing.T) {
	// A file whose text diff was not loaded still has to appear as a changed
	// path, otherwise the list would understate what the commit does.
	r := newRenderer(t)
	out := render(t, r, restorePage(fullChrome(LangEN), true))
	for _, path := range []string{
		"internal/retry/backoff.go",
		"internal/retry/limits.go",
		"internal/retry/legacy.go",
	} {
		if !strings.Contains(out, path) {
			t.Errorf("changed path %q is missing from the preview", path)
		}
	}
}

func TestRestoreTruncatedDiffSaysWhatIsIncomplete(t *testing.T) {
	// The cut is in the line by line view, never in the path list, and the
	// sentence has to say which one, or an incomplete screen would read as a
	// fully reviewed one.
	r := newRenderer(t)
	page := restorePage(fullChrome(LangEN), true)
	page.DiffTruncated = true
	out := render(t, r, page)

	if !strings.Contains(out, wantText(LangEN, MsgRestoreDiffTruncated)) {
		t.Fatal("a truncated diff is not reported")
	}
	notice := Text(LangEN, MsgRestoreDiffTruncated)
	if !strings.Contains(notice, "list of changed files is complete") {
		t.Errorf("the truncation notice does not distinguish the two lists: %q", notice)
	}
	for _, path := range []string{"internal/retry/limits.go", "internal/retry/legacy.go"} {
		if !strings.Contains(out, path) {
			t.Errorf("%q disappeared from a truncated preview", path)
		}
	}
}

// ---------------------------------------------------------------------------
// outcomes the backend reports
// ---------------------------------------------------------------------------

func TestEveryRestoreOutcomeReachesTheScreenInBothLanguages(t *testing.T) {
	r := newRenderer(t)
	outcomes := []struct {
		code MessageCode
		kind NoticeKind
	}{
		{MsgRestoreInvalid, NoticeError},
		{MsgRestoreConflict, NoticeError},
		{MsgRestoreNoChanges, NoticeInfo},
		{MsgRestoreUnsupported, NoticeError},
		{MsgRestoreFailed, NoticeError},
		{MsgRestoreReady, NoticeInfo},
		{MsgRestoreSuccess, NoticeSuccess},
	}
	for _, outcome := range outcomes {
		if !Has(outcome.code) {
			t.Errorf("the shared outcome %q is not in the catalog", outcome.code)
			continue
		}
		for _, lang := range Langs() {
			c := fullChrome(lang)
			c.Notices = []Notice{{Kind: outcome.kind, Code: outcome.code}}
			out := render(t, r, restorePage(c, true))
			if !strings.Contains(out, wantText(lang, outcome.code)) {
				t.Errorf("%s (%s): the outcome text is not rendered", outcome.code, lang)
			}
			if outcome.kind == NoticeError && !strings.Contains(out, `role="alert"`) {
				t.Errorf("%s: a failure is not announced", outcome.code)
			}
		}
	}
}

func TestRestoreUncertainOutcomesDoNotAssertAState(t *testing.T) {
	// A failure is reported when publishing the change failed and reading the
	// branch back failed too. In that case the backend does not know whether
	// the branch moved, so the message must not promise it was left alone. It
	// asks the reader to look instead.
	for _, lang := range Langs() {
		failed := Text(lang, MsgRestoreFailed)
		for _, promise := range []string{
			"left as it was", "was not changed", "nothing was restored",
			"\uadf8\ub300\ub85c \ub450\uc5c8", "\ubc14\ub00c\uc9c0 \uc54a", "\uc544\ubb34\uac83\ub3c4 \ub418\ub3cc\ub9ac\uc9c0",
		} {
			if strings.Contains(failed, promise) {
				t.Errorf("%s: the failure claims an outcome nothing verified: %q", lang, failed)
			}
		}
	}
	if en := Text(LangEN, MsgRestoreFailed); !strings.Contains(en, "Check the branch") {
		t.Errorf("the failure does not tell the reader to check the branch: %q", en)
	}

	// A conflict means the branch no longer matches what was previewed. It can
	// have received a commit, been deleted, been recreated elsewhere, or moved
	// back to an older commit, so the message must not name just one of those.
	for _, lang := range Langs() {
		conflict := Text(lang, MsgRestoreConflict)
		for _, guess := range []string{
			"new commit", "received a", "\uc0c8 \ucee4\ubc0b", "\ub4e4\uc5b4\uc640",
		} {
			if strings.Contains(conflict, guess) {
				t.Errorf("%s: the conflict names one cause among several: %q", lang, conflict)
			}
		}
		if !strings.Contains(conflict, "changed") && !strings.Contains(conflict, "\ubc14\ub00c\uc5c8") {
			t.Errorf("%s: the conflict does not say the branch changed: %q", lang, conflict)
		}
	}
}

func TestRestorePreviewErrorsAreAnnouncedWithoutAControl(t *testing.T) {
	// A field notice is normally announced through the input it describes,
	// which carries aria-describedby. The previewed page has no inputs, so a
	// scoped error there was visible and silent: a stale branch reported
	// against "target" rendered as an ordinary paragraph with no role, and
	// nothing told a reader who was not looking at that corner why the restore
	// had become unavailable.
	r := newRenderer(t)
	c := fullChrome(LangEN)
	c.Notices = []Notice{Error("target", MsgRestoreConflict)}
	page := restorePage(c, true)
	page.CanApply = false
	out := render(t, r, page)

	if !strings.Contains(out, wantText(LangEN, MsgRestoreConflict)) {
		t.Fatal("the conflict is not shown at all")
	}
	note := out[strings.Index(out, `id="`+noteID(nil, "target")+`"`):]
	note = note[:strings.Index(note, ">")]
	if !strings.Contains(note, `role="alert"`) {
		t.Errorf("the scoped error is not announced: %s", note)
	}
	if got := strings.Count(out, `role="alert"`); got != 1 {
		t.Errorf("a previewed page with one error has %d alerts, want 1", got)
	}

	// Every scoped field on this summary behaves the same way.
	for _, field := range []string{"target", "mode", "path"} {
		for _, lang := range Langs() {
			c := fullChrome(lang)
			c.Notices = []Notice{Error(field, MsgRestoreConflict)}
			page := restorePage(c, true)
			page.CanApply = false
			out := render(t, r, page)
			if !strings.Contains(out, wantText(lang, MsgRestoreConflict)) {
				t.Errorf("%s (%s): the error text is missing", field, lang)
				continue
			}
			if !strings.Contains(out, `role="alert"`) {
				t.Errorf("%s (%s): the error is not announced", field, lang)
			}
		}
	}

	// A non-error outcome is announced politely rather than as an alert.
	info := fullChrome(LangEN)
	info.Notices = []Notice{{Kind: NoticeInfo, Field: "target", Code: MsgRestoreNoChanges}}
	polite := render(t, r, restorePage(info, true))
	if strings.Contains(polite, `role="alert"`) {
		t.Error("an informational outcome interrupts as an alert")
	}
	if !strings.Contains(polite, `role="status"`) {
		t.Error("an informational outcome is not announced at all")
	}
}

// alertTag returns the opening tag of the read-only notice for a field.
func alertTag(t *testing.T, out, field string) string {
	t.Helper()
	idx := strings.Index(out, `id="`+noteID(nil, field)+`"`)
	if idx < 0 {
		t.Fatalf("the page has no notice for %q", field)
	}
	tag := out[strings.LastIndex(out[:idx], "<"):]
	if end := strings.Index(tag, ">"); end >= 0 {
		tag = tag[:end]
	}
	return tag
}

func TestRestoreRefusalPutsFocusOnTheBlockingError(t *testing.T) {
	// A refused apply answers with a whole new document, so the browser starts
	// the reader at the top of it. Observed on the real build: after a 409 the
	// focus was on a header link, and moving on reached a diff container, while
	// the message explaining why the restore was blocked sat further down the
	// summary. role="alert" is a live region and says nothing about where the
	// reader is placed on a fresh page, so the error also has to be a focus
	// target the response hands the reader.
	r := newRenderer(t)

	for _, lang := range Langs() {
		c := fullChrome(lang)
		c.Notices = []Notice{Error("target", MsgRestoreConflict)}
		page := restorePage(c, true)
		page.CanApply = false
		out := render(t, r, page)

		tag := alertTag(t, out, "target")
		if !strings.Contains(tag, `tabindex="-1"`) {
			t.Errorf("%s: the blocking error cannot receive focus: %s", lang, tag)
		}
		if !strings.Contains(tag, "autofocus") {
			t.Errorf("%s: the response does not place the reader on the blocking error: %s", lang, tag)
		}
		if !strings.Contains(tag, `role="alert"`) {
			t.Errorf("%s: the blocking error lost its announcement: %s", lang, tag)
		}
		if !strings.Contains(out, wantText(lang, MsgRestoreConflict)) {
			t.Errorf("%s: the conflict text is missing", lang)
		}
	}

	// One refusal is one destination. Several errors on one summary must not
	// each claim the reader, because the browser honours only the first and the
	// markup would no longer say which one it is.
	many := fullChrome(LangEN)
	many.Notices = []Notice{
		Error("target", MsgRestoreConflict),
		Error("mode", MsgRestoreInvalid),
		Error("path", MsgRestoreUnsupported),
	}
	crowded := restorePage(many, true)
	crowded.CanApply = false
	out := render(t, r, crowded)
	if got := strings.Count(out, "autofocus"); got != 1 {
		t.Errorf("a summary with three errors has %d focus targets, want 1", got)
	}
	if !strings.Contains(alertTag(t, out, "target"), "autofocus") {
		t.Error("the first reported error is not the one the reader is placed on")
	}
	// Each error is still announced and can still be reached programmatically
	// or by an assistive technology's own navigation. tabindex="-1" is
	// deliberately not in the Tab order: these are messages, not controls.
	for _, field := range []string{"target", "mode", "path"} {
		tag := alertTag(t, out, field)
		if !strings.Contains(tag, `role="alert"`) || !strings.Contains(tag, `tabindex="-1"`) {
			t.Errorf("the %q error is not announced and focusable: %s", field, tag)
		}
		if strings.Contains(tag, `tabindex="0"`) {
			t.Errorf("the %q message was put in the Tab order: %s", field, tag)
		}
	}

	// An outcome that blocks nothing must not steal the reader's position. A
	// successful preview is the ordinary case and starts at the top of the
	// page, as any navigation does.
	info := fullChrome(LangEN)
	info.Notices = []Notice{{Kind: NoticeInfo, Field: "target", Code: MsgRestoreNoChanges}}
	polite := render(t, r, restorePage(info, true))
	if strings.Contains(polite, "autofocus") {
		t.Error("an informational outcome moves the reader")
	}
	if !strings.Contains(alertTag(t, polite, "target"), `role="status"`) {
		t.Error("an informational outcome is no longer announced politely")
	}

	// And a page with nothing to report leaves focus alone entirely.
	if out := render(t, r, restorePage(fullChrome(LangEN), true)); strings.Contains(out, "autofocus") {
		t.Error("a page with no error still moves the reader")
	}

	// HTML allows one autofocus per scoping root, which is this document.
	// Whatever the backend reports, and at whichever step, the page must never
	// render a second one, and every message must still be rendered.
	//
	// One field carrying two errors is the case a field-name selector gets
	// wrong: the name matches both notices, so both claim the reader. A field
	// mixing an informational notice with an error is the other, in both
	// orders, because only the error may be the destination.
	combinations := []struct {
		name    string
		notices []Notice
		// want is the message the reader must be placed on, if any.
		want MessageCode
	}{
		{"conflict", []Notice{Error("target", MsgRestoreConflict)}, MsgRestoreConflict},
		{"every field", []Notice{Error("target", MsgRestoreConflict), Error("mode", MsgRestoreInvalid), Error("path", MsgRestoreUnsupported)}, MsgRestoreConflict},
		{"page and field", []Notice{Error("", MsgRestoreFailed), Error("target", MsgRestoreConflict)}, MsgRestoreConflict},
		{"scope only", []Notice{Error("mode", MsgRestoreInvalid)}, MsgRestoreInvalid},
		{"paths only", []Notice{Error("path", MsgRestoreUnsupported)}, MsgRestoreUnsupported},
		{"one field twice", []Notice{Error("target", MsgRestoreConflict), Error("target", MsgRestoreInvalid)}, MsgRestoreConflict},
		{"info before error", []Notice{{Kind: NoticeInfo, Field: "target", Code: MsgRestoreNoChanges}, Error("target", MsgRestoreConflict)}, MsgRestoreConflict},
		{"error before info", []Notice{Error("target", MsgRestoreConflict), {Kind: NoticeInfo, Field: "target", Code: MsgRestoreNoChanges}}, MsgRestoreConflict},
		{"two fields twice each", []Notice{
			Error("mode", MsgRestoreInvalid), Error("mode", MsgRestoreUnsupported),
			Error("target", MsgRestoreConflict), Error("target", MsgRestoreFailed)}, MsgRestoreConflict},
		// The destination follows the order the summary renders, not the order
		// the backend happened to report.
		{"later field reported first", []Notice{Error("path", MsgRestoreUnsupported), Error("target", MsgRestoreConflict)}, MsgRestoreConflict},
	}
	for _, lang := range Langs() {
		for _, combo := range combinations {
			for _, previewed := range []bool{false, true} {
				c := fullChrome(lang)
				c.Notices = combo.notices
				out := render(t, r, restorePage(c, previewed))

				if got := strings.Count(out, "autofocus"); got > 1 {
					t.Errorf("%s/%s (previewed=%v): %d autofocus targets in one document", lang, combo.name, previewed, got)
				}
				// No message is dropped to keep the count at one.
				for _, notice := range combo.notices {
					if !strings.Contains(out, wantText(lang, notice.Code)) {
						t.Errorf("%s/%s (previewed=%v): the %q message is missing", lang, combo.name, previewed, notice.Code)
					}
				}

				if !previewed {
					// Step one has its own inputs, which carry the message
					// through aria-describedby, so it must not move the reader.
					if strings.Contains(out, "autofocus") {
						t.Errorf("%s/%s: the editable form moves the reader instead of using its inputs", lang, combo.name)
					}
					continue
				}
				// And the one target is the intended error, not merely some
				// notice on the right field.
				if got := focusedMessage(t, out, lang); got != combo.want {
					t.Errorf("%s/%s: the reader is placed on the %q message, want %q",
						lang, combo.name, got, combo.want)
				}
			}
		}
	}
}

// focusedMessage returns the code of the notice carrying autofocus. Every
// notice is rendered in both languages so the in-place switch can reach it, so
// the match is made in the page's own language.
func focusedMessage(t *testing.T, out string, lang Lang) MessageCode {
	t.Helper()
	idx := strings.Index(out, "autofocus")
	if idx < 0 {
		t.Fatal("no notice claims the reader")
	}
	paragraph := out[idx:]
	if end := strings.Index(paragraph, "</p>"); end >= 0 {
		paragraph = paragraph[:end]
	}
	found := MessageCode("")
	for _, code := range []MessageCode{
		MsgRestoreConflict, MsgRestoreInvalid, MsgRestoreUnsupported,
		MsgRestoreFailed, MsgRestoreNoChanges,
	} {
		if !strings.Contains(paragraph, wantText(lang, code)) {
			continue
		}
		if found != "" {
			t.Fatalf("the focused notice matched both %q and %q", found, code)
		}
		found = code
	}
	if found == "" {
		t.Fatalf("the focused notice carries no known message: %s", paragraph)
	}
	return found
}

func TestRestoreSelectionErrorsStayWithTheirInput(t *testing.T) {
	// Step one does have inputs, so its notices are announced through them.
	// Giving those a role as well would read the same message twice.
	r := newRenderer(t)
	c := fullChrome(LangEN)
	c.Notices = []Notice{Error("target", MsgRestoreInvalid)}
	out := render(t, r, restorePage(c, false))

	if !strings.Contains(out, `aria-describedby="`+noteID(nil, "target")+`"`) {
		t.Error("the control does not point at its error")
	}
	if strings.Contains(out, `role="alert"`) {
		t.Error("the error is announced twice: once by the control and once by itself")
	}
}

func TestFieldNoticesElsewhereAreUnchanged(t *testing.T) {
	// The announcement was added for summaries with no control. Ordinary forms
	// must keep describing their errors through the input, so this change
	// cannot start duplicating announcements across the rest of the interface.
	r := newRenderer(t)
	c := fullChrome(LangEN)
	c.Notices = []Notice{Error("name", MsgRepoNameTaken)}
	out := render(t, r, NewRepositoryPage{Chrome: c, SubmitURL: "/repositories"})

	if !strings.Contains(out, `aria-describedby="`+noteID(nil, "name")+`"`) {
		t.Error("an ordinary form no longer links its error to the input")
	}
	if strings.Contains(out, `role="alert"`) {
		t.Error("an ordinary form's field error is now announced twice")
	}
}

func TestRestoreFieldErrorsAreAttachedToTheirControls(t *testing.T) {
	r := newRenderer(t)
	cases := []struct {
		field string
		code  MessageCode
	}{
		{"target", MsgRestoreTargetEmpty},
		{"path", MsgRestoreFilesNone},
		{"mode", MsgRestoreInvalid},
		{"confirm", MsgRestoreConflict},
	}
	for _, tc := range cases {
		for _, lang := range Langs() {
			c := fullChrome(lang)
			c.Notices = []Notice{Error(tc.field, tc.code)}
			out := render(t, r, restorePage(c, true))
			if !strings.Contains(out, wantText(lang, tc.code)) {
				t.Errorf("%s (%s): the error text is not rendered", tc.field, lang)
			}
			wantID := noteID(nil, tc.field)
			if !strings.Contains(out, `id="`+wantID+`"`) {
				t.Errorf("%s (%s): the error has no note element", tc.field, lang)
			}
		}
	}
}

func TestRestoreNoChangeResultDoesNotLookLikeWork(t *testing.T) {
	// A preview with nothing to do must say so where the changes would be,
	// rather than showing an empty area next to an enabled restore button.
	r := newRenderer(t)
	c := fullChrome(LangEN)
	c.Notices = []Notice{Info(MsgRestoreNoChanges)}
	page := restorePage(c, true)
	page.Changes = nil
	page.CanApply = false
	out := render(t, r, page)

	if !strings.Contains(out, wantText(LangEN, MsgRestoreNoChanges)) {
		t.Fatal("an empty preview does not explain itself")
	}
	apply := formAt(t, out, "/repositories/r1/restore")
	if !strings.Contains(apply, "disabled") {
		t.Error("a restore with no changes is still offered as an action")
	}
}

// ---------------------------------------------------------------------------
// the target branch is never silently changed
// ---------------------------------------------------------------------------

// targetInput returns the target control's opening tag.
func targetInput(t *testing.T, out string) string {
	t.Helper()
	idx := strings.Index(out, `id="restore-target"`)
	if idx < 0 {
		t.Fatal("the page has no target control")
	}
	tag := out[strings.LastIndex(out[:idx], "<"):]
	if end := strings.Index(tag, ">"); end >= 0 {
		tag = tag[:end]
	}
	return tag
}

func TestRestoreTargetCanBeATypedName(t *testing.T) {
	// Recovering deleted work often means recreating a branch that is gone. A
	// control offering only existing names makes that impossible, so the
	// target is typed and the existing names are suggestions.
	r := newRenderer(t)
	out := render(t, r, restorePage(fullChrome(LangEN), false))

	tag := targetInput(t, out)
	if !strings.Contains(tag, `type="text"`) {
		t.Errorf("the target cannot be typed: %s", tag)
	}
	if !strings.Contains(tag, `name="target"`) {
		t.Errorf("the target control does not submit the agreed field: %s", tag)
	}
	if !strings.Contains(tag, "required") {
		t.Errorf("the form can be submitted with no target at all: %s", tag)
	}
	// Existing branches are offered without limiting what can be entered.
	if !strings.Contains(tag, `list="restore-branches"`) {
		t.Errorf("the existing branches are not offered as suggestions: %s", tag)
	}
	if !strings.Contains(out, `<datalist id="restore-branches">`) {
		t.Fatal("the suggestion list is missing")
	}
	list := out[strings.Index(out, `<datalist id="restore-branches">`):]
	if end := strings.Index(list, "</datalist>"); end >= 0 {
		list = list[:end]
	}
	for _, branch := range []string{"main", "fix/cursor"} {
		if !strings.Contains(list, `value="`+branch+`"`) {
			t.Errorf("the existing branch %q is not suggested", branch)
		}
	}
	// A datalist suggests; it must not be a select that constrains.
	if strings.Contains(out, `<select id="restore-target"`) {
		t.Error("the target is still a fixed list of existing names")
	}

	// Both outcomes are explained before the reader commits to either.
	for _, lang := range Langs() {
		out := render(t, r, restorePage(fullChrome(lang), false))
		if !strings.Contains(out, wantText(lang, MsgRestoreTargetChoose)) {
			t.Errorf("%s: the page does not explain existing versus new branches", lang)
		}
	}
	// The two targets do different things, and the help has to distinguish
	// them. An existing branch keeps its own history and the restore commit
	// sits on top of it; a new branch starts from the selected commit.
	choose := Text(LangEN, MsgRestoreTargetChoose)
	for _, phrase := range []string{"existing branch", "new name", "continues from the selected commit"} {
		if !strings.Contains(choose, phrase) {
			t.Errorf("the target help no longer covers %q: %q", phrase, choose)
		}
	}
	// Restoring changes and deletes files on purpose. Only the history is
	// preserved, so the help must not promise the working tree is untouched.
	for _, lang := range Langs() {
		text := Text(lang, MsgRestoreTargetChoose)
		for _, false_ := range []string{"removed or rewritten", "nothing already in the repository", "\uc9c0\uc6b0\uac70\ub098 \uace0\uce58\uc9c0 \uc54a"} {
			if strings.Contains(text, false_) {
				t.Errorf("%s: the target help claims files are untouched: %q", lang, text)
			}
		}
	}
	// The help describes the branch choice, so it must not attach the restore
	// commit to the selected commit in the existing-branch case.
	ko := Text(LangKO, MsgRestoreTargetChoose)
	if !strings.Contains(ko, "\ud604\uc7ac \uae30\ub85d \ub4a4\uc5d0") {
		t.Errorf("the Korean help does not say an existing branch continues from its own tip: %q", ko)
	}
}

func TestRestoreTargetIsPrefilledWithTheCurrentChoice(t *testing.T) {
	r := newRenderer(t)

	// An existing branch is filled in and not described as a new one.
	out := render(t, r, restorePage(fullChrome(LangEN), false))
	if tag := targetInput(t, out); !strings.Contains(tag, `value="main"`) {
		t.Errorf("the current target is not prefilled: %s", tag)
	}
	if strings.Contains(out, wantText(LangEN, MsgRestoreTargetNew)) {
		t.Error("an existing branch is described as one that would be created")
	}

	// A name that does not exist is kept exactly as typed, and the preview it
	// leads to says what will happen to it. The claim is made there rather
	// than beside the field, because the field can still be changed.
	page := restorePage(fullChrome(LangEN), false)
	page.TargetBranch = "recovered/9a8b154"
	page.CreatesBranch = true
	typed := render(t, r, page)
	if tag := targetInput(t, typed); !strings.Contains(tag, `value="recovered/9a8b154"`) {
		t.Errorf("a typed branch name was not preserved: %s", tag)
	}
	page.Previewed = true
	if !strings.Contains(render(t, r, page), wantText(LangEN, MsgRestoreTargetNew)) {
		t.Error("the preview does not say the branch would be created")
	}
	// This notice covers any name that is not currently a branch, including a
	// suggested one that never existed. It says the branch is created; saying
	// it is recreated would claim the original name was identified.
	for _, lang := range Langs() {
		text := Text(lang, MsgRestoreTargetNew)
		if !strings.Contains(text, "does not exist") && !strings.Contains(text, "\uc544\uc9c1 \uc5c6\ub294") {
			t.Errorf("%s: the notice no longer says the branch is absent: %q", lang, text)
		}
		for _, claim := range []string{"recreates", "\ub2e4\uc2dc \ub9cc\ub4ed\ub2c8\ub2e4"} {
			if strings.Contains(text, claim) {
				t.Errorf("%s: the notice claims the branch existed before: %q", lang, text)
			}
		}
	}
}

func TestRestoreNewBranchCopyFollowsTheBackendNotTheSuggestions(t *testing.T) {
	// Whether restoring creates the branch is observed by the backend together
	// with the tip the preview was computed against. The suggestion list is
	// read separately and can be out of date, so deriving the answer from it
	// would let the page describe a different plan than the one that will run.
	//
	// Both directions of that disagreement are checked.
	r := newRenderer(t)

	// A name absent from the suggestions, but the branch exists: it was
	// created after the list was read. Nothing is being created here.
	existing := restorePage(fullChrome(LangEN), true)
	existing.TargetBranch = "added/after-the-list"
	existing.CreatesBranch = false
	if out := render(t, r, existing); strings.Contains(out, wantText(LangEN, MsgRestoreTargetNew)) {
		t.Error("a branch the backend found is described as one that would be created")
	}

	// A name that is in the suggestions, but the branch is gone: it was
	// deleted after the list was read. Restoring creates it.
	gone := restorePage(fullChrome(LangEN), true)
	gone.TargetBranch = "main"
	gone.CreatesBranch = true
	if !strings.Contains(render(t, r, gone), wantText(LangEN, MsgRestoreTargetNew)) {
		t.Error("a branch the backend did not find is not flagged as one that would be created")
	}

	// It stays a suggestion either way; the list does not decide anything, and
	// it does not change with the branch's state.
	selecting := restorePage(fullChrome(LangEN), false)
	selecting.TargetBranch = "main"
	selecting.CreatesBranch = true
	if out := render(t, r, selecting); !strings.Contains(out, `<option value="main">`) {
		t.Error("the suggestion list changed because of the branch's state")
	}
}

func TestRestoreSelectionStageMakesNoClaimTheReaderCanInvalidate(t *testing.T) {
	// Observed in real use (A11Y-UI-01): the screen was opened with a target
	// that did not exist, so the backend reported CreatesBranch and step one
	// rendered "this branch will be created" beside the field. The reader then
	// picked the existing "release" from the suggestion list. The field is
	// editable and the server is not asked again until the preview, so the
	// notice stayed on screen next to a branch it was no longer true of.
	//
	// The fix is not to watch the field. A claim about what the write will do
	// belongs to the preview, which is computed from one observation and cannot
	// be edited afterwards. Step one therefore states no per-branch outcome at
	// all, which is checked here by rendering both observations and requiring
	// the selection stage to be identical: if nothing varies with the
	// observation, nothing can go stale when the reader types over it.
	r := newRenderer(t)

	for _, lang := range Langs() {
		missing := restorePage(fullChrome(lang), false)
		missing.TargetBranch = "rel"
		missing.CreatesBranch = true

		same := restorePage(fullChrome(lang), false)
		same.TargetBranch = "rel"
		same.CreatesBranch = false

		stale := formAt(t, render(t, r, missing), "/repositories/r1/restore/preview")
		if strings.Contains(stale, wantText(lang, MsgRestoreTargetNew)) {
			t.Errorf("%s: the editable selection claims an outcome that the next keystroke can falsify", lang)
		}
		if fresh := formAt(t, render(t, r, same), "/repositories/r1/restore/preview"); stale != fresh {
			t.Errorf("%s: the selection stage varies with the branch observation, so it can disagree with the field", lang)
		}

		// Removing the per-branch notice must not remove the explanation. The
		// generic help describes both outcomes and is true whatever is typed.
		if !strings.Contains(stale, wantText(lang, MsgRestoreTargetChoose)) {
			t.Errorf("%s: step one no longer explains existing versus new branches", lang)
		}
		if !strings.Contains(stale, wantText(lang, MsgRestoreTargetHelp)) {
			t.Errorf("%s: step one no longer says other computers are untouched", lang)
		}
		// The suggestions and the typed value are untouched by this change.
		if !strings.Contains(stale, `value="rel"`) || !strings.Contains(stale, `list="restore-branches"`) {
			t.Errorf("%s: the target control lost its value or its suggestions", lang)
		}
	}

	// The reader still learns what will happen, on the summary that was
	// actually computed and can no longer be edited.
	for _, lang := range Langs() {
		creates := restorePage(fullChrome(lang), true)
		creates.TargetBranch = "recovered-9a8b154"
		creates.CreatesBranch = true
		if !strings.Contains(render(t, r, creates), wantText(lang, MsgRestoreTargetNew)) {
			t.Errorf("%s: the previewed summary no longer says the branch would be created", lang)
		}

		adds := restorePage(fullChrome(lang), true)
		adds.TargetBranch = "release"
		adds.CreatesBranch = false
		if strings.Contains(render(t, r, adds), wantText(lang, MsgRestoreTargetNew)) {
			t.Errorf("%s: the previewed summary invents a branch creation", lang)
		}
	}

	// A refused apply still reports the stale branch on that same summary, and
	// still announces it. Dropping the step-one notice must not touch this.
	conflicted := fullChrome(LangEN)
	conflicted.Notices = []Notice{Error("target", MsgRestoreConflict)}
	page := restorePage(conflicted, true)
	page.TargetBranch = "release"
	page.CreatesBranch = false
	page.CanApply = false
	out := render(t, r, page)
	if !strings.Contains(out, wantText(LangEN, MsgRestoreConflict)) {
		t.Fatal("the conflict is no longer reported")
	}
	if !strings.Contains(out, `role="alert"`) {
		t.Error("the conflict is no longer announced")
	}
	if !strings.Contains(out, "disabled") {
		t.Error("a refused restore is still offered as an action")
	}
}

func TestRestoreDoesNotGuessBranchStateFromTheExpectedTip(t *testing.T) {
	// The zero OID is what the backend submits for an absent branch, but it is
	// a value in a form field, not a signal this page interprets. Reading it
	// here would be a second policy that could disagree with CreatesBranch.
	r := newRenderer(t)
	const zero = "0000000000000000000000000000000000000000"

	page := restorePage(fullChrome(LangEN), true)
	page.ExpectedHead = zero
	page.CreatesBranch = false
	if strings.Contains(render(t, r, page), wantText(LangEN, MsgRestoreTargetNew)) {
		t.Error("the page inferred a new branch from the zero tip")
	}

	// And the tip is still submitted unchanged, whatever it is.
	out := render(t, r, page)
	if !strings.Contains(out, `name="expected_head" value="`+zero+`"`) {
		t.Error("the observed tip was not submitted")
	}
}

func TestRestoreTypedTargetSurvivesTheRoundTrip(t *testing.T) {
	// A reader who typed a new name must not lose it by switching language or
	// by stepping back from the preview to change something else.
	r := newRenderer(t)
	c := fullChrome(LangEN)
	c.CurrentURL = "/repositories/r1/restore/preview"
	page := restorePage(c, true)
	page.TargetBranch = "recovered/9a8b154"

	back := restoreSelectionURL(page)
	if !strings.Contains(back, "target=recovered%2F9a8b154") {
		t.Errorf("stepping back loses the typed branch: %q", back)
	}

	out := render(t, r, page)
	address, _ := clickLanguage(t, out, c.CurrentURL)
	if !strings.Contains(address, "target=recovered%2F9a8b154") {
		t.Errorf("switching language loses the typed branch: %q", address)
	}

	// Arriving back at step one, the typed name is in the control again.
	returned := restorePage(fullChrome(LangEN), false)
	returned.TargetBranch = "recovered/9a8b154"
	if tag := targetInput(t, render(t, r, returned)); !strings.Contains(tag, `value="recovered/9a8b154"`) {
		t.Errorf("the typed branch did not come back: %s", tag)
	}
}

func TestRestoreTargetNameIsNotJudgedHere(t *testing.T) {
	// Whether a name is a valid ref, and whether writing there is allowed, is
	// decided by Git and the backend. The page must not filter, rewrite or
	// refuse names on its own, and it must render whatever it is given as
	// data.
	r := newRenderer(t)
	page := restorePage(fullChrome(LangEN), false)
	page.TargetBranch = `feature/"><script>alert(1)</script>`
	out := render(t, r, page)

	if strings.Contains(out, "<script>alert(1)</script>") {
		t.Error("a branch name was rendered as markup")
	}
	tag := targetInput(t, out)
	for _, guess := range []string{"pattern=", "maxlength="} {
		if strings.Contains(tag, guess) {
			t.Errorf("the page decides name validity itself via %s: %s", guess, tag)
		}
	}
	// A rejected name comes back from the backend and is shown against the
	// control the reader typed into.
	c := fullChrome(LangEN)
	c.Notices = []Notice{Error("target", MsgRestoreInvalid)}
	rejected := render(t, r, restorePage(c, false))
	if !strings.Contains(rejected, wantText(LangEN, MsgRestoreInvalid)) {
		t.Error("a rejected branch name is not explained")
	}
	if !strings.Contains(targetInput(t, rejected), `aria-invalid="true"`) {
		t.Error("the rejected control is not marked invalid")
	}
}

func TestRestoreStatesThatOtherComputersAreNotTouched(t *testing.T) {
	// This is the boundary a reader is most likely to get wrong, so it sits
	// beside the branch choice rather than only in the result message.
	r := newRenderer(t)
	for _, lang := range Langs() {
		out := render(t, r, restorePage(fullChrome(lang), false))
		if !strings.Contains(out, wantText(lang, MsgRestoreTargetHelp)) {
			t.Errorf("%s: the page does not say what happens to other computers", lang)
		}
	}
	en := Text(LangEN, MsgRestoreTargetHelp)
	if !strings.Contains(en, "other computers are not touched") {
		t.Errorf("the branch help no longer states the boundary: %q", en)
	}
	keep := Text(LangEN, MsgRestoreConfirmHelp)
	if !strings.Contains(keep, "does not remove or rewrite") {
		t.Errorf("the confirmation no longer promises the existing history is kept: %q", keep)
	}
}

func TestRestoreGenericCopyDoesNotPromiseACommitThatMayNotExist(t *testing.T) {
	// The two targets do different things. Restoring onto an existing branch
	// writes a commit whose parent is that branch's tip. Restoring to a name
	// that is not a branch yet creates the branch at the selected commit and
	// writes no commit at all.
	//
	// Messages shown in both cases therefore cannot promise a new commit. Only
	// the two messages about the branch choice may describe either outcome,
	// because they say which case they are talking about.
	generic := []MessageCode{
		MsgRestoreIntro,
		MsgRestoreTargetHelp,
		MsgRestorePreviewHelp,
		MsgRestoreConfirmHelp,
		MsgRestoreConfirmLabel,
		MsgRestoreSuccess,
	}
	// "new commit" in English, and the Korean phrasings for adding one.
	promises := []string{"new commit", "one commit", "adds a commit", "\uc0c8 \ucee4\ubc0b", "\ucee4\ubc0b \ud558\ub098"}
	for _, code := range generic {
		for _, lang := range Langs() {
			text := Text(lang, code)
			for _, promise := range promises {
				if strings.Contains(text, promise) {
					t.Errorf("%s (%s) promises a commit that a new-branch restore does not write: %q",
						code, lang, text)
				}
			}
		}
	}

	// What every restore really does share is still stated.
	if intro := Text(LangEN, MsgRestoreIntro); !strings.Contains(intro, "never removed or rewritten") {
		t.Errorf("the introduction no longer promises earlier commits survive: %q", intro)
	}
	if done := Text(LangEN, MsgRestoreSuccess); !strings.Contains(done, "earlier history is still there") {
		t.Errorf("the result no longer says the earlier history survived: %q", done)
	}

	// The difference between the two targets is explained where the reader
	// chooses one, so removing it from the generic copy loses nothing.
	choose := Text(LangEN, MsgRestoreTargetChoose)
	if !strings.Contains(choose, "adds to an existing branch") {
		t.Errorf("the existing-branch case is no longer described: %q", choose)
	}
	if !strings.Contains(choose, "continues from the selected commit") {
		t.Errorf("the new-branch case is no longer described: %q", choose)
	}
	absent := Text(LangEN, MsgRestoreTargetNew)
	if !strings.Contains(absent, "creates it from the selected commit") {
		t.Errorf("the absent-branch notice no longer says where the branch starts: %q", absent)
	}
}

func TestRestoreCopyIsShownForBothTargetKinds(t *testing.T) {
	// Both explanations have to reach the screen, in both languages, in the
	// case they describe.
	r := newRenderer(t)
	for _, lang := range Langs() {
		// Choosing is where both outcomes are described, because either is
		// still reachable from the field.
		existing := render(t, r, restorePage(fullChrome(lang), false))
		if !strings.Contains(existing, wantText(lang, MsgRestoreTargetChoose)) {
			t.Errorf("%s: the branch choice is not explained", lang)
		}
		if strings.Contains(existing, wantText(lang, MsgRestoreTargetNew)) {
			t.Errorf("%s: an existing branch is described as one that would be created", lang)
		}

		// Confirming is where the one that applies is named.
		absent := restorePage(fullChrome(lang), true)
		absent.TargetBranch = "recovered-9a8b154"
		absent.CreatesBranch = true
		if out := render(t, r, absent); !strings.Contains(out, wantText(lang, MsgRestoreTargetNew)) {
			t.Errorf("%s: a branch that does not exist is not flagged before the write", lang)
		}
	}
}

// ---------------------------------------------------------------------------
// entry points
// ---------------------------------------------------------------------------

func TestRestoreIsReachableFromTheViewsThatShowLostWork(t *testing.T) {
	r := newRenderer(t)

	overview := render(t, r, repoPage(fullChrome(LangEN), RepoTabOverview))
	if !strings.Contains(overview, `href="/repositories/r1/restore"`) {
		t.Error("the repository overview has no restore entry point")
	}
	if !strings.Contains(overview, `href="/repositories/r1/restore?source=7f2c1a0bb"`) {
		t.Error("kept history does not open restore with its commit preselected")
	}

	code := render(t, r, repoPage(fullChrome(LangEN), RepoTabCode))
	if !strings.Contains(code, "/repositories/r1/restore?path=") {
		t.Error("an opened file has no restore entry point")
	}
	if !strings.Contains(code, wantText(LangEN, MsgRestoreOpenFile)) {
		t.Error("the file entry point does not say it restores that file")
	}

	commits := render(t, r, repoPage(fullChrome(LangEN), RepoTabCommits))
	if !strings.Contains(commits, `href="/repositories/r1/restore?source=a41c9e2ff"`) {
		t.Error("an opened commit has no restore entry point")
	}
}

func TestRestoreLinksAreOptionalForExistingCallers(t *testing.T) {
	// The fields were added to existing page types. A caller that never sets
	// them must still render, with no control and no empty link.
	r := newRenderer(t)
	page := RepositoryPage{
		Chrome: fullChrome(LangEN),
		Tab:    RepoTabOverview,
		Repo:   RepositoryHeader{ID: "r1", Name: "forge-cli", URL: "/repositories/r1"},
		Overview: RepositoryOverview{
			Head:         CommitSummary{ShortOID: "a41c9e2", Subject: "Add JSON output", AuthorDate: testNow, URL: "/c"},
			Branches:     []RefLine{{Name: "main", URL: "/x", Kind: "branch", IsDefault: true}},
			RetainedRefs: []RefLine{{Name: "old/main", URL: "/y", Kind: "branch", Retained: true}},
		},
		OverviewURL: "/repositories/r1",
		CodeURL:     "/repositories/r1/code",
		CommitsURL:  "/repositories/r1/commits",
	}
	out := render(t, r, page)
	if strings.Contains(out, "/restore") {
		t.Error("a restore link appeared for a caller that supplied none")
	}
	if strings.Contains(out, `href=""`) {
		t.Error("an unset restore URL rendered as an empty link")
	}

	file := repoPage(fullChrome(LangEN), RepoTabCode)
	file.Code.File.RestoreURL = ""
	if out := render(t, r, file); strings.Contains(out, `href=""`) {
		t.Error("an unset file restore URL rendered as an empty link")
	}
}

// ---------------------------------------------------------------------------
// repository content is data
// ---------------------------------------------------------------------------

func TestRestoreEscapesPathsBranchesAndDiffContent(t *testing.T) {
	r := newRenderer(t)
	page := restorePage(fullChrome(LangEN), true)
	page.TargetBranch = `"><script>branch()</script>`
	page.Branches = nil
	page.Paths = []RestorePath{
		{Path: `<img src=x onerror="alert(1)">.go`, Status: "deleted", Selected: true},
	}
	page.Changes = []DiffFile{{
		Path: `<img src=x onerror="alert(1)">.go`, Status: "deleted", Deletions: 3,
		Hunks: []DiffHunk{{Header: "@@ -1,3 +0,0 @@", Lines: []DiffLine{
			{Kind: "del", OldLine: 1, Text: `<script>alert("diff")</script>`},
		}}},
	}}
	out := render(t, r, page)

	for _, raw := range []string{
		"<script>branch()</script>",
		`<img src=x onerror="alert(1)">`,
		`<script>alert("diff")</script>`,
	} {
		if strings.Contains(out, raw) {
			t.Errorf("repository content was rendered as markup: %q", raw)
		}
	}
	if !strings.Contains(out, "&lt;script&gt;") || !strings.Contains(out, "&lt;img") {
		t.Error("repository content was not escaped")
	}
}

func TestRestoreSymlinkAndSubmoduleContentIsShownAsData(t *testing.T) {
	// A symlink's stored value is its target path. It is shown as the text it
	// is and never followed, and a gitlink is explained rather than offered as
	// something this screen can restore.
	r := newRenderer(t)
	c := fullChrome(LangEN)
	c.Notices = []Notice{Error("path", MsgRestoreUnsupported).WithDetail("vendor/libgit2")}
	page := restorePage(c, true)
	page.Changes = []DiffFile{{
		Path: "config/current", Status: "modified", Additions: 1, Deletions: 1,
		Hunks: []DiffHunk{{Header: "@@ -1 +1 @@", Lines: []DiffLine{
			{Kind: "del", OldLine: 1, Text: "../../etc/passwd"},
			{Kind: "add", NewLine: 1, Text: "./settings.toml"},
		}}},
	}}
	out := render(t, r, page)

	if !strings.Contains(out, "../../etc/passwd") {
		t.Error("the symlink's stored value is not shown as data")
	}
	if !strings.Contains(out, wantText(LangEN, MsgRestoreUnsupported)) {
		t.Error("an unsupported selection is not explained")
	}
	if !strings.Contains(out, "vendor/libgit2") {
		t.Error("the unsupported entry is not named")
	}
	unsupported := Text(LangEN, MsgRestoreUnsupported)
	if !strings.Contains(unsupported, "Submodule") {
		t.Errorf("the unsupported message no longer names gitlinks: %q", unsupported)
	}
	if !strings.Contains(unsupported, "did not select") {
		t.Errorf("the unsupported message no longer covers path collisions: %q", unsupported)
	}
	// Reachable straight from this screen: selected files with a branch name
	// that does not exist yet. Picking files is a comparison against the
	// branch's current state, and a new branch has none, so the message has to
	// name that case and say what to do instead.
	for _, lang := range Langs() {
		text := Text(lang, MsgRestoreUnsupported)
		if !strings.Contains(text, "already exists") && !strings.Contains(text, "\uc774\ubbf8 \uc788\ub294 \ube0c\ub79c\uce58") {
			t.Errorf("%s: the message does not explain that selected files need an existing branch: %q", lang, text)
		}
	}
}

// ---------------------------------------------------------------------------
// working without scripting, and reachable by keyboard
// ---------------------------------------------------------------------------

func TestRestoreWorksAsPlainHTML(t *testing.T) {
	r := newRenderer(t)

	// The scope is a radio group and the paths are checkboxes, so step one
	// carries the selection in the form itself.
	choose := render(t, r, restorePage(fullChrome(LangEN), false))
	if strings.Count(choose, `type="radio" name="mode"`) != 2 {
		t.Error("the two restore scopes are not a radio group")
	}
	if !strings.Contains(choose, `type="checkbox" name="path"`) {
		t.Error("path selection is not part of the submitted form")
	}

	for _, previewed := range []bool{false, true} {
		out := render(t, r, restorePage(fullChrome(LangEN), previewed))
		if strings.Contains(out, `href="#"`) || strings.Contains(out, "javascript:") {
			t.Error("a control on the restore page needs scripting to do anything")
		}
		if strings.Contains(out, "onclick=") || strings.Contains(out, "onchange=") {
			t.Error("a restore control depends on an inline handler")
		}
		// Nothing on this page may be hidden behind the script, because a
		// browser without it must still be able to review and restore.
		if strings.Contains(out, "data-hide-with-script") {
			t.Error("a restore control disappears when scripting is available")
		}
	}
}

func TestRestoreScopeHintIsPresentationOnly(t *testing.T) {
	// The script dims the file list while the whole project is selected. It
	// must not disable or clear the inputs, or a reader would lose their ticks
	// and the backend would receive a selection nobody made.
	js := scriptSource(t)
	idx := strings.Index(js, "data-restore-files")
	if idx < 0 {
		t.Fatal("the restore scope hint is missing from the script")
	}
	block := js[idx:]
	if end := strings.Index(block, "})();"); end >= 0 {
		block = block[:end]
	}
	for _, destructive := range []string{"checked = false", "disabled", "remove()"} {
		if strings.Contains(block, destructive) {
			t.Errorf("the scope hint changes the submitted selection: %q", destructive)
		}
	}
	if !strings.Contains(block, "data-restore-dimmed") {
		t.Error("the scope hint does not use the presentation attribute")
	}
}

func TestRestoreStatusIsNotCarriedByColourAlone(t *testing.T) {
	r := newRenderer(t)
	out := render(t, r, restorePage(fullChrome(LangEN), false))

	// Every path row states its outcome in words as well as a shape.
	rows := out[strings.Index(out, `class="rpaths"`):]
	if end := strings.Index(rows, "</ul>"); end >= 0 {
		rows = rows[:end]
	}
	if strings.Count(rows, "<svg") < 3 {
		t.Error("path rows do not carry an icon shape beside the status word")
	}
	for _, code := range []MessageCode{MsgRestoreStatusAdded, MsgRestoreStatusModified, MsgRestoreStatusDeleted} {
		if !strings.Contains(rows, wantText(LangEN, code)) {
			t.Errorf("the status %q is not stated in words", code)
		}
	}
}

func TestRestoreControlsAreLabelledInBothLanguages(t *testing.T) {
	r := newRenderer(t)

	choose := render(t, r, restorePage(fullChrome(LangEN), false))
	if !strings.Contains(choose, `<label for="restore-target"`) {
		t.Error("the target picker has no label")
	}
	if !strings.Contains(choose, "<legend>") {
		t.Error("the scope group has no legend")
	}

	for _, previewed := range []bool{false, true} {
		out := render(t, r, restorePage(fullChrome(LangEN), previewed))
		if !strings.Contains(out, `data-ko="`) || !strings.Contains(out, `data-en="`) {
			t.Error("the restore page cannot switch language in place")
		}
		if !strings.Contains(out, `data-title-ko="`) {
			t.Error("the restore page title is not switchable")
		}
	}
}

func TestRestoreLanguageLinksAreFollowableGETs(t *testing.T) {
	// Regression. A previewed page is rendered from a POST-only route, and the
	// language links were built from the request URL. Following one, which is
	// what an ordinary click or an open-in-new-tab does, was a GET at
	// /restore/preview and returned 404.
	r := newRenderer(t)
	c := fullChrome(LangEN)
	c.CurrentURL = "/repositories/r1/restore/preview"
	page := restorePage(c, true)
	out := render(t, r, page)

	links := out[strings.Index(out, `data-lang-picker`):]
	if end := strings.Index(links, "</nav>"); end >= 0 {
		links = links[:end]
	}
	if strings.Contains(links, "/restore/preview") {
		t.Error("a language link points at the POST-only preview route")
	}
	for _, lang := range Langs() {
		want := attrEscape(withLang(restoreSelectionURL(page), lang))
		if !strings.Contains(links, `href="`+want+`"`) {
			t.Errorf("%s: the language link is not the canonical restore address", lang)
		}
	}
	// Switching language keeps the reader's choices.
	for _, keep := range []string{"source=7f2c1a0bb", "target=main", "mode=files", "backoff.go"} {
		if !strings.Contains(links, keep) {
			t.Errorf("switching language drops %q", keep)
		}
	}
}

func TestOtherPagesKeepTheirRequestURLForLanguageLinks(t *testing.T) {
	// Only pages that have no followable GET of their own override this. A
	// normal page must still switch language in place.
	r := newRenderer(t)
	c := fullChrome(LangEN)
	c.CurrentURL = "/repositories/r1/code?ref=main&path=internal"
	out := render(t, r, repoPage(c, RepoTabCode))
	for _, lang := range Langs() {
		if !strings.Contains(out, `href="`+attrEscape(withLang(c.CurrentURL, lang))+`"`) {
			t.Errorf("%s: an ordinary page lost its own address when switching language", lang)
		}
	}
}

func TestRestoreLinksNeverDescribeTwoScopesAtOnce(t *testing.T) {
	// A whole-project request that also names paths describes two different
	// restores, and the backend rejects it. A reader who ticks files and then
	// chooses the whole project leaves both pieces of state on the page, so
	// the address this renderer builds states the scope as it actually stands
	// rather than everything it happens to know.
	//
	// The ticks themselves stay in the form, because without scripting the
	// radio is what decides whether they are read and clearing them would lose
	// the reader's work.
	page := restorePage(fullChrome(LangEN), true)
	page.Mode = RestoreModeAll

	whole := restoreSelectionURL(page)
	if strings.Contains(whole, "path=") {
		t.Errorf("a whole-project address also names paths: %q", whole)
	}
	if !strings.Contains(whole, "mode=all") {
		t.Errorf("the whole-project scope is missing: %q", whole)
	}

	// Switching back to files restores them, so nothing was thrown away.
	page.Mode = RestoreModeFiles
	picked := restoreSelectionURL(page)
	for _, keep := range []string{"backoff.go", "limits.go"} {
		if !strings.Contains(picked, keep) {
			t.Errorf("the file scope lost %q: %q", keep, picked)
		}
	}

	// The language links carry the same normalized selection.
	r := newRenderer(t)
	c := fullChrome(LangEN)
	c.CurrentURL = "/repositories/r1/restore/preview"
	all := restorePage(c, true)
	all.Mode = RestoreModeAll
	out := render(t, r, all)
	links := out[strings.Index(out, `data-lang-picker`):]
	if end := strings.Index(links, "</nav>"); end >= 0 {
		links = links[:end]
	}
	if strings.Contains(links, "path=") {
		t.Error("a language link on a whole-project page also names paths")
	}

	// And so does the address the script leaves behind.
	address, _ := clickLanguage(t, out, c.CurrentURL)
	if strings.Contains(address, "path=") {
		t.Errorf("switching language produced a mixed-scope address: %q", address)
	}
	if !strings.Contains(address, "mode=all") {
		t.Errorf("switching language lost the whole-project scope: %q", address)
	}
}

func TestRestoreFileTicksSurviveTheWholeProjectScope(t *testing.T) {
	// Without scripting the radio is the only thing that decides the scope, so
	// the checkboxes must stay present and usable even while the whole project
	// is selected. The page says, in words, that they are not being used.
	r := newRenderer(t)
	page := restorePage(fullChrome(LangEN), false)
	page.Mode = RestoreModeAll
	out := render(t, r, page)

	if !strings.Contains(out, `type="checkbox" name="path"`) {
		t.Fatal("the file list is gone while the whole project is selected")
	}
	panel := out[strings.Index(out, "data-restore-files"):]
	if end := strings.Index(panel, "</fieldset>"); end >= 0 {
		panel = panel[:end]
	}
	if strings.Contains(panel, "disabled") {
		t.Error("the file ticks are disabled, so a reader without scripting cannot choose files")
	}
	if !strings.Contains(panel, "checked") {
		t.Error("an earlier tick was cleared by choosing the whole project")
	}
	for _, lang := range Langs() {
		out := render(t, r, restorePage(fullChrome(lang), false))
		if !strings.Contains(out, wantText(lang, MsgRestoreFilesInactive)) {
			t.Errorf("%s: the page does not say the ticks are unused in this scope", lang)
		}
	}
}

func TestRestoreSelectionURLUsesOnlyAgreedQueryFields(t *testing.T) {
	// The canonical address is read by the existing GET handler, which looks
	// at source, target, mode and repeated path. Adding a field here without
	// agreeing it would be silently ignored, so the names are pinned.
	page := restorePage(fullChrome(LangEN), true)
	raw := restoreSelectionURL(page)
	query := raw[strings.Index(raw, "?")+1:]

	allowed := map[string]bool{"source": true, "target": true, "mode": true, "path": true}
	for _, pair := range strings.Split(query, "&") {
		name := pair[:strings.Index(pair, "=")]
		if !allowed[name] {
			t.Errorf("the canonical restore address carries an unagreed field %q", name)
		}
	}

	// With nothing chosen it is still the plain restore page rather than a
	// address full of empty parameters.
	bare := RestorePage{Chrome: fullChrome(LangEN), ApplyURL: "/repositories/r1/restore", CancelURL: "/repositories/r1"}
	if got := restoreSelectionURL(bare); got != "/repositories/r1/restore" {
		t.Errorf("an empty selection produced %q", got)
	}
}

// attrEscape renders a URL the way html/template writes it into an attribute.
func attrEscape(url string) string {
	return strings.ReplaceAll(url, "&", "&amp;")
}

// langLinkPattern pulls the rendered language links out of a page.
var langLinkPattern = regexp.MustCompile(`data-lang-set="(\w+)"`)

// clickLanguage runs the shipped owngit.js against the page's real language
// links and reports where its click handler leaves the address bar.
//
// The anchor markup is only half the answer. The script intercepts the click
// and rewrites the address itself, so the address a reader can reload or share
// is decided by the handler, not by the href. That is what this drives.
func clickLanguage(t *testing.T, out, currentURL string) (address string, lang string) {
	t.Helper()
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is not available to run the shipped script")
	}

	// Take the links exactly as rendered, with their attribute escaping undone
	// the way a browser would parse them.
	type link struct {
		Lang string `json:"lang"`
		Href string `json:"href"`
	}
	var links []link
	for _, m := range langLinkPattern.FindAllStringSubmatchIndex(out, -1) {
		tag := out[strings.LastIndex(out[:m[0]], "<a "):m[1]]
		href := tag[strings.Index(tag, `href="`)+len(`href="`):]
		href = href[:strings.Index(href, `"`)]
		links = append(links, link{
			Lang: out[m[2]:m[3]],
			Href: strings.ReplaceAll(href, "&amp;", "&"),
		})
	}
	if len(links) < 2 {
		t.Fatalf("the page rendered %d language links", len(links))
	}

	script, err := filepath.Abs("assets/owngit.js")
	if err != nil {
		t.Fatal(err)
	}
	input, err := json.Marshal(map[string]any{
		"script": script, "currentURL": currentURL, "links": links,
	})
	if err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command(node, "testdata/langclick.mjs", string(input))
	cmd.Stderr = os.Stderr
	stdout, err := cmd.Output()
	if err != nil {
		t.Fatalf("running the shipped script: %v", err)
	}
	var result struct {
		Address   string `json:"address"`
		Prevented bool   `json:"prevented"`
		Lang      string `json:"lang"`
	}
	if err := json.Unmarshal(stdout, &result); err != nil {
		t.Fatalf("reading the script's result: %v", err)
	}
	if !result.Prevented {
		t.Fatal("the language click was not intercepted")
	}
	return result.Address, result.Lang
}

func TestRestoreLanguageClickLandsOnAFollowableAddress(t *testing.T) {
	// Regression, driven through the real script. The interceptor used to
	// build the new address from window.location, which on a previewed page is
	// the POST-only preview route. A reader who switched language was left on
	// /restore/preview?lang=ko: a 404 on reload or share, with the selection
	// gone.
	r := newRenderer(t)
	c := fullChrome(LangEN)
	c.CurrentURL = "/repositories/r1/restore/preview"
	out := render(t, r, restorePage(c, true))

	address, lang := clickLanguage(t, out, c.CurrentURL)

	if lang != string(LangKO) {
		t.Errorf("the click did not switch the language: %q", lang)
	}
	if strings.Contains(address, "/restore/preview") {
		t.Errorf("the address bar was left on the POST-only route: %q", address)
	}
	if !strings.HasPrefix(address, "/repositories/r1/restore?") {
		t.Errorf("the address bar is not the canonical restore address: %q", address)
	}
	if !strings.Contains(address, "lang=ko") {
		t.Errorf("the address bar does not carry the chosen language: %q", address)
	}
	// The reader's choices survive the switch.
	for _, keep := range []string{"source=7f2c1a0bb", "target=main", "mode=files", "backoff.go"} {
		if !strings.Contains(address, keep) {
			t.Errorf("switching language dropped %q from %q", keep, address)
		}
	}
}

func TestLanguageClickKeepsAnOrdinaryPageWhereItIs(t *testing.T) {
	// The same handler must not disturb a page that already has a followable
	// address of its own.
	r := newRenderer(t)
	c := fullChrome(LangEN)
	c.CurrentURL = "/repositories/r1/code?ref=main&path=internal"
	out := render(t, r, repoPage(c, RepoTabCode))

	address, lang := clickLanguage(t, out, c.CurrentURL)
	if lang != string(LangKO) {
		t.Errorf("the click did not switch the language: %q", lang)
	}
	if !strings.HasPrefix(address, "/repositories/r1/code?") {
		t.Errorf("an ordinary page moved elsewhere: %q", address)
	}
	for _, keep := range []string{"ref=main", "path=internal", "lang=ko"} {
		if !strings.Contains(address, keep) {
			t.Errorf("switching language dropped %q from %q", keep, address)
		}
	}
}

func TestRestoreTitleNamesTheRepository(t *testing.T) {
	// Restoring writes to one repository. A title reading only "Restore files"
	// would not say which.
	for _, lang := range Langs() {
		got := documentTitle(RestorePage{
			Chrome: fullChrome(lang),
			Repo:   RepositoryHeader{ID: "r1", Name: "forge-cli"},
		}, lang)
		if !strings.Contains(got, "forge-cli") {
			t.Errorf("%s: the restore title does not name the repository: %q", lang, got)
		}
		if !strings.Contains(got, Text(lang, MsgRestoreTitle)) {
			t.Errorf("%s: the restore title does not name the action: %q", lang, got)
		}
	}
}

func TestRestoreKoreanUsesStandardGitTerms(t *testing.T) {
	r := newRenderer(t)
	out := render(t, r, restorePage(fullChrome(LangKO), true))
	for _, term := range []string{"브랜치", "커밋", "저장소"} {
		if !strings.Contains(out, term) {
			t.Errorf("the Korean restore page is missing the standard term %q", term)
		}
	}
	for _, bad := range []string{"·", "—", "–", "오운깃"} {
		if strings.Contains(out, bad) {
			t.Errorf("the restore page contains %q", bad)
		}
	}
}
