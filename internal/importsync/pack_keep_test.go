package importsync

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"testing"

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
