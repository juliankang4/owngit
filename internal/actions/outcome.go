package actions

const (
	StatusPending     = "pending"
	StatusWaiting     = "waiting"
	StatusClaimed     = "claimed"
	StatusStarted     = "started"
	StatusPassed      = "passed"
	StatusFailed      = "failed"
	StatusError       = "error"
	StatusIncomplete  = "incomplete"
	StatusUnavailable = "unavailable"
	StatusCancelled   = "cancelled"
	StatusInterrupted = "interrupted"
	StatusAmbiguous   = "ambiguous"
	StatusSkipped     = "skipped"
	StatusNotRun      = "not_run"
	StatusRefused     = "refused"
	StatusPartial     = "partial"
	StatusRunning     = "running"
	StatusQueued      = "queued"
)

const (
	RoleRun       = "run"
	RoleTolerated = "tolerated"
	RoleBuiltin   = "builtin"

	ResultSuccess   = "success"
	ResultFailure   = "failure"
	ResultCancelled = "cancelled"
	ResultSkipped   = "skipped"
)

// StepEvidence keeps raw outcomes separate from resolved continue-on-error.
// Empty roles belong to JSON checks, not Actions results.
type StepEvidence struct {
	Status       string `json:"status"`
	Role         string `json:"role"`
	CleanupError string `json:"cleanup_error,omitempty"`
}

// JobEvidence carries the facts needed without the machine-local job plan.
type JobEvidence struct {
	JobKey      string `json:"job_key"`
	MatrixIndex int    `json:"matrix_index"`
	PlanDigest  string `json:"plan_digest"`
	Tolerated   bool   `json:"tolerated"`
	Status      string `json:"status"`
}

type RunEvidence struct {
	// Outcome is empty for admitted jobs, or refused/not_run without jobs.
	Outcome string        `json:"outcome,omitempty"`
	Facts   RunFacts      `json:"facts"`
	Jobs    []JobEvidence `json:"jobs,omitempty"`
}

// StepConclusion supplies steps.<id>.conclusion and status-function evidence.
// A cleanup failure and cancellation cannot be tolerated.
func StepConclusion(result StepEvidence) string {
	if result.CleanupError != "" {
		return ResultFailure
	}
	switch result.Role {
	case RoleRun, RoleTolerated, RoleBuiltin:
	default:
		return ResultFailure
	}
	switch result.Status {
	case StatusPassed:
		return ResultSuccess
	case StatusSkipped, StatusNotRun:
		return ResultSkipped
	case StatusCancelled:
		return ResultCancelled
	case StatusFailed, StatusError, StatusIncomplete, StatusUnavailable:
		if result.Role == RoleTolerated {
			return ResultSuccess
		}
	}
	return ResultFailure
}

// AggregateAttemptStatus excludes built-ins and steps that did not run.
// Errors outrank cancellation; cleanup failures always remain errors.
func AggregateAttemptStatus(results []StepEvidence, cancelled bool) string {
	status := StatusSkipped
	rank := map[string]int{StatusError: 0, StatusCancelled: 1, StatusFailed: 2, StatusUnavailable: 3, StatusIncomplete: 4, StatusPassed: 5}
	for _, result := range results {
		if result.CleanupError != "" {
			return StatusError
		}
		switch result.Role {
		case RoleRun, RoleTolerated, RoleBuiltin:
		default:
			return StatusError
		}
		if result.Status == StatusSkipped || result.Status == StatusNotRun {
			continue
		}
		value, ok := rank[result.Status]
		if !ok {
			return StatusError
		}
		if result.Role == RoleBuiltin {
			continue
		}
		effective := result.Status
		if result.Role == RoleTolerated && effective != StatusCancelled {
			effective, value = StatusPassed, rank[StatusPassed]
		}
		if status == StatusSkipped || value < rank[status] {
			status = effective
		}
	}
	if cancelled && status != StatusError {
		return StatusCancelled
	}
	return status
}

// NeedsResult maps job outcomes to GitHub's four dependency results.
// Job tolerance accepts a failed command, not uncertain or unavailable execution.
func NeedsResult(status string, tolerated bool) string {
	switch status {
	case StatusPassed:
		return ResultSuccess
	case StatusFailed:
		if tolerated {
			return ResultSuccess
		}
	case StatusCancelled:
		return ResultCancelled
	case StatusSkipped:
		return ResultSkipped
	}
	return ResultFailure
}

// RunConclusion counts all siblings, never only the latest attempt.
func RunConclusion(run RunEvidence) string {
	if run.Outcome != "" {
		if len(run.Jobs) == 0 && (run.Outcome == StatusRefused || run.Outcome == StatusNotRun) {
			return run.Outcome
		}
		return StatusIncomplete
	}
	if len(run.Jobs) == 0 && len(run.Facts.RefusedJobs) != 0 {
		return StatusRefused
	}
	conclusions := make([]string, 0, len(run.Jobs)+1)
	for _, job := range run.Jobs {
		switch job.Status {
		case StatusPending, StatusWaiting:
			conclusions = append(conclusions, StatusQueued)
		case StatusClaimed, StatusStarted:
			conclusions = append(conclusions, StatusRunning)
		case StatusFailed:
			if job.Tolerated {
				conclusions = append(conclusions, StatusPassed)
			} else {
				conclusions = append(conclusions, StatusFailed)
			}
		case StatusPassed, StatusCancelled, StatusSkipped:
			conclusions = append(conclusions, job.Status)
		default:
			conclusions = append(conclusions, StatusIncomplete)
		}
	}
	if len(run.Facts.RefusedJobs) != 0 {
		conclusions = append(conclusions, StatusPartial)
	}
	return firstConclusion(conclusions, []string{StatusRunning, StatusQueued, StatusFailed, StatusIncomplete, StatusCancelled, StatusPartial, StatusPassed}, StatusSkipped)
}

// RevisionConclusion combines lane conclusions. Refused workflows are neutral,
// but partial runs are not. Empty evidence lets callers use their stale fallback.
func RevisionConclusion(conclusions []string) string {
	if len(conclusions) == 0 {
		return ""
	}
	normalized := make([]string, len(conclusions))
	for i, conclusion := range conclusions {
		switch conclusion {
		case StatusFailed, StatusIncomplete, StatusCancelled, StatusRunning, StatusQueued, StatusNotRun, StatusPartial, StatusPassed, StatusSkipped, StatusRefused:
			normalized[i] = conclusion
		default:
			normalized[i] = StatusIncomplete
		}
	}
	return firstConclusion(normalized, []string{StatusFailed, StatusIncomplete, StatusCancelled, StatusRunning, StatusQueued, StatusNotRun, StatusPartial, StatusPassed}, StatusSkipped)
}

func firstConclusion(conclusions, order []string, fallback string) string {
	for _, candidate := range order {
		for _, conclusion := range conclusions {
			if candidate == conclusion {
				return candidate
			}
		}
	}
	return fallback
}
