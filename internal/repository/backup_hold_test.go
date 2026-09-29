package repository

import (
	"context"
	"errors"
	"testing"
)

// While a backup holds a repository, deleting it is refused as busy, with
// the backup named, and maintenance leaves its object files alone. Both
// work again once the hold ends.
func TestBackupHoldRefusesDeletionAndMaintenance(t *testing.T) {
	ctx := context.Background()
	fixture := newMaintenanceFixture(t)
	manager := fixture.manager
	hold, err := manager.HoldForBackup()
	noErr(t, err, "hold")
	if _, err := manager.HoldForBackup(); !errors.Is(err, ErrBackupRunning) {
		t.Fatalf("second backup err=%v", err)
	}
	hold.Add("sample")

	before := inventory(t, fixture.remote)
	steps, err := manager.maintain(ctx, "sample", MaintenanceFull, MaintenanceSchedule{}.withDefaults())
	if !errors.Is(err, errMaintenanceBusy) || steps != 0 {
		t.Fatalf("maintenance of a held repository steps=%d err=%v", steps, err)
	}
	assertSameInventory(t, before, inventory(t, fixture.remote))
	if !manager.Locks.For("sample").TryLock() {
		t.Fatal("refused maintenance kept the lock")
	}
	manager.Locks.For("sample").Unlock()

	if _, err := manager.Delete(ctx, "sample", DeleteFiles); !errors.Is(err, ErrBackupReading) || !errors.Is(err, ErrRepositoryBusy) {
		t.Fatalf("deletion of a held repository err=%v", err)
	}
	if _, _, exists, err := manager.ExistingPath(ctx, "sample"); err != nil || !exists {
		t.Fatalf("refused deletion changed the repository: exists=%v err=%v", exists, err)
	}

	hold.Release("sample")
	if _, err := manager.maintain(ctx, "sample", MaintenanceSmall, MaintenanceSchedule{}.withDefaults()); err != nil {
		t.Fatalf("maintenance after the hold: %v", err)
	}
	hold.Close()
	hold.Close()
	if _, err := manager.Delete(ctx, "sample", DeleteFiles); err != nil {
		t.Fatalf("deletion after the backup: %v", err)
	}
	if _, err := manager.HoldForBackup(); err != nil {
		t.Fatalf("a new backup after the first ended: %v", err)
	}
}
