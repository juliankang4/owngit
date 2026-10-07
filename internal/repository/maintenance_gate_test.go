package repository

// A transfer holds a gate slot and waits for the repository lock, so
// maintenance must not wait for the gate while it holds that lock:
// maintenance holds the repository write lock while it waits for the memory
// gate, and a transfer of the same repository holds the gate slot while it
// waits for that lock, in the order githttp's handler takes them (admission
// with the gate, then the repository lock).

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"owngit/internal/hostmem"
)

func TestMaintenanceRecordRefusesAReplacedDirectory(t *testing.T) {
	manager, path, _ := newTestRepository(t)
	ctx := context.Background()
	manager.recordMaintenance(ctx, "sample", time.Now())
	_, err := os.Stat(maintenanceRecordPath(path))
	noErr(t, err)
	noErr(t, os.Rename(path, path+"-original"))
	noErr(t, os.Mkdir(path, 0o700))
	var logged string
	manager.maintenance.logf = func(format string, args ...any) { logged = fmt.Sprintf(format, args...) }
	manager.recordMaintenance(ctx, "sample", time.Now())
	if _, err := os.Lstat(maintenanceRecordPath(path)); !os.IsNotExist(err) {
		t.Fatalf("replacement received a maintenance record: %v", err)
	}
	if !strings.Contains(logged, ErrStorageChanged.Error()) {
		t.Fatalf("maintenance refusal was not reported: %q", logged)
	}
}

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
		_, err := manager.maintain(context.Background(), "sample", MaintenanceFull, MaintenanceSchedule{}.withDefaults(), false)
		done <- err
	}()
	// The transfer holds the only gate slot and waits for the write lock of
	// the running step. It gets that lock only once the step leaves it, so it
	// finishes while the run is still in flight: a run that waited for the
	// gate under the lock would hold it for the whole gate budget and hit
	// this bound instead. A maintenance that stops for the gate lets it in.
	select {
	case <-transferDone:
	case <-time.After(10 * time.Second):
		t.Fatalf("after 10 s the transfer (gate slot held, waiting for the write lock) has not finished: lock waiting=%v", lock.Waiting())
	}
	select {
	case err := <-done:
		t.Logf("maintenance finished: %v", err)
		if !errors.Is(err, errMaintenanceBusy) {
			t.Errorf("maintenance error = %v, want errMaintenanceBusy", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("after 10 s the maintenance that met the full gate has not returned")
	}
	if lock.TryLock() {
		lock.UnlockWithoutRefChanges()
	} else {
		t.Error("the repository lock is still held")
	}
}

// Maintenance that keeps finding every slot of the memory gate in use stops
// only trying it: after a few pauses the next run waits for a free slot with
// no repository lock held, and that run finishes once a slot is free.
func TestMaintenanceWaitsForTheGateAfterRepeatedPauses(t *testing.T) {
	fixture := newMaintenanceFixture(t)
	manager := fixture.manager
	gate := hostmem.NewGate(1)
	hostmem.Shared.Store(gate)
	t.Cleanup(func() { hostmem.Shared.Store(nil) })
	held, _ := gate.TryAcquire()
	if held == nil {
		t.Fatal("the gate is full before the test")
	}
	var freeOnce sync.Once
	free := func() { freeOnce.Do(held) }
	defer free()

	log := startMaintenanceForTest(t, manager, MaintenanceSchedule{
		Idle: time.Millisecond, Retry: time.Millisecond, CommandTimeout: 30 * time.Second, Now: shiftedClock(12),
	})
	defer func() {
		if t.Failed() {
			t.Logf("maintenance log: %v", log.matching(""))
		}
	}()
	manager.NoteRepositoryWrite("sample")
	waitFor(t, "the gate pauses", func() bool { return len(log.matching("paused")) >= maintenanceGatePauses })
	// The next run stays in flight: it waits for a free slot instead of
	// returning and pausing again.
	waitFor(t, "a run in flight", func() bool { return manager.maintenanceRunning("sample") })
	pauses := len(log.matching("paused"))
	if becameTrueWithin(time.Second, func() bool { return len(log.matching("paused")) > pauses }) {
		t.Fatalf("maintenance kept pausing instead of waiting: %v", log.matching("paused"))
	}
	// Waiting holds no repository lock, so a reader still gets it.
	lock := manager.Locks.For("sample")
	read := make(chan struct{})
	go func() {
		lock.RLock()
		lock.RUnlock()
		close(read)
	}()
	select {
	case <-read:
	case <-time.After(5 * time.Second):
		t.Fatal("a reader could not take the repository while maintenance waited for the gate")
	}
	// A free slot lets the waiting run finish, without another pause.
	free()
	waitFor(t, "maintenance after the gate freed", func() bool { return len(log.matching(`"sample" maintenance (small) completed`)) == 1 })
	if got := len(log.matching("paused")); got != maintenanceGatePauses {
		t.Fatalf("maintenance paused %d times, want %d", got, maintenanceGatePauses)
	}
}
