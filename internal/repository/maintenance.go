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
	"owngit/internal/state"
)

// OwnGit turns off Git's automatic maintenance (see repositoryConfig) because
// gc may prune history that retention keeps. Without maintenance, every push
// leaves loose objects and refs behind and reads slow down, most of all on
// network storage. The maintenance here never deletes a ref, and never an
// object unless the owner turned unused object cleanup on: it packs loose
// refs and objects and writes a commit-graph. With cleanup on, the nightly
// run also removes objects that no ref reaches and that are older than the
// chosen grace period (see MaintenanceCleanup).
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
// The owner chooses these, and turns maintenance off, under Settings
// (state.Maintenance). The scheduler reads the choices before each job, so
// a change applies from the next job on and never stops a running one.
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
	// MaintenanceCleanup rewrites every pack into one like full maintenance,
	// but removes the objects that no ref reaches and that are older than
	// the cleanup grace period, packed and loose. Unreachable objects
	// younger than that stay in a separate cruft pack with their times, and
	// packs with a .keep file stay as they are. It runs only at night, while
	// the owner has turned cleanup on.
	MaintenanceCleanup MaintenanceKind = "cleanup"
)

// MaintenanceSchedule configures repository maintenance. A zero field
// follows the owner's saved choices (state.Maintenance), read before each
// job; Retry, FailureRetry and Now are fixed.
type MaintenanceSchedule struct {
	// Idle is how long a repository must go without requests before
	// maintenance starts.
	Idle time.Duration
	// Retry is the wait after the repository was in use. Default 1 minute.
	Retry time.Duration
	// FailureRetry is the wait after a failed maintenance, and the longest
	// wait while maintenance is off or its choices cannot be read. Default
	// 1 hour.
	FailureRetry time.Duration
	// NightStartHour and NightEndHour bound the local-time window for full
	// consolidation. It passes midnight when the start is later than the
	// end.
	NightStartHour, NightEndHour int
	// PackThreshold is the pack count above which the night consolidates.
	PackThreshold int
	// CommandTimeout bounds each command except the full repack.
	CommandTimeout time.Duration
	// FullRepackTimeout bounds the full repack.
	FullRepackTimeout time.Duration
	// Now replaces the clock in tests.
	Now func() time.Time
	// cleanupGrace is the unused object cleanup grace period the nightly
	// run follows, or zero while cleanup is off or its choice cannot be
	// read.
	cleanupGrace time.Duration
}

// following fills the zero fields of schedule from the owner's choices.
func (schedule MaintenanceSchedule) following(choices state.Maintenance) MaintenanceSchedule {
	if schedule.Idle <= 0 {
		schedule.Idle = choices.Idle
	}
	if schedule.Retry <= 0 {
		schedule.Retry = time.Minute
	}
	if schedule.FailureRetry <= 0 {
		schedule.FailureRetry = time.Hour
	}
	if schedule.NightStartHour == 0 && schedule.NightEndHour == 0 {
		schedule.NightStartHour, schedule.NightEndHour = choices.WindowStart, choices.WindowEnd
	}
	if schedule.PackThreshold <= 0 {
		schedule.PackThreshold = choices.PackThreshold
	}
	if schedule.CommandTimeout <= 0 {
		schedule.CommandTimeout = choices.CommandTime
	}
	if schedule.FullRepackTimeout <= 0 {
		schedule.FullRepackTimeout = choices.FullRepackTime
	}
	if schedule.Now == nil {
		schedule.Now = time.Now
	}
	return schedule
}

// withDefaults fills the zero fields of schedule with the default choices.
func (schedule MaintenanceSchedule) withDefaults() MaintenanceSchedule {
	return schedule.following(state.DefaultMaintenance)
}

// maintenanceCommands lists the Git commands of kind. Only cleanup deletes
// an object: repack -d removes only loose objects that the new pack holds,
// full maintenance keeps unreachable objects, and gc and --prune=now are
// never used. Cleanup removes unreachable objects older than grace with
// the cruft expiration and prune, both counting from each object's own
// time. Packs with a .keep file are never removed.
func maintenanceCommands(kind MaintenanceKind, grace time.Duration) [][]string {
	repack := []string{"repack", "-d"}
	switch kind {
	case MaintenanceFull:
		repack = []string{"repack", "-a", "-d", "--keep-unreachable"}
	case MaintenanceCleanup:
		expire := fmt.Sprintf("%d.days.ago", int64(grace/(24*time.Hour)))
		return [][]string{
			{"pack-refs", "--all"},
			{"repack", "-a", "-d", "--cruft", "--cruft-expiration=" + expire},
			{"prune", "--expire=" + expire},
			{"commit-graph", "write", "--reachable", "--split"},
		}
	}
	return [][]string{
		{"pack-refs", "--all"},
		repack,
		{"commit-graph", "write", "--reachable", "--split"},
	}
}

// errCleanupDeferred reports that a check or an import that may still need
// an unreachable object was unfinished for the repository; its cleanup
// waits for the next window.
var errCleanupDeferred = errors.New("a check or an import of the repository has not finished")

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
	// choicesProblem and cleanupProblem are the last reasons the saved
	// maintenance and cleanup choices could not be used, each logged once
	// until it changes.
	choicesProblem, cleanupProblem string
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
	s.started, s.schedule, s.logf = true, schedule, logf
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
	// schedule is what the job follows, read when it was chosen.
	schedule MaintenanceSchedule
}

// WakeMaintenance makes the scheduler read the saved maintenance choices
// again now, after they were changed.
func (m *Manager) WakeMaintenance() {
	s := &m.maintenance
	s.mu.Lock()
	defer s.mu.Unlock()
	s.signalLocked()
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
	now, base, logf := s.nowLocked(), s.schedule, s.logf
	s.mu.Unlock()
	choices, err := m.Store.Maintenance(ctx)
	problem := ""
	switch {
	case err != nil:
		problem = fmt.Sprintf("repository maintenance paused: %v", err)
	case !choices.Enabled:
		problem = "repository maintenance is turned off in Settings"
	}
	s.mu.Lock()
	if problem != s.choicesProblem {
		s.choicesProblem = problem
		if problem != "" {
			logf("%s", problem)
		}
	}
	s.mu.Unlock()
	if problem != "" {
		// Saving the choices wakes the scheduler (WakeMaintenance).
		return nil, base.withDefaults().FailureRetry
	}
	schedule := base.following(choices)
	cleanup, err := m.Store.UnusedObjectCleanup(ctx)
	cleanupProblem := ""
	if err != nil {
		cleanupProblem = fmt.Sprintf("unused object cleanup paused: %v", err)
	} else if cleanup.Enabled {
		schedule.cleanupGrace = cleanup.Grace
	}
	s.mu.Lock()
	if cleanupProblem != s.cleanupProblem {
		s.cleanupProblem = cleanupProblem
		if cleanupProblem != "" {
			logf("%s", cleanupProblem)
		}
	}
	s.mu.Unlock()
	s.mu.Lock()
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
		job := &maintenanceJob{id: id, schedule: schedule}
		if nightDue {
			job.night = night
		}
		return job, 0
	}
	return nil, wait
}

// nightOf returns the local date the consolidation window started on when
// now is inside it, and "" otherwise. A window that passes midnight belongs
// to the date it started.
func (schedule MaintenanceSchedule) nightOf(now time.Time) string {
	start, end, hour := schedule.NightStartHour, schedule.NightEndHour, now.Hour()
	switch {
	case start < end && hour >= start && hour < end, start > end && hour >= start:
		return now.Format(time.DateOnly)
	case start > end && hour < end:
		return now.AddDate(0, 0, -1).Format(time.DateOnly)
	}
	return ""
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
	schedule, logf := job.schedule, s.logf
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
	switch {
	case job.night != "" && schedule.cleanupGrace > 0:
		kind = MaintenanceCleanup
	case job.night != "":
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
	case errors.Is(err, errCleanupDeferred):
		// Tonight's cleanup is over; a write still gets its small
		// maintenance.
		entry.pending = entry.pending || wasPending
		entry.night = job.night
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
	total := len(maintenanceCommands(kind, schedule.cleanupGrace))
	switch {
	case kind == "" || (steps == 0 && (errors.Is(err, errMaintenanceBusy) || errors.Is(err, ErrRepositoryNotFound))):
		// Nothing ran; a deferral is retried without a log line.
	case err == nil:
		logf("repository %q maintenance (%s) completed in %s", job.id, kind, elapsed)
	case errors.Is(err, errCleanupDeferred):
		logf("repository %q maintenance (%s) deferred to the next window after %d of %d steps: %v", job.id, kind, steps, total, err)
	case errors.Is(err, errMaintenanceBusy):
		logf("repository %q maintenance (%s) paused after %s and %d of %d steps: %v; the rest runs later", job.id, kind, elapsed, steps, total, err)
	case jobContext.Err() != nil:
		logf("repository %q maintenance (%s) stopped after %s and %d of %d steps: OwnGit is stopping or the repository is being deleted", job.id, kind, elapsed, steps, total)
	default:
		logf("repository %q maintenance (%s) failed after %s and %d of %d steps: %v", job.id, kind, elapsed, steps, total, err)
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
	commands := maintenanceCommands(kind, schedule.cleanupGrace)
	for index, args := range commands {
		if err := ctx.Err(); err != nil {
			return index, err
		}
		if lock.Waiting() || m.repositoryUses(id) != uses || !lock.TryLock() {
			return index, errMaintenanceBusy
		}
		// A backup reads the repository's object files until its bundle is
		// written; repacking would remove some of them.
		if m.heldForBackup(id) {
			lock.UnlockWithoutRefChanges()
			return index, errMaintenanceBusy
		}
		// A check or an import records the commits it will read only in
		// the state, where no ref reaches them. Cleanup reads whether any
		// is unfinished under the write lock it cleans under.
		if kind == MaintenanceCleanup {
			busy, err := m.Store.RepositoryObjectsInUse(ctx, id)
			if err == nil && busy {
				err = errCleanupDeferred
			}
			if err != nil {
				lock.UnlockWithoutRefChanges()
				return index, err
			}
		}
		timeout := schedule.CommandTimeout
		if kind != MaintenanceSmall && args[0] == "repack" {
			timeout = schedule.FullRepackTimeout
		}
		if err := m.maintenanceStep(ctx, id, lock, timeout, args); err != nil {
			return index, err
		}
	}
	return len(commands), nil
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
	// The write lock is held, so the command must not wait for the memory
	// gate; it leaves and the scheduler retries later, as for a busy repository.
	if _, err := m.Git.RunWithLimits(gitexec.WithoutGateWait(ctx), path, nil, limits, append([]string{"--git-dir", "."}, args...)...); err != nil {
		if errors.Is(err, gitexec.ErrMemoryBusy) {
			return errMaintenanceBusy
		}
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
