package repository

// A transfer holds a gate slot and waits for the repository lock, so maintenance must not wait for the gate while it holds that lock: maintenance holds the
// repository write lock while it waits for the memory gate, and a transfer
// of the same repository holds the gate slot while it waits for that lock,
// in the order githttp's handler takes them (admission with the gate, then
// the repository lock).

import (
	"context"
	"errors"
	"testing"
	"time"

	"owngit/internal/hostmem"
)

func TestMaintenanceDoesNotWaitForTheGateUnderTheRepositoryLock(t *testing.T) {
	fixture := newMaintenanceFixture(t)
	manager := fixture.manager
	gate := hostmem.NewGate(1)
	hostmem.Shared.Store(gate)
	defer hostmem.Shared.Store(nil)
	lock := manager.Locks.For("sample")

	transferHolds := make(chan struct{})
	transferDone := make(chan struct{})
	manager.maintenanceHook = func(ctx context.Context, id string, args []string) error {
		if args[0] != "repack" {
			return nil
		}
		// A clone of the same repository arrives now: it passes admission,
		// which takes the gate slot, then waits for the read lock.
		go func() {
			release, _ := gate.TryAcquire()
			if release == nil {
				t.Error("the transfer found the gate full")
				close(transferHolds)
				return
			}
			close(transferHolds)
			lock.RLock()
			lock.RUnlock()
			release()
			close(transferDone)
		}()
		<-transferHolds
		return nil
	}
	done := make(chan error, 1)
	go func() {
		_, err := manager.maintain(context.Background(), "sample", MaintenanceFull, MaintenanceSchedule{}.withDefaults())
		done <- err
	}()
	select {
	case err := <-done:
		t.Logf("maintenance finished: %v", err)
		select {
		case <-transferDone:
			t.Log("transfer finished after the maintenance left")
		case <-time.After(10 * time.Second):
			t.Error("maintenance left but the transfer did not finish")
		}
		if !errors.Is(err, errMaintenanceBusy) {
			t.Errorf("maintenance error = %v, want errMaintenanceBusy", err)
		}
		if lock.TryLock() {
			lock.UnlockWithoutRefChanges()
		} else {
			t.Error("the repository lock is still held")
		}
	case <-transferDone:
		t.Error("transfer finished while maintenance still runs")
	case <-time.After(10 * time.Second):
		t.Errorf("after 10 s neither the maintenance (write lock held, waiting for the gate) nor the transfer (gate slot held, waiting for the lock) has moved: lock waiting=%v", lock.Waiting())
	}
}
