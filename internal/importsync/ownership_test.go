package importsync

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"owngit/internal/state"
)

func (f *fixture) prepareRuntime(t *testing.T) string {
	t.Helper()
	info, err := f.service.Prepare(context.Background())
	noErr(t, err, "prepare runtime")
	return info.RootID
}

func (f *fixture) writeSentinel(directory, payload string) string {
	f.t.Helper()
	path := filepath.Join(directory, "sentinel-"+payload+".txt")
	if err := os.WriteFile(path, []byte(payload), 0o600); err != nil {
		f.t.Fatal(err)
	}
	return path
}

func (f *fixture) assertSentinel(path, payload string) {
	f.t.Helper()
	content, err := os.ReadFile(path)
	if err != nil || string(content) != payload {
		f.t.Fatalf("sentinel %s=%q err=%v", path, content, err)
	}
}

// writeStagingEntry builds one staging-shaped directory with a valid marker but
// no authorization row.
func (f *fixture) writeStagingEntry(t *testing.T, name, repositoryID, runID, token string) string {
	t.Helper()
	rootID := f.prepareRuntime(t)
	directory := filepath.Join(f.service.stagingRootPath(), name)
	noErr(t, os.Mkdir(directory, 0o700))
	marker := stagingMarker{
		Version: stagingMarkerVersion, Name: name, RootID: rootID, RunID: runID,
		RepositoryID: repositoryID, Token: token, CreatedAt: f.now.Unix(),
	}
	noErr(t, writeStagingMarker(directory, marker))
	return directory
}

// A marker written by nobody is not ownership.
// Repeated reconciliation, and a later process, must keep preserving it.
func TestUnknownStagingNeverManufacturesCleanupAuthority(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	rootID := f.prepareRuntime(t)
	root := f.service.stagingRootPath()
	preserved := map[string]string{}

	markerless := filepath.Join(root, "run-"+strings.Repeat("a", 32))
	noErr(t, os.Mkdir(markerless, 0o700))
	preserved[f.writeSentinel(markerless, "markerless")] = "markerless"

	malformed := filepath.Join(root, "run-"+strings.Repeat("b", 32))
	noErr(t, os.Mkdir(malformed, 0o700))
	noErr(t, os.WriteFile(filepath.Join(malformed, stagingMarkerName), []byte("not json"), 0o600))
	preserved[f.writeSentinel(malformed, "malformed")] = "malformed"

	foreign := filepath.Join(root, "run-"+strings.Repeat("c", 32))
	noErr(t, os.Mkdir(foreign, 0o700))
	foreignID := strings.Repeat("d", 32)
	marker := stagingMarker{
		Version: stagingMarkerVersion, Name: filepath.Base(foreign), RootID: foreignID,
		RunID: strings.Repeat("e", 32), RepositoryID: "foreign", Token: strings.Repeat("f", 32),
		CreatedAt: f.now.Unix(),
	}
	noErr(t, writeStagingMarker(foreign, marker))
	preserved[f.writeSentinel(foreign, "foreign")] = "foreign"

	// A well-formed marker without a runtime root identity is invalid.
	rootless := filepath.Join(root, "run-"+strings.Repeat("9", 32))
	noErr(t, os.Mkdir(rootless, 0o700))
	noErr(t, writeStagingMarker(rootless, stagingMarker{
		Version: stagingMarkerVersion, Name: filepath.Base(rootless), RunID: strings.Repeat("9", 32),
		RepositoryID: "foreign", Token: strings.Repeat("8", 64), CreatedAt: f.now.Unix(),
	}))
	preserved[f.writeSentinel(rootless, "rootless")] = "rootless"

	// A readable marker naming this runtime root and repository still proves
	// nothing without an authorization row.
	readable := filepath.Join(root, "run-"+strings.Repeat("7", 32))
	noErr(t, os.Mkdir(readable, 0o700))
	noErr(t, writeStagingMarker(readable, stagingMarker{
		Version: stagingMarkerVersion, Name: filepath.Base(readable), RootID: rootID,
		RunID: strings.Repeat("7", 32), RepositoryID: "project", Token: strings.Repeat("6", 64),
		CreatedAt: f.now.Unix(),
	}))
	if _, err := readStagingMarker(readable); err != nil {
		t.Fatalf("fixture marker must be readable: %v", err)
	}
	preserved[f.writeSentinel(readable, "readable")] = "readable"
	directories := []string{markerless, malformed, foreign, rootless, readable}

	stray := filepath.Join(root, "notes.txt")
	noErr(t, os.WriteFile(stray, []byte("stray"), 0o600))
	preserved[stray] = "stray"

	check := func(label string) {
		t.Helper()
		for path, payload := range preserved {
			f.assertSentinel(path, payload)
		}
		for _, directory := range directories {
			if _, err := os.Stat(directory); err != nil {
				t.Fatalf("%s: marker directory %s was removed", label, directory)
			}
		}
		info, err := os.Lstat(root)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			t.Fatalf("%s: staging root changed: %v %v", label, info, err)
		}
	}
	check("initial")
	for pass := 1; pass <= 2; pass++ {
		if err := f.service.Reconcile(ctx); err != nil {
			t.Fatalf("reconcile pass %d: %v", pass, err)
		}
		check("same process")
	}
	// A restart takes the lease again and must not gain authority either.
	f.service.Close()
	second := &Service{Store: f.store, Repositories: f.manager, Clock: func() time.Time { return f.now }}
	defer func() { _ = second.Close() }()
	for pass := 1; pass <= 2; pass++ {
		if err := second.Reconcile(ctx); err != nil {
			t.Fatalf("restart reconcile pass %d: %v", pass, err)
		}
		check("restarted process")
	}
	// Unknown rows stay informational.
	for _, directory := range append(directories, stray) {
		name := filepath.Base(directory)
		row, exists, err := f.store.ImportStaging(ctx, name)
		if err != nil || !exists || row.State != state.ImportStagingUnknown {
			t.Fatalf("unknown row %s=%+v exists=%v err=%v", name, row, exists, err)
		}
	}
}

// A genuine run directory carries an authorization row written before any
// cleanup. Once its run is terminal it is removed, so preservation is not the
// only behavior.
func TestAuthorizedTerminalStagingIsCleaned(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.commit("one", "one\n")
	f.mustImport(ImportInput{})
	root := f.service.stagingRootPath()
	for _, entry := range mustReadDir(t, root) {
		if strings.HasPrefix(entry.Name(), "run-") {
			t.Fatalf("staging was not settled after the import: %s", entry.Name())
		}
	}

	runID := strings.Repeat("7", 32)
	token, err := newStagingToken()
	noErr(t, err)
	directory := f.writeStagingEntry(t, "run-"+runID, "project", runID, token)
	f.writeSentinel(directory, "terminal")
	if err := f.store.RegisterImportStaging(ctx, state.ImportStaging{
		Name: "run-" + runID, RepositoryID: "project", RunID: runID, Token: token,
		State: state.ImportStagingActive, CreatedAt: f.now,
	}); err != nil {
		t.Fatal(err)
	}
	run := state.ImportRun{
		ID: runID, RepositoryID: "project", SourceGeneration: 1, AuthorityRevision: 1, Kind: state.ImportKindRefresh,
		Status: state.ImportRunPreparing, StartedAt: f.now, CreatedAt: f.now,
	}
	noErr(t, f.store.BeginImportRun(ctx, run))
	run.Status = state.ImportRunInterrupted
	run.FinishedAt = f.now
	noErr(t, f.store.FinishImportRun(ctx, run))

	noErr(t, f.service.Reconcile(ctx))
	if _, err := os.Stat(directory); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("authorized terminal staging survived: %v", err)
	}
	row, exists, err := f.store.ImportStaging(ctx, "run-"+runID)
	if err != nil || !exists || row.State != state.ImportStagingReleased {
		t.Fatalf("released row=%+v exists=%v err=%v", row, exists, err)
	}
	// The root marker and lease file stay in place.
	for _, name := range []string{runtimeRootMarkerName, runtimeRootLockName} {
		if _, err := os.Lstat(filepath.Join(root, name)); err != nil {
			t.Fatalf("root %s missing after cleanup: %v", name, err)
		}
	}
}

// Reconciliation inside a live server must not interrupt or delete the run the
// server is executing.
func TestReconcileLeavesLiveRunAlone(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.commit("one", "one\n")
	f.mustImport(ImportInput{})

	f.transport.gate = make(chan struct{}, 1)
	started := make(chan struct{})
	f.transport.before = func() { close(started) }
	var refreshRun state.ImportRun
	var refreshErr error
	done := make(chan struct{})
	go func() {
		defer close(done)
		refreshRun, refreshErr = f.refresh()
	}()
	<-started

	noErr(t, f.service.Reconcile(ctx), "reconcile during live run")
	active, exists, err := f.store.ActiveImportRun(ctx, "project")
	if err != nil || !exists {
		t.Fatalf("live run disappeared: exists=%v err=%v", exists, err)
	}
	if active.Status == state.ImportRunInterrupted {
		t.Fatalf("live run was interrupted by reconciliation: %+v", active)
	}
	if active.StagingName == "" {
		t.Fatal("live run has no staging record")
	}
	if _, err := os.Stat(filepath.Join(f.service.stagingRootPath(), active.StagingName)); err != nil {
		t.Fatalf("live staging was removed: %v", err)
	}

	f.transport.gate <- struct{}{}
	<-done
	noErr(t, refreshErr, "refresh")
	if refreshRun.ID != active.ID || refreshRun.Status != state.ImportRunComplete {
		t.Fatalf("live run did not finish: %+v", refreshRun)
	}
	stored, _, err := f.store.ImportRun(ctx, active.ID)
	if err != nil || stored.Status != state.ImportRunComplete {
		t.Fatalf("live run did not finish: %+v err=%v", stored, err)
	}
	if _, err := os.Stat(filepath.Join(f.service.stagingRootPath(), active.StagingName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("finished staging survived: %v", err)
	}
}

// Two live services on one state directory cannot both own the root. The
// second refuses until the first releases, and then adopts the same identity.
func TestSecondRuntimeOwnershipIsRefused(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	first, err := f.service.Prepare(ctx)
	noErr(t, err)
	second := &Service{Store: f.store, Repositories: f.manager}
	defer func() { _ = second.Close() }()
	if _, err := second.Prepare(ctx); !errors.Is(err, ErrRuntimeHeld) {
		t.Fatalf("second prepare err=%v", err)
	}
	f.service.Close()
	recovered, err := second.Prepare(ctx)
	noErr(t, err, "prepare after release")
	if recovered.RootID != first.RootID {
		t.Fatalf("root identity changed across ownership: %s then %s", first.RootID, recovered.RootID)
	}
}

// A same-user replacement of the lock file leaves the first process holding a
// lock on an unlinked file. Authority is bound to the root marker generation
// and its marker lock, so the first process keeps ownership while it lives, and
// only a free marker lock lets the second bind the replacement to the same
// generation and adopt the same root identity.
func TestReplacedRuntimeLockIsRevalidated(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.commit("one", "one\n")
	f.mustImport(ImportInput{})
	rootID := f.prepareRuntime(t)

	lockPath := runtimeLockPath(f.service)
	replaced := true
	if err := os.Remove(lockPath); err != nil {
		if !runtimeSharingViolation(err) {
			t.Fatalf("remove runtime lock: %v", err)
		}
		replaced = false
	} else if err := os.WriteFile(lockPath, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	noErr(t, assertRuntimeStillOwned(f.service), "established runtime ownership after replacement attempt")

	second := &Service{Store: f.store, Repositories: f.manager}
	defer func() { _ = second.Close() }()
	if _, err := second.Prepare(ctx); !errors.Is(err, ErrRuntimeHeld) {
		t.Fatalf("second owner adopted a held root authority: err=%v", err)
	}
	if replaced {
		// A platform that permits unlinking the lock still relies on the held
		// marker generation for authority.
		noErr(t, f.service.Reconcile(ctx), "established owner lost authority")
	} else if err := assertRuntimeStillOwned(f.service); err != nil {
		t.Fatalf("sharing protection did not preserve ownership: %v", err)
	}
	noErr(t, f.service.Close())
	info, err := second.Prepare(ctx)
	noErr(t, err, "next owner could not prepare after release")
	if info.RootID != rootID {
		t.Fatalf("root identity changed: %s then %s", rootID, info.RootID)
	}
}

// Replacement must not give a second service authority over a live run.
func TestReplacementPreservesAnotherOwnersLiveRun(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.commit("one", "one\n")
	f.mustImport(ImportInput{})
	rootID := f.prepareRuntime(t)
	started, done, refreshRun, refreshErr := f.gatedRefresh(t)
	<-started
	released := false
	defer func() {
		if !released {
			f.transport.gate <- struct{}{}
			<-done
		}
	}()
	ids := f.service.liveRunIDs()
	if len(ids) != 1 {
		t.Fatalf("expected one live run, got %d", len(ids))
	}
	before, exists, err := f.store.ImportRun(ctx, ids[0])
	if err != nil || !exists || before.Status != state.ImportRunFetching {
		t.Fatalf("run did not reach fetching: exists=%v status=%s err=%v", exists, before.Status, err)
	}
	staging := filepath.Join(f.service.stagingRootPath(), before.StagingName)
	if _, err := os.Stat(staging); err != nil {
		t.Fatal(err)
	}
	lockPath := runtimeLockPath(f.service)
	if err := os.Rename(lockPath, lockPath+".preserved"); err != nil && !runtimeSharingViolation(err) {
		t.Fatalf("rename runtime lock: %v", err)
	}
	noErr(t, assertRuntimeStillOwned(f.service), "live owner's runtime after replacement attempt")
	second := &Service{Store: f.store, Repositories: f.manager}
	defer func() { _ = second.Close() }()
	if err := second.Reconcile(ctx); !errors.Is(err, ErrRuntimeHeld) {
		t.Errorf("second owner was not refused: %v", err)
	}
	after, exists, err := f.store.ImportRun(ctx, ids[0])
	if err != nil || !exists {
		t.Fatalf("live run readback: exists=%v err=%v", exists, err)
	}
	if after.Status == state.ImportRunInterrupted {
		t.Error("replacement let a second owner interrupt a genuinely fetching run")
	}
	if _, err := os.Stat(staging); err != nil {
		t.Errorf("replacement let a second owner remove live staging: %v", err)
	}

	f.transport.gate <- struct{}{}
	<-done
	released = true
	if *refreshErr != nil || refreshRun.Status != state.ImportRunComplete {
		t.Fatalf("live run did not finish: status=%q err=%v", refreshRun.Status, *refreshErr)
	}
	noErr(t, f.service.Close(), "close first owner")
	info, err := second.Prepare(ctx)
	if err != nil || info.RootID != rootID {
		t.Fatalf("next owner did not retain root identity: info=%+v err=%v", info, err)
	}
}

// Identity loss while a run is active stays latched after the same marker is
// restored. The pipeline stops at its next boundary and preserves staging.
func TestIdentityLossLatchesWhileRunActive(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.commit("one", "one\n")
	f.mustImport(ImportInput{})
	rootID := f.prepareRuntime(t)
	started, done, refreshRun, refreshErr := f.gatedRefresh(t)
	<-started

	ids := f.service.liveRunIDs()
	if len(ids) != 1 {
		t.Fatalf("live runs=%v", ids)
	}
	live, exists, err := f.store.ImportRun(ctx, ids[0])
	if err != nil || !exists || live.Status != state.ImportRunFetching {
		t.Fatalf("live run changed: %+v exists=%v err=%v", live, exists, err)
	}
	staging := filepath.Join(f.service.stagingRootPath(), live.StagingName)
	entries := mustReadDir(t, staging)
	if len(entries) != 1 || entries[0].Name() != stagingMarkerName || !entries[0].Type().IsRegular() {
		t.Fatalf("staging was initialized before transport release: %v", entries)
	}
	stagingMarkerPath := filepath.Join(staging, stagingMarkerName)
	markerBefore, err := os.ReadFile(stagingMarkerPath)
	noErr(t, err)
	if _, err := os.Lstat(filepath.Join(staging, "HEAD")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("staging HEAD existed before transport release: %v", err)
	}

	restore, err := induceRuntimeMarkerMismatch(f.service, ".identity-preserved")
	noErr(t, err)
	availability := f.service.Availability(ctx)
	if availability.Available || availability.Code != CodeRuntimeUnavailable {
		t.Fatalf("availability did not report ownership loss: %+v", availability)
	}
	noErr(t, restore(), "restore runtime marker")
	if _, err := f.service.Prepare(ctx); !errors.Is(err, ErrRuntimeLost) {
		t.Fatalf("restored marker cleared ownership loss: err=%v", err)
	}

	f.transport.gate <- struct{}{}
	<-done
	if !errors.Is(*refreshErr, ErrRuntimeLost) || refreshRun.Status != state.ImportRunFailed {
		t.Fatalf("run did not stop on ownership loss: status=%q err=%v", refreshRun.Status, *refreshErr)
	}
	if _, err := os.Stat(staging); err != nil {
		t.Fatalf("lost run staging was removed: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(staging, "HEAD")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("pipeline initialized stale staging after runtime loss: %v", err)
	}
	entries = mustReadDir(t, staging)
	if len(entries) != 1 || entries[0].Name() != stagingMarkerName || !entries[0].Type().IsRegular() {
		t.Fatalf("pipeline changed stale staging contents: %v", entries)
	}
	markerAfter, err := os.ReadFile(stagingMarkerPath)
	if err != nil || string(markerAfter) != string(markerBefore) {
		t.Fatalf("staging marker changed: err=%v", err)
	}
	if _, err := f.service.Prepare(ctx); !errors.Is(err, ErrRuntimeLost) {
		t.Fatalf("completed run cleared ownership loss: err=%v", err)
	}
	noErr(t, f.service.Close(), "close lost idle runtime")
	info, err := f.service.Prepare(ctx)
	if err != nil || info.RootID != rootID {
		t.Fatalf("explicit close did not recover the restored root: info=%+v err=%v", info, err)
	}
}

func TestHeldMarkerCanBeRevalidatedThroughOwningHandle(t *testing.T) {
	f := newFixture(t)
	f.prepareRuntime(t)
	root, ok := f.service.preparedRuntime()
	if !ok {
		t.Fatal("runtime was not prepared")
	}
	owned, err := root.stillOwned()
	if err != nil || !owned {
		t.Fatalf("held marker revalidation: owned=%v err=%v", owned, err)
	}
}

func TestPartialRuntimeInitializationWithoutMarkerIsRefused(t *testing.T) {
	f := newFixture(t)
	root := f.service.stagingRootPath()
	noErr(t, os.MkdirAll(root, 0o700))
	generation := strings.Repeat("a", 32)
	content := fmt.Sprintf("{\"version\":%d,\"generation\":%q}\n", runtimeLockRecordVersion, generation)
	noErr(t, os.WriteFile(filepath.Join(root, runtimeRootLockName), []byte(content), 0o600))
	if _, err := f.service.Prepare(context.Background()); !errors.Is(err, ErrRuntimeUnsafe) {
		t.Fatalf("partial initialization was adopted: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(root, runtimeRootMarkerName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("partial initialization created a marker: %v", err)
	}
}

// Passive reads never create the runtime tree; an explicit mutation does.
func TestAvailabilityIsPassive(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	runtimeRoot := f.service.runtimeRootPath()
	if _, err := os.Lstat(runtimeRoot); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("precondition: runtime root exists: %v", err)
	}
	availability := f.service.Availability(ctx)
	if !availability.Available || availability.Prepared {
		t.Fatalf("availability before prepare=%+v", availability)
	}
	if _, err := os.Lstat(runtimeRoot); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Availability created the runtime root: %v", err)
	}

	first := f.prepareRuntime(t)
	second := f.prepareRuntime(t)
	if first != second {
		t.Fatalf("Prepare changed the root identity: %s then %s", first, second)
	}
	availability = f.service.Availability(ctx)
	if !availability.Prepared || availability.RootID != first {
		t.Fatalf("availability after prepare=%+v", availability)
	}
	if _, err := os.Lstat(filepath.Join(f.service.stagingRootPath(), runtimeRootMarkerName)); err != nil {
		t.Fatalf("root marker missing after prepare: %v", err)
	}
	f.service.Close()
	availability = f.service.Availability(ctx)
	if !availability.Available || availability.Prepared {
		t.Fatalf("availability after close=%+v", availability)
	}
}

// An unknown nonempty root is adopted without changing its mode, and every
// unknown entry inside it survives.
func TestUnownedNonemptyRootIsAdoptedWithoutModeChange(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	root := f.service.stagingRootPath()
	noErr(t, os.MkdirAll(root, 0o755))
	noErr(t, os.Chmod(root, 0o755))
	unknown := filepath.Join(root, "run-"+strings.Repeat("1", 32))
	noErr(t, os.Mkdir(unknown, 0o755))
	sentinel := f.writeSentinel(unknown, "unowned")
	before, err := os.Stat(root)
	noErr(t, err)
	// Windows reports directory permissions from attributes, not from
	// Chmod, so only the unchanged mode is portable. Unix also checks the
	// exact mode the test set.
	if runtime.GOOS != "windows" && before.Mode().Perm() != 0o755 {
		t.Fatalf("fixture root mode=%v", before.Mode().Perm())
	}

	if _, err := f.service.Prepare(ctx); err != nil {
		t.Fatalf("adopt unowned nonempty root: %v", err)
	}
	info, err := os.Stat(root)
	if err != nil || info.Mode().Perm() != before.Mode().Perm() {
		t.Fatalf("adopted root mode=%v before=%v err=%v", info.Mode().Perm(), before.Mode().Perm(), err)
	}
	f.assertSentinel(sentinel, "unowned")
	for pass := 1; pass <= 2; pass++ {
		noErr(t, f.service.Reconcile(ctx))
		f.assertSentinel(sentinel, "unowned")
	}
	if _, err := os.Stat(filepath.Join(root, runtimeRootMarkerName)); err != nil {
		t.Fatalf("root marker missing after adoption: %v", err)
	}
}

// A symlinked staging root is refused instead of followed, and nothing inside
// the link target is touched.
func TestLinkedStagingRootIsRefusedAndPreserved(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	target := filepath.Join(f.root, "linked-target")
	noErr(t, os.MkdirAll(target, 0o700))
	sentinel := f.writeSentinel(target, "linked")
	root := f.service.stagingRootPath()
	noErr(t, os.MkdirAll(filepath.Dir(root), 0o700))
	noErr(t, os.Symlink(target, root))
	if _, err := f.service.Prepare(ctx); !errors.Is(err, ErrRuntimeUnsafe) {
		t.Fatalf("prepare linked root err=%v", err)
	}
	if err := f.service.Reconcile(ctx); !errors.Is(err, ErrRuntimeUnsafe) {
		t.Fatalf("reconcile linked root err=%v", err)
	}
	info, err := os.Lstat(root)
	if err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("staging root symlink was replaced: %v %v", info, err)
	}
	f.assertSentinel(sentinel, "linked")
	if _, err := os.Lstat(filepath.Join(target, runtimeRootMarkerName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("marker was written through the link: %v", err)
	}
	availability := f.service.Availability(ctx)
	if availability.Available || availability.Code != CodeRuntimeUnsafe {
		t.Fatalf("availability on linked root=%+v", availability)
	}
}

// A name collision refuses instead of adopting, so pre-existing content is
// never removed by the creation path.
func TestStagingCollisionPreservesExistingDirectory(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.prepareRuntime(t)
	runID := strings.Repeat("9", 32)
	collision := filepath.Join(f.service.stagingRootPath(), "run-"+runID)
	noErr(t, os.Mkdir(collision, 0o700))
	sentinel := f.writeSentinel(collision, "collision")

	if _, err := f.service.acquireStaging(ctx, runID, "project", f.now); err == nil {
		t.Fatal("collision was adopted by acquireStaging")
	}
	f.assertSentinel(sentinel, "collision")
	if _, exists, err := f.store.ImportStaging(ctx, "run-"+runID); err != nil || exists {
		t.Fatalf("collision left an authorization row: exists=%v err=%v", exists, err)
	}

	noErr(t, f.service.Reconcile(ctx))
	f.assertSentinel(sentinel, "collision")
	row, exists, err := f.store.ImportStaging(ctx, "run-"+runID)
	if err != nil || !exists || row.State != state.ImportStagingUnknown {
		t.Fatalf("collision row=%+v exists=%v err=%v", row, exists, err)
	}
}

// A root marker that does not parse is refused, never rewritten.
func TestMalformedRootMarkerIsRefusedAndPreserved(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	root := f.service.stagingRootPath()
	noErr(t, os.MkdirAll(root, 0o700))
	markerPath := filepath.Join(root, runtimeRootMarkerName)
	content := []byte(`{"version":1,"root_id":"short"}`)
	noErr(t, os.WriteFile(markerPath, content, 0o600))
	if _, err := f.service.Prepare(ctx); !errors.Is(err, ErrRuntimeUnsafe) {
		t.Fatalf("prepare with malformed root marker err=%v", err)
	}
	after, err := os.ReadFile(markerPath)
	if err != nil || string(after) != string(content) {
		t.Fatalf("malformed root marker changed: %q err=%v", after, err)
	}
}

func mustReadDir(t *testing.T, directory string) []os.DirEntry {
	t.Helper()
	entries, err := os.ReadDir(directory)
	noErr(t, err)
	return entries
}
