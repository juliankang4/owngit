package backups

import (
	"context"
	"fmt"
	"path/filepath"
	"time"

	"owngit/internal/state"
)

// Schedule states.
const (
	ScheduleNotConfigured = "not_configured"
	ScheduleOff           = "off"
	ScheduleOn            = "on"
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
	// BackupName is the folder in Destination that holds the run's
	// backup, empty when it wrote none or OwnGit removed it or found it
	// gone. Path is the whole path, or empty.
	BackupName string `json:"backup_name"`
	Path       string `json:"path"`
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
}

// Status is the state of backups as OwnGit's records hold it: the
// schedule, the running backup, the last one that ended, the newest
// verified backup that OwnGit keeps, and when the next scheduled one is
// due (a time already past means as soon as the running backup ends). It
// reads nothing in the backup folder, so a slow or unavailable folder
// never holds it up; each backup and its removal of older ones record what
// they found there. Check, Upload and RestoreLimit are what this OwnGit
// found since it started: the last verification an owner asked for, the
// uploaded backup, and whether a restore could write into a folder on the
// disk of the backup folder. A finished backup whose record the state
// store has not saved yet stands in as the running one, and the last runs
// stay the ones OwnGit recorded.
type Status struct {
	Schedule     ScheduleView  `json:"schedule"`
	Running      *RunView      `json:"running"`
	LastRun      *RunView      `json:"last_run"`
	LastVerified *RunView      `json:"last_verified"`
	NextRun      *time.Time    `json:"next_run"`
	Check        *Check        `json:"check"`
	Upload       *Upload       `json:"upload"`
	RestoreLimit *RestoreLimit `json:"restore_limit,omitempty"`
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

// ViewRun describes run.
func ViewRun(run state.BackupRun) *RunView {
	view := &RunView{
		ID: run.ID, Kind: run.Kind, Status: run.Status, Destination: run.Destination, BackupName: run.BackupName,
		Verification: run.Verification, Message: run.Message, StartedAt: run.StartedAt,
	}
	if run.BackupName != "" {
		view.Path = filepath.Join(run.Destination, run.BackupName)
	}
	if run.HoldKnown {
		milliseconds := run.LongestHold.Milliseconds()
		view.LongestHoldMS, view.LongestHoldRepository = &milliseconds, run.LongestHoldRepository
	}
	if run.Status != state.BackupRunning {
		finished := run.FinishedAt
		view.FinishedAt = &finished
	}
	return view
}

// pendingResult is how a run whose final record the state store refused
// appears: as the backup that still occupies the folder, since no other
// may start while its record stands as running, saying how it ended and
// that its record waits for the state store. It is nil for every other
// run.
func (s *Service) pendingResult(id string) *state.BackupRun {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.pending == nil || s.pending.ID != id {
		return nil
	}
	result := *s.pending
	result.Message = pendingMessage(result.Status, result.Message)
	result.Status = state.BackupRunning
	return &result
}

// forgetPending forgets the result held for the run id, if any.
func (s *Service) forgetPending(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.pending != nil && s.pending.ID == id {
		s.pending = nil
	}
}

// pendingMessage is how a finished backup whose record waits for the
// state store is described: how it ended, and that its record is not saved
// yet. It keeps within the bytes a stored record holds, and keeps the
// notice complete, so the message in front of it is cut first.
func pendingMessage(status, message string) string {
	notice := fmt.Sprintf("The backup finished: %s. Its record could not be saved yet; OwnGit keeps trying and saves it as soon as the state is writable again.", status)
	if message == "" {
		return notice
	}
	return state.CutBackupRunMessage(message, state.MaxBackupRunMessage-len(notice)-1) + " " + notice
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
		if result := s.pendingResult(run.ID); result != nil {
			// The finished run whose record waits stands in as the running
			// one; the last runs stay the ones OwnGit recorded.
			if status.Running == nil {
				status.Running = ViewRun(*result)
			}
			continue
		}
		switch {
		case run.Status == state.BackupRunning:
			status.Running = ViewRun(run)
		case status.LastRun == nil:
			status.LastRun = ViewRun(run)
		}
		if status.LastVerified == nil && run.Verification == state.BackupVerifyPassed && run.BackupName != "" {
			status.LastVerified = ViewRun(run)
		}
	}
	next, err := s.nextRun(ctx)
	if err != nil {
		return Status{}, err
	}
	if !next.IsZero() {
		status.NextRun = &next
	}
	s.mu.Lock()
	if s.check != nil {
		check := *s.check
		status.Check = &check
	}
	if s.upload != nil {
		upload := *s.upload
		status.Upload = &upload
	}
	s.mu.Unlock()
	if configured {
		status.RestoreLimit = s.restoreLimit(schedule.Destination)
	}
	return status, nil
}

// Summary says whether backups work without naming a folder, a repository
// or an error: what general access may read, for coding agents. Every field
// is a fixed word or a time.
type Summary struct {
	// Schedule is not_configured, off or on.
	Schedule string      `json:"schedule"`
	LastRun  *SummaryRun `json:"last_run"`
	// LastVerifiedAt is when the newest verified backup that is still in
	// its folder finished.
	LastVerifiedAt *time.Time `json:"last_verified_at"`
	NextRun        *time.Time `json:"next_run"`
}

// SummaryRun is the last backup that ended, without its folder or message.
type SummaryRun struct {
	Kind         string    `json:"kind"`
	Status       string    `json:"status"`
	Verification string    `json:"verification"`
	FinishedAt   time.Time `json:"finished_at"`
}

// Summarize reduces status to its Summary.
func Summarize(status Status) Summary {
	summary := Summary{Schedule: status.Schedule.State, NextRun: status.NextRun}
	if run := status.LastRun; run != nil {
		summary.LastRun = &SummaryRun{Kind: run.Kind, Status: run.Status, Verification: run.Verification, FinishedAt: *run.FinishedAt}
	}
	if status.LastVerified != nil {
		summary.LastVerifiedAt = status.LastVerified.FinishedAt
	}
	return summary
}

// Runs lists every recorded run, the newest first. A finished run whose
// record waits appears as the running one, as in Status.
func (s *Service) Runs(ctx context.Context) ([]RunView, error) {
	runs, err := s.Store.BackupRuns(ctx)
	if err != nil {
		return nil, err
	}
	views := make([]RunView, 0, len(runs))
	for _, run := range runs {
		if result := s.pendingResult(run.ID); result != nil {
			run = *result
		}
		views = append(views, *ViewRun(run))
	}
	return views, nil
}
