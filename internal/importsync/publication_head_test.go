package importsync

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"owngit/internal/importgit"
	"owngit/internal/repository"
	"owngit/internal/state"
)

func markHEADOwnedForTest(t *testing.T, f *fixture, runID string) {
	t.Helper()
	intent, exists, err := f.store.CompletedImportIntentForRun(context.Background(), runID)
	require(t, err == nil && exists, "complete intent for run %s exists=%v err=%v", runID, exists, err)
	desiredHEAD, desiredExists := intent.Desired[state.ImportHeadRef]
	observedHEAD, observedExists := intent.Observed[state.ImportHeadRef]
	var receipt map[string]string
	noErr(t, json.Unmarshal([]byte(intent.ReceiptJSON), &receipt))
	require(t, desiredExists && observedExists && receipt[state.ImportHeadRef] == desiredHEAD,
		"synthetic ownership seed lacks consistent HEAD facts: desired=%q observed=%q receipt=%q", desiredHEAD, observedHEAD, receipt[state.ImportHeadRef])
	desired, err := decodeHeadIdentity(desiredHEAD)
	noErr(t, err)
	observed, err := decodeHeadIdentity(observedHEAD)
	require(t, err == nil && sameHEADIdentity(desired, observed),
		"synthetic ownership seed HEAD mismatch: desired=%q observed=%q err=%v", desiredHEAD, observedHEAD, err)
	if err := f.store.UpdateImportIntentHEADOwnership(context.Background(), intent.ID, state.ImportIntentComplete,
		"synthetic test ownership; checkpoint3C still owns real initial provenance", f.now); err != nil {
		t.Fatal(err)
	}
}

// A local HEAD that differs from the source (a retarget, or the same object
// detached) is preserved and reported as divergent.
func TestRefreshPreservesLocalHEADAndReportsDivergence(t *testing.T) {
	for _, test := range []struct {
		name, symref string
		set          func(f *fixture, path, oid string)
	}{
		{"retargeted", "refs/heads/dev", func(f *fixture, path, _ string) { f.git(path, "symbolic-ref", "HEAD", "refs/heads/dev") }},
		{"same object detached", "", func(f *fixture, path, oid string) { f.git(path, "update-ref", "--no-deref", "HEAD", oid) }},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := newFixture(t)
			oid := f.commit("initial", "initial\n")
			f.git(f.source, "branch", "dev")
			f.mustImport(ImportInput{})
			path := f.destinationPath()
			test.set(f, path, oid)
			run, err := f.refresh()
			noErr(t, err)
			eq(t, "divergent refs", run.RefsDivergent, 1)
			symref, resolved, err := f.manager.ReadHead(context.Background(), path)
			require(t, err == nil && symref == test.symref && resolved == oid,
				"local HEAD changed: symref=%q oid=%q err=%v", symref, resolved, err)
		})
	}
}

func TestOwnedHEADTransitionsPreserveDisplacedDetachedHistory(t *testing.T) {
	f := newFixture(t)
	main := f.commit("main", "main\n")
	f.git(f.source, "branch", "dev")
	f.importOwned()
	path := f.destinationPath()

	f.git(f.source, "checkout", "--quiet", "dev")
	run, err := f.refresh()
	require(t, err == nil && run.RefsDivergent == 0, "symbolic retarget run=%+v err=%v", run, err)
	eq(t, "retargeted HEAD", f.git(path, "symbolic-ref", "HEAD"), "refs/heads/dev")

	f.git(f.source, "checkout", "--quiet", "--detach", main)
	_, err = f.refresh()
	noErr(t, err, "symbolic to detached")
	symref, oid, err := f.manager.ReadHead(context.Background(), path)
	require(t, err == nil && symref == "" && oid == main,
		"detached destination HEAD symref=%q oid=%q err=%v", symref, oid, err)

	next := f.commit("detached next", "next\n")
	_, err = f.refresh()
	noErr(t, err, "detached replacement")
	refs := f.destinationRefs()
	for _, name := range detachedHEADRetentionNames(main) {
		eq(t, "detached retention "+name, refs[name], main)
	}

	f.git(f.source, "checkout", "--quiet", "-b", "detached-tip")
	_, err = f.refresh()
	noErr(t, err, "detached to symbolic")
	eq(t, "symbolic HEAD", f.git(path, "symbolic-ref", "HEAD"), "refs/heads/detached-tip")
	refs = f.destinationRefs()
	for _, name := range detachedHEADRetentionNames(next) {
		eq(t, "cross-kind detached retention "+name, refs[name], next)
	}
}

func TestOwnedUnresolvedSymbolicHEADIsNotTreatedAsAbsent(t *testing.T) {
	f := newFixture(t)
	f.commit("initial", "initial\n")
	f.importOwned()
	f.git(f.source, "symbolic-ref", "HEAD", "refs/heads/future")
	run, err := f.refresh()
	require(t, err == nil && run.Status == state.ImportRunComplete, "unresolved symbolic HEAD run=%+v err=%v", run, err)
	symref, oid, err := f.manager.ReadHead(context.Background(), f.destinationPath())
	require(t, err == nil && symref == "refs/heads/future" && oid == "",
		"unresolved symbolic HEAD symref=%q oid=%q err=%v", symref, oid, err)
	observations, err := f.store.ImportObservations(context.Background(), "project", 1)
	noErr(t, err)
	for _, observation := range observations {
		if observation.RefName == state.ImportHeadRef && observation.SymrefTarget == "refs/heads/future" && observation.OID == "" {
			return
		}
	}
	t.Fatalf("unresolved symbolic HEAD observation missing: %+v", observations)
}

func TestUnadvertisedAndNewGenerationHEADCannotAcquireOwnership(t *testing.T) {
	f := newFixture(t)
	f.commit("initial", "initial\n")
	f.git(f.source, "branch", "dev")
	f.mustImport(ImportInput{})
	path := f.destinationPath()

	f.transport.mutateAdvertised = func(advertisement *importgit.Advertisement) {
		advertisement.Head = importgit.Head{}
		filtered := advertisement.Refs[:0]
		for _, ref := range advertisement.Refs {
			if ref.Name != state.ImportHeadRef {
				filtered = append(filtered, ref)
			}
		}
		advertisement.Refs = filtered
	}
	_, err := f.refresh()
	noErr(t, err, "unadvertised HEAD refresh")
	f.transport.mutateAdvertised = nil
	f.git(f.source, "checkout", "--quiet", "dev")
	run, err := f.refresh()
	require(t, err == nil && run.RefsDivergent == 1, "HEAD after absent observation run=%+v err=%v", run, err)
	eq(t, "HEAD after absent source HEAD", f.git(path, "symbolic-ref", "HEAD"), "refs/heads/main")

	_, err = f.service.ConfigureSource(context.Background(), ConfigureInput{RepositoryID: "project", URL: "https://example.invalid/new-generation.git"})
	noErr(t, err)
	run, err = f.refresh()
	require(t, err == nil && run.RefsDivergent == 1, "new generation HEAD run=%+v err=%v", run, err)
	eq(t, "HEAD after new generation", f.git(path, "symbolic-ref", "HEAD"), "refs/heads/main")
}

func TestMatchingExistingHEADDoesNotBecomeImportOwned(t *testing.T) {
	f := newFixture(t)
	oid := f.commit("source", "source bytes\n")
	_, err := f.manager.CreateWithOptions(context.Background(), "project", "independent destination", repository.CreateOptions{ObjectFormat: "sha1"})
	noErr(t, err)
	path := f.destinationPath()
	before := f.headRef(path)
	if _, err := f.importProject(ImportInput{}); err != nil {
		require(t, problemCode(err) == CodeRepositoryTaken, "unexpected initial refusal: %v", err)
		eq(t, "HEAD after initial refusal", f.headRef(path), before)
		return
	}
	f.git(f.source, "branch", "source-next", oid)
	f.git(f.source, "symbolic-ref", "HEAD", "refs/heads/source-next")
	_, err = f.refresh()
	noErr(t, err, "refresh preserving independent HEAD")
	eq(t, "adopted HEAD", f.headRef(path), before)
}

func TestDivergentObservationCannotReacquireHEADOwnership(t *testing.T) {
	f := newFixture(t)
	oid := f.commit("source", "source bytes\n")
	f.git(f.source, "branch", "source-choice", oid)
	f.mustImport(ImportInput{})
	path := f.destinationPath()
	f.git(path, "--git-dir", ".", "update-ref", "refs/heads/local-choice", oid)
	f.git(path, "--git-dir", ".", "symbolic-ref", "HEAD", "refs/heads/local-choice")
	f.git(f.source, "symbolic-ref", "HEAD", "refs/heads/source-choice")
	_, err := f.refresh()
	noErr(t, err)
	eq(t, "HEAD after first divergent refresh", f.headRef(path), "refs/heads/local-choice")
	f.git(path, "--git-dir", ".", "symbolic-ref", "HEAD", "refs/heads/source-choice")
	f.git(f.source, "branch", "source-next", oid)
	f.git(f.source, "symbolic-ref", "HEAD", "refs/heads/source-next")
	_, err = f.refresh()
	noErr(t, err)
	eq(t, "HEAD after divergent observation", f.headRef(path), "refs/heads/source-choice")
}

func TestPublicationRecordsImmediateSymbolicHEAD(t *testing.T) {
	f := newFixture(t)
	f.commit("first", "first bytes\n")
	f.mustImport(ImportInput{})
	path := f.destinationPath()
	const localTarget = "refs/heads/local-alias"
	f.git(path, "--git-dir", ".", "symbolic-ref", localTarget, "refs/heads/main")
	f.git(path, "--git-dir", ".", "symbolic-ref", "HEAD", localTarget)
	f.commit("second", "second bytes\n")
	run, err := f.refresh()
	noErr(t, err, "preserving local symbolic HEAD should allow other owned refs to refresh")
	eq(t, "local immediate HEAD", f.headRef(path), localTarget)
	intent, exists, err := f.service.Store.CompletedImportIntentForRun(context.Background(), run.ID)
	require(t, err == nil && exists, "refresh publication intent exists=%v err=%v", exists, err)
	expected, err := decodeHeadIdentity(intent.Expected[state.ImportHeadRef])
	require(t, err == nil && expected.kind == headSymbolic && expected.target == localTarget,
		"publication recorded resolved HEAD instead of immediate identity: expected=%q err=%v", intent.Expected[state.ImportHeadRef], err)
	require(t, run.RefsDivergent != 0, "local symbolic retarget was not reported as divergent")
}

// An owned branch advance, or a rewrite with its retention, completes in one
// publication together with the HEAD retarget.
func TestOwnedRefUpdatesAndHEADRetargetCompleteTogether(t *testing.T) {
	for _, rewrite := range []bool{false, true} {
		name := "branch advance"
		if rewrite {
			name = "rewrite and retention"
		}
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			old := f.commit("first", "first bytes\n")
			f.importOwned()
			if rewrite {
				f.git(f.source, "checkout", "--orphan", "rewritten")
			}
			tip := f.commit("second", "second bytes\n")
			if rewrite {
				f.git(f.source, "branch", "-f", "main", tip)
			}
			f.git(f.source, "branch", "release", tip)
			f.git(f.source, "symbolic-ref", "HEAD", "refs/heads/release")
			_, err := f.refresh()
			noErr(t, err, "refs plus HEAD retarget")
			refs := f.destinationRefs()
			eq(t, "main", refs["refs/heads/main"], tip)
			if rewrite {
				eq(t, "retained old tip", refs[repository.RetainedRefName("heads", old)], old)
			} else {
				eq(t, "release", refs["refs/heads/release"], tip)
			}
			eq(t, "HEAD", f.headRef(f.destinationPath()), "refs/heads/release")
		})
	}
}

func TestHEADCommitPreservesReplacedLock(t *testing.T) {
	f := newFixture(t)
	oid := f.commit("source", "source bytes\n")
	f.mustImport(ImportInput{})
	path := f.destinationPath()
	headPath := filepath.Join(path, "HEAD")
	before, err := os.ReadFile(headPath)
	noErr(t, err)
	expected, err := readRawHEAD(headPath)
	noErr(t, err)
	lock, err := f.service.acquireHEADLock(context.Background(), &runState{limits: DefaultLimits()}, path, expected)
	noErr(t, err)
	defer lock.rollback()
	if runtime.GOOS == "windows" {
		noErr(t, lock.file.Close())
		lock.file = nil
	}
	noErr(t, os.Rename(lock.path, lock.path+".original-owned"), "replacement fixture cannot move the owned lock")
	foreign := "ref: refs/heads/foreign-writer\n"
	noErr(t, os.WriteFile(lock.path, []byte(foreign), 0o600))
	if runtime.GOOS == "windows" {
		require(t, lock.checkLockIdentity() != nil, "closed HEAD lock accepted a replacement pathname")
	} else {
		require(t, lock.commit(headIdentity{kind: headDetached, oid: oid}) != nil,
			"HEAD commit consumed a replacement lock it did not own")
	}
	fileIs(t, headPath, string(before))
	fileIs(t, lock.path, foreign)
}

func TestHEADPreflightReplacementStopsBeforeRefWrites(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("open-file replacement is refused on Windows; closed-lock pathname identity is covered separately")
	}
	f := newFixture(t)
	oldDev := f.ownedDevCheckout()
	f.commit("dev next", "next\n")
	lockPath := filepath.Join(f.destinationPath(), "HEAD.lock")
	f.service.beforeHEADPreflightRelease = func() {
		f.service.beforeHEADPreflightRelease = nil
		noErr(t, os.Rename(lockPath, lockPath+".original-owned"))
		noErr(t, os.WriteFile(lockPath, []byte("foreign preflight lock\n"), 0o600))
	}
	f.refreshFails(CodePublishFailed, state.ImportRunFailed)
	eq(t, "dev after preflight release failure", f.destinationRefs()["refs/heads/dev"], oldDev)
	fileIs(t, lockPath, "foreign preflight lock\n")
}

func TestBetweenHEADLocksRacesPreserveIndependentWriterAndRefOutcome(t *testing.T) {
	for _, test := range []struct {
		name       string
		mutateHEAD func(*testing.T, *fixture, string, string)
		assertHEAD func(*testing.T, *fixture, string, string)
	}{
		{
			name: "symbolic retarget",
			mutateHEAD: func(_ *testing.T, f *fixture, path, oid string) {
				f.git(path, "--git-dir", ".", "update-ref", "refs/heads/local", oid)
				f.git(path, "--git-dir", ".", "symbolic-ref", "HEAD", "refs/heads/local")
			},
			assertHEAD: func(t *testing.T, f *fixture, path, _ string) {
				eq(t, "independent symbolic HEAD", f.headRef(path), "refs/heads/local")
			},
		},
		{
			name: "detach",
			mutateHEAD: func(_ *testing.T, f *fixture, path, oid string) {
				f.git(path, "--git-dir", ".", "update-ref", "--no-deref", "HEAD", oid)
			},
			assertHEAD: func(t *testing.T, f *fixture, path, oid string) {
				symref, got, err := f.manager.ReadHead(context.Background(), path)
				require(t, err == nil && symref == "" && got == oid,
					"independent detached HEAD symref=%q oid=%q err=%v", symref, got, err)
			},
		},
		{
			name: "foreign lock",
			mutateHEAD: func(t *testing.T, _ *fixture, path, _ string) {
				noErr(t, os.WriteFile(filepath.Join(path, "HEAD.lock"), []byte("foreign final lock\n"), 0o600))
			},
			assertHEAD: func(t *testing.T, f *fixture, path, _ string) {
				eq(t, "HEAD with a foreign lock", f.headRef(path), "refs/heads/main")
				fileIs(t, filepath.Join(path, "HEAD.lock"), "foreign final lock\n")
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := newFixture(t)
			old := f.ownedDevCheckout()
			newDev := f.commit("dev next", "next\n")
			path := f.destinationPath()
			f.service.beforeFinalHEADLock = func() {
				f.service.beforeFinalHEADLock = nil
				test.mutateHEAD(t, f, path, old)
			}
			f.refreshFails(CodeUnresolved, state.ImportRunUnresolved)
			eq(t, "dev after the ref transaction", f.destinationRefs()["refs/heads/dev"], newDev)
			test.assertHEAD(t, f, path, old)
		})
	}
}

func TestExistingHEADLockIsPreservedAndPreventsPublication(t *testing.T) {
	f := newFixture(t)
	f.ownedDevCheckout()
	path := f.destinationPath()
	lockPath := filepath.Join(path, "HEAD.lock")
	noErr(t, os.WriteFile(lockPath, []byte("independent writer\n"), 0o600))
	f.refreshFails(CodeDestinationChanged, state.ImportRunFailed)
	fileIs(t, lockPath, "independent writer\n")
	eq(t, "HEAD despite contention", f.git(path, "symbolic-ref", "HEAD"), "refs/heads/main")
}

func TestHEADSymlinkIsRefusedWithoutWritingThroughIt(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symbolic link creation needs privileges on Windows; native reparse-point coverage is proposed separately")
	}
	f := newFixture(t)
	f.ownedDevCheckout()
	headPath := filepath.Join(f.destinationPath(), "HEAD")
	sentinel := filepath.Join(f.root, "head-sentinel")
	noErr(t, os.WriteFile(sentinel, []byte("ref: refs/heads/main\n"), 0o600))
	noErr(t, os.Remove(headPath))
	noErr(t, os.Symlink(sentinel, headPath))
	f.refreshFails("", state.ImportRunFailed)
	fileIs(t, sentinel, "ref: refs/heads/main\n")
	isSymlink(t, headPath)
}

func TestFailedRefTransactionDoesNotWriteHEAD(t *testing.T) {
	f := newFixture(t)
	oldDev := f.ownedDevCheckout()
	f.commit("dev next", "next\n")
	path := f.destinationPath()
	refLock := filepath.Join(path, "refs", "heads", "dev.lock")
	noErr(t, os.WriteFile(refLock, []byte("independent ref writer\n"), 0o600))
	f.refreshFails(CodePublishFailed, state.ImportRunFailed)
	eq(t, "HEAD after a failed ref transaction", f.git(path, "symbolic-ref", "HEAD"), "refs/heads/main")
	eq(t, "dev after a failed ref transaction", f.destinationRefs()["refs/heads/dev"], oldDev)
	fileIs(t, refLock, "independent ref writer\n")
	absent(t, filepath.Join(path, "HEAD.lock"))
}

func TestRealGitHEADWriterContendsWithOwnedLock(t *testing.T) {
	f := newFixture(t)
	oid := f.commit("initial", "initial\n")
	f.git(f.source, "branch", "dev")
	f.mustImport(ImportInput{})
	limits, err := (Limits{}).effective()
	noErr(t, err)
	expected := headIdentity{kind: headSymbolic, target: "refs/heads/main", oid: oid}
	lock, err := f.service.acquireHEADLock(context.Background(), &runState{limits: limits}, f.destinationPath(), expected)
	noErr(t, err)
	_, err = f.manager.Git.Run(context.Background(), f.destinationPath(), nil, "--git-dir", ".", "symbolic-ref", "HEAD", "refs/heads/dev")
	if err == nil {
		_ = lock.rollback()
		t.Fatal("real Git writer ignored HEAD.lock")
	}
	noErr(t, lock.rollback())
	f.git(f.destinationPath(), "symbolic-ref", "HEAD", "refs/heads/dev")
}

func TestAppliedStateFailureClassifiesMixedRefAndHEADOutcome(t *testing.T) {
	f := newFixture(t)
	f.ownedDevCheckout()
	newDev := f.commit("dev next", "next\n")
	noErr(t, f.store.Exec(context.Background(), `CREATE TRIGGER fail_applied BEFORE UPDATE ON import_publication_intents
		WHEN NEW.status='applied' BEGIN SELECT RAISE(FAIL,'synthetic applied failure'); END`))
	f.refreshFails(CodeUnresolved, state.ImportRunUnresolved)
	path := f.destinationPath()
	eq(t, "dev after the ref transaction", f.destinationRefs()["refs/heads/dev"], newDev)
	eq(t, "HEAD after failed applied-state persistence", f.git(path, "symbolic-ref", "HEAD"), "refs/heads/main")
	intents, err := f.store.PendingImportIntents(context.Background(), "project")
	require(t, err == nil && len(intents) == 1 && intents[0].Status == state.ImportIntentUnresolved,
		"mixed intent=%+v err=%v", intents, err)
	noErr(t, f.store.Exec(context.Background(), `DROP TRIGGER fail_applied`))
	err = f.service.Reconcile(context.Background())
	require(t, err != nil && problemCode(err) == CodeUnresolved, "mixed outcome was silently promoted: %v", err)
	eq(t, "HEAD after reconciliation", f.git(path, "symbolic-ref", "HEAD"), "refs/heads/main")
}

func TestPublicationBookkeepingFailuresRemainRecoverable(t *testing.T) {
	for _, test := range []struct {
		name    string
		trigger string
		change  func(*fixture)
	}{
		{
			name: "no-change observations",
			trigger: `CREATE TRIGGER fail_publication BEFORE INSERT ON import_ref_observations
				BEGIN SELECT RAISE(FAIL,'synthetic observations failure'); END`,
			change: func(*fixture) {},
		},
		{
			name: "applied state",
			trigger: `CREATE TRIGGER fail_publication BEFORE UPDATE ON import_publication_intents
				WHEN NEW.status='applied' BEGIN SELECT RAISE(FAIL,'synthetic applied failure'); END`,
			change: func(f *fixture) { f.commit("next", "next\n") },
		},
		{
			name: "receipt",
			trigger: `CREATE TRIGGER fail_publication BEFORE UPDATE ON import_publication_intents
				WHEN NEW.status='complete' BEGIN SELECT RAISE(FAIL,'synthetic receipt failure'); END`,
			change: func(f *fixture) { f.commit("next", "next\n") },
		},
		{
			name: "final run state",
			trigger: `CREATE TRIGGER fail_publication BEFORE UPDATE ON import_runs
				WHEN NEW.status='complete' BEGIN SELECT RAISE(FAIL,'synthetic run failure'); END`,
			change: func(f *fixture) { f.commit("next", "next\n") },
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := newFixture(t)
			f.commit("initial", "initial\n")
			f.mustImport(ImportInput{})
			test.change(f)
			noErr(t, f.store.Exec(context.Background(), test.trigger))
			run := f.refreshFails(CodeStateUnavailable, state.ImportRunFailed)
			intents, err := f.store.PendingImportIntents(context.Background(), "project")
			require(t, err == nil && len(intents) == 1 && intents[0].Status != state.ImportIntentComplete,
				"recoverable intents=%+v err=%v", intents, err)
			noErr(t, f.store.Exec(context.Background(), `DROP TRIGGER fail_publication`))
			noErr(t, f.service.Reconcile(context.Background()), "reconcile durable outcome")
			stored, exists, err := f.store.ImportRun(context.Background(), run.ID)
			require(t, err == nil && exists && stored.Status == state.ImportRunComplete,
				"reconciled run=%+v exists=%v err=%v", stored, exists, err)
		})
	}
}

func TestIntentCreationFailureWritesNoRefs(t *testing.T) {
	f := newFixture(t)
	old := f.commit("initial", "initial\n")
	f.mustImport(ImportInput{})
	f.commit("next", "next\n")
	noErr(t, f.store.Exec(context.Background(), `CREATE TRIGGER fail_intent BEFORE INSERT ON import_publication_intents
		BEGIN SELECT RAISE(FAIL,'synthetic intent failure'); END`))
	f.refreshFails(CodeStateUnavailable, state.ImportRunFailed)
	eq(t, "main after an intent failure", f.destinationRefs()["refs/heads/main"], old)
}

func TestCompleteReceiptWithUnfinishedRunIsReconciled(t *testing.T) {
	f := newFixture(t)
	oid := f.commit("initial", "initial\n")
	f.mustImport(ImportInput{})
	head := (headIdentity{kind: headSymbolic, target: "refs/heads/main", oid: oid}).encode()
	refs := map[string]string{"refs/heads/main": oid, state.ImportHeadRef: head}
	intent := f.seedIntent('c', refs, refs, refs)
	receipt := `{"HEAD":"` + head + `","refs/heads/main":"` + oid + `"}`
	noErr(t, f.store.UpdateImportIntent(context.Background(),
		intent.ID, state.ImportIntentComplete, receipt, state.ImportReceiptDigest(receipt), "synthetic old sequence", f.now))
	noErr(t, f.service.Reconcile(context.Background()))
	stored, exists, err := f.store.ImportRun(context.Background(), intent.RunID)
	require(t, err == nil && exists && stored.Status == state.ImportRunComplete,
		"recovered run=%+v exists=%v err=%v", stored, exists, err)
}

func TestDetachedRetentionNamesRemainProtected(t *testing.T) {
	oid := strings.Repeat("a", 40)
	for _, name := range detachedHEADRetentionNames(oid) {
		require(t, strings.HasPrefix(name, "refs/owngit/"), "detached retention is not protected: %s", name)
	}
	require(t, repository.RetainedRefName("detached-heads", oid) != repository.ProvenanceRefName("detached-heads", "HEAD", oid),
		"detached retention names collide")
}
