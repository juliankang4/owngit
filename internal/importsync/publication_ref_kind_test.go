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
	require(t, err == nil && run.RefsDivergent != 0,
		"symbolic tag was not preserved and reported as divergent: run=%+v err=%v", run, err)
	eq(t, "referent after refresh", f.rev(path, protected), oldTag)
	eq(t, "symbolic tag", f.symref(path, "refs/tags/v1"), protected)
}

// A destination ref that is not a plain direct ref (a dangling or cyclic
// alias, garbage content, a directory, a link) stops the refresh before it
// writes, and the independent content stays as it was.
func TestUnsafeDestinationRefsFailClosed(t *testing.T) {
	for _, test := range []struct {
		name    string
		arrange func(t *testing.T, f *fixture, root string) (verify func())
	}{
		{"dangling branch alias", func(t *testing.T, _ *fixture, root string) func() {
			p := filepath.Join(root, "refs", "heads", "feature")
			noErr(t, os.WriteFile(p, []byte("ref: refs/heads/dangling-local-target\n"), 0o600))
			return func() { fileIs(t, p, "ref: refs/heads/dangling-local-target\n") }
		}},
		{"dangling tag alias", func(t *testing.T, _ *fixture, root string) func() {
			p := filepath.Join(root, "refs", "tags", "v1")
			noErr(t, os.WriteFile(p, []byte("ref: refs/heads/dangling-local-target\n"), 0o600))
			return func() { fileIs(t, p, "ref: refs/heads/dangling-local-target\n") }
		}},
		{"invalid content", func(t *testing.T, _ *fixture, root string) func() {
			p := filepath.Join(root, "refs", "tags", "v1")
			noErr(t, os.WriteFile(p, []byte("not-an-object-id\n"), 0o600))
			return func() { fileIs(t, p, "not-an-object-id\n") }
		}},
		{"directory", func(t *testing.T, _ *fixture, root string) func() {
			p := filepath.Join(root, "refs", "tags", "v1")
			noErr(t, os.Remove(p))
			noErr(t, os.Mkdir(p, 0o700))
			return func() {
				info, err := os.Lstat(p)
				require(t, err == nil && info.IsDir(), "directory ref changed: info=%v err=%v", info, err)
			}
		}},
		{"symlink to a protected ref", func(t *testing.T, f *fixture, root string) func() {
			if runtime.GOOS == "windows" {
				t.Skip("native reparse-point coverage is required on Windows")
			}
			const protected = "refs/owngit/manual/symlink-target"
			old := f.rev(root, "refs/tags/v1")
			f.git(root, "--git-dir", ".", "update-ref", protected, old)
			p := filepath.Join(root, "refs", "tags", "v1")
			noErr(t, os.Remove(p))
			noErr(t, os.Symlink(filepath.Join(root, filepath.FromSlash(protected)), p))
			return func() {
				eq(t, "protected ref written through the link", f.rev(root, protected), old)
				isSymlink(t, p)
			}
		}},
		{"cyclic aliases", func(t *testing.T, _ *fixture, root string) func() {
			v1, v2 := filepath.Join(root, "refs", "tags", "v1"), filepath.Join(root, "refs", "tags", "v2")
			noErr(t, os.WriteFile(v1, []byte("ref: refs/tags/v2\n"), 0o600))
			noErr(t, os.WriteFile(v2, []byte("ref: refs/tags/v1\n"), 0o600))
			return func() {
				fileIs(t, v1, "ref: refs/tags/v2\n")
				fileIs(t, v2, "ref: refs/tags/v1\n")
			}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := newFixture(t)
			first := f.commit("source", "first\n")
			f.git(f.source, "branch", "feature", first)
			f.git(f.source, "tag", "-a", "v1", "-m", "first annotation")
			f.mustImport(ImportInput{})
			f.git(f.source, "branch", "-f", "feature", f.commit("source", "second\n"))
			f.git(f.source, "tag", "-f", "-a", "v1", "-m", "second annotation")
			verify := test.arrange(t, f, f.destinationPath())
			f.refreshFails("", state.ImportRunFailed)
			verify()
		})
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
			// The prepared transaction aborts in every case. Exact readback then
			// finds a symbolic alias where a direct ref was expected. A resolved
			// alias to the old object is not the untouched direct ref, so the
			// outcome is unresolved rather than a proven not-applied failure.
			run, err := f.refresh()
			require(t, err != nil && run.Status == state.ImportRunUnresolved && problemCode(err) == CodeUnresolved,
				"symbolic race run=%+v err=%v", run, err)
			require(t, test.target != "" || strings.Contains(err.Error(), "became symbolic"),
				"resolved alias race was not caught while prepared: %v", err)
			eq(t, "alias referent", f.rev(path, protected), old)
			eq(t, "symbolic ref", f.symref(path, test.ref), aliasTarget)
			if test.ref == "refs/tags/v1" {
				for _, retention := range []string{
					repository.RetainedRefName("tags", old),
					repository.ProvenanceRefName("tags", "v1", old),
				} {
					_, retentionErr := f.manager.Git.Run(context.Background(), path, nil, "--git-dir", ".", "rev-parse", "--verify", retention)
					require(t, retentionErr != nil, "aborted transaction created retention ref %s", retention)
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
	f.importOwned()
	want := f.commit("source", "next\n")
	f.service.afterPreparedRefResult = func() error {
		f.service.afterPreparedRefResult = nil
		return errors.New("synthetic lost commit acknowledgement")
	}
	run, err := f.refresh()
	require(t, err == nil && run.Status == state.ImportRunComplete, "lost acknowledgement run=%+v err=%v", run, err)
	eq(t, "committed ref", f.rev(f.destinationPath(), "refs/heads/main"), want)
	intent, exists, err := f.store.CompletedImportIntentForRun(context.Background(), run.ID)
	require(t, err == nil && exists && intent.HeadOwned,
		"completed readback intent=%+v exists=%v err=%v", intent, exists, err)
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
	run, err := f.refresh()
	require(t, err == nil && run.Status == state.ImportRunComplete,
		"prepared retention publication run=%+v err=%v", run, err)
	eq(t, "retention", f.rev(path, retention), oldTag)
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
	f.refreshFails("", state.ImportRunFailed)
	eq(t, "symbolic retention identity", f.symref(path, retention), protected)
	eq(t, "symbolic retention referent", f.rev(path, protected), oldTag)
}

func TestHistoricalHEADOwnershipSurvivesAuthorityRevision(t *testing.T) {
	f := newFixture(t)
	oid := f.commit("source", "source bytes\n")
	f.git(f.source, "branch", "dev", oid)
	f.importOwned()
	source, exists, err := f.store.ImportSource(context.Background(), "project")
	require(t, err == nil && exists, "source exists=%v err=%v", exists, err)
	_, err = f.service.ConfigureSource(context.Background(), ConfigureInput{
		RepositoryID: "project", URL: source.URL, Mode: ModeCoexistence,
		GitOnlyConsent: !source.GitOnlyConsent, AllowPrivateNetwork: source.AllowPrivateNetwork,
	})
	noErr(t, err)
	f.git(f.source, "symbolic-ref", "HEAD", "refs/heads/dev")
	_, err = f.refresh()
	noErr(t, err)
	eq(t, "HEAD after an authority revision", f.headRef(f.destinationPath()), "refs/heads/dev")
}

func TestHEADCommitWithoutPersistedProofDoesNotGrantOwnership(t *testing.T) {
	f := newFixture(t)
	f.commit("source", "source bytes\n")
	f.importOwned()
	f.git(f.source, "symbolic-ref", "HEAD", "refs/heads/future")
	noErr(t, f.store.Exec(context.Background(), `CREATE TRIGGER fail_head_owned BEFORE UPDATE OF head_owned ON import_publication_intents
		WHEN NEW.head_owned=1 AND OLD.head_owned=0 BEGIN SELECT RAISE(FAIL,'synthetic ownership persistence failure'); END`))
	f.refreshFails(CodeStateUnavailable, state.ImportRunFailed)
	eq(t, "HEAD after the hidden commit", f.headRef(f.destinationPath()), "refs/heads/future")
	intents, err := f.store.PendingImportIntents(context.Background(), "project")
	require(t, err == nil && len(intents) == 1 && !intents[0].HeadOwned,
		"failed ownership proof became authority: intents=%+v err=%v", intents, err)
	noErr(t, f.store.Exec(context.Background(), `DROP TRIGGER fail_head_owned`))
	noErr(t, f.service.Reconcile(context.Background()))
	f.git(f.source, "symbolic-ref", "HEAD", "refs/heads/future-next")
	_, err = f.refresh()
	noErr(t, err)
	eq(t, "HEAD after reconciliation", f.headRef(f.destinationPath()), "refs/heads/future")
}

func TestDiagnosticTextCannotGrantHEADOwnership(t *testing.T) {
	f := newFixture(t)
	oid := f.commit("source", "source bytes\n")
	f.mustImport(ImportInput{})
	ctx := context.Background()
	run := f.lastRun()
	intent, exists, err := f.store.CompletedImportIntentForRun(ctx, run.ID)
	require(t, err == nil && exists, "initial intent=%+v exists=%v err=%v", intent, exists, err)
	// Owner acceptance clears structured ownership. Interruption alone keeps
	// historical proof, but diagnostic text must never recreate revoked proof.
	noErr(t, f.store.UpdateImportIntent(ctx, intent.ID, state.ImportIntentUnresolved, "", "", "", f.now))
	run.Status = state.ImportRunUnresolved
	run.ErrorClass = CodeUnresolved
	noErr(t, f.store.FinishImportRun(ctx, run))
	_, err = f.service.ResolveUnresolved(ctx, "project")
	noErr(t, err)
	accepted, exists, err := f.store.ImportIntent(ctx, intent.ID)
	noErr(t, err)
	require(t, exists && !accepted.HeadOwned, "owner acceptance did not revoke structured ownership")
	reason := "read /synthetic/destination HEAD ownership proven by an applied publication/repository: permission denied"
	noErr(t, f.store.UpdateImportIntent(ctx, intent.ID, state.ImportIntentUnresolved, "", "", reason, f.now))
	noErr(t, f.service.Reconcile(ctx))
	f.git(f.source, "branch", "source-next", oid)
	f.git(f.source, "symbolic-ref", "HEAD", "refs/heads/source-next")
	_, err = f.refresh()
	noErr(t, err)
	eq(t, "HEAD after diagnostic text", f.headRef(f.destinationPath()), "refs/heads/main")
}
