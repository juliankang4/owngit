package importsync

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

// Scheduler runs opt-in scheduled refreshes only while the serving process
// lives. It holds no daemon and performs nothing on passive reads: a tick asks
// for due schedules and starts at most one refresh per repository.
//
// Fairness comes from the store: starting a run stamps its schedule's
// last_started_at, and due selection orders by that stamp, so a slow repository
// cannot starve the others.
type Scheduler struct {
	Service     *Service
	Interval    time.Duration
	Batch       int
	Concurrency int
	Logf        func(string, ...any)

	mu      sync.Mutex
	cancel  context.CancelFunc
	done    chan struct{}
	wake    chan struct{}
	slots   chan struct{}
	wait    sync.WaitGroup
	running bool
}

// Start begins the scheduler loop. It returns immediately; the caller can
// optionally call Service.Reconcile before or after.
func (s *Scheduler) Start(parent context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.running {
		return errors.New("import scheduler is already running")
	}
	if s.Service == nil || s.Service.Store == nil {
		return errors.New("import scheduler has no service")
	}
	// Starting the scheduler is an explicit mutation: prepare the runtime root
	// and its lifetime lease now, so the first tick cannot race preparation.
	if _, err := s.Service.Prepare(parent); err != nil {
		err = fmt.Errorf("prepare import runtime: %w", err)
		s.Service.noteScheduler(false, err)
		return err
	}
	s.Service.noteScheduler(true, nil)
	interval := s.Interval
	if interval <= 0 {
		interval = 30 * time.Second
	}
	concurrency := s.Concurrency
	if concurrency <= 0 {
		concurrency = 1
	}
	ctx, cancel := context.WithCancel(parent)
	done := make(chan struct{})
	wake := make(chan struct{}, 1)
	slots := make(chan struct{}, concurrency)
	s.cancel, s.done, s.wake, s.slots, s.running = cancel, done, wake, slots, true
	go s.loop(ctx, interval, done, wake, slots)
	return nil
}

// Stop cancels the loop and waits for in-flight scheduled runs or the context.
func (s *Scheduler) Stop(ctx context.Context) error {
	s.mu.Lock()
	cancel, done := s.cancel, s.done
	s.mu.Unlock()
	if cancel == nil {
		return nil
	}
	cancel()
	s.Service.noteScheduler(false, nil)
	select {
	case <-done:
	case <-ctx.Done():
		return ctx.Err()
	}
	waitCh := make(chan struct{})
	go func() {
		s.wait.Wait()
		close(waitCh)
	}()
	select {
	case <-waitCh:
	case <-ctx.Done():
		return ctx.Err()
	}
	s.mu.Lock()
	if s.done == done {
		s.cancel, s.done, s.wake, s.slots, s.running = nil, nil, nil, nil, false
	}
	s.mu.Unlock()
	return nil
}

// Wake requests an immediate due check. It never blocks.
func (s *Scheduler) Wake() {
	s.mu.Lock()
	wake := s.wake
	s.mu.Unlock()
	if wake == nil {
		return
	}
	select {
	case wake <- struct{}{}:
	default:
	}
}

func (s *Scheduler) loop(ctx context.Context, interval time.Duration, done chan<- struct{}, wake <-chan struct{}, slots chan struct{}) {
	defer close(done)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	s.pump(ctx, slots)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		case <-wake:
		}
		s.pump(ctx, slots)
	}
}

func (s *Scheduler) pump(ctx context.Context, slots chan struct{}) {
	limit := s.Batch
	if limit <= 0 {
		limit = 4
	}
	now := s.Service.clock()
	schedules, err := s.Service.Store.DueImportSchedules(ctx, now, limit)
	if err != nil {
		s.logf("due import schedules could not be read: %v", err)
		return
	}
	for _, schedule := range schedules {
		if err := ctx.Err(); err != nil {
			return
		}
		claimed, err := s.Service.Store.ClaimDueImportSchedule(ctx, schedule.RepositoryID, now)
		if err != nil {
			s.logf("due import schedule for %s could not be claimed: %v", schedule.RepositoryID, err)
			return
		}
		if !claimed {
			continue
		}
		select {
		case slots <- struct{}{}:
		default:
			_ = s.Service.recordScheduledClaimFailure(ctx, schedule.RepositoryID, "scheduled import concurrency is saturated", now)
			return
		}
		repositoryID := schedule.RepositoryID
		s.wait.Add(1)
		go func() {
			defer s.wait.Done()
			defer func() { <-slots }()
			run, err := s.Service.RefreshScheduled(ctx, repositoryID, Limits{})
			if err != nil && problemCode(err) != CodeBusy {
				s.logf("scheduled import for %s failed: %v", repositoryID, err)
				if run.ID == "" {
					_ = s.Service.recordScheduledClaimFailure(context.WithoutCancel(ctx), repositoryID, err.Error(), now)
				}
			}
		}()
	}
}

func (s *Scheduler) logf(format string, arguments ...any) {
	if s.Logf != nil {
		s.Logf(format, arguments...)
		return
	}
	if s.Service != nil {
		s.Service.logf(format, arguments...)
	}
}
