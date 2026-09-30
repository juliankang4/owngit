package backups

import (
	"context"
	"path/filepath"
	"time"

	"owngit/internal/recovery"
	"owngit/internal/state"
)

// Schedule states.
const (
	ScheduleNotConfigured = "not_configured"
	ScheduleOff           = "off"
	ScheduleOn            = "on"
)

// Copy states of a finished run's backup.
const (
	CopyPresent = "present"
	CopyAbsent  = "absent"
)

// ScheduleView is the schedule as owners see it.
type ScheduleView struct {
	// State is not_configured before a backup folder is set, then on or
	// off.
	State           string     `json:"state"`
	Destination     string     `json:"destination,omitempty"`
	Interval        string     `json:"interval,omitempty"`
	IntervalSeconds int64      `json:"interval_seconds,omitempty"`
	Keep            int        `json:"keep,omitempty"`
	Verify          *bool      `json:"verify,omitempty"`
	UpdatedAt       *time.Time `json:"updated_at,omitempty"`
}

// RunView is one run as owners see it.
type RunView struct {
	ID          string `json:"id"`
	Kind        string `json:"kind"`
	Status      string `json:"status"`
	Destination string `json:"destination"`
	BackupName  string `json:"backup_name"`
	Path        string `json:"path"`
	// Verification is passed only when a rehearsed restore of this backup
	// passed.
	Verification string     `json:"verification"`
	Message      string     `json:"message,omitempty"`
	StartedAt    time.Time  `json:"started_at"`
	FinishedAt   *time.Time `json:"finished_at,omitempty"`
	// LongestHoldMS is how long Git writes to one repository waited for
	// the backup at most, and LongestHoldRepository names it; both are
	// absent until the backup read every repository.
	LongestHoldMS         *int64 `json:"longest_hold_ms,omitempty"`
	LongestHoldRepository string `json:"longest_hold_repository,omitempty"`
	// Copy says whether the backup is still in its folder; absent while
	// the run runs.
	Copy string `json:"copy,omitempty"`
}

// Status is the state of backups: the schedule, the running backup, the
// last one that ended, the newest verified backup still there, and when
// the next scheduled one is due (a time already past means as soon as the
// running backup ends).
type Status struct {
	Schedule     ScheduleView `json:"schedule"`
	Running      *RunView     `json:"running"`
	LastRun      *RunView     `json:"last_run"`
	LastVerified *RunView     `json:"last_verified"`
	NextRun      *time.Time   `json:"next_run"`
}

// ViewSchedule describes a schedule; configured is false when none was
// saved.
func ViewSchedule(schedule state.BackupSchedule, configured bool) ScheduleView {
	if !configured {
		return ScheduleView{State: ScheduleNotConfigured}
	}
	view := ScheduleView{
		State: ScheduleOff, Destination: schedule.Destination, Interval: IntervalName(schedule.Interval),
		IntervalSeconds: int64(schedule.Interval / time.Second), Keep: schedule.Keep, Verify: &schedule.Verify, UpdatedAt: &schedule.UpdatedAt,
	}
	if schedule.Enabled {
		view.State = ScheduleOn
	}
	return view
}

// ViewRun describes run; a finished run says whether its backup is still
// there.
func ViewRun(run state.BackupRun) *RunView {
	view := &RunView{
		ID: run.ID, Kind: run.Kind, Status: run.Status, Destination: run.Destination, BackupName: run.BackupName,
		Path: filepath.Join(run.Destination, run.BackupName), Verification: run.Verification, Message: run.Message, StartedAt: run.StartedAt,
	}
	if run.HoldKnown {
		milliseconds := run.LongestHold.Milliseconds()
		view.LongestHoldMS, view.LongestHoldRepository = &milliseconds, run.LongestHoldRepository
	}
	if run.Status != state.BackupRunning {
		finished := run.FinishedAt
		view.FinishedAt = &finished
		view.Copy = CopyAbsent
		if _, err := recovery.ReadBackupHeader(view.Path); err == nil && validName(run.BackupName) {
			view.Copy = CopyPresent
		}
	}
	return view
}

// Status reads the state of backups.
func (s *Service) Status(ctx context.Context) (Status, error) {
	schedule, configured, err := s.Store.BackupSchedule(ctx)
	if err != nil {
		return Status{}, err
	}
	runs, err := s.Store.BackupRuns(ctx)
	if err != nil {
		return Status{}, err
	}
	status := Status{Schedule: ViewSchedule(schedule, configured)}
	for _, run := range runs {
		switch {
		case run.Status == state.BackupRunning:
			status.Running = ViewRun(run)
		case status.LastRun == nil:
			status.LastRun = ViewRun(run)
		}
		if status.LastVerified == nil && run.Verification == state.BackupVerifyPassed {
			if view := ViewRun(run); view.Copy == CopyPresent {
				status.LastVerified = view
			}
		}
	}
	next, err := s.nextRun(ctx)
	if err != nil {
		return Status{}, err
	}
	if !next.IsZero() {
		status.NextRun = &next
	}
	return status, nil
}

// Runs lists every recorded run, the newest first.
func (s *Service) Runs(ctx context.Context) ([]RunView, error) {
	runs, err := s.Store.BackupRuns(ctx)
	if err != nil {
		return nil, err
	}
	views := make([]RunView, 0, len(runs))
	for _, run := range runs {
		views = append(views, *ViewRun(run))
	}
	return views, nil
}
