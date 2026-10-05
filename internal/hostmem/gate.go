package hostmem

import (
	"context"
	"sync"
	"sync/atomic"
)

// Gate counts the Git processes that use memory at once: transfers, a backup
// and maintenance. A background job (Acquire) is served before any transfer
// (TryAcquire), so it never starves behind a busy period of transfers.
type Gate struct {
	mu      sync.Mutex
	limit   int
	used    int
	waiting int
	changed chan struct{}
}

// Shared is the gate of this process, or nil when the ceiling is unknown.
var Shared atomic.Pointer[Gate]

// NewGate returns a gate that admits limit holders at once.
func NewGate(limit int) *Gate { return &Gate{limit: max(limit, 1), changed: make(chan struct{})} }

// TryAcquire takes a slot for a transfer if one is free and no background job
// waits. changed is closed when that may have become different.
func (g *Gate) TryAcquire() (release func(), changed <-chan struct{}) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.used < g.limit && g.waiting == 0 {
		g.used++
		return g.release, nil
	}
	return nil, g.changed
}

// TryAcquireBackground takes a slot for a background job that holds a
// repository lock and so must not wait, or returns nil.
func (g *Gate) TryAcquireBackground() func() {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.used >= g.limit {
		return nil
	}
	g.used++
	return g.release
}

// Acquire waits for a slot for a background job, or for ctx to end.
func (g *Gate) Acquire(ctx context.Context) (func(), error) {
	g.mu.Lock()
	g.waiting++
	for g.used >= g.limit {
		changed := g.changed
		g.mu.Unlock()
		select {
		case <-changed:
		case <-ctx.Done():
			g.mu.Lock()
			g.waiting--
			g.wake()
			g.mu.Unlock()
			return nil, ctx.Err()
		}
		g.mu.Lock()
	}
	g.waiting--
	g.used++
	if g.used < g.limit {
		g.wake() // another waiter may fit as well
	}
	g.mu.Unlock()
	return g.release, nil
}

func (g *Gate) release() {
	g.mu.Lock()
	g.used--
	g.wake()
	g.mu.Unlock()
}

func (g *Gate) wake() {
	close(g.changed)
	g.changed = make(chan struct{})
}
