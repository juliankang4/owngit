package importsync

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// Removals bind to the directory that was proven or held, not to its path.
func TestRemovalsRefuseAReplacedDirectory(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows refuses to rename a folder that is held open")
	}
	storage := t.TempDir()
	name := ".owngit-import-x"
	path := filepath.Join(storage, name)
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(os.Mkdir(path, 0o700))
	proven, err := directoryIdentityIn(storage, name)
	must(err)
	held, err := os.Open(path)
	must(err)
	t.Cleanup(func() { _ = held.Close() })
	must(os.WriteFile(filepath.Join(path, "HEAD.lock"), []byte("x"), 0o600))
	lock, err := os.Lstat(filepath.Join(path, "HEAD.lock"))
	must(err)
	must(os.Rename(path, path+"-moved"))
	must(os.Mkdir(path, 0o700))
	must(os.WriteFile(filepath.Join(path, "HEAD.lock"), []byte("other"), 0o600))
	if removeDirectoryWithIdentity(storage, name, proven) == nil {
		t.Fatal("a replaced destination was removed")
	}
	if removeOwnedLockIn(path, held, "HEAD.lock", lock) == nil {
		t.Fatal("a lock in a replaced folder was removed")
	}
	if _, err := os.Stat(filepath.Join(path, "HEAD.lock")); err != nil {
		t.Fatalf("replacement content was removed: %v", err)
	}
}

// A scheduled refresh publishes under the repository write lock, which refuses
// once the repository folder claim is lost.
func TestRefreshPublishesNothingAfterTheStorageClaimIsLost(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows refuses to remove the held lock file")
	}
	f := newFixture(t)
	f.commit("one", "one\n")
	f.mustImport(ImportInput{})
	noErr(t, f.manager.ClaimStorage())
	t.Cleanup(f.manager.ReleaseStorage)
	noErr(t, os.Remove(filepath.Join(f.manager.RepositoryRoot(), ".owngit-serve.lock")))
	before := f.destinationRefs()["refs/heads/main"]
	f.commit("two", "two\n")
	run, _ := f.refresh()
	if after := f.destinationRefs()["refs/heads/main"]; after != before || run.RefsUpdated != 0 {
		t.Fatalf("refresh published after the claim was lost: run=%+v", run)
	}
	if run.ErrorClass != CodeUnresolved {
		t.Fatalf("refresh error class=%q, want %q (not a cancellation)", run.ErrorClass, CodeUnresolved)
	}
}

// The destination pack is written before the repository write lock, so a claim
// lost during the source transfer must still stop it.
func TestRefreshWritesNoPackWhenStorageChangesMidRun(t *testing.T) {
	for _, change := range []string{"root claim lost", "repository replaced"} {
		t.Run(change, func(t *testing.T) {
			if change == "root claim lost" && runtime.GOOS == "windows" {
				t.Skip("Windows refuses to remove the held lock file")
			}
			f := newFixture(t)
			f.commit("one", "one\n")
			f.mustImport(ImportInput{})
			noErr(t, f.manager.ClaimStorage())
			t.Cleanup(f.manager.ReleaseStorage)
			destination := f.destinationPath()
			revision := f.commit("two", "two\n")
			_, err := f.refresh()
			noErr(t, err)
			require(t, f.git(destination, "rev-parse", "refs/heads/main") == revision, "intact refresh did not publish")
			f.commit("three", "three\n")
			f.service.beforeStagingVerification = func(context.Context) {
				if change == "root claim lost" {
					noErr(t, os.Remove(filepath.Join(f.manager.RepositoryRoot(), ".owngit-serve.lock")))
				}
				noErr(t, os.Rename(destination, destination+"-original"))
				f.git("", "init", "--bare", "--quiet", "--initial-branch=main", destination)
			}
			run, err := f.refresh()
			require(t, err != nil && run.ErrorClass == CodeUnresolved, "run=%+v err=%v", run, err)
			packs, err := os.ReadDir(filepath.Join(destination, "objects", "pack"))
			noErr(t, err)
			require(t, len(packs) == 0, "the changed folder received %d pack files", len(packs))
			require(t, f.git(destination, "for-each-ref", "--format=%(refname)") == "", "the changed folder received refs")
		})
	}
}

// The keep file of a run whose claim was lost before cleanup stays in place.
func TestRefreshKeepsItsPackKeepFileWhenStorageChangesBeforeCleanup(t *testing.T) {
	for _, change := range []string{"root claim lost", "repository replaced"} {
		t.Run(change, func(t *testing.T) {
			if change == "root claim lost" && runtime.GOOS == "windows" {
				t.Skip("Windows refuses to remove the held lock file")
			}
			f := newFixture(t)
			f.commit("one", "one\n")
			f.mustImport(ImportInput{})
			noErr(t, f.manager.ClaimStorage())
			t.Cleanup(f.manager.ReleaseStorage)
			destination := f.destinationPath()
			original := destination
			var replacementKeep string
			f.commit("two", "two\n")
			f.service.beforeFinalAuthorityCheck = func() {
				if change == "root claim lost" {
					noErr(t, os.Remove(filepath.Join(f.manager.RepositoryRoot(), ".owngit-serve.lock")))
					return
				}
				keeps := destinationKeepFiles(t, destination)
				require(t, len(keeps) == 1, "indexing made %d keep files", len(keeps))
				original = destination + "-original"
				noErr(t, os.Rename(destination, original))
				f.git("", "init", "--bare", "--quiet", "--initial-branch=main", destination)
				replacementKeep = filepath.Join(destination, "objects", "pack", filepath.Base(keeps[0]))
				noErr(t, os.WriteFile(replacementKeep, []byte("replacement\n"), 0o600))
			}
			run, err := f.refresh()
			require(t, err != nil && run.ErrorClass == CodeUnresolved, "run=%+v err=%v", run, err)
			require(t, len(destinationKeepFiles(t, original)) == 1, "the owned keep file was removed")
			if replacementKeep != "" {
				content, err := os.ReadFile(replacementKeep)
				noErr(t, err)
				require(t, string(content) == "replacement\n", "cleanup changed the replacement keep file")
			}
		})
	}
}
