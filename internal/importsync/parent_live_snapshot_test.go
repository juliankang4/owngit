package importsync

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"owngit/internal/importfetch"
)

// A run may enter after the liveness snapshot but before the database update.
// The clock seam deterministically models that ordinary scheduling boundary.
func TestParentReconcileCannotInterruptRunAfterLiveSnapshot(t *testing.T) {
	f := newFixture(t)
	f.commit("synthetic initial", "one\n")
	f.mustImport(ImportInput{})
	clockReached := make(chan struct{})
	continueReconcile := make(chan struct{})
	fetchStarted := make(chan struct{})
	reconcileStopped := make(chan struct{})
	runStopped := make(chan struct{})
	reconcileError := make(chan error, 1)
	ctx, cancel := context.WithCancel(context.Background())
	var first atomic.Bool
	var release sync.Once
	f.service.Clock = func() time.Time {
		if first.CompareAndSwap(false, true) {
			close(clockReached)
			<-continueReconcile
		}
		return f.now
	}
	f.service.Fetch = func(ctx context.Context, _ importfetch.Request, _ importfetch.PackConsumer) (*importfetch.Result, error) {
		close(fetchStarted)
		<-ctx.Done()
		return nil, ctx.Err()
	}
	t.Cleanup(func() {
		release.Do(func() { close(continueReconcile) })
		cancel()
		for _, stopped := range []<-chan struct{}{reconcileStopped, runStopped} {
			select {
			case <-stopped:
			case <-time.After(5 * time.Second):
				t.Error("synthetic operation did not stop during cleanup")
			}
		}
	})
	go func() {
		defer close(reconcileStopped)
		reconcileError <- f.service.Reconcile(ctx)
	}()
	go func() {
		defer close(runStopped)
		select {
		case <-clockReached:
			_, _ = f.service.Refresh(ctx, "project", Limits{})
		case <-ctx.Done():
		}
	}()
	select {
	case <-fetchStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("new run did not reach the controlled fetch boundary")
	}
	ids := f.service.liveRunIDs()
	if len(ids) != 1 {
		t.Fatalf("expected one live run, got %d", len(ids))
	}
	before, exists, err := f.store.ImportRun(ctx, ids[0])
	if err != nil || !exists || terminalImportRun(before.Status) {
		t.Fatalf("precondition: live run status=%q exists=%v error=%v", before.Status, exists, err)
	}
	release.Do(func() { close(continueReconcile) })
	select {
	case err := <-reconcileError:
		if err != nil {
			t.Fatalf("reconciliation: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("reconciliation did not finish")
	}
	after, exists, err := f.store.ImportRun(ctx, ids[0])
	if err != nil || !exists || after.Status != before.Status {
		t.Fatalf("reconciliation changed a live run: before=%q after=%q exists=%v error=%v", before.Status, after.Status, exists, err)
	}
}
