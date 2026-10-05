package state

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"time"
	"unicode/utf8"
)

// BackupSchedule is when and where OwnGit makes backups while it serves.
// It is machine-local: a backup does not carry it.
type BackupSchedule struct {
	// Enabled is false when scheduled backups are off; the destination
	// still serves backups started by hand.
	Enabled     bool
	Interval    time.Duration
	Destination string
	// Keep is how many of its own backups OwnGit keeps in Destination.
	Keep int
	// Verify rehearses a restore of every new backup.
	Verify    bool
	UpdatedAt time.Time
}

// Bounds of a backup schedule, as the schema holds them.
const (
	MinBackupInterval = time.Hour
	MaxBackupInterval = 30 * 24 * time.Hour
	MaxBackupKeep     = 1000
	// MaxBackupRunMessage is the most bytes a run's message holds.
	MaxBackupRunMessage = 500
	// MaxBackupRunRecords is how many finished run records OwnGit keeps
	// besides the records of the backups it still keeps: the newest ones,
	// whether their runs succeeded or failed, and the newest scheduled run,
	// whose start decides when the next scheduled backup is due. Every
	// recorded outcome applies the limit, so backups that keep failing do
	// not grow the history without bound.
	MaxBackupRunRecords = 100
)

// Kinds, states and verification results of a backup run.
const (
	BackupRunScheduled = "scheduled"
	BackupRunManual    = "manual"

	BackupRunning     = "running"
	BackupSucceeded   = "succeeded"
	BackupFailed      = "failed"
	BackupInterrupted = "interrupted"

	BackupVerifyNotRun = "not_run"
	BackupVerifyPassed = "passed"
	BackupVerifyFailed = "failed"
)

// ErrBackupRunning refuses a backup run while another one runs.
var ErrBackupRunning = errors.New("a backup is already running")

// ErrBackupRunEnded reports a run that is not running, so its outcome can
// no longer be recorded.
var ErrBackupRunEnded = errors.New("the backup run is not running")

// BackupRun is one backup that OwnGit started while serving.
type BackupRun struct {
	ID          string
	Kind        string
	Status      string
	Destination string
	// BackupName is the folder in Destination that the run writes.
	BackupName   string
	Verification string
	Message      string
	StartedAt    time.Time
	// FinishedAt is zero while the run runs.
	FinishedAt time.Time
	// HoldKnown says that the capture finished, so LongestHold and
	// LongestHoldRepository (empty without repositories) say how long
	// Git writes waited for it.
	HoldKnown             bool
	LongestHold           time.Duration
	LongestHoldRepository string
	// ManifestSHA256 is the SHA-256 of the manifest the run wrote, empty
	// when it wrote none: only the folder holding that manifest is the
	// run's backup.
	ManifestSHA256 string
}

// ValidateBackupSchedule checks a schedule against the bounds the schema
// keeps.
func ValidateBackupSchedule(schedule BackupSchedule) error {
	switch {
	case schedule.Interval < MinBackupInterval || schedule.Interval > MaxBackupInterval || schedule.Interval%time.Second != 0:
		return fmt.Errorf("the backup interval must be between %s and %s", MinBackupInterval, MaxBackupInterval)
	case schedule.Keep < 1 || schedule.Keep > MaxBackupKeep:
		return fmt.Errorf("the number of backups to keep must be between 1 and %d", MaxBackupKeep)
	case schedule.Destination == "" || len(schedule.Destination) > 4096 || !filepath.IsAbs(schedule.Destination):
		return errors.New("the backup folder must be an absolute path")
	case !utf8.ValidString(schedule.Destination):
		return errors.New("the backup folder must be valid UTF-8")
	}
	return nil
}

// BackupSchedule returns the saved schedule, and false when none was ever
// saved.
func (s *Store) BackupSchedule(ctx context.Context) (BackupSchedule, bool, error) {
	var schedule BackupSchedule
	var enabled, verify int
	var interval, updated int64
	err := s.db.QueryRowContext(ctx, `SELECT enabled,interval_seconds,destination,keep,verify,updated_at FROM backup_schedule WHERE singleton=1`).
		Scan(&enabled, &interval, &schedule.Destination, &schedule.Keep, &verify, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return BackupSchedule{}, false, nil
	}
	if err != nil {
		return BackupSchedule{}, false, err
	}
	schedule.Enabled, schedule.Verify = enabled == 1, verify == 1
	schedule.Interval = time.Duration(interval) * time.Second
	schedule.UpdatedAt = time.Unix(updated, 0)
	return schedule, true, nil
}

// SaveBackupSchedule replaces the schedule.
func (s *Store) SaveBackupSchedule(ctx context.Context, schedule BackupSchedule) error {
	if err := ValidateBackupSchedule(schedule); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO backup_schedule(singleton,enabled,interval_seconds,destination,keep,verify,updated_at) VALUES(1,?,?,?,?,?,?)
		ON CONFLICT(singleton) DO UPDATE SET enabled=excluded.enabled,interval_seconds=excluded.interval_seconds,destination=excluded.destination,
		keep=excluded.keep,verify=excluded.verify,updated_at=excluded.updated_at`,
		boolInt(schedule.Enabled), int64(schedule.Interval/time.Second), schedule.Destination, schedule.Keep, boolInt(schedule.Verify), schedule.UpdatedAt.Unix())
	return err
}

// StartBackupRun records run as running, or returns ErrBackupRunning when
// another run is running.
func (s *Store) StartBackupRun(ctx context.Context, run BackupRun) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var running int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM backup_runs WHERE status='running'`).Scan(&running); err != nil {
		return err
	}
	if running != 0 {
		return ErrBackupRunning
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO backup_runs(id,kind,status,destination,backup_name,started_at) VALUES(?,?,'running',?,?,?)`,
		run.ID, run.Kind, run.Destination, run.BackupName, run.StartedAt.Unix()); err != nil {
		return err
	}
	return tx.Commit()
}

// FinishBackupRun records how a running run ended: its status,
// verification, message (cut to MaxBackupRunMessage bytes), finish time,
// hold, and its backup name, empty when the run published no backup. It
// also removes the finished records beyond the newest
// MaxBackupRunRecords, so the history stays bounded however the runs
// ended.
func (s *Store) FinishBackupRun(ctx context.Context, run BackupRun) error {
	var holdMS, holdRepository, manifest any
	if run.ManifestSHA256 != "" {
		manifest = run.ManifestSHA256
	}
	if run.HoldKnown {
		holdMS = run.LongestHold.Milliseconds()
		if run.LongestHoldRepository != "" {
			holdRepository = run.LongestHoldRepository
		}
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `UPDATE backup_runs SET status=?,verification=?,message=?,finished_at=?,longest_hold_ms=?,longest_hold_repository=?,backup_name=?,manifest_sha256=?
		WHERE id=? AND status='running'`,
		run.Status, run.Verification, cutText(run.Message, MaxBackupRunMessage), run.FinishedAt.Unix(), holdMS, holdRepository, run.BackupName, manifest, run.ID)
	if err != nil {
		return err
	}
	if changed, err := result.RowsAffected(); err != nil {
		return err
	} else if changed != 1 {
		return fmt.Errorf("%w: %s", ErrBackupRunEnded, run.ID)
	}
	if err := forgetOldBackupRuns(ctx, tx); err != nil {
		return err
	}
	return tx.Commit()
}

// InterruptBackupRuns marks every running run interrupted, with message,
// and applies the record limit as every recorded outcome does. Only a
// starting server calls it, before it starts a run: a run recorded as
// running then belongs to a process that ended.
func (s *Store) InterruptBackupRuns(ctx context.Context, message string, now time.Time) (int64, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `UPDATE backup_runs SET status='interrupted',message=?,finished_at=? WHERE status='running'`,
		cutText(message, MaxBackupRunMessage), now.Unix())
	if err != nil {
		return 0, err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return 0, err
	}
	if err := forgetOldBackupRuns(ctx, tx); err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return count, nil
}

// forgetOldBackupRuns removes the finished records beyond the newest
// MaxBackupRunRecords that hold no backup, except the newest scheduled
// run, whose start decides when the next scheduled backup is due. The
// record of a backup OwnGit still keeps stays.
func forgetOldBackupRuns(ctx context.Context, tx *sql.Tx) error {
	rows, err := tx.QueryContext(ctx, `SELECT id,kind,status,backup_name FROM backup_runs ORDER BY rowid DESC`)
	if err != nil {
		return err
	}
	var forget []string
	scheduledSeen, index := false, -1
	for rows.Next() {
		index++
		var id, kind, status, backupName string
		if err := rows.Scan(&id, &kind, &status, &backupName); err != nil {
			rows.Close()
			return err
		}
		newestScheduled := kind == BackupRunScheduled && !scheduledSeen
		if kind == BackupRunScheduled {
			scheduledSeen = true
		}
		if index >= MaxBackupRunRecords && status != BackupRunning && backupName == "" && !newestScheduled {
			forget = append(forget, id)
		}
	}
	if err := closeRows(rows); err != nil {
		return err
	}
	for _, id := range forget {
		if _, err := tx.ExecContext(ctx, `DELETE FROM backup_runs WHERE id=? AND status<>'running'`, id); err != nil {
			return err
		}
	}
	return nil
}

// BackupRuns returns every recorded run, the newest first: in the order
// they were started, which one at a time keeps, whatever the clock said.
func (s *Store) BackupRuns(ctx context.Context) ([]BackupRun, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,kind,status,destination,backup_name,verification,message,started_at,finished_at,longest_hold_ms,longest_hold_repository,manifest_sha256
		FROM backup_runs ORDER BY rowid DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	runs := []BackupRun{}
	for rows.Next() {
		var run BackupRun
		var started int64
		var finished, holdMS sql.NullInt64
		var holdRepository, manifest sql.NullString
		if err := rows.Scan(&run.ID, &run.Kind, &run.Status, &run.Destination, &run.BackupName, &run.Verification, &run.Message,
			&started, &finished, &holdMS, &holdRepository, &manifest); err != nil {
			return nil, err
		}
		run.StartedAt, run.ManifestSHA256 = time.Unix(started, 0), manifest.String
		if finished.Valid {
			run.FinishedAt = time.Unix(finished.Int64, 0)
		}
		if holdMS.Valid {
			run.HoldKnown, run.LongestHold, run.LongestHoldRepository = true, time.Duration(holdMS.Int64)*time.Millisecond, holdRepository.String
		}
		runs = append(runs, run)
	}
	return runs, rows.Err()
}

// ForgetBackup records that the backup of the finished run id is gone:
// OwnGit removed it, or its folder no longer holds it.
func (s *Store) ForgetBackup(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE backup_runs SET backup_name='' WHERE id=? AND status<>'running'`, id)
	return err
}

// CutBackupRunMessage cuts text to what a run's stored message holds at
// most, or to limit when that is smaller, without splitting a character: a
// message OwnGit shows without a record obeys the same bound as one it
// recorded. A limit below zero means no room.
func CutBackupRunMessage(text string, limit int) string {
	return cutText(text, max(0, min(limit, MaxBackupRunMessage)))
}

// cutText cuts text to at most limit bytes without splitting a character.
func cutText(text string, limit int) string {
	if len(text) <= limit {
		return text
	}
	cut := limit
	for cut > 0 && !utf8.RuneStart(text[cut]) {
		cut--
	}
	return text[:cut]
}

// SetBackupRunMessage replaces the message of the finished run id, cut to
// MaxBackupRunMessage bytes, so a pass that reads the backup folder after
// the run was recorded can add what it found there. A run whose record is
// still running keeps no message that would say it ended.
func (s *Store) SetBackupRunMessage(ctx context.Context, id, message string) error {
	result, err := s.db.ExecContext(ctx, `UPDATE backup_runs SET message=? WHERE id=? AND status<>'running'`,
		cutText(message, MaxBackupRunMessage), id)
	if err != nil {
		return err
	}
	if changed, err := result.RowsAffected(); err != nil {
		return err
	} else if changed != 1 {
		return fmt.Errorf("backup run %s has no finished record", id)
	}
	return nil
}

// RecordBackupVerification records the result of verifying the backup of
// the finished run id again: passed or failed.
func (s *Store) RecordBackupVerification(ctx context.Context, id, verification string) error {
	if verification != BackupVerifyPassed && verification != BackupVerifyFailed {
		return fmt.Errorf("invalid verification result %q", verification)
	}
	result, err := s.db.ExecContext(ctx, `UPDATE backup_runs SET verification=? WHERE id=? AND status<>'running' AND backup_name<>''`, verification, id)
	if err != nil {
		return err
	}
	if changed, err := result.RowsAffected(); err != nil {
		return err
	} else if changed != 1 {
		return fmt.Errorf("backup run %s has no finished backup", id)
	}
	return nil
}
