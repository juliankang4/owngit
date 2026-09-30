// Package backups makes the backups that OwnGit takes while it serves: on a
// schedule and when an owner asks, each one recorded as a run, verified when
// the schedule says so, and followed by the removal of older backups that
// OwnGit made in the same folder.
package backups

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"owngit/internal/recovery"
	"owngit/internal/repository"
	"owngit/internal/state"
)

const (
	// DefaultVerifyLimit bounds the verification of one backup. A
	// verification that has not finished by then fails.
	DefaultVerifyLimit = 2 * time.Hour
	// recheck bounds how long the scheduler sleeps before it reads the
	// schedule again, so a changed clock delays a backup at most this long.
	recheck = time.Hour
	// runsKept is how many run records OwnGit keeps besides those of the
	// backups it still keeps.
	runsKept = 100
	// namePrefix starts the folder name of every backup a run writes.
	namePrefix = "owngit-backup-"
)

// Default schedule once a backup folder is set.
const (
	DefaultInterval = 24 * time.Hour
	DefaultKeep     = 7
)

// Intervals are the schedule's choices, shortest first.
var Intervals = []struct {
	Name     string
	Interval time.Duration
}{{"12h", 12 * time.Hour}, {"1d", 24 * time.Hour}, {"7d", 7 * 24 * time.Hour}}

// IntervalName is the choice name of interval, or "" when it is none.
func IntervalName(interval time.Duration) string {
	for _, choice := range Intervals {
		if choice.Interval == interval {
			return choice.Name
		}
	}
	return ""
}

// ErrNotConfigured refuses a backup before a backup folder is set.
var ErrNotConfigured = errors.New("no backup folder is set")

// interruptedMessage is the message of a run that OwnGit stopped, or that a
// process which ended left running.
const interruptedMessage = "OwnGit stopped before this backup finished, so it is not a finished backup."

// Service starts scheduled backups and backups asked for now. One runs at a
// time, which the store enforces across restarts too.
type Service struct {
	Store        *state.Store
	Repositories *repository.Manager
	Logf         func(format string, arguments ...any)
	// Now is the clock; nil is time.Now.
	Now func() time.Time
	// VerifyLimit bounds each verification; zero is DefaultVerifyLimit.
	VerifyLimit time.Duration

	mu   sync.Mutex
	ctx  context.Context
	stop context.CancelFunc
	// work counts the scheduler and the running backup.
	work sync.WaitGroup
	wake chan struct{}
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *Service) logf(format string, arguments ...any) {
	if s.Logf != nil {
		s.Logf(format, arguments...)
	}
}

// Start records every run that a previous process left running as
// interrupted, then starts the scheduler. Stop ends it.
func (s *Service) Start(ctx context.Context) error {
	if count, err := s.Store.InterruptBackupRuns(ctx, interruptedMessage, s.now()); err != nil {
		return fmt.Errorf("record unfinished backups as interrupted: %w", err)
	} else if count > 0 {
		s.logf("a backup that OwnGit was making when it stopped is recorded as interrupted")
	}
	s.open(ctx)
	s.work.Add(1)
	go s.schedule()
	return nil
}

// open lets backups start under ctx until Stop.
func (s *Service) open(ctx context.Context) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ctx, s.stop = context.WithCancel(ctx)
	s.wake = make(chan struct{}, 1)
}

// Stop cancels the scheduler and a running backup, which records itself
// as interrupted, and waits for both until ctx ends.
func (s *Service) Stop(ctx context.Context) error {
	s.mu.Lock()
	if s.stop != nil {
		s.stop()
	}
	s.mu.Unlock()
	done := make(chan struct{})
	go func() {
		s.work.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("a backup was still stopping: %w", ctx.Err())
	}
}

// Wake makes the scheduler read the schedule again, as after a change.
func (s *Service) Wake() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.wake != nil {
		select {
		case s.wake <- struct{}{}:
		default:
		}
	}
}

func (s *Service) schedule() {
	defer s.work.Done()
	for {
		timer := time.NewTimer(s.tick())
		select {
		case <-s.ctx.Done():
			timer.Stop()
			return
		case <-s.wake:
			timer.Stop()
		case <-timer.C:
		}
	}
}

// tick starts a scheduled backup when one is due and returns how long to
// wait before looking again. A backup that ends wakes the scheduler.
func (s *Service) tick() time.Duration {
	next, err := s.nextRun(s.ctx)
	if err != nil {
		s.logf("scheduled backups could not read their schedule: %v", err)
		return recheck
	}
	if next.IsZero() {
		return recheck
	}
	if wait := next.Sub(s.now()); wait > 0 {
		return min(wait, recheck)
	}
	if _, err := s.start(state.BackupRunScheduled); err != nil && !errors.Is(err, state.ErrBackupRunning) && s.ctx.Err() == nil {
		s.logf("a scheduled backup did not start: %v", err)
	}
	return recheck
}

// nextRun is when the next scheduled backup is due: one interval after the
// start of the last scheduled backup, or at once when there was none. It
// is zero when scheduled backups are off or not configured. A run that
// ended in any way used its time, so a restart never repeats it.
func (s *Service) nextRun(ctx context.Context) (time.Time, error) {
	schedule, configured, err := s.Store.BackupSchedule(ctx)
	if err != nil || !configured || !schedule.Enabled {
		return time.Time{}, err
	}
	runs, err := s.Store.BackupRuns(ctx)
	if err != nil {
		return time.Time{}, err
	}
	for _, run := range runs {
		if run.Kind == state.BackupRunScheduled {
			return run.StartedAt.Add(schedule.Interval), nil
		}
	}
	return schedule.UpdatedAt, nil
}

// StartNow starts a backup into the configured folder and returns its run
// at once. It fails with state.ErrBackupRunning while another one runs and
// with ErrNotConfigured before a folder is set.
func (s *Service) StartNow() (state.BackupRun, error) {
	return s.start(state.BackupRunManual)
}

func (s *Service) start(kind string) (state.BackupRun, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ctx == nil || s.ctx.Err() != nil {
		return state.BackupRun{}, errors.New("OwnGit is stopping, so it starts no backup")
	}
	schedule, configured, err := s.Store.BackupSchedule(s.ctx)
	if err != nil {
		return state.BackupRun{}, err
	}
	if !configured {
		return state.BackupRun{}, ErrNotConfigured
	}
	id := make([]byte, 16)
	if _, err := rand.Read(id); err != nil {
		return state.BackupRun{}, err
	}
	now := s.now()
	run := state.BackupRun{
		ID: hex.EncodeToString(id), Kind: kind, Status: state.BackupRunning, Destination: schedule.Destination,
		Verification: state.BackupVerifyNotRun, StartedAt: now,
	}
	run.BackupName = namePrefix + now.UTC().Format("20060102-150405") + "-" + run.ID[:8]
	if err := s.Store.StartBackupRun(s.ctx, run); err != nil {
		return state.BackupRun{}, err
	}
	s.work.Add(1)
	go func(ctx context.Context) {
		defer s.work.Done()
		s.execute(ctx, run, schedule)
		s.Wake()
	}(s.ctx)
	return run, nil
}

// execute makes the backup of run, verifies it when schedule asks, removes
// older backups after a good one, and records how it ended.
func (s *Service) execute(ctx context.Context, run state.BackupRun, schedule state.BackupSchedule) {
	output := filepath.Join(run.Destination, run.BackupName)
	err := s.checkRoom(ctx, run)
	var report recovery.CaptureReport
	if err == nil {
		report, err = recovery.CreateWhileServing(ctx, s.Store, s.Repositories, output)
	}
	if report.Captured {
		run.HoldKnown, run.LongestHold, run.LongestHoldRepository = true, report.LongestHold, report.LongestHoldRepository
	}
	if err == nil && schedule.Verify {
		if err = s.verify(ctx, output); err != nil {
			run.Verification = state.BackupVerifyFailed
			err = fmt.Errorf("the backup was written but did not pass verification: %w", err)
		} else {
			run.Verification = state.BackupVerifyPassed
		}
	}
	switch {
	case ctx.Err() != nil:
		run.Status, run.Message = state.BackupInterrupted, interruptedMessage
		if run.Verification == state.BackupVerifyFailed {
			run.Verification = state.BackupVerifyNotRun
		}
	case err != nil:
		run.Status, run.Message = state.BackupFailed, err.Error()
	default:
		run.Status = state.BackupSucceeded
		run.Message = s.removeOld(ctx, run, schedule.Keep)
	}
	run.FinishedAt = s.now()
	record, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	if err := s.Store.FinishBackupRun(record, run); err != nil {
		s.logf("backup %s ended %s but could not be recorded: %v", output, run.Status, err)
		return
	}
	line := fmt.Sprintf("backup %s %s", output, run.Status)
	if run.HoldKnown && run.LongestHoldRepository != "" {
		line += fmt.Sprintf("; Git writes waited at most %s (repository %s)", run.LongestHold.Round(time.Millisecond), run.LongestHoldRepository)
	}
	if run.Message != "" {
		line += ": " + run.Message
	}
	s.logf("%s", line)
}

// checkRoom refuses the backup of run when its folder has less free space
// than the newest backup that OwnGit keeps there took, for the repositories
// that still exist. Without such a backup there is nothing to estimate from,
// and a full disk ends the backup while it writes.
func (s *Service) checkRoom(ctx context.Context, run state.BackupRun) error {
	runs, err := s.Store.BackupRuns(ctx)
	if err != nil {
		return err
	}
	// run has written nothing yet, so the newest is an earlier backup.
	copies := ownedCopies(runs, run)
	if len(copies) == 0 {
		return nil
	}
	repositories, err := s.Store.Repositories(ctx)
	if err != nil {
		return err
	}
	exists := map[string]bool{}
	for _, item := range repositories {
		exists[item.ID] = true
	}
	return recovery.CheckBackupRoom(run.Destination, copies[0].path, func(id string) bool { return exists[id] })
}

// verify rehearses a restore of the backup at output within the limit.
func (s *Service) verify(ctx context.Context, output string) error {
	limit := s.VerifyLimit
	if limit == 0 {
		limit = DefaultVerifyLimit
	}
	bounded, cancel := context.WithTimeout(ctx, limit)
	defer cancel()
	result, err := recovery.Verify(bounded, output, "", s.Repositories.Git.GitPath)
	switch {
	case ctx.Err() != nil:
		return ctx.Err()
	case bounded.Err() != nil:
		return fmt.Errorf("the verification did not finish within %s", limit)
	case err != nil:
		return err
	case !result.Verified:
		return errors.New(result.Error)
	}
	return nil
}

// ownedCopy is a backup that one of OwnGit's runs wrote and that is still
// where it wrote it.
type ownedCopy struct {
	run  state.BackupRun
	path string
}

// ownedCopies lists, newest first, the backups in current's folder that
// OwnGit's runs wrote and that are still there, current included. A run
// owns the folder it names only while that folder holds a backup: a
// manifest of OwnGit's format. current is taken as it is in memory, since
// its record may not show how it ended yet.
func ownedCopies(runs []state.BackupRun, current state.BackupRun) []ownedCopy {
	var copies []ownedCopy
	for _, run := range runs {
		if run.ID == current.ID {
			run = current
		} else if run.Status == state.BackupRunning {
			continue
		}
		if run.Destination != current.Destination || !validName(run.BackupName) {
			continue
		}
		path := filepath.Join(run.Destination, run.BackupName)
		if _, err := recovery.ReadBackupHeader(path); err == nil {
			copies = append(copies, ownedCopy{run: run, path: path})
		}
	}
	return copies
}

// validName accepts a folder name that a run writes: a single name in its
// folder, never a path.
func validName(name string) bool {
	return strings.HasPrefix(name, namePrefix) && filepath.Base(name) == name && !strings.ContainsAny(name, `/\:`)
}

// removeOld removes the backups in run's folder beyond the newest keep that
// OwnGit's runs wrote, never the newest verified one, and forgets old run
// records. It says what it could not remove; nothing else is touched.
func (s *Service) removeOld(ctx context.Context, run state.BackupRun, keep int) string {
	runs, err := s.Store.BackupRuns(ctx)
	if err != nil {
		return "Older backups were not removed because the backup records could not be read: " + err.Error()
	}
	var problems []string
	kept, verifiedKept := 0, false
	keptRuns := map[string]bool{}
	for _, owned := range ownedCopies(runs, run) {
		verified := owned.run.Verification == state.BackupVerifyPassed
		if kept < keep || verified && !verifiedKept {
			kept++
			verifiedKept = verifiedKept || verified
			keptRuns[owned.run.ID] = true
			continue
		}
		if err := recovery.RemoveBackup(owned.path); err != nil {
			problems = append(problems, fmt.Sprintf("could not remove the older backup %s: %v", owned.path, err))
			keptRuns[owned.run.ID] = true
		}
	}
	var forget []string
	for index, record := range runs {
		if index >= runsKept && record.ID != run.ID && record.Status != state.BackupRunning && !keptRuns[record.ID] {
			forget = append(forget, record.ID)
		}
	}
	if err := s.Store.ForgetBackupRuns(ctx, forget); err != nil {
		problems = append(problems, "could not forget old backup records: "+err.Error())
	}
	if len(problems) == 0 {
		return ""
	}
	return "The backup is complete, but " + strings.Join(problems, "; ")
}
