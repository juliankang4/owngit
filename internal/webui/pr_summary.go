package webui

// The pull request screen's evidence summary.
//
// One rule shapes this file. The default screen answers three questions at a
// glance: what changed, what is known about it, and can it be merged. Each
// kind of evidence keeps its own row, because they answer different
// questions and merging them into one verdict would hide the one that has
// nothing to say.
//
// What must never move behind a disclosure is which revision a result
// describes and whether a result exists at all. A result about other code is
// not a weaker result, it is a different subject, and an unreadable record is
// not an absence. Both stay on the row itself as a short label. The failure
// detail and the measured record are the parts a reader asks for after the
// summary, so those are disclosed on demand.

// Evidence relevance as one short label.
//
// This is deliberately not a sentence. Three sentences repeated down three
// rows read as prose and stopped being scanned, which is the failure that
// made the previous screen unreadable. The words still distinguish the four
// answers that matter: this revision, an earlier one, nothing recorded, and
// not determinable.
func evidenceRelevance(current, recorded, determinable bool) stateLabel {
	switch {
	case !determinable:
		// OwnGit could not establish which revision the record describes,
		// which is not the same as it describing this one.
		return known(MsgRelevanceUnknown, "warning", "warn")
	case !recorded:
		return known(MsgRelevanceNone, "minus", "quiet")
	case current:
		return known(MsgRelevanceCurrent, "check", "quiet")
	default:
		return known(MsgRelevancePrior, "clock", "warn")
	}
}

// checkRelevance names which revision a check result describes.
//
// An unreadable record is not evidence about any revision, so it is reported
// as undeterminable rather than as a result for another commit.
func checkRelevance(evidence CheckEvidence) stateLabel {
	if evidence.ReadFailure != nil {
		return evidenceRelevance(false, false, false)
	}
	if !evidence.Recorded() {
		return evidenceRelevance(false, false, true)
	}
	return evidenceRelevance(!evidence.EffectivelyStale(), true, true)
}

// reviewRelevance names which revision a supplied review opinion describes.
func reviewRelevance(evidence ReviewEvidence) stateLabel {
	if evidence.ReadFailure != nil {
		return evidenceRelevance(false, false, false)
	}
	if !evidence.Recorded() {
		return evidenceRelevance(false, false, true)
	}
	return evidenceRelevance(evidence.BoundToCurrentRevision, true, true)
}

// The one-line summary under each row's key.
//
// It qualifies the state word with what the record actually establishes, and
// returns nothing when the state word is already the whole answer. It never
// repeats the relevance label beside it.

// prCheckSummary qualifies a check result.
func prCheckSummary(evidence CheckEvidence) MessageCode {
	switch {
	case evidence.ReadFailure != nil:
		// The backend's own reason for the failed read, which is more
		// specific than the state word.
		return evidence.ReadFailure.Message
	case !evidence.Recorded() && !evidence.Configured:
		return MsgCheckNotConfigured
	case !evidence.Recorded():
		return ""
	case evidence.Pending():
		return MsgCheckPendingDetail
	case evidence.EffectivelyStale():
		return MsgCheckStaleDetail
	case evidence.CleanupFailed:
		// A leftover process is a fact about the machine and outranks the
		// tested-commit line: the checks may well have passed, but the run
		// did not clean up after itself.
		return MsgCheckCleanupFailed
	case evidence.claimsTestedCommit():
		return MsgCheckTestedCommit
	default:
		return MsgCheckNotTested
	}
}

// prReviewSummary qualifies a supplied review opinion.
//
// What OwnGit did not establish comes first. A label a tool supplied is not
// evidence that a person wrote the opinion or that the reviewer was
// independent, and that qualification matters more on a summary row than the
// origin of the record does.
func prReviewSummary(evidence ReviewEvidence) MessageCode {
	switch {
	case evidence.ReadFailure != nil:
		return evidence.ReadFailure.Message
	case !evidence.Recorded():
		return ""
	case !evidence.BoundToCurrentRevision:
		return MsgReviewOtherRevision
	case evidence.HasReviewer() && !evidence.Independent:
		return MsgReviewNotIndependent
	case evidence.HasReviewer() && !evidence.ExecutedChecks:
		return MsgReviewNoChecksRun
	default:
		return reviewOrigin(evidence.Provenance)
	}
}
