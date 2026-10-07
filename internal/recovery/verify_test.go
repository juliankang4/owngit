package recovery

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"owngit/internal/state"
)

// newTwoRepositoryBackup backs up a state with two repositories that hold
// commits, "project" and "second", and an empty one, "empty".
func newTwoRepositoryBackup(t *testing.T, root string) string {
	t.Helper()
	ctx := context.Background()
	store, manager := newBackupStore(t, root)
	for _, name := range []string{"second", "empty"} {
		if _, err := manager.Create(ctx, name, ""); err != nil {
			t.Fatal(err)
		}
	}
	second, err := manager.Path("second")
	noErr(t, err)
	runGit(t, filepath.Join(root, "backup-work"), "push", second, "HEAD:refs/heads/main", "HEAD:refs/heads/other")
	backup := filepath.Join(root, "backup")
	_, err = CreateWithReport(ctx, store, manager, backup)
	noErr(t, err)
	return backup
}

// listing describes every entry under root, so a test can show that
// nothing there changed.
func listing(t *testing.T, root string) string {
	t.Helper()
	var entries []string
	noErr(t, filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		entries = append(entries, fmt.Sprintf("%s %v %d %d", path, info.Mode(), info.Size(), info.ModTime().UnixNano()))
		return nil
	}))
	return strings.Join(entries, "\n")
}

// Verify restores the backup in a folder of its own, reports each
// repository with its refs, checks the database, and removes the folder. It
// changes nothing beside or in the backup.
func TestVerifyRehearsesTheRestoreAndLeavesNothingBehind(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	backup := newTwoRepositoryBackup(t, root)
	temporary := filepath.Join(root, "temporary")
	noErr(t, os.Mkdir(temporary, 0o700))
	before := listing(t, backup)
	siblings, err := os.ReadDir(root)
	noErr(t, err)

	result, err := Verify(ctx, backup, temporary, "")
	noErr(t, err)
	if !result.Verified || result.Error != "" || result.Database != VerifyPassed || result.Version != closedPullRequestBackupVersion || result.CreatedAt == nil {
		t.Fatalf("result=%+v", result)
	}
	want := map[string]int{"project": 1, "second": 2, "empty": 0}
	if len(result.Repositories) != len(want) {
		t.Fatalf("repositories=%+v", result.Repositories)
	}
	for _, item := range result.Repositories {
		if item.Status != VerifyPassed || item.Error != "" || item.Refs != want[item.ID] {
			t.Errorf("repository %+v, want passed with %d refs", item, want[item.ID])
		}
	}
	if len(result.Limits) != 1 || !strings.Contains(result.Limits[0], "SHA-256") {
		t.Fatalf("limits=%q", result.Limits)
	}

	if left, err := os.ReadDir(temporary); err != nil || len(left) != 0 {
		t.Fatalf("the rehearsal left %v (%v)", left, err)
	}
	if after := listing(t, backup); after != before {
		t.Fatalf("the backup changed:\n%s\nwas\n%s", after, before)
	}
	if after, err := os.ReadDir(root); err != nil || len(after) != len(siblings) {
		t.Fatalf("entries beside the backup: %v, were %v (%v)", after, siblings, err)
	}
}

// A backup whose bundles are damaged is not verified. Each repository says
// whether it failed and why, and a failed repository does not stop the
// others from being restored and checked.
func TestVerifyNamesEachRepositoryThatFails(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	backup := newTwoRepositoryBackup(t, root)
	temporary := t.TempDir()

	// A bundle replaced along with its digest: only Git can tell.
	manifestPath := filepath.Join(backup, manifestName)
	manifest, err := readManifest(manifestPath)
	noErr(t, err)
	replaced := []byte("# v2 git bundle\nnot a bundle\n")
	noErr(t, os.WriteFile(filepath.Join(backup, "repositories", "second.bundle"), replaced, 0o600))
	digest := sha256.Sum256(replaced)
	for index := range manifest.Repositories {
		if manifest.Repositories[index].ID == "second" {
			manifest.Repositories[index].SHA256 = hex.EncodeToString(digest[:])
		}
	}
	file, err := os.OpenFile(manifestPath, os.O_WRONLY|os.O_TRUNC, 0)
	noErr(t, err)
	noErr(t, writeManifest(file, manifest))
	noErr(t, file.Close())

	result, err := Verify(ctx, backup, temporary, "")
	if err == nil || result.Verified || !strings.Contains(result.Error, `repository "second"`) || result.Database != VerifyNotRun {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	statuses := map[string]string{}
	for _, item := range result.Repositories {
		statuses[item.ID] = item.Status
		if item.ID == "second" && !strings.Contains(item.Error, "verify bundle") {
			t.Errorf("second failed with %q, want the bundle check", item.Error)
		}
	}
	if statuses["project"] != VerifyPassed || statuses["second"] != VerifyFailed || statuses["empty"] != VerifyPassed {
		t.Fatalf("statuses=%v", statuses)
	}

	// A bundle that no longer matches its digest fails too, and is named
	// with the other.
	project := filepath.Join(backup, "repositories", "project.bundle")
	file, err = os.OpenFile(project, os.O_APPEND|os.O_WRONLY, 0)
	noErr(t, err)
	_, err = file.WriteString("damage")
	noErr(t, err)
	noErr(t, file.Close())
	result, err = Verify(ctx, backup, temporary, "")
	if err == nil || result.Verified || !strings.Contains(result.Error, "checksum mismatch") || !strings.Contains(result.Error, `repository "second"`) {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	for _, item := range result.Repositories {
		wantStatus := VerifyFailed
		if item.ID == "empty" {
			wantStatus = VerifyPassed
		}
		if item.Status != wantStatus {
			t.Errorf("repository %+v, want %s", item, wantStatus)
		}
	}
	if left, err := os.ReadDir(temporary); err != nil || len(left) != 0 {
		t.Fatalf("the failed rehearsals left %v (%v)", left, err)
	}
}

// A restored database that fails SQLite's checks is not verified, although
// every repository passed.
func TestVerifyChecksTheRestoredDatabase(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	backup := newTwoRepositoryBackup(t, root)
	operations := defaultRestoreOperations()
	operations.openState = func(ctx context.Context, dir string) (*state.Store, error) {
		store, err := state.Open(ctx, dir)
		if err != nil || filepath.Base(dir) != "state" {
			return store, err
		}
		// The published rehearsal state gets a record that refers to a
		// missing repository.
		for _, statement := range []string{`PRAGMA foreign_keys=OFF`, `INSERT INTO repository_policies(repository_id,updated_at) VALUES('missing',0)`} {
			if err := store.Exec(ctx, statement); err != nil {
				store.Close()
				return nil, err
			}
		}
		return store, nil
	}
	result, err := verify(ctx, backup, t.TempDir(), "", operations)
	if err == nil || result.Verified || result.Database != VerifyFailed || !strings.Contains(result.Error, "refers to a missing record") {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	for _, item := range result.Repositories {
		if item.Status != VerifyPassed {
			t.Errorf("repository %+v, want passed", item)
		}
	}
}

// A released format older than the current one gets the same checks.
func TestVerifyChecksOlderFormatsTheSameWay(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	backup := newTwoRepositoryBackup(t, root)
	manifestPath := filepath.Join(backup, manifestName)
	manifest, err := readManifest(manifestPath)
	noErr(t, err)
	manifest.Version = checkBackupVersion
	file, err := os.OpenFile(manifestPath, os.O_WRONLY|os.O_TRUNC, 0)
	noErr(t, err)
	noErr(t, writeManifest(file, manifest))
	noErr(t, file.Close())

	result, err := Verify(ctx, backup, t.TempDir(), "")
	noErr(t, err)
	if !result.Verified || result.Version != checkBackupVersion || result.Database != VerifyPassed || len(result.Repositories) != 3 {
		t.Fatalf("result=%+v", result)
	}
}

// A manifest that fails validation lists no repositories, so nothing it
// holds, such as terminal control characters in a repository ID, reaches the
// result.
func TestVerifyShowsNothingFromAnInvalidManifest(t *testing.T) {
	root := t.TempDir()
	backup := newTwoRepositoryBackup(t, root)
	manifestPath := filepath.Join(backup, manifestName)
	manifest, err := readManifest(manifestPath)
	noErr(t, err)
	manifest.Repositories[0].ID = "\x1b]0;title\a\x1b[31m"
	file, err := os.OpenFile(manifestPath, os.O_WRONLY|os.O_TRUNC, 0)
	noErr(t, err)
	noErr(t, writeManifest(file, manifest))
	noErr(t, file.Close())

	result, err := Verify(context.Background(), backup, t.TempDir(), "")
	if err == nil || result.Verified || len(result.Repositories) != 0 || result.Version != 0 || strings.ContainsRune(result.Error, 0x1b) {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

// An interrupted verification stops, reports what it did not check as not
// run, and removes its folder. Folders that an earlier verification left are
// named and kept, and this run's own folder name is new.
func TestVerifyRemovesItsFolderWhenInterrupted(t *testing.T) {
	root := t.TempDir()
	backup := newTwoRepositoryBackup(t, root)
	temporary := t.TempDir()
	left := filepath.Join(temporary, rehearsalPrefix+"left")
	noErr(t, os.Mkdir(left, 0o700))
	assertOnlyLeft := func(t *testing.T) {
		t.Helper()
		entries, err := os.ReadDir(temporary)
		if err != nil || len(entries) != 1 || entries[0].Name() != filepath.Base(left) {
			t.Fatalf("temporary folder holds %v (%v), want only %s", entries, err, left)
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	result, err := Verify(ctx, backup, temporary, "")
	if err == nil || result.Verified || !strings.Contains(result.Error, "interrupted") {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	for _, item := range result.Repositories {
		if item.Status != VerifyNotRun {
			t.Errorf("repository %+v, want not run", item)
		}
	}
	if len(result.Leftovers) != 1 || filepath.Base(result.Leftovers[0]) != filepath.Base(left) {
		t.Fatalf("leftovers=%v, want %s", result.Leftovers, left)
	}
	assertOnlyLeft(t)

	// Interrupted after every repository was restored, while the restore
	// opens its staged state.
	ctx, cancel = context.WithCancel(context.Background())
	defer cancel()
	operations := defaultRestoreOperations()
	operations.openState = func(ctx context.Context, dir string) (*state.Store, error) {
		cancel()
		return state.Open(ctx, dir)
	}
	result, err = verify(ctx, backup, temporary, "", operations)
	if err == nil || result.Verified || !strings.Contains(result.Error, "interrupted") || result.Database != VerifyNotRun {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	assertOnlyLeft(t)
}

// A verification whose folder could not be removed is not verified, and
// says why.
func TestVerifyIsNotVerifiedUntilItsFolderIsGone(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("the fault is a Unix folder mode, which does not stop root")
	}
	backup := newTwoRepositoryBackup(t, t.TempDir())
	temporary := filepath.Join(t.TempDir(), "temporary")
	noErr(t, os.Mkdir(temporary, 0o700))
	operations := defaultRestoreOperations()
	operations.openState = func(ctx context.Context, dir string) (*state.Store, error) {
		// Once the rehearsal is published, its folder can no longer be
		// removed from the temporary folder.
		if filepath.Base(dir) == "state" {
			if err := os.Chmod(temporary, 0o500); err != nil {
				return nil, err
			}
		}
		return state.Open(ctx, dir)
	}
	result, err := verify(context.Background(), backup, temporary, "", operations)
	noErr(t, os.Chmod(temporary, 0o700))
	if err == nil || result.Verified || !strings.Contains(result.Error, "remove the rehearsal folder") || result.CleanupError == "" || result.Database != VerifyPassed {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

// A restore that its context stopped says that it was interrupted and that
// nothing was restored, and leaves no target. The command-line test stops
// one in the middle of its work.
func TestRestoreSaysItWasInterrupted(t *testing.T) {
	root := t.TempDir()
	backup := newTwoRepositoryBackup(t, root)
	stateTarget, repositoryTarget := filepath.Join(root, "restored-state"), filepath.Join(root, "restored-repositories")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := RestoreWithReport(ctx, backup, stateTarget, repositoryTarget, "")
	var interrupted *Interrupted
	if !errors.As(err, &interrupted) || err.Error() != "the restore was interrupted: nothing was restored" || !errors.Is(err, context.Canceled) {
		t.Fatalf("error=%v", err)
	}
	for _, target := range []string{stateTarget, repositoryTarget} {
		if _, err := os.Lstat(target); !os.IsNotExist(err) {
			t.Fatalf("the interrupted restore left %s (%v)", target, err)
		}
	}
}
