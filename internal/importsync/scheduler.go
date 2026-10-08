package importsync

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"owngit/internal/state"
)

// Scheduler runs opt-in scheduled refreshes only while the serving process
// lives. It holds no daemon and performs nothing on passive reads: a tick asks
// for due schedules and starts at most one refresh per repository.
//
// Fairness comes from the store: starting a run stamps its schedule's
// last_started_at, and due selection orders by that stamp, so a slow repository
// cannot starve the others. A repository still being prepared is passed over
// unclaimed, so its schedule stays due and keeps its place. A schedule is
// claimed only once an execution slot is held; when every slot is busy the
// schedules stay due and unchanged, and they run in that order as slots free.
//
// Batch bounds the runs one pass starts and the rows one query returns. A
// pass pages past the rows it passed over, so a page of preparing
// repositories does not hide the due ones behind it.
//
// Service is required; the serving process always sets it.
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
	// A slot is taken before any schedule is read or claimed. With none free
	// the pass ends at once and every schedule stays due, so a run that never
	// started is never recorded. A slot not handed to start is given back.
	held := false
	acquire := func() bool {
		select {
		case slots <- struct{}{}:
			held = true
		default:
		}
		return held
	}
	defer func() {
		if held {
			<-slots
		}
	}()
	if !acquire() {
		return
	}
	// The pass keeps one time, so its pages read one due set, and each page
	// continues after the last row examined, whether claimed or passed over.
	now := s.Service.clock()
	started := 0
	var last *state.ImportSchedule
	for {
		schedules, err := s.Service.Store.DueImportSchedulesAfter(ctx, now, last, limit)
		if err != nil {
			s.logf("due import schedules could not be read: %v", err)
			return
		}
		for _, schedule := range schedules {
			last = &schedule
			if err := ctx.Err(); err != nil {
				return
			}
			if s.Service.Repositories.Preparing(schedule.RepositoryID) {
				continue
			}
			claimed, err := s.Service.Store.ClaimDueImportSchedule(ctx, schedule.RepositoryID, now)
			if err != nil {
				s.logf("due import schedule for %s could not be claimed: %v", schedule.RepositoryID, err)
				return
			}
			if !claimed {
				continue
			}
			held = false
			s.start(ctx, schedule.RepositoryID, now, slots)
			started++
			// Waiting schedules stay due for the next tick or wake once a slot frees.
			if started == limit || !acquire() {
				return
			}
		}
		if len(schedules) < limit {
			return
		}
	}
}

// start runs one claimed scheduled refresh in the slot it holds. A refresh
// refused before it recorded a run is recorded as a failed run instead.
func (s *Scheduler) start(ctx context.Context, repositoryID string, now time.Time, slots chan struct{}) {
	s.wait.Add(1)
	go func() {
		defer s.wait.Done()
		defer func() {
			<-slots
			s.mu.Lock()
			defer s.mu.Unlock()
			select {
			case s.wake <- struct{}{}:
			default:
			}
		}()
		run, err := s.Service.RefreshScheduled(ctx, repositoryID, Limits{})
		if err != nil && problemCode(err) != CodeBusy {
			s.logf("scheduled import for %s failed: %v", repositoryID, err)
			if run.ID == "" {
				_ = s.Service.recordScheduledClaimFailure(context.WithoutCancel(ctx), repositoryID, err.Error(), now)
			}
		}
	}()
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
