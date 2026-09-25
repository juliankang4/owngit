package repository

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"

	"owngit/internal/gitexec"
)

// OwnGit turns off Git's automatic maintenance (see repositoryConfig) because
// gc may prune history that retention keeps. Without maintenance, every push
// leaves loose objects and refs behind and reads slow down, most of all on
// network storage. The maintenance here never deletes an object or a ref: it
// only packs loose refs and objects and writes a commit-graph.
//
// One scheduler goroutine runs every maintenance, so at most one repository is
// maintained at a time. A repository is maintained only while nobody uses it:
//
//   - small maintenance runs once the repository has had no request for Idle
//     after OwnGit wrote to it (a push, an import, a pull request or restore
//     change, or its preparation at startup);
//   - full consolidation runs once per night between NightStartHour and
//     NightEndHour, server local time, on an idle repository with more than
//     PackThreshold packs.
//
// Each command takes the repository write lock with TryLock and releases it
// right after. Before each command, the run stops when the repository had a
// request or write since the run started or when anyone waits for its lock,
// so a push or page that arrives during a command waits for that command
// only and the rest of the run waits until the repository is idle again.
// The commands change no ref value and no object, so the lock is released
// without invalidating cached ref snapshots (see maintenanceStep), and the
// dashboard keeps showing the repository without waiting for it.

// MaintenanceKind names what a maintenance run does.
type MaintenanceKind string

const (
	// MaintenanceSmall packs refs and loose objects and extends the
	// commit-graph.
	MaintenanceSmall MaintenanceKind = "small"
	// MaintenanceFull also rewrites every pack into one, keeping unreachable
	// objects.
	MaintenanceFull MaintenanceKind = "full"
)

// MaintenanceSchedule configures repository maintenance. A zero field keeps
// its default.
type MaintenanceSchedule struct {
	// Idle is how long a repository must go without requests before
	// maintenance starts. Default 5 minutes.
	Idle time.Duration
	// Retry is the wait after the repository was in use. Default 1 minute.
	Retry time.Duration
	// FailureRetry is the wait after a failed maintenance. Default 1 hour.
	FailureRetry time.Duration
	// NightStartHour and NightEndHour bound the local-time window for full
	// consolidation. Defaults 3 and 5.
	NightStartHour, NightEndHour int
	// PackThreshold is the pack count above which the night consolidates.
	// Default 20.
	PackThreshold int
	// CommandTimeout bounds each command except the full repack. Default 30
	// minutes.
	CommandTimeout time.Duration
	// FullRepackTimeout bounds the full repack. Default 2 hours.
	FullRepackTimeout time.Duration
	// Now replaces the clock in tests.
	Now func() time.Time
}

func (schedule MaintenanceSchedule) withDefaults() MaintenanceSchedule {
	if schedule.Idle <= 0 {
		schedule.Idle = 5 * time.Minute
	}
	if schedule.Retry <= 0 {
		schedule.Retry = time.Minute
	}
	if schedule.FailureRetry <= 0 {
		schedule.FailureRetry = time.Hour
	}
	if schedule.NightStartHour == 0 && schedule.NightEndHour == 0 {
		schedule.NightStartHour, schedule.NightEndHour = 3, 5
	}
	if schedule.PackThreshold <= 0 {
		schedule.PackThreshold = 20
	}
	if schedule.CommandTimeout <= 0 {
		schedule.CommandTimeout = 30 * time.Minute
	}
	if schedule.FullRepackTimeout <= 0 {
		schedule.FullRepackTimeout = 2 * time.Hour
	}
	if schedule.Now == nil {
		schedule.Now = time.Now
	}
	return schedule
}

// maintenanceCommands lists the Git commands of kind. None of them deletes an
// object: repack -d removes only loose objects that the new pack holds, and
// -a without --keep-unreachable, -A, gc, prune and --expire are never used.
// Packs with a .keep file are never removed.
func maintenanceCommands(kind MaintenanceKind) [][]string {
	repack := []string{"repack", "-d"}
	if kind == MaintenanceFull {
		repack = []string{"repack", "-a", "-d", "--keep-unreachable"}
	}
	return [][]string{
		{"pack-refs", "--all"},
		repack,
		{"commit-graph", "write", "--reachable", "--split"},
	}
}

// errMaintenanceBusy reports that the repository was in use or still being
// prepared; the maintenance runs later.
var errMaintenanceBusy = errors.New("the repository is in use")

// maintenanceState is the scheduler. Its zero value records writes and use
// before StartMaintenance.
type maintenanceState struct {
	mu       sync.Mutex
	entries  map[string]*maintenanceEntry
	wake     chan struct{}
	started  bool
	cancel   context.CancelFunc
	done     chan struct{}
	schedule MaintenanceSchedule
	logf     func(string, ...any)
	// listedNight is the night whose repository list was read.
	listedNight string
	running     *runningMaintenance
}

type maintenanceEntry struct {
	lastUse time.Time
	// uses counts requests and writes; a run stops when it changes.
	uses uint64
	// pending records a write since the last maintenance started.
	pending   bool
	notBefore time.Time
	// night is the last night whose consolidation check finished.
	night string
}

type runningMaintenance struct {
	id     string
	cancel context.CancelFunc
}

func (s *maintenanceState) entryLocked(id string) *maintenanceEntry {
	if s.entries == nil {
		s.entries = make(map[string]*maintenanceEntry)
	}
	entry := s.entries[id]
	if entry == nil {
		entry = &maintenanceEntry{}
		s.entries[id] = entry
	}
	return entry
}

func (s *maintenanceState) nowLocked() time.Time {
	if s.schedule.Now != nil {
		return s.schedule.Now()
	}
	return time.Now()
}

func (s *maintenanceState) signalLocked() {
	if s.wake == nil {
		s.wake = make(chan struct{}, 1)
	}
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

// NoteRepositoryWrite records that OwnGit changed repository id, so it gets
// small maintenance once it is idle. It never blocks.
func (m *Manager) NoteRepositoryWrite(id string) {
	s := &m.maintenance
	s.mu.Lock()
	defer s.mu.Unlock()
	entry := s.entryLocked(id)
	entry.pending = true
	entry.lastUse = s.nowLocked()
	entry.uses++
	s.signalLocked()
}

// NoteRepositoryUse records a request for repository id, or a check job that
// found it busy, which postpones its maintenance and stops a running one
// before its next step. Only repositories the scheduler already knows are recorded,
// so requests for arbitrary names cannot grow its state.
func (m *Manager) NoteRepositoryUse(id string) {
	if m == nil {
		return
	}
	s := &m.maintenance
	s.mu.Lock()
	defer s.mu.Unlock()
	if entry := s.entries[id]; entry != nil {
		entry.lastUse = s.nowLocked()
		entry.uses++
	}
}

// repositoryUses returns the count of requests and writes recorded for id.
func (m *Manager) repositoryUses(id string) uint64 {
	s := &m.maintenance
	s.mu.Lock()
	defer s.mu.Unlock()
	if entry := s.entries[id]; entry != nil {
		return entry.uses
	}
	return 0
}

// StartMaintenance starts the maintenance scheduler for every recorded
// repository. It stops when ctx ends or StopMaintenance is called.
func (m *Manager) StartMaintenance(ctx context.Context, schedule MaintenanceSchedule, logf func(string, ...any)) error {
	repositories, err := m.Store.Repositories(ctx)
	if err != nil {
		return fmt.Errorf("read repositories for maintenance: %w", err)
	}
	if logf == nil {
		logf = func(string, ...any) {}
	}
	s := &m.maintenance
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.started {
		return errors.New("repository maintenance has already started")
	}
	s.started, s.schedule, s.logf = true, schedule.withDefaults(), logf
	for _, stored := range repositories {
		s.entryLocked(stored.ID)
	}
	loopContext, cancel := context.WithCancel(ctx)
	s.cancel, s.done = cancel, make(chan struct{})
	s.signalLocked()
	go m.maintenanceLoop(loopContext, s.wake, s.done)
	return nil
}

// StopMaintenance stops the scheduler and waits until a running maintenance
// command has been terminated and reaped, or until ctx ends.
func (m *Manager) StopMaintenance(ctx context.Context) error {
	s := &m.maintenance
	s.mu.Lock()
	cancel, done := s.cancel, s.done
	s.mu.Unlock()
	if cancel == nil {
		return nil
	}
	cancel()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("repository maintenance did not stop: %w", ctx.Err())
	}
}

// stopMaintenanceOf cancels a running maintenance of id, so a deletion does
// not wait for it.
func (m *Manager) stopMaintenanceOf(id string) {
	s := &m.maintenance
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.running != nil && s.running.id == id {
		s.running.cancel()
	}
}

// forgetMaintenance drops the scheduled maintenance of a deleted repository,
// so it never reaches a later repository with the same ID.
func (m *Manager) forgetMaintenance(id string) {
	s := &m.maintenance
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.entries, id)
	if s.running != nil && s.running.id == id {
		s.running.cancel()
	}
}

type maintenanceJob struct {
	id string
	// night is set for the nightly consolidation check.
	night string
}

func (m *Manager) maintenanceLoop(ctx context.Context, wake <-chan struct{}, done chan<- struct{}) {
	defer close(done)
	for {
		job, wait := m.nextMaintenance(ctx)
		if ctx.Err() != nil {
			return
		}
		if job != nil {
			m.runMaintenance(ctx, *job)
			continue
		}
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-wake:
		case <-timer.C:
		}
		timer.Stop()
	}
}

// nextMaintenance returns the next job that is due, or how long to wait for
// one.
func (m *Manager) nextMaintenance(ctx context.Context) (*maintenanceJob, time.Duration) {
	s := &m.maintenance
	s.mu.Lock()
	now, schedule := s.nowLocked(), s.schedule
	night := schedule.nightOf(now)
	listed := s.listedNight
	s.mu.Unlock()
	if night != "" && listed != night {
		// Repositories created since the last night join the check.
		if repositories, err := m.Store.Repositories(ctx); err == nil {
			s.mu.Lock()
			for _, stored := range repositories {
				s.entryLocked(stored.ID)
			}
			s.listedNight = night
			s.mu.Unlock()
		}
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	ids := make([]string, 0, len(s.entries))
	for id := range s.entries {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	wait := schedule.untilNight(now)
	for _, id := range ids {
		entry := s.entries[id]
		nightDue := night != "" && entry.night != night
		if !entry.pending && !nightDue {
			continue
		}
		if m.Preparing(id) {
			// Preparation reports a write when it finishes; check again
			// later in case nobody reports it.
			wait = min(wait, schedule.Retry)
			continue
		}
		ready := entry.lastUse.Add(schedule.Idle)
		if entry.notBefore.After(ready) {
			ready = entry.notBefore
		}
		if now.Before(ready) {
			wait = min(wait, ready.Sub(now))
			continue
		}
		job := &maintenanceJob{id: id}
		if nightDue {
			job.night = night
		}
		return job, 0
	}
	return nil, wait
}

// nightOf returns the local date of now when now is inside the consolidation
// window, and "" otherwise.
func (schedule MaintenanceSchedule) nightOf(now time.Time) string {
	if hour := now.Hour(); hour < schedule.NightStartHour || hour >= schedule.NightEndHour {
		return ""
	}
	return now.Format(time.DateOnly)
}

// untilNight returns the time until the next consolidation window starts,
// tomorrow's when the window has already started today.
func (schedule MaintenanceSchedule) untilNight(now time.Time) time.Duration {
	start := time.Date(now.Year(), now.Month(), now.Day(), schedule.NightStartHour, 0, 0, 0, now.Location())
	if !start.After(now) {
		start = time.Date(now.Year(), now.Month(), now.Day()+1, schedule.NightStartHour, 0, 0, 0, now.Location())
	}
	return start.Sub(now)
}

// runMaintenance runs one job and records its outcome for the schedule.
func (m *Manager) runMaintenance(ctx context.Context, job maintenanceJob) {
	s := &m.maintenance
	s.mu.Lock()
	entry := s.entries[job.id]
	if entry == nil {
		s.mu.Unlock()
		return
	}
	schedule, logf := s.schedule, s.logf
	wasPending := entry.pending
	entry.pending = false
	jobContext, cancel := context.WithCancel(ctx)
	defer cancel()
	s.running = &runningMaintenance{id: job.id, cancel: cancel}
	s.mu.Unlock()

	kind := MaintenanceSmall
	started := schedule.Now()
	var steps int
	var err error
	if job.night != "" {
		var consolidate bool
		consolidate, err = m.needsConsolidation(jobContext, job.id, schedule.PackThreshold)
		if consolidate {
			kind = MaintenanceFull
		} else if err == nil && !wasPending {
			kind = ""
		}
	}
	if kind != "" && err == nil {
		steps, err = m.maintain(jobContext, job.id, kind, schedule)
	}
	elapsed := schedule.Now().Sub(started).Round(10 * time.Millisecond)

	s.mu.Lock()
	s.running = nil
	current := s.entries[job.id] == entry
	switch {
	case !current:
	case errors.Is(err, ErrRepositoryNotFound):
		delete(s.entries, job.id)
	case err == nil:
		if job.night != "" {
			entry.night = job.night
		}
	case errors.Is(err, errMaintenanceBusy) || jobContext.Err() != nil:
		entry.pending = entry.pending || wasPending
		entry.notBefore = schedule.Now().Add(schedule.Retry)
	default:
		entry.pending = entry.pending || wasPending
		entry.notBefore = schedule.Now().Add(schedule.FailureRetry)
		if job.night != "" {
			entry.night = job.night
		}
	}
	s.mu.Unlock()

	if !current && err == nil {
		err = errors.New("the repository was deleted")
	}
	switch {
	case kind == "" || (steps == 0 && (errors.Is(err, errMaintenanceBusy) || errors.Is(err, ErrRepositoryNotFound))):
		// Nothing ran; a deferral is retried without a log line.
	case err == nil:
		logf("repository %q maintenance (%s) completed in %s", job.id, kind, elapsed)
	case errors.Is(err, errMaintenanceBusy):
		logf("repository %q maintenance (%s) paused after %s and %d of 3 steps: %v; the rest runs later", job.id, kind, elapsed, steps, err)
	case jobContext.Err() != nil:
		logf("repository %q maintenance (%s) stopped after %s and %d of 3 steps: OwnGit is stopping or the repository is being deleted", job.id, kind, elapsed, steps)
	default:
		logf("repository %q maintenance (%s) failed after %s and %d of 3 steps: %v", job.id, kind, elapsed, steps, err)
	}
}

// needsConsolidation reports whether the repository has more than threshold
// packs. It only lists the pack directory.
func (m *Manager) needsConsolidation(ctx context.Context, id string, threshold int) (bool, error) {
	path, _, exists, err := m.existingPath(ctx, id)
	if err == nil && !exists {
		err = ErrRepositoryNotFound
	}
	if err != nil {
		return false, err
	}
	packs, err := filepath.Glob(filepath.Join(path, "objects", "pack", "pack-*.pack"))
	if err != nil {
		return false, err
	}
	return len(packs) > threshold, nil
}

// maintain runs the commands of kind on repository id and returns how many
// completed. Each command takes the repository write lock with TryLock and
// releases it right after, and re-checks that the repository still exists
// and is prepared. The run stops with errMaintenanceBusy when the repository
// is locked, when anyone waits for its lock, or when it had a request or a
// write since the run started. Checking for waiters matters: after Unlock,
// TryLock in this goroutine wins over a blocked push. The commands are
// idempotent, so a later run starts again from the first.
func (m *Manager) maintain(ctx context.Context, id string, kind MaintenanceKind, schedule MaintenanceSchedule) (int, error) {
	lock := m.Locks.For(id)
	uses := m.repositoryUses(id)
	for index, args := range maintenanceCommands(kind) {
		if err := ctx.Err(); err != nil {
			return index, err
		}
		if lock.Waiting() || m.repositoryUses(id) != uses || !lock.TryLock() {
			return index, errMaintenanceBusy
		}
		timeout := schedule.CommandTimeout
		if kind == MaintenanceFull && args[0] == "repack" {
			timeout = schedule.FullRepackTimeout
		}
		if err := m.maintenanceStep(ctx, id, lock, timeout, args); err != nil {
			return index, err
		}
	}
	return len(maintenanceCommands(kind)), nil
}

// maintenanceStep runs one command with the write lock held. repack and
// commit-graph store the same objects differently and never touch refs, so
// they release the lock with UnlockWithoutRefChanges and cached ref
// snapshots stay current. pack-refs moves refs into packed-refs without
// changing them; the step compares every ref and HEAD before and after and
// releases the same way only when they are identical. Otherwise, or when a
// command failed, Unlock invalidates the snapshots.
func (m *Manager) maintenanceStep(ctx context.Context, id string, lock *gitexec.RepositoryLock, timeout time.Duration, args []string) error {
	// Nothing has run until the command starts.
	refsUnchanged := true
	defer func() {
		if refsUnchanged {
			lock.UnlockWithoutRefChanges()
		} else {
			lock.Unlock()
		}
	}()
	if m.Preparing(id) {
		return errMaintenanceBusy
	}
	path, _, exists, err := m.existingPath(ctx, id)
	if err == nil && !exists {
		err = ErrRepositoryNotFound
	}
	if err != nil {
		return err
	}
	limits := gitexec.CommandLimits{Timeout: timeout}
	packsRefs := args[0] == "pack-refs"
	// An unreadable ref state leaves before empty, and the release then
	// invalidates the snapshots.
	var before string
	if packsRefs {
		if state, err := m.refState(ctx, path, limits); err == nil {
			before = state
		}
	}
	refsUnchanged = false
	if m.maintenanceHook != nil {
		if err := m.maintenanceHook(ctx, id, args); err != nil {
			return err
		}
	}
	if _, err := m.Git.RunWithLimits(ctx, path, nil, limits, append([]string{"--git-dir", "."}, args...)...); err != nil {
		return err
	}
	if !packsRefs {
		refsUnchanged = true
	} else if before != "" {
		after, err := m.refState(ctx, path, limits)
		refsUnchanged = err == nil && after == before
	}
	return nil
}

// refState returns every ref with its OID and symbolic target, and HEAD, in
// one string that is equal only for equal refs.
func (m *Manager) refState(ctx context.Context, path string, limits gitexec.CommandLimits) (string, error) {
	head, err := os.ReadFile(filepath.Join(path, "HEAD"))
	if err != nil {
		return "", err
	}
	result, err := m.Git.RunWithLimits(ctx, path, nil, limits, "--git-dir", ".", "for-each-ref", "--format=%(objectname) %(refname) %(symref)")
	if err != nil {
		return "", err
	}
	return string(head) + "\n" + string(result.Stdout), nil
}
