package importsync

import (
	"context"
	"os"
	"strings"
	"testing"

	"owngit/internal/state"
)

// A cancelled first import must leave nothing behind: no repository, no
// unpublished directory, no stored source or credential, and nothing that
// keeps the name blocked. Once its directory was renamed into place the import
// is published, and a cancellation that arrives later cannot undo that: the
// import completes instead of reporting a cancellation. Each case cancels at
// one point, from the stage hooks inside the run, so the timing is exact.
func TestCancellingAFirstImportLeavesNothingBehindOrCompletes(t *testing.T) {
	if testing.Short() {
		t.Skip("runs a whole first import for each cancellation point")
	}
	cases := []struct {
		name      string
		arm       func(f *fixture, cancel func())
		published bool
	}{
		{"preparing", func(f *fixture, cancel func()) { f.service.afterRunStage = onStage(state.ImportRunPreparing, cancel) }, false},
		{"fetching", func(f *fixture, cancel func()) { f.service.afterRunStage = onStage(state.ImportRunFetching, cancel) }, false},
		{"indexing", func(f *fixture, cancel func()) { f.service.afterRunStage = onStage(state.ImportRunIndexing, cancel) }, false},
		{"inspecting", func(f *fixture, cancel func()) { f.service.afterRunStage = onStage(state.ImportRunInspecting, cancel) }, false},
		{"destination check", func(f *fixture, cancel func()) {
			f.service.beforeInitialDestinationCheck = func() error { cancel(); return nil }
		}, false},
		// The window the reviewer found: the unpublished directory exists and
		// its repository is being written.
		{"destination created", func(f *fixture, cancel func()) { f.service.afterInitialDirectoryCreated = cancel }, false},
		{"before objects", func(f *fixture, cancel func()) {
			f.service.beforeInitialPublication = func() error { cancel(); return nil }
		}, false},
		{"publishing", func(f *fixture, cancel func()) { f.service.afterRunStage = onStage(state.ImportRunPublishing, cancel) }, false},
		{"refs prepared", func(f *fixture, cancel func()) {
			f.service.whileRefsPrepared = func() { f.service.whileRefsPrepared = nil; cancel() }
		}, false},
		{"before rename", func(f *fixture, cancel func()) {
			f.service.beforeInitialRename = func() error { cancel(); return nil }
		}, false},
		{"after rename", func(f *fixture, cancel func()) {
			f.service.beforeInitialRepositoryRecord = func() error { cancel(); return nil }
		}, true},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			f := newFixture(t)
			f.commit("initial", "initial\n")
			ctx := context.Background()
			cancelled := false
			test.arm(f, func() {
				requested, err := f.service.Cancel(ctx, "project")
				if err != nil || !requested {
					t.Errorf("cancel requested=%v err=%v", requested, err)
				}
				cancelled = true
			})
			result, err := f.importProject(ImportInput{Credentials: &Credentials{BearerToken: "first-import-token"}})
			require(t, cancelled, "the cancellation point was not reached: run=%+v err=%v", result.Run, err)
			_, repositoryExists, readErr := f.store.Repository(ctx, "project")
			noErr(t, readErr)
			if test.published {
				require(t, err == nil && result.Run.Status == state.ImportRunComplete && repositoryExists &&
					result.Run.CancelRequestedAt != nil,
					"a cancel after publication: status=%s message=%q repository=%v err=%v", result.Run.Status, result.Run.Message, repositoryExists, err)
				refs := f.destinationRefs()
				require(t, refs["refs/heads/main"] != "", "published refs=%v", refs)
				_, exists, err := f.store.LoadImportCredentials(ctx, "project")
				require(t, err == nil && exists, "a published import lost its credential exists=%v err=%v", exists, err)
				assertNoLeftoverDirectories(t, f)
				return
			}
			require(t, problemCode(err) == CodeCancelled && result.Run.Status == state.ImportRunCancelled,
				"cancelled first import: run=%+v err=%v", result.Run, err)
			stored, exists, readErr := f.store.ImportRun(ctx, result.Run.ID)
			require(t, readErr == nil && exists && stored.Status == state.ImportRunCancelled,
				"stored run=%+v exists=%v err=%v", stored, exists, readErr)
			require(t, !repositoryExists, "a cancelled first import created its repository")
			finalPath, pathErr := f.manager.Path("project")
			noErr(t, pathErr)
			_, statErr := os.Lstat(finalPath)
			require(t, os.IsNotExist(statErr), "a cancelled first import left its final path: %v", statErr)
			assertNoLeftoverDirectories(t, f)
			assertNoBinding(t, f, "project")
			rows, readErr := f.store.ImportInitialDestinationsForRun(ctx, result.Run.ID)
			noErr(t, readErr)
			for _, row := range rows {
				require(t, row.State == state.ImportInitialReleased, "destination %s stayed %s", row.Name, row.State)
			}
			pending, readErr := f.store.PendingImportIntents(ctx, "project")
			noErr(t, readErr)
			for _, intent := range pending {
				require(t, intent.Status != state.ImportIntentPlanning && intent.Status != state.ImportIntentApplied &&
					intent.Status != state.ImportIntentUnresolved, "intent %s stayed %s", intent.ID, intent.Status)
			}
			// Nothing blocks the name.
			_, err = f.manager.Create(ctx, "project", "")
			noErr(t, err, "the name stayed blocked")
		})
	}
}

func onStage(stage string, cancel func()) func(string) {
	done := false
	return func(status string) {
		if status == stage && !done {
			done = true
			cancel()
		}
	}
}

func assertNoLeftoverDirectories(t *testing.T, f *fixture) {
	t.Helper()
	entries, err := os.ReadDir(f.manager.Root)
	noErr(t, err)
	for _, entry := range entries {
		require(t, !strings.HasPrefix(entry.Name(), unpublishedDirectoryPrefix),
			"an unpublished directory was left behind: %s", entry.Name())
	}
}

// strandFirstImport records what an earlier version left when a cancelled
// first import failed to remove its unpublished directory: an unresolved
// initial run without a publication intent, a destination row still
// preparing, and the stored source and token. The directory itself is gone.
func strandFirstImport(t *testing.T, f *fixture) state.ImportRun {
	t.Helper()
	ctx := context.Background()
	source := bindOrphan(t, f, "project")
	run := state.ImportRun{
		ID: strings.Repeat("5", 32), RepositoryID: "project", SourceGeneration: source.SourceGeneration,
		AuthorityRevision: source.AuthorityRevision, Kind: state.ImportKindInitial, Status: state.ImportRunPreparing,
		StartedAt: f.now, CreatedAt: f.now,
	}
	noErr(t, f.store.BeginImportRun(ctx, run))
	name, err := newUnpublishedDirectoryName()
	noErr(t, err)
	noErr(t, f.store.RegisterImportInitialDestination(ctx, state.ImportInitialDestination{
		Name: name, RepositoryID: "project", RunID: run.ID, RootID: strings.Repeat("6", 32), Token: strings.Repeat("7", 32),
		DisplayName: "project", State: state.ImportInitialPreparing, CreatedAt: f.now,
	}))
	run.Status, run.ErrorClass, run.FinishedAt = state.ImportRunUnresolved, CodeUnresolved, f.now
	run.Message = "initial destination creation failed and cleanup also failed"
	noErr(t, f.store.FinishImportRun(ctx, run))
	return run
}

// Such a name used to stay blocked: clearing its credentials answered busy,
// and neither a restart nor repository creation helped. Clearing now settles
// the stopped run and removes the stored source and token.
func TestClearingAStrandedFirstImportSettlesIt(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	run := strandFirstImport(t, f)
	forgotten, err := f.service.ForgetOrphanImport(ctx, "project")
	require(t, err == nil && forgotten, "clear forgotten=%v err=%v", forgotten, err)
	assertNoBinding(t, f, "project")
	stored, _, err := f.store.ImportRun(ctx, run.ID)
	require(t, err == nil && stored.Status == state.ImportRunFailed, "stranded run=%+v err=%v", stored, err)
	_, err = f.manager.Create(ctx, "project", "")
	noErr(t, err, "the name stayed blocked")
}

// The next start settles it too.
func TestRestartSettlesAStrandedFirstImport(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	run := strandFirstImport(t, f)
	restartService(t, f)
	assertNoBinding(t, f, "project")
	stored, _, err := f.store.ImportRun(ctx, run.ID)
	require(t, err == nil && stored.Status == state.ImportRunFailed, "stranded run=%+v err=%v", stored, err)
}
