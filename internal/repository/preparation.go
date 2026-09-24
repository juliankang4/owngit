package repository

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

// ErrRepositoryPreparing reports a repository whose preparation after startup
// has not succeeded yet. OwnGit refuses to read or write it until then.
var ErrRepositoryPreparing = errors.New("the repository is being prepared")

// Waits between failed preparation attempts: the first wait, doubling up to
// the cap.
const (
	preparationRetryFirst = 30 * time.Second
	preparationRetryMax   = 10 * time.Minute
)

// PreparationStep is extra recovery that must succeed before a repository is
// served, such as pull request recovery. It runs with the repository write
// lock held and receives the repository path, because the ordinary lookup
// refuses the repository until preparation succeeds.
type PreparationStep func(ctx context.Context, id, path string) error

// preparationState records which repositories are still being prepared. The
// zero value has no preparation running, so every repository is served; only
// a serving process starts preparation.
type preparationState struct {
	mu      sync.Mutex
	started bool
	ctx     context.Context
	step    PreparationStep
	logf    func(string, ...any)
	slots   chan struct{}
	jobs    map[string]*preparationJob
}

// preparationJob runs the attempts for one repository, one at a time.
type preparationJob struct {
	cancel context.CancelFunc
	// attempted closes when the first attempt returns, successful or not.
	attempted chan struct{}
	// done closes when the job stops.
	done chan struct{}
}

// StartPreparation marks every recorded repository as being prepared and
// prepares each one in the background: its safety configuration, its
// retention hook, and then step. A repository is served only after all of
// them succeed. A failed repository is retried alone, 30 seconds after the
// failed attempt and then at doubling intervals up to 10 minutes, while the
// other repositories are served.
//
// StartPreparation returns when every repository finished its first attempt
// or after grace, whichever comes first, so one hung repository does not hold
// back startup. A failure to read the repository list is returned. The jobs
// stop when ctx ends.
func (m *Manager) StartPreparation(ctx context.Context, step PreparationStep, grace time.Duration, logf func(string, ...any)) error {
	repositories, err := m.Store.Repositories(ctx)
	if err != nil {
		return fmt.Errorf("read repositories for preparation: %w", err)
	}
	if logf == nil {
		logf = func(string, ...any) {}
	}
	p := &m.preparation
	p.mu.Lock()
	if p.started {
		p.mu.Unlock()
		return errors.New("repository preparation has already started")
	}
	p.started, p.ctx, p.step, p.logf = true, ctx, step, logf
	p.slots = make(chan struct{}, prepareConcurrency)
	p.jobs = make(map[string]*preparationJob, len(repositories))
	jobs := make([]*preparationJob, 0, len(repositories))
	for _, stored := range repositories {
		jobs = append(jobs, m.startPreparationLocked(stored.ID))
	}
	p.mu.Unlock()

	timer := time.NewTimer(grace)
	defer timer.Stop()
	for _, job := range jobs {
		select {
		case <-job.attempted:
		case <-timer.C:
			logf("some repositories are still being prepared; they stay locked until preparation succeeds")
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}

// PrepareRegistered marks a repository that is about to be registered by
// recovery after startup as being prepared, and prepares it in the background
// like StartPreparation. Call it before the repository row is recorded, so
// the repository is never served unprepared. It does nothing when no
// preparation was started.
func (m *Manager) PrepareRegistered(id string) {
	p := &m.preparation
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.started || p.ctx.Err() != nil {
		return
	}
	if _, running := p.jobs[id]; running {
		return
	}
	m.startPreparationLocked(id)
}

// CancelPreparation stops preparing id and forgets its state. Deletion calls
// it with the repository lock held once the repository is removed, so a stale
// attempt can never change a later repository with the same ID.
func (m *Manager) CancelPreparation(id string) {
	p := &m.preparation
	p.mu.Lock()
	defer p.mu.Unlock()
	if job := p.jobs[id]; job != nil {
		job.cancel()
		delete(p.jobs, id)
	}
}

// Preparing reports whether id is still being prepared.
func (m *Manager) Preparing(id string) bool {
	if m == nil {
		return false
	}
	p := &m.preparation
	p.mu.Lock()
	defer p.mu.Unlock()
	_, preparing := p.jobs[id]
	return preparing
}

// StopPreparation cancels every preparation and waits for the attempts to
// return until ctx ends. The repositories stay locked.
func (m *Manager) StopPreparation(ctx context.Context) error {
	p := &m.preparation
	p.mu.Lock()
	jobs := make([]*preparationJob, 0, len(p.jobs))
	for _, job := range p.jobs {
		job.cancel()
		jobs = append(jobs, job)
	}
	p.mu.Unlock()
	for _, job := range jobs {
		select {
		case <-job.done:
		case <-ctx.Done():
			return fmt.Errorf("repository preparation did not stop: %w", ctx.Err())
		}
	}
	return nil
}

// startPreparationLocked records and starts the job for id. The caller holds
// the preparation mutex.
func (m *Manager) startPreparationLocked(id string) *preparationJob {
	p := &m.preparation
	ctx, cancel := context.WithCancel(p.ctx)
	job := &preparationJob{cancel: cancel, attempted: make(chan struct{}), done: make(chan struct{})}
	p.jobs[id] = job
	go m.runPreparation(ctx, id, job)
	return job
}

func (m *Manager) runPreparation(ctx context.Context, id string, job *preparationJob) {
	defer close(job.done)
	defer job.cancel()
	p := &m.preparation
	delay := m.PreparationRetry
	if delay <= 0 {
		delay = preparationRetryFirst
	}
	for attempt := 1; ; attempt++ {
		err := m.prepareAttempt(ctx, id, job)
		if attempt == 1 {
			close(job.attempted)
		}
		if err == nil {
			p.mu.Lock()
			ready := p.jobs[id] == job
			if ready {
				delete(p.jobs, id)
			}
			p.mu.Unlock()
			if ready && attempt > 1 {
				p.logf("repository %q is prepared after %d attempts and is served again", id, attempt)
			}
			if ready && m.OnChange != nil {
				m.OnChange(id)
			}
			return
		}
		if ctx.Err() != nil {
			return
		}
		p.logf("repository %q could not be prepared and stays locked; retrying in %s: %v", id, delay, err)
		timer := time.NewTimer(delay)
		select {
		case <-timer.C:
		case <-ctx.Done():
			timer.Stop()
			return
		}
		delay = min(delay*2, preparationRetryMax)
	}
}

// prepareAttempt makes one preparation attempt. It holds the repository write
// lock throughout, so no request can use the repository between the steps.
func (m *Manager) prepareAttempt(ctx context.Context, id string, job *preparationJob) error {
	p := &m.preparation
	select {
	case p.slots <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	}
	defer func() { <-p.slots }()
	lock := m.Locks.For(id)
	lock.Lock()
	defer lock.Unlock()
	p.mu.Lock()
	current := p.jobs[id] == job
	p.mu.Unlock()
	if !current {
		return errors.New("repository preparation was cancelled")
	}
	path, _, exists, err := m.existingPath(ctx, id)
	if err == nil && !exists {
		err = ErrRepositoryNotFound
	}
	if err != nil {
		return err
	}
	if err := m.configureLocked(ctx, path, m.Git); err != nil {
		return err
	}
	if p.step != nil {
		return p.step(ctx, id, path)
	}
	return nil
}
