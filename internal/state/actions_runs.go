package state

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"strings"
	"time"

	"owngit/internal/actions"
)

const (
	ActionsEventDispatch    = "workflow_dispatch"
	ActionsEventSchedule    = "schedule"
	MaximumActionsRunJobs   = 16
	MaximumActionsJSONBytes = 64 << 10
	MaximumActionsPlanBytes = 2 << 20
)

var (
	ErrInvalidActionsRun   = errors.New("invalid workflow run")
	ErrActionsWorkflowsOff = errors.New("workflows are off for this repository")
)

type ActionsRun struct {
	ID                string           `json:"id"`
	RepositoryID      string           `json:"repository_id"`
	WorkflowPath      string           `json:"workflow_path"`
	Number            int64            `json:"number"`
	Event             string           `json:"event"`
	EventKey          string           `json:"event_key"`
	SourceOID         string           `json:"source_oid"`
	BaseOID           string           `json:"base_oid,omitempty"`
	PullRequestNumber int64            `json:"pull_request_number,omitempty"`
	TriggerRef        string           `json:"trigger_ref"`
	InputsJSON        string           `json:"inputs_json"`
	ScheduledFor      *time.Time       `json:"scheduled_for,omitempty"`
	RerunRoot         string           `json:"rerun_root,omitempty"`
	RerunGeneration   int64            `json:"rerun_generation,omitempty"`
	Outcome           string           `json:"outcome,omitempty"`
	Reason            string           `json:"reason,omitempty"`
	ConcurrencyGroup  string           `json:"concurrency_group,omitempty"`
	CancelInProgress  bool             `json:"cancel_in_progress,omitempty"`
	ConcurrencyQueue  string           `json:"concurrency_queue"`
	CancelRequestedAt *time.Time       `json:"cancel_requested_at,omitempty"`
	Facts             actions.RunFacts `json:"facts"`
	PolicyVersion     int64            `json:"policy_version"`
	ConsentVersion    int64            `json:"consent_version"`
	Actor             Actor            `json:"actor"`
	CreatedAt         time.Time        `json:"created_at"`
}

type ActionsJobRequest struct {
	CheckJobRequest
	Plan json.RawMessage
}

type ActionsRunRequest struct {
	Run  ActionsRun
	Jobs []ActionsJobRequest
}

// AdmitActionsRun writes one file's run, jobs and local plans atomically. Queue
// refusal is a durable not_run outcome, with no partially admitted jobs.
func (s *Store) AdmitActionsRun(ctx context.Context, request ActionsRunRequest, now time.Time) (ActionsRun, bool, error) {
	if now.IsZero() {
		return ActionsRun{}, false, fmt.Errorf("%w: missing time", ErrInvalidActionsRun)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ActionsRun{}, false, err
	}
	defer tx.Rollback()
	run, deduped, err := admitActionsRunTx(ctx, tx, request, now)
	if err != nil {
		return ActionsRun{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return ActionsRun{}, false, err
	}
	return run, deduped, nil
}

func admitActionsRunTx(ctx context.Context, tx *sql.Tx, request ActionsRunRequest, now time.Time) (ActionsRun, bool, error) {
	run := request.Run
	policy, exists, err := readCheckPolicyTx(ctx, tx, run.RepositoryID)
	if err != nil {
		return ActionsRun{}, false, err
	}
	if !exists {
		return ActionsRun{}, false, ErrCheckPolicyMissing
	}
	if !policy.RunWorkflows {
		return ActionsRun{}, false, ErrActionsWorkflowsOff
	}
	if !checkConsentCurrent(policy) {
		return ActionsRun{}, false, ErrCheckConsentRequired
	}
	if !policyAllowsCheckEvent(policy, run.Event) {
		return ActionsRun{}, false, ErrCheckEventNotAllowed
	}
	ceilings, err := checkCeilings(ctx, tx)
	if err != nil {
		return ActionsRun{}, false, err
	}
	if err := admissionError(ceilings.Exceeded(policy.CeilingValues())); err != nil {
		return ActionsRun{}, false, err
	}
	if run.ID == "" {
		run.ID, err = RandomID()
		if err != nil {
			return ActionsRun{}, false, err
		}
	}
	if len(run.InputsJSON) == 0 {
		run.InputsJSON = `{}`
	}
	if run.ConcurrencyQueue == "" {
		run.ConcurrencyQueue = "single"
	}
	run.CreatedAt, run.PolicyVersion, run.ConsentVersion = now.UTC(), policy.Version, policy.ConsentVersion
	normalizeActionsRunTimes(&run)
	if run.Number == 0 {
		if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(number),0)+1 FROM actions_runs WHERE repository_id=? AND workflow_path=?`, run.RepositoryID, run.WorkflowPath).Scan(&run.Number); err != nil {
			return ActionsRun{}, false, err
		}
	}
	if err := validateActionsRun(run); err != nil {
		return ActionsRun{}, false, err
	}
	if err := validateActionsRunRootTx(ctx, tx, run); err != nil {
		return ActionsRun{}, false, err
	}
	existing, found, err := readActionsRunTx(ctx, tx, actionsRunSelect+` WHERE repository_id=? AND event=? AND event_key=? AND workflow_path=? AND rerun_generation=?`, run.RepositoryID, run.Event, run.EventKey, run.WorkflowPath, run.RerunGeneration)
	if err != nil {
		return ActionsRun{}, false, err
	}
	if found {
		return existing, true, nil
	}
	if len(request.Jobs) > MaximumActionsRunJobs || (run.Outcome != "" && len(request.Jobs) != 0) || (run.Outcome == "" && len(request.Jobs) == 0) {
		return ActionsRun{}, false, fmt.Errorf("%w: invalid job count or outcome", ErrInvalidActionsRun)
	}
	identities := make(map[string]bool, len(request.Jobs))
	for i := range request.Jobs {
		job := &request.Jobs[i]
		job.RepositoryID, job.Trigger, job.EventKey = run.RepositoryID, run.Event, run.EventKey
		job.SourceOID, job.BaseOID, job.PullRequestNumber = run.SourceOID, run.BaseOID, run.PullRequestNumber
		job.TriggerRef, job.WorkflowPath, job.RunID = run.TriggerRef, run.WorkflowPath, run.ID
		job.RerunRoot, job.RerunGeneration = run.RerunRoot, run.RerunGeneration
		if err := validateCheckJobRequest(job.CheckJobRequest); err != nil {
			return ActionsRun{}, false, err
		}
		if err := validateActionsPlan(job.Plan, job.JobKey, job.MatrixIndex, job.PlanDigest, len(job.Checks)); err != nil {
			return ActionsRun{}, false, err
		}
		key := fmt.Sprintf("%s/%d", job.JobKey, job.MatrixIndex)
		if identities[key] {
			return ActionsRun{}, false, fmt.Errorf("%w: duplicate job", ErrInvalidActionsRun)
		}
		identities[key] = true
	}
	unfinished, err := countUnfinishedCheckJobsTx(ctx, tx, run.RepositoryID)
	if err != nil {
		return ActionsRun{}, false, err
	}
	if run.Outcome == "" && len(request.Jobs) > policy.QueueLimit {
		run.Outcome = actions.StatusRefused
		run.Reason = fmt.Sprintf("This workflow needs %d jobs at once, but the check policy's queue holds at most %d. Raise queue_limit, or make the matrix smaller.", len(request.Jobs), policy.QueueLimit)
		run.Facts.Notes = append(run.Facts.Notes, actions.Message{Code: "workflow.never_fits", Detail: run.Reason})
	} else if run.Outcome == "" && unfinished+len(request.Jobs) > policy.QueueLimit {
		run.Outcome = actions.StatusNotRun
		run.Reason = fmt.Sprintf("Not run: the queue holds %d of %d jobs and this run needs %d.", unfinished, policy.QueueLimit, len(request.Jobs))
		run.Facts.Notes = append(run.Facts.Notes, actions.Message{Code: "workflow.not_run_queue", Detail: run.Reason})
	}
	if err := validateActionsRun(run); err != nil {
		return ActionsRun{}, false, err
	}
	if err := insertActionsRunTx(ctx, tx, run); err != nil {
		return ActionsRun{}, false, err
	}
	if run.Outcome != "" {
		return run, false, nil
	}
	for _, request := range request.Jobs {
		job, deduped, err := admitCheckJobTx(ctx, tx, request.CheckJobRequest, now)
		if err != nil {
			return ActionsRun{}, false, err
		}
		if deduped {
			return ActionsRun{}, false, fmt.Errorf("%w: job already belongs to another run", ErrInvalidActionsRun)
		}
		if err := validateActionsJobRun(job, run); err != nil {
			return ActionsRun{}, false, fmt.Errorf("%w: %v", ErrInvalidCheckJob, err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO actions_job_plans(job_id,plan_json,plan_digest) VALUES(?,?,?)`, job.ID, string(request.Plan), job.PlanDigest); err != nil {
			return ActionsRun{}, false, err
		}
	}
	return run, false, nil
}

func insertActionsRunTx(ctx context.Context, tx *sql.Tx, run ActionsRun) error {
	facts, err := json.Marshal(run.Facts)
	if err != nil {
		return err
	}
	actor, err := encodeActor(run.Actor)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO actions_runs(id,repository_id,workflow_path,number,event,event_key,source_oid,base_oid,pull_request_number,trigger_ref,inputs_json,scheduled_for,rerun_root,rerun_generation,outcome,reason,concurrency_group,cancel_in_progress,concurrency_queue,cancel_requested_at,facts_json,policy_version,consent_version,actor,created_at)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		run.ID, run.RepositoryID, run.WorkflowPath, run.Number, run.Event, run.EventKey, run.SourceOID, run.BaseOID, run.PullRequestNumber, run.TriggerRef, run.InputsJSON, nullableNano(run.ScheduledFor), run.RerunRoot, run.RerunGeneration, run.Outcome, run.Reason, run.ConcurrencyGroup, boolInt(run.CancelInProgress), run.ConcurrencyQueue, nullableNano(run.CancelRequestedAt), string(facts), run.PolicyVersion, run.ConsentVersion, actor, run.CreatedAt.UnixNano())
	return err
}

const actionsRunSelect = `SELECT id,repository_id,workflow_path,number,event,event_key,source_oid,base_oid,pull_request_number,trigger_ref,inputs_json,scheduled_for,rerun_root,rerun_generation,outcome,reason,concurrency_group,cancel_in_progress,concurrency_queue,cancel_requested_at,facts_json,policy_version,consent_version,actor,created_at FROM actions_runs`

func (s *Store) ActionsRun(ctx context.Context, repositoryID, id string) (ActionsRun, bool, error) {
	return readActionsRunTx(ctx, s.db, actionsRunSelect+` WHERE repository_id=? AND id=?`, repositoryID, id)
}

// ActionsRuns returns newest runs first. A zero limit uses the bounded default.
func (s *Store) ActionsRuns(ctx context.Context, repositoryID string, limit int) ([]ActionsRun, error) {
	if limit <= 0 || limit > 1000 {
		limit = 50
	}
	return readActionsRuns(ctx, s.db, actionsRunSelect+` WHERE repository_id=? ORDER BY created_at DESC,id DESC LIMIT ?`, repositoryID, limit)
}

func (s *Store) ActionsRunsForRevision(ctx context.Context, repositoryID, oid string) ([]ActionsRun, error) {
	return readActionsRuns(ctx, s.db, actionsRunSelect+` WHERE repository_id=? AND source_oid=? ORDER BY workflow_path,rerun_generation DESC`, repositoryID, oid)
}

func (s *Store) ActionsRunJobs(ctx context.Context, repositoryID, runID string) ([]CheckJob, error) {
	rows, err := s.db.QueryContext(ctx, checkJobSelect+` WHERE repository_id=? AND run_id=? ORDER BY job_key,matrix_index,id`, repositoryID, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var jobs []CheckJob
	for rows.Next() {
		job, err := scanCheckJob(rows)
		if err != nil {
			return nil, err
		}
		jobs = append(jobs, job)
	}
	return jobs, rows.Err()
}

// ActionsJobPlan checks the exact encoded plan against both durable digests.
func (s *Store) ActionsJobPlan(ctx context.Context, repositoryID, jobID string) (json.RawMessage, bool, error) {
	var encoded, digest, jobDigest, key string
	var index int
	err := s.db.QueryRowContext(ctx, `SELECT p.plan_json,p.plan_digest,j.plan_digest,j.job_key,j.matrix_index FROM actions_job_plans p JOIN check_jobs j ON j.id=p.job_id WHERE j.repository_id=? AND j.id=? AND j.run_id!=''`, repositoryID, jobID).Scan(&encoded, &digest, &jobDigest, &key, &index)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	if digest != jobDigest {
		return nil, false, errors.New("workflow plan digest does not match its job")
	}
	if err := validateActionsPlan(json.RawMessage(encoded), key, index, digest, 0); err != nil {
		return nil, false, err
	}
	return json.RawMessage(encoded), true, nil
}

func readActionsRunTx(ctx context.Context, queryer querier, query string, args ...any) (ActionsRun, bool, error) {
	run, err := scanActionsRun(queryer.QueryRowContext(ctx, query, args...))
	if errors.Is(err, sql.ErrNoRows) {
		return ActionsRun{}, false, nil
	}
	return run, err == nil, err
}

func readActionsRuns(ctx context.Context, queryer querier, query string, args ...any) ([]ActionsRun, error) {
	rows, err := queryer.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var runs []ActionsRun
	for rows.Next() {
		run, err := scanActionsRun(rows)
		if err != nil {
			return nil, err
		}
		runs = append(runs, run)
	}
	return runs, rows.Err()
}

func scanActionsRun(scanner rowScanner) (ActionsRun, error) {
	var run ActionsRun
	var inputs, facts, actor string
	var created int64
	var cancelInProgress int
	var scheduled, cancelled sql.NullInt64
	if err := scanner.Scan(&run.ID, &run.RepositoryID, &run.WorkflowPath, &run.Number, &run.Event, &run.EventKey, &run.SourceOID, &run.BaseOID, &run.PullRequestNumber, &run.TriggerRef, &inputs, &scheduled, &run.RerunRoot, &run.RerunGeneration, &run.Outcome, &run.Reason, &run.ConcurrencyGroup, &cancelInProgress, &run.ConcurrencyQueue, &cancelled, &facts, &run.PolicyVersion, &run.ConsentVersion, &actor, &created); err != nil {
		return ActionsRun{}, err
	}
	decoder := json.NewDecoder(strings.NewReader(facts))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&run.Facts); err != nil {
		return ActionsRun{}, err
	}
	var err error
	run.Actor, err = decodeActor(actor)
	if err != nil {
		return ActionsRun{}, err
	}
	run.InputsJSON = inputs
	run.ScheduledFor, run.CancelRequestedAt = nullableNanoTime(scheduled), nullableNanoTime(cancelled)
	run.CancelInProgress, run.CreatedAt = cancelInProgress != 0, unixNanoTime(created)
	normalizeActionsRunTimes(&run)
	return run, nil
}

func normalizeActionsRunTimes(run *ActionsRun) {
	run.CreatedAt = run.CreatedAt.UTC()
	for _, observed := range []**time.Time{&run.ScheduledFor, &run.CancelRequestedAt} {
		if *observed != nil {
			utc := (**observed).UTC()
			*observed = &utc
		}
	}
}

func validActionsWorkflowPath(value string) bool {
	name := strings.TrimPrefix(value, ".github/workflows/")
	return name != value && name != "" && len(name) <= 100 && !strings.ContainsAny(name, "/\\\x00\r\n") && (path.Ext(name) == ".yml" || path.Ext(name) == ".yaml")
}

func validateActionsJobFacts(job CheckJob) error {
	if job.RunID == "" {
		if job.JobKey != "" || job.MatrixIndex != 0 || job.PlanDigest != "" || job.Tolerated || job.ConcurrencyGroup != "" || job.MaxParallel != 0 || job.Status == CheckJobWaiting || job.Status == CheckJobSkipped {
			return fmt.Errorf("%w: workflow facts on a JSON job", ErrInvalidCheckJob)
		}
		return nil
	}
	if !validAttemptID(job.RunID) || !validText(job.JobKey, 100) || job.MatrixIndex < 0 || job.MatrixIndex >= MaximumActionsRunJobs || !validDigest(job.PlanDigest) || len(job.ConcurrencyGroup) > 200 || strings.ContainsAny(job.ConcurrencyGroup, "\x00\r\n") || job.MaxParallel < 0 || job.MaxParallel > MaximumActionsRunJobs {
		return fmt.Errorf("%w: invalid workflow job facts", ErrInvalidCheckJob)
	}
	return nil
}

func validateActionsPlan(encoded json.RawMessage, key string, index int, digest string, expectedSteps int) error {
	if len(encoded) == 0 || len(encoded) > MaximumActionsPlanBytes || !json.Valid(encoded) || fmt.Sprintf("%x", sha256.Sum256(encoded)) != digest {
		return fmt.Errorf("%w: invalid workflow plan or digest", ErrInvalidCheckJob)
	}
	var plan actions.JobPlan
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&plan); err != nil || plan.JobKey != key || plan.MatrixIndex != index || len(plan.Steps) == 0 || len(plan.Steps) > MaximumCheckDefinitions || expectedSteps > 0 && len(plan.Steps) != expectedSteps || plan.Context.GitHub.RunID != "" || plan.Context.GitHub.RunNumber != 0 || plan.Context.GitHub.RunAttempt != 0 {
		return fmt.Errorf("%w: workflow plan does not describe its job", ErrInvalidCheckJob)
	}
	return nil
}

func validateActionsRun(run ActionsRun) error {
	if !validAttemptID(run.ID) || run.RepositoryID == "" || !validActionsWorkflowPath(run.WorkflowPath) || run.Number <= 0 || !validObjectID(run.SourceOID) || run.PolicyVersion <= 0 || run.ConsentVersion <= 0 || run.CreatedAt.IsZero() {
		return fmt.Errorf("%w: invalid identity", ErrInvalidActionsRun)
	}
	if run.TriggerRef == "" || run.TriggerRef == "@" || !validBranchText(run.TriggerRef) || len(run.TriggerRef) > MaximumCheckTriggerRefBytes || len(run.EventKey) == 0 || len(run.EventKey) > MaximumCheckEventKeyBytes || strings.ContainsAny(run.EventKey, "\x00\r\n") {
		return fmt.Errorf("%w: invalid event context", ErrInvalidActionsRun)
	}
	switch run.Event {
	case "push", ActionsEventDispatch, ActionsEventSchedule:
		if run.PullRequestNumber != 0 || run.BaseOID != "" {
			return fmt.Errorf("%w: unexpected pull request facts", ErrInvalidActionsRun)
		}
	case "pull_request":
		if run.PullRequestNumber <= 0 || !validObjectID(run.BaseOID) {
			return fmt.Errorf("%w: missing pull request facts", ErrInvalidActionsRun)
		}
	default:
		return fmt.Errorf("%w: unknown event", ErrInvalidActionsRun)
	}
	if (run.ScheduledFor != nil) != (run.Event == ActionsEventSchedule) || (run.ScheduledFor != nil && run.ScheduledFor.IsZero()) || (run.CancelRequestedAt != nil && run.CancelRequestedAt.IsZero()) {
		return fmt.Errorf("%w: invalid observed time", ErrInvalidActionsRun)
	}
	facts, err := json.Marshal(run.Facts)
	if err != nil || len(facts) > MaximumActionsJSONBytes || len(run.InputsJSON) > MaximumActionsJSONBytes || !json.Valid([]byte(run.InputsJSON)) || !strings.HasPrefix(strings.TrimSpace(run.InputsJSON), "{") {
		return fmt.Errorf("%w: invalid portable JSON", ErrInvalidActionsRun)
	}
	if run.ConcurrencyQueue != "single" && run.ConcurrencyQueue != "max" || run.ConcurrencyQueue == "max" && run.CancelInProgress || len(run.ConcurrencyGroup) > 200 || strings.ContainsAny(run.ConcurrencyGroup, "\x00\r\n") || (run.Outcome != "" && run.Outcome != actions.StatusRefused && run.Outcome != actions.StatusNotRun) || len(run.Reason) > MaximumActionsJSONBytes {
		return fmt.Errorf("%w: invalid outcome or concurrency", ErrInvalidActionsRun)
	}
	if (run.RerunRoot == "") != (run.RerunGeneration == 0) || run.RerunGeneration < 0 || (run.RerunRoot != "" && (!validAttemptID(run.RerunRoot) || run.RerunRoot == run.ID)) {
		return fmt.Errorf("%w: invalid rerun identity", ErrInvalidActionsRun)
	}
	return run.Actor.Validate()
}

func validateActionsRunRootTx(ctx context.Context, tx *sql.Tx, run ActionsRun) error {
	if run.RerunRoot == "" {
		return nil
	}
	root, exists, err := readActionsRunTx(ctx, tx, actionsRunSelect+` WHERE id=?`, run.RerunRoot)
	if err != nil {
		return err
	}
	if !exists || !sameActionsRunRoot(run, root) {
		return fmt.Errorf("%w: unknown or mismatched rerun root", ErrInvalidActionsRun)
	}
	return nil
}

func sameActionsRunRoot(run, root ActionsRun) bool {
	return root.RerunRoot == "" && root.RepositoryID == run.RepositoryID && root.WorkflowPath == run.WorkflowPath && root.Event == run.Event && root.EventKey == run.EventKey && root.SourceOID == run.SourceOID && root.BaseOID == run.BaseOID && root.PullRequestNumber == run.PullRequestNumber && root.TriggerRef == run.TriggerRef && root.InputsJSON == run.InputsJSON
}
