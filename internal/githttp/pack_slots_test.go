package githttp

import (
	"context"
	"errors"
	"testing"
	"time"
)

// With a pack limit, a further clone waits for the one running, while a push
// or a ref advertisement finds a slot at once; a clone that waits past the
// queue wait is refused as busy and holds nothing.
func TestPackSlotsHoldBackOnlyRequestsThatBuildAPack(t *testing.T) {
	slots := newAdmission()
	ctx := context.Background()
	limits := func(wait time.Duration) Limits {
		return Limits{PerRepository: 4, ExtraSlots: 1, PackSlots: 1, QueueWait: wait}
	}
	clone, err := slots.acquirePacking(ctx, "a", limits(0))
	noErr(t, err, "first clone")

	for range 2 {
		release, err := slots.acquire(ctx, "a", limits(0))
		noErr(t, err, "a push or ref advertisement behind a running clone")
		defer release()
	}
	if _, err := slots.acquirePacking(ctx, "a", limits(50*time.Millisecond)); !errors.Is(err, errBusy) {
		t.Fatalf("second clone: %v, want busy", err)
	}
	if _, err := slots.acquirePacking(ctx, "other", limits(50*time.Millisecond)); !errors.Is(err, errBusy) {
		t.Fatalf("clone of another repository: %v, want busy", err)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := slots.acquirePacking(cancelled, "a", limits(time.Minute)); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled clone: %v, want cancelled", err)
	}
	slots.mu.Lock()
	packing, active := slots.packing, slots.active
	slots.mu.Unlock()
	if packing != 1 || active != 3 {
		t.Fatalf("after refused waiters: %d packing, %d active; want 1 and 3 (only the clone and two requests hold slots)", packing, active)
	}

	got := make(chan error, 1)
	go func() {
		release, err := slots.acquirePacking(ctx, "a", limits(5*time.Second))
		if err == nil {
			release()
		}
		got <- err
	}()
	clone()
	noErr(t, <-got, "a waiting clone did not get the released pack slot")
}

// Without a pack limit, requests that build a pack are limited only by the
// transfer slots, as before.
func TestNoPackSlotsLeavesTransferSlotsAlone(t *testing.T) {
	slots := newAdmission()
	ctx := context.Background()
	limits := Limits{PerRepository: 4, ExtraSlots: 0}
	for range 4 {
		release, err := slots.acquirePacking(ctx, "a", limits)
		noErr(t, err, "clone within the transfer slots")
		defer release()
	}
	if _, err := slots.acquirePacking(ctx, "a", limits); !errors.Is(err, errBusy) {
		t.Fatalf("fifth clone of one repository: %v, want busy", err)
	}
}
