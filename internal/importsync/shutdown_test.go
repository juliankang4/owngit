package importsync

import (
	"context"
	"strings"
	"testing"
	"time"

	"owngit/internal/state"
)

// Shutdown cancels an in-flight run, waits until it has recorded its final
// state, releases the lease, and refuses later admission.
func TestShutdownDrainsInFlightRunBeforeReturning(t *testing.T) {
	f := newFixture(t)
	f.commit("one", "one\n")
	f.mustImport(ImportInput{})
	f.commit("two", "two\n")
	f.transport.gate = make(chan struct{})
	entered := make(chan struct{})
	f.transport.before = func() { close(entered) }
	finished := make(chan error, 1)
	go func() {
		_, err := f.service.Refresh(context.Background(), "project", Limits{})
		finished <- err
	}()
	select {
	case <-entered:
	case <-time.After(20 * time.Second):
		t.Fatal("refresh did not reach the source")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := f.service.Shutdown(ctx); err != nil {
		t.Fatalf("shutdown: %v", err)
	}
	// Shutdown returned, so the run is already terminal in durable state.
	run := f.lastRun()
	if run.Status != state.ImportRunCancelled || !strings.Contains(run.Message, ErrShuttingDown.Error()) {
		t.Fatalf("drained run status=%s message=%q", run.Status, run.Message)
	}
	if active, exists, err := f.store.ActiveImportRun(context.Background(), "project"); err != nil || exists {
		t.Fatalf("active run after shutdown=%+v err=%v", active, err)
	}
	if err := <-finished; problemCode(err) != CodeCancelled {
		t.Fatalf("drained refresh err=%v", err)
	}
	if availability := f.service.Availability(context.Background()); availability.Prepared {
		t.Fatal("shutdown kept the runtime lease")
	}
	if _, err := f.refresh(); problemCode(err) != CodeRuntimeUnavailable {
		t.Fatalf("refresh after shutdown err=%v", err)
	}
}
