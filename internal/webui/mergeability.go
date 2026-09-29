package webui

// Mergeability answer statuses, as the backend reports them.
const (
	MergeabilityClean       = "clean"
	MergeabilityConflict    = "conflict"
	MergeabilityUnavailable = "unavailable"
	MergeabilityStale       = "stale"
)

// MergeabilityAnswer is the answer to Check mergeability, shown once on the
// page where it was asked and kept nowhere. It is about exactly the commits
// ShortSourceOID and ShortTargetOID name.
type MergeabilityAnswer struct {
	// Status is one of the Mergeability constants.
	Status string
	// Method is the merge a clean answer would make: a MergeMode constant.
	Method string
	// ConflictPaths are the conflicting paths; ConflictPathsTruncated says
	// more exist than are listed.
	ConflictPaths          []string
	ConflictPathsTruncated bool
	// Reason is the backend's code for a conflict without paths or for an
	// unavailable answer.
	Reason         string
	ShortSourceOID string
	ShortTargetOID string
}

// Note is the sentence that gives the answer.
func (a MergeabilityAnswer) Note() MessageCode {
	switch a.Status {
	case MergeabilityClean:
		switch a.Method {
		case MergeModeFastForward:
			return MsgMergeabilityFastForward
		case MergeModeUpToDate:
			return MsgMergeabilityUpToDate
		default:
			return MsgMergeabilityMergeCommit
		}
	case MergeabilityConflict:
		switch {
		case a.Reason == "no_merge_base":
			return MsgMergeabilityNoBase
		case len(a.ConflictPaths) == 0:
			// Git can report a conflict without naming a file, such as for
			// some directory renames.
			return MsgMergeabilityConflictUnlisted
		}
		return MsgMergeabilityConflict
	case MergeabilityStale:
		return MsgMergeabilityStale
	}
	switch a.Reason {
	case "unsupported_git":
		return MsgMergeabilityOldGit
	case "source_branch_missing", "target_branch_missing", "source_not_commit", "target_not_commit":
		return mergeBlockerNote(a.Reason)
	default:
		return MsgMergeabilityFailed
	}
}

// Clean reports an answer that merging now succeeds.
func (a MergeabilityAnswer) Clean() bool { return a.Status == MergeabilityClean }

// Conflict reports an answer that merging now is refused as a conflict.
func (a MergeabilityAnswer) Conflict() bool { return a.Status == MergeabilityConflict }
