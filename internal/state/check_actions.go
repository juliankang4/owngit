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

func checkAttemptOutcome(results []CheckResult, cancelled bool, worktree string, workflow bool) (string, string) {
	if !workflow {
		status := AggregateAttemptStatus(results, cancelled)
		return status, AttemptSummary(results, worktree, status)
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
