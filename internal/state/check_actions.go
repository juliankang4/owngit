package state

import (
	"fmt"
	"strings"

	"owngit/internal/actions"
)

// AggregateActionsAttemptStatus uses portable roles, not a retained job plan.
// AggregateAttemptStatus remains the separate JSON check contract.
func AggregateActionsAttemptStatus(results []CheckResult, cancelled bool) string {
	evidence := make([]actions.StepEvidence, len(results))
	for i, result := range results {
		evidence[i] = actions.StepEvidence{Status: result.Status, Role: result.Role, CleanupError: result.CleanupError}
	}
	return actions.AggregateAttemptStatus(evidence, cancelled)
}

func validCheckResultStatus(result CheckResult, workflow bool) bool {
	if !workflow {
		return result.Role == "" && validAttemptStatus(result.Status)
	}
	if result.Role != actions.RoleRun && result.Role != actions.RoleTolerated && result.Role != actions.RoleBuiltin {
		return false
	}
	return validAttemptStatus(result.Status) || result.Status == actions.StatusSkipped || result.Status == actions.StatusNotRun
}

// ValidOutputLimitFact rejects impossible metadata without treating absence as false.
func ValidOutputLimitFact(limit int64, status string, truncated bool) bool {
	return limit == 0 || limit > 0 && truncated && (status == AttemptIncomplete || status == AttemptError || status == AttemptCancelled)
}

// AttemptOutcome describes a verified durable summary without parsing its prose.
type AttemptOutcome struct {
	Workflow       bool
	Total          int
	Failed         int
	Error          int
	Unavailable    int
	Incomplete     int
	CancelledCount int
	Passed         int
	Worktree       string
	Cancelled      bool
	NothingRan     bool
	Tolerated      int
	Refusal        string
}

// Outcome returns facts only when the existing formatter reproduces the record.
func (attempt CheckAttempt) Outcome() (AttemptOutcome, bool) {
	if attempt.Status == AttemptPending || attempt.FinishedAt.IsZero() {
		return AttemptOutcome{}, false
	}
	workflow := len(attempt.Results) > 0 && attempt.Results[0].Role != ""
	status, summary := checkAttemptOutcome(attempt.Results, attempt.SubmittedCancelled, attempt.EffectiveWorktreeState(), workflow, attempt.CredentialID)
	if status != attempt.Status || summary != attempt.Summary {
		return AttemptOutcome{}, false
	}
	outcome := AttemptOutcome{Workflow: workflow, Total: len(attempt.Results), Worktree: attempt.EffectiveWorktreeState(), Cancelled: status == AttemptCancelled, NothingRan: workflow && status == actions.StatusSkipped}
	if !workflow && isJSONAdmissionRefusal(attempt.CredentialID) && len(attempt.Results) != 0 {
		outcome.Refusal = attempt.Results[0].OutputExcerpt
	}
	for _, result := range attempt.Results {
		switch effectiveCheckStatus(result) {
		case AttemptFailed:
			outcome.Failed++
		case AttemptError:
			outcome.Error++
		case AttemptUnavailable:
			outcome.Unavailable++
		case AttemptIncomplete:
			outcome.Incomplete++
		case AttemptCancelled:
			outcome.CancelledCount++
		case AttemptPassed:
			outcome.Passed++
		}
		if workflow && result.Role == actions.RoleTolerated && result.Status != actions.StatusPassed && result.Status != actions.StatusSkipped && result.Status != actions.StatusNotRun {
			outcome.Tolerated++
		}
	}
	return outcome, true
}

func checkAttemptOutcome(results []CheckResult, cancelled bool, worktree string, workflow bool, credentialID string) (string, string) {
	if !workflow {
		status := AggregateAttemptStatus(results, cancelled)
		return status, AttemptSummary(results, worktree, status, credentialID)
	}
	status := AggregateActionsAttemptStatus(results, cancelled)
	summary := strings.Replace(AttemptSummary(results, worktree, status), "checks", "steps", 1)
	if status == actions.StatusSkipped {
		summary = "Nothing ran: every step was skipped or built in."
	}
	tolerated := 0
	for _, result := range results {
		if result.Role == actions.RoleTolerated && result.Status != actions.StatusPassed && result.Status != actions.StatusSkipped && result.Status != actions.StatusNotRun {
			tolerated++
		}
	}
	if tolerated != 0 {
		summary += fmt.Sprintf("; %d tolerated failures", tolerated)
	}
	return status, summary
}
