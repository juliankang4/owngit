package state

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"time"

	"owngit/internal/checkworkflow"
)

// readCheckJobRecovery reads portable policy and job state inside the snapshot
// transaction.
func readCheckJobRecovery(ctx context.Context, tx *sql.Tx, snapshot *RecoveryState) error {
	rows, err := tx.QueryContext(ctx, checkPolicySelect+` ORDER BY repository_id`)
	if err != nil {
		return err
	}
	for rows.Next() {
		policy, err := scanCheckPolicy(rows)
		if err != nil {
			rows.Close()
			return err
		}
		snapshot.CheckPolicies = append(snapshot.CheckPolicies, policy)
	}
	if err := closeRows(rows); err != nil {
		return err
	}
	rows, err = tx.QueryContext(ctx, checkJobSelect+` ORDER BY repository_id,admitted_at,id`)
	if err != nil {
		return err
	}
	for rows.Next() {
		job, err := scanCheckJob(rows)
		if err != nil {
			rows.Close()
			return err
		}
		snapshot.CheckJobs = append(snapshot.CheckJobs, job)
	}
	return closeRows(rows)
}

// restoreCheckJobRecovery writes policies and jobs. Consent and every lease are
// machine-local, so the restored rows have no active consent and unfinished
// work is closed as interrupted instead of silently becoming claimable.
func restoreCheckJobRecovery(ctx context.Context, tx *sql.Tx, snapshot RecoveryState) error {
	for _, policy := range snapshot.CheckPolicies {
		authorityEpoch, err := RandomID()
		if err != nil {
			return fmt.Errorf("create restored check authority for %q: %w", policy.RepositoryID, err)
		}
		consentActive := 0
		executionJSON, err := checkExecutionSettingsJSON(policy.Execution)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO check_policies(
			repository_id,policy_version,policy_digest,executor,allowed_events,max_timeout_ms,max_output_limit_bytes,
			queue_limit,max_active_jobs,max_lease_ms,execution_json,consent_version,consent_digest,consent_active,runner_generation,
			authority_epoch,created_at,updated_at
		) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			policy.RepositoryID, policy.Version, policy.Digest, policy.Executor, marshalCheckEvents(policy.AllowedEvents),
			policy.MaxTimeoutMS, policy.MaxOutputLimitBytes, policy.QueueLimit, policy.MaxActiveJobs, policy.MaxLeaseMS, executionJSON,
			policy.ConsentVersion, policy.ConsentDigest, consentActive, policy.RunnerGeneration,
			authorityEpoch, policy.CreatedAt.Unix(), policy.UpdatedAt.Unix()); err != nil {
			return fmt.Errorf("restore check policy for %q: %w", policy.RepositoryID, err)
		}
	}
	for _, job := range snapshot.CheckJobs {
		if !terminalCheckJob(job.Status) {
			interruptedAt := latestCheckJobActivity(job)
			job.Status = CheckJobInterrupted
			job.InterruptedAt = &interruptedAt
			if job.FinishedAt == nil {
				job.FinishedAt = &interruptedAt
			}
			// The lease belonged to the previous process and cannot be resumed.
			job.LeaseID = ""
			job.LeaseExpiresAt = nil
		}
		limitsJSON, err := checkJobLimitsJSON(job.Limits)
		if err != nil {
			return err
		}
		executionJSON, err := checkExecutionSettingsJSON(job.Execution)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO check_jobs(
			id,repository_id,task_id,trigger_kind,event_key,source_oid,base_oid,pull_request_number,trigger_ref,workflow_path,
			workflow_oid,workflow_digest,configuration_version,executor,policy_version,consent_version,limits_json,execution_json,
			dedup_digest,rerun_root,rerun_generation,status,attempt_id,lease_id,lease_expires_at,credential_id,credential_generation,credential_role,
			protection,admitted_at,claimed_at,started_at,finished_at,lease_lost_at,cancel_requested_at,interrupted_at,summary
		) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			job.ID, job.RepositoryID, job.TaskID, job.Trigger, job.EventKey, job.SourceOID, job.BaseOID, job.PullRequestNumber,
			job.TriggerRef, job.WorkflowPath, job.WorkflowOID, job.WorkflowDigest, job.ConfigurationVersion,
			job.Executor, job.PolicyVersion, job.ConsentVersion, limitsJSON, executionJSON, job.DedupDigest, job.RerunRoot,
			job.RerunGeneration, job.Status, job.AttemptID, job.LeaseID, nullableNano(job.LeaseExpiresAt),
			job.CredentialID, job.CredentialGeneration, job.CredentialRole, job.Protection, job.AdmittedAt.UnixNano(),
			nullableNano(job.ClaimedAt), nullableNano(job.StartedAt), nullableNano(job.FinishedAt),
			nullableNano(job.LeaseLostAt), nullableNano(job.CancelRequestedAt), nullableNano(job.InterruptedAt),
			job.Summary); err != nil {
			return fmt.Errorf("restore check job %q: %w", job.ID, err)
		}
	}
	return nil
}

func checkJobLimitsJSON(limits CheckJobLimits) (string, error) {
	encoded, err := json.Marshal(limits)
	if err != nil {
		return "", err
	}
	return string(encoded), nil
}

// latestCheckJobActivity is the last observed activity of an interrupted job.
// Using stored facts keeps a restore deterministic.
func latestCheckJobActivity(job CheckJob) time.Time {
	latest := job.AdmittedAt
	for _, candidate := range []*time.Time{job.ClaimedAt, job.StartedAt} {
		if candidate != nil && candidate.After(latest) {
			latest = *candidate
		}
	}
	return latest
}

// validatePortableCheckJobs validates policy and job records and returns them
// by identity. Jobs are validated before attempts so an attempt can be checked
// against the job that owns its server-derived origin.
func validatePortableCheckJobs(snapshot RecoveryState, repositories map[string]bool, tasks map[string]RecoveryTask, configurations map[string]CheckConfiguration) (map[string]CheckJob, error) {
	policies := make(map[string]CheckPolicy, len(snapshot.CheckPolicies))
	for _, policy := range snapshot.CheckPolicies {
		if err := validatePortableCheckPolicy(policy, repositories, policies); err != nil {
			return nil, err
		}
		policies[policy.RepositoryID] = policy
	}
	jobs := make(map[string]CheckJob, len(snapshot.CheckJobs))
	digests := make(map[string]bool, len(snapshot.CheckJobs))
	for _, job := range snapshot.CheckJobs {
		if err := validatePortableCheckJob(job, repositories, tasks, policies, configurations); err != nil {
			return nil, err
		}
		if jobs[job.ID].ID != "" {
			return nil, errors.New("duplicate check job record")
		}
		jobs[job.ID] = job
		if digests[job.RepositoryID+"\x00"+job.DedupDigest] {
			return nil, errors.New("duplicate check job dedup digest")
		}
		digests[job.RepositoryID+"\x00"+job.DedupDigest] = true
	}
	// A rerun names the root of its chain, and the root is never its own child.
	for _, job := range jobs {
		if job.RerunRoot == "" {
			continue
		}
		if job.RerunRoot == job.ID {
			return nil, errors.New("check job rerun root refers to itself")
		}
		root, exists := jobs[job.RerunRoot]
		if !exists || root.RepositoryID != job.RepositoryID || root.RerunRoot != "" {
			return nil, errors.New("check job rerun root is unknown or is not the chain root")
		}
	}
	return jobs, nil
}

func validatePortableCheckPolicy(policy CheckPolicy, repositories map[string]bool, policies map[string]CheckPolicy) error {
	if !repositories[policy.RepositoryID] || policy.Version <= 0 || !validCheckExecutor(policy.Executor) {
		return errors.New("invalid check policy record")
	}
	allowed, err := normalizeCheckEvents(policy.AllowedEvents)
	if err != nil || !reflect.DeepEqual(allowed, policy.AllowedEvents) {
		return errors.New("check policy events are not canonical")
	}
	if policy.Digest != checkPolicyDigest(policy) {
		return errors.New("check policy digest does not match its facts")
	}
	if !policy.Execution.Legacy {
		normalized, err := normalizeCheckExecutionSettings(policy.Executor, policy.Execution)
		if err != nil || !reflect.DeepEqual(normalized, policy.Execution) {
			return errors.New("check policy execution settings are invalid")
		}
	}
	if policy.MaxTimeoutMS < checkworkflow.MinimumTimeoutMS || policy.MaxTimeoutMS > checkworkflow.MaximumTimeoutMS ||
		policy.MaxOutputLimitBytes < checkworkflow.MinimumOutputLimitBytes || policy.MaxOutputLimitBytes > checkworkflow.MaximumOutputLimitBytes ||
		policy.QueueLimit < 1 || policy.QueueLimit > MaximumCheckQueueLimit ||
		policy.MaxActiveJobs < 1 || policy.MaxActiveJobs > MaximumCheckActiveJobs ||
		policy.MaxLeaseMS < MinimumCheckLeaseMS || policy.MaxLeaseMS > MaximumCheckLeaseMS {
		return errors.New("check policy limits are out of range")
	}
	if policy.ConsentVersion == 0 {
		if policy.ConsentDigest != "" {
			return errors.New("check policy consent without a generation")
		}
	} else if !validDigest(policy.ConsentDigest) {
		return errors.New("invalid check policy consent digest")
	}
	if policy.ConsentActive && policy.ConsentVersion > 0 && policy.ConsentDigest != policy.Digest {
		return errors.New("active check consent does not match its policy")
	}
	// AuthorityEpoch is local, so a snapshot converted from a manifest has none.
	if policy.RunnerGeneration < 0 || (policy.AuthorityEpoch != "" && !validAttemptID(policy.AuthorityEpoch)) || policy.CreatedAt.IsZero() || policy.UpdatedAt.Before(policy.CreatedAt) {
		return errors.New("invalid check policy contents")
	}
	if policies[policy.RepositoryID].RepositoryID != "" {
		return errors.New("duplicate check policy record")
	}
	return nil
}

func validatePortableCheckJob(job CheckJob, repositories map[string]bool, tasks map[string]RecoveryTask, policies map[string]CheckPolicy, configurations map[string]CheckConfiguration) error {
	if !repositories[job.RepositoryID] || !validAttemptID(job.ID) || !validAttemptID(job.TaskID) || !validCheckJobStatus(job.Status) || !validCheckExecutor(job.Executor) {
		return errors.New("invalid check job record")
	}
	task, taskExists := tasks[job.TaskID]
	if !taskExists || task.RepositoryID != job.RepositoryID {
		return errors.New("check job refers to an unknown task")
	}
	policy, exists := policies[job.RepositoryID]
	if !exists || job.PolicyVersion <= 0 || job.PolicyVersion > policy.Version || job.ConsentVersion <= 0 || job.ConsentVersion > policy.ConsentVersion {
		return errors.New("check job policy generation is invalid")
	}
	switch job.Trigger {
	case checkworkflow.EventPush:
		if job.PullRequestNumber != 0 || job.BaseOID != "" {
			return errors.New("push check job carries pull request facts")
		}
	case checkworkflow.EventPullRequest:
		if job.PullRequestNumber <= 0 || !validObjectID(job.BaseOID) {
			return errors.New("pull request check job is missing its facts")
		}
	default:
		return errors.New("invalid check job trigger")
	}
	if !validObjectID(job.SourceOID) || job.TriggerRef == "" || job.TriggerRef == "@" || !validBranchText(job.TriggerRef) || len(job.TriggerRef) > MaximumCheckTriggerRefBytes {
		return errors.New("invalid check job trigger context")
	}
	expectedTaskID := automaticCheckTaskID(CheckJobRequest{
		RepositoryID: job.RepositoryID, Trigger: job.Trigger, TriggerRef: job.TriggerRef, PullRequestNumber: job.PullRequestNumber,
	})
	if job.TaskID != expectedTaskID {
		return errors.New("check job task does not match its trigger identity")
	}
	if len(job.EventKey) == 0 || len(job.EventKey) > MaximumCheckEventKeyBytes {
		return errors.New("invalid check job event key")
	}
	if job.WorkflowPath != checkworkflow.Path || !validDigest(job.WorkflowDigest) || (job.WorkflowOID != "" && !validObjectID(job.WorkflowOID)) {
		return errors.New("invalid check job workflow identity")
	}
	configuration, exists := configurations[configurationKey(job.RepositoryID, job.ConfigurationVersion)]
	if !exists {
		return errors.New("check job refers to an unknown configuration")
	}
	if job.Limits.TimeoutMS < checkworkflow.MinimumTimeoutMS || job.Limits.TimeoutMS > checkworkflow.MaximumTimeoutMS ||
		job.Limits.OutputLimitBytes < checkworkflow.MinimumOutputLimitBytes || job.Limits.OutputLimitBytes > checkworkflow.MaximumOutputLimitBytes {
		return errors.New("check job limits are out of range")
	}
	if !job.Execution.Legacy {
		normalized, err := normalizeCheckExecutionSettings(job.Executor, job.Execution)
		if err != nil || !reflect.DeepEqual(normalized, job.Execution) {
			return errors.New("check job execution settings are invalid")
		}
	}
	if job.PolicyVersion == policy.Version && (job.Executor != policy.Executor || !policyAllowsCheckEvent(policy, job.Trigger) ||
		job.Limits.TimeoutMS > policy.MaxTimeoutMS || job.Limits.OutputLimitBytes > policy.MaxOutputLimitBytes) {
		return errors.New("check job facts do not match their policy generation")
	}
	if !validDigest(job.DedupDigest) || job.DedupDigest != checkJobDedupDigest(job, configuration.ConfigHash) {
		return errors.New("check job dedup digest does not match its facts")
	}
	if (job.RerunRoot == "") != (job.RerunGeneration == 0) || job.RerunGeneration < 0 {
		return errors.New("invalid check job rerun identity")
	}
	if job.RerunRoot != "" && (!validAttemptID(job.RerunRoot) || job.RerunGeneration < 1) {
		return errors.New("invalid check job rerun identity")
	}
	if job.AttemptID != "" && !validAttemptID(job.AttemptID) {
		return errors.New("invalid check job attempt identity")
	}
	if (job.CredentialID == "") != (job.CredentialGeneration == 0) || (job.CredentialID != "" && !validAttemptID(job.CredentialID)) ||
		(job.CredentialID == "" && job.CredentialRole != "") ||
		(job.CredentialID != "" && !validRunnerRole(job.CredentialRole) && !(job.Execution.Legacy && job.CredentialRole == "")) {
		return errors.New("invalid check job credential evidence")
	}
	if job.AdmittedAt.IsZero() || len(job.Summary) > 500 {
		return errors.New("invalid check job contents")
	}
	if err := validateCheckJobTimelineAndLease(job); err != nil {
		return err
	}
	return nil
}

func validateCheckJobTimelineAndLease(job CheckJob) error {
	hasClaim := job.ClaimedAt != nil
	hasStart := job.StartedAt != nil
	hasFinish := job.FinishedAt != nil
	hasLease := job.LeaseID != "" || job.LeaseExpiresAt != nil
	hasCredential := job.CredentialID != ""
	hasAttempt := job.AttemptID != ""

	if (job.LeaseID == "") != (job.LeaseExpiresAt == nil) || (hasLease && !validAttemptID(job.LeaseID)) {
		return errors.New("check job lease evidence is incomplete")
	}
	if hasClaim != hasCredential || (hasLease && !hasClaim) || (hasStart && !hasClaim) || (hasAttempt && !hasStart) || (job.LeaseLostAt != nil && !hasLease) {
		return errors.New("check job claim and execution evidence is inconsistent")
	}
	if (!hasStart && job.Protection != ProtectionUnknown) || !validProtectionForExecutor(job.Executor, job.Protection) {
		return errors.New("check job protection does not match its execution")
	}
	if job.InterruptedAt != nil && job.Status != CheckJobInterrupted {
		return errors.New("non-interrupted check job has interruption evidence")
	}

	switch job.Status {
	case CheckJobPending:
		if hasClaim || hasStart || hasFinish || hasLease || job.LeaseLostAt != nil || hasAttempt || job.CancelRequestedAt != nil {
			return errors.New("pending check job already has lease or execution facts")
		}
	case CheckJobClaimed:
		if !hasClaim || hasStart || hasFinish || !hasLease || job.LeaseLostAt != nil || hasAttempt {
			return errors.New("claimed check job has inconsistent facts")
		}
	case CheckJobStarted:
		if !hasClaim || !hasStart || hasFinish || !hasLease || job.LeaseLostAt != nil {
			return errors.New("started check job has inconsistent facts")
		}
	case CheckJobAmbiguous:
		if !hasClaim || hasFinish || !hasLease || job.LeaseLostAt == nil {
			return errors.New("ambiguous check job is missing lease loss evidence")
		}
	case CheckJobInterrupted:
		if !hasFinish || hasLease || job.LeaseLostAt != nil || job.InterruptedAt == nil || !job.InterruptedAt.Equal(*job.FinishedAt) {
			return errors.New("interrupted check job has inconsistent facts")
		}
	case CheckJobCancelled:
		if !hasFinish || job.CancelRequestedAt == nil ||
			(hasClaim && (!hasLease || hasStart != hasAttempt || (!hasStart && job.LeaseLostAt != nil))) ||
			(!hasClaim && (hasStart || hasLease || hasAttempt)) {
			return errors.New("cancelled check job has inconsistent facts")
		}
	case CheckJobPassed, CheckJobFailed, CheckJobError, CheckJobIncomplete, CheckJobUnavailable:
		if !hasClaim || !hasStart || !hasFinish || !hasLease || !hasAttempt {
			return errors.New("terminal check job has inconsistent facts")
		}
	default:
		return errors.New("invalid check job status")
	}
	if hasClaim && job.ClaimedAt.Before(job.AdmittedAt) {
		return errors.New("check job claim precedes admission")
	}
	if hasStart && job.StartedAt.Before(*job.ClaimedAt) {
		return errors.New("check job start precedes its claim")
	}
	if (hasFinish && job.FinishedAt.Before(job.AdmittedAt)) || (hasStart && hasFinish && job.FinishedAt.Before(*job.StartedAt)) {
		return errors.New("check job finish precedes its execution")
	}
	if hasLease && !job.LeaseExpiresAt.After(*job.ClaimedAt) {
		return errors.New("check job lease does not follow its claim")
	}
	if job.LeaseLostAt != nil && (!job.LeaseLostAt.Equal(*job.LeaseExpiresAt) || job.LeaseLostAt.Before(*job.ClaimedAt)) {
		return errors.New("check job lease loss does not match its expiry")
	}
	if job.CancelRequestedAt != nil && job.CancelRequestedAt.Before(job.AdmittedAt) {
		return errors.New("check job cancellation precedes admission")
	}
	return nil
}
