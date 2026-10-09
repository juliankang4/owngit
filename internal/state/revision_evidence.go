package state

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"owngit/internal/actions"
)

// RevisionCheckEvidence keeps JSON attempts and workflow runs as separate lanes.
type RevisionCheckEvidence struct {
	RevisionOID        string              `json:"revision_oid"`
	Conclusion         string              `json:"conclusion"`
	JSONAttempt        *CheckAttempt       `json:"-"`
	JSONRevisionOID    string              `json:"json_revision_oid,omitempty"`
	JSONConclusion     string              `json:"json_conclusion,omitempty"`
	JSONStale          bool                `json:"json_stale,omitempty"`
	Workflows          []ActionsRunSummary `json:"workflows"`
	WorkflowsTotal     int                 `json:"workflows_total"`
	WorkflowsTruncated bool                `json:"workflows_truncated,omitempty"`
}

const (
	MaximumActionsRunSummaryNoteKeys     = 8
	MaximumActionsRunSummaryNoteKeyBytes = 32
	MaximumRevisionWorkflowSummaries     = 22
)

type ActionsRunSummaryCounts struct {
	Total      int `json:"total"`
	Waiting    int `json:"waiting"`
	Queued     int `json:"queued"`
	Running    int `json:"running"`
	Passed     int `json:"passed"`
	Failed     int `json:"failed"`
	Cancelled  int `json:"cancelled"`
	Skipped    int `json:"skipped"`
	Incomplete int `json:"incomplete"`
	Refused    int `json:"refused"`
	Notes      int `json:"notes"`
}

type ActionsRunSummary struct {
	ID             string                  `json:"id"`
	WorkflowPath   string                  `json:"workflow_path"`
	Event          string                  `json:"event"`
	Status         string                  `json:"status"`
	Conclusion     string                  `json:"conclusion"`
	CreatedAt      time.Time               `json:"created_at"`
	StartedAt      *time.Time              `json:"started_at,omitempty"`
	FinishedAt     *time.Time              `json:"finished_at,omitempty"`
	Counts         ActionsRunSummaryCounts `json:"counts"`
	NoteKeys       []string                `json:"note_keys"`
	NotesTruncated bool                    `json:"notes_truncated,omitempty"`
	Stale          bool                    `json:"stale,omitempty"`
}

type ActionsRunEvidence struct {
	ActionsRun
	Conclusion string `json:"conclusion"`
}

const jsonAttemptCondition = ` NOT EXISTS (SELECT 1 FROM check_jobs j WHERE j.id=check_attempts.job_id AND j.run_id<>'')`

func (s *Store) LatestJSONCheckAttemptForRevision(ctx context.Context, repositoryID, oid string) (CheckAttempt, bool, error) {
	return s.latestCheckAttempt(ctx, attemptSelect+` WHERE repository_id=? AND revision_oid=? AND `+jsonAttemptCondition+` ORDER BY sequence DESC LIMIT 1`, repositoryID, oid)
}

// ActionsRunEvidence counts every job, including jobs without an attempt.
func (s *Store) ActionsRunEvidence(ctx context.Context, run ActionsRun) (ActionsRunEvidence, error) {
	jobs, err := s.ActionsRunJobs(ctx, run.RepositoryID, run.ID)
	if err != nil {
		return ActionsRunEvidence{}, err
	}
	return actionsRunEvidence(run, jobs), nil
}

func actionsRunEvidence(run ActionsRun, jobs []CheckJob) ActionsRunEvidence {
	evidence := actions.RunEvidence{Outcome: run.Outcome, Facts: run.Facts}
	for _, job := range jobs {
		evidence.Jobs = append(evidence.Jobs, actions.JobEvidence{JobKey: job.JobKey, MatrixIndex: job.MatrixIndex, PlanDigest: job.PlanDigest, Tolerated: job.Tolerated, Status: job.Status})
	}
	return ActionsRunEvidence{ActionsRun: run, Conclusion: actions.RunConclusion(evidence)}
}

func (s *Store) ActionsRunSummary(ctx context.Context, run ActionsRun) (ActionsRunSummary, error) {
	jobs, err := s.ActionsRunJobs(ctx, run.RepositoryID, run.ID)
	if err != nil {
		return ActionsRunSummary{}, err
	}
	return actionsRunSummary(actionsRunEvidence(run, jobs), jobs), nil
}

func actionsRunSummary(evidence ActionsRunEvidence, jobs []CheckJob) ActionsRunSummary {
	summary := ActionsRunSummary{ID: evidence.ID, WorkflowPath: evidence.WorkflowPath, Event: evidence.Event, Conclusion: evidence.Conclusion, CreatedAt: evidence.CreatedAt, NoteKeys: []string{}}
	switch evidence.Conclusion {
	case actions.StatusQueued:
		summary.Status = actions.StatusQueued
	case actions.StatusRunning:
		summary.Status = actions.StatusRunning
	default:
		summary.Status = "completed"
	}
	summary.Counts.Total = len(jobs)
	finished := len(jobs) > 0
	for _, job := range jobs {
		switch job.Status {
		case CheckJobWaiting:
			summary.Counts.Waiting++
		case CheckJobPending:
			summary.Counts.Queued++
		case CheckJobClaimed, CheckJobStarted:
			summary.Counts.Running++
		case CheckJobPassed:
			summary.Counts.Passed++
		case CheckJobFailed:
			summary.Counts.Failed++
		case CheckJobCancelled:
			summary.Counts.Cancelled++
		case CheckJobSkipped:
			summary.Counts.Skipped++
		default:
			summary.Counts.Incomplete++
		}
		if job.StartedAt != nil && (summary.StartedAt == nil || job.StartedAt.Before(*summary.StartedAt)) {
			started := *job.StartedAt
			summary.StartedAt = &started
		}
		if job.FinishedAt == nil {
			finished = false
		} else if summary.FinishedAt == nil || job.FinishedAt.After(*summary.FinishedAt) {
			ended := *job.FinishedAt
			summary.FinishedAt = &ended
		}
	}
	if len(jobs) == 0 && evidence.Outcome != "" {
		ended := evidence.CreatedAt
		summary.FinishedAt = &ended
	} else if !finished {
		summary.FinishedAt = nil
	}
	summary.Counts.Refused = len(evidence.Facts.RefusedJobs)
	keys := make([]string, 0, len(evidence.Facts.Notes)+len(evidence.Facts.RefusedJobs))
	for _, note := range evidence.Facts.Notes {
		keys = append(keys, note.Code)
	}
	for _, refused := range evidence.Facts.RefusedJobs {
		keys = append(keys, refused.Reason.Code)
	}
	for _, job := range jobs {
		keys = append(keys, actionsSummaryKeys(job.Summary)...)
	}
	summary.Counts.Notes = len(keys)
	seen := map[string]bool{}
	for _, key := range keys {
		if !validActionsSummaryNoteKey(key) {
			summary.NotesTruncated = true
			continue
		}
		if seen[key] {
			continue
		}
		seen[key] = true
		if len(summary.NoteKeys) == MaximumActionsRunSummaryNoteKeys {
			summary.NotesTruncated = true
			continue
		}
		summary.NoteKeys = append(summary.NoteKeys, key)
	}
	return summary
}

func actionsSummaryKeys(text string) []string {
	var keys []string
	for _, field := range strings.Fields(text) {
		key := strings.TrimSuffix(field, ":")
		if strings.HasPrefix(key, "note.") && validActionsSummaryNoteKey(key) {
			keys = append(keys, key)
		}
	}
	return keys
}

func validActionsSummaryNoteKey(value string) bool {
	if value == "" || len(value) > MaximumActionsRunSummaryNoteKeyBytes {
		return false
	}
	for _, character := range value {
		if character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' || character >= '0' && character <= '9' || character == '.' || character == '_' || character == '-' {
			continue
		}
		return false
	}
	return true
}

// RevisionEvidence uses the latest generation of each workflow at this commit.
func (s *Store) RevisionEvidence(ctx context.Context, repositoryID, oid string) (RevisionCheckEvidence, error) {
	evidence := RevisionCheckEvidence{RevisionOID: oid, Workflows: []ActionsRunSummary{}}
	attempt, found, err := s.LatestJSONCheckAttemptForRevision(ctx, repositoryID, oid)
	if err != nil {
		return evidence, err
	}
	conclusions := []string{}
	if found {
		evidence.JSONAttempt = &attempt
		evidence.JSONRevisionOID, evidence.JSONConclusion = attempt.RevisionOID, attempt.Status
		conclusions = append(conclusions, jsonRevisionConclusion(attempt.Status))
	}
	runs, err := readActionsRuns(ctx, s.db, actionsRunSelect+` WHERE repository_id=? AND source_oid=? AND number=(
        SELECT MAX(latest.number) FROM actions_runs latest WHERE latest.repository_id=actions_runs.repository_id AND latest.workflow_path=actions_runs.workflow_path AND latest.source_oid=actions_runs.source_oid
    ) ORDER BY workflow_path`, repositoryID, oid)
	if err != nil {
		return evidence, err
	}
	evidence.WorkflowsTotal = len(runs)
	evidence.WorkflowsTruncated = len(runs) > MaximumRevisionWorkflowSummaries
	for _, run := range runs {
		item, err := s.ActionsRunSummary(ctx, run)
		if err != nil {
			return evidence, err
		}
		if len(evidence.Workflows) < MaximumRevisionWorkflowSummaries {
			evidence.Workflows = append(evidence.Workflows, item)
		}
		conclusions = append(conclusions, item.Conclusion)
	}
	evidence.Conclusion = actions.RevisionConclusion(conclusions)
	return evidence, nil
}

func jsonRevisionConclusion(status string) string {
	switch status {
	case AttemptPending:
		return actions.StatusRunning
	case AttemptError, AttemptUnavailable:
		return actions.StatusIncomplete
	default:
		return status
	}
}

// PullRequestRevisionEvidence falls back independently for each lane, only to
// revisions recorded for this pull request. Stale evidence never passes.
func (s *Store) PullRequestRevisionEvidence(ctx context.Context, repositoryID string, number int64, oid string) (RevisionCheckEvidence, error) {
	evidence, err := s.RevisionEvidence(ctx, repositoryID, oid)
	if err != nil {
		return evidence, err
	}
	if evidence.JSONAttempt == nil {
		attempt, found, err := s.latestCheckAttempt(ctx, attemptSelect+` WHERE repository_id=? AND `+jsonAttemptCondition+` AND revision_oid IN (SELECT source_oid FROM pull_request_revisions WHERE repository_id=? AND pull_request_number=?) ORDER BY sequence DESC LIMIT 1`, repositoryID, repositoryID, number)
		if err != nil {
			return evidence, err
		}
		if found {
			evidence.JSONAttempt = &attempt
			evidence.JSONRevisionOID, evidence.JSONConclusion, evidence.JSONStale = attempt.RevisionOID, attempt.Status, attempt.RevisionOID != oid
		}
	}
	if len(evidence.Workflows) == 0 {
		var previous string
		err := s.db.QueryRowContext(ctx, `SELECT source_oid FROM actions_runs WHERE repository_id=? AND source_oid IN (SELECT source_oid FROM pull_request_revisions WHERE repository_id=? AND pull_request_number=?) ORDER BY created_at DESC,rowid DESC LIMIT 1`, repositoryID, repositoryID, number).Scan(&previous)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return evidence, err
		}
		if err == nil {
			earlier, err := s.RevisionEvidence(ctx, repositoryID, previous)
			if err != nil {
				return evidence, err
			}
			evidence.Workflows = earlier.Workflows
			evidence.WorkflowsTotal = earlier.WorkflowsTotal
			evidence.WorkflowsTruncated = earlier.WorkflowsTruncated
			for i := range evidence.Workflows {
				evidence.Workflows[i].Stale = previous != oid
			}
		}
	}
	if evidence.Conclusion != "" && evidence.HasStaleEvidence() {
		evidence.Conclusion = actions.RevisionConclusion([]string{evidence.Conclusion, actions.StatusIncomplete})
	}
	return evidence, nil
}

func (evidence RevisionCheckEvidence) HasStaleEvidence() bool {
	if evidence.JSONStale {
		return true
	}
	for _, run := range evidence.Workflows {
		if run.Stale {
			return true
		}
	}
	return false
}

// TaskRevisionEvidence also finds queued workflow work before an attempt exists.
func (s *Store) TaskRevisionEvidence(ctx context.Context, repositoryID, taskID string) (RevisionCheckEvidence, error) {
	var oid string
	err := s.db.QueryRowContext(ctx, `SELECT revision_oid FROM (
 SELECT revision_oid, created_at*1000000000 AS observed_at, sequence AS ordinal FROM check_attempts WHERE repository_id=? AND task_id=?
 UNION ALL SELECT source_oid, admitted_at, rowid FROM check_jobs WHERE repository_id=? AND task_id=?
 ) ORDER BY observed_at DESC, ordinal DESC LIMIT 1`, repositoryID, taskID, repositoryID, taskID).Scan(&oid)
	if errors.Is(err, sql.ErrNoRows) {
		return RevisionCheckEvidence{Workflows: []ActionsRunSummary{}}, nil
	}
	if err != nil {
		return RevisionCheckEvidence{}, err
	}
	return s.RevisionEvidence(ctx, repositoryID, oid)
}
