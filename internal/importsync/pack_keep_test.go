package importsync

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"owngit/internal/state"
)

func destinationKeepFiles(t *testing.T, path string) []string {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(path, "objects", "pack", "pack-*.keep"))
	noErr(t, err)
	names := make([]string, 0, len(matches))
	for _, match := range matches {
		names = append(names, filepath.Base(match))
	}
	sort.Strings(names)
	return names
}

// Destination indexing uses index-pack --keep only to protect the new pack
// until the ref transaction ends, as git receive-pack does. A .keep file left
// behind would exclude every imported pack from later repacking.
func TestImportRemovesItsDestinationPackKeep(t *testing.T) {
	f := newFixture(t)
	f.commit("one", "one\n")
	initial := f.mustImport(ImportInput{})
	require(t, initial.Run.Status == state.ImportRunComplete, "initial run=%+v", initial.Run)
	path := f.destinationPath()
	keeps := destinationKeepFiles(t, path)
	require(t, len(keeps) == 0, "initial import left keep files %v", keeps)
	packs, err := filepath.Glob(filepath.Join(path, "objects", "pack", "pack-*.pack"))
	require(t, err == nil && len(packs) == 1, "initial packs=%v err=%v", packs, err)
	// An operator's .keep file on an existing pack is not this run's to remove.
	operatorKeep := packs[0][:len(packs[0])-len(".pack")] + ".keep"
	noErr(t, os.WriteFile(operatorKeep, []byte("operator\n"), 0o600))

	f.commit("two", "two\n")
	run, err := f.refresh()
	require(t, err == nil && run.Status == state.ImportRunComplete, "refresh run=%+v err=%v", run, err)
	packs, err = filepath.Glob(filepath.Join(path, "objects", "pack", "pack-*.pack"))
	require(t, err == nil && len(packs) == 2,
		"refresh did not index a new destination pack: packs=%v err=%v", packs, err)
	keeps = destinationKeepFiles(t, path)
	require(t, len(keeps) == 1 && keeps[0] == filepath.Base(operatorKeep),
		"keep files after refresh=%v want only %s", keeps, filepath.Base(operatorKeep))
	content, err := os.ReadFile(operatorKeep)
	require(t, err == nil && string(content) == "operator\n", "operator keep file changed: %q err=%v", content, err)
}

// The .keep file protects the pack while the ref transaction is prepared,
// names the import run that created it, and is removed after a refused ref
// transaction too.
func TestFailedPublicationRemovesItsDestinationPackKeep(t *testing.T) {
	f := newFixture(t)
	old := f.commit("one", "one\n")
	f.mustImport(ImportInput{})
	f.commit("two", "two\n")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var preparedKeeps []string
	var preparedContent string
	f.service.whileRefsPrepared = func() {
		f.service.whileRefsPrepared = nil
		path := f.destinationPath()
		preparedKeeps = destinationKeepFiles(t, path)
		if len(preparedKeeps) == 1 {
			content, _ := os.ReadFile(filepath.Join(path, "objects", "pack", preparedKeeps[0]))
			preparedContent = string(content)
		}
		cancel()
	}
	run, err := f.service.Refresh(ctx, "project", Limits{})
	require(t, err != nil && run.Status != state.ImportRunComplete, "cancelled refresh run=%+v err=%v", run, err)
	path := f.destinationPath()
	eq(t, "cancelled refresh moved main to", f.git(path, "--git-dir", ".", "rev-parse", "refs/heads/main"), old)
	keeps := destinationKeepFiles(t, path)
	require(t, len(keeps) == 0, "failed refresh left keep files %v", keeps)
	want := "owngit import " + f.lastRun().ID + "\n"
	require(t, len(preparedKeeps) == 1 && preparedContent == want,
		"keep files while prepared=%v content=%q want one with %q", preparedKeeps, preparedContent, want)
}

// A successful refresh removes its pack keep file while a reader holds the
// repository. The removal only has to keep out of the way of repack, which runs
// as repository maintenance under the write lock, and a clone that starts as the
// publication ends must not make the cleanup wait out its bound and leave the
// pack out of every later repack.
func TestSuccessfulRefreshRemovesItsPackKeepBehindAReader(t *testing.T) {
	f := newFixture(t)
	f.commit("one", "one\n")
	f.mustImport(ImportInput{})
	f.commit("two", "two\n")
	reader := f.manager.Locks.For("project")
	holding := make(chan struct{})
	release := make(chan struct{})
	defer close(release)
	// OnChange runs after publication released the write lock and before the keep
	// cleanup, so a reader that holds the lock from here is a clone that started
	// in that window.
	f.manager.OnChange = func(string) {
		go func() {
			reader.RLock()
			close(holding)
			<-release
			reader.RUnlock()
		}()
		<-holding
	}
	run, err := f.refresh()
	require(t, err == nil && run.Status == state.ImportRunComplete, "refresh run=%+v err=%v", run, err)
	keeps := destinationKeepFiles(t, f.destinationPath())
	require(t, len(keeps) == 0, "the refresh left keep files %v while a reader held the repository", keeps)
}

func TestReconcileRemovesOnlyAbandonedImportKeeps(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.commit("one", "one\n")
	completed := f.mustImport(ImportInput{}).Run
	otherSource, err := f.store.ConfigureImportSource(ctx, state.ImportSourceInput{
		RepositoryID: "other", URL: "https://example.invalid/other.git", Mode: string(ModeStandalone), Now: f.now,
	})
	noErr(t, err)
	other := state.ImportRun{ID: strings.Repeat("e", 32), RepositoryID: "other", SourceGeneration: otherSource.SourceGeneration,
		AuthorityRevision: otherSource.AuthorityRevision, Kind: state.ImportKindRefresh, Status: state.ImportRunPreparing, StartedAt: f.now}
	noErr(t, f.store.BeginImportRun(ctx, other))
	other.Status, other.FinishedAt = state.ImportRunFailed, f.now
	noErr(t, f.store.FinishImportRun(ctx, other))
	entered, gate := make(chan struct{}), make(chan struct{})
	f.transport.before = func() { close(entered) }
	f.transport.gate = gate
	refreshed := make(chan error, 1)
	t.Cleanup(func() {
		close(gate)
		select {
		case err := <-refreshed:
			noErr(t, err)
		case <-time.After(10 * time.Second):
			t.Error("refresh did not finish")
		}
	})
	go func() { _, err := f.refresh(); refreshed <- err }()
	select {
	case <-entered:
	case <-time.After(10 * time.Second):
		t.Fatal("refresh did not reach the source")
	}
	live := f.service.liveRunIDs()
	require(t, len(live) == 1, "live runs=%v", live)
	packDir := filepath.Join(f.destinationPath(), "objects", "pack")
	for _, test := range []struct {
		name    string
		content string
		removed bool
		link    bool
	}{
		{"completed", "owngit import " + completed.ID + "\n", true, false},
		{"live", "owngit import " + live[0] + "\n", false, false},
		{"other repository", "owngit import " + other.ID + "\n", false, false},
		{"unknown run", "owngit import " + strings.Repeat("f", 32) + "\n", false, false},
		{"operator", "operator\n", false, false},
		{"extra text", "owngit import " + completed.ID + "\noperator\n", false, false},
		{"linked keep", "owngit import " + completed.ID + "\n", false, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(packDir, "pack-"+state.ImportReceiptDigest(test.name)[:40]+".keep")
			if test.link {
				target := filepath.Join(f.root, "keep-target")
				noErr(t, os.WriteFile(target, []byte(test.content), 0o600))
				noErr(t, os.Symlink(target, path))
			} else {
				noErr(t, os.WriteFile(path, []byte(test.content), 0o600))
			}
			noErr(t, f.service.ReconcilePackKeeps(ctx))
			content, err := os.ReadFile(path)
			if test.removed {
				require(t, os.IsNotExist(err), "abandoned keep remained: %q err=%v", content, err)
			} else {
				require(t, err == nil && string(content) == test.content, "unowned or active keep changed: %q err=%v", content, err)
			}
		})
	}
}

func TestCreatedPackKeepOnlyTrustsKeepReports(t *testing.T) {
	sha1 := "0123456789abcdef0123456789abcdef01234567"
	sha256 := sha1 + "0123456789abcdef01234567"
	for _, test := range []struct {
		stdout string
		want   string
	}{
		{"keep\t" + sha1 + "\n", sha1},
		{"keep\t" + sha256 + "\n", sha256},
		{"keep\t" + sha1 + "\ntrailing input", sha1},
		// The pack and its .keep already existed; that file is not ours.
		{"pack\t" + sha1 + "\n", ""},
		{"keep\t../../" + sha1[6:] + "\n", ""},
		{"keep\t" + sha1[:39] + "\n", ""},
		{"keep\t" + "0123456789ABCDEF0123456789abcdef01234567\n", ""},
		{"", ""},
	} {
		got, created := createdPackKeep([]byte(test.stdout))
		require(t, got == test.want && created == (test.want != ""),
			"createdPackKeep(%q)=%q,%v want %q", test.stdout, got, created, test.want)
	}
}
