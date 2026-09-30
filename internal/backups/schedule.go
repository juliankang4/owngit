package backups

import (
	"context"
	"path/filepath"
	"time"

	"owngit/internal/recovery"
	"owngit/internal/state"
)

// ScheduleChange names the parts of the schedule to change; nil leaves a
// part as it is.
type ScheduleChange struct {
	Destination *string
	Enabled     *bool
	Interval    *time.Duration
	Keep        *int
	Verify      *bool
}

// ChangeError is a schedule change that cannot be saved as asked.
type ChangeError struct{ Message string }

func (e *ChangeError) Error() string { return e.Message }

// ChangeSchedule saves change and returns the schedule before it
// (configured false when there was none) and after it. The first change
// must set a backup folder; the rest starts at the defaults: on, daily,
// seven backups kept and each one verified. A new folder must be an
// absolute path that no backup would overlap OwnGit's storage in; it is
// created, private to this account, when missing.
func (s *Service) ChangeSchedule(ctx context.Context, change ScheduleChange) (before state.BackupSchedule, configured bool, after state.BackupSchedule, err error) {
	before, configured, err = s.Store.BackupSchedule(ctx)
	if err != nil {
		return before, configured, after, err
	}
	after = before
	if !configured {
		if change.Destination == nil {
			return before, configured, after, &ChangeError{"Choose a backup folder first."}
		}
		after = state.BackupSchedule{Enabled: true, Interval: DefaultInterval, Keep: DefaultKeep, Verify: true}
	}
	if change.Destination != nil {
		destination := *change.Destination
		if !filepath.IsAbs(destination) {
			return before, configured, after, &ChangeError{"The backup folder must be an absolute path."}
		}
		destination = filepath.Clean(destination)
		if err := recovery.CheckBackupFolder(s.Store, s.Repositories, destination); err != nil {
			return before, configured, after, &ChangeError{"The backup folder cannot be used: " + err.Error() + "."}
		}
		area, err := state.OpenStagingArea(destination)
		if err != nil {
			return before, configured, after, &ChangeError{"The backup folder cannot be used: " + err.Error() + "."}
		}
		area.Close()
		after.Destination = destination
	}
	if change.Enabled != nil {
		after.Enabled = *change.Enabled
	}
	if change.Interval != nil {
		after.Interval = *change.Interval
	}
	if change.Keep != nil {
		after.Keep = *change.Keep
	}
	if change.Verify != nil {
		after.Verify = *change.Verify
	}
	after.UpdatedAt = s.now()
	if err := state.ValidateBackupSchedule(after); err != nil {
		return before, configured, after, &ChangeError{err.Error() + "."}
	}
	if err := s.Store.SaveBackupSchedule(ctx, after); err != nil {
		return before, configured, after, err
	}
	s.Wake()
	return before, configured, after, nil
}
