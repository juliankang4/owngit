package importsync

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"owngit/internal/gitexec"
	"owngit/internal/repository"
	"owngit/internal/state"
)

// TestImportPublicationRefreshesTheRefSnapshot proves that a ref snapshot
// read after an import publication shows the published refs, although the
// snapshot of the earlier refs is cached.
func TestImportPublicationRefreshesTheRefSnapshot(t *testing.T) {
	f := newFixture(t)
	f.commit("one", "one\n")
	f.mustImport(ImportInput{})
	ctx := context.Background()
	imported, err := f.manager.RefSnapshot(ctx, "project")
	noErr(t, err, "snapshot after import")
	require(t, imported.Summary.DefaultOID == f.sourceRefs()["refs/heads/main"],
		"snapshot after import=%+v", imported.Summary)

	second := f.commit("two", "two\n")
	f.git(f.source, "tag", "v2")
	run, err := f.refresh()
	noErr(t, err, "refresh")
	require(t, run.Status == state.ImportRunComplete, "refresh run=%+v", run)
	refreshed, err := f.manager.RefSnapshot(ctx, "project")
	noErr(t, err, "snapshot after refresh")
	require(t, refreshed.Summary.DefaultOID == second && refreshed.Head.OID == second && refreshed.Head.Subject == "two",
		"snapshot after refresh=%+v head=%+v, want main at %s", refreshed.Summary, refreshed.Head, second)
	require(t, len(refreshed.Summary.Tags) == 1 && refreshed.Summary.Tags[0].Name == "v2",
		"snapshot after refresh tags=%+v, want v2", refreshed.Summary.Tags)
}

// A scheduled import that finds every ref unchanged writes no ref, so the
// cached ref snapshot stays valid and the next page runs no for-each-ref. An
// import that publishes a change still makes the next page read the refs.
func TestScheduledImportWithoutRefChangesKeepsTheRefSnapshot(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the Git process counter is a POSIX shell wrapper")
	}
	f := newFixture(t)
	f.commit("one", "one\n")
	f.mustImport(ImportInput{})
	reads := countSnapshotReads(t, f.manager)
	ctx := context.Background()
	snapshot := func(wantReads int) repository.RefSnapshot {
		t.Helper()
		result, err := f.manager.RefSnapshot(ctx, "project")
		noErr(t, err, "snapshot")
		got := reads()
		require(t, got == wantReads, "snapshot for-each-ref runs=%d, want %d", got, wantReads)
		return result
	}
	snapshot(1)
	run, err := f.service.RefreshScheduled(ctx, "project", Limits{})
	noErr(t, err, "unchanged scheduled import")
	require(t, run.Status == state.ImportRunComplete && run.RefsUnchanged != 0 && run.RefsCreated == 0 &&
		run.RefsUpdated == 0, "unchanged scheduled import=%+v", run)
	snapshot(1)

	second := f.commit("two", "two\n")
	run, err = f.service.RefreshScheduled(ctx, "project", Limits{})
	noErr(t, err, "changed scheduled import")
	require(t, run.Status == state.ImportRunComplete && run.RefsUpdated == 1, "changed scheduled import=%+v", run)
	changed := snapshot(2)
	require(t, changed.Summary.DefaultOID == second,
		"snapshot after a changed import=%+v, want main at %s", changed.Summary, second)
}

// countSnapshotReads makes the manager run Git through a wrapper that counts
// the for-each-ref runs of ref snapshot reads.
func countSnapshotReads(t *testing.T, manager *repository.Manager) func() int {
	t.Helper()
	gitPath, err := exec.LookPath("git")
	noErr(t, err, "find Git")
	directory := t.TempDir()
	trace := filepath.Join(directory, "trace")
	wrapper := filepath.Join(directory, "git")
	script := "#!/bin/sh\ncase \"$*\" in *for-each-ref*refs/owngit/provenance*) echo read >> '" + trace + "';; esac\nexec '" + gitPath + "' \"$@\"\n"
	noErr(t, os.WriteFile(wrapper, []byte(script), 0o700), "write Git wrapper")
	counting, err := gitexec.New(wrapper, filepath.Join(directory, "runtime"))
	noErr(t, err, "Git wrapper runner")
	manager.Git = counting
	return func() int {
		t.Helper()
		content, err := os.ReadFile(trace)
		if os.IsNotExist(err) {
			return 0
		}
		noErr(t, err, "read Git trace")
		return strings.Count(string(content), "\n")
	}
}

// Only a plan that writes a ref or HEAD counts as a ref change. A retained ref
// that already holds its commit is only verified.
func TestPublicationPlanChangesRefs(t *testing.T) {
	const one, two = "1111111111111111111111111111111111111111", "2222222222222222222222222222222222222222"
	retained := repository.RetainedRefName("heads", one)
	for _, test := range []struct {
		name string
		plan publicationPlan
		want bool
	}{
		{"unchanged", publicationPlan{expected: map[string]string{"refs/heads/main": one}, desired: map[string]string{"refs/heads/main": one}}, false},
		{"verified retention", publicationPlan{expected: map[string]string{retained: one}, desired: map[string]string{retained: one}, retained: map[string]string{retained: one}}, false},
		{"HEAD entry only", publicationPlan{desired: map[string]string{state.ImportHeadRef: "symbolic refs/heads/main"}}, false},
		{"updated branch", publicationPlan{expected: map[string]string{"refs/heads/main": one}, desired: map[string]string{"refs/heads/main": two}}, true},
		{"created tag", publicationPlan{expected: map[string]string{}, desired: map[string]string{"refs/tags/v1": one}}, true},
		{"new retention", publicationPlan{expected: map[string]string{retained: ""}, desired: map[string]string{retained: one}, retained: map[string]string{retained: one}}, true},
		{"HEAD change", publicationPlan{headChange: true}, true},
		{"expected only", publicationPlan{expected: map[string]string{"refs/heads/old": one}, desired: map[string]string{}}, true},
	} {
		if got := test.plan.changesRefs(); got != test.want {
			t.Errorf("%s: changesRefs=%v, want %v", test.name, got, test.want)
		}
	}
}

// A publication that fails, even before it plans any write, does not prove
// that the refs are as cached: reconciliation can fail precisely because the
// destination differs from the record. The next page therefore reads the
// refs again. A run that stops while it waits for the repository lock never
// enters publication and says nothing about the destination, so it leaves the
// cached refs alone.
func TestFailedImportPublicationInvalidatesTheRefSnapshot(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the Git process counter is a POSIX shell wrapper")
	}
	f := newFixture(t)
	f.commit("one", "one\n")
	f.mustImport(ImportInput{Options: OptionsChange{OverwriteDiverged: boolPointer(true)}})
	reads := countSnapshotReads(t, f.manager)
	ctx := context.Background()
	_, err := f.manager.RefSnapshot(ctx, "project")
	noErr(t, err, "snapshot")
	// A diverged protected default branch refuses the run inside publication,
	// after it took the repository lock and before it planned a write.
	f.localWork("main", "local main\n")
	protect := true
	_, err = f.store.SaveRepositoryRefPolicy(ctx, "project", state.RepositoryRefPolicyChange{ProtectDefaultBranch: &protect})
	noErr(t, err)
	f.commit("source main", "source main\n")
	run, err := f.refresh()
	require(t, problemCode(err) == CodeProtectedBranch && run.Status != state.ImportRunComplete,
		"refused refresh run=%+v err=%v", run, err)
	_, err = f.manager.RefSnapshot(ctx, "project")
	noErr(t, err, "snapshot after a failed publication")
	got := reads()
	require(t, got == 2, "snapshot for-each-ref runs=%d, want 2", got)
}
