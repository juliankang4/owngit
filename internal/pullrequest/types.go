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

type Review struct {
	Status         string     `json:"status"`
	SourceOID      string     `json:"source_oid,omitempty"`
	TargetOID      string     `json:"target_oid,omitempty"`
	ReviewerLabel  string     `json:"reviewer_label,omitempty"`
	Provenance     string     `json:"provenance,omitempty"`
	Independent    bool       `json:"independent"`
	ExecutedChecks bool       `json:"executed_checks"`
	SubmittedAt    *time.Time `json:"submitted_at,omitempty"`
}

type Checks struct {
	Status     string `json:"status"`
	Configured bool   `json:"configured"`
	Passed     bool   `json:"passed"`
	Blocking   bool   `json:"blocking"`
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
