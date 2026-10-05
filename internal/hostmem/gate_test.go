package hostmem

import (
	"context"
	"testing"
	"time"
)

// A background job waits for a slot, is served before a later transfer, and a
// cancelled waiter takes nothing.
func TestGateServesBackgroundWorkFirstAndCancelHoldsNothing(t *testing.T) {
	gate := NewGate(1)
	first, _ := gate.TryAcquire()
	if first == nil {
		t.Fatal("first transfer was refused")
	}
	if release, changed := gate.TryAcquire(); release != nil || changed == nil {
		t.Fatal("a second transfer got a slot of a full gate")
	}
	cancelled, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := gate.Acquire(cancelled); err == nil {
		t.Fatal("a background job got a slot of a full gate")
	}
	got := make(chan func(), 1)
	go func() {
		release, _ := gate.Acquire(context.Background())
		got <- release
	}()
	for {
		gate.mu.Lock()
		waiting := gate.waiting
		gate.mu.Unlock()
		if waiting == 1 {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if release, _ := gate.TryAcquire(); release != nil {
		t.Fatal("a transfer passed a waiting background job")
	}
	first()
	select {
	case release := <-got:
		release()
	case <-time.After(2 * time.Second):
		t.Fatal("the waiting background job did not get the released slot")
	}
	if release, _ := gate.TryAcquire(); release == nil {
		t.Fatal("the gate leaked a slot")
	}
}

// A waiter that takes a slot wakes the others when slots remain, so no
// waiter sleeps while a slot is free.
func TestGateWakesTheNextWaiterWhileSlotsRemain(t *testing.T) {
	gate := NewGate(2)
	a, _ := gate.TryAcquire()
	b, _ := gate.TryAcquire()
	got := make(chan func(), 2)
	for range 2 {
		go func() {
			release, _ := gate.Acquire(context.Background())
			got <- release
		}()
	}
	for {
		gate.mu.Lock()
		waiting := gate.waiting
		gate.mu.Unlock()
		if waiting == 2 {
			break
		}
		time.Sleep(time.Millisecond)
	}
	a()
	b() // two slots free at once: both waiters must start
	for range 2 {
		select {
		case release := <-got:
			release()
		case <-time.After(2 * time.Second):
			t.Fatal("a waiter slept while a slot was free")
		}
	}
}
