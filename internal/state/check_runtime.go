package state

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

var ErrCheckJobRuntimeOwned = errors.New("the check job already has another active runtime")

var (
	// ErrCheckContainerJobActive refuses to forget the container record of a
	// job that is still pending, claimed or started, because a running server
	// may still clean up that container.
	ErrCheckContainerJobActive = errors.New("the check job has not finished; wait for it to finish or cancel it, or start OwnGit once so it records the interrupted job, then try again")
	// ErrCheckContainerRecordChanged means the record changed after it was read.
	ErrCheckContainerRecordChanged = errors.New("the container cleanup record changed; run the command again")
)

// CheckContainerOwnership is the machine-local immutable identity needed to
// reconcile one active Docker container after cancellation or restart.
type CheckContainerOwnership struct {
	JobID         string
	RepositoryID  string
	ContainerName string
	ContainerID   string
	DaemonID      string
	CreatedAt     time.Time
}

// PlanCheckContainer records cleanup ownership before container creation. The
// immutable ID is confirmed immediately after Docker returns it.
func (s *Store) PlanCheckContainer(ctx context.Context, authority CheckJobCompletionAuthority, containerName, daemonID string, now time.Time) error {
	if !validText(containerName, 200) || !validText(daemonID, 200) || now.IsZero() {
		return fmt.Errorf("%w: invalid container runtime identity", ErrInvalidCheckJob)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	job, exists, err := readCheckJobByIDTx(ctx, tx, authority.JobID)
	if err != nil {
		return err
	}
	if !exists {
		return ErrCheckJobNotFound
	}
	if job.Executor != CheckExecutorContainer || (job.Status != CheckJobClaimed && job.Status != CheckJobStarted) {
		return ErrCheckJobState
	}
	if job.LeaseID != authority.LeaseID || job.CredentialID != authority.CredentialID || job.CredentialGeneration != authority.CredentialGeneration || job.CredentialRole != RunnerRoleServer {
		return ErrCheckJobLease
	}
	policy, policyExists, err := readCheckPolicyTx(ctx, tx, job.RepositoryID)
	if err != nil {
		return err
	}
	credentialCurrent, err := checkJobCredentialCurrentTx(ctx, tx, job)
	if err != nil {
		return err
	}
	if !policyExists || !checkJobAuthorityCurrent(job, policy) || !credentialCurrent {
		return ErrCheckConsentRequired
	}
	var storedName, storedContainer, storedDaemon string
	err = tx.QueryRowContext(ctx, `SELECT container_name,container_id,daemon_id FROM check_job_runtime_ownership WHERE job_id=?`, job.ID).Scan(&storedName, &storedContainer, &storedDaemon)
	switch {
	case err == nil && storedName == containerName && storedContainer == "" && storedDaemon == daemonID:
		return tx.Commit()
	case err == nil:
		return ErrCheckJobRuntimeOwned
	case !errors.Is(err, sql.ErrNoRows):
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO check_job_runtime_ownership(job_id,repository_id,container_name,container_id,daemon_id,created_at) VALUES(?,?,?,?,?,?)`,
		job.ID, job.RepositoryID, containerName, "", daemonID, now.UTC().UnixNano()); err != nil {
		return err
	}
	return tx.Commit()
}

// ConfirmCheckContainer replaces the creation plan with Docker's immutable ID.
func (s *Store) ConfirmCheckContainer(ctx context.Context, jobID, containerName, containerID, daemonID string) error {
	if !validContainerRuntimeID(containerID) {
		return fmt.Errorf("%w: invalid container runtime identity", ErrInvalidCheckJob)
	}
	result, err := s.db.ExecContext(ctx, `UPDATE check_job_runtime_ownership SET container_id=? WHERE job_id=? AND container_name=? AND container_id='' AND daemon_id=?`,
		containerID, jobID, containerName, daemonID)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 1 {
		return nil
	}
	var storedID string
	if err := s.db.QueryRowContext(ctx, `SELECT container_id FROM check_job_runtime_ownership WHERE job_id=? AND container_name=? AND daemon_id=?`, jobID, containerName, daemonID).Scan(&storedID); err == nil && storedID == containerID {
		return nil
	}
	return ErrCheckJobRuntimeOwned
}

// ClearCheckContainer removes ownership only after cleanup of the exact
// original daemon and recorded container identity has been proved. An empty ID
// clears a creation plan whose container was proved absent by its unique name.
func (s *Store) ClearCheckContainer(ctx context.Context, jobID, containerID, daemonID string) error {
	result, err := s.db.ExecContext(ctx, `DELETE FROM check_job_runtime_ownership WHERE job_id=? AND container_id=? AND daemon_id=?`, jobID, containerID, daemonID)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected != 1 {
		return ErrCheckJobRuntimeOwned
	}
	return nil
}

// CheckContainerOwnershipForJob returns the cleanup record of one job.
func (s *Store) CheckContainerOwnershipForJob(ctx context.Context, jobID string) (CheckContainerOwnership, bool, error) {
	var item CheckContainerOwnership
	var createdAt int64
	err := s.db.QueryRowContext(ctx, `SELECT job_id,repository_id,container_name,container_id,daemon_id,created_at FROM check_job_runtime_ownership WHERE job_id=?`, jobID).
		Scan(&item.JobID, &item.RepositoryID, &item.ContainerName, &item.ContainerID, &item.DaemonID, &createdAt)
	if errors.Is(err, sql.ErrNoRows) {
		return CheckContainerOwnership{}, false, nil
	}
	if err != nil {
		return CheckContainerOwnership{}, false, err
	}
	item.CreatedAt = unixNanoTime(createdAt)
	return item, true, nil
}

// ForgetCheckContainer removes one cleanup record whose container the owner
// confirmed removed, without OwnGit proving it. Only the exact record that was
// read is removed, and only while its job is finished. The job state is checked
// in the same statement, so a job that a server claims meanwhile keeps its
// record.
func (s *Store) ForgetCheckContainer(ctx context.Context, record CheckContainerOwnership) error {
	result, err := s.db.ExecContext(ctx, `DELETE FROM check_job_runtime_ownership
		WHERE job_id=? AND container_name=? AND container_id=? AND daemon_id=?
		AND NOT EXISTS (SELECT 1 FROM check_jobs WHERE id=? AND status IN (?,?,?))`,
		record.JobID, record.ContainerName, record.ContainerID, record.DaemonID,
		record.JobID, CheckJobPending, CheckJobClaimed, CheckJobStarted)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 1 {
		return nil
	}
	// Nothing was removed. The job state only chooses the explanation.
	var status string
	if err := s.db.QueryRowContext(ctx, `SELECT status FROM check_jobs WHERE id=?`, record.JobID).Scan(&status); err == nil && !terminalCheckJob(status) {
		return ErrCheckContainerJobActive
	}
	return ErrCheckContainerRecordChanged
}

// ActiveCheckContainers returns a bounded machine-local cleanup set. Rows are
// retained after uncertain cleanup so a later startup can retry safely.
func (s *Store) ActiveCheckContainers(ctx context.Context, limit int) ([]CheckContainerOwnership, error) {
	if limit < 1 || limit > 1000 {
		return nil, fmt.Errorf("%w: invalid runtime cleanup bound", ErrInvalidCheckJob)
	}
	rows, err := s.db.QueryContext(ctx, `SELECT job_id,repository_id,container_name,container_id,daemon_id,created_at FROM check_job_runtime_ownership ORDER BY created_at,job_id LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ownership []CheckContainerOwnership
	for rows.Next() {
		var item CheckContainerOwnership
		var createdAt int64
		if err := rows.Scan(&item.JobID, &item.RepositoryID, &item.ContainerName, &item.ContainerID, &item.DaemonID, &createdAt); err != nil {
			return nil, err
		}
		item.CreatedAt = unixNanoTime(createdAt)
		ownership = append(ownership, item)
	}
	return ownership, rows.Err()
}

func validContainerRuntimeID(value string) bool {
	if len(value) < 12 || len(value) > 64 || value != strings.ToLower(value) {
		return false
	}
	for _, character := range value {
		if !strings.ContainsRune("0123456789abcdef", character) {
			return false
		}
	}
	return true
}
