package webui

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

// These screens exist to report evidence about code, which makes their failure
// mode specific: a state the reader misreads as a pass is worse than no screen
// at all. The tests below are about that, and about the security properties of
// the credential screen.

// formNamed returns the markup of the POST form whose action contains the
// given fragment, so a test can assert on one form rather than the page.
func formNamed(t *testing.T, out, action string) string {
	t.Helper()
	for _, form := range strings.Split(out, "<form")[1:] {
		if end := strings.Index(form, "</form>"); end >= 0 {
			form = form[:end]
		}
		if strings.Contains(form, action) && strings.Contains(form, `method="post"`) {
			return form
		}
	}
	t.Fatalf("no POST form with action containing %q", action)
	return ""
}

// checks and reviews are advisory

func TestFailingChecksDoNotBlockMerge(t *testing.T) {
	// The product rule this screen has to honour: a failed or pending check is
	// information, never a gate. If a failure ever disabled the control, the
	// interface would be enforcing a policy the backend deliberately does not.
	r := newRenderer(t)
	for _, status := range []string{
		CheckFailed, CheckError, CheckCancelled, CheckIncomplete, CheckUnavailable, CheckStale, CheckAbsent,
	} {
		page := pullRequestPage(fullChrome(LangEN), prFixtureFailing)
		page.Checks.Status = status
		out := render(t, r, page)

		merge := formNamed(t, out, "/merge")
		if strings.Contains(merge, "disabled") {
			t.Errorf("check status %q disabled the merge control", status)
		}
		// The result still has to be visible beside the control, otherwise
		// "merging is allowed" would quietly become "the failure is hidden".
		if want := wantText(LangEN, checkState(status).Code); !strings.Contains(out, want) {
			t.Errorf("check status %q is not stated on the page", status)
		}
	}
}

func TestPendingOrNegativeReviewDoesNotBlockMerge(t *testing.T) {
	r := newRenderer(t)
	for _, status := range []string{
		ReviewPending, ReviewChanges, ReviewUnavailable, ReviewPartial, ReviewNotRequested, ReviewSkipped,
	} {
		page := pullRequestPage(fullChrome(LangEN), prFixtureFailing)
		page.Review = ReviewEvidence{Status: status, BoundToCurrentRevision: true, SubmittedAt: testNow}
		out := render(t, r, page)

		if merge := formNamed(t, out, "/merge"); strings.Contains(merge, "disabled") {
			t.Errorf("review status %q disabled the merge control", status)
		}
	}
}

func TestOnlyGitAndRepositoryReasonsRefuseAMerge(t *testing.T) {
	// The merge control is disabled only when the backend itself refuses, and
	// the reasons shown are Git and repository state. A check or review result
	// must never appear as a refusal.
	r := newRenderer(t)
	out := render(t, r, pullRequestPage(fullChrome(LangEN), prFixtureBlocked))

	if merge := formNamed(t, out, "/merge"); !strings.Contains(merge, "disabled") {
		t.Error("a merge the backend refuses is still offered as available")
	}
	if !strings.Contains(out, wantText(LangEN, MsgPRMergeRefused)) {
		t.Error("the refusal is not explained")
	}
	// The failing check on this fixture is stated as information, not as a
	// reason the merge was refused.
	blockers := out[strings.Index(out, `class="blockers"`):]
	if end := strings.Index(blockers, "</ul>"); end >= 0 {
		blockers = blockers[:end]
	}
	for _, forbidden := range []MessageCode{
		MsgCheckStateFailed, MsgCheckStateAbsent, MsgReviewStateNone, MsgReviewStatePending,
	} {
		if strings.Contains(blockers, wantText(LangEN, forbidden)) {
			t.Errorf("%q is listed as a merge blocker, but it is advisory", forbidden)
		}
	}
}

func TestUnknownRefusalCodeStillReachesTheReader(t *testing.T) {
	// A reason this interface has no words for must not vanish. It is shown
	// generically with the backend's own code, so the reader can act on it.
	r := newRenderer(t)
	out := render(t, r, pullRequestPage(fullChrome(LangEN), prFixtureBlocked))

	if !strings.Contains(out, wantText(LangEN, MsgMergeBlockedOther)) {
		t.Error("an unrecognised refusal was not explained at all")
	}
	if !strings.Contains(out, "some_new_backend_reason") {
		t.Error("an unrecognised refusal code was swallowed")
	}
}

func TestAdvisoryNatureIsStatedNextToTheEvidence(t *testing.T) {
	r := newRenderer(t)
	out := render(t, r, pullRequestPage(fullChrome(LangEN), prFixtureFailing))
	if !strings.Contains(out, wantText(LangEN, MsgEvidenceAdvisory)) {
		t.Error("the screen does not say that checks and reviews never block merging")
	}
	if !strings.Contains(out, wantText(LangEN, MsgPRMergeDespite)) {
		t.Error("the merge control does not say the failing result stays as it is")
	}
}

func TestKoreanEvidenceUsesFamiliarDeveloperTerms(t *testing.T) {
	expected := map[MessageCode]string{
		MsgEvidenceAdvisory: "테스트, 린트, 빌드 같은 자동 체크와 코드 리뷰는 참고 정보이며 병합을 막지 않습니다.",
		MsgCheckTitle:       "체크",
		MsgCheckRevision:    "대상 커밋",
		MsgReviewTitle:      "리뷰",
		MsgTasksIntro:       "체크 에이전트가 실행한 명령과 각 결과가 어느 커밋에 해당하는지 보여 줍니다.",
		MsgHelperTitle:      "체크 에이전트 토큰",
		MsgHelperIntro:      "체크 에이전트가 결과를 보고할 때 쓰는 전용 토큰입니다. 저장소 접근 권한이나 관리자 비밀번호와는 별개입니다.",
		MsgHelperTokenLabel: "토큰",
	}
	for code, want := range expected {
		if got := Text(LangKO, code); got != want {
			t.Errorf("%s Korean text=%q, want %q", code, got, want)
		}
	}

	r := newRenderer(t)
	var rendered strings.Builder
	rendered.WriteString(render(t, r, pullRequestPage(fullChrome(LangKO), prFixtureFailing)))
	rendered.WriteString(render(t, r, tasksPage(fullChrome(LangKO), false)))
	rendered.WriteString(render(t, r, helperPage(fullChrome(LangKO), true)))
	out := rendered.String()
	for _, code := range []MessageCode{
		MsgEvidenceAdvisory, MsgCheckTitle, MsgReviewTitle, MsgTasksIntro,
		MsgHelperTitle, MsgHelperIntro, MsgHelperTokenLabel,
	} {
		if want := wantText(LangKO, code); !strings.Contains(out, want) {
			t.Errorf("rendered Korean screens do not contain %s text %q", code, want)
		}
	}

	for code, entry := range evidenceCatalog {
		for _, stale := range []string{"검사", "도우미", "자격 증명", "비밀 값", "이름표", "리비전"} {
			if strings.Contains(entry.ko, stale) {
				t.Errorf("%s Korean text still contains %q: %q", code, stale, entry.ko)
			}
		}
	}
}

func TestKoreanPullRequestListShowsSourceToTargetDirection(t *testing.T) {
	r := newRenderer(t)
	page := pullRequestsPage(fullChrome(LangKO), false)
	page.Items = page.Items[:1]
	page.Items[0].Source.Branch = "feature"
	page.Items[0].Target.Branch = "main"
	out := render(t, r, page)

	source := strings.Index(out, ">feature</span>")
	flow := strings.Index(out, `data-en="into" data-ko="→">→</span>`)
	target := strings.Index(out, ">main</span>")
	if source < 0 || flow < 0 || target < 0 {
		t.Fatalf("source-to-target row is incomplete: source=%d flow=%d target=%d", source, flow, target)
	}
	if !(source < flow && flow < target) {
		t.Fatalf("branch flow order source=%d flow=%d target=%d", source, flow, target)
	}
}

// no state is ever inferred into a pass

func TestUnknownStatusesNeverRenderAsPassed(t *testing.T) {
	// A status string this package does not recognise reaches the reader as an
	// explicit non-claim. Falling through to "passed" would be the worst
	// possible default, and falling through to blank would be almost as bad.
	r := newRenderer(t)
	out := render(t, r, pullRequestPage(fullChrome(LangEN), prFixtureUnknown))

	if strings.Contains(out, wantText(LangEN, MsgCheckStatePassed)) {
		t.Error("an unrecognised check status rendered as passed")
	}
	if strings.Contains(out, wantText(LangEN, MsgReviewStateApproved)) {
		t.Error("an unrecognised review status rendered as approved")
	}
	if n := strings.Count(out, wantText(LangEN, MsgEvidenceUnknownState)); n < 2 {
		t.Errorf("unrecognised statuses are stated %d times, want both the check and the review", n)
	}
}

func TestNoProtectionValueEverClaimsASandbox(t *testing.T) {
	// OwnGit runs checks in the developer's own environment. There is no
	// sandbox, so no protection value may be worded as one, including when the
	// backend sends the literal string.
	r := newRenderer(t)
	var all strings.Builder
	for _, lang := range Langs() {
		for _, page := range allPages(lang) {
			all.WriteString(render(t, r, page))
		}
	}
	out := all.String()
	for _, claim := range []string{"sandbox", "Sandbox", "격리된 환경", "안전한 환경"} {
		// "It is not a sandbox" is the one permitted use, so check the words
		// that would constitute a claim instead.
		if strings.Contains(out, claim) && !strings.Contains(out, "not a sandbox") {
			t.Errorf("the interface claims a sandbox (%q)", claim)
		}
	}
	// A backend value of "sandboxed" is not recognised and produces no note at
	// all, rather than a protection claim.
	if protectionNote("sandboxed") != "" {
		t.Error(`a protection value of "sandboxed" produced a note`)
	}
}

func TestRevisionLabelDoesNotClaimPendingOrUnprovenEvidenceWasTested(t *testing.T) {
	r := newRenderer(t)

	pendingReview := pullRequestPage(fullChrome(LangKO), prFixtureFailing)
	pendingReview.Checks = CheckEvidence{Status: CheckAbsent, Configured: true, Advisory: true}
	pendingReview.Review = ReviewEvidence{
		Status: ReviewPending, SourceOID: "7f2c1a0bb", ShortSourceOID: "7f2c1a0",
		BoundToCurrentRevision: true, Provenance: ReviewFromRequest, SubmittedAt: testNow,
	}

	pages := map[string]PullRequestPage{"pending review": pendingReview}
	for _, tree := range []string{WorktreeDirty, WorktreeUnknown} {
		page := pullRequestPage(fullChrome(LangKO), prFixtureFailing)
		page.Checks.Status = CheckPassed
		page.Checks.WorktreeState = tree
		page.Checks.TestedCommit = false
		page.Review = ReviewEvidence{}
		pages[tree+" check"] = page
	}

	for name, page := range pages {
		t.Run(name, func(t *testing.T) {
			out := render(t, r, page)
			if !strings.Contains(out, "대상 커밋") {
				t.Error("the recorded revision is not labelled as the target commit")
			}
			if strings.Contains(out, "테스트한 커밋") {
				t.Error("pending or unproven evidence labels its revision as tested")
			}
			if !strings.Contains(out, "7f2c1a0") {
				t.Error("the recorded revision is missing")
			}
		})
	}
}

func TestDirtyOrUnknownWorktreeIsNotATestedCommit(t *testing.T) {
	// The whole value of a green check is the claim that this commit is the
	// code that ran. That claim is only true for a clean tree, so the other
	// two cases have to say so in words.
	r := newRenderer(t)
	for _, tree := range []string{WorktreeDirty, WorktreeUnknown} {
		page := pullRequestPage(fullChrome(LangEN), prFixtureFailing)
		page.Checks.Status = CheckPassed
		page.Checks.WorktreeState = tree
		page.Checks.TestedCommit = false
		out := render(t, r, page)

		if !strings.Contains(out, wantText(LangEN, MsgCheckNotTested)) {
			t.Errorf("worktree %q: the result is not qualified as unproven", tree)
		}
		if strings.Contains(out, wantText(LangEN, MsgCheckTestedCommit)) {
			t.Errorf("worktree %q: the result claims the commit was the code that ran", tree)
		}
	}

	// A clean tree on the current revision is the case that may claim it.
	page := pullRequestPage(fullChrome(LangEN), prFixtureFailing)
	page.Checks.Status = CheckPassed
	page.Checks.WorktreeState = WorktreeClean
	page.Checks.TestedCommit = true
	if out := render(t, r, page); !strings.Contains(out, wantText(LangEN, MsgCheckTestedCommit)) {
		t.Error("a clean tree on this revision does not state that the result describes this code")
	}
}

func TestAStaleResultNeverLooksCurrent(t *testing.T) {
	// An old success is the most dangerous thing this screen can show: the
	// word "passed" is true, and the code it describes is not on screen.
	r := newRenderer(t)
	page := pullRequestPage(fullChrome(LangEN), prFixtureFailing)
	page.Checks.Status = CheckStale
	page.Checks.Stale = true
	page.Checks.TestedCommit = true
	page.Checks.RevisionShortOID = "5d0aa13"
	out := render(t, r, page)

	if strings.Contains(out, wantText(LangEN, MsgCheckStatePassed)) {
		t.Error("a stale result is presented as a pass")
	}
	if !strings.Contains(out, wantText(LangEN, MsgCheckStateStale)) {
		t.Error("a stale result does not say it belongs to an earlier revision")
	}
	if !strings.Contains(out, wantText(LangEN, MsgCheckStaleDetail)) {
		t.Error("a stale result does not say it tells the reader nothing about this change")
	}
	// It must not simultaneously claim to describe the code on screen.
	if strings.Contains(out, wantText(LangEN, MsgCheckTestedCommit)) {
		t.Error("a stale result claims to describe this pull request's code")
	}
	// The revision it actually belongs to is named, so the reader can tell.
	if !strings.Contains(out, "5d0aa13") {
		t.Error("a stale result does not name the revision it belongs to")
	}
}

func TestAReviewOfAnotherRevisionIsNotCurrentApproval(t *testing.T) {
	r := newRenderer(t)
	out := render(t, r, pullRequestPage(fullChrome(LangEN), prFixtureFailing))
	if !strings.Contains(out, wantText(LangEN, MsgReviewOtherRevision)) {
		t.Error("an approval for an earlier revision is not qualified")
	}
	if !strings.Contains(out, "5d0aa13") {
		t.Error("the revision the review belongs to is not named")
	}
}

func TestEmptyAndUnconfiguredStatesAreRealStates(t *testing.T) {
	// Nothing recorded, nothing configured, and records that could not be read
	// are three different answers. None of them may render as a pass, and none
	// may render as a blank that reads like one.
	r := newRenderer(t)

	page := pullRequestPage(fullChrome(LangEN), prFixtureFailing)
	page.Checks = CheckEvidence{Status: CheckAbsent, Configured: false, Advisory: true}
	out := render(t, r, page)
	for _, want := range []MessageCode{MsgCheckStateAbsent, MsgCheckNotConfigured} {
		if !strings.Contains(out, wantText(LangEN, want)) {
			t.Errorf("an unconfigured repository does not state %q", want)
		}
	}
	if strings.Contains(out, wantText(LangEN, MsgCheckStatePassed)) {
		t.Error("an unconfigured repository renders as passed")
	}

	list := render(t, r, pullRequestsPage(fullChrome(LangEN), true))
	if !strings.Contains(list, wantText(LangEN, MsgPRListUnavailable)) {
		t.Error("unreadable pull request records render as an empty list")
	}
	if strings.Contains(list, wantText(LangEN, MsgPRListEmpty)) {
		t.Error("unreadable records claim there are no pull requests")
	}

	tasks := tasksPage(fullChrome(LangEN), false)
	tasks.Unavailable = true
	tasks.UnavailableReason = MsgPRFailed
	tasks.Tasks = nil
	if out := render(t, r, tasks); !strings.Contains(out, wantText(LangEN, MsgTasksUnavail)) {
		t.Error("unreadable task records render as an empty list")
	}
}

func TestAChangeListThatCouldNotBeBuiltIsNotShownAsNoChanges(t *testing.T) {
	r := newRenderer(t)
	page := pullRequestPage(fullChrome(LangEN), prFixtureFailing)
	page.Changes = nil
	page.ChangesUnavailable = true
	out := render(t, r, page)

	if !strings.Contains(out, wantText(LangEN, MsgPRChangesUnavail)) {
		t.Error("a comparison that failed is not reported")
	}
	if strings.Contains(out, wantText(LangEN, MsgPRChangesNone)) {
		t.Error("a failed comparison claims the branches have the same content")
	}
}

func TestATaskWithNoRunShowsTheAbsence(t *testing.T) {
	r := newRenderer(t)
	out := render(t, r, tasksPage(fullChrome(LangEN), false))
	if !strings.Contains(out, wantText(LangEN, MsgCheckStateAbsent)) {
		t.Error("a task with no recorded run does not show that absence")
	}
}

func TestExhaustedBudgetDoesNotReadAsALockout(t *testing.T) {
	// An exhausted correction budget stops automatic correction. It does not
	// restrict Git, remove work, or block a merge, and the screen has to say
	// so or it reads as a punishment.
	r := newRenderer(t)
	out := render(t, r, tasksPage(fullChrome(LangEN), true))
	if !strings.Contains(out, wantText(LangEN, MsgTaskExhausted)) {
		t.Error("an exhausted task does not explain what actually stopped")
	}
	if !strings.Contains(out, wantText(LangEN, MsgTaskBudgetHelp)) {
		t.Error("the screen does not say the budget belongs to the task")
	}
}

func TestLogDispositionIsFourDistinctAnswers(t *testing.T) {
	// The raw log is disposable and the durable record outlives it. "Not
	// recorded", "expired", "could not be read" and "not stated" are different
	// facts, and collapsing them would let a missing log imply a missing run.
	seen := map[MessageCode]string{}
	for _, status := range []string{LogAvailable, LogExpired, LogNotRecorded, LogUnavailable, LogUnknown, "shredded"} {
		note := logNote(status)
		if prior, ok := seen[note]; ok && prior != LogUnknown && status != "shredded" {
			t.Errorf("log statuses %q and %q produce the same sentence", prior, status)
		}
		seen[note] = status
	}
	// An unrecognised value states nothing rather than implying availability.
	if logNote("shredded") != MsgCheckLogUnknown {
		t.Error("an unrecognised log status does not fall back to a non-claim")
	}

	r := newRenderer(t)
	page := pullRequestPage(fullChrome(LangEN), prFixtureFailing)
	page.Checks.LogStatus = LogExpired
	page.Checks.LogExpiresAt = time.Time{}
	out := render(t, r, page)
	if !strings.Contains(out, wantText(LangEN, MsgCheckLogExpired)) {
		t.Error("an expired log is not reported")
	}
	// An expired log does not erase the recorded result.
	if !strings.Contains(out, wantText(LangEN, MsgCheckStateFailed)) {
		t.Error("an expired log removed the durable result from the screen")
	}
}

func TestTruncatedOutputIsNotPresentedAsTheWholeLog(t *testing.T) {
	r := newRenderer(t)
	out := render(t, r, tasksPage(fullChrome(LangEN), true))
	if !strings.Contains(out, wantText(LangEN, MsgCheckOutputCut)) {
		t.Error("shortened output is not marked as shortened")
	}
}

func TestNoUserFacingIdempotencyBadge(t *testing.T) {
	// Attempt identity is a backend property proven by backend tests. The
	// interface may show an identifier, but it must not award a badge that
	// asserts the property to the reader.
	r := newRenderer(t)
	var all strings.Builder
	for _, lang := range Langs() {
		for _, page := range allPages(lang) {
			all.WriteString(render(t, r, page))
		}
	}
	out := all.String()
	for _, claim := range []string{"idempot", "Idempot", "멱등"} {
		if strings.Contains(out, claim) {
			t.Errorf("the interface asserts an idempotency property (%q)", claim)
		}
	}
	// The identifier itself is still available, which is what a reader
	// actually needs to correlate a run.
	if !strings.Contains(out, "att_9f31c0d4") {
		t.Error("no attempt identifier is shown anywhere")
	}
}

func TestRequestingAReviewClaimsOnlyThatItWasRecorded(t *testing.T) {
	// Nothing runs a reviewer, so requesting and skipping record an intent.
	// Wording that implies a model ran would be a false claim.
	r := newRenderer(t)
	out := render(t, r, pullRequestPage(fullChrome(LangEN), prFixtureFailing))
	if !strings.Contains(out, wantText(LangEN, MsgReviewRequestIntent)) {
		t.Error("the review controls do not say they only record a decision")
	}
}

func TestLegacyDecisionRequiredReadsAsNoReview(t *testing.T) {
	// An older record means this revision has no review. It is not an
	// instruction and not a mandatory step.
	if got := reviewState(ReviewDecisionRequired).Code; got != MsgReviewStateNone {
		t.Errorf("decision_required renders as %q, want the no-review state", got)
	}
	if reviewState(ReviewDecisionRequired) != reviewState(ReviewNotRequested) {
		t.Error("a legacy record and an absent record read differently")
	}
}

// create: the submitted commits describe the selected branches

func TestCreateOffersNoFormBeforeTheBranchTipsAreRead(t *testing.T) {
	// Without observed tips there is nothing to create from, so there is no
	// create form to submit stale or mismatched commit ids with.
	r := newRenderer(t)
	out := render(t, r, newPullRequestPage(fullChrome(LangEN), false))

	if strings.Contains(out, `name="source_oid"`) {
		t.Error("a commit id is submitted before either branch tip was read")
	}
	if strings.Contains(out, wantText(LangEN, MsgPRNewSubmit)) {
		t.Error("the create control exists before the branches were compared")
	}
}

func TestCreateSubmitsTheCommitsObservedForTheSelectedBranches(t *testing.T) {
	// The defect this prevents: picking a second branch pair while the page
	// still carries the first pair's commit ids, which without scripting would
	// create a pull request for code nobody looked at. The selection is a
	// server round trip, so the names and ids always describe one observation.
	r := newRenderer(t)
	page := newPullRequestPage(fullChrome(LangEN), true)
	out := render(t, r, page)

	form := formNamed(t, out, page.SubmitURL)
	for _, want := range []string{
		`name="source_branch" value="` + page.Source.Branch + `"`,
		`name="target_branch" value="` + page.Target.Branch + `"`,
		`name="source_oid" value="` + page.Source.OID + `"`,
		`name="target_oid" value="` + page.Target.OID + `"`,
	} {
		if !strings.Contains(form, want) {
			t.Errorf("the create form does not submit %s", want)
		}
	}
	// Exactly one pair is submitted, so no other branch's tip can travel with
	// the request.
	if n := strings.Count(form, `name="source_oid"`); n != 1 {
		t.Errorf("the create form submits %d source commits, want 1", n)
	}
	if !strings.Contains(out, wantText(LangEN, MsgPRNewObservedTips)) {
		t.Error("the screen does not explain that a moved branch fails instead of being used")
	}
}

func TestChangingBranchesReturnsToAFollowableSelection(t *testing.T) {
	r := newRenderer(t)
	page := newPullRequestPage(fullChrome(LangEN), true)
	back := pullRequestSelectionURL(page)

	if !strings.Contains(back, "source=fix%2Fcursor") || !strings.Contains(back, "target=main") {
		t.Errorf("the selection address does not carry the chosen branches: %s", back)
	}
	// It carries no commit ids: returning here reads the tips again rather
	// than reusing a pair that may already be out of date.
	if strings.Contains(back, "oid") || strings.Contains(back, page.Source.OID) {
		t.Errorf("the selection address carries an observed commit: %s", back)
	}
	if out := render(t, r, page); !strings.Contains(out, `href="`+strings.ReplaceAll(back, "&", "&amp;")+`"`) {
		t.Error("the change-branches control does not lead to the selection address")
	}
}

func TestNonCommitBranchTipsOfferNoCreateForm(t *testing.T) {
	r := newRenderer(t)
	page := newPullRequestPage(fullChrome(LangEN), true)
	page.Source = RevisionState{Branch: "fix/cursor", Status: RevisionMissing}
	out := render(t, r, page)

	if strings.Contains(out, wantText(LangEN, MsgPRNewSubmit)) {
		t.Error("a branch that is not a commit still offers a create control")
	}
	if !strings.Contains(out, wantText(LangEN, MsgPRNewNoCommit)) {
		t.Error("the screen does not say why there is nothing to compare")
	}
	if !strings.Contains(out, wantText(LangEN, MsgPRBranchGone)) {
		t.Error("a missing branch tip is not named")
	}
}

func TestPullRequestActionsResubmitTheRevisionOnScreen(t *testing.T) {
	// Every action carries the tips this page was rendered from, so a branch
	// that moved is refused rather than acted on behind the reader's back.
	r := newRenderer(t)
	page := pullRequestPage(fullChrome(LangEN), prFixtureFailing)
	out := render(t, r, page)

	for _, action := range []string{"/review/request", "/review/skip", "/merge"} {
		form := formNamed(t, out, action)
		if !strings.Contains(form, `name="source_oid" value="`+page.Source.OID+`"`) {
			t.Errorf("%s does not resubmit the source commit on screen", action)
		}
		if !strings.Contains(form, `name="target_oid" value="`+page.Target.OID+`"`) {
			t.Errorf("%s does not resubmit the target commit on screen", action)
		}
		if !strings.Contains(form, `name="csrf"`) {
			t.Errorf("%s has no CSRF token", action)
		}
	}
}

func TestAMergedPullRequestOffersNoFurtherActions(t *testing.T) {
	r := newRenderer(t)
	out := render(t, r, pullRequestPage(fullChrome(LangEN), prFixtureMerged))

	if strings.Contains(out, `action="/repositories/r1/pull-requests/12/merge"`) {
		t.Error("a merged pull request still offers a merge control")
	}
	// The record of what happened stays.
	for _, want := range []string{"c07f4ab", "refs/owngit/merges/12", "merge-commit"} {
		if !strings.Contains(out, want) {
			t.Errorf("the merge record does not show %q", want)
		}
	}
}

// helper credentials

func TestIssuingAndRevokingEachCollectTheAdminPassword(t *testing.T) {
	// A remembered administrator session reads the list. Minting or destroying
	// a credential is a security change and re-verifies the password each
	// time, the same rule the settings screen follows.
	r := newRenderer(t)
	out := render(t, r, helperPage(fullChrome(LangEN), false))

	forms := 0
	for _, form := range strings.Split(out, `method="post"`)[1:] {
		if end := strings.Index(form, "</form>"); end >= 0 {
			form = form[:end]
		}
		forms++
		if !strings.Contains(form, `name="admin_password"`) {
			t.Errorf("a credential form does not re-verify the administrator password: %.160s", form)
		}
		if !strings.Contains(form, `name="csrf"`) {
			t.Errorf("a credential form has no CSRF token: %.160s", form)
		}
	}
	// One issue form plus one revoke form per active credential.
	if forms < 3 {
		t.Errorf("rendered %d credential forms, want an issue form and one per active credential", forms)
	}
	if !strings.Contains(out, wantText(LangEN, MsgHelperPasswordEachTime)) {
		t.Error("the screen does not say the password is required for each operation")
	}
}

func TestHelperCredentialLabelShowsTheStoredUTF8ByteBoundary(t *testing.T) {
	r := newRenderer(t)
	expected := map[Lang]string{
		LangEN: "Enter a single-line label between 1 and 100 UTF-8 bytes.",
		LangKO: "한 줄 이름을 UTF-8 기준 100바이트 이내로 입력하세요. 한글만 쓰면 최대 33자입니다.",
	}
	for _, lang := range Langs() {
		page := helperPage(fullChrome(lang), false)
		page.PendingAction = ActionIssueHelperCredential
		page.Chrome.Notices = []Notice{Error("label", MsgHelperLabelInvalid)}
		out := render(t, r, page)
		tag := regexp.MustCompile(`<input id="helper-label"[^>]*>`).FindString(out)
		for _, attribute := range []string{`name="label"`, `type="text"`, `required`, `maxlength="100"`, `autocomplete="off"`} {
			if !strings.Contains(tag, attribute) {
				t.Errorf("%s label input lost %s: %s", lang, attribute, tag)
			}
		}
		if strings.Contains(tag, `maxlength="200"`) {
			t.Errorf("%s label input still promises 200 characters", lang)
		}
		if got := wantText(lang, MsgHelperLabelInvalid); got != expected[lang] {
			t.Errorf("%s invalid-label text=%q, want %q", lang, got, expected[lang])
		}
		if !strings.Contains(out, expected[lang]) {
			t.Errorf("%s invalid-label text was not rendered", lang)
		}
		if !strings.Contains(out, wantText(lang, MsgHelperLabelHelp)) {
			t.Errorf("%s label help intent disappeared", lang)
		}
	}
}

func TestEveryAdminPasswordFieldOnTheCredentialScreenIsUnique(t *testing.T) {
	// Several forms collect the same field name. Duplicate ids would point
	// every aria-describedby at the first one, announcing the wrong form's
	// error.
	r := newRenderer(t)
	out := render(t, r, helperPage(fullChrome(LangEN), true))

	ids := regexp.MustCompile(`id="([^"]+)"`).FindAllStringSubmatch(out, -1)
	seen := map[string]bool{}
	for _, m := range ids {
		if seen[m[1]] {
			t.Errorf("duplicate id %q on the credential screen", m[1])
		}
		seen[m[1]] = true
	}
}

func TestIssuedTokenIsShownOnceAndNeverPersisted(t *testing.T) {
	// The secret exists in this one response. It must never reach a URL, a
	// link, browser storage, or the page's own scripting, because each of
	// those outlives the response and is readable afterwards.
	r := newRenderer(t)
	out := render(t, r, helperPage(fullChrome(LangEN), true))

	if n := strings.Count(out, helperTestToken); n != 1 {
		t.Fatalf("the token appears %d times, want exactly once", n)
	}
	// Not in any address: a query string or href would put it in history and
	// in the referrer of the next request.
	for _, m := range regexp.MustCompile(`(?:href|src|action|formaction)="([^"]*)"`).FindAllStringSubmatch(out, -1) {
		if strings.Contains(m[1], helperTestToken) {
			t.Errorf("the token reached an address: %s", m[1])
		}
	}
	// Not submitted back to the server by any form.
	if strings.Contains(out, `value="`+helperTestToken+`"`) {
		t.Error("the token is carried in a form value")
	}
	// Not handed to scripting for storage.
	for _, api := range []string{"localStorage", "sessionStorage", "indexedDB", "document.cookie"} {
		if strings.Contains(out, api) {
			t.Errorf("the credential screen references %s", api)
		}
	}
	if !strings.Contains(out, wantText(LangEN, MsgHelperTokenOnce)) {
		t.Error("the screen does not warn that this is the only time the secret is shown")
	}
}

func TestReturningToTheCredentialScreenHasNoToken(t *testing.T) {
	// A later GET shows the credential without its secret, which is what makes
	// the one-time warning true.
	r := newRenderer(t)
	out := render(t, r, helperPage(fullChrome(LangEN), false))

	if strings.Contains(out, helperTestToken) {
		t.Error("the token survived into a later view of the screen")
	}
	if strings.Contains(out, wantText(LangEN, MsgHelperTokenTitle)) {
		t.Error("a later view still shows the one-time secret panel")
	}
	// The credential itself is still listed.
	if !strings.Contains(out, "dev-mini") {
		t.Error("the credential list is missing after the issuing response")
	}
}

func TestCredentialScreenLanguageLinkIsAFollowableGET(t *testing.T) {
	// The issuing response is a POST result. A language link built from the
	// request URL would be a GET at a route that refuses GET, and following it
	// would lose the screen.
	page := helperPage(fullChrome(LangEN), true)
	page.Chrome.CurrentURL = "/repositories/r1/helper-credentials"
	if got := canonicalURL(page, page.Chrome); got != page.SelfURL {
		t.Errorf("language links use %q, want the screen's own address %q", got, page.SelfURL)
	}

	pr := pullRequestPage(fullChrome(LangEN), prFixtureFailing)
	pr.Chrome.CurrentURL = "/repositories/r1/pull-requests/12/merge"
	if got := canonicalURL(pr, pr.Chrome); got != pr.SelfURL {
		t.Errorf("a refused merge sends language links to %q, want %q", got, pr.SelfURL)
	}
}

func TestRevokedCredentialsStayVisibleAsARecord(t *testing.T) {
	r := newRenderer(t)
	out := render(t, r, helperPage(fullChrome(LangEN), false))

	if !strings.Contains(out, "old-ci") {
		t.Error("a revoked credential disappeared, erasing the record of its access")
	}
	if !strings.Contains(out, wantText(LangEN, MsgHelperRevoked)) {
		t.Error("a revoked credential is not labelled as revoked")
	}
	// A revoked credential offers no revoke control.
	revoked := out[strings.Index(out, "old-ci"):]
	if end := strings.Index(revoked, `class="cred`); end > 0 {
		revoked = revoked[:end]
	}
	if strings.Contains(revoked, wantText(LangEN, MsgHelperRevoke)+"</button>") {
		t.Error("a revoked credential still offers a revoke control")
	}
	// Never used is stated rather than left blank, because it is how an unused
	// credential gets noticed.
	if !strings.Contains(out, wantText(LangEN, MsgHelperNeverUsed)) {
		t.Error("a credential that was never used does not say so")
	}
}

func TestNoServerSecretReachesTheEvidenceScreens(t *testing.T) {
	// General repository sessions read task results without a helper
	// credential, and no page may embed a bearer token or server secret.
	r := newRenderer(t)
	var all strings.Builder
	for _, lang := range Langs() {
		for name, page := range allPages(lang) {
			if strings.HasPrefix(name, "helper-credentials") {
				continue
			}
			all.WriteString(render(t, r, page))
		}
	}
	out := all.String()
	for _, secret := range []string{helperTestToken, "Bearer ", "Authorization:", "X-Owngit-Admin-Password"} {
		if strings.Contains(out, secret) {
			t.Errorf("a credential or header value (%q) reached an ordinary screen", secret)
		}
	}
}

// version

func TestRunningVersionComesFromTheBackendOnly(t *testing.T) {
	// The version on screen is whatever the backend passed from the binary's
	// own version source. This package must contain no version literal of its
	// own, because a stale copy here would be a confident lie.
	r := newRenderer(t)
	c := fullChrome(LangEN)
	c.Version = "9.9.9-test"
	if out := render(t, r, OverviewPage{Chrome: c, Activity: sampleGraph()}); !strings.Contains(out, "9.9.9-test") {
		t.Error("the version the backend supplied is not shown")
	}

	// With nothing supplied, nothing is claimed.
	c.Version = ""
	out := render(t, r, OverviewPage{Chrome: c, Activity: sampleGraph()})
	if strings.Contains(out, "toolbar__ver") {
		t.Error("an absent version renders a version element anyway")
	}
	if regexp.MustCompile(`\b\d+\.\d+\.\d+\b`).MatchString(out) {
		t.Error("a version number appeared without the backend supplying one")
	}
}

func TestVersionIsVisibleOnEveryDashboardScreen(t *testing.T) {
	// fullChrome carries a version, so every screen built on it proves the
	// answer to "which OwnGit is this" is reachable without a terminal.
	r := newRenderer(t)
	for _, lang := range Langs() {
		for name, page := range allPages(lang) {
			chrome, ok := chromeOf(page)
			if !ok || chrome.Version == "" {
				continue // setup and sign-in render without the dashboard chrome
			}
			if out := render(t, r, page); !strings.Contains(out, chrome.Version) {
				t.Errorf("%s/%s: the running version is not visible", lang, name)
			}
		}
	}
}

func TestPackageContainsNoVersionLiteral(t *testing.T) {
	// A version written here would drift from the binary's own version and
	// state it with total confidence. There is one source, and it is the
	// backend's.
	number := regexp.MustCompile(`\b\d+\.\d+\.\d+\b`)
	paths, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	templates, err := filepath.Glob("templates/*.html")
	if err != nil {
		t.Fatal(err)
	}
	pages, err := filepath.Glob("templates/pages/*.html")
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range append(append(paths, templates...), pages...) {
		if strings.HasSuffix(path, "_test.go") {
			continue // fixtures deliberately carry a synthetic version
		}
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		for _, line := range strings.Split(string(data), "\n") {
			// Example text in comments names real Git and font versions, which
			// are not claims about OwnGit.
			if strings.Contains(line, "//") || strings.Contains(line, "{{/*") {
				continue
			}
			if hit := number.FindString(line); hit != "" {
				t.Errorf("%s: version-like literal %q in package source", path, hit)
			}
		}
	}
}

// attempts registered before they run

func TestARegisteredAttemptIsNotAResult(t *testing.T) {
	// The backend registers an attempt before executing it, so a record can
	// exist with no outcome. That state must not borrow the appearance of a
	// finished run, and it must not claim a process is currently running:
	// nothing in this package observes one.
	r := newRenderer(t)
	page := pullRequestPage(fullChrome(LangEN), prFixtureFailing)
	page.Checks = CheckEvidence{
		Status: CheckPending, Configured: true, Advisory: true,
		RevisionShortOID: "7f2c1a0", RegisteredAt: testNow.Add(-2 * time.Minute),
		WorktreeState: WorktreeClean,
		// A registration the backend wrote before running anything.
		Summary: "",
	}
	out := render(t, r, page)

	if strings.Contains(out, wantText(LangEN, MsgCheckStatePassed)) {
		t.Error("a registered attempt with no result rendered as passed")
	}
	if strings.Contains(out, wantText(LangEN, MsgCheckStateFailed)) {
		t.Error("a registered attempt with no result rendered as failed")
	}
	if !strings.Contains(out, wantText(LangEN, MsgCheckStatePending)) {
		t.Error("a registered attempt does not say it has no result yet")
	}
	if !strings.Contains(out, wantText(LangEN, MsgCheckPendingDetail)) {
		t.Error("the screen does not say it cannot tell whether the run is still going")
	}
	// It must not claim the commit was tested, because nothing has run.
	if strings.Contains(out, wantText(LangEN, MsgCheckTestedCommit)) {
		t.Error("a pending attempt claims its commit was the code that ran")
	}
	// And it must never disable merging.
	if merge := formNamed(t, out, "/merge"); strings.Contains(merge, "disabled") {
		t.Error("a pending attempt blocked the merge control")
	}
}

func TestAPendingAttemptStatesNoDurationOrOutcome(t *testing.T) {
	r := newRenderer(t)
	page := tasksPage(fullChrome(LangEN), true)
	pending := AttemptRecord{
		ID: "att_pending01", ShortID: "att_pending01", Status: CheckPending,
		RevisionShortOID: "7f2c1a0", WorktreeState: WorktreeClean,
		StartedAt: testNow.Add(-1 * time.Minute),
	}
	page.Detail.Attempts = []AttemptRecord{pending}
	out := render(t, r, page)

	if pending.TestedCommit() {
		t.Error("a pending attempt reports a tested commit from its clean tree alone")
	}
	if !strings.Contains(out, wantText(LangEN, MsgCheckStatePending)) {
		t.Error("a pending run is not labelled as having no result yet")
	}
	// A zero duration would read as an instant run.
	if strings.Contains(out, wantText(LangEN, MsgAttemptDuration)) {
		t.Error("a pending run states a duration it does not have")
	}
}

func TestAnUnreadableCheckRecordIsItsOwnState(t *testing.T) {
	// Three different facts: no run, a run that reported an unavailable
	// environment, and a record that could not be read at all.
	r := newRenderer(t)
	page := pullRequestPage(fullChrome(LangEN), prFixtureFailing)
	page.Checks = CheckEvidence{
		Status: CheckAbsent, Configured: true, Advisory: true,
		ReadFailure: &EvidenceReadFailure{Code: ReadFailureCheckEvidence, Message: MsgCheckRecordUnreadable},
	}
	out := render(t, r, page)

	if !strings.Contains(out, wantText(LangEN, MsgCheckRecordUnreadable)) {
		t.Error("the check read failure is hidden")
	}
	if strings.Contains(out, wantText(LangEN, MsgCheckStatePassed)) {
		t.Error("an unreadable check record rendered as passed")
	}
}

func TestEvidenceReadFailuresAreLocalizedWithoutInventingAbsentEvidence(t *testing.T) {
	r := newRenderer(t)
	bilingual := func(code MessageCode, lang Lang) string {
		return `data-en="` + wantText(LangEN, code) + `" data-ko="` + wantText(LangKO, code) + `">` + wantText(lang, code) + `</span>`
	}
	for _, lang := range Langs() {
		for _, test := range []struct {
			name    string
			failure EvidenceReadFailure
		}{
			{name: "configuration", failure: EvidenceReadFailure{Code: ReadFailureCheckConfiguration, Message: MsgCheckConfigurationUnreadable}},
			{name: "evidence", failure: EvidenceReadFailure{Code: ReadFailureCheckEvidence, Message: MsgCheckRecordUnreadable}},
		} {
			page := pullRequestPage(fullChrome(lang), prFixtureFailing)
			page.Checks = CheckEvidence{Status: CheckAbsent, Advisory: true, ReadFailure: &test.failure}
			out := render(t, r, page)
			if !strings.Contains(out, bilingual(test.failure.Message, lang)) {
				t.Errorf("%s/%s check read failure is not rendered in the active language", lang, test.name)
			}
			if strings.Contains(out, wantText(LangEN, MsgCheckNotConfigured)) || strings.Contains(out, wantText(LangKO, MsgCheckNotConfigured)) {
				t.Errorf("%s/%s unknown configuration claims no checks are configured", lang, test.name)
			}
			if strings.Contains(out, bilingual(MsgCheckStateUnavailable, lang)) {
				t.Errorf("%s/%s read failure is shown as an unavailable execution environment", lang, test.name)
			}
		}

		page := pullRequestPage(fullChrome(lang), prFixtureFailing)
		page.Merge = MergeAvailability{Eligible: true}
		page.Review = ReviewEvidence{ReadFailure: &EvidenceReadFailure{Code: ReadFailureReviewEvidence, Message: MsgReviewRecordUnreadable}}
		out := render(t, r, page)
		if !strings.Contains(out, bilingual(MsgReviewRecordUnreadable, lang)) {
			t.Errorf("%s review read failure is not rendered in the active language", lang)
		}
		if strings.Contains(out, bilingual(MsgReviewStateUnavailable, lang)) {
			t.Errorf("%s review read failure is shown as a reviewer execution failure", lang)
		}
		if merge := formNamed(t, out, "/merge"); strings.Contains(merge, "disabled") {
			t.Errorf("%s review read failure became a merge gate", lang)
		}
	}

	const externalDetail = "provider returned 503"
	external := pullRequestPage(fullChrome(LangKO), prFixtureFailing)
	external.Review = ReviewEvidence{
		Status: ReviewUnavailable, BoundToCurrentRevision: true,
		Provenance: ReviewFromExternalTool, Detail: externalDetail, SubmittedAt: testNow,
	}
	if out := render(t, r, external); !strings.Contains(out, externalDetail) {
		t.Error("quoted external review evidence was translated or hidden")
	}
}

// correction rounds

func TestCorrectionRoundsExplainWhatConsumesThem(t *testing.T) {
	// A round is reserved before it runs and a successful corrective round
	// still counts, while a manual rerun does not. Without saying so, the
	// number looks like a failure counter.
	r := newRenderer(t)
	out := render(t, r, tasksPage(fullChrome(LangEN), true))

	for _, want := range []MessageCode{MsgTaskRoundsHelp, MsgTaskManualRerun, MsgTaskBudgetHelp} {
		if !strings.Contains(out, wantText(LangEN, want)) {
			t.Errorf("the task screen does not explain %q", want)
		}
	}
}

func TestZeroRoundsBeforeTheFirstCheckIsNotASuccess(t *testing.T) {
	// Zero rounds used means nothing was corrected. Before the first check
	// exists it also means nothing was measured, which is a different thing
	// from passing without needing correction.
	r := newRenderer(t)
	page := tasksPage(fullChrome(LangEN), true)
	page.Detail.Task.Status = TaskActive
	page.Detail.Task.CyclesUsed = 0
	page.Detail.Task.InitialCheckDone = false
	out := render(t, r, page)

	if !strings.Contains(out, wantText(LangEN, MsgTaskInitialNone)) {
		t.Error("a task with no first check does not say its work is unmeasured")
	}

	page.Detail.Task.InitialCheckDone = true
	if out := render(t, r, page); strings.Contains(out, wantText(LangEN, MsgTaskInitialNone)) {
		t.Error("a task whose first check ran still claims it was never measured")
	}
}

// defects found by looking at the rendered screen

func TestAnApprovalForAnotherRevisionIsNotAnApprovalChip(t *testing.T) {
	// Found in a screenshot of the finished screen: an approval for a commit
	// that was no longer the tip rendered as a green "Approved" chip, with the
	// correction only in the sentences below it. The chip is what a reader
	// takes at a glance, so it has to carry the same truth as the text.
	r := newRenderer(t)
	out := render(t, r, pullRequestPage(fullChrome(LangEN), prFixtureFailing))

	if strings.Contains(out, wantText(LangEN, MsgReviewStateApproved)) {
		t.Error("a review of an earlier revision is presented as an approval")
	}
	if !strings.Contains(out, wantText(LangEN, MsgReviewStateNone)) {
		t.Error("a review that does not cover this revision is not stated as such")
	}
	// The record itself is still shown, so the reader can see what exists.
	for _, want := range []string{"codex", "5d0aa13"} {
		if !strings.Contains(out, want) {
			t.Errorf("the earlier review's detail %q was dropped instead of qualified", want)
		}
	}
	if !strings.Contains(out, wantText(LangEN, MsgReviewOtherRevision)) {
		t.Error("the screen does not say which revision the review belongs to")
	}

	// The same record bound to the current revision is a real approval.
	page := pullRequestPage(fullChrome(LangEN), prFixtureFailing)
	page.Review.BoundToCurrentRevision = true
	if out := render(t, r, page); !strings.Contains(out, wantText(LangEN, MsgReviewStateApproved)) {
		t.Error("an approval for this revision is not shown as an approval")
	}
}

func TestPullRequestChangesAreNotDescribedAsFuturePlans(t *testing.T) {
	// Also found by reading the rendered screen: the change list reused the
	// restore screen's wording, so a pull request that already contains its
	// change announced the file "will be changed".
	r := newRenderer(t)
	for _, lang := range Langs() {
		out := render(t, r, pullRequestPage(fullChrome(lang), prFixtureFailing))
		for _, future := range []MessageCode{
			MsgRestoreStatusAdded, MsgRestoreStatusModified, MsgRestoreStatusDeleted,
		} {
			if strings.Contains(out, wantText(lang, future)) {
				t.Errorf("%s: the change list describes existing changes as planned (%q)", lang, future)
			}
		}
		if !strings.Contains(out, wantText(lang, MsgPRChangeModified)) {
			t.Errorf("%s: a changed file is not labelled", lang)
		}
	}

	// The restore screen keeps its own future-tense wording.
	if out := render(t, r, restorePage(fullChrome(LangEN), true)); !strings.Contains(out, wantText(LangEN, MsgRestoreStatusModified)) {
		t.Error("the restore screen lost its own wording")
	}
}

func TestBackendLogDispositionMapsOntoTheRenderedVocabulary(t *testing.T) {
	// The wire says found, expired, or missing. Anything else must degrade to
	// a non-claim rather than implying the log can still be read.
	for wire, want := range map[string]string{
		"found":             LogAvailable,
		"expired":           LogExpired,
		"missing":           LogNotRecorded,
		"":                  LogUnknown,
		"some_future_state": LogUnknown,
	} {
		if got := LogStatusOf(wire); got != want {
			t.Errorf("LogStatusOf(%q) = %q, want %q", wire, got, want)
		}
	}
	// An unmapped value never reads as an available log.
	if logNote(LogStatusOf("some_future_state")) != MsgCheckLogUnknown {
		t.Error("an unmapped log disposition does not render as a non-claim")
	}
}

func TestCredentialFormsSubmitTheDocumentedActionValues(t *testing.T) {
	// The constants are the contract a handler compares against. They drifted
	// from the values the forms actually sent, so a handler written to the
	// documented names would have matched neither form.
	r := newRenderer(t)
	out := render(t, r, helperPage(fullChrome(LangEN), false))

	for _, want := range []string{
		`name="action" value="` + ActionIssueHelperCredential + `"`,
		`name="action" value="` + ActionRevokeHelperCredential + `"`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("no form submits %s", want)
		}
	}
	// Every action value on the page is one of the two constants.
	for _, m := range regexp.MustCompile(`name="action" value="([^"]*)"`).FindAllStringSubmatch(out, -1) {
		if m[1] != ActionIssueHelperCredential && m[1] != ActionRevokeHelperCredential {
			t.Errorf("a form submits the undocumented action %q", m[1])
		}
	}
}

func TestAnActiveCredentialWithAnIDCanBeRevoked(t *testing.T) {
	// Revocation used to require an extra field that no contract mentioned,
	// so a backend filling in the documented fields produced credentials
	// nobody could revoke.
	r := newRenderer(t)
	page := helperPage(fullChrome(LangEN), false)
	page.Credentials = []HelperCredentialRow{
		{ID: "hc1", Label: "dev-mini", CreatedAt: testNow},
		{ID: "hc0", Label: "old-ci", CreatedAt: testNow, RevokedAt: testNow, Revoked: true},
		// No identifier, so there is nothing to submit.
		{Label: "unidentified", CreatedAt: testNow},
	}
	out := render(t, r, page)

	if !strings.Contains(out, `name="credential_id" value="hc1"`) {
		t.Error("an active credential with an identifier offers no revocation")
	}
	if strings.Contains(out, `name="credential_id" value="hc0"`) {
		t.Error("a revoked credential still offers revocation")
	}
	if strings.Contains(out, `name="credential_id" value=""`) {
		t.Error("a credential without an identifier offers an empty revocation")
	}
}

func TestARefusedRevocationIsAnnouncedOnItsOwnRow(t *testing.T) {
	// Every row submits the same action, so the action alone cannot say which
	// row failed. Without the credential id the wrong row would claim the
	// error, or every row would.
	r := newRenderer(t)
	page := helperPage(fullChrome(LangEN), false)
	page.PendingAction = ActionRevokeHelperCredential
	page.PendingCredentialID = "hc2"
	page.Chrome.Notices = []Notice{Error("admin_password", MsgAdminFailed)}
	out := render(t, r, page)

	// Count rendered notice elements, not text occurrences: every string is
	// carried twice on the element for in-place language switching.
	if n := strings.Count(out, `class="fieldnote fieldnote--error"`); n != 1 {
		t.Errorf("the refusal is announced on %d forms, want only the row that failed", n)
	}
	if !strings.Contains(out, `id="hc2-admin_password-note"`) {
		t.Error("the refusal is not attached to the row that failed")
	}
	// It is attached to the input on that row, so reaching the field reads it.
	row := out[strings.Index(out, `value="hc2"`):]
	if end := strings.Index(row, "</form>"); end >= 0 {
		row = row[:end]
	}
	if !strings.Contains(row, `aria-invalid="true"`) {
		t.Error("the failing row's password field is not marked invalid")
	}
}

func TestContradictoryEvidenceResolvesAgainstTheReassuringReading(t *testing.T) {
	// A record can disagree with itself. When it does, the screen takes the
	// reading that does not credit the code on screen with something unproven.
	r := newRenderer(t)

	// Passed, but for an earlier revision. Stale wins: a pass for other code
	// does not answer the question this screen is asking.
	stale := pullRequestPage(fullChrome(LangEN), prFixtureFailing)
	stale.Checks.Status = CheckPassed
	stale.Checks.Stale = true
	stale.Checks.TestedCommit = true
	stale.Checks.WorktreeState = WorktreeClean
	out := render(t, r, stale)
	if strings.Contains(out, wantText(LangEN, MsgCheckStatePassed)) {
		t.Error("a passed status for an earlier revision still renders as a pass")
	}
	if !strings.Contains(out, wantText(LangEN, MsgCheckStateStale)) {
		t.Error("a stale pass is not named as stale")
	}
	if strings.Contains(out, wantText(LangEN, MsgCheckTestedCommit)) {
		t.Error("a stale result claims to describe the commit on screen")
	}

	// Passed with a dirty tree and a TestedCommit flag set anyway. The tree
	// decides: the bytes that ran were not necessarily this commit.
	for _, tree := range []string{WorktreeDirty, WorktreeUnknown, ""} {
		page := pullRequestPage(fullChrome(LangEN), prFixtureFailing)
		page.Checks.Status = CheckPassed
		page.Checks.TestedCommit = true
		page.Checks.WorktreeState = tree
		out := render(t, r, page)
		if strings.Contains(out, wantText(LangEN, MsgCheckTestedCommit)) {
			t.Errorf("worktree %q: a set flag overrode the recorded working state", tree)
		}
		if !strings.Contains(out, wantText(LangEN, MsgCheckNotTested)) {
			t.Errorf("worktree %q: the result is not qualified as unproven", tree)
		}
	}
}

func TestAnUnreadableRecordIsNotAnUnavailableEnvironment(t *testing.T) {
	// Two different facts with two different owners: OwnGit could not read its
	// own record, or a run reported that the check environment was missing.
	r := newRenderer(t)

	unreadable := pullRequestPage(fullChrome(LangEN), prFixtureFailing)
	unreadable.Checks = CheckEvidence{
		Status: CheckPassed, Configured: true, Advisory: true,
		ReadFailure:  &EvidenceReadFailure{Code: ReadFailureCheckEvidence, Message: MsgCheckRecordUnreadable},
		TestedCommit: true, WorktreeState: WorktreeClean,
	}
	out := render(t, r, unreadable)
	if !strings.Contains(out, wantText(LangEN, MsgCheckRecordUnreadable)) {
		t.Error("an unreadable record is not named")
	}
	// It must not borrow the outcome stored beside it.
	if strings.Contains(out, wantText(LangEN, MsgCheckStatePassed)) {
		t.Error("an unreadable record rendered as a pass")
	}
	if strings.Contains(out, wantText(LangEN, MsgCheckTestedCommit)) {
		t.Error("an unreadable record claims a tested commit")
	}

	// A run that reported an unavailable environment keeps its own wording.
	env := pullRequestPage(fullChrome(LangEN), prFixtureFailing)
	env.Checks.Status = CheckUnavailable
	env.Checks.ReadFailure = nil
	out = render(t, r, env)
	if !strings.Contains(out, wantText(LangEN, MsgCheckStateUnavailable)) {
		t.Error("an unavailable check environment is not named")
	}
	if strings.Contains(out, wantText(LangEN, MsgCheckRecordUnreadable)) {
		t.Error("an unavailable environment is reported as an unreadable record")
	}
}

func TestMergeIsOfferedOnlyWhenTheDecisionAndTheReasonsAgree(t *testing.T) {
	// The two fields can contradict each other. A decision that says eligible
	// while listing a reason against it is not a licence to offer the action.
	r := newRenderer(t)

	contradictory := pullRequestPage(fullChrome(LangEN), prFixtureFailing)
	contradictory.Merge = MergeAvailability{
		Eligible: true,
		Blockers: []MergeBlocker{{Code: "merge_conflict", Detail: "internal/retry/backoff.go"}},
	}
	out := render(t, r, contradictory)
	if merge := formNamed(t, out, "/merge"); !strings.Contains(merge, "disabled") {
		t.Error("a merge with a stated blocker was offered because the decision said eligible")
	}
	if !strings.Contains(out, wantText(LangEN, MsgMergeBlockedConflict)) {
		t.Error("the stated blocker is not shown")
	}

	// Agreement in the other direction is the only case that offers it.
	clean := pullRequestPage(fullChrome(LangEN), prFixtureFailing)
	clean.Merge = MergeAvailability{Eligible: true}
	if merge := formNamed(t, render(t, r, clean), "/merge"); strings.Contains(merge, "disabled") {
		t.Error("an eligible merge with no blockers was not offered")
	}
}

func TestARefusalWithoutAReasonSaysSo(t *testing.T) {
	// A heading above an empty list promises an explanation the record does
	// not contain.
	r := newRenderer(t)
	page := pullRequestPage(fullChrome(LangEN), prFixtureFailing)
	page.Merge = MergeAvailability{Eligible: false}
	out := render(t, r, page)

	if !strings.Contains(out, wantText(LangEN, MsgMergeUnexplained)) {
		t.Error("a refusal with no recorded reason does not say the reason is missing")
	}
	if strings.Contains(out, `<ul class="blockers">`) {
		t.Error("an empty reason list was rendered")
	}
	if merge := formNamed(t, out, "/merge"); !strings.Contains(merge, "disabled") {
		t.Error("an unexplained refusal still offered the merge control")
	}
}

func TestConservativeMergeHandlingAddsNoQualityGate(t *testing.T) {
	// The stricter eligibility rule must not become a back door for advisory
	// results. Every failing and pending combination still merges.
	r := newRenderer(t)
	for _, check := range []string{
		CheckFailed, CheckError, CheckCancelled, CheckIncomplete,
		CheckUnavailable, CheckStale, CheckAbsent, CheckPending,
	} {
		for _, review := range []string{
			ReviewPending, ReviewChanges, ReviewUnavailable, ReviewPartial, ReviewNotRequested,
		} {
			page := pullRequestPage(fullChrome(LangEN), prFixtureFailing)
			page.Merge = MergeAvailability{Eligible: true}
			page.Checks.Status = check
			page.Checks.ReadFailure = nil
			page.Review = ReviewEvidence{Status: review, BoundToCurrentRevision: true, SubmittedAt: testNow}
			out := render(t, r, page)

			if merge := formNamed(t, out, "/merge"); strings.Contains(merge, "disabled") {
				t.Errorf("check %q with review %q blocked the merge control", check, review)
			}
			if strings.Contains(out, wantText(LangEN, MsgMergeUnexplained)) {
				t.Errorf("check %q with review %q produced a phantom refusal", check, review)
			}
		}
	}

	// An unreadable check record is also not a blocker.
	page := pullRequestPage(fullChrome(LangEN), prFixtureFailing)
	page.Merge = MergeAvailability{Eligible: true}
	page.Checks.ReadFailure = &EvidenceReadFailure{Code: ReadFailureCheckEvidence, Message: MsgCheckRecordUnreadable}
	if merge := formNamed(t, render(t, r, page), "/merge"); strings.Contains(merge, "disabled") {
		t.Error("an unreadable check record blocked the merge control")
	}
}

func TestEveryUnknownBlockerIsDisplayed(t *testing.T) {
	// Reasons this interface has no words for must all reach the reader, not
	// just the first one.
	r := newRenderer(t)
	page := pullRequestPage(fullChrome(LangEN), prFixtureFailing)
	page.Merge = MergeAvailability{Blockers: []MergeBlocker{
		{Code: "future_reason_one"},
		{Code: "merge_conflict"},
		{Code: "future_reason_two", Detail: "extra context"},
	}}
	out := render(t, r, page)

	for _, code := range []string{"future_reason_one", "future_reason_two", "extra context"} {
		if !strings.Contains(out, code) {
			t.Errorf("blocker %q was not displayed", code)
		}
	}
	if n := strings.Count(out, `class="blocker"`); n != 3 {
		t.Errorf("%d blockers rendered, want all 3", n)
	}
}

func TestNoPageRequiresAnInlineScriptOrStyleException(t *testing.T) {
	// Inline handlers and inline style attributes both need a CSP exception
	// the real server does not grant, so anything relying on one is dead in
	// the product even though it works in a test fixture.
	//
	// This checks every attribute rather than a list of event names. The
	// earlier list omitted onfocus, and a token field shipped using it.
	r := newRenderer(t)
	javascriptURL := regexp.MustCompile(`(?i)(?:href|src|action|formaction)\s*=\s*["']?\s*javascript:`)

	for _, lang := range Langs() {
		for name, page := range allPages(lang) {
			out := render(t, r, page)
			if hit := inlineEventHandler(out); hit != "" {
				t.Errorf("%s/%s: inline event handler %q needs a CSP exception", lang, name, hit)
			}
			if hit := javascriptURL.FindString(out); hit != "" {
				t.Errorf("%s/%s: javascript: URL %q", lang, name, strings.TrimSpace(hit))
			}
			// An inline <script> or <style> block would need the same
			// exception, so the page carries neither.
			if strings.Contains(out, "<script>") {
				t.Errorf("%s/%s: inline script block", lang, name)
			}
			if strings.Contains(out, "<style") {
				t.Errorf("%s/%s: inline style block", lang, name)
			}
		}
	}
}

func TestTokenSelectionIsHandledByTheExternalScript(t *testing.T) {
	// The convenience survives the handler's removal: the script hooks the
	// same field, and the field is selectable by hand without scripting.
	r := newRenderer(t)
	out := render(t, r, helperPage(fullChrome(LangEN), true))

	if !strings.Contains(out, "data-select-on-focus") {
		t.Fatal("the token field carries no hook for the external script")
	}
	script, err := assetFS.ReadFile("assets/owngit.js")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(script), "data-select-on-focus") {
		t.Error("the external script does not implement the selection hook")
	}
	// Without scripting the value is still readable and selectable, so the
	// screen never depends on the hook.
	if !strings.Contains(out, helperTestToken) {
		t.Error("the token is not present as readable text")
	}
	if strings.Contains(out, "disabled") {
		t.Error("the token field is disabled, which would prevent manual selection")
	}
}

// htmlTag matches one element's opening tag, and inlineEventAttribute matches
// an on* attribute name inside it.
//
// Scanning tags rather than the whole document is what keeps escaped code in
// page content from being reported: a diff line reading "el.onfocus = fn" is
// text a reader sees, not a handler the browser runs. The name match is case
// insensitive because HTML attribute names are, and it stops at the equals
// sign so an unquoted value matches too.
var (
	htmlTag              = regexp.MustCompile(`<[a-zA-Z][^<>]*>`)
	inlineEventAttribute = regexp.MustCompile(`(?i)[\s"\']on[a-z]+\s*=`)
)

// inlineEventHandler returns the first inline event attribute in the document,
// or the empty string when there is none.
func inlineEventHandler(document string) string {
	for _, tag := range htmlTag.FindAllString(document, -1) {
		if match := inlineEventAttribute.FindString(tag); match != "" {
			return strings.TrimSpace(match)
		}
	}
	return ""
}

func TestStalenessIsReadFromTheStatusAndTheFlagTogether(t *testing.T) {
	// The backend can record staleness in either place. Reading only one let a
	// stale status through beside a clean tree, which printed the sentence
	// this screen must never print about other code.
	r := newRenderer(t)

	cases := []struct {
		name   string
		status string
		flag   bool
	}{
		{"status only", CheckStale, false},
		{"flag only", CheckPassed, true},
		{"both", CheckStale, true},
	}
	for _, c := range cases {
		page := pullRequestPage(fullChrome(LangEN), prFixtureFailing)
		page.Checks.Status = c.status
		page.Checks.Stale = c.flag
		page.Checks.TestedCommit = true
		page.Checks.WorktreeState = WorktreeClean
		out := render(t, r, page)

		if !strings.Contains(out, wantText(LangEN, MsgCheckStateStale)) {
			t.Errorf("%s: the chip does not say stale", c.name)
		}
		if !strings.Contains(out, wantText(LangEN, MsgCheckStaleDetail)) {
			t.Errorf("%s: the stale warning is missing", c.name)
		}
		if strings.Contains(out, wantText(LangEN, MsgCheckTestedCommit)) {
			t.Errorf("%s: stale evidence claims to describe the commit on screen", c.name)
		}
		if strings.Contains(out, wantText(LangEN, MsgCheckStatePassed)) {
			t.Errorf("%s: stale evidence renders as a pass", c.name)
		}
	}

	// Current evidence is unaffected, so the guard did not simply suppress
	// the tested-commit line everywhere.
	current := pullRequestPage(fullChrome(LangEN), prFixtureFailing)
	current.Checks.Status = CheckPassed
	current.Checks.Stale = false
	current.Checks.TestedCommit = true
	current.Checks.WorktreeState = WorktreeClean
	if out := render(t, r, current); !strings.Contains(out, wantText(LangEN, MsgCheckTestedCommit)) {
		t.Error("current clean evidence no longer states it describes the commit")
	}
}

func TestAnEmptyBlockerRefusesWithoutInventingAnExplanation(t *testing.T) {
	// A blocker with nothing in it still means something refused the merge.
	// It must not enable merging and must not render an empty bullet under a
	// heading that promises a reason.
	r := newRenderer(t)

	for _, eligible := range []bool{true, false} {
		page := pullRequestPage(fullChrome(LangEN), prFixtureFailing)
		page.Merge = MergeAvailability{Eligible: eligible, Blockers: []MergeBlocker{{}, {}}}
		out := render(t, r, page)

		if merge := formNamed(t, out, "/merge"); !strings.Contains(merge, "disabled") {
			t.Errorf("eligible=%v: an empty blocker was filtered away and merging was enabled", eligible)
		}
		if !strings.Contains(out, wantText(LangEN, MsgMergeUnexplained)) {
			t.Errorf("eligible=%v: the missing explanation is not reported", eligible)
		}
		if strings.Contains(out, `class="blocker"`) {
			t.Errorf("eligible=%v: an empty blocker rendered as an empty bullet", eligible)
		}
	}

	// A mixed list shows the reasons it has and drops only the empty ones,
	// without claiming the explanation is missing.
	mixed := pullRequestPage(fullChrome(LangEN), prFixtureFailing)
	mixed.Merge = MergeAvailability{Blockers: []MergeBlocker{
		{},
		{Code: "merge_conflict"},
		{Detail: "detail with no code"},
		{},
	}}
	out := render(t, r, mixed)
	if n := strings.Count(out, `class="blocker"`); n != 2 {
		t.Errorf("%d blockers rendered from a mixed list, want the 2 that say something", n)
	}
	if strings.Contains(out, wantText(LangEN, MsgMergeUnexplained)) {
		t.Error("a list that does explain something reported a missing explanation")
	}
	if !strings.Contains(out, "detail with no code") {
		t.Error("a blocker carrying only a detail was dropped")
	}
	if merge := formNamed(t, out, "/merge"); !strings.Contains(merge, "disabled") {
		t.Error("a refused merge was offered")
	}
}

func TestInlineHandlerDetectionIgnoresCaseQuotingAndEscapedText(t *testing.T) {
	// The earlier pattern matched lowercase quoted attributes only, so an
	// uppercase or unquoted handler would have shipped unnoticed. It must
	// still leave escaped code in page text alone, since that is content a
	// reader sees rather than something the browser runs.
	caught := []string{
		`<textarea onfocus="this.select()">`,
		`<textarea ONFOCUS="this.select()">`,
		`<textarea onFocus="this.select()">`,
		`<textarea onfocus=this.select()>`,
		`<button onclick = "go()">`,
		`<form onsubmit='return false'>`,
		`<img src="x" ONERROR=alert(1)>`,
	}
	for _, markup := range caught {
		if inlineEventHandler(markup) == "" {
			t.Errorf("an inline handler went undetected: %s", markup)
		}
	}

	ignored := []string{
		`<pre>el.onfocus = function () {};</pre>`,
		`<pre>&lt;div onclick=&quot;go()&quot;&gt;</pre>`,
		`<span class="mono">form.onsubmit</span>`,
		`<input data-on="x">`,
		`<a href="/search?on=1">link</a>`,
		`<label for="button-only">x</label>`,
	}
	for _, markup := range ignored {
		if hit := inlineEventHandler(markup); hit != "" {
			t.Errorf("harmless markup reported as a handler (%q): %s", hit, markup)
		}
	}
}

func TestAKnownCleanupFailureCannotRenderAPass(t *testing.T) {
	// The checks can pass while the run leaves a process behind. Contradictory
	// status metadata saying "passed" does not settle it: the reader is being
	// told the machine is clean, and it is not.
	r := newRenderer(t)

	evidence := render(t, r, uncleanPullRequestPage(fullChrome(LangEN)))
	if strings.Contains(evidence, wantText(LangEN, MsgCheckStatePassed)) {
		t.Error("check evidence with a cleanup failure renders as a plain pass")
	}
	if !strings.Contains(evidence, wantText(LangEN, MsgCheckStateUnclean)) {
		t.Error("check evidence does not name the cleanup failure in its status")
	}
	if !strings.Contains(evidence, wantText(LangEN, MsgCheckCleanupFailed)) {
		t.Error("check evidence does not warn about the unconfirmed cleanup")
	}
	if strings.Contains(evidence, wantText(LangEN, MsgCheckTestedCommit)) {
		t.Error("a run that did not clean up claims to have tested the commit cleanly")
	}
	if !strings.Contains(evidence, wantText(LangEN, MsgCheckNotTested)) {
		t.Error("the tested-commit claim is withheld without saying so")
	}

	// The same rule on an attempt, both from a result row and from the
	// aggregate alone.
	attempts := render(t, r, uncleanTasksPage(fullChrome(LangEN)))
	for i, head := range attemptHeads(t, attempts) {
		if strings.Contains(head, wantText(LangEN, MsgCheckStatePassed)) {
			t.Errorf("attempt %d with a cleanup failure renders as a plain pass", i)
		}
		if !strings.Contains(head, wantText(LangEN, MsgCheckStateUnclean)) {
			t.Errorf("attempt %d does not name the cleanup failure in its status", i)
		}
	}
	// The line that did clean up keeps its own passing status, so the rule
	// applies where the failure was recorded rather than across the run.
	if !strings.Contains(attempts, wantText(LangEN, MsgCheckStatePassed)) {
		t.Error("a check with no cleanup failure of its own lost its passing status")
	}
	if n := strings.Count(attempts, wantText(LangEN, MsgCheckCleanupFailed)); n < 2 {
		t.Errorf("%d attempts warned about cleanup, want both the row-detailed and aggregate-only ones", n)
	}
	if strings.Contains(attempts, wantText(LangEN, MsgCheckTestedCommit)) {
		t.Error("an attempt that did not clean up claims a tested commit")
	}
}

func TestTheActualExitCodeSurvivesACleanupFailure(t *testing.T) {
	// Withholding the word "passed" must not also hide what the command
	// actually returned. Zero is a real value here, not an absent one.
	r := newRenderer(t)
	out := render(t, r, uncleanTasksPage(fullChrome(LangEN)))

	// Scoped to the row that failed to clean up, since another row also
	// exited zero and would mask the loss.
	row := resultNamed(t, out, "test")
	if !strings.Contains(row, wantText(LangEN, MsgAttemptExitCode)) {
		t.Error("the cleanup-failed row shows no exit code")
	}
	if !strings.Contains(row, `<span class="mono">0</span>`) {
		t.Error("exit code 0 was dropped from the row whose cleanup failed")
	}
	// The passing output is still the run's own output.
	if !strings.Contains(row, "ok  owngit/internal/retry") {
		t.Error("the recorded output was dropped")
	}
	if !strings.Contains(row, wantText(LangEN, MsgCheckCleanupRow)) {
		t.Error("the scoped row is not the one that failed to clean up")
	}
}

func TestTheCleanupReasonIsShownSeparatelyAndEscaped(t *testing.T) {
	// The reason is backend text about a process, so it belongs beside the
	// output rather than inside the box a reader takes for command output.
	// Like every recorded string, it is escaped.
	r := newRenderer(t)
	out := render(t, r, uncleanTasksPage(fullChrome(LangEN)))

	if !strings.Contains(out, "killpg 40211: operation not permitted") {
		t.Fatal("the recorded cleanup reason is not shown")
	}
	if strings.Contains(out, `killpg 40211: operation not permitted <&"'>`) {
		t.Error("the cleanup reason was emitted as raw markup")
	}
	if !strings.Contains(out, "&lt;&amp;&#34;&#39;&gt;") {
		t.Error("the cleanup reason is not HTML escaped")
	}
	// It sits outside the output box, so it cannot be read as something the
	// check printed.
	for _, box := range strings.Split(out, `<pre class="result__out">`)[1:] {
		printed := box[:strings.Index(box, "</pre>")]
		if strings.Contains(printed, "killpg") {
			t.Error("the cleanup reason was placed inside the command output box")
		}
	}
	// Both languages label it.
	for _, lang := range Langs() {
		if !strings.Contains(render(t, r, uncleanTasksPage(fullChrome(lang))), wantText(lang, MsgCheckCleanupRow)) {
			t.Errorf("%s: the cleanup reason carries no label", lang)
		}
	}
}

func TestCleanupFailureNeverBlocksAMerge(t *testing.T) {
	// Check evidence is advisory, and a cleanup failure is check evidence.
	r := newRenderer(t)
	page := uncleanPullRequestPage(fullChrome(LangEN))
	page.Merge = MergeAvailability{Eligible: true}
	out := render(t, r, page)

	if merge := formNamed(t, out, "/merge"); strings.Contains(merge, "disabled") {
		t.Error("a cleanup failure blocked the merge control")
	}
	if strings.Contains(out, wantText(LangEN, MsgMergeUnexplained)) {
		t.Error("a cleanup failure produced a phantom refusal")
	}
	if !strings.Contains(out, wantText(LangEN, MsgEvidenceAdvisory)) {
		t.Error("the advisory contract is no longer stated beside the result")
	}
}

func TestCleanupHandlingPreservesStaleAndUnavailableDistinctions(t *testing.T) {
	// The new rule sits alongside the earlier ones rather than replacing them.
	r := newRenderer(t)

	stale := uncleanPullRequestPage(fullChrome(LangEN))
	stale.Checks.Stale = true
	out := render(t, r, stale)
	if !strings.Contains(out, wantText(LangEN, MsgCheckStateStale)) {
		t.Error("stale evidence with a cleanup failure lost its stale status")
	}
	if !strings.Contains(out, wantText(LangEN, MsgCheckStaleDetail)) {
		t.Error("the stale warning disappeared")
	}
	if !strings.Contains(out, wantText(LangEN, MsgCheckCleanupFailed)) {
		t.Error("the cleanup warning disappeared behind the stale status")
	}

	unreadable := uncleanPullRequestPage(fullChrome(LangEN))
	unreadable.Checks.ReadFailure = &EvidenceReadFailure{Code: ReadFailureCheckEvidence, Message: MsgCheckRecordUnreadable}
	out = render(t, r, unreadable)
	if !strings.Contains(out, wantText(LangEN, MsgCheckRecordUnreadable)) {
		t.Error("an unreadable record with a cleanup failure lost its own wording")
	}
	if strings.Contains(out, wantText(LangEN, MsgCheckStatePassed)) {
		t.Error("an unreadable record rendered as a pass")
	}

	// A clean run is unaffected, so the guard did not suppress passes broadly.
	clean := uncleanPullRequestPage(fullChrome(LangEN))
	clean.Checks.CleanupFailed = false
	out = render(t, r, clean)
	if !strings.Contains(out, wantText(LangEN, MsgCheckStatePassed)) {
		t.Error("a run that did clean up no longer renders as a pass")
	}
	if strings.Contains(out, wantText(LangEN, MsgCheckCleanupFailed)) {
		t.Error("a clean run warns about cleanup")
	}
	if !strings.Contains(out, wantText(LangEN, MsgCheckTestedCommit)) {
		t.Error("a clean run no longer claims its tested commit")
	}
}

func TestAttemptSequenceIsPresentedAsRepositoryOrder(t *testing.T) {
	// The number is server registration order across the repository, so it
	// must not be offered as a count of this task's runs.
	r := newRenderer(t)
	out := render(t, r, uncleanTasksPage(fullChrome(LangEN)))

	if !strings.Contains(out, wantText(LangEN, MsgAttemptOrder)) {
		t.Fatal("the attempt sequence is not labelled")
	}
	if !strings.Contains(out, wantText(LangEN, MsgAttemptOrderGap)) {
		t.Error("the gap between numbers within one task is not explained")
	}
	// The fixture's two attempts are 47 and 46 on a task with three cycles
	// used, so the number cannot be mistaken for an execution count.
	for _, n := range []string{"47", "46"} {
		if !strings.Contains(out, `<span class="mono">`+n+`</span>`) {
			t.Errorf("sequence %s is not shown", n)
		}
	}
	// A backend that states no order renders nothing rather than a zero.
	none := uncleanTasksPage(fullChrome(LangEN))
	for i := range none.Detail.Attempts {
		none.Detail.Attempts[i].Sequence = 0
	}
	if strings.Contains(render(t, r, none), wantText(LangEN, MsgAttemptOrder)) {
		t.Error("an unstated order rendered a label anyway")
	}
}

// attemptHeads returns the head block of each rendered attempt, which is where
// the attempt's own status chip lives.
func attemptHeads(t *testing.T, document string) []string {
	t.Helper()
	var heads []string
	for _, part := range strings.Split(document, `<div class="attempt__head">`)[1:] {
		end := strings.Index(part, "</div>")
		if end < 0 {
			t.Fatal("an attempt head was never closed")
		}
		heads = append(heads, part[:end])
	}
	if len(heads) == 0 {
		t.Fatal("no attempts were rendered")
	}
	return heads
}

// resultNamed returns the rendered block for one check inside an attempt.
func resultNamed(t *testing.T, document, name string) string {
	t.Helper()
	for _, part := range strings.Split(document, `<div class="result">`)[1:] {
		if end := strings.Index(part, `<div class="result">`); end >= 0 {
			part = part[:end]
		}
		if strings.Contains(part, `<span class="result__n">`+name+`</span>`) {
			return part
		}
	}
	t.Fatalf("no rendered result named %q", name)
	return ""
}

func TestTheUncleanStatusIsANonSuccessLabelInBothLanguages(t *testing.T) {
	// A label reading "Passed, cleanup failed" would undo the rule the rest of
	// this file enforces, so the words themselves are checked rather than the
	// constant's current value.
	english := catalog[MsgCheckStateUnclean].en
	korean := catalog[MsgCheckStateUnclean].ko

	if english != "Cleanup failed" {
		t.Errorf("English unclean status is %q, want a non-success label", english)
	}
	if korean != "정리 실패" {
		t.Errorf("Korean unclean status is %q, want a non-success label", korean)
	}

	// No wording that asserts the checks succeeded.
	for _, banned := range []string{"Passed", "passed", "Pass", "Success", "succeeded", "OK"} {
		if strings.Contains(english, banned) {
			t.Errorf("English unclean status claims success with %q: %q", banned, english)
		}
	}
	for _, banned := range []string{"통과", "성공", "정상"} {
		if strings.Contains(korean, banned) {
			t.Errorf("Korean unclean status claims success with %q: %q", banned, korean)
		}
	}

	// The status must reach the screen in both languages, and the passing
	// label must not appear anywhere on the evidence.
	r := newRenderer(t)
	for _, lang := range Langs() {
		out := render(t, r, uncleanPullRequestPage(fullChrome(lang)))
		if !strings.Contains(out, wantText(lang, MsgCheckStateUnclean)) {
			t.Errorf("%s: the unclean status is not rendered", lang)
		}
		if strings.Contains(out, wantText(lang, MsgCheckStatePassed)) {
			t.Errorf("%s: the passing label appears beside a failed cleanup", lang)
		}
	}
}

func TestCleanupWordingClaimsNoMoreThanTheRecordEstablishes(t *testing.T) {
	// The record says one thing: OwnGit did not confirm the processes it
	// started had stopped. It does not establish that they are still running,
	// that work continues, or that the machine was restored.
	warning := catalog[MsgCheckCleanupFailed]

	for _, overreach := range []string{
		"still running", "is running", "still doing work", "restored", "clean machine",
	} {
		if strings.Contains(strings.ToLower(warning.en), overreach) {
			t.Errorf("English cleanup warning asserts %q: %q", overreach, warning.en)
		}
	}
	for _, overreach := range []string{"복원", "정상으로", "실행 중입니다"} {
		if strings.Contains(warning.ko, overreach) {
			t.Errorf("Korean cleanup warning asserts %q: %q", overreach, warning.ko)
		}
	}

	// It is scoped to processes this run owned, not to the machine at large.
	if !strings.Contains(warning.en, "this run started") {
		t.Errorf("English cleanup warning is not scoped to owned processes: %q", warning.en)
	}
	if !strings.Contains(warning.ko, "이 실행이 시작한") {
		t.Errorf("Korean cleanup warning is not scoped to owned processes: %q", warning.ko)
	}
}

// The pull request summary

// TestEveryEvidenceKindKeepsItsOwnSummaryRow checks that the compact summary
// did not merge two answers into one verdict.
//
// A check run and an opinion a reviewer submitted answer different questions.
// The one with nothing to say has to stay visible: a missing review must not
// disappear because the checks passed.
func TestEveryEvidenceKindKeepsItsOwnSummaryRow(t *testing.T) {
	r := newRenderer(t)
	page := pullRequestPage(fullChrome(LangEN), prFixtureFailing)
	page.Review = ReviewEvidence{}
	out := render(t, r, page)

	for _, code := range []MessageCode{MsgCheckTitle, MsgReviewTitle} {
		if !strings.Contains(out, wantText(LangEN, code)) {
			t.Errorf("%s has no row of its own", code)
		}
	}
	// Absent is a state, not a blank.
	if !strings.Contains(out, wantText(LangEN, MsgReviewStateNone)) {
		t.Error("a missing review is not named on the summary")
	}
}

// TestRevisionRelevanceStaysOnTheDefaultScreen checks the one fact the
// compact layout may never fold away.
//
// A result about an earlier revision is a different subject rather than a
// weaker answer, and a record that could not be read is not an absence. Both
// have to be readable without opening the disclosure, so this asserts they
// appear before it in the document.
func TestRevisionRelevanceStaysOnTheDefaultScreen(t *testing.T) {
	r := newRenderer(t)

	staleCheck := pullRequestPage(fullChrome(LangEN), prFixtureFailing)
	staleCheck.Checks.Status = CheckPassed
	staleCheck.Checks.Stale = true

	unreadableCheck := pullRequestPage(fullChrome(LangEN), prFixtureFailing)
	unreadableCheck.Checks.ReadFailure = &EvidenceReadFailure{Code: "io_error", Message: MsgCheckRecordUnreadable}

	otherReview := pullRequestPage(fullChrome(LangEN), prFixtureFailing)
	otherReview.Review = ReviewEvidence{Status: ReviewApproved, ShortSourceOID: "5d0aa13", SubmittedAt: testNow}

	cases := []struct {
		name string
		page PullRequestPage
		want MessageCode
	}{
		{"stale check", staleCheck, MsgRelevancePrior},
		{"unreadable check record", unreadableCheck, MsgRelevanceUnknown},
		{"review of another revision", otherReview, MsgRelevancePrior},
	}
	for _, item := range cases {
		t.Run(item.name, func(t *testing.T) {
			out := render(t, r, item.page)
			want := wantText(LangEN, item.want)
			at := strings.Index(out, want)
			if at < 0 {
				t.Fatalf("the screen does not say %q", want)
			}
			// Before the disclosure, so it is read without opening anything.
			if disclosure := strings.Index(out, "<details"); disclosure >= 0 && at > disclosure {
				t.Error("the relevance label was folded behind the disclosure")
			}
		})
	}
}

// TestTheMergeControlIsNotGatedByEvidence checks the contract the compact
// layout must preserve: only the backend's own refusal disables merging, and
// its reasons are Git and repository state.
func TestTheMergeControlIsNotGatedByEvidence(t *testing.T) {
	r := newRenderer(t)

	// Failing checks and a negative review.
	page := pullRequestPage(fullChrome(LangEN), prFixtureFailing)
	page.Review = ReviewEvidence{Status: ReviewChanges, BoundToCurrentRevision: true, SubmittedAt: testNow}
	out := render(t, r, page)
	if strings.Contains(out, "disabled") {
		t.Error("evidence disabled the merge control")
	}
	if !strings.Contains(out, wantText(LangEN, MsgPRMergeDespite)) {
		t.Error("the screen no longer says the result stays as it is")
	}

	// A Git refusal, which is the only thing that does disable it.
	blocked := render(t, r, pullRequestPage(fullChrome(LangEN), prFixtureBlocked))
	if !strings.Contains(blocked, "disabled") {
		t.Error("a refused merge still offers the control")
	}
	if !strings.Contains(blocked, wantText(LangEN, MsgPRMergeRefused)) {
		t.Error("the refusal is not explained on the default screen")
	}
}

// TestTheMergeClaimMatchesWhatIsActuallyOffered checks that the advisory line
// does not promise a merge the screen is not offering.
//
// "You can merge with checks or a review still failing" is true beside an
// available merge control and false on a closed or refused request. The
// neutral wording states what the results are without claiming eligibility.
func TestTheMergeClaimMatchesWhatIsActuallyOffered(t *testing.T) {
	r := newRenderer(t)

	offered := render(t, r, pullRequestPage(fullChrome(LangEN), prFixtureFailing))
	if !strings.Contains(offered, wantText(LangEN, MsgPRMergeDespite)) {
		t.Error("an offered merge no longer says the failing result stays as it is")
	}

	// A merged request offers no merge control at all.
	merged := render(t, r, pullRequestPage(fullChrome(LangEN), prFixtureMerged))
	if strings.Contains(merged, wantText(LangEN, MsgPRMergeDespite)) {
		t.Error("a merged pull request claims the reader can still merge")
	}
	if !strings.Contains(merged, wantText(LangEN, MsgEvidenceAdvisory)) {
		t.Error("a merged pull request no longer states the advisory contract")
	}

	// A refused merge draws a disabled control, so the claim is false there
	// too.
	blocked := render(t, r, pullRequestPage(fullChrome(LangEN), prFixtureBlocked))
	if strings.Contains(blocked, wantText(LangEN, MsgPRMergeDespite)) {
		t.Error("a refused merge claims the reader can merge anyway")
	}
	if !strings.Contains(blocked, wantText(LangEN, MsgEvidenceAdvisory)) {
		t.Error("a refused merge no longer states the advisory contract")
	}
	// The refusal and its reasons still reach the default screen.
	if !strings.Contains(blocked, wantText(LangEN, MsgPRMergeRefused)) {
		t.Error("the refusal disappeared from the default screen")
	}
}

// TestKoreanBranchDirectionReadsCorrectly checks the direction line's grammar.
//
// Korean places the particle after the target, so an English-order phrase
// between the two branch names produced "feature 에 합칩니다 main". The arrow
// the pull request list already uses is correct in both languages and leaves
// the branch identifiers untranslated.
func TestKoreanBranchDirectionReadsCorrectly(t *testing.T) {
	r := newRenderer(t)
	page := pullRequestPage(fullChrome(LangKO), prFixtureFailing)
	page.Source.Branch = "feature"
	page.Target.Branch = "main"
	out := render(t, r, page)

	source := strings.Index(out, ">feature</span>")
	flow := strings.Index(out, `data-en="into" data-ko="→">→</span>`)
	target := strings.Index(out, ">main</span>")
	if source < 0 || flow < 0 || target < 0 {
		t.Fatalf("direction line is incomplete: source=%d flow=%d target=%d", source, flow, target)
	}
	if !(source < flow && flow < target) {
		t.Fatalf("direction order source=%d flow=%d target=%d", source, flow, target)
	}
	// The phrase that read as nonsense between two branch names.
	if strings.Contains(out, "에 합칩니다") {
		t.Error("the direction line still puts a Korean particle between the branch names")
	}
}

// TestAnEmptyRowDoesNotSayNothingTwice checks the summary rows.
//
// An empty row carried both a "Nothing recorded" relevance label and a state
// that already said "for this revision", so the same absence was stated twice
// on one line. The label belongs to records that exist and need placing.
func TestAnEmptyRowDoesNotSayNothingTwice(t *testing.T) {
	r := newRenderer(t)
	// Nothing has run and nothing was submitted for this pull request.
	page := pullRequestPage(fullChrome(LangEN), prFixtureFailing)
	page.Checks = CheckEvidence{Configured: true, Advisory: true}
	page.Review = ReviewEvidence{}
	out := render(t, r, page)

	if strings.Contains(out, wantText(LangEN, MsgRelevanceNone)) {
		t.Error("an empty row still repeats the absence as a relevance label")
	}
	// Each kind still states its own absence once.
	for _, code := range []MessageCode{MsgCheckStateAbsent, MsgReviewStateNone} {
		if !strings.Contains(out, wantText(LangEN, code)) {
			t.Errorf("the row no longer says %q", wantText(LangEN, code))
		}
	}
}

// TestEveryOtherRelevanceAnswerStaysOnTheRow checks that removing the empty
// label did not remove the ones that carry information.
//
// Whether a result describes this revision, an earlier one, or could not be
// placed at all is the fact the compact layout exists to keep visible.
func TestEveryOtherRelevanceAnswerStaysOnTheRow(t *testing.T) {
	r := newRenderer(t)

	cases := []struct {
		name   string
		mutate func(*PullRequestPage)
		want   MessageCode
	}{
		{"current revision", func(p *PullRequestPage) {}, MsgRelevanceCurrent},
		{"earlier revision", func(p *PullRequestPage) {
			// A review of a commit the branch has moved past.
			p.Review = ReviewEvidence{Status: ReviewApproved, SourceOID: "5d0aa1399",
				ShortSourceOID: "5d0aa13", Provenance: ReviewFromExternalTool,
				SubmittedAt: testNow.AddDate(0, 0, -1)}
		}, MsgRelevancePrior},
		{"could not be determined", func(p *PullRequestPage) {
			// A record OwnGit could not read is evidence about no revision.
			p.Checks = CheckEvidence{Configured: true, Advisory: true,
				ReadFailure: &EvidenceReadFailure{Code: ReadFailureCheckEvidence, Message: MsgCheckRecordUnreadable}}
		}, MsgRelevanceUnknown},
	}
	for _, item := range cases {
		t.Run(item.name, func(t *testing.T) {
			page := pullRequestPage(fullChrome(LangEN), prFixtureFailing)
			item.mutate(&page)
			out := render(t, r, page)
			label := wantText(LangEN, item.want)
			position := strings.Index(out, label)
			if position < 0 {
				t.Fatalf("the row does not say %q", label)
			}
			// It stays on the default screen, not inside the disclosure.
			if disclosure := strings.Index(out, "<details"); disclosure >= 0 && position > disclosure {
				t.Errorf("%q moved behind the disclosure", label)
			}
		})
	}
}

// TestTheSecondaryActionsAreOneCompactDisclosure checks the record-keeping
// controls.
//
// They were a second expanded card that restated the advisory point and the
// merge explanation the summary already makes. Folding them keeps the real
// forms, their tokens and the revision they bind to.
func TestTheSecondaryActionsAreOneCompactDisclosure(t *testing.T) {
	r := newRenderer(t)
	page := pullRequestPage(fullChrome(LangEN), prFixtureFailing)
	out := render(t, r, page)

	title := wantText(LangEN, MsgPRActionsTitle)
	position := strings.Index(out, title)
	if position < 0 {
		t.Fatal("the secondary actions disappeared")
	}
	// It is a disclosure summary, not a card heading.
	if !strings.Contains(out[max(0, position-120):position], "<summary>") {
		t.Error("the secondary actions are not behind a disclosure")
	}

	// The real forms survive, with their token and the revision pair each
	// action is bound to.
	for _, want := range []string{
		`name="csrf"`,
		`name="source_oid" value="` + sourceRevision().OID,
		`name="target_oid" value="` + targetRevision().OID,
		wantText(LangEN, MsgPRRequestReview),
		wantText(LangEN, MsgPRSkipReview),
		// Recording a request is not asking a model to review.
		wantText(LangEN, MsgReviewRequestIntent),
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the disclosure lost %q", want)
		}
	}

	// Merge stays primary and outside the disclosure, with its explanation.
	merge := strings.Index(out, `class="btn btn--primary"`)
	if merge < 0 {
		t.Fatal("the merge control is no longer primary")
	}
	if merge > position {
		t.Error("the merge control moved below the secondary actions")
	}
	if !strings.Contains(out, wantText(LangEN, MsgPRMergeHelp)) {
		t.Error("the merge explanation disappeared with the second card")
	}
}

func TestAutomaticRunsDoNotBorrowTheManualWording(t *testing.T) {
	// A manual helper run happens in the operator's own environment with their
	// permissions, reported by their own repository credential. A server-owned
	// automatic job is a different machine and a different authority. Saying
	// the manual sentence over an automatic job names the wrong one.
	manualProtection := wantText(LangEN, MsgCheckProtectionInherited)
	manualProvenance := wantText(LangEN, MsgCheckProvenanceHelper)

	for _, tc := range []struct {
		name        string
		protection  string
		provenance  string
		wantMessage MessageCode
		wantOrigin  MessageCode
	}{
		{"host job", ProtectionAutomaticHost, ProvenanceAutomaticJob,
			MsgCheckProtectionAutoHost, MsgCheckProvenanceAutomatic},
		{"container job", ProtectionAutomaticContainer, ProvenanceAutomaticJob,
			MsgCheckProtectionAutoContainer, MsgCheckProvenanceAutomatic},
		{"external runner", ProtectionRunnerReported, ProvenanceRunnerClaimed,
			MsgCheckProtectionRunner, MsgCheckProvenanceRunner},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := protectionNote(tc.protection); got != tc.wantMessage {
				t.Errorf("protection note = %q, want %q", got, tc.wantMessage)
			}
			if got := provenanceNote(tc.provenance); got != tc.wantOrigin {
				t.Errorf("provenance note = %q, want %q", got, tc.wantOrigin)
			}
			for _, lang := range Langs() {
				protection := Text(lang, tc.wantMessage)
				origin := Text(lang, tc.wantOrigin)
				if protection == "" || origin == "" {
					t.Fatalf("%s: an automatic run has no wording of its own", lang)
				}
				if protection == Text(lang, MsgCheckProtectionInherited) {
					t.Errorf("%s: the automatic run reuses the manual environment sentence", lang)
				}
				if origin == Text(lang, MsgCheckProvenanceHelper) {
					t.Errorf("%s: the automatic run reuses the manual credential sentence", lang)
				}
			}
		})
	}

	// The manual wording still exists for the run it actually describes.
	if protectionNote(ProtectionInherited) != MsgCheckProtectionInherited {
		t.Error("the manual environment sentence was lost")
	}
	if provenanceNote(ProvenanceAuthenticatedHelper) != MsgCheckProvenanceHelper {
		t.Error("the manual credential sentence was lost")
	}

	// The configured-check job detail shows automatic jobs, so neither manual
	// sentence may appear on it.
	r := newRenderer(t)
	out := render(t, r, allPages(LangEN)["configured-checks-job"])
	if strings.Contains(out, manualProtection) {
		t.Error("the automatic job detail says the run used the operator's own environment")
	}
	if strings.Contains(out, manualProvenance) {
		t.Error("the automatic job detail credits the manual check helper")
	}
	if !strings.Contains(out, wantText(LangEN, MsgCheckProtectionAutoHost)) {
		t.Error("the automatic job detail does not say where it actually ran")
	}
	if !strings.Contains(out, wantText(LangEN, MsgCheckProvenanceAutomatic)) {
		t.Error("the automatic job detail does not say who ran it")
	}
	// The new wording is still not a sandbox claim.
	if !strings.Contains(Text(LangEN, MsgCheckProtectionAutoHost), "not a sandbox") {
		t.Error("the automatic host wording stopped saying it is not a sandbox")
	}
}
