package importsync

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"owngit/internal/state"
)

// unresolvedHEADRefresh leaves the repository with one unresolved intent: the
// ref transaction of a refresh landed, and HEAD stayed on the old branch.
func unresolvedHEADRefresh(t *testing.T) (*fixture, state.ImportRun, string) {
	t.Helper()
	f := newFixture(t)
	f.commit("initial", "initial\n")
	f.git(f.source, "branch", "dev")
	initial := f.mustImport(ImportInput{})
	markHEADOwnedForTest(t, f, initial.Run.ID)
	f.git(f.source, "checkout", "--quiet", "dev")
	newDev := f.commit("dev next", "next\n")
	if err := f.store.Exec(context.Background(), `CREATE TRIGGER fail_applied BEFORE UPDATE ON import_publication_intents
		WHEN NEW.status='applied' BEGIN SELECT RAISE(FAIL,'synthetic applied failure'); END`); err != nil {
		t.Fatal(err)
	}
	run, err := f.refresh()
	require(t, err != nil && problemCode(err) == CodeUnresolved && run.Status == state.ImportRunUnresolved,
		"mixed outcome run=%+v err=%v", run, err)
	noErr(t, f.store.Exec(context.Background(), `DROP TRIGGER fail_applied`))
	return f, run, newDev
}

// Without the owner action, an unresolved publication keeps every later
// refresh refused. Owner resolution records the destination as found, keeps
// the unresolved run truthful, and lets the next refresh plan from it.
func TestOwnerResolutionLetsTheNextRefreshPlanFromTheDestination(t *testing.T) {
	f, unresolvedRun, newDev := unresolvedHEADRefresh(t)
	ctx := context.Background()
	path := f.destinationPath()
	before := f.destinationRefs()
	_, err := f.refresh()
	require(t, problemCode(err) == CodeUnresolved, "refresh before owner resolution err=%v", err)
	eq(t, "refused refresh changed HEAD", f.git(path, "symbolic-ref", "HEAD"), "refs/heads/main")

	result, err := f.service.ResolveUnresolved(ctx, "project")
	require(t, err == nil && len(result.Resolved) == 1, "resolve result=%+v err=%v", result, err)
	status, err := f.service.Status(ctx, "project")
	require(t, err == nil && status.UnresolvedIntents == 0,
		"status after resolve unresolved=%d err=%v", status.UnresolvedIntents, err)
	intent, exists, err := f.store.ImportIntent(ctx, result.Resolved[0])
	require(t, err == nil && exists && intent.Status == state.ImportIntentOwnerResolved &&
		strings.HasPrefix(intent.Reason, "owner resolved"), "resolved intent=%+v exists=%v err=%v", intent, exists, err)
	var receipt map[string]string
	noErr(t, json.Unmarshal([]byte(intent.ReceiptJSON), &receipt))
	require(t, receipt["refs/heads/dev"] == newDev &&
		strings.HasPrefix(receipt[state.ImportHeadRef], "symbolic refs/heads/main ") &&
		intent.ReceiptDigest == state.ImportReceiptDigest(intent.ReceiptJSON), "owner receipt=%v", receipt)
	// No Git write, replay, or rollback happened.
	after := f.destinationRefs()
	require(t, len(after) == len(before), "owner resolution changed refs: before=%v after=%v", before, after)
	for ref, oid := range before {
		require(t, after[ref] == oid, "owner resolution changed %s: %s -> %s", ref, oid, after[ref])
	}
	eq(t, "owner resolution changed HEAD", f.git(path, "symbolic-ref", "HEAD"), "refs/heads/main")
	stored, exists, err := f.store.ImportRun(ctx, unresolvedRun.ID)
	require(t, err == nil && exists && stored.Status == state.ImportRunUnresolved && stored.ErrorClass == CodeUnresolved,
		"unresolved run was rewritten: %+v exists=%v err=%v", stored, exists, err)
	_, err = f.service.ResolveUnresolved(ctx, "project")
	require(t, problemCode(err) == CodeNothingToResolve, "second resolution err=%v", err)

	run, err := f.refresh()
	require(t, err == nil && run.Status == state.ImportRunComplete,
		"refresh after owner resolution run=%+v err=%v", run, err)
	eq(t, "refresh after owner resolution HEAD", f.git(path, "symbolic-ref", "HEAD"), "refs/heads/dev")
	got := f.destinationRefs()["refs/heads/dev"]
	require(t, got == newDev, "refresh after owner resolution dev=%s want %s", got, newDev)
	// The owner decision is preserved and never reinterpreted as complete.
	intent, _, _ = f.store.ImportIntent(ctx, result.Resolved[0])
	require(t, intent.Status == state.ImportIntentOwnerResolved, "owner-resolved intent became %s", intent.Status)
	noErr(t, f.service.Reconcile(ctx), "reconcile after owner resolution")
}

func TestOwnerResolutionRefusals(t *testing.T) {
	ctx := context.Background()
	t.Run("nothing unresolved", func(t *testing.T) {
		f := newFixture(t)
		f.commit("initial", "initial\n")
		f.mustImport(ImportInput{})
		_, err := f.service.ResolveUnresolved(ctx, "project")
		require(t, problemCode(err) == CodeNothingToResolve, "resolution without an unresolved intent err=%v", err)
	})
	t.Run("missing repository", func(t *testing.T) {
		f := newFixture(t)
		_, err := f.service.ResolveUnresolved(ctx, "project")
		require(t, problemCode(err) == CodeRepositoryMissing, "resolution without a repository err=%v", err)
	})
	t.Run("active run", func(t *testing.T) {
		f, _, _ := unresolvedHEADRefresh(t)
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
			t.Fatal("refresh did not become active")
		}
		_, err := f.service.ResolveUnresolved(ctx, "project")
		close(f.transport.gate)
		<-finished
		require(t, problemCode(err) == CodeBusy, "resolution during an active run err=%v", err)
		count, _ := f.store.UnresolvedImportIntentCount(ctx, "project")
		require(t, count == 1, "refused resolution changed the intent: unresolved=%d", count)
	})
	t.Run("unreadable destination", func(t *testing.T) {
		f, _, _ := unresolvedHEADRefresh(t)
		headPath := filepath.Join(f.destinationPath(), "HEAD")
		original, err := os.ReadFile(headPath)
		noErr(t, err)
		noErr(t, os.WriteFile(headPath, []byte("ref: refs/tags/not-a-branch\n"), 0o644))
		_, resolveErr := f.service.ResolveUnresolved(ctx, "project")
		noErr(t, os.WriteFile(headPath, original, 0o644))
		require(t, resolveErr != nil, "resolution accepted an unreadable destination HEAD")
		count, _ := f.store.UnresolvedImportIntentCount(ctx, "project")
		require(t, count == 1, "refused resolution changed the intent: unresolved=%d", count)
	})
}

// The startup reconciliation problem that reported this repository's
// unresolved publication clears once the owner resolves it. Other startup
// problems stay visible.
func TestOwnerResolutionClearsOnlyItsStartupProblem(t *testing.T) {
	f, _, _ := unresolvedHEADRefresh(t)
	ctx := context.Background()
	reconcileErr := f.service.Reconcile(ctx)
	require(t, problemCode(reconcileErr) == CodeUnresolved, "startup reconciliation err=%v", reconcileErr)
	f.service.NoteStartupFailure(reconcileErr)
	status, err := f.service.Status(ctx, "project")
	require(t, err == nil && status.Runtime.Code == CodeUnresolved,
		"runtime before resolution=%+v err=%v", status.Runtime, err)
	_, err = f.service.ResolveUnresolved(ctx, "project")
	noErr(t, err)
	status, err = f.service.Status(ctx, "project")
	require(t, err == nil && status.Runtime.Code == "", "runtime after resolution=%+v err=%v", status.Runtime, err)

	other := newProblem(CodeStateUnavailable, "staging could not be read", nil)
	f.service.NoteStartupFailure(errors.Join(other, &repositoryReconcileError{repositoryID: "project", err: newProblem(CodeUnresolved, "prior publication remains partial", nil)}))
	f.service.forgetResolvedStartupProblem("project")
	status, _ = f.service.Status(ctx, "project")
	require(t, status.Runtime.Code == CodeStateUnavailable,
		"an unrelated startup problem was cleared: %+v", status.Runtime)
}

// Resolution waits for another repository writer only until the request
// deadline, then refuses without recording anything.
func TestOwnerResolutionHonoursTheRequestDeadline(t *testing.T) {
	f, _, _ := unresolvedHEADRefresh(t)
	lock := f.manager.Locks.For("project")
	lock.RLock()
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	started := time.Now()
	_, err := f.service.ResolveUnresolved(ctx, "project")
	lock.RUnlock()
	require(t, problemCode(err) == CodeBusy && time.Since(started) <= 5*time.Second,
		"resolve behind a writer after %s err=%v", time.Since(started), err)
	status, err := f.service.Status(context.Background(), "project")
	require(t, err == nil && status.UnresolvedIntents == 1,
		"a refused resolution recorded something: unresolved=%d err=%v", status.UnresolvedIntents, err)
	_, err = f.service.ResolveUnresolved(context.Background(), "project")
	noErr(t, err, "resolve after the writer finished")
}
