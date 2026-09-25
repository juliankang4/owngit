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
// perRepository transfers, and the server runs at most one more than that.
// The extra slot is only for a repository with no transfer running, so one
// busy repository never blocks the others, while a single repository keeps
// all perRepository slots. A request waits for a slot at most for the queue
// wait.
type admission struct {
	mu            sync.Mutex
	perRepository int
	active        int
	byRepository  map[string]int
	// changed is closed and replaced whenever a slot is released.
	changed chan struct{}
}

func newAdmission(perRepository int) *admission {
	return &admission{perRepository: perRepository, byRepository: make(map[string]int), changed: make(chan struct{})}
}

// admits reports whether repositoryID may start a transfer now. The caller
// holds mu.
func (a *admission) admits(repositoryID string) bool {
	running := a.byRepository[repositoryID]
	if running >= a.perRepository {
		return false
	}
	return a.active < a.perRepository || (running == 0 && a.active == a.perRepository)
}

// acquire waits for a slot for repositoryID. It returns errBusy after wait,
// or the context error, and otherwise the function that releases the slot.
func (a *admission) acquire(ctx context.Context, repositoryID string, wait time.Duration) (func(), error) {
	timer := time.NewTimer(wait)
	defer timer.Stop()
	for {
		a.mu.Lock()
		if a.admits(repositoryID) {
			a.active++
			a.byRepository[repositoryID]++
			a.mu.Unlock()
			return func() { a.release(repositoryID) }, nil
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

func (a *admission) release(repositoryID string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.active--
	a.byRepository[repositoryID]--
	if a.byRepository[repositoryID] == 0 {
		delete(a.byRepository, repositoryID)
	}
	close(a.changed)
	a.changed = make(chan struct{})
}
