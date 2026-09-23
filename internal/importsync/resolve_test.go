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
	if err == nil || problemCode(err) != CodeUnresolved || run.Status != state.ImportRunUnresolved {
		t.Fatalf("mixed outcome run=%+v err=%v", run, err)
	}
	if err := f.store.Exec(context.Background(), `DROP TRIGGER fail_applied`); err != nil {
		t.Fatal(err)
	}
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
	if _, err := f.refresh(); problemCode(err) != CodeUnresolved {
		t.Fatalf("refresh before owner resolution err=%v", err)
	}
	if got := f.git(path, "symbolic-ref", "HEAD"); got != "refs/heads/main" {
		t.Fatalf("refused refresh changed HEAD: %s", got)
	}

	result, err := f.service.ResolveUnresolved(ctx, "project")
	if err != nil || len(result.Resolved) != 1 || result.Status.UnresolvedIntents != 0 {
		t.Fatalf("resolve result=%+v err=%v", result, err)
	}
	intent, exists, err := f.store.ImportIntent(ctx, result.Resolved[0])
	if err != nil || !exists || intent.Status != state.ImportIntentOwnerResolved || !strings.HasPrefix(intent.Reason, "owner resolved") {
		t.Fatalf("resolved intent=%+v exists=%v err=%v", intent, exists, err)
	}
	var receipt map[string]string
	if err := json.Unmarshal([]byte(intent.ReceiptJSON), &receipt); err != nil {
		t.Fatal(err)
	}
	if receipt["refs/heads/dev"] != newDev || !strings.HasPrefix(receipt[state.ImportHeadRef], "symbolic refs/heads/main ") || intent.ReceiptDigest != state.ImportReceiptDigest(intent.ReceiptJSON) {
		t.Fatalf("owner receipt=%v", receipt)
	}
	// No Git write, replay, or rollback happened.
	after := f.destinationRefs()
	if len(after) != len(before) {
		t.Fatalf("owner resolution changed refs: before=%v after=%v", before, after)
	}
	for ref, oid := range before {
		if after[ref] != oid {
			t.Fatalf("owner resolution changed %s: %s -> %s", ref, oid, after[ref])
		}
	}
	if got := f.git(path, "symbolic-ref", "HEAD"); got != "refs/heads/main" {
		t.Fatalf("owner resolution changed HEAD: %s", got)
	}
	stored, exists, err := f.store.ImportRun(ctx, unresolvedRun.ID)
	if err != nil || !exists || stored.Status != state.ImportRunUnresolved || stored.ErrorClass != CodeUnresolved {
		t.Fatalf("unresolved run was rewritten: %+v exists=%v err=%v", stored, exists, err)
	}
	if _, err := f.service.ResolveUnresolved(ctx, "project"); problemCode(err) != CodeNothingToResolve {
		t.Fatalf("second resolution err=%v", err)
	}

	run, err := f.refresh()
	if err != nil || run.Status != state.ImportRunComplete {
		t.Fatalf("refresh after owner resolution run=%+v err=%v", run, err)
	}
	if got := f.git(path, "symbolic-ref", "HEAD"); got != "refs/heads/dev" {
		t.Fatalf("refresh after owner resolution HEAD=%s", got)
	}
	if got := f.destinationRefs()["refs/heads/dev"]; got != newDev {
		t.Fatalf("refresh after owner resolution dev=%s want %s", got, newDev)
	}
	// The owner decision is preserved and never reinterpreted as complete.
	intent, _, _ = f.store.ImportIntent(ctx, result.Resolved[0])
	if intent.Status != state.ImportIntentOwnerResolved {
		t.Fatalf("owner-resolved intent became %s", intent.Status)
	}
	if err := f.service.Reconcile(ctx); err != nil {
		t.Fatalf("reconcile after owner resolution: %v", err)
	}
}

func TestOwnerResolutionRefusals(t *testing.T) {
	ctx := context.Background()
	t.Run("nothing unresolved", func(t *testing.T) {
		f := newFixture(t)
		f.commit("initial", "initial\n")
		f.mustImport(ImportInput{})
		if _, err := f.service.ResolveUnresolved(ctx, "project"); problemCode(err) != CodeNothingToResolve {
			t.Fatalf("resolution without an unresolved intent err=%v", err)
		}
	})
	t.Run("missing repository", func(t *testing.T) {
		f := newFixture(t)
		if _, err := f.service.ResolveUnresolved(ctx, "project"); problemCode(err) != CodeRepositoryMissing {
			t.Fatalf("resolution without a repository err=%v", err)
		}
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
		if problemCode(err) != CodeBusy {
			t.Fatalf("resolution during an active run err=%v", err)
		}
		if count, _ := f.store.UnresolvedImportIntentCount(ctx, "project"); count != 1 {
			t.Fatalf("refused resolution changed the intent: unresolved=%d", count)
		}
	})
	t.Run("unreadable destination", func(t *testing.T) {
		f, _, _ := unresolvedHEADRefresh(t)
		headPath := filepath.Join(f.destinationPath(), "HEAD")
		original, err := os.ReadFile(headPath)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(headPath, []byte("ref: refs/tags/not-a-branch\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		_, resolveErr := f.service.ResolveUnresolved(ctx, "project")
		if err := os.WriteFile(headPath, original, 0o644); err != nil {
			t.Fatal(err)
		}
		if resolveErr == nil {
			t.Fatal("resolution accepted an unreadable destination HEAD")
		}
		if count, _ := f.store.UnresolvedImportIntentCount(ctx, "project"); count != 1 {
			t.Fatalf("refused resolution changed the intent: unresolved=%d", count)
		}
	})
}

// The startup reconciliation problem that reported this repository's
// unresolved publication clears once the owner resolves it. Other startup
// problems stay visible.
func TestOwnerResolutionClearsOnlyItsStartupProblem(t *testing.T) {
	f, _, _ := unresolvedHEADRefresh(t)
	ctx := context.Background()
	reconcileErr := f.service.Reconcile(ctx)
	if problemCode(reconcileErr) != CodeUnresolved {
		t.Fatalf("startup reconciliation err=%v", reconcileErr)
	}
	f.service.NoteStartupFailure(reconcileErr)
	status, err := f.service.Status(ctx, "project")
	if err != nil || status.Runtime.Code != CodeUnresolved {
		t.Fatalf("runtime before resolution=%+v err=%v", status.Runtime, err)
	}
	if _, err := f.service.ResolveUnresolved(ctx, "project"); err != nil {
		t.Fatal(err)
	}
	status, err = f.service.Status(ctx, "project")
	if err != nil || status.Runtime.Code != "" {
		t.Fatalf("runtime after resolution=%+v err=%v", status.Runtime, err)
	}

	other := newProblem(CodeStateUnavailable, "staging could not be read", nil)
	f.service.NoteStartupFailure(errors.Join(other, &repositoryReconcileError{repositoryID: "project", err: newProblem(CodeUnresolved, "prior publication remains partial", nil)}))
	f.service.forgetResolvedStartupProblem("project")
	if status, _ := f.service.Status(ctx, "project"); status.Runtime.Code != CodeStateUnavailable {
		t.Fatalf("an unrelated startup problem was cleared: %+v", status.Runtime)
	}
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
	if problemCode(err) != CodeBusy || time.Since(started) > 5*time.Second {
		t.Fatalf("resolve behind a writer after %s err=%v", time.Since(started), err)
	}
	if status, err := f.service.Status(context.Background(), "project"); err != nil || status.UnresolvedIntents != 1 {
		t.Fatalf("a refused resolution recorded something: unresolved=%d err=%v", status.UnresolvedIntents, err)
	}
	if _, err := f.service.ResolveUnresolved(context.Background(), "project"); err != nil {
		t.Fatalf("resolve after the writer finished: %v", err)
	}
}
