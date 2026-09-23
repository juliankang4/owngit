package importsync

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"owngit/internal/state"
)

// gatedRefresh starts one refresh that stops inside the transport and returns
// after the caller releases the gate. It reports when Fetch was reached and
// when the run returned.
func (f *fixture) gatedRefresh(t *testing.T) (fetchStarted, runDone chan struct{}, run *state.ImportRun, runErr *error) {
	t.Helper()
	f.transport.gate = make(chan struct{}, 1)
	fetchStarted = make(chan struct{})
	f.transport.before = func() { close(fetchStarted) }
	run = new(state.ImportRun)
	runErr = new(error)
	runDone = make(chan struct{})
	go func() {
		defer close(runDone)
		*run, *runErr = f.refresh()
	}()
	return fetchStarted, runDone, run, runErr
}

// A run is admitted under the same barrier that reconciliation uses to snapshot
// live runs and interrupt abandoned ones, so no interleaving can make an active
// run look abandoned. The transport is outside that barrier, so reconciliation
// still completes while a run waits in Fetch.
func TestAdmissionAndReconcileShareOneBarrier(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.commit("one", "one\n")
	f.mustImport(ImportInput{})

	// Hold the barrier directly before anything can be admitted: admission and
	// reconciliation must both wait, and neither may have a durable effect yet.
	f.service.lifecycle.Lock()
	fetchStarted, runDone, refreshRun, refreshErr := f.gatedRefresh(t)
	reconcileErr := make(chan error, 1)
	reconcileDone := make(chan struct{})
	go func() {
		defer close(reconcileDone)
		reconcileErr <- f.service.Reconcile(ctx)
	}()
	if ids := f.service.liveRunIDs(); len(ids) != 0 {
		t.Fatalf("run registered while the barrier was held: %v", ids)
	}
	if _, exists, err := f.store.ActiveImportRun(ctx, "project"); err != nil || exists {
		t.Fatalf("run row exists while the barrier is held: exists=%v err=%v", exists, err)
	}
	f.service.lifecycle.Unlock()

	select {
	case <-fetchStarted:
	case <-runDone:
		t.Fatalf("refresh ended before the transport: %v", *refreshErr)
	}
	<-reconcileDone
	noErr(t, <-reconcileErr, "reconciliation")
	active, exists, err := f.store.ActiveImportRun(ctx, "project")
	if err != nil || !exists {
		t.Fatalf("live run missing after reconciliation: exists=%v err=%v", exists, err)
	}
	staging := filepath.Join(f.service.stagingRootPath(), active.StagingName)
	if _, err := os.Stat(staging); err != nil {
		t.Fatalf("live staging was removed: %v", err)
	}

	// Fetch is outside the barrier, so a second reconciliation does not wait.
	second := make(chan error, 1)
	go func() { second <- f.service.Reconcile(ctx) }()
	select {
	case err := <-second:
		noErr(t, err, "reconciliation during fetch")
	case <-runDone:
		t.Fatalf("refresh ended before the second reconciliation: %v", *refreshErr)
	}
	again, _, err := f.store.ImportRun(ctx, active.ID)
	if err != nil || again.Status != active.Status || again.Status == state.ImportRunInterrupted {
		t.Fatalf("live run status changed: %q then %q error=%v", active.Status, again.Status, err)
	}

	f.transport.gate <- struct{}{}
	<-runDone
	if *refreshErr != nil {
		t.Fatalf("refresh: %v", *refreshErr)
	}
	if refreshRun.Status != state.ImportRunComplete {
		t.Fatalf("refresh status=%q", refreshRun.Status)
	}
	if _, err := os.Stat(staging); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("finished staging survived: %v", err)
	}
}

// Releasing the lease while a run is still active would let another process
// reconcile that run. Close refuses, keeps the lease and the root identity, and
// succeeds once the run finished.
func TestCloseRefusesWhileRunActiveAndKeepsOwnership(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.commit("one", "one\n")
	f.mustImport(ImportInput{})
	rootID := f.prepareRuntime(t)
	fetchStarted, runDone, refreshRun, refreshErr := f.gatedRefresh(t)
	select {
	case <-fetchStarted:
	case <-runDone:
		t.Fatalf("refresh ended before the transport: %v", *refreshErr)
	}

	if err := f.service.Close(); !errors.Is(err, ErrRuntimeActive) {
		t.Fatalf("close while a run is active: err=%v", err)
	}
	if _, ok := f.service.preparedRuntime(); !ok {
		t.Fatal("close released the lease while a run was active")
	}
	second := &Service{Store: f.store, Repositories: f.manager}
	defer func() { _ = second.Close() }()
	if _, err := second.Prepare(ctx); !errors.Is(err, ErrRuntimeHeld) {
		t.Fatalf("second owner while the first still runs: err=%v", err)
	}
	if ids := f.service.liveRunIDs(); len(ids) != 1 {
		t.Fatalf("live runs=%v", ids)
	}
	if _, exists, err := f.store.ActiveImportRun(ctx, "project"); err != nil || !exists {
		t.Fatalf("live run missing: exists=%v err=%v", exists, err)
	}

	f.transport.gate <- struct{}{}
	<-runDone
	if *refreshErr != nil {
		t.Fatalf("refresh: %v", *refreshErr)
	}
	if refreshRun.Status != state.ImportRunComplete {
		t.Fatalf("refresh status=%q", refreshRun.Status)
	}
	noErr(t, f.service.Close(), "close after the run finished")
	info, err := second.Prepare(ctx)
	noErr(t, err, "second prepare after release")
	if info.RootID != rootID {
		t.Fatalf("root identity changed: %s then %s", rootID, info.RootID)
	}
	if ids := second.liveRunIDs(); len(ids) != 0 {
		t.Fatalf("second owner inherited live runs: %v", ids)
	}
}

// A run admitted while reconciliation sits between its liveness snapshot and
// its interruption update must not be interrupted. The barrier must cover both
// halves, so the admission either is in the snapshot or has no durable row yet.
//
// The test also works as a negative control. If the write side is dropped
// around that window, TryRLock succeeds and the admission is allowed to commit
// before reconciliation continues, so the stale update interrupts live work and
// the final assertion fails. There are no sleeps: the branch is decided by
// whether the write side is actually held, not by scheduling.
func TestReconcileSnapshotAndInterruptShareTheBarrier(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.commit("one", "one\n")
	f.mustImport(ImportInput{})

	f.transport.gate = make(chan struct{}, 1)
	fetchStarted := make(chan struct{})
	f.transport.before = func() { close(fetchStarted) }

	snapshotTaken := make(chan struct{})
	releaseSnapshot := make(chan struct{})
	admissionAtClock := make(chan struct{})
	releaseAdmission := make(chan struct{})
	var admissionOnce sync.Once
	var clockCalls atomic.Int64
	f.service.Clock = func() time.Time {
		// The first call is reconciliation's, before the snapshot. The second is
		// the admission's, before its lifecycle lock attempt.
		if clockCalls.Add(1) == 2 {
			admissionOnce.Do(func() {
				close(admissionAtClock)
				<-releaseAdmission
			})
		}
		return f.now
	}
	f.service.afterLiveSnapshot = func() {
		close(snapshotTaken)
		<-releaseSnapshot
	}

	reconcileErr := make(chan error, 1)
	go func() { reconcileErr <- f.service.Reconcile(ctx) }()
	<-snapshotTaken

	runDone := make(chan struct{})
	runErr := new(error)
	go func() {
		defer close(runDone)
		_, *runErr = f.refresh()
	}()
	<-admissionAtClock
	close(releaseAdmission)

	if f.service.lifecycle.TryRLock() {
		// No write side is held: the admission can commit before the update.
		// Wait for that commit so the stale snapshot is guaranteed to miss it.
		f.service.lifecycle.RUnlock()
		select {
		case <-fetchStarted:
		case <-runDone:
			t.Fatalf("admission ended before the transport: %v", *runErr)
		}
	}
	close(releaseSnapshot)
	noErr(t, <-reconcileErr, "reconciliation")
	select {
	case <-fetchStarted:
	case <-runDone:
		t.Fatalf("admission ended before the transport: %v", *runErr)
	}
	liveIDs := f.service.liveRunIDs()
	if len(liveIDs) != 1 {
		t.Fatalf("live runs=%v", liveIDs)
	}
	live, exists, err := f.store.ImportRun(ctx, liveIDs[0])
	if err != nil || !exists {
		t.Fatalf("live run exists=%v err=%v", exists, err)
	}
	if live.Status == state.ImportRunInterrupted {
		t.Fatalf("reconciliation interrupted a run admitted inside its window: %+v", live)
	}

	f.transport.gate <- struct{}{}
	<-runDone
	if *runErr != nil {
		t.Fatalf("refresh: %v", *runErr)
	}
}

// A reconciliation keeps its starting generation. If marker loss is detected
// inside the snapshot window, restoring the same path cannot let the operation
// interrupt an abandoned row or continue under rebound authority.
func TestReconcileLatchesRestoredMarkerLossBeforeMutation(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.commit("one", "one\n")
	f.mustImport(ImportInput{})
	rootID := f.prepareRuntime(t)
	runID := strings.Repeat("5", 32)
	abandoned := state.ImportRun{
		ID: runID, RepositoryID: "project", SourceGeneration: 1, AuthorityRevision: 1, Kind: state.ImportKindRefresh,
		Status: state.ImportRunPreparing, StartedAt: f.now, CreatedAt: f.now,
	}
	noErr(t, f.store.BeginImportRun(ctx, abandoned))
	var stimulusErr, restoreErr error
	var lossReported bool
	f.service.afterLiveSnapshot = func() {
		var restore func() error
		restore, stimulusErr = induceRuntimeMarkerMismatch(f.service, ".operation-preserved")
		if stimulusErr != nil {
			return
		}
		availability := f.service.Availability(ctx)
		lossReported = !availability.Available && availability.Code == CodeRuntimeUnavailable
		restoreErr = restore()
	}
	reconcileErr := f.service.Reconcile(ctx)
	noErr(t, stimulusErr, "induce operation marker mismatch")
	noErr(t, restoreErr, "restore operation marker")
	if !lossReported {
		t.Fatal("operation did not report runtime ownership loss")
	}
	if !errors.Is(reconcileErr, ErrRuntimeLost) {
		t.Fatalf("reconcile after restored marker: %v", reconcileErr)
	}
	stored, exists, err := f.store.ImportRun(ctx, runID)
	if err != nil || !exists || stored.Status != state.ImportRunPreparing {
		t.Fatalf("lost reconciliation mutated the run: %+v exists=%v err=%v", stored, exists, err)
	}
	if _, err := f.service.Prepare(ctx); !errors.Is(err, ErrRuntimeLost) {
		t.Fatalf("completed operation cleared ownership loss: %v", err)
	}
	noErr(t, f.service.Close(), "close lost idle runtime")
	info, err := f.service.Prepare(ctx)
	if err != nil || info.RootID != rootID {
		t.Fatalf("explicit close did not recover restored root: info=%+v err=%v", info, err)
	}
}

// Close must keep the lease while reconciliation still scans staging or
// reconciles intents outside the write section, so a second owner cannot
// acquire the root and reconcile the same work concurrently.
func TestCloseRefusesWhileReconciliationInFlight(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.commit("one", "one\n")
	f.mustImport(ImportInput{})
	rootID := f.prepareRuntime(t)

	clockReached := make(chan struct{})
	continueReconcile := make(chan struct{})
	var once sync.Once
	f.service.Clock = func() time.Time {
		once.Do(func() {
			close(clockReached)
			<-continueReconcile
		})
		return f.now
	}
	reconcileErr := make(chan error, 1)
	go func() { reconcileErr <- f.service.Reconcile(ctx) }()
	<-clockReached

	if err := f.service.Close(); !errors.Is(err, ErrRuntimeActive) {
		t.Fatalf("close during reconciliation: err=%v", err)
	}
	second := &Service{Store: f.store, Repositories: f.manager}
	defer func() { _ = second.Close() }()
	if _, err := second.Prepare(ctx); !errors.Is(err, ErrRuntimeHeld) {
		t.Fatalf("second owner during reconciliation: err=%v", err)
	}

	close(continueReconcile)
	noErr(t, <-reconcileErr, "reconciliation")
	noErr(t, f.service.Close(), "close after reconciliation")
	info, err := f.service.Prepare(ctx)
	noErr(t, err, "prepare after close")
	if info.RootID != rootID {
		t.Fatalf("root identity changed: %s then %s", rootID, info.RootID)
	}
}

// A row authorized by another run keeps its identity; the fresh directory is
// discarded instead of being silently adopted under a stale authorization.
func TestAcquireStagingRefusesForeignAuthorizedRow(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.prepareRuntime(t)
	runID := strings.Repeat("8", 32)
	foreignRunID := strings.Repeat("9", 32)
	name := "run-" + runID
	if err := f.store.RegisterImportStaging(ctx, state.ImportStaging{
		Name: name, RepositoryID: "other", RunID: foreignRunID, Token: strings.Repeat("a", 64),
		State: state.ImportStagingActive, CreatedAt: f.now,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.acquireStaging(ctx, runID, "project", f.now); err == nil {
		t.Fatal("foreign authorized row was adopted")
	}
	if _, err := os.Stat(filepath.Join(f.service.stagingRootPath(), name)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("refused staging directory survived: %v", err)
	}
	row, exists, err := f.store.ImportStaging(ctx, name)
	if err != nil || !exists || row.RunID != foreignRunID || row.RepositoryID != "other" || row.State != state.ImportStagingActive {
		t.Fatalf("foreign row changed: %+v exists=%v err=%v", row, exists, err)
	}
}

// Close must keep the lease while reconciliation is past its barrier and the
// staging scan or intent reconciliation is still running. The Logf callback is
// an existing seam after the scan; breaking the deferred operation release
// would let Close succeed and a second owner enter here.
func TestCloseRefusesAfterReconciliationScan(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.commit("one", "one\n")
	f.mustImport(ImportInput{})
	f.prepareRuntime(t)
	unknown := filepath.Join(f.service.stagingRootPath(), "unknown-parent-fixture.txt")
	noErr(t, os.WriteFile(unknown, []byte("preserve"), 0o600))
	reached := make(chan struct{})
	release := make(chan struct{})
	done := make(chan error, 1)
	f.service.Logf = func(format string, _ ...any) {
		if strings.HasPrefix(format, "import staging reconciliation reported") {
			close(reached)
			<-release
		}
	}
	go func() { done <- f.service.Reconcile(ctx) }()
	<-reached
	defer func() {
		close(release)
		if err := <-done; err != nil {
			t.Errorf("reconciliation: %v", err)
		}
	}()
	// This callback is after the scan and outside the snapshot/update barrier.
	if !f.service.lifecycle.TryLock() {
		t.Fatal("post-scan boundary unexpectedly holds the lifecycle barrier")
	}
	f.service.lifecycle.Unlock()
	if err := f.service.Close(); !errors.Is(err, ErrRuntimeActive) {
		t.Errorf("close during post-scan reconciliation: %v", err)
	}
	second := &Service{Store: f.store, Repositories: f.manager}
	defer func() { _ = second.Close() }()
	if _, err := second.Prepare(ctx); !errors.Is(err, ErrRuntimeHeld) {
		t.Errorf("second owner entered during post-scan reconciliation: %v", err)
	}
	content, err := os.ReadFile(unknown)
	if err != nil || string(content) != "preserve" {
		t.Errorf("unknown content changed: %v", err)
	}
}

// The lifecycle barrier is free while an external clock callback runs, which is
// what keeps a caller that blocks in Clock able to admit a run. That run is live
// when reconciliation takes its snapshot, so it must not be interrupted. Moving
// the clock callback inside the barrier, or snapshotting before it, fails here.
func TestClockCallbackRunsOutsideLifecycleBarrier(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.commit("one", "one\n")
	f.mustImport(ImportInput{})
	clockReached := make(chan struct{})
	continueReconcile := make(chan struct{})
	release := sync.OnceFunc(func() { close(continueReconcile) })
	var first atomic.Bool
	f.service.Clock = func() time.Time {
		// Only reconciliation's first call blocks; the run's calls pass.
		if first.CompareAndSwap(false, true) {
			close(clockReached)
			<-continueReconcile
		}
		return f.now
	}
	reconcileErr := make(chan error, 1)
	reconcileStopped := make(chan struct{})
	go func() {
		defer close(reconcileStopped)
		reconcileErr <- f.service.Reconcile(ctx)
	}()
	// Cleanups run in reverse order, before the fixture's teardown. Each one
	// lets its operation finish so nothing outlives the fixture.
	t.Cleanup(func() {
		release()
		waitStopped(t, reconcileStopped, "reconciliation")
	})
	select {
	case <-clockReached:
	case <-time.After(5 * time.Second):
		t.Fatal("reconciliation did not reach the Clock callback")
	}
	if !f.service.lifecycle.TryLock() {
		t.Fatal("the lifecycle barrier is held while the clock callback runs")
	}
	f.service.lifecycle.Unlock()

	started, done, _, runErr := f.gatedRefresh(t)
	t.Cleanup(func() {
		release()
		select {
		case f.transport.gate <- struct{}{}:
		default:
		}
		waitStopped(t, done, "the run")
	})
	select {
	case <-started:
	case <-done:
		t.Fatalf("admission ended before the transport: %v", *runErr)
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
	release()
	select {
	case err := <-reconcileErr:
		noErr(t, err, "reconciliation")
	case <-time.After(5 * time.Second):
		t.Fatal("reconciliation did not finish")
	}
	after, exists, err := f.store.ImportRun(ctx, ids[0])
	if err != nil || !exists || after.Status != before.Status {
		t.Fatalf("reconciliation changed a live run: before=%q after=%q exists=%v error=%v", before.Status, after.Status, exists, err)
	}
	f.transport.gate <- struct{}{}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("live run did not finish")
	}
	noErr(t, *runErr, "refresh")
}

func waitStopped(t *testing.T, stopped <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Errorf("%s did not stop during cleanup", what)
	}
}

// The staging scan runs outside the lifecycle barrier, so a run may create its
// directory while the scan is walking it. The scan's informational row must not
// block the run, and the run's claim must win over it.
func TestAcquireStagingClaimsExistingInformationalRow(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.prepareRuntime(t)
	runID := strings.Repeat("6", 32)
	name := "run-" + runID
	if err := f.store.RegisterImportStaging(ctx, state.ImportStaging{
		Name: name, Token: strings.Repeat("a", 32), State: state.ImportStagingUnknown,
		Issue: "no authorization record was written by a run", CreatedAt: f.now,
	}); err != nil {
		t.Fatal(err)
	}
	dir, err := f.service.acquireStaging(ctx, runID, "project", f.now)
	noErr(t, err, "acquire with an informational row")
	row, exists, err := f.store.ImportStaging(ctx, name)
	if err != nil || !exists {
		t.Fatalf("claimed row exists=%v err=%v", exists, err)
	}
	if row.State != state.ImportStagingActive || row.Token != dir.token || row.RunID != runID || row.RepositoryID != "project" {
		t.Fatalf("claim did not take ownership: %+v", row)
	}
	if _, err := f.service.proveStagingOwnership(ctx, dir); err != nil {
		t.Fatalf("marker and row disagree after the claim: %v", err)
	}
}

// The scan tolerates a row that appeared between its existence check and its
// insert, and keeps the first informational row instead of failing.
func TestUnknownStagingRegistrationToleratesConcurrentClaim(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.prepareRuntime(t)
	name := "run-" + strings.Repeat("7", 32)
	first := state.ImportStaging{Name: name, Token: strings.Repeat("b", 32), State: state.ImportStagingUnknown, CreatedAt: f.now}
	noErr(t, f.service.registerUnknownStaging(ctx, first))
	second := state.ImportStaging{Name: name, Token: strings.Repeat("c", 32), State: state.ImportStagingUnknown, CreatedAt: f.now}
	noErr(t, f.service.registerUnknownStaging(ctx, second), "second registration")
	row, _, err := f.store.ImportStaging(ctx, name)
	if err != nil || row.Token != first.Token {
		t.Fatalf("informational row was replaced: %+v err=%v", row, err)
	}
}
