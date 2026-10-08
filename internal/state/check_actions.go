package state

import "owngit/internal/actions"

// AggregateActionsAttemptStatus uses portable roles, not a retained job plan.
// AggregateAttemptStatus remains the separate JSON check contract.
func AggregateActionsAttemptStatus(results []CheckResult, cancelled bool) string {
	evidence := make([]actions.StepEvidence, len(results))
	for i, result := range results {
		evidence[i] = actions.StepEvidence{Status: result.Status, Role: result.Role, CleanupError: result.CleanupError}
	}
	return actions.AggregateAttemptStatus(evidence, cancelled)
}
