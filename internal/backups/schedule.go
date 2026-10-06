package backups

import (
	"context"
	"errors"
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
type ChangeError struct {
	// Message is the whole refusal in English, for the API and logs.
	Message string
	// Reason names a refusal that the dashboard words itself, and Detail is
	// the cause that goes with it; both are empty for any other refusal.
	Reason, Detail string
}

// The Reason of a ChangeError that the dashboard words itself.
const (
	ReasonChooseFolder   = "choose_folder"
	ReasonNotAbsolute    = "not_absolute"
	ReasonFolderUnusable = "folder_unusable"
	ReasonFolderOverlaps = "folder_overlaps"
)

func folderUnusable(err error) *ChangeError {
	if errors.Is(err, recovery.ErrBackupOverlapsStorage) {
		return &ChangeError{Message: "The backup folder cannot be used: " + err.Error() + ".", Reason: ReasonFolderOverlaps}
	}
	return &ChangeError{"The backup folder cannot be used: " + err.Error() + ".", ReasonFolderUnusable, err.Error()}
}

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
			return before, configured, after, &ChangeError{Message: "Choose a backup folder first.", Reason: ReasonChooseFolder}
		}
		after = state.BackupSchedule{Enabled: true, Interval: DefaultInterval, Keep: DefaultKeep, Verify: true}
	}
	if change.Destination != nil {
		destination := *change.Destination
		if !filepath.IsAbs(destination) {
			return before, configured, after, &ChangeError{Message: "The backup folder must be an absolute path.", Reason: ReasonNotAbsolute}
		}
		destination = filepath.Clean(destination)
		if err := recovery.CheckBackupFolder(s.Store, s.Repositories, destination); err != nil {
			return before, configured, after, folderUnusable(err)
		}
		area, err := state.OpenStagingArea(destination)
		if err != nil {
			return before, configured, after, folderUnusable(err)
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
		return before, configured, after, &ChangeError{Message: err.Error() + "."}
	}
	if err := s.Store.SaveBackupSchedule(ctx, after); err != nil {
		return before, configured, after, err
	}
	s.Wake()
	if change.Destination != nil {
		s.noteRestoreLimit(after.Destination)
	}
	return before, configured, after, nil
}
