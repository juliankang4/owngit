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
	"io/fs"
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
	// task is the verification or upload that runs, or "" (check.go).
	task  string
	check *Check
	// upload is the uploaded backup (upload.go), and uploadTimer removes
	// it when its time is up.
	upload      *Upload
	uploadTimer *time.Timer
	// inUse counts the downloads of each run's backup; -1 marks one that
	// is being removed.
	inUse map[string]int
	// limit is what the last test of the backup folder found.
	limit *RestoreLimit
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
	if err := s.removeUploads(); err != nil {
		return fmt.Errorf("remove the uploaded backup left from before: %w", err)
	}
	s.open(ctx)
	s.work.Add(1)
	go s.schedule()
	if schedule, configured, err := s.Store.BackupSchedule(ctx); err == nil && configured {
		go s.noteRestoreLimit(schedule.Destination)
	}
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
	if s.uploadTimer != nil {
		s.uploadTimer.Stop()
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
	if _, err := s.start(state.BackupRunScheduled); err != nil && !errors.Is(err, state.ErrBackupRunning) && !errors.Is(err, ErrBusy) && s.ctx.Err() == nil {
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
// at once. It fails with state.ErrBackupRunning while another one runs,
// with ErrBusy while OwnGit verifies or receives a backup, and with
// ErrNotConfigured before a folder is set.
func (s *Service) StartNow() (state.BackupRun, error) {
	return s.start(state.BackupRunManual)
}

func (s *Service) start(kind string) (state.BackupRun, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ctx == nil || s.ctx.Err() != nil {
		return state.BackupRun{}, errors.New("OwnGit is stopping, so it starts no backup")
	}
	if s.task != "" {
		return state.BackupRun{}, ErrBusy
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
	note, err := s.checkRoom(ctx, run)
	var report recovery.CaptureReport
	if err == nil {
		report, err = recovery.CreateWhileServing(ctx, s.Store, s.Repositories, output)
	}
	if report.Captured {
		run.HoldKnown, run.LongestHold, run.LongestHoldRepository = true, report.LongestHold, report.LongestHoldRepository
	}
	run.ManifestSHA256 = report.ManifestSHA256
	if err == nil && schedule.Verify {
		if err = s.verify(ctx, output); err != nil {
			run.Verification = state.BackupVerifyFailed
			err = fmt.Errorf("the backup was written but did not pass verification: %w", err)
		} else {
			run.Verification = state.BackupVerifyPassed
		}
	}
	var problems []string
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
		problems = s.removeOld(ctx, run, schedule.Keep)
	}
	// A backup that failed after it was published, or was stopped then,
	// is still there, and is kept as OwnGit's; one never published is not.
	if err != nil || ctx.Err() != nil {
		if published, problem := s.published(run); !published {
			run.BackupName, run.ManifestSHA256 = "", ""
		} else if problem != "" {
			problems = append(problems, problem)
		}
	}
	if note != "" {
		problems = append([]string{note}, problems...)
	}
	if len(problems) > 0 {
		if run.Message == "" {
			run.Message = "The backup is complete, but " + strings.Join(problems, "; ")
		} else {
			run.Message += " Also, " + strings.Join(problems, "; ")
		}
	}
	if run.BackupName != "" {
		if run.Status == state.BackupSucceeded {
			s.noteAliases(&run, report)
		}
		s.noteRestoreLimit(run.Destination)
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

// noteAliases keeps complete guidance within the run record's remaining space.
// Log the full notice before recording, even if recording later fails.
func (s *Service) noteAliases(run *state.BackupRun, report recovery.CaptureReport) {
	if len(report.AliasBranches) == 0 {
		return
	}
	budget := state.MaxBackupRunMessage - len(run.Message)
	if run.Message != "" {
		budget-- // Keep existing warnings first, separated by a newline.
	}
	var summary string
	if budget > 0 {
		summary = report.AliasNoticeWithin(budget)
	}
	output := filepath.Join(run.Destination, run.BackupName)
	if summary == "" {
		s.logf("backup %s alias notice did not fit in the backup run record; the full list follows", output)
	} else {
		if run.Message != "" {
			run.Message += "\n"
		}
		run.Message += summary
	}
	for _, entry := range strings.Split(report.AliasNotice(), "\n") {
		s.logf("backup %s alias notice: %s", output, entry)
	}
}

// published says whether the folder of run holds the backup it wrote, and
// what kept it from telling.
func (s *Service) published(run state.BackupRun) (bool, string) {
	folder, err := recovery.OpenBackupFolder(run.Destination)
	if err != nil {
		return true, fmt.Sprintf("could not check whether %s was written: %v", run.BackupName, err)
	}
	defer folder.Close()
	backup, err := openOwned(folder, run)
	switch {
	case errors.Is(err, errMissing), errors.Is(err, errNotOwned):
		return false, ""
	case err != nil:
		return true, fmt.Sprintf("could not check whether %s was written: %v", run.BackupName, err)
	}
	backup.Close()
	return true, ""
}

// checkRoom refuses the backup of run when its folder has less free space
// than the newest backup that OwnGit keeps there took, for the repositories
// that still exist. Without such a backup there is nothing to estimate from,
// and a full disk ends the backup while it writes. When that backup cannot
// be read, the backup goes ahead and note says why the room was not
// checked.
func (s *Service) checkRoom(ctx context.Context, run state.BackupRun) (note string, err error) {
	runs, err := s.Store.BackupRuns(ctx)
	if err != nil {
		return "", err
	}
	folder, err := recovery.OpenBackupFolder(run.Destination)
	if err != nil {
		return "", err
	}
	defer folder.Close()
	for _, record := range runs {
		if record.ID == run.ID || record.Status == state.BackupRunning || record.Destination != run.Destination || record.BackupName == "" {
			continue
		}
		backup, err := openOwned(folder, record)
		if errors.Is(err, errMissing) || errors.Is(err, errNotOwned) {
			continue
		}
		if err != nil {
			return fmt.Sprintf("the free space was not checked, because the last backup %s could not be read: %v", record.BackupName, err), nil
		}
		defer backup.Close()
		repositories, err := s.Store.Repositories(ctx)
		if err != nil {
			return "", err
		}
		exists := map[string]bool{}
		for _, item := range repositories {
			exists[item.ID] = true
		}
		needed, err := backup.Size(func(id string) bool { return exists[id] })
		if err != nil {
			return fmt.Sprintf("the free space was not checked, because the last backup %s could not be read: %v", record.BackupName, err), nil
		}
		return "", folder.CheckRoom(needed)
	}
	return "", nil
}

// verify rehearses a restore of the backup at output within the limit.
func (s *Service) verify(ctx context.Context, output string) error {
	return s.verifyWith(ctx, func(ctx context.Context, gitPath string) (recovery.Verification, error) {
		return recovery.Verify(ctx, output, "", gitPath)
	})
}

// verifyWith runs the verification rehearse within the limit.
func (s *Service) verifyWith(ctx context.Context, rehearse func(ctx context.Context, gitPath string) (recovery.Verification, error)) error {
	limit := s.VerifyLimit
	if limit == 0 {
		limit = DefaultVerifyLimit
	}
	bounded, cancel := context.WithTimeout(ctx, limit)
	defer cancel()
	result, err := rehearse(bounded, s.Repositories.Git.GitPath)
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

// A run's folder that holds no backup of that run: errMissing when the
// folder is gone, errNotOwned when it holds another backup or something
// that is no backup.
var (
	errMissing  = errors.New("the backup is missing")
	errNotOwned = errors.New("the folder does not hold the backup that OwnGit wrote there")
)

// openOwned opens the backup that run wrote, held for its removal. The run
// owns its folder only while the folder, not a link, holds the very
// manifest the run wrote, as its SHA-256 recorded with the run shows; no
// other backup, even one made in the same second, has it. A folder that is
// missing gives errMissing, one that holds anything else errNotOwned
// (wrapped, saying why); a folder that cannot be read gives that error.
func openOwned(folder *recovery.BackupFolder, run state.BackupRun) (*recovery.BackupCopy, error) {
	if !validName(run.BackupName) {
		return nil, fmt.Errorf("%w: %q is not a backup name", errNotOwned, run.BackupName)
	}
	if run.ManifestSHA256 == "" {
		return nil, fmt.Errorf("%s: %w: the run recorded no manifest, so OwnGit cannot tell its backup", run.BackupName, errNotOwned)
	}
	backup, err := folder.Open(run.BackupName)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return nil, errMissing
	case errors.Is(err, recovery.ErrNotABackup):
		return nil, fmt.Errorf("%s: %w: %v", run.BackupName, errNotOwned, err)
	case err != nil:
		return nil, err
	}
	if backup.ManifestSHA256 != run.ManifestSHA256 {
		backup.Close()
		return nil, fmt.Errorf("%s: %w: it holds another backup", run.BackupName, errNotOwned)
	}
	return backup, nil
}

// forget records that the backup of run is gone.
func (s *Service) forget(ctx context.Context, run state.BackupRun) {
	if err := s.Store.ForgetBackup(ctx, run.ID); err != nil {
		s.logf("could not record that backup %s is gone: %v", run.BackupName, err)
	}
}

// validName accepts a folder name that a run writes: a single name in its
// folder, never a path.
func validName(name string) bool {
	return strings.HasPrefix(name, namePrefix) && filepath.Base(name) == name && !strings.ContainsAny(name, `/\:`)
}

// removeOld removes the backups in run's folder beyond the newest keep that
// OwnGit's runs wrote, in the order the runs started, never run's own and
// never the newest verified one, and forgets old run records. A backup is
// found by its run record and its manifest and removed through the folder
// that was opened, never through a link, and not when it holds anything
// OwnGit did not write. It returns what it could not do; nothing else is
// touched.
func (s *Service) removeOld(ctx context.Context, run state.BackupRun, keep int) []string {
	runs, err := s.Store.BackupRuns(ctx)
	if err != nil {
		return []string{"older backups were not removed because the backup records could not be read: " + err.Error()}
	}
	folder, err := recovery.OpenBackupFolder(run.Destination)
	if err != nil {
		return []string{"older backups were not removed because the folder could not be opened: " + err.Error()}
	}
	defer folder.Close()
	var problems []string
	kept, verifiedKept := 0, false
	for _, record := range runs {
		if record.ID == run.ID {
			record = run
		} else if record.Status == state.BackupRunning || record.Destination != run.Destination || record.BackupName == "" {
			continue
		}
		backup, err := openOwned(folder, record)
		switch {
		case record.ID == run.ID && err != nil:
			problems = append(problems, fmt.Sprintf("the new backup %s could not be opened again, so no older backup was removed: %v", record.BackupName, err))
			return problems
		case errors.Is(err, errMissing):
			s.forget(ctx, record)
			continue
		case errors.Is(err, errNotOwned):
			s.forget(ctx, record)
			problems = append(problems, fmt.Sprintf("%v; it was left in place", err))
			continue
		}
		if err != nil {
			problems = append(problems, fmt.Sprintf("could not read the older backup %s, so it was left in place: %v", record.BackupName, err))
			continue
		}
		verified := record.Verification == state.BackupVerifyPassed
		if record.ID == run.ID || kept < keep || verified && !verifiedKept {
			kept++
			verifiedKept = verifiedKept || verified
			backup.Close()
			continue
		}
		if !s.claimRemoval(record.ID) {
			backup.Close()
			problems = append(problems, fmt.Sprintf("the older backup %s was being downloaded, so it was left in place until the next backup", record.BackupName))
			continue
		}
		err = backup.Remove()
		backup.Close()
		s.releaseRemoval(record.ID)
		if err != nil {
			problems = append(problems, fmt.Sprintf("could not remove the older backup %s: %v", record.BackupName, err))
			continue
		}
		s.forget(ctx, record)
	}
	if err := s.forgetOldRuns(ctx, run); err != nil {
		problems = append(problems, "could not forget old backup records: "+err.Error())
	}
	return problems
}

// forgetOldRuns forgets the records beyond the newest runsKept of runs
// whose backup is gone, except the newest scheduled run, whose start
// decides when the next scheduled backup is due.
func (s *Service) forgetOldRuns(ctx context.Context, current state.BackupRun) error {
	runs, err := s.Store.BackupRuns(ctx)
	if err != nil {
		return err
	}
	var forget []string
	scheduledSeen := current.Kind == state.BackupRunScheduled
	for index, record := range runs {
		newestScheduled := record.Kind == state.BackupRunScheduled && !scheduledSeen
		if record.Kind == state.BackupRunScheduled {
			scheduledSeen = true
		}
		if index >= runsKept && record.ID != current.ID && record.Status != state.BackupRunning && record.BackupName == "" && !newestScheduled {
			forget = append(forget, record.ID)
		}
	}
	return s.Store.ForgetBackupRuns(ctx, forget)
}
