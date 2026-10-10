package server

import (
	"owngit/internal/state"
	"owngit/internal/webui"
)

func browserOutcome(outcome state.AttemptOutcome) *webui.CheckOutcome {
	return &webui.CheckOutcome{
		Workflow: outcome.Workflow, Total: outcome.Total,
		Failed: outcome.Failed, Error: outcome.Error, Unavailable: outcome.Unavailable,
		Incomplete: outcome.Incomplete, CancelledCount: outcome.CancelledCount, Passed: outcome.Passed,
		Worktree: outcome.Worktree, Cancelled: outcome.Cancelled, NothingRan: outcome.NothingRan,
		Tolerated: outcome.Tolerated, Refusal: outcome.Refusal,
	}
}

func browserAttemptOutcome(attempt state.CheckAttempt) *webui.CheckOutcome {
	if outcome, ok := attempt.Outcome(); ok {
		return browserOutcome(outcome)
	}
	return nil
}

func browserJobOutcome(attemptID, summary string, attempt state.CheckAttempt) *webui.CheckOutcome {
	if attemptID != attempt.ID || summary != attempt.Summary {
		return nil
	}
	return browserAttemptOutcome(attempt)
}
