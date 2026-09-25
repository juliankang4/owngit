package webui

import "time"

// Evidence presentation.
//
// The backend observes what actually happened and reports it as the plain
// strings below. This file turns one of those strings into a word, a shape,
// and a tone for the screen. Nothing here infers a state: a value this package
// does not recognise renders as "state not recognised", never as a pass, and a
// field the backend left empty renders as not stated rather than as a claim.

// Check attempt statuses. They mirror the durable record and describe what
// happened, not what the reader hoped happened.
const (
	CheckAbsent      = "absent"
	CheckPassed      = "passed"
	CheckFailed      = "failed"
	CheckError       = "error"
	CheckCancelled   = "cancelled"
	CheckIncomplete  = "incomplete"
	CheckUnavailable = "unavailable"
	// CheckStale means evidence exists for a different revision. It is never
	// a reused success for the revision on screen.
	CheckStale = "stale"
	// CheckPending is an attempt the backend registered before running it.
	//
	// A record therefore exists with no outcome and no finish time. That is
	// its own state: not a pass, and not evidence that a process is running at
	// this moment. Nothing in this package observes a live process, so the
	// screen reports the recorded registration and claims nothing more.
	CheckPending = "pending"
)

// LogStatusOf maps the backend's recorded log disposition onto the vocabulary
// this package renders.
//
// The wire says found, expired, or missing. "Found" is presented as available
// because that is what it means to the reader. A value this package does not
// recognise falls through to the unknown state rather than implying the log
// can still be read, so a future metadata state degrades to a non-claim
// instead of a wrong one.
func LogStatusOf(recorded string) string {
	switch recorded {
	case "found":
		return LogAvailable
	case "missing":
		return LogNotRecorded
	// "expired" is already this package's own value, so it passes through here.
	case LogAvailable, LogExpired, LogNotRecorded, LogUnavailable:
		return recorded
	default:
		return LogUnknown
	}
}

// Worktree states recorded with an attempt.
const (
	WorktreeClean   = "clean"
	WorktreeDirty   = "dirty"
	WorktreeUnknown = "unknown"
)

// Log availability. The raw log is disposable and the durable record outlives
// it, so "not recorded", "expired", and "unknown" are separate facts rather
// than one missing flag.
const (
	LogUnknown     = ""
	LogAvailable   = "available"
	LogExpired     = "expired"
	LogNotRecorded = "not_recorded"
	LogUnavailable = "unavailable"
)

// Execution protection actually established for a check run. There is
// deliberately no "sandboxed" value: OwnGit does not provide one in this
// release, so the interface can never claim it.
//
// ProtectionInherited belongs to a manual helper run, which happens in the
// operator's own environment. A server-owned automatic job does not run there,
// so it uses the values below instead. Describing an automatic job as running
// "in your own development environment, with your permissions" would name the
// wrong machine and the wrong authority.
const (
	ProtectionUnstated  = ""
	ProtectionInherited = "inherited"
	ProtectionUnknown   = "unknown"
	// ProtectionAutomaticHost is an automatic job the server ran on its own
	// host account. It is still not a sandbox, and the wording says so.
	ProtectionAutomaticHost = "automatic_host"
	// ProtectionAutomaticContainer is an automatic job the server ran in a
	// container with the policy's limits.
	ProtectionAutomaticContainer = "automatic_container"
	// ProtectionRunnerReported is an automatic job an external runner
	// executed. The protection is what that runner reported, not something
	// OwnGit established or can verify.
	ProtectionRunnerReported = "runner_reported"
)

// How the evidence reached OwnGit.
//
// The helper and a runner both authenticate, but they are not the same origin
// and must not share one sentence: the helper is a person's own tool acting
// with their repository credential, while a runner claims work the server
// admitted under a policy.
const (
	ProvenanceUnstated            = ""
	ProvenanceAuthenticatedHelper = "authenticated_helper"
	// ProvenanceAutomaticJob is an attempt the server itself ran for a job it
	// admitted.
	ProvenanceAutomaticJob = "automatic_job"
	// ProvenanceRunnerClaimed is an attempt an external runner claimed and
	// reported using a repository-scoped runner token.
	ProvenanceRunnerClaimed = "runner_claimed"
)

// Review statuses.
const (
	ReviewNotRequested = "not_requested"
	ReviewPending      = "pending"
	ReviewApproved     = "approved"
	ReviewChanges      = "changes_requested"
	ReviewSkipped      = "skipped"
	// ReviewDecisionRequired is the older record for "no review exists for
	// this revision". It is read as an absence, never as a required action.
	ReviewDecisionRequired = "decision_required"
	// ReviewUnavailable and ReviewPartial describe a review that could not
	// run or that covered only part of the change. Neither is a completed
	// successful review.
	ReviewUnavailable = "unavailable"
	ReviewPartial     = "partial"
)

// How a recorded review reached OwnGit.
const (
	ReviewFromRequest      = "review_request"
	ReviewFromSkip         = "explicit_skip"
	ReviewFromExternalTool = "supplied_external_tool"
	ReviewFromDefault      = "default"
)

// Task statuses. A task keeps one identity while its revisions change.
const (
	TaskActive    = "active"
	TaskResolved  = "resolved"
	TaskExhausted = "exhausted"
)

// Pull request states.
const (
	PullRequestCreating = "creating"
	PullRequestOpen     = "open"
	PullRequestMerged   = "merged"
	PullRequestClosed   = "closed"
)

// Branch tip resolution as the backend observed it.
const (
	RevisionCommit    = "commit"
	RevisionMissing   = "missing"
	RevisionNotCommit = "not_commit"
)

// stateLabel is one rendered state: its sentence, its shape, and its tone.
//
// Tone only repeats what the word and the shape already say, so no state
// depends on colour. Known is false for a value this package does not
// recognise, which the templates render as an explicit non-claim.
type stateLabel struct {
	Code  MessageCode
	Icon  string
	Tone  string
	Known bool
}

func known(code MessageCode, shape, tone string) stateLabel {
	return stateLabel{Code: code, Icon: shape, Tone: tone, Known: true}
}

// unrecognised is what an unexpected status renders as. It is a warning, not a
// pass and not a silent blank.
var unrecognised = stateLabel{Code: MsgEvidenceUnknownState, Icon: "warning", Tone: "warn"}

// checkState names one check attempt status.
func checkState(status string) stateLabel {
	switch status {
	case CheckPassed:
		return known(MsgCheckStatePassed, "check", "ok")
	case CheckFailed:
		return known(MsgCheckStateFailed, "error", "bad")
	case CheckError:
		return known(MsgCheckStateError, "warning", "bad")
	case CheckCancelled:
		return known(MsgCheckStateCancelled, "stop", "warn")
	case CheckIncomplete:
		return known(MsgCheckStateIncomplete, "info", "warn")
	case CheckUnavailable:
		return known(MsgCheckStateUnavailable, "slash", "warn")
	case CheckStale:
		return known(MsgCheckStateStale, "clock", "warn")
	// Registered but not finished. It is quiet on purpose: there is nothing to
	// report yet, and colouring it would suggest progress this package cannot
	// observe.
	case CheckPending:
		return known(MsgCheckStatePending, "clock", "quiet")
	case CheckAbsent, "":
		return known(MsgCheckStateAbsent, "minus", "quiet")
	default:
		return unrecognised
	}
}

// reviewState names one review status.
func reviewState(status string) stateLabel {
	switch status {
	case ReviewApproved:
		return known(MsgReviewStateApproved, "check", "ok")
	case ReviewChanges:
		return known(MsgReviewStateChanges, "error", "warn")
	case ReviewPending:
		return known(MsgReviewStatePending, "clock", "warn")
	case ReviewSkipped:
		return known(MsgReviewStateSkipped, "minus", "quiet")
	case ReviewUnavailable:
		return known(MsgReviewStateUnavailable, "slash", "warn")
	case ReviewPartial:
		return known(MsgReviewStatePartial, "info", "warn")
	// An older record and an empty record both mean the same thing: this
	// revision has no review. Neither is an instruction to the reader.
	case ReviewNotRequested, ReviewDecisionRequired, "":
		return known(MsgReviewStateNone, "minus", "quiet")
	default:
		return unrecognised
	}
}

// reviewOrigin names where a recorded review came from.
func reviewOrigin(provenance string) MessageCode {
	switch provenance {
	case ReviewFromRequest:
		return MsgReviewFromRequest
	case ReviewFromSkip:
		return MsgReviewFromSkip
	case ReviewFromExternalTool:
		return MsgReviewFromTool
	case ReviewFromDefault:
		return MsgReviewFromDefault
	default:
		return ""
	}
}

// taskState names one task status.
func taskState(status string) stateLabel {
	switch status {
	case TaskActive:
		return known(MsgTaskStateActive, "clock", "quiet")
	case TaskResolved:
		return known(MsgTaskStateResolved, "check", "ok")
	case TaskExhausted:
		return known(MsgTaskStateExhausted, "stop", "warn")
	default:
		return unrecognised
	}
}

// pullRequestState names one pull request state.
func pullRequestState(state string) stateLabel {
	switch state {
	case PullRequestOpen:
		return known(MsgPRStateOpen, "branch", "quiet")
	case PullRequestMerged:
		return known(MsgPRStateMerged, "check", "ok")
	case PullRequestClosed:
		return known(MsgPRStateClosed, "minus", "quiet")
	case PullRequestCreating:
		return known(MsgPRStateCreating, "clock", "warn")
	default:
		return unrecognised
	}
}

// worktreeNote states what the recorded working tree means for the evidence.
// A dirty or unknown tree means the commit named beside it is not proven to be
// the bytes that ran.
func worktreeNote(state string) MessageCode {
	switch state {
	case WorktreeClean:
		return MsgCheckWorktreeClean
	case WorktreeDirty:
		return MsgCheckWorktreeDirty
	case WorktreeUnknown:
		return MsgCheckWorktreeUnknown
	default:
		return ""
	}
}

// logNote states whether the disposable raw log can still be read.
func logNote(status string) MessageCode {
	switch status {
	case LogAvailable:
		return MsgCheckLogAvailable
	case LogExpired:
		return MsgCheckLogExpired
	case LogNotRecorded:
		return MsgCheckLogNotRecorded
	case LogUnavailable:
		return MsgCheckLogUnavailable
	default:
		return MsgCheckLogUnknown
	}
}

// protectionNote states the protection actually established for a run.
//
// The manual wording is reserved for a manual run. An automatic job is the
// server's own work under a saved policy, so it is described as that, and an
// unrecognised value still claims nothing.
func protectionNote(protection string) MessageCode {
	switch protection {
	case ProtectionInherited:
		return MsgCheckProtectionInherited
	case ProtectionAutomaticHost:
		return MsgCheckProtectionAutoHost
	case ProtectionAutomaticContainer:
		return MsgCheckProtectionAutoContainer
	case ProtectionRunnerReported:
		return MsgCheckProtectionRunner
	case ProtectionUnknown:
		return MsgCheckProtectionUnknown
	default:
		return ""
	}
}

// provenanceNote states how the evidence reached OwnGit.
func provenanceNote(provenance string) MessageCode {
	switch provenance {
	case ProvenanceAuthenticatedHelper:
		return MsgCheckProvenanceHelper
	case ProvenanceAutomaticJob:
		return MsgCheckProvenanceAutomatic
	case ProvenanceRunnerClaimed:
		return MsgCheckProvenanceRunner
	default:
		return ""
	}
}

// mergeBlockerNote explains one refusal the backend reported. An unknown code
// renders as a generic refusal and the raw code is shown as data beside it, so
// a new backend reason is never swallowed.
func mergeBlockerNote(code string) MessageCode {
	switch code {
	case "already_merged":
		return MsgMergeBlockedMerged
	case "source_branch_missing":
		return MsgMergeBlockedSourceGone
	case "target_branch_missing":
		return MsgMergeBlockedTargetGone
	case "source_not_commit":
		return MsgMergeBlockedSourceKind
	case "target_not_commit":
		return MsgMergeBlockedTargetKind
	case "merge_conflict":
		return MsgMergeBlockedConflict
	case "stale_revision":
		return MsgMergeBlockedStale
	case "pull_request_not_open":
		return MsgMergeBlockedNotOpen
	case "git_update_failed":
		return MsgMergeBlockedGitFailed
	default:
		return MsgMergeBlockedOther
	}
}

// Presentation values

// RevisionState is one branch tip as the backend observed it. The OID is what
// a form submits back so the server can refuse a branch that moved.
type RevisionState struct {
	Branch   string
	OID      string
	ShortOID string
	// Status is RevisionCommit, RevisionMissing, or RevisionNotCommit.
	Status string
}

// Resolved reports whether this tip is a commit the interface can act on.
func (r RevisionState) Resolved() bool { return r.Status == RevisionCommit && r.OID != "" }

const (
	ReadFailureCheckConfiguration = "check_configuration"
	ReadFailureCheckEvidence      = "check_evidence"
	ReadFailureReviewEvidence     = "review_evidence"
)

// EvidenceReadFailure identifies a failed OwnGit evidence query. It is not an
// execution outcome and never becomes a merge gate.
type EvidenceReadFailure struct {
	Code    string
	Message MessageCode
}

// CheckEvidence is the check result bound to one revision.
//
// Status and RevisionOID are read together: evidence for an older revision is
// stale, not a result for the revision on screen. A dirty or unknown tree is
// never shown as a tested commit, whatever TestedCommit says.
type CheckEvidence struct {
	Status string
	// Configured is false when the repository has no recorded check
	// configuration at all.
	Configured bool
	// Passed is the raw exit result of the bound attempt.
	Passed bool
	// TestedCommit is true only for a clean working tree on this revision.
	TestedCommit bool
	// Advisory states the contract: checks never hold a merge.
	Advisory bool
	// Stale is true when the newest evidence belongs to another revision.
	Stale bool

	WorktreeState        string
	RevisionOID          string
	RevisionShortOID     string
	ConfigurationVersion int64
	AttemptID            string
	AttemptShortID       string
	FinishedAt           time.Time
	Summary              string
	// AttemptURL opens the task this attempt belongs to. Empty means no link.
	AttemptURL string

	// LogStatus is one of the Log* values. The empty value is unknown, which
	// is stated as unknown rather than as an absent log.
	LogStatus    string
	LogExpiresAt time.Time
	// OutputTruncated reports that the stored excerpt is not the whole output.
	OutputTruncated bool
	// Protection is what was actually established, never a sandbox claim.
	Protection string
	// CredentialProvenance is how the evidence reached OwnGit.
	CredentialProvenance string
	// LogError is the backend's untranslated reason a log could not be read.
	LogError string
	// RegisteredAt is when the attempt was recorded, which is the only time a
	// pending attempt has.
	RegisteredAt time.Time
	// ReadFailure reports that OwnGit could not read its own configuration or
	// check record. This is different from a run reporting an unavailable
	// environment, and different again from no configured checks.
	ReadFailure *EvidenceReadFailure
	// CleanupFailed is the backend's aggregate: a process started by this
	// check outlived it. The renderer receives the decision and never works
	// out process ownership or tries to stop anything itself.
	CleanupFailed bool
}

// Pending reports a registered attempt with no outcome yet.
func (e CheckEvidence) Pending() bool { return e.Status == CheckPending }

// Recorded reports whether any evidence exists to describe.
func (c CheckEvidence) Recorded() bool {
	return c.Status != "" && c.Status != CheckAbsent
}

// EffectivelyStale reports evidence that belongs to an earlier revision.
//
// The backend can say so with either the flag or the status, so both are read
// together. Checking only the flag let a stale status through beside a clean
// tree and produced the one sentence this screen must never print about other
// code: that the result describes the commit on screen. The status chip, the
// warning and the tested-commit line all use this.
func (c CheckEvidence) EffectivelyStale() bool {
	return c.Stale || c.Status == CheckStale
}

// claimsTestedCommit reports whether the result may be said to describe the
// commit named beside it.
//
// Every condition has to hold: a clean working tree, evidence for this
// revision, a finished run, and a readable record. TestedCommit alone is an
// assertion a dirty or unknown tree contradicts.
func (c CheckEvidence) claimsTestedCommit() bool {
	return c.TestedCommit &&
		c.WorktreeState == WorktreeClean &&
		!c.EffectivelyStale() && !c.Pending() && c.ReadFailure == nil &&
		!c.CleanupFailed
}

// ReviewEvidence is the review record bound to one revision.
type ReviewEvidence struct {
	Status        string
	Provenance    string
	ReviewerLabel string
	// Independent is true only when OwnGit itself established independence.
	// It does not become true because a supplied result says so.
	Independent bool
	// ExecutedChecks is true only when the reviewer actually ran the checks.
	ExecutedChecks bool

	SourceOID      string
	TargetOID      string
	ShortSourceOID string
	SubmittedAt    time.Time
	// BoundToCurrentRevision is false when the record belongs to an earlier
	// revision, which the screen states instead of presenting it as current.
	BoundToCurrentRevision bool
	// ReadFailure reports that OwnGit could not read its own review record. It
	// is separate from a reviewer that reported an unavailable execution.
	ReadFailure *EvidenceReadFailure
	// Detail is quoted evidence supplied by an external reviewer and is shown
	// verbatim, including on a page whose interface language differs.
	Detail string
	// Coverage describes partial coverage, for example the paths a review
	// actually reached. Rendered as text.
	Coverage string
}

// Recorded reports whether a review record exists for this revision.
func (r ReviewEvidence) Recorded() bool {
	switch r.Status {
	case "", ReviewNotRequested, ReviewDecisionRequired:
		return false
	default:
		return true
	}
}

// HasReviewer reports whether the record carries a reviewer's own result.
// A review request or an explicit skip has no reviewer yet, so statements
// about the reviewer do not apply to it.
func (r ReviewEvidence) HasReviewer() bool {
	return r.Recorded() && r.Status != ReviewPending && r.Status != ReviewSkipped
}

// RevisionLabel names the revision row of a review record: the revision a
// request or a skip belongs to, or the revision a given review tested.
func (r ReviewEvidence) RevisionLabel() MessageCode {
	switch r.Status {
	case ReviewPending:
		return MsgReviewRevisionRequested
	case ReviewSkipped:
		return MsgReviewRevisionSkipped
	default:
		return MsgCheckRevision
	}
}

// MergeBlocker is one reason the backend refuses to merge. These are Git and
// state reasons only; check and review results never appear here.
type MergeBlocker struct {
	Code   string
	Detail string
}

// MergeAvailability is the backend's merge decision.
//
// The two fields must agree. A record that says eligible while listing a
// reason, or that refuses without giving one, is contradictory, and the screen
// resolves it against offering the action.
type MergeAvailability struct {
	Eligible bool
	Blockers []MergeBlocker
}

// Offered reports whether the merge control may be used.
//
// It requires an eligible decision and no reason recorded against it. A
// blocker with nothing in it still counts: something refused this merge, and
// the record simply failed to say what. Blockers are Git and repository facts
// only, so this can never become a quality gate for checks or reviews.
func (m MergeAvailability) Offered() bool { return m.Eligible && len(m.Blockers) == 0 }

// ExplainedBlockers are the blockers that actually say something.
//
// A record with neither a code nor a detail explains nothing, so rendering it
// would produce an empty bullet under a heading promising a reason.
func (m MergeAvailability) ExplainedBlockers() []MergeBlocker {
	explained := make([]MergeBlocker, 0, len(m.Blockers))
	for _, blocker := range m.Blockers {
		if blocker.Code != "" || blocker.Detail != "" {
			explained = append(explained, blocker)
		}
	}
	return explained
}

// Unexplained reports a refusal the reader cannot be given a reason for.
//
// That covers a refusal with no blockers at all and one whose blockers are
// all empty. Both say the explanation is missing rather than promising one.
// This never re-enables merging: Offered counts every blocker, empty or not.
func (m MergeAvailability) Unexplained() bool {
	return !m.Offered() && len(m.ExplainedBlockers()) == 0
}

// MergeRecord describes a completed merge.
type MergeRecord struct {
	Mode       string
	OID        string
	ShortOID   string
	ReceiptRef string
	MergedAt   time.Time
}

// Merge modes the backend records.
const (
	MergeModeFastForward = "fast_forward"
	MergeModeCommit      = "merge_commit"
	MergeModeUpToDate    = "up_to_date"
)

// ModeNote names how the merge was made. An unknown mode returns nothing, and
// the screen shows the recorded value instead.
func (m MergeRecord) ModeNote() MessageCode {
	switch m.Mode {
	case MergeModeFastForward:
		return MsgPRMergedFastForward
	case MergeModeCommit:
		return MsgPRMergedMergeCommit
	case MergeModeUpToDate:
		return MsgPRMergedUpToDate
	default:
		return ""
	}
}

// CommitLabel names the recorded commit. Only a merge commit is a new commit;
// otherwise the commit is where the target branch now points.
func (m MergeRecord) CommitLabel() MessageCode {
	if m.Mode == MergeModeCommit {
		return MsgPRMergedCommit
	}
	return MsgPRMergedTarget
}

// CheckDefinitionLine is one configured check.
type CheckDefinitionLine struct {
	Name    string
	Command string
}

// CheckConfigurationView is the versioned configuration an attempt ran under.
type CheckConfigurationView struct {
	// Configured is false when no configuration has been recorded.
	Configured bool
	Version    int64
	Checks     []CheckDefinitionLine
	RecordedAt time.Time
}

// CheckResultLine is one check inside an attempt.
type CheckResultLine struct {
	Name          string
	Command       string
	Status        string
	ExitCode      int
	HasExitCode   bool
	DurationMS    int64
	OutputExcerpt string
	Truncated     bool
	// CleanupError is the backend's untranslated reason a process this check
	// started could not be cleaned up. Empty means no cleanup failure was
	// reported. It is shown as recorded text, separate from the output, and
	// it is never treated as markup.
	CleanupError string
}

// CleanupFailed reports a cleanup failure on this line.
func (l CheckResultLine) CleanupFailed() bool { return l.CleanupError != "" }

// AttemptRecord is one recorded check run.
type AttemptRecord struct {
	ID                   string
	ShortID              string
	Status               string
	RevisionOID          string
	RevisionShortOID     string
	WorktreeState        string
	ConfigurationVersion int64
	StartedAt            time.Time
	FinishedAt           time.Time
	DurationMS           int64
	Summary              string
	Results              []CheckResultLine

	LogStatus            string
	LogExpiresAt         time.Time
	OutputTruncated      bool
	Protection           string
	CredentialProvenance string
	// LogError is the backend's untranslated reason the log could not be read.
	// Shown as recorded text beside the log disposition.
	LogError string

	// Sequence is the server-assigned registration order across the whole
	// repository, so numbers within one task can have gaps. It is an order,
	// not a count of how many times this task ran. Zero means the backend
	// stated none, which renders nothing. The correction cycle number is the
	// task-local reservation budget and is separate.
	Sequence int64
	// CycleID identifies the reserved correction round this attempt belongs
	// to, when the backend states one.
	CycleID string
	// TimeoutMS and OutputLimitBytes are the limits this run was given. They
	// are what makes a cancelled or incomplete result explainable rather than
	// mysterious. Zero means not stated.
	TimeoutMS        int64
	OutputLimitBytes int64

	// CleanupFailed is the backend's aggregate for this attempt, used when
	// result rows are not supplied. CleanupUnclean reads it together with the
	// rows, so either source is enough.
	CleanupFailed bool
}

// CleanupDetailedByRow reports that a result row already gives the reason, so
// the attempt does not repeat the warning above it.
func (a AttemptRecord) CleanupDetailedByRow() bool {
	for _, line := range a.Results {
		if line.CleanupFailed() {
			return true
		}
	}
	return false
}

// CleanupUnclean reports a cleanup failure anywhere in this attempt.
//
// The aggregate covers an attempt rendered without its rows, and the rows
// cover one that carries them without the aggregate set.
func (a AttemptRecord) CleanupUnclean() bool {
	return a.CleanupFailed || a.CleanupDetailedByRow()
}

// Pending reports an attempt registered before its result exists.
//
// It is not a pass and not proof that a process is running: this package sees
// a record, never a live process.
func (a AttemptRecord) Pending() bool { return a.Status == CheckPending }

// TestedCommit reports whether this attempt proves the named revision is the
// code that ran. A dirty or unrecorded tree does not, and a pending attempt
// has produced no result to describe anything with.
func (a AttemptRecord) TestedCommit() bool {
	return a.WorktreeState == WorktreeClean && !a.Pending() && !a.CleanupUnclean()
}

// TaskSummary is one durable task. Its correction budget belongs to the task,
// not to each revision.
type TaskSummary struct {
	ID         string
	ShortID    string
	Title      string
	Status     string
	URL        string
	CyclesUsed int
	CyclesLeft int
	CycleLimit int
	CreatedAt  time.Time
	UpdatedAt  time.Time
	// Latest is the most recent attempt, used for the list row. Zero when the
	// task has no attempt yet.
	Latest AttemptRecord
	// InitialCheckDone reports whether the task's first check was recorded.
	// Until it is, a zero round count means the work has not been measured
	// yet rather than that it passed without needing correction.
	InitialCheckDone bool
}

// HasAttempt reports whether the summary carries a latest attempt.
func (t TaskSummary) HasAttempt() bool { return t.Latest.Status != "" }

// HelperCredentialRow is one revocable helper credential. It never carries a
// token: the token exists on screen only in the response that created it.
type HelperCredentialRow struct {
	ID         string
	ShortID    string
	Label      string
	CreatedAt  time.Time
	LastUsedAt time.Time
	RevokedAt  time.Time
	Revoked    bool
}

// Revocable reports whether this row offers revocation.
//
// An active credential with an identifier is revocable. Nothing else is
// required: making it depend on an extra field meant a backend that filled in
// the documented fields still rendered a credential nobody could revoke.
func (h HelperCredentialRow) Revocable() bool { return h.ID != "" && !h.Revoked }
