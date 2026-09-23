package server

import (
	"context"
	"errors"
	"sync"
	"time"

	"owngit/internal/repository"
	"owngit/internal/state"
)

const (
	// activityWait is how long a page waits for activity that is still being
	// counted before it renders with an explicit "still counting" state.
	activityWait = 2 * time.Second
	// activityConcurrency bounds background activity computations, and
	// snapshotConcurrency bounds the ref listings a page reads at once. Both
	// keep a network share from receiving a burst of Git processes.
	activityConcurrency = 4
	snapshotConcurrency = 8
)

// errRefsUnlisted reports a repository whose refs could not be listed, so its
// activity key is unknown.
var errRefsUnlisted = errors.New("repository refs could not be listed")

// activityCache keeps one activity observation per repository. Each
// observation carries repository.Activity.Key, a digest of the refs it was
// computed from, and is reused only while RefSnapshot reports the same key.
// A push, restore, merge, import, or any other ref change therefore selects a
// new computation without an explicit invalidation call.
//
// Computations run in the background, so a slow repository delays its own
// numbers instead of the page. They keep the page-wide budget: a repository
// is counted with the budget left by the repositories before it, and is not
// counted at all until those are known or once the budget is spent.
type activityCache struct {
	mu      sync.Mutex
	root    context.Context
	cancel  context.CancelFunc
	stopped bool
	runs    sync.WaitGroup
	slots   chan struct{}
	entries map[string]*activityEntry
	// records counts cached records. The least recently used observations are
	// dropped above capacity, twice the page-wide activity limit, so memory
	// stays bounded by the budget that bounds one page.
	records  int
	capacity int
	clock    uint64
	// wait overrides activityWait in tests.
	wait time.Duration
}

type activityEntry struct {
	activity repository.Activity
	// limit is the commit budget activity was computed with.
	limit    int
	computed bool
	// err is the latest failure, for the ref key it was requested for.
	err       error
	failedKey string
	used      uint64
	running   chan struct{}
	// cancel ends the running computation early, for drop.
	cancel context.CancelFunc
}

// activityPart is one repository's share of a page observation.
type activityPart struct {
	// records is the observation cut to the repository's share of the budget.
	records    []repository.ActivityRecord
	incomplete bool
	// pending means the repository is still being counted; records then hold
	// its previous observation, if any.
	pending bool
	err     error
}

func (cache *activityCache) initLocked() {
	if cache.entries == nil {
		cache.entries = make(map[string]*activityEntry)
		cache.slots = make(chan struct{}, activityConcurrency)
	}
	if cache.root == nil {
		cache.root, cache.cancel = context.WithCancel(context.Background())
	}
}

// bind makes ctx, the serving lifetime, the parent of later computations.
func (cache *activityCache) bind(ctx context.Context) {
	cache.mu.Lock()
	defer cache.mu.Unlock()
	if cache.root == nil {
		cache.root, cache.cancel = context.WithCancel(ctx)
	}
	cache.initLocked()
}

// stop cancels running computations and waits for them to return. Later
// pages report activity as still counting.
func (cache *activityCache) stop() {
	cache.mu.Lock()
	cancel := cache.cancel
	cache.stopped = true
	cache.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	cache.runs.Wait()
}

func (cache *activityCache) entryLocked(id string) *activityEntry {
	entry := cache.entries[id]
	if entry == nil {
		entry = &activityEntry{}
		cache.entries[id] = entry
	}
	return entry
}

// scheduleLocked starts one computation for id with the given budget unless
// one is already running, and returns a channel that closes when the running
// computation ends. It returns nil after stop.
func (cache *activityCache) scheduleLocked(manager *repository.Manager, id, key string, limit int) <-chan struct{} {
	entry := cache.entryLocked(id)
	if entry.running != nil {
		return entry.running
	}
	if cache.stopped {
		return nil
	}
	done := make(chan struct{})
	ctx, cancel := context.WithCancel(cache.root)
	entry.running, entry.cancel = done, cancel
	slots := cache.slots
	cache.runs.Add(1)
	go func() {
		defer cache.runs.Done()
		defer cancel()
		var activity repository.Activity
		var err error
		select {
		case slots <- struct{}{}:
			activity, err = manager.Activity(ctx, id, limit)
			<-slots
		case <-ctx.Done():
			err = ctx.Err()
		}
		cache.mu.Lock()
		defer cache.mu.Unlock()
		entry.running, entry.cancel = nil, nil
		defer close(done)
		if cache.entries[id] != entry {
			// Dropped while running; the result belongs to no repository.
			return
		}
		if err != nil {
			entry.err, entry.failedKey = err, key
		} else {
			if entry.computed {
				cache.records -= len(entry.activity.Records)
			}
			cache.records += len(activity.Records)
			entry.activity, entry.limit, entry.computed, entry.err, entry.failedKey = activity, limit, true, nil, ""
			cache.evictLocked(entry)
		}
	}()
	return done
}

func (cache *activityCache) evictLocked(keep *activityEntry) {
	for cache.records > cache.capacity {
		var oldest *activityEntry
		for _, entry := range cache.entries {
			if entry != keep && entry.computed && (oldest == nil || entry.used < oldest.used) {
				oldest = entry
			}
		}
		if oldest == nil {
			return
		}
		cache.records -= len(oldest.activity.Records)
		oldest.activity, oldest.computed = repository.Activity{}, false
	}
}

// planLocked splits the budget across repositories in order, using cached
// observations whose key matches and scheduling the others. A repository with
// no observation at all blocks scheduling after it, because the budget left
// for later repositories is unknown until it is counted. attempted records
// repositories already scheduled by this page, so a page never counts one
// twice.
func (cache *activityCache) planLocked(manager *repository.Manager, ids, keys []string, maximum int, attempted map[string]bool) ([]activityPart, []<-chan struct{}) {
	parts := make([]activityPart, len(ids))
	var pending []<-chan struct{}
	schedule := func(id, key string, limit int) {
		if attempted[id] {
			return
		}
		attempted[id] = true
		if done := cache.scheduleLocked(manager, id, key, limit); done != nil {
			pending = append(pending, done)
		}
	}
	remaining := maximum
	blocked := false
	for index, id := range ids {
		part := &parts[index]
		if remaining <= 0 {
			// The budget is spent, so this repository is not counted and the
			// page is honestly incomplete.
			part.incomplete = true
			continue
		}
		if keys[index] == "" {
			part.err = errRefsUnlisted
			continue
		}
		if cache.entries[id] == nil && attempted[id] {
			// Dropped after this observation scheduled it, for a deletion or a
			// default-branch change. It takes no share of the budget and does
			// not hold back the repositories after it. The page says it is
			// still being counted rather than showing it as empty, because the
			// repository may still exist when that change was refused.
			part.pending = true
			continue
		}
		entry := cache.entryLocked(id)
		entry.used = cache.clock
		current := entry.computed && entry.activity.Key == keys[index] && (entry.limit >= remaining || !entry.activity.Incomplete)
		if !current && entry.err != nil && entry.failedKey == keys[index] {
			part.err = entry.err
			if entry.running == nil && !attempted[id] {
				// Retry once per page without waiting, as a page used to.
				attempted[id] = true
				cache.scheduleLocked(manager, id, keys[index], remaining)
			}
			continue
		}
		if !current {
			part.pending = true
			if !blocked {
				schedule(id, keys[index], remaining)
			}
			if !entry.computed {
				blocked = true
				continue
			}
		}
		records := entry.activity.Records
		part.incomplete = entry.activity.Incomplete
		if len(records) > remaining {
			records, part.incomplete = records[:remaining], true
		}
		part.records = records
		remaining -= len(records)
	}
	return parts, pending
}

// observe returns each repository's share of one page observation for the
// given ref keys. It counts what is missing and waits until the wait elapses
// or ctx ends; what is still being counted is returned as pending.
func (cache *activityCache) observe(ctx context.Context, manager *repository.Manager, ids, keys []string, maximum int, wait time.Duration) []activityPart {
	cache.mu.Lock()
	cache.initLocked()
	cache.clock++
	cache.capacity = max(cache.capacity, 2*maximum)
	if cache.wait > 0 {
		wait = cache.wait
	}
	cache.mu.Unlock()
	timer := time.NewTimer(wait)
	defer timer.Stop()
	attempted := make(map[string]bool)
	for {
		cache.mu.Lock()
		parts, pending := cache.planLocked(manager, ids, keys, maximum, attempted)
		cache.mu.Unlock()
		if len(pending) == 0 {
			return parts
		}
		for _, done := range pending {
			select {
			case <-done:
			case <-timer.C:
				return cache.finalPlan(manager, ids, keys, maximum, attempted)
			case <-ctx.Done():
				return cache.finalPlan(manager, ids, keys, maximum, attempted)
			}
		}
	}
}

func (cache *activityCache) finalPlan(manager *repository.Manager, ids, keys []string, maximum int, attempted map[string]bool) []activityPart {
	cache.mu.Lock()
	defer cache.mu.Unlock()
	parts, _ := cache.planLocked(manager, ids, keys, maximum, attempted)
	return parts
}

// drop cancels a running computation for id and forgets its observation. The
// deletion of a repository calls it first, so a background history walk does
// not keep holding the repository's read lock while the deletion waits briefly
// for the write lock, and calls it again afterwards, so nothing of the removed
// repository stays cached.
func (cache *activityCache) drop(id string) {
	cache.mu.Lock()
	defer cache.mu.Unlock()
	cache.dropLocked(id)
}

// dropIfCounting drops id only while a computation for it runs. A
// default-branch change calls it for the same lock reason as a deletion, but
// the change leaves a finished observation valid, so that one is kept.
func (cache *activityCache) dropIfCounting(id string) {
	cache.mu.Lock()
	defer cache.mu.Unlock()
	if entry := cache.entries[id]; entry != nil && entry.running != nil {
		cache.dropLocked(id)
	}
}

func (cache *activityCache) dropLocked(id string) {
	entry := cache.entries[id]
	if entry == nil {
		return
	}
	if entry.cancel != nil {
		entry.cancel()
	}
	if entry.computed {
		cache.records -= len(entry.activity.Records)
	}
	delete(cache.entries, id)
}

// forget drops observations of repositories that no longer exist.
func (cache *activityCache) forget(present []state.Repository) {
	cache.mu.Lock()
	defer cache.mu.Unlock()
	keep := make(map[string]bool, len(present))
	for _, stored := range present {
		keep[stored.ID] = true
	}
	for id, entry := range cache.entries {
		if !keep[id] && entry.running == nil {
			if entry.computed {
				cache.records -= len(entry.activity.Records)
			}
			delete(cache.entries, id)
		}
	}
}

// StartBackground begins counting activity for every repository under ctx,
// the serving lifetime, so the first dashboard visit usually finds current
// numbers. StopBackground cancels and waits for this counting. Pages work
// without StartBackground and then count on first use.
func (app *App) StartBackground(ctx context.Context) {
	app.activity.bind(ctx)
	app.activity.runs.Add(1)
	go func() {
		defer app.activity.runs.Done()
		repositories, err := app.Store.Repositories(ctx)
		if err != nil {
			return
		}
		ids := make([]string, len(repositories))
		for index, stored := range repositories {
			ids[index] = stored.ID
		}
		app.activity.observe(ctx, app.Repositories, ids, app.activityKeys(ctx, repositories), app.activityLimit(), 24*time.Hour)
	}()
}

// StopBackground cancels background activity counting and waits for it.
func (app *App) StopBackground() {
	app.activity.stop()
}

// refSnapshots reads every repository's ref snapshot with bounded
// concurrency. errs[i] reports a failure for repositories[i].
func (app *App) refSnapshots(ctx context.Context, repositories []state.Repository) ([]repository.RefSnapshot, []error) {
	snapshots := make([]repository.RefSnapshot, len(repositories))
	errs := make([]error, len(repositories))
	slots := make(chan struct{}, snapshotConcurrency)
	var group sync.WaitGroup
	for index := range repositories {
		group.Add(1)
		go func() {
			defer group.Done()
			select {
			case slots <- struct{}{}:
			case <-ctx.Done():
				errs[index] = ctx.Err()
				return
			}
			defer func() { <-slots }()
			snapshots[index], errs[index] = app.Repositories.RefSnapshot(ctx, repositories[index].ID)
		}()
	}
	group.Wait()
	return snapshots, errs
}

// activityKeys returns each repository's activity key. A repository whose
// refs cannot be listed gets an empty key, which no observation has.
func (app *App) activityKeys(ctx context.Context, repositories []state.Repository) []string {
	return activityKeysFrom(app.refSnapshots(ctx, repositories))
}

func activityKeysFrom(snapshots []repository.RefSnapshot, errs []error) []string {
	keys := make([]string, len(snapshots))
	for index := range snapshots {
		if errs[index] == nil {
			keys[index] = snapshots[index].ActivityKey
		}
	}
	return keys
}
