package pullrequest

import (
	"encoding/json"
	"errors"
	"time"
)

const MaximumListResults = 1000

type Problem struct {
	Code    string
	Message string
	Details any
	Cause   error
}

func (problem *Problem) Error() string {
	if problem == nil {
		return ""
	}
	return problem.Message
}

func (problem *Problem) Unwrap() error {
	if problem == nil {
		return nil
	}
	return problem.Cause
}

func NewProblem(code, message string) *Problem {
	return &Problem{Code: code, Message: message}
}

func AsProblem(err error) *Problem {
	var problem *Problem
	if errors.As(err, &problem) {
		return problem
	}
	return &Problem{Code: "internal_error", Message: "The pull request operation could not be completed.", Cause: err}
}

type Revision struct {
	Branch string `json:"branch"`
	OID    string `json:"oid,omitempty"`
	Status string `json:"status"`
}

const (
	ReadFailureCheckConfiguration = "check_configuration"
	ReadFailureCheckEvidence      = "check_evidence"
	ReadFailureReviewEvidence     = "review_evidence"
)

// ReadFailure identifies an OwnGit evidence query that failed. It is separate
// from an unavailable check or review execution, which is evidence supplied by
// the tool that attempted the work.
type ReadFailure struct {
	Code string `json:"code"`
}

type Review struct {
	Status         string     `json:"status"`
	SourceOID      string     `json:"source_oid,omitempty"`
	TargetOID      string     `json:"target_oid,omitempty"`
	ReviewerLabel  string     `json:"reviewer_label,omitempty"`
	Provenance     string     `json:"provenance,omitempty"`
	Independent    bool       `json:"independent"`
	ExecutedChecks bool       `json:"executed_checks"`
	SubmittedAt    *time.Time `json:"submitted_at,omitempty"`
	// ReadFailure reports that OwnGit could not read its own review evidence.
	// Review remains advisory, so this never becomes a merge blocker.
	ReadFailure *ReadFailure `json:"read_failure,omitempty"`
	// Detail is quoted evidence supplied by an external reviewer. It is kept
	// verbatim rather than treated as system-authored interface text.
	Detail string `json:"detail,omitempty"`
}

type Checks struct {
	// Status is absent when no evidence exists, stale when only an older
	// revision has evidence, or the real attempt status otherwise. It is never
	// a reused success for a different revision.
	Status     string `json:"status"`
	Configured bool   `json:"configured"`
	// Passed carries the raw exit result of the bound attempt. It can be true
	// while TestedCommit is false, so render both.
	Passed bool `json:"passed"`
	// Blocking is retained for compatibility and is always false: checks are
	// advisory and never hold a merge.
	Blocking bool `json:"blocking"`
	// Advisory is always true and states the contract explicitly.
	Advisory bool `json:"advisory"`
	// RevisionOID is the revision the evidence actually tested. It can differ
	// from the current source when Status is stale.
	RevisionOID string `json:"revision_oid,omitempty"`
	// WorktreeState is clean, dirty, or unknown. A dirty or unknown worktree
	// means the tested commit is not proven to be the bytes that ran.
	WorktreeState string `json:"worktree_state,omitempty"`
	// TestedCommit is true only for a clean worktree on the current revision.
	TestedCommit         bool       `json:"tested_commit"`
	Stale                bool       `json:"stale"`
	ConfigurationVersion int64      `json:"configuration_version,omitempty"`
	AttemptID            string     `json:"attempt_id,omitempty"`
	FinishedAt           *time.Time `json:"finished_at,omitempty"`
	Summary              string     `json:"summary,omitempty"`
	// LogStatus is found, expired, or missing. The durable attempt record
	// outlives its disposable log.
	LogStatus    string     `json:"log_status,omitempty"`
	LogExpiresAt *time.Time `json:"log_expires_at,omitempty"`
	LogTruncated bool       `json:"log_truncated"`
	// Protection and ExecutionScope state what was actually established.
	Protection     string `json:"protection,omitempty"`
	ExecutionScope string `json:"execution_scope,omitempty"`
	// CredentialID is the server-authenticated helper credential that
	// submitted the evidence.
	CredentialID string `json:"credential_id,omitempty"`
	// JobID is the automatic job OwnGit admitted for this attempt, empty for a
	// manual or historical helper submission. It is the recorded authority for
	// describing a run as the server's own work rather than the operator's.
	// It is projected for the browser only and stays out of the pull request
	// API response, which this release does not extend.
	JobID string `json:"-"`
	// TaskID links the attempt back to its durable task without requiring a
	// second lookup by the browser layer.
	TaskID string `json:"task_id,omitempty"`
	// RegisteredAt is the server observation available for a pending attempt.
	// FinishedAt remains absent until a completion is accepted.
	RegisteredAt *time.Time `json:"registered_at,omitempty"`
	// LogError records why the disposable raw log could not be stored or read.
	LogError string `json:"log_error,omitempty"`
	// CleanupFailed prevents a raw successful exit from being presented as a
	// clean completed run when owned-process cleanup was not confirmed.
	CleanupFailed bool `json:"cleanup_failed"`
	// ReadFailure reports which OwnGit evidence query failed. Checks remain
	// advisory, so this never becomes a merge blocker.
	ReadFailure *ReadFailure `json:"read_failure,omitempty"`
}

type Blocker struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type Eligibility struct {
	Eligible bool      `json:"eligible"`
	Blockers []Blocker `json:"blockers"`
}

type MergeResult struct {
	Mode       string    `json:"mode"`
	OID        string    `json:"oid"`
	ReceiptRef string    `json:"receipt_ref"`
	MergedAt   time.Time `json:"merged_at"`
}

type View struct {
	Repository       string       `json:"repository"`
	Number           int64        `json:"number"`
	Title            string       `json:"title"`
	State            string       `json:"state"`
	Source           Revision     `json:"source"`
	Target           Revision     `json:"target"`
	Review           Review       `json:"review"`
	Checks           Checks       `json:"checks"`
	MergeEligibility Eligibility  `json:"merge_eligibility"`
	Merge            *MergeResult `json:"merge,omitempty"`
	CreatedAt        time.Time    `json:"created_at"`
	UpdatedAt        time.Time    `json:"updated_at"`
}

type CreateInput struct {
	Repository   string `json:"repository,omitempty"`
	Title        string `json:"title"`
	SourceBranch string `json:"source_branch"`
	TargetBranch string `json:"target_branch"`
	ReviewChoice string `json:"review"`
	// SourceOID and TargetOID are optional expected heads. They are validated
	// inside the repository lock, so a form cannot create a pull request for a
	// revision the user never saw.
	SourceOID string `json:"source_oid,omitempty"`
	TargetOID string `json:"target_oid,omitempty"`
}

type RevisionInput struct {
	SourceOID string `json:"source_oid"`
	TargetOID string `json:"target_oid"`
}

type ReviewSubmitInput struct {
	SourceOID     string `json:"source_oid"`
	TargetOID     string `json:"target_oid"`
	Decision      string `json:"decision"`
	ReviewerLabel string `json:"reviewer_label"`
}

type SuccessEnvelope struct {
	OK          bool    `json:"ok"`
	PullRequest *View   `json:"pull_request,omitempty"`
	Items       []*View `json:"pull_requests,omitempty"`
}

type ErrorEnvelope struct {
	OK    bool             `json:"ok"`
	Error ErrorDescription `json:"error"`
}

type ErrorDescription struct {
	Code    string          `json:"code"`
	Message string          `json:"message"`
	Details json.RawMessage `json:"details,omitempty"`
}
