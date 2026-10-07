package importsync

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"owngit/internal/gitexec"
	"owngit/internal/recovery"
	"owngit/internal/state"
)

// initialIntentOf returns the only publication intent of the named run.
func initialIntentOf(t *testing.T, f *fixture, runID string) state.ImportIntent {
	t.Helper()
	for _, intent := range portableImportIntentsAllowingFailure(f.store, "project") {
		if intent.RunID == runID {
			return intent
		}
	}
	t.Fatalf("run %s has no pending publication intent", runID)
	return state.ImportIntent{}
}

// restartService releases the lease and reconciles, as the next serve does.
func restartService(t *testing.T, f *fixture) {
	t.Helper()
	f.service.lifecycle.Lock()
	f.service.closing = false
	f.service.lifecycle.Unlock()
	noErr(t, f.service.Close(), "close before restart")
	noErr(t, f.service.Reconcile(context.Background()), "reconcile after restart")
}

// reimportLeavesNothingUnresolved imports the same name again and checks that
// no earlier intent remains for the owner to resolve, and that backup works.
func reimportLeavesNothingUnresolved(t *testing.T, f *fixture) {
	t.Helper()
	ctx := context.Background()
	_, err := f.importProject(ImportInput{})
	noErr(t, err, "import of the same name again")
	status, err := f.service.Status(ctx, "project")
	require(t, err == nil && status.UnresolvedIntents == 0,
		"status after the new import unresolved=%d err=%v", status.UnresolvedIntents, err)
	_, err = recovery.CreateWithReport(ctx, f.store, f.manager, filepath.Join(f.root, "backup-after-reimport"))
	noErr(t, err,
		"backup after the new import")
}

// A cancel or a shutdown that stops an initial import after its ref
// transaction and before its HEAD write leaves nothing unresolved: the run
// reports the stop, the intent is invalidated, and the unpublished directory is
// removed at once. Backup, restart, and a new import of the same name all work.
func TestStoppedInitialPublicationIsSettledByTheRun(t *testing.T) {
	for _, mode := range []string{"cancel", "shutdown", "killed-after-commit"} {
		t.Run(mode, func(t *testing.T) {
			f := newFixture(t)
			completeFixtureSetup(t, f)
			f.commit("one", "one\n")
			f.git(f.source, "branch", "trunk")
			f.git(f.source, "symbolic-ref", "HEAD", "refs/heads/trunk")
			if mode == "killed-after-commit" {
				// The cancel kills the Git process after its ref transaction
				// committed, so the command reports an unclassified error.
				f.service.afterPreparedRefResult = func() error {
					f.service.afterPreparedRefResult = nil
					if _, err := f.service.Cancel(context.Background(), "project"); err != nil {
						t.Errorf("cancel: %v", err)
					}
					return fmt.Errorf("git update-ref: signal: killed: %w", context.Canceled)
				}
			}
			f.service.beforeFinalHEADLock = func() {
				f.service.beforeFinalHEADLock = nil
				switch mode {
				case "killed-after-commit":
					t.Error("HEAD was attempted after the ref command failed")
					return
				case "cancel":
					if _, err := f.service.Cancel(context.Background(), "project"); err != nil {
						t.Errorf("cancel: %v", err)
					}
					return
				}
				go func() { _ = f.service.Shutdown(context.Background()) }()
				for {
					f.service.lifecycle.RLock()
					closing := f.service.closing
					f.service.lifecycle.RUnlock()
					if closing {
						return
					}
				}
			}
			_, err := f.importProject(ImportInput{})
			f.service.beforeFinalHEADLock = nil
			// The API maps only classified problems to their HTTP status.
			var problem *Problem
			if !errors.As(err, &problem) || problem.Code != CodeCancelled {
				t.Fatalf("stopped initial import err=%v", err)
			}
			ctx := context.Background()
			run := f.lastRun()
			require(t, run.Status == state.ImportRunCancelled, "stopped initial run=%s/%s", run.Status, run.ErrorClass)
			intent := initialIntentOf(t, f, run.ID)
			require(t, intent.Status == state.ImportIntentInvalidated &&
				strings.Contains(intent.Reason, "nothing was published"),
				"stopped initial intent=%s reason=%q", intent.Status, intent.Reason)
			dirs := unpublishedInitialDirectories(t, f)
			require(t, len(dirs) == 0, "unpublished directories left: %v", dirs)
			_, _, exists, err := f.manager.ExistingPath(ctx, "project")
			require(t, err == nil && !exists,
				"stopped initial import created a repository exists=%v err=%v", exists, err)
			_, err = recovery.CreateWithReport(ctx, f.store, f.manager, filepath.Join(f.root, "backup"))
			noErr(t, err,
				"backup after the stopped initial import")
			if mode == "shutdown" {
				waitForShutdownClose(t, f)
			}
			restartService(t, f)
			after := initialIntentOf(t, f, run.ID)
			require(t, after.Status == state.ImportIntentInvalidated,
				"restart changed the settled intent to %s", after.Status)
			reimportLeavesNothingUnresolved(t, f)
		})
	}
}

// waitForShutdownClose waits until the background Shutdown has released the
// lease, so the simulated restart does not race it.
func waitForShutdownClose(t *testing.T, f *fixture) {
	t.Helper()
	for i := 0; i < 500; i++ {
		if _, prepared := f.service.preparedRuntime(); !prepared {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("shutdown did not release the import runtime")
}

// unreadableInitialPublication stops an initial import so that its readback
// fails: another writer holds HEAD and the refs directory is briefly missing.
// The run cannot prove the outcome, so the intent is unresolved and the
// directory is kept for reconciliation. restore puts the directory back into
// a readable, partial state.
func unreadableInitialPublication(t *testing.T, f *fixture) (state.ImportRun, string, func()) {
	t.Helper()
	completeFixtureSetup(t, f)
	f.commit("one", "one\n")
	f.git(f.source, "branch", "trunk")
	f.git(f.source, "symbolic-ref", "HEAD", "refs/heads/trunk")
	var directory string
	hidden := filepath.Join(f.root, "hidden-refs")
	f.service.beforeFinalHEADLock = func() {
		f.service.beforeFinalHEADLock = nil
		dirs := unpublishedInitialDirectories(t, f)
		if len(dirs) != 1 {
			t.Errorf("unpublished directories=%v", dirs)
			return
		}
		directory = dirs[0]
		if err := os.WriteFile(filepath.Join(directory, "HEAD.lock"), []byte("ref: refs/heads/other\n"), 0o600); err != nil {
			t.Error(err)
		}
		if err := os.Rename(filepath.Join(directory, "refs"), hidden); err != nil {
			t.Error(err)
		}
	}
	_, err := f.importProject(ImportInput{})
	require(t, problemCode(err) == CodeUnresolved, "unreadable initial publication err=%v", err)
	run := f.lastRun()
	intent := initialIntentOf(t, f, run.ID)
	require(t, intent.Status == state.ImportIntentUnresolved, "unreadable initial intent=%s", intent.Status)
	restore := func() {
		noErr(t, os.Rename(hidden, filepath.Join(directory, "refs")))
		noErr(t, os.Remove(filepath.Join(directory, "HEAD.lock")))
	}
	return run, directory, restore
}

// An unresolved initial publication whose repository was never recorded is
// refused by backup with advice that works: start OwnGit once. Reconciliation
// then settles the intent and the run in the same step that removes the owned
// unpublished directory.
func TestReconciliationSettlesUnresolvedInitialPublicationItRemoves(t *testing.T) {
	f := newFixture(t)
	run, directory, restore := unreadableInitialPublication(t, f)
	ctx := context.Background()
	_, err := recovery.CreateWithReport(ctx, f.store, f.manager, filepath.Join(f.root, "backup-refused"))
	require(t, err != nil && strings.Contains(err.Error(), `"project"`) &&
		strings.Contains(err.Error(), "unresolved") && strings.Contains(err.Error(), "Start and stop OwnGit once"),
		"backup of an unresolved intent without a repository err=%v", err)
	_, statErr := os.Stat(filepath.Join(f.root, "backup-refused"))
	require(t, os.IsNotExist(statErr), "refused backup left output: %v", statErr)
	restore()

	restartService(t, f)
	_, statErr = os.Stat(directory)
	require(t, os.IsNotExist(statErr), "reconciliation kept the owned incomplete directory: %v", statErr)
	intent := initialIntentOf(t, f, run.ID)
	require(t, intent.Status == state.ImportIntentInvalidated &&
		strings.Contains(intent.Reason, "nothing was published"),
		"reconciled intent=%s reason=%q", intent.Status, intent.Reason)
	stored, _, err := f.store.ImportRun(ctx, run.ID)
	require(t, err == nil && stored.Status == state.ImportRunFailed && stored.ErrorClass == CodePublishFailed &&
		strings.Contains(stored.Message, "earlier outcome"), "reconciled run=%+v err=%v", stored, err)
	_, err = recovery.CreateWithReport(ctx, f.store, f.manager, filepath.Join(f.root, "backup"))
	noErr(t, err, "backup after reconciliation")
	reimportLeavesNothingUnresolved(t, f)
}

// When reconciliation preserves the unpublished directory, the backup advice
// names the remaining action: move that directory out of the repository root
// and start once more. The intent is then settled even though the repository
// name is already taken by a newer import.
func TestPreservedInitialDirectoryMovedAwaySettlesItsIntent(t *testing.T) {
	f := newFixture(t)
	run, directory, restore := unreadableInitialPublication(t, f)
	restore()
	ctx := context.Background()
	// A marker that no longer matches makes reconciliation preserve the
	// directory instead of removing it.
	noErr(t, os.WriteFile(filepath.Join(directory, initialMarkerName), []byte("{}\n"), 0o600))
	restartService(t, f)
	_, statErr := os.Stat(directory)
	require(t, statErr == nil, "an unproven directory was not preserved: %v", statErr)
	intent := initialIntentOf(t, f, run.ID)
	require(t, intent.Status == state.ImportIntentUnresolved, "intent of a preserved directory=%s", intent.Status)
	_, err := recovery.CreateWithReport(ctx, f.store, f.manager, filepath.Join(f.root, "backup-refused"))
	require(t, err != nil && strings.Contains(err.Error(), "move that directory out of the repository root"),
		"backup advice for a preserved directory err=%v", err)
	// A newer import takes the name while the old directory is preserved.
	_, err = f.importProject(ImportInput{})
	noErr(t, err, "new import while the old directory is preserved")
	_, err = f.service.ResolveUnresolved(ctx, "project")
	require(t, problemCode(err) == CodeUnresolved && strings.Contains(err.Error(), "move it out of the repository root"),
		"resolve of an earlier import's intent err=%v", err)

	moved := filepath.Join(f.root, "inspected")
	noErr(t, os.Rename(directory, moved))
	restartService(t, f)
	intent = initialIntentOf(t, f, run.ID)
	require(t, intent.Status == state.ImportIntentInvalidated,
		"intent after its directory was moved away=%s", intent.Status)
	status, err := f.service.Status(ctx, "project")
	require(t, err == nil && status.UnresolvedIntents == 0,
		"status after settlement unresolved=%d err=%v", status.UnresolvedIntents, err)
	_, err = recovery.CreateWithReport(ctx, f.store, f.manager, filepath.Join(f.root, "backup"))
	noErr(t, err, "backup after settlement")
	_, err = f.service.ResolveUnresolved(ctx, "project")
	require(t, problemCode(err) == CodeNothingToResolve, "resolve after settlement err=%v", errors.Unwrap(err))
}

// unreapedRefTransaction makes the next ref transaction report, after it
// committed, the joined error the prepared runner returns when its update-ref
// process was not reaped after termination. mutate first changes the
// destination refs, standing in for what that process may have left.
func unreapedRefTransaction(f *fixture, mutate func()) {
	f.service.afterPreparedRefResult = func() error {
		f.service.afterPreparedRefResult = nil
		if mutate != nil {
			mutate()
		}
		return errors.Join(context.DeadlineExceeded, fmt.Errorf("git update-ref: %w", gitexec.ErrPreparedProcessNotReaped))
	}
}

// An update-ref process that was not reaped may still run in the unpublished
// directory, so the run settles nothing from readback, whatever readback shows:
// it does not publish, mark the intent not applied, or remove the directory.
// It records the intent unresolved and reports an unresolved run. Restart
// reconciliation then settles it from a fresh observation: a matching
// directory is published and its run completes, and a directory with nothing
// or only part applied is settled as never published and removed.
func TestUnreapedRefTransactionLeavesInitialPublicationToReconciliation(t *testing.T) {
	for _, outcome := range []string{"matching", "nothing applied", "partial"} {
		t.Run(outcome, func(t *testing.T) {
			f := newFixture(t)
			completeFixtureSetup(t, f)
			first := f.commit("one", "one\n")
			f.commit("two", "two\n")
			ctx := context.Background()
			unreapedRefTransaction(f, func() {
				dirs := unpublishedInitialDirectories(t, f)
				if len(dirs) != 1 {
					t.Errorf("unpublished directories=%v", dirs)
					return
				}
				switch outcome {
				case "nothing applied":
					f.git(dirs[0], "update-ref", "-d", "refs/heads/main")
				case "partial":
					f.git(dirs[0], "update-ref", "refs/heads/main", first)
				}
			})
			f.service.beforeInitialRename = func() error {
				f.service.beforeInitialRename = nil
				t.Error("the initial directory was published while the update-ref process may be alive")
				return nil
			}
			_, err := f.importProject(ImportInput{})
			f.service.beforeInitialRename = nil
			require(t, problemCode(err) == CodeUnresolved && errors.Is(err, gitexec.ErrPreparedProcessNotReaped),
				"unreaped ref transaction err=%v", err)
			run := f.lastRun()
			require(t, run.Status == state.ImportRunUnresolved, "unreaped run=%s/%s", run.Status, run.ErrorClass)
			intent := initialIntentOf(t, f, run.ID)
			require(t, intent.Status == state.ImportIntentUnresolved &&
				strings.Contains(intent.Reason, "could not be reaped"),
				"unreaped intent=%s reason=%q, want unresolved", intent.Status, intent.Reason)
			dirs := unpublishedInitialDirectories(t, f)
			require(t, len(dirs) == 1, "unpublished directories=%v, want the kept directory", dirs)
			_, _, exists, err := f.manager.ExistingPath(ctx, "project")
			require(t, err == nil && !exists, "unreaped run created a repository exists=%v err=%v", exists, err)

			restartService(t, f)
			_, _, published, err := f.manager.ExistingPath(ctx, "project")
			require(t, err == nil && published == (outcome == "matching"),
				"after reconciliation repository exists=%v err=%v", published, err)
			dirs = unpublishedInitialDirectories(t, f)
			require(t, len(dirs) == 0, "unpublished directories after reconciliation: %v", dirs)
			status, err := f.service.Status(ctx, "project")
			require(t, err == nil && status.UnresolvedIntents == 0,
				"status after reconciliation unresolved=%d err=%v", status.UnresolvedIntents, err)
			settledRun := f.lastRun()
			settledIntent, exists, err := f.store.ImportIntent(ctx, intent.ID)
			require(t, err == nil && exists && settledRun.ID == run.ID,
				"settled run=%s intent exists=%v err=%v", settledRun.ID, exists, err)
			if outcome == "matching" {
				require(t, settledRun.Status == state.ImportRunComplete &&
					settledIntent.Status == state.ImportIntentComplete,
					"reconciled matching run=%s intent=%s, want complete", settledRun.Status, settledIntent.Status)
				return
			}
			require(t, settledRun.Status == state.ImportRunFailed &&
				settledIntent.Status == state.ImportIntentInvalidated &&
				strings.Contains(settledIntent.Reason, "nothing was published"),
				"reconciled %s run=%s/%s intent=%s reason=%q", outcome, settledRun.Status, settledRun.ErrorClass, settledIntent.Status, settledIntent.Reason)
		})
	}
}

// For a refresh the destination repository stays as the unreaped process left
// it, the intent is recorded unresolved, and a partial outcome that restart
// reconciliation still cannot prove remains for the owner to resolve.
func TestUnreapedRefreshTransactionStaysResolvable(t *testing.T) {
	f := newFixture(t)
	completeFixtureSetup(t, f)
	f.commit("one", "one\n")
	f.mustImport(ImportInput{})
	middle := f.commit("two", "two\n")
	f.commit("three", "three\n")
	ctx := context.Background()
	repositoryPath := f.destinationPath()
	unreapedRefTransaction(f, func() { f.git(repositoryPath, "update-ref", "refs/heads/main", middle) })
	run, err := f.refresh()
	require(t, problemCode(err) == CodeUnresolved && errors.Is(err, gitexec.ErrPreparedProcessNotReaped),
		"unreaped refresh err=%v", err)
	intent := initialIntentOf(t, f, run.ID)
	require(t, run.Status == state.ImportRunUnresolved && intent.Status == state.ImportIntentUnresolved,
		"unreaped refresh run=%s intent=%s", run.Status, intent.Status)
	got := strings.TrimSpace(f.git(repositoryPath, "rev-parse", "refs/heads/main"))
	require(t, got == middle, "destination main=%s, want it left at %s", got, middle)
	err = f.service.Reconcile(ctx)
	require(t, problemCode(err) == CodeUnresolved, "reconcile of a partial refresh err=%v", err)
	result, err := f.service.ResolveUnresolved(ctx, "project")
	require(t, err == nil && len(result.Resolved) == 1 && result.Resolved[0] == intent.ID,
		"resolve result=%+v err=%v", result, err)
	status, err := f.service.Status(ctx, "project")
	require(t, err == nil && status.UnresolvedIntents == 0,
		"status after resolve unresolved=%d err=%v", status.UnresolvedIntents, err)
}
