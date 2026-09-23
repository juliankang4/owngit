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
	if err != nil || !exists {
		t.Fatalf("complete intent for run %s exists=%v err=%v", runID, exists, err)
	}
	desiredHEAD, desiredExists := intent.Desired[state.ImportHeadRef]
	observedHEAD, observedExists := intent.Observed[state.ImportHeadRef]
	var receipt map[string]string
	noErr(t, json.Unmarshal([]byte(intent.ReceiptJSON), &receipt))
	if !desiredExists || !observedExists || receipt[state.ImportHeadRef] != desiredHEAD {
		t.Fatalf("synthetic ownership seed lacks consistent HEAD facts: desired=%q observed=%q receipt=%q", desiredHEAD, observedHEAD, receipt[state.ImportHeadRef])
	}
	desired, err := decodeHeadIdentity(desiredHEAD)
	noErr(t, err)
	observed, err := decodeHeadIdentity(observedHEAD)
	if err != nil || !sameHEADIdentity(desired, observed) {
		t.Fatalf("synthetic ownership seed HEAD mismatch: desired=%q observed=%q err=%v", desiredHEAD, observedHEAD, err)
	}
	if err := f.store.UpdateImportIntentHEADOwnership(context.Background(), intent.ID, state.ImportIntentComplete,
		"synthetic test ownership; checkpoint3C still owns real initial provenance", f.now); err != nil {
		t.Fatal(err)
	}
}

func TestRefreshPreservesLocallyRetargetedHEADAndReportsDivergence(t *testing.T) {
	f := newFixture(t)
	oid := f.commit("initial", "initial\n")
	f.git(f.source, "branch", "dev")
	f.mustImport(ImportInput{})

	path := f.destinationPath()
	f.git(path, "symbolic-ref", "HEAD", "refs/heads/dev")
	run, err := f.refresh()
	noErr(t, err)
	if run.RefsDivergent != 1 {
		t.Fatalf("HEAD divergence count=%d run=%+v", run.RefsDivergent, run)
	}
	symref, resolved, err := f.manager.ReadHead(context.Background(), path)
	if err != nil || symref != "refs/heads/dev" || resolved != oid {
		t.Fatalf("local HEAD changed: symref=%q oid=%q err=%v", symref, resolved, err)
	}
}

func TestRefreshPreservesSameOIDCrossKindLocalHEAD(t *testing.T) {
	f := newFixture(t)
	oid := f.commit("initial", "initial\n")
	f.mustImport(ImportInput{})
	path := f.destinationPath()
	f.git(path, "update-ref", "--no-deref", "HEAD", oid)

	run, err := f.refresh()
	noErr(t, err)
	if run.RefsDivergent != 1 {
		t.Fatalf("cross-kind HEAD divergence count=%d run=%+v", run.RefsDivergent, run)
	}
	symref, resolved, err := f.manager.ReadHead(context.Background(), path)
	if err != nil || symref != "" || resolved != oid {
		t.Fatalf("detached HEAD changed: symref=%q oid=%q err=%v", symref, resolved, err)
	}
}

func TestOwnedHEADTransitionsPreserveDisplacedDetachedHistory(t *testing.T) {
	f := newFixture(t)
	main := f.commit("main", "main\n")
	f.git(f.source, "branch", "dev")
	initial := f.mustImport(ImportInput{})
	markHEADOwnedForTest(t, f, initial.Run.ID)

	f.git(f.source, "checkout", "--quiet", "dev")
	if run, err := f.refresh(); err != nil || run.RefsDivergent != 0 {
		t.Fatalf("symbolic retarget run=%+v err=%v", run, err)
	}
	if symref := f.git(f.destinationPath(), "symbolic-ref", "HEAD"); symref != "refs/heads/dev" {
		t.Fatalf("destination HEAD=%q", symref)
	}

	f.git(f.source, "checkout", "--quiet", "--detach", main)
	if _, err := f.refresh(); err != nil {
		t.Fatalf("symbolic to detached: %v", err)
	}
	if symref, oid, err := f.manager.ReadHead(context.Background(), f.destinationPath()); err != nil || symref != "" || oid != main {
		t.Fatalf("detached destination HEAD symref=%q oid=%q err=%v", symref, oid, err)
	}

	next := f.commit("detached next", "next\n")
	if _, err := f.refresh(); err != nil {
		t.Fatalf("detached replacement: %v", err)
	}
	refs := f.destinationRefs()
	for _, name := range detachedHEADRetentionNames(main) {
		if refs[name] != main {
			t.Fatalf("missing detached retention %s=%q", name, refs[name])
		}
	}

	f.git(f.source, "checkout", "--quiet", "-b", "detached-tip")
	if _, err := f.refresh(); err != nil {
		t.Fatalf("detached to symbolic: %v", err)
	}
	if symref := f.git(f.destinationPath(), "symbolic-ref", "HEAD"); symref != "refs/heads/detached-tip" {
		t.Fatalf("destination HEAD=%q", symref)
	}
	refs = f.destinationRefs()
	for _, name := range detachedHEADRetentionNames(next) {
		if refs[name] != next {
			t.Fatalf("missing cross-kind detached retention %s=%q", name, refs[name])
		}
	}
}

func TestOwnedUnresolvedSymbolicHEADIsNotTreatedAsAbsent(t *testing.T) {
	f := newFixture(t)
	f.commit("initial", "initial\n")
	initial := f.mustImport(ImportInput{})
	markHEADOwnedForTest(t, f, initial.Run.ID)
	f.git(f.source, "symbolic-ref", "HEAD", "refs/heads/future")
	run, err := f.refresh()
	if err != nil || run.Status != state.ImportRunComplete {
		t.Fatalf("unresolved symbolic HEAD run=%+v err=%v", run, err)
	}
	symref, oid, err := f.manager.ReadHead(context.Background(), f.destinationPath())
	if err != nil || symref != "refs/heads/future" || oid != "" {
		t.Fatalf("unresolved symbolic HEAD symref=%q oid=%q err=%v", symref, oid, err)
	}
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
	if _, err := f.refresh(); err != nil {
		t.Fatalf("unadvertised HEAD refresh: %v", err)
	}
	f.transport.mutateAdvertised = nil
	f.git(f.source, "checkout", "--quiet", "dev")
	if run, err := f.refresh(); err != nil || run.RefsDivergent != 1 {
		t.Fatalf("HEAD after absent observation run=%+v err=%v", run, err)
	}
	if got := f.git(path, "symbolic-ref", "HEAD"); got != "refs/heads/main" {
		t.Fatalf("absent source HEAD acquired ownership: %s", got)
	}

	if _, err := f.service.ConfigureSource(context.Background(), ConfigureInput{
		RepositoryID: "project", URL: "https://example.invalid/new-generation.git",
	}); err != nil {
		t.Fatal(err)
	}
	if run, err := f.refresh(); err != nil || run.RefsDivergent != 1 {
		t.Fatalf("new generation HEAD run=%+v err=%v", run, err)
	}
	if got := f.git(path, "symbolic-ref", "HEAD"); got != "refs/heads/main" {
		t.Fatalf("new generation inherited HEAD ownership: %s", got)
	}
}

func TestMatchingExistingHEADDoesNotBecomeImportOwned(t *testing.T) {
	f := newFixture(t)
	oid := f.commit("source", "source bytes\n")
	if _, err := f.manager.CreateWithOptions(context.Background(), "project", "independent destination", repository.CreateOptions{ObjectFormat: "sha1"}); err != nil {
		t.Fatal(err)
	}
	path := f.destinationPath()
	before := f.git(path, "--git-dir", ".", "symbolic-ref", "--no-recurse", "HEAD")
	if _, err := f.importProject(ImportInput{}); err != nil {
		if problemCode(err) != CodeRepositoryTaken {
			t.Fatalf("unexpected initial refusal: %v", err)
		}
		if got := f.git(path, "--git-dir", ".", "symbolic-ref", "--no-recurse", "HEAD"); got != before {
			t.Fatalf("initial refusal changed existing HEAD=%s", got)
		}
		return
	}
	f.git(f.source, "branch", "source-next", oid)
	f.git(f.source, "symbolic-ref", "HEAD", "refs/heads/source-next")
	if _, err := f.refresh(); err != nil {
		t.Fatalf("refresh preserving independent HEAD: %v", err)
	}
	if got := f.git(path, "--git-dir", ".", "symbolic-ref", "--no-recurse", "HEAD"); got != before {
		t.Fatalf("matching existing HEAD was adopted without ownership: before=%s after=%s", before, got)
	}
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
	if _, err := f.refresh(); err != nil {
		t.Fatal(err)
	}
	if got := f.git(path, "--git-dir", ".", "symbolic-ref", "--no-recurse", "HEAD"); got != "refs/heads/local-choice" {
		t.Fatalf("first divergent refresh changed local HEAD=%s", got)
	}
	f.git(path, "--git-dir", ".", "symbolic-ref", "HEAD", "refs/heads/source-choice")
	f.git(f.source, "branch", "source-next", oid)
	f.git(f.source, "symbolic-ref", "HEAD", "refs/heads/source-next")
	if _, err := f.refresh(); err != nil {
		t.Fatal(err)
	}
	if got := f.git(path, "--git-dir", ".", "symbolic-ref", "--no-recurse", "HEAD"); got != "refs/heads/source-choice" {
		t.Fatalf("divergent observation reacquired HEAD ownership: got=%s", got)
	}
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
	if got := f.git(path, "--git-dir", ".", "symbolic-ref", "--no-recurse", "HEAD"); got != localTarget {
		t.Fatalf("local immediate HEAD changed: got=%s want=%s", got, localTarget)
	}
	intent, exists, err := f.service.Store.CompletedImportIntentForRun(context.Background(), run.ID)
	if err != nil || !exists {
		t.Fatalf("refresh publication intent exists=%v err=%v", exists, err)
	}
	expected, err := decodeHeadIdentity(intent.Expected[state.ImportHeadRef])
	if err != nil || expected.kind != headSymbolic || expected.target != localTarget {
		t.Fatalf("publication recorded resolved HEAD instead of immediate identity: expected=%q target=%s err=%v", intent.Expected[state.ImportHeadRef], localTarget, err)
	}
	if run.RefsDivergent == 0 {
		t.Fatal("local symbolic retarget was not reported as divergent")
	}
}

func TestOwnedBranchAdvanceAndHEADRetargetCompleteTogether(t *testing.T) {
	f := newFixture(t)
	f.commit("first", "first bytes\n")
	initial := f.mustImport(ImportInput{})
	markHEADOwnedForTest(t, f, initial.Run.ID)
	second := f.commit("second", "second bytes\n")
	f.git(f.source, "branch", "release", second)
	f.git(f.source, "symbolic-ref", "HEAD", "refs/heads/release")
	run, err := f.refresh()
	if err != nil {
		t.Fatalf("owned branch advance plus HEAD retarget failed: status=%s err=%v", run.Status, err)
	}
	refs := f.destinationRefs()
	if refs["refs/heads/main"] != second || refs["refs/heads/release"] != second {
		t.Fatalf("incomplete owned ref update: refs=%v want=%s", refs, second)
	}
	if got := f.git(f.destinationPath(), "--git-dir", ".", "symbolic-ref", "--no-recurse", "HEAD"); got != "refs/heads/release" {
		t.Fatalf("HEAD=%s", got)
	}
}

func TestOwnedRewriteRetentionAndHEADRetargetCompleteTogether(t *testing.T) {
	f := newFixture(t)
	old := f.commit("first", "first bytes\n")
	initial := f.mustImport(ImportInput{})
	markHEADOwnedForTest(t, f, initial.Run.ID)
	f.git(f.source, "checkout", "--orphan", "rewritten")
	rewritten := f.commit("rewritten", "rewritten bytes\n")
	f.git(f.source, "branch", "-f", "main", rewritten)
	f.git(f.source, "branch", "release", rewritten)
	f.git(f.source, "symbolic-ref", "HEAD", "refs/heads/release")
	if _, err := f.refresh(); err != nil {
		t.Fatalf("rewrite retention plus HEAD retarget failed: %v", err)
	}
	refs := f.destinationRefs()
	if refs["refs/heads/main"] != rewritten || refs[repository.RetainedRefName("heads", old)] != old {
		t.Fatalf("rewrite or retention incomplete: refs=%v", refs)
	}
	if got := f.git(f.destinationPath(), "--git-dir", ".", "symbolic-ref", "--no-recurse", "HEAD"); got != "refs/heads/release" {
		t.Fatalf("HEAD=%s", got)
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
	foreign := []byte("ref: refs/heads/foreign-writer\n")
	noErr(t, os.WriteFile(lock.path, foreign, 0o600))
	if runtime.GOOS == "windows" {
		if err := lock.checkLockIdentity(); err == nil {
			t.Fatal("closed HEAD lock accepted a replacement pathname")
		}
	} else if err := lock.commit(headIdentity{kind: headDetached, oid: oid}); err == nil {
		t.Fatal("HEAD commit consumed a replacement lock it did not own")
	}
	if after, err := os.ReadFile(headPath); err != nil || string(after) != string(before) {
		t.Fatalf("replaced-lock refusal changed HEAD: before=%q after=%q err=%v", before, after, err)
	}
	if got, err := os.ReadFile(lock.path); err != nil || string(got) != string(foreign) {
		t.Fatalf("foreign lock was not preserved: bytes=%q err=%v", got, err)
	}
}

func TestHEADPreflightReplacementStopsBeforeRefWrites(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("open-file replacement is refused on Windows; closed-lock pathname identity is covered separately")
	}
	f := newFixture(t)
	f.commit("initial", "initial\n")
	f.git(f.source, "branch", "dev")
	initial := f.mustImport(ImportInput{})
	markHEADOwnedForTest(t, f, initial.Run.ID)
	oldDev := f.git(f.source, "rev-parse", "refs/heads/dev")
	f.git(f.source, "checkout", "--quiet", "dev")
	f.commit("dev next", "next\n")
	path := f.destinationPath()
	lockPath := filepath.Join(path, "HEAD.lock")
	f.service.beforeHEADPreflightRelease = func() {
		f.service.beforeHEADPreflightRelease = nil
		noErr(t, os.Rename(lockPath, lockPath+".original-owned"))
		noErr(t, os.WriteFile(lockPath, []byte("foreign preflight lock\n"), 0o600))
	}
	run, err := f.refresh()
	if err == nil || problemCode(err) != CodePublishFailed || run.Status != state.ImportRunFailed {
		t.Fatalf("preflight replacement run=%+v err=%v", run, err)
	}
	if got := f.destinationRefs()["refs/heads/dev"]; got != oldDev {
		t.Fatalf("preflight release failure allowed ref write: got=%s want=%s", got, oldDev)
	}
	if got, err := os.ReadFile(lockPath); err != nil || string(got) != "foreign preflight lock\n" {
		t.Fatalf("foreign preflight lock changed: bytes=%q err=%v", got, err)
	}
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
				if got := f.git(path, "--git-dir", ".", "symbolic-ref", "--no-recurse", "HEAD"); got != "refs/heads/local" {
					t.Fatalf("independent symbolic HEAD changed: %s", got)
				}
			},
		},
		{
			name: "detach",
			mutateHEAD: func(_ *testing.T, f *fixture, path, oid string) {
				f.git(path, "--git-dir", ".", "update-ref", "--no-deref", "HEAD", oid)
			},
			assertHEAD: func(t *testing.T, f *fixture, path, oid string) {
				symref, got, err := f.manager.ReadHead(context.Background(), path)
				if err != nil || symref != "" || got != oid {
					t.Fatalf("independent detached HEAD symref=%q oid=%q err=%v", symref, got, err)
				}
			},
		},
		{
			name: "foreign lock",
			mutateHEAD: func(t *testing.T, _ *fixture, path, _ string) {
				noErr(t, os.WriteFile(filepath.Join(path, "HEAD.lock"), []byte("foreign final lock\n"), 0o600))
			},
			assertHEAD: func(t *testing.T, f *fixture, path, _ string) {
				if got := f.git(path, "--git-dir", ".", "symbolic-ref", "--no-recurse", "HEAD"); got != "refs/heads/main" {
					t.Fatalf("foreign lock race changed HEAD: %s", got)
				}
				if got, err := os.ReadFile(filepath.Join(path, "HEAD.lock")); err != nil || string(got) != "foreign final lock\n" {
					t.Fatalf("foreign final lock changed: bytes=%q err=%v", got, err)
				}
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := newFixture(t)
			old := f.commit("initial", "initial\n")
			f.git(f.source, "branch", "dev")
			initial := f.mustImport(ImportInput{})
			markHEADOwnedForTest(t, f, initial.Run.ID)
			f.git(f.source, "checkout", "--quiet", "dev")
			newDev := f.commit("dev next", "next\n")
			path := f.destinationPath()
			f.service.beforeFinalHEADLock = func() {
				f.service.beforeFinalHEADLock = nil
				test.mutateHEAD(t, f, path, old)
			}
			run, err := f.refresh()
			if err == nil || problemCode(err) != CodeUnresolved || run.Status != state.ImportRunUnresolved {
				t.Fatalf("between-lock race run=%+v err=%v", run, err)
			}
			if got := f.destinationRefs()["refs/heads/dev"]; got != newDev {
				t.Fatalf("successful ref transaction was rolled back: got=%s want=%s", got, newDev)
			}
			test.assertHEAD(t, f, path, old)
		})
	}
}

func TestExistingHEADLockIsPreservedAndPreventsPublication(t *testing.T) {
	f := newFixture(t)
	f.commit("initial", "initial\n")
	f.git(f.source, "branch", "dev")
	initial := f.mustImport(ImportInput{})
	markHEADOwnedForTest(t, f, initial.Run.ID)
	f.git(f.source, "checkout", "--quiet", "dev")
	path := f.destinationPath()
	lockPath := filepath.Join(path, "HEAD.lock")
	noErr(t, os.WriteFile(lockPath, []byte("independent writer\n"), 0o600))
	run, err := f.refresh()
	if err == nil || problemCode(err) != CodeDestinationChanged || run.Status != state.ImportRunFailed {
		t.Fatalf("lock contention run=%+v err=%v", run, err)
	}
	content, readErr := os.ReadFile(lockPath)
	if readErr != nil || string(content) != "independent writer\n" {
		t.Fatalf("independent lock changed: %q err=%v", content, readErr)
	}
	if got := f.git(path, "symbolic-ref", "HEAD"); got != "refs/heads/main" {
		t.Fatalf("HEAD changed despite contention: %s", got)
	}
}

func TestHEADSymlinkIsRefusedWithoutWritingThroughIt(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symbolic link creation needs privileges on Windows; native reparse-point coverage is proposed separately")
	}
	f := newFixture(t)
	f.commit("initial", "initial\n")
	f.git(f.source, "branch", "dev")
	initial := f.mustImport(ImportInput{})
	markHEADOwnedForTest(t, f, initial.Run.ID)
	f.git(f.source, "checkout", "--quiet", "dev")
	path := f.destinationPath()
	headPath := filepath.Join(path, "HEAD")
	sentinel := filepath.Join(f.root, "head-sentinel")
	noErr(t, os.WriteFile(sentinel, []byte("ref: refs/heads/main\n"), 0o600))
	noErr(t, os.Remove(headPath))
	noErr(t, os.Symlink(sentinel, headPath))
	run, err := f.refresh()
	if err == nil || run.Status != state.ImportRunFailed {
		t.Fatalf("symlink HEAD run=%+v err=%v", run, err)
	}
	content, readErr := os.ReadFile(sentinel)
	if readErr != nil || string(content) != "ref: refs/heads/main\n" {
		t.Fatalf("HEAD symlink target changed: %q err=%v", content, readErr)
	}
	info, statErr := os.Lstat(headPath)
	if statErr != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("HEAD symlink was replaced: info=%v err=%v", info, statErr)
	}
}

func TestFailedRefTransactionDoesNotWriteHEAD(t *testing.T) {
	f := newFixture(t)
	f.commit("initial", "initial\n")
	f.git(f.source, "branch", "dev")
	initial := f.mustImport(ImportInput{})
	markHEADOwnedForTest(t, f, initial.Run.ID)
	oldDev := f.git(f.source, "rev-parse", "refs/heads/dev")
	f.git(f.source, "checkout", "--quiet", "dev")
	f.commit("dev next", "next\n")
	path := f.destinationPath()
	refLock := filepath.Join(path, "refs", "heads", "dev.lock")
	noErr(t, os.WriteFile(refLock, []byte("independent ref writer\n"), 0o600))
	run, err := f.refresh()
	if err == nil || problemCode(err) != CodePublishFailed || run.Status != state.ImportRunFailed {
		t.Fatalf("ref failure run=%+v err=%v", run, err)
	}
	if got := f.git(path, "symbolic-ref", "HEAD"); got != "refs/heads/main" {
		t.Fatalf("HEAD followed failed ref transaction: %s", got)
	}
	if got := f.destinationRefs()["refs/heads/dev"]; got != oldDev {
		t.Fatalf("failed ref transaction changed dev: got=%s want=%s", got, oldDev)
	}
	if content, readErr := os.ReadFile(refLock); readErr != nil || string(content) != "independent ref writer\n" {
		t.Fatalf("independent ref lock changed: %q err=%v", content, readErr)
	}
	if _, statErr := os.Lstat(filepath.Join(path, "HEAD.lock")); !os.IsNotExist(statErr) {
		t.Fatalf("owned HEAD lock was not cleaned up: %v", statErr)
	}
}

func TestRealGitHEADWriterContendsWithOwnedLock(t *testing.T) {
	f := newFixture(t)
	oid := f.commit("initial", "initial\n")
	f.git(f.source, "branch", "dev")
	f.mustImport(ImportInput{})
	limits, err := (Limits{}).effective()
	noErr(t, err)
	run := &runState{limits: limits}
	expected := headIdentity{kind: headSymbolic, target: "refs/heads/main", oid: oid}
	lock, err := f.service.acquireHEADLock(context.Background(), run, f.destinationPath(), expected)
	noErr(t, err)
	if _, err := f.manager.Git.Run(context.Background(), f.destinationPath(), nil, "--git-dir", ".", "symbolic-ref", "HEAD", "refs/heads/dev"); err == nil {
		_ = lock.rollback()
		t.Fatal("real Git writer ignored HEAD.lock")
	}
	noErr(t, lock.rollback())
	f.git(f.destinationPath(), "symbolic-ref", "HEAD", "refs/heads/dev")
}

func TestAppliedStateFailureClassifiesMixedRefAndHEADOutcome(t *testing.T) {
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
	path := f.destinationPath()
	if got := f.destinationRefs()["refs/heads/dev"]; got != newDev {
		t.Fatalf("successful ref transaction was hidden: got=%s want=%s", got, newDev)
	}
	if got := f.git(path, "symbolic-ref", "HEAD"); got != "refs/heads/main" {
		t.Fatalf("HEAD followed failed applied-state persistence: %s", got)
	}
	intents, queryErr := f.store.PendingImportIntents(context.Background(), "project")
	if queryErr != nil || len(intents) != 1 || intents[0].Status != state.ImportIntentUnresolved {
		t.Fatalf("mixed intent=%+v err=%v", intents, queryErr)
	}
	noErr(t, f.store.Exec(context.Background(), `DROP TRIGGER fail_applied`))
	if err := f.service.Reconcile(context.Background()); err == nil || problemCode(err) != CodeUnresolved {
		t.Fatalf("mixed outcome was silently promoted: %v", err)
	}
	if got := f.git(path, "symbolic-ref", "HEAD"); got != "refs/heads/main" {
		t.Fatalf("reconciliation overwrote independent HEAD: %s", got)
	}
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
			run, err := f.refresh()
			if err == nil || problemCode(err) != CodeStateUnavailable || run.Status != state.ImportRunFailed {
				t.Fatalf("failed bookkeeping run=%+v err=%v", run, err)
			}
			intents, queryErr := f.store.PendingImportIntents(context.Background(), "project")
			if queryErr != nil || len(intents) != 1 || intents[0].Status == state.ImportIntentComplete {
				t.Fatalf("recoverable intents=%+v err=%v", intents, queryErr)
			}
			noErr(t, f.store.Exec(context.Background(), `DROP TRIGGER fail_publication`))
			noErr(t, f.service.Reconcile(context.Background()), "reconcile durable outcome")
			stored, exists, queryErr := f.store.ImportRun(context.Background(), run.ID)
			if queryErr != nil || !exists || stored.Status != state.ImportRunComplete {
				t.Fatalf("reconciled run=%+v exists=%v err=%v", stored, exists, queryErr)
			}
		})
	}
}

func TestIntentCreationFailureWritesNoRefs(t *testing.T) {
	f := newFixture(t)
	old := f.commit("initial", "initial\n")
	f.mustImport(ImportInput{})
	f.commit("next", "next\n")
	if err := f.store.Exec(context.Background(), `CREATE TRIGGER fail_intent BEFORE INSERT ON import_publication_intents
		BEGIN SELECT RAISE(FAIL,'synthetic intent failure'); END`); err != nil {
		t.Fatal(err)
	}
	run, err := f.refresh()
	if err == nil || problemCode(err) != CodeStateUnavailable || run.Status != state.ImportRunFailed {
		t.Fatalf("intent failure run=%+v err=%v", run, err)
	}
	if got := f.destinationRefs()["refs/heads/main"]; got != old {
		t.Fatalf("intent failure changed destination: got=%s want=%s", got, old)
	}
}

func TestCompleteReceiptWithUnfinishedRunIsReconciled(t *testing.T) {
	f := newFixture(t)
	oid := f.commit("initial", "initial\n")
	f.mustImport(ImportInput{})
	now := f.now
	run := state.ImportRun{
		ID: strings.Repeat("c", 32), RepositoryID: "project", SourceGeneration: 1, AuthorityRevision: 1,
		Kind: state.ImportKindRefresh, Status: state.ImportRunPreparing, StartedAt: now, CreatedAt: now,
	}
	noErr(t, f.store.BeginImportRun(context.Background(), run))
	run.Status = state.ImportRunInterrupted
	run.FinishedAt = now
	noErr(t, f.store.FinishImportRun(context.Background(), run))
	head := (headIdentity{kind: headSymbolic, target: "refs/heads/main", oid: oid}).encode()
	intent := state.ImportIntent{
		ID: strings.Repeat("d", 32), RepositoryID: "project", RunID: run.ID,
		SourceGeneration: 1, AuthorityRevision: 1, Status: state.ImportIntentPlanning,
		Expected: map[string]string{"refs/heads/main": oid, state.ImportHeadRef: head},
		Desired:  map[string]string{"refs/heads/main": oid, state.ImportHeadRef: head},
		Observed: map[string]string{"refs/heads/main": oid, state.ImportHeadRef: head},
		Retained: map[string]string{}, CreatedAt: now,
	}
	noErr(t, f.store.CreateImportIntent(context.Background(), intent))
	receipt := `{"HEAD":"` + head + `","refs/heads/main":"` + oid + `"}`
	noErr(t, f.store.UpdateImportIntent(context.Background(), intent.ID, state.ImportIntentComplete, receipt, state.ImportReceiptDigest(receipt), "synthetic old sequence", now))
	noErr(t, f.service.Reconcile(context.Background()))
	stored, exists, err := f.store.ImportRun(context.Background(), run.ID)
	if err != nil || !exists || stored.Status != state.ImportRunComplete {
		t.Fatalf("recovered run=%+v exists=%v err=%v", stored, exists, err)
	}
}

func TestDetachedRetentionNamesRemainProtected(t *testing.T) {
	oid := strings.Repeat("a", 40)
	for _, name := range detachedHEADRetentionNames(oid) {
		if !strings.HasPrefix(name, "refs/owngit/") {
			t.Fatalf("detached retention is not protected: %s", name)
		}
	}
	if repository.RetainedRefName("detached-heads", oid) == repository.ProvenanceRefName("detached-heads", "HEAD", oid) {
		t.Fatal("detached retention names collide")
	}
}
