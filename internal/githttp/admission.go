package githttp

import (
	"context"
	"errors"
	"sync"
	"time"
)

// errBusy reports that no Git transfer slot became free within the queue wait.
var errBusy = errors.New("all Git transfer slots stayed busy")

// admission limits concurrent Git transfers. One repository may run up to
// perRepository transfers, and the server at most extra more than that.
// The extra slots are only for a repository with no transfer running, so
// one busy repository never blocks the others, while a single repository
// keeps all perRepository slots. A request waits for a slot at most for
// the queue wait.
//
// A request that builds a pack (a clone or fetch, an archive) is also
// limited to PackSlots at once when that limit is set, for a computer whose
// memory cannot pack more. Such a request waits until it fits both limits and
// holds nothing while it waits, so a push or a ref advertisement never waits
// behind queued clones. The queue wait covers the whole wait.
//
// Each request brings the limits saved when it started, and they replace
// the counts under the lock when it asks for a slot, so a new limit applies
// from the next request on. Lowering one never stops a transfer that holds
// a slot: requests wait until enough transfers have ended.
type admission struct {
	mu            sync.Mutex
	perRepository int
	extra         int
	packSlots     int
	packing       int
	active        int
	byRepository  map[string]int
	// changed is closed and replaced whenever a slot is released or the
	// limits change.
	changed chan struct{}
}

func newAdmission() *admission {
	return &admission{byRepository: make(map[string]int), changed: make(chan struct{})}
}

// admits reports whether repositoryID may start a transfer now. The caller
// holds mu.
func (a *admission) admits(repositoryID string, packs bool) bool {
	running := a.byRepository[repositoryID]
	if running >= a.perRepository || (packs && a.packSlots > 0 && a.packing >= a.packSlots) {
		return false
	}
	return a.active < a.perRepository || (running == 0 && a.active < a.perRepository+a.extra)
}

// wakeLocked wakes every waiting request to look again. The caller holds
// mu.
func (a *admission) wakeLocked() {
	close(a.changed)
	a.changed = make(chan struct{})
}

// acquire waits for a slot for repositoryID under limits. It returns
// errBusy after the queue wait, or the context error, and otherwise the
// function that releases the slot.
func (a *admission) acquire(ctx context.Context, repositoryID string, limits Limits) (func(), error) {
	return a.acquireFor(ctx, repositoryID, limits, false)
}

// acquirePacking is acquire for a request that builds a pack.
func (a *admission) acquirePacking(ctx context.Context, repositoryID string, limits Limits) (func(), error) {
	return a.acquireFor(ctx, repositoryID, limits, true)
}

func (a *admission) acquireFor(ctx context.Context, repositoryID string, limits Limits, packs bool) (func(), error) {
	timer := time.NewTimer(limits.QueueWait)
	defer timer.Stop()
	a.mu.Lock()
	if a.perRepository != limits.PerRepository || a.extra != limits.ExtraSlots || a.packSlots != limits.PackSlots {
		a.perRepository, a.extra, a.packSlots = limits.PerRepository, limits.ExtraSlots, limits.PackSlots
		a.wakeLocked()
	}
	a.mu.Unlock()
	for {
		a.mu.Lock()
		if a.admits(repositoryID, packs) {
			a.active++
			a.byRepository[repositoryID]++
			if packs {
				a.packing++
			}
			a.mu.Unlock()
			return func() { a.release(repositoryID, packs) }, nil
		}
		changed := a.changed
		a.mu.Unlock()
		select {
		case <-changed:
		case <-timer.C:
			return nil, errBusy
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

func (a *admission) release(repositoryID string, packs bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.active--
	if packs {
		a.packing--
	}
	a.byRepository[repositoryID]--
	if a.byRepository[repositoryID] == 0 {
		delete(a.byRepository, repositoryID)
	}
	a.wakeLocked()
}
