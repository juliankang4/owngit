package server

import (
	"net/http"
	"time"

	"owngit/internal/actions"
	"owngit/internal/checkapi"
	"owngit/internal/state"
)

type workflowJobMetadata struct {
	ID              string                     `json:"id"`
	RepositoryID    string                     `json:"repository_id"`
	TaskID          string                     `json:"task_id"`
	RunID           string                     `json:"run_id"`
	JobKey          string                     `json:"job_key"`
	MatrixIndex     int                        `json:"matrix_index"`
	SourceOID       string                     `json:"source_oid"`
	Executor        string                     `json:"executor"`
	PolicyVersion   int64                      `json:"policy_version"`
	ConsentVersion  int64                      `json:"consent_version"`
	Tolerated       bool                       `json:"tolerated"`
	Status          string                     `json:"status"`
	Summary         string                     `json:"summary,omitempty"`
	Protection      string                     `json:"protection"`
	CancelRequested bool                       `json:"cancel_requested"`
	AdmittedAt      time.Time                  `json:"admitted_at"`
	StartedAt       *time.Time                 `json:"started_at,omitempty"`
	FinishedAt      *time.Time                 `json:"finished_at,omitempty"`
	Checks          []checkapi.CheckDefinition `json:"checks,omitempty"`
}

type workflowStepSummary struct {
	Name          string `json:"name"`
	Status        string `json:"status,omitempty"`
	Role          string `json:"role,omitempty"`
	ExitCode      *int   `json:"exit_code,omitempty"`
	DurationMS    int64  `json:"duration_ms"`
	CleanupFailed bool   `json:"cleanup_failed,omitempty"`
	Truncated     bool   `json:"truncated,omitempty"`
}

type workflowJobView struct {
	Job              *workflowJobMetadata  `json:"job"`
	Attempt          *checkapi.Attempt     `json:"attempt,omitempty"`
	Steps            []workflowStepSummary `json:"steps,omitempty"`
	ExcerptsIncluded bool                  `json:"excerpts_included"`
}

type workflowJobResponse struct {
	OK    bool   `json:"ok"`
	RunID string `json:"run_id"`
	workflowJobView
}

func workflowJobMetadataJSON(job state.CheckJob, checks []state.CheckDefinition) *workflowJobMetadata {
	result := &workflowJobMetadata{ID: job.ID, RepositoryID: job.RepositoryID, TaskID: job.TaskID, RunID: job.RunID, JobKey: job.JobKey, MatrixIndex: job.MatrixIndex, SourceOID: job.SourceOID, Executor: job.Executor, PolicyVersion: job.PolicyVersion, ConsentVersion: job.ConsentVersion, Tolerated: job.Tolerated, Status: job.Status, Summary: job.Summary, Protection: job.Protection, CancelRequested: job.CancelRequestedAt != nil, AdmittedAt: job.AdmittedAt, StartedAt: job.StartedAt, FinishedAt: job.FinishedAt}
	for _, check := range checks {
		name, command := actions.StepDisplay(actions.Step{Name: check.Name, Run: check.Command})
		result.Checks = append(result.Checks, checkapi.CheckDefinition{Name: name, Command: command})
	}
	return result
}

func (app *App) workflowJobRecord(request *http.Request, job state.CheckJob) (workflowJobView, error) {
	checks, err := app.jobChecks(request.Context(), job)
	if err != nil {
		return workflowJobView{}, err
	}
	item := workflowJobView{Job: workflowJobMetadataJSON(job, checks), ExcerptsIncluded: true}
	if job.AttemptID != "" {
		attempt, err := app.jobAttempt(request.Context(), job)
		if err != nil {
			return workflowJobView{}, err
		}
		item.Attempt = app.attemptJSON(request, attempt)
		for i := range item.Attempt.Results {
			result := &item.Attempt.Results[i]
			result.Name, result.Command = actions.StepDisplay(actions.Step{Name: result.Name, Run: result.Command})
		}
	}
	return item, nil
}

func workflowJobSummary(record workflowJobView) workflowJobView {
	job := *record.Job
	job.Checks = nil
	summary := workflowJobView{Job: &job, Steps: []workflowStepSummary{}}
	if record.Attempt != nil {
		attempt := *record.Attempt
		attempt.Results = nil
		summary.Attempt = &attempt
		for _, result := range record.Attempt.Results {
			summary.Steps = append(summary.Steps, workflowStepSummary{Name: result.Name, Status: result.Status, Role: result.Role, ExitCode: result.ExitCode, DurationMS: result.DurationMS, CleanupFailed: result.CleanupError != "", Truncated: result.Truncated})
		}
	}
	if len(summary.Steps) == 0 {
		for _, check := range record.Job.Checks {
			summary.Steps = append(summary.Steps, workflowStepSummary{Name: check.Name})
		}
	}
	return summary
}
