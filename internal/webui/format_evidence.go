package webui

import (
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Formatting for the pull request, task, and helper credential screens.

// pullRequestSelectionURL is the followable GET for a branch pair on the
// create screen.
//
// A refused create answers on a POST route, so a language link built from the
// request URL would be a GET that route refuses. This is the same choice as an
// openable address. It carries branch names only: returning here reads the
// tips again instead of reusing an observation that may be out of date.
func pullRequestSelectionURL(p NewPullRequestPage) string {
	base := p.SelectURL
	if base == "" {
		return p.CancelURL
	}
	values := url.Values{}
	if p.Source.Branch != "" {
		values.Set("source", p.Source.Branch)
	}
	if p.Target.Branch != "" {
		values.Set("target", p.Target.Branch)
	}
	if len(values) == 0 {
		return base
	}
	return base + "?" + values.Encode()
}

// The template entry points. A template names a value rather than a status
// string, keeping the mapping in Go where the tests reach it.
// checkStateOf names a check result for display.
//
// Contradictory records resolve against the reassuring reading: a pass that
// belongs to an earlier revision is shown as stale, and an unreadable record
// says so instead of borrowing the outcome stored beside it.
func checkStateOf(evidence CheckEvidence) stateLabel {
	if evidence.ReadFailure != nil {
		return known(MsgCheckRecordUnreadable, "slash", "warn")
	}
	if evidence.EffectivelyStale() && evidence.Recorded() {
		return checkState(CheckStale)
	}
	if evidence.CleanupFailed && evidence.Status == CheckPassed {
		return uncleanState()
	}
	return checkState(evidence.Status)
}

// reviewStateOf names a review for display.
//
// An outcome for an earlier revision is not shown as that outcome: the chip is
// what a reader takes at a glance, so it reads as no review of the code on
// screen while the detail still names the revision it belongs to. Pending and
// absent are left alone, since neither asserts coverage of this revision.
func reviewStateOf(evidence ReviewEvidence) stateLabel {
	if evidence.ReadFailure != nil {
		return known(MsgReviewRecordUnreadable, "slash", "warn")
	}
	if evidence.Recorded() && !evidence.BoundToCurrentRevision {
		switch evidence.Status {
		case ReviewApproved, ReviewChanges, ReviewPartial, ReviewSkipped:
			return known(MsgReviewStateNone, "minus", "quiet")
		}
	}
	return reviewState(evidence.Status)
}

func taskStateOf(status string) stateLabel { return taskState(status) }

func pullRequestStateOf(state string) stateLabel { return pullRequestState(state) }

// attemptStateOf names an attempt status with the same vocabulary as the
// evidence summary, so one run reads the same way in both places.
func attemptStateOf(status string) stateLabel { return checkState(status) }

// uncleanState is the status of a run whose cleanup did not confirm.
//
// It is a non-success label. The status metadata saying "passed" is
// contradicted by the cleanup failure, so the chip reports the cleanup rather
// than repeating a success the record does not establish. The exit code the
// command actually returned stays in its own field, where it is a fact about
// the command instead of a verdict on the run.
func uncleanState() stateLabel { return known(MsgCheckStateUnclean, "warning", "warn") }

// attemptRecordState names an attempt, reading its cleanup aggregate and its
// result rows together.
func attemptRecordState(attempt AttemptRecord) stateLabel {
	if attempt.CleanupUnclean() && attempt.Status == CheckPassed {
		return uncleanState()
	}
	return checkState(attempt.Status)
}

// resultLineState names one check inside an attempt.
func resultLineState(line CheckResultLine) stateLabel {
	if line.CleanupFailed() && line.Status == CheckPassed {
		return uncleanState()
	}
	return checkState(line.Status)
}

// checkNotes are the qualifying facts shown under a check result, in reading
// order. Each one is a recorded observation; a fact the backend did not record
// produces no line rather than a reassuring default.
//
// The order matters. Whether the result describes this code comes first,
// because it decides how much the status word is worth. Everything else is
// context.
func checkNotes(evidence CheckEvidence) []MessageCode {
	var notes []MessageCode
	if !evidence.Recorded() {
		return notes
	}
	// A registered attempt has produced nothing to qualify yet. Saying "the
	// result does not prove this commit ran" would imply a result exists.
	if evidence.Pending() {
		return append(notes, MsgCheckPendingDetail)
	}
	// A clean tree on this revision is the only case where the result
	// describes the commit named beside it.
	if evidence.claimsTestedCommit() {
		notes = append(notes, MsgCheckTestedCommit)
	} else {
		notes = append(notes, MsgCheckNotTested)
	}
	if note := worktreeNote(evidence.WorktreeState); note != "" {
		notes = append(notes, note)
	}
	if note := protectionNote(evidence.Protection); note != "" {
		notes = append(notes, note)
	}
	if note := provenanceNote(evidence.CredentialProvenance); note != "" {
		notes = append(notes, note)
	}
	if evidence.OutputTruncated {
		notes = append(notes, MsgCheckOutputCut)
	}
	return notes
}

// attemptNotes are the same qualifying facts for one recorded run.
func attemptNotes(attempt AttemptRecord) []MessageCode {
	var notes []MessageCode
	if attempt.Pending() {
		return append(notes, MsgCheckPendingDetail)
	}
	if attempt.TestedCommit() {
		notes = append(notes, MsgCheckTestedCommit)
	} else {
		notes = append(notes, MsgCheckNotTested)
	}
	if note := worktreeNote(attempt.WorktreeState); note != "" {
		notes = append(notes, note)
	}
	if note := protectionNote(attempt.Protection); note != "" {
		notes = append(notes, note)
	}
	if note := provenanceNote(attempt.CredentialProvenance); note != "" {
		notes = append(notes, note)
	}
	if attempt.OutputTruncated {
		notes = append(notes, MsgCheckOutputCut)
	}
	return notes
}

// reviewNotes are the qualifying facts shown under a review result.
//
// A recorded review states what OwnGit did not establish, because a label
// supplied by a tool is not evidence of independence and a review is not a
// check run.
func reviewNotes(evidence ReviewEvidence) []MessageCode {
	var notes []MessageCode
	if !evidence.Recorded() {
		return notes
	}
	if !evidence.BoundToCurrentRevision {
		notes = append(notes, MsgReviewOtherRevision)
	}
	if !evidence.Independent {
		notes = append(notes, MsgReviewNotIndependent)
	}
	if !evidence.ExecutedChecks {
		notes = append(notes, MsgReviewNoChecksRun)
	}
	return notes
}

// logState names whether the disposable raw log can still be read.
func logState(status string) MessageCode { return logNote(status) }

// blockerNote explains one merge refusal.
func blockerNote(blocker MergeBlocker) MessageCode { return mergeBlockerNote(blocker.Code) }

// unknownBlocker reports whether a refusal code is one this package explains.
// An unrecognised code is shown with its raw value so a new backend reason
// reaches the reader instead of disappearing behind a generic sentence.
func unknownBlocker(blocker MergeBlocker) bool {
	return mergeBlockerNote(blocker.Code) == MsgMergeBlockedOther
}

// revisionNote names a branch tip that is not a commit.
func revisionNote(revision RevisionState) MessageCode {
	switch revision.Status {
	case RevisionMissing:
		return MsgPRBranchGone
	case RevisionNotCommit:
		return MsgPRBranchNotCommit
	default:
		return ""
	}
}

// pullRequestNumber renders "#12" for a heading or a row.
func pullRequestNumber(number int64) string {
	return "#" + strconv.FormatInt(number, 10)
}

// shortID abbreviates an opaque identifier for display. The full value stays
// available where it is needed, for example in a form field.
func shortID(id string) string {
	if len(id) > 12 {
		return id[:12]
	}
	return id
}

// formatDuration renders a recorded run length. Milliseconds are useless to
// read for a long build and a second is too coarse for a fast one, so the unit
// follows the size.
func formatDuration(lang Lang, milliseconds int64) string {
	if milliseconds < 0 {
		return ""
	}
	if milliseconds < 1000 {
		if lang == LangKO {
			return fmt.Sprintf("%d밀리초", milliseconds)
		}
		return fmt.Sprintf("%d ms", milliseconds)
	}
	seconds := float64(milliseconds) / 1000
	if seconds < 60 {
		if lang == LangKO {
			return fmt.Sprintf("%.1f초", seconds)
		}
		return fmt.Sprintf("%.1f s", seconds)
	}
	minutes := int(seconds) / 60
	rest := int(seconds) % 60
	if lang == LangKO {
		return fmt.Sprintf("%d분 %d초", minutes, rest)
	}
	return fmt.Sprintf("%d min %d s", minutes, rest)
}

// budgetText states a task's correction budget as a fraction, which is how the
// limit and the amount used stay readable together.
func budgetText(lang Lang, used, limit int) string {
	if limit <= 0 {
		return formatNumber(used)
	}
	if lang == LangKO {
		return fmt.Sprintf("%d회 중 %d회 사용", limit, used)
	}
	return fmt.Sprintf("%d of %d used", used, limit)
}

// exitCodeText renders a recorded exit code. A run with no recorded code
// renders nothing rather than a zero, which would read as success.
func exitCodeText(line CheckResultLine) string {
	if !line.HasExitCode {
		return ""
	}
	return strconv.Itoa(line.ExitCode)
}

// notZero reports whether a timestamp was recorded, for templates that would
// otherwise print an empty date row.
func notZero(t time.Time) bool { return !t.IsZero() }

// tokenLines splits a one-time secret for display.
//
// It is rendered as text in a read-only field rather than as a link or a
// query, so it never enters an address bar, a referrer, or browser history.
func tokenLines(token string) []string {
	if token == "" {
		return nil
	}
	return strings.Split(token, "\n")
}

// prChangeStatus names what a pull request's change did to a file.
//
// The restore screen's status words are future tense ("Will be added"),
// because there the reader is choosing an action that has not happened. A pull
// request already contains its change, so reusing those words told the reader
// the file was about to be modified. This states it in the present tense.
func prChangeStatus(status string) MessageCode {
	switch status {
	case "added", "copied":
		return MsgPRChangeAdded
	case "modified", "renamed":
		return MsgPRChangeModified
	case "deleted":
		return MsgPRChangeDeleted
	default:
		return ""
	}
}

// code converts a template string into a MessageCode.
//
// A literal passed straight to bi converts at parse time, but a value carried
// through dict arrives as a plain string and fails at render time. This is the
// conversion for those call sites.
func code(s string) MessageCode { return MessageCode(s) }

// repoTabsOf builds a repository page's tab strip.
//
// The pull request and checks sections are optional: a caller that does not
// supply their addresses renders the three tabs it always had, and one that
// does renders five. Empty URLs produce no tab rather than a dead link.
func repoTabsOf(p RepositoryPage) RepoTabs {
	return RepoTabs{
		OverviewURL:     p.OverviewURL,
		CodeURL:         p.CodeURL,
		CommitsURL:      p.CommitsURL,
		PullRequestsURL: p.PullRequestsURL,
		TasksURL:        p.TasksURL,
		ImportsURL:      p.ImportsURL,
		Active:          p.Tab,
	}
}

// issueAction and revokeAction expose the action constants to templates, so
// the markup and any handler compare the same strings.
func issueAction() string { return ActionIssueHelperCredential }

func revokeAction() string { return ActionRevokeHelperCredential }

// forCredential returns the notices belonging to one credential row.
//
// Every row submits the same revoke action, so the action alone cannot say
// which row failed. A refused password is announced on the row it was typed
// into, not on all of them.
func forCredential(pendingAction, pendingID, rowID string, notices []Notice) []Notice {
	if pendingAction != ActionRevokeHelperCredential || pendingID == "" || pendingID != rowID {
		return nil
	}
	return notices
}
