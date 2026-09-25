package state

import (
	"context"
	"errors"
	"testing"
)

// A serve process that takes the running-record lock removes a record left
// by a run that ended without cleanup, so readers see "starting" until it
// publishes its own record, never the dead run's values.
func TestClaimRunningNetworkClearsAStaleRecord(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)
	noErr(t, store.PublishRunningNetwork(ctx, RunningNetwork{PID: 1, Listen: "192.0.2.1:7654", ListenSource: "saved"}))

	observed, err := store.ObserveRunningNetwork(ctx)
	noErr(t, err)
	if observed.Server != ServerNotRunning || observed.Record != nil || !observed.StaleRecord {
		t.Fatalf("stale record without a holder: %+v", observed)
	}

	release, err := store.ClaimRunningNetwork(ctx)
	noErr(t, err)
	observed, err = store.ObserveRunningNetwork(ctx)
	noErr(t, err)
	if observed.Server != ServerStarting || observed.Record != nil || observed.StaleRecord {
		release()
		t.Fatalf("after the claim the dead run's record is still reported: %+v", observed)
	}
	if _, published, err := store.RunningNetwork(ctx); err != nil || published {
		release()
		t.Fatalf("the claim left the stale record in storage: published=%v err=%v", published, err)
	}
	own, err := store.OwnRunningNetwork(ctx, true)
	noErr(t, err)
	if own.Server != ServerStarting {
		release()
		t.Fatalf("own view before publishing: %+v", own)
	}

	noErr(t, store.PublishRunningNetwork(ctx, RunningNetwork{PID: 2, Listen: "127.0.0.1:7654", ListenSource: "default"}))
	observed, err = store.ObserveRunningNetwork(ctx)
	noErr(t, err)
	if observed.Server != ServerRunning || observed.Record == nil || observed.Record.PID != 2 {
		release()
		t.Fatalf("published record of the live holder: %+v", observed)
	}
	// A second serve on the same directory cannot claim the lock.
	if second, err := store.ClaimRunningNetwork(ctx); !errors.Is(err, ErrInstanceRunning) {
		if second != nil {
			second()
		}
		release()
		t.Fatalf("second claim err=%v", err)
	}
	release()
	observed, err = store.ObserveRunningNetwork(ctx)
	noErr(t, err)
	if observed.Server != ServerNotRunning || observed.Record != nil || !observed.StaleRecord {
		t.Fatalf("after the holder is gone: %+v", observed)
	}
}

// The serve process itself trusts the stored record only when it holds the
// running-record lock; otherwise the record is another run's leftover.
func TestOwnRunningNetworkTrustsTheRecordOnlyWhenClaimed(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)
	noErr(t, store.PublishRunningNetwork(ctx, RunningNetwork{PID: 3, Listen: "127.0.0.1:7654"}))
	own, err := store.OwnRunningNetwork(ctx, false)
	noErr(t, err)
	if own.Server != ServerUnknown || own.Record != nil || !own.StaleRecord {
		t.Fatalf("unclaimed: %+v", own)
	}
	own, err = store.OwnRunningNetwork(ctx, true)
	noErr(t, err)
	if own.Server != ServerRunning || own.Record == nil || own.Record.PID != 3 {
		t.Fatalf("claimed: %+v", own)
	}
}
