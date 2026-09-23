package importsync

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"owngit/internal/repository"
	"owngit/internal/state"
)

func TestSymbolicTagCannotWriteProtectedReferent(t *testing.T) {
	f := newFixture(t)
	f.commit("source", "source bytes\n")
	f.git(f.source, "tag", "-a", "v1", "-m", "original annotation")
	oldTag := f.git(f.source, "rev-parse", "refs/tags/v1")
	f.mustImport(ImportInput{})
	path := f.destinationPath()
	const protected = "refs/owngit/manual/keep"
	f.git(path, "--git-dir", ".", "update-ref", protected, oldTag)
	f.git(path, "--git-dir", ".", "symbolic-ref", "refs/tags/v1", protected)
	f.git(f.source, "tag", "-f", "-a", "v1", "-m", "new annotation")
	run, err := f.refresh()
	if err != nil {
		t.Fatalf("preserving a pre-existing symbolic tag: run=%+v err=%v", run, err)
	}
	if run.RefsDivergent == 0 {
		t.Fatalf("symbolic tag was not reported as divergent: %+v", run)
	}
	if got := f.git(path, "--git-dir", ".", "rev-parse", protected); got != oldTag {
		t.Fatalf("refresh wrote through symbolic tag: got=%s want=%s", got, oldTag)
	}
	if got := f.git(path, "--git-dir", ".", "symbolic-ref", "--no-recurse", "refs/tags/v1"); got != protected {
		t.Fatalf("independent symbolic tag changed: %s", got)
	}
}

func TestDanglingSymbolicDestinationRefsFailClosed(t *testing.T) {
	for _, test := range []struct {
		name string
		ref  string
	}{
		{name: "branch", ref: "refs/heads/feature"},
		{name: "tag", ref: "refs/tags/v1"},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := newFixture(t)
			first := f.commit("source", "first\n")
			if test.name == "branch" {
				f.git(f.source, "branch", "feature", first)
			} else {
				f.git(f.source, "tag", "-a", "v1", "-m", "first annotation")
			}
			f.mustImport(ImportInput{})
			if test.name == "branch" {
				second := f.commit("source", "second\n")
				f.git(f.source, "branch", "-f", "feature", second)
			} else {
				f.git(f.source, "tag", "-f", "-a", "v1", "-m", "second annotation")
			}
			path := filepath.Join(f.destinationPath(), filepath.FromSlash(test.ref))
			const target = "refs/heads/dangling-local-target"
			if err := os.WriteFile(path, []byte("ref: "+target+"\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			run, err := f.refresh()
			if err == nil || run.Status != state.ImportRunFailed {
				t.Fatalf("dangling alias run=%+v err=%v", run, err)
			}
			content, readErr := os.ReadFile(path)
			if readErr != nil || string(content) != "ref: "+target+"\n" {
				t.Fatalf("dangling alias changed: content=%q err=%v", content, readErr)
			}
		})
	}
}

func TestInvalidLooseRefContentFailsClosed(t *testing.T) {
	f := newFixture(t)
	f.commit("source", "first\n")
	f.git(f.source, "tag", "-a", "v1", "-m", "first annotation")
	f.mustImport(ImportInput{})
	f.git(f.source, "tag", "-f", "-a", "v1", "-m", "second annotation")
	path := filepath.Join(f.destinationPath(), "refs", "tags", "v1")
	if err := os.WriteFile(path, []byte("not-an-object-id\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if run, err := f.refresh(); err == nil || run.Status != state.ImportRunFailed {
		t.Fatalf("invalid loose ref run=%+v err=%v", run, err)
	}
	if content, err := os.ReadFile(path); err != nil || string(content) != "not-an-object-id\n" {
		t.Fatalf("invalid loose ref changed: content=%q err=%v", content, err)
	}
}

func TestNonRegularLooseRefPathFailsClosed(t *testing.T) {
	f := newFixture(t)
	f.commit("source", "first\n")
	f.git(f.source, "tag", "-a", "v1", "-m", "first annotation")
	f.mustImport(ImportInput{})
	f.git(f.source, "tag", "-f", "-a", "v1", "-m", "second annotation")
	path := filepath.Join(f.destinationPath(), "refs", "tags", "v1")
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	if run, err := f.refresh(); err == nil || run.Status != state.ImportRunFailed {
		t.Fatalf("nonregular loose ref run=%+v err=%v", run, err)
	}
	if info, err := os.Lstat(path); err != nil || !info.IsDir() {
		t.Fatalf("nonregular loose ref changed: info=%v err=%v", info, err)
	}
}

func TestUnsafeLooseRefPathFailsClosed(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("native reparse-point coverage is required on Windows")
	}
	f := newFixture(t)
	f.commit("source", "first\n")
	f.git(f.source, "tag", "-a", "v1", "-m", "first annotation")
	old := f.git(f.source, "rev-parse", "refs/tags/v1")
	f.mustImport(ImportInput{})
	f.git(f.source, "tag", "-f", "-a", "v1", "-m", "second annotation")
	root := f.destinationPath()
	const protected = "refs/owngit/manual/symlink-target"
	f.git(root, "--git-dir", ".", "update-ref", protected, old)
	refPath := filepath.Join(root, "refs", "tags", "v1")
	if err := os.Remove(refPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, filepath.FromSlash(protected)), refPath); err != nil {
		t.Fatal(err)
	}
	if run, err := f.refresh(); err == nil || run.Status != state.ImportRunFailed {
		t.Fatalf("unsafe loose ref run=%+v err=%v", run, err)
	}
	if got := f.git(root, "--git-dir", ".", "rev-parse", protected); got != old {
		t.Fatalf("unsafe ref path wrote through its target: got=%s want=%s", got, old)
	}
	if info, err := os.Lstat(refPath); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("unsafe ref path changed: info=%v err=%v", info, err)
	}
}

func TestCyclicSymbolicDestinationRefFailsClosed(t *testing.T) {
	f := newFixture(t)
	f.commit("source", "first\n")
	f.git(f.source, "tag", "-a", "v1", "-m", "first annotation")
	f.mustImport(ImportInput{})
	f.git(f.source, "tag", "-f", "-a", "v1", "-m", "second annotation")
	root := f.destinationPath()
	v1 := filepath.Join(root, "refs", "tags", "v1")
	v2 := filepath.Join(root, "refs", "tags", "v2")
	if err := os.WriteFile(v1, []byte("ref: refs/tags/v2\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(v2, []byte("ref: refs/tags/v1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if run, err := f.refresh(); err == nil || run.Status != state.ImportRunFailed {
		t.Fatalf("cyclic alias run=%+v err=%v", run, err)
	}
	for path, want := range map[string]string{v1: "ref: refs/tags/v2\n", v2: "ref: refs/tags/v1\n"} {
		if content, err := os.ReadFile(path); err != nil || string(content) != want {
			t.Fatalf("cyclic alias %s changed: content=%q err=%v", path, content, err)
		}
	}
}

func TestDirectRefRaceToSymbolicAbortsPreparedTransaction(t *testing.T) {
	for _, test := range []struct {
		name   string
		ref    string
		target string
		move   func(*fixture)
	}{
		{
			name: "branch",
			ref:  "refs/heads/main",
			move: func(f *fixture) { f.commit("source next", "next\n") },
		},
		{
			name: "tag",
			ref:  "refs/tags/v1",
			move: func(f *fixture) { f.git(f.source, "tag", "-f", "-a", "v1", "-m", "next annotation") },
		},
		{
			name:   "tag_dangling",
			ref:    "refs/tags/v1",
			target: "refs/heads/missing-after-plan",
			move:   func(f *fixture) { f.git(f.source, "tag", "-f", "-a", "v1", "-m", "next annotation") },
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := newFixture(t)
			f.commit("source", "source bytes\n")
			if test.ref == "refs/tags/v1" {
				f.git(f.source, "tag", "-a", "v1", "-m", "original annotation")
			}
			f.mustImport(ImportInput{})
			path := f.destinationPath()
			old := f.git(path, "--git-dir", ".", "rev-parse", test.ref)
			const protected = "refs/owngit/manual/race-keep"
			f.git(path, "--git-dir", ".", "update-ref", protected, old)
			test.move(f)
			aliasTarget := test.target
			if aliasTarget == "" {
				aliasTarget = protected
			}
			f.service.beforeRefTransaction = func() {
				f.service.beforeRefTransaction = nil
				f.git(path, "--git-dir", ".", "symbolic-ref", test.ref, aliasTarget)
			}
			run, err := f.refresh()
			// The prepared transaction aborts in every case. Exact readback then
			// finds a symbolic alias where a direct ref was expected. A resolved
			// alias to the old object is not the untouched direct ref, so the
			// outcome is unresolved rather than a proven not-applied failure.
			if err == nil || run.Status != state.ImportRunUnresolved || problemCode(err) != CodeUnresolved {
				t.Fatalf("symbolic race run=%+v err=%v", run, err)
			}
			if test.target == "" && !strings.Contains(err.Error(), "became symbolic") {
				t.Fatalf("resolved alias race was not caught while prepared: %v", err)
			}
			if got := f.git(path, "--git-dir", ".", "rev-parse", protected); got != old {
				t.Fatalf("prepared transaction wrote through alias: got=%s want=%s", got, old)
			}
			if got := f.git(path, "--git-dir", ".", "symbolic-ref", "--no-recurse", test.ref); got != aliasTarget {
				t.Fatalf("prepared transaction replaced symbolic ref: got=%s want=%s", got, aliasTarget)
			}
			if test.ref == "refs/tags/v1" {
				for _, retention := range []string{
					repository.RetainedRefName("tags", old),
					repository.ProvenanceRefName("tags", "v1", old),
				} {
					if _, retentionErr := f.manager.Git.Run(context.Background(), path, nil, "--git-dir", ".", "rev-parse", "--verify", retention); retentionErr == nil {
						t.Fatalf("aborted transaction created retention ref %s", retention)
					}
				}
			}
			// Both the rejected update and unrelated protected ref remain writable;
			// the runner did not leak Git's prepared locks.
			f.git(path, "--git-dir", ".", "symbolic-ref", test.ref, protected)
			f.git(path, "--git-dir", ".", "update-ref", "--no-deref", protected, old, old)
		})
	}
}

func TestLostPreparedCommitResultUsesExactReadback(t *testing.T) {
	f := newFixture(t)
	f.commit("source", "initial\n")
	initial := f.mustImport(ImportInput{})
	markHEADOwnedForTest(t, f, initial.Run.ID)
	want := f.commit("source", "next\n")
	f.service.afterPreparedRefResult = func() error {
		f.service.afterPreparedRefResult = nil
		return errors.New("synthetic lost commit acknowledgement")
	}
	run, err := f.refresh()
	if err != nil || run.Status != state.ImportRunComplete {
		t.Fatalf("lost acknowledgement run=%+v err=%v", run, err)
	}
	if got := f.git(f.destinationPath(), "--git-dir", ".", "rev-parse", "refs/heads/main"); got != want {
		t.Fatalf("readback hid committed ref: got=%s want=%s", got, want)
	}
	intent, exists, err := f.store.CompletedImportIntentForRun(context.Background(), run.ID)
	if err != nil || !exists || !intent.HeadOwned {
		t.Fatalf("completed readback intent=%+v exists=%v err=%v", intent, exists, err)
	}
}

func TestPreparedCancellationAbortsAndReleasesLocks(t *testing.T) {
	f := newFixture(t)
	old := f.commit("source", "initial\n")
	f.mustImport(ImportInput{})
	f.commit("source", "next\n")
	ctx, cancel := context.WithCancel(context.Background())
	f.service.whileRefsPrepared = func() {
		f.service.whileRefsPrepared = nil
		cancel()
	}
	if run, err := f.service.Refresh(ctx, "project", Limits{}); err == nil || (run.Status != state.ImportRunFailed && run.Status != state.ImportRunCancelled) {
		t.Fatalf("cancelled prepared run=%+v err=%v", run, err)
	}
	path := f.destinationPath()
	if got := f.git(path, "--git-dir", ".", "rev-parse", "refs/heads/main"); got != old {
		t.Fatalf("cancelled prepared transaction committed: got=%s want=%s", got, old)
	}
	f.git(path, "--git-dir", ".", "update-ref", "--no-deref", "refs/heads/main", old, old)
}

func TestPreparedTransactionLocksUnchangedRequiredRetention(t *testing.T) {
	f := newFixture(t)
	f.commit("source", "source bytes\n")
	f.git(f.source, "tag", "-a", "v1", "-m", "original annotation")
	oldTag := f.git(f.source, "rev-parse", "refs/tags/v1")
	f.mustImport(ImportInput{})
	path := f.destinationPath()
	retention := repository.RetainedRefName("tags", oldTag)
	const protected = "refs/owngit/manual/prepared-keep"
	f.git(path, "--git-dir", ".", "update-ref", retention, oldTag)
	f.git(path, "--git-dir", ".", "update-ref", protected, oldTag)
	f.git(f.source, "tag", "-f", "-a", "v1", "-m", "replacement annotation")
	f.service.whileRefsPrepared = func() {
		f.service.whileRefsPrepared = nil
		if _, err := f.manager.Git.Run(context.Background(), path, nil, "--git-dir", ".", "-c", "core.filesRefLockTimeout=0", "symbolic-ref", retention, protected); err == nil {
			t.Error("verify-only retention ref was not locked during prepare")
		}
	}
	if run, err := f.refresh(); err != nil || run.Status != state.ImportRunComplete {
		t.Fatalf("prepared retention publication run=%+v err=%v", run, err)
	}
	if got := f.git(path, "--git-dir", ".", "rev-parse", retention); got != oldTag {
		t.Fatalf("retention changed: got=%s want=%s", got, oldTag)
	}
}

func TestSymbolicRetentionCollisionStopsBeforePublication(t *testing.T) {
	f := newFixture(t)
	f.commit("source", "source bytes\n")
	f.git(f.source, "tag", "-a", "v1", "-m", "original annotation")
	oldTag := f.git(f.source, "rev-parse", "refs/tags/v1")
	f.mustImport(ImportInput{})
	path := f.destinationPath()
	const protected = "refs/owngit/manual/retention-keep"
	f.git(path, "--git-dir", ".", "update-ref", protected, oldTag)
	retention := repository.RetainedRefName("tags", oldTag)
	f.git(path, "--git-dir", ".", "symbolic-ref", retention, protected)
	f.git(f.source, "tag", "-f", "-a", "v1", "-m", "replacement annotation")
	if run, err := f.refresh(); err == nil || run.Status != state.ImportRunFailed {
		t.Fatalf("symbolic retention collision run=%+v err=%v", run, err)
	}
	if got := f.git(path, "--git-dir", ".", "symbolic-ref", "--no-recurse", retention); got != protected {
		t.Fatalf("symbolic retention identity changed: %s", got)
	}
	if got := f.git(path, "--git-dir", ".", "rev-parse", protected); got != oldTag {
		t.Fatalf("symbolic retention referent changed: got=%s want=%s", got, oldTag)
	}
}

func TestHistoricalHEADOwnershipSurvivesAuthorityRevision(t *testing.T) {
	f := newFixture(t)
	oid := f.commit("source", "source bytes\n")
	f.git(f.source, "branch", "dev", oid)
	initial := f.mustImport(ImportInput{})
	markHEADOwnedForTest(t, f, initial.Run.ID)
	source, exists, err := f.store.ImportSource(context.Background(), "project")
	if err != nil || !exists {
		t.Fatalf("source exists=%v err=%v", exists, err)
	}
	if _, err := f.service.ConfigureSource(context.Background(), ConfigureInput{
		RepositoryID: "project", URL: source.URL, Mode: ModeCoexistence,
		GitOnlyConsent: !source.GitOnlyConsent, AllowPrivateNetwork: source.AllowPrivateNetwork,
	}); err != nil {
		t.Fatal(err)
	}
	f.git(f.source, "symbolic-ref", "HEAD", "refs/heads/dev")
	if _, err := f.refresh(); err != nil {
		t.Fatal(err)
	}
	if got := f.git(f.destinationPath(), "--git-dir", ".", "symbolic-ref", "--no-recurse", "HEAD"); got != "refs/heads/dev" {
		t.Fatalf("historical ownership was tied to current authority revision: %s", got)
	}
}

func TestHEADCommitWithoutPersistedProofDoesNotGrantOwnership(t *testing.T) {
	f := newFixture(t)
	f.commit("source", "source bytes\n")
	initial := f.mustImport(ImportInput{})
	markHEADOwnedForTest(t, f, initial.Run.ID)
	f.git(f.source, "symbolic-ref", "HEAD", "refs/heads/future")
	if err := f.store.Exec(context.Background(), `CREATE TRIGGER fail_head_owned BEFORE UPDATE OF head_owned ON import_publication_intents
		WHEN NEW.head_owned=1 AND OLD.head_owned=0 BEGIN SELECT RAISE(FAIL,'synthetic ownership persistence failure'); END`); err != nil {
		t.Fatal(err)
	}
	run, err := f.refresh()
	if err == nil || problemCode(err) != CodeStateUnavailable || run.Status != state.ImportRunFailed {
		t.Fatalf("HEAD ownership persistence run=%+v err=%v", run, err)
	}
	if got := f.git(f.destinationPath(), "--git-dir", ".", "symbolic-ref", "--no-recurse", "HEAD"); got != "refs/heads/future" {
		t.Fatalf("successful HEAD commit was hidden: %s", got)
	}
	intents, queryErr := f.store.PendingImportIntents(context.Background(), "project")
	if queryErr != nil || len(intents) != 1 || intents[0].HeadOwned {
		t.Fatalf("failed ownership proof became authority: intents=%+v err=%v", intents, queryErr)
	}
	if err := f.store.Exec(context.Background(), `DROP TRIGGER fail_head_owned`); err != nil {
		t.Fatal(err)
	}
	if err := f.service.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	f.git(f.source, "symbolic-ref", "HEAD", "refs/heads/future-next")
	if _, err := f.refresh(); err != nil {
		t.Fatal(err)
	}
	if got := f.git(f.destinationPath(), "--git-dir", ".", "symbolic-ref", "--no-recurse", "HEAD"); got != "refs/heads/future" {
		t.Fatalf("reconciliation invented lost ownership proof: %s", got)
	}
}

func TestDiagnosticTextCannotGrantHEADOwnership(t *testing.T) {
	f := newFixture(t)
	oid := f.commit("source", "source bytes\n")
	f.mustImport(ImportInput{})
	ctx := context.Background()
	run := f.lastRun()
	intent, exists, err := f.store.CompletedImportIntentForRun(ctx, run.ID)
	if err != nil || !exists {
		t.Fatalf("initial intent=%+v exists=%v err=%v", intent, exists, err)
	}
	// An initial destination created by this run now records structured HEAD
	// ownership. The update below clears that field. Diagnostic text must not
	// put it back.
	reason := "read /synthetic/destination HEAD ownership proven by an applied publication/repository: permission denied"
	if err := f.store.UpdateImportIntent(ctx, intent.ID, state.ImportIntentUnresolved, "", "", reason, f.now); err != nil {
		t.Fatal(err)
	}
	run.Status = state.ImportRunUnresolved
	run.ErrorClass = CodeUnresolved
	if err := f.store.FinishImportRun(ctx, run); err != nil {
		t.Fatal(err)
	}
	if err := f.service.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	f.git(f.source, "branch", "source-next", oid)
	f.git(f.source, "symbolic-ref", "HEAD", "refs/heads/source-next")
	if _, err := f.refresh(); err != nil {
		t.Fatal(err)
	}
	if got := f.git(f.destinationPath(), "--git-dir", ".", "symbolic-ref", "--no-recurse", "HEAD"); got != "refs/heads/main" {
		t.Fatalf("diagnostic path text granted HEAD ownership: got=%s", got)
	}
}
