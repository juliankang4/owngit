package recovery

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"owngit/internal/gitexec"
	"owngit/internal/repository"
	"owngit/internal/state"
)

// commitInto writes an unrelated commit into a bare repository and returns
// its ID, without moving any ref.
func commitInto(t *testing.T, repositoryPath, message string) string {
	t.Helper()
	blob := gitOutputInput(t, repositoryPath, message+"\n", "--git-dir", ".", "hash-object", "-w", "--stdin")
	tree := gitOutputInput(t, repositoryPath, "100644 blob "+blob+"\tfile\n", "--git-dir", ".", "mktree")
	return gitOutput(t, repositoryPath, "-c", "user.name=Backup Test", "-c", "user.email=backup@example.invalid", "--git-dir", ".", "commit-tree", tree, "-m", message)
}

func gitOutputInput(t *testing.T, directory, input string, arguments ...string) string {
	t.Helper()
	command := exec.Command("git", arguments...)
	command.Dir = directory
	command.Stdin = strings.NewReader(input)
	output, err := command.Output()
	if err != nil {
		t.Fatalf("git %s: %v", strings.Join(arguments, " "), err)
	}
	return strings.TrimSpace(string(output))
}

// writeRefAsAPush moves a ref the way an OwnGit push does, with the
// repository write lock held.
func writeRefAsAPush(t *testing.T, manager *repository.Manager, id string, arguments ...string) {
	t.Helper()
	lock := manager.Locks.For(id)
	lock.Lock()
	defer lock.Unlock()
	path, err := manager.Path(id)
	noErr(t, err)
	runGit(t, path, append([]string{"--git-dir", "."}, arguments...)...)
}

// pausingRunner stops at the bundle of one repository until released.
type pausingRunner struct {
	delegate commandRunner
	at       string
	reached  chan struct{}
	release  chan struct{}
	once     sync.Once
}

func newPausingRunner(delegate commandRunner, at string) *pausingRunner {
	return &pausingRunner{delegate: delegate, at: at, reached: make(chan struct{}), release: make(chan struct{})}
}

func (runner *pausingRunner) Run(ctx context.Context, directory string, stdin io.Reader, arguments ...string) (gitexec.Result, error) {
	if strings.Contains(strings.Join(arguments, " "), "bundle create") && filepath.Base(directory) == runner.at+".git" {
		runner.once.Do(func() { close(runner.reached) })
		select {
		case <-runner.release:
		case <-ctx.Done():
			return gitexec.Result{}, ctx.Err()
		}
	}
	return runner.delegate.Run(ctx, directory, stdin, arguments...)
}

// A backup made while OwnGit serves describes the instant it began. While
// its bundles are written, pushes are not held, a force push and a branch
// deletion (as with kept history off) change nothing in the backup, a new
// repository is not in it, a repository whose bundle is not written yet
// cannot be deleted, and one whose bundle is written can. The backup
// verifies and restores to that instant.
func TestBackupWhileServingKeepsItsInstant(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	store, manager := newBackupStore(t, root)
	if _, err := manager.Create(ctx, "second", ""); err != nil {
		t.Fatal(err)
	}
	second, err := manager.Path("second")
	noErr(t, err)
	runGit(t, filepath.Join(root, "backup-work"), "push", second, "HEAD:refs/heads/main", "HEAD:refs/heads/topic")
	secondMain := gitOutput(t, second, "--git-dir", ".", "rev-parse", "refs/heads/main")
	project, err := manager.Path("project")
	noErr(t, err)
	projectMain := gitOutput(t, project, "--git-dir", ".", "rev-parse", "refs/heads/main")

	runner := newPausingRunner(manager.Git, "second")
	backup := filepath.Join(root, "backup")
	type outcome struct {
		report CaptureReport
		err    error
	}
	done := make(chan outcome, 1)
	go func() {
		report, err := create(ctx, store, manager, runner, backup, manifestLimit)
		done <- outcome{report, err}
	}()
	select {
	case <-runner.reached:
	case result := <-done:
		t.Fatalf("backup ended before its bundles: %+v", result)
	}

	// Pushes proceed while bundles are written.
	replacement := commitInto(t, second, "rewritten")
	writeRefAsAPush(t, manager, "second", "update-ref", "refs/heads/main", replacement)
	writeRefAsAPush(t, manager, "second", "update-ref", "-d", "refs/heads/topic")
	if _, err := manager.Create(ctx, "later", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Delete(ctx, "second", repository.DeleteFiles); !errors.Is(err, repository.ErrBackupReading) {
		t.Fatalf("deleting a repository not yet bundled: %v", err)
	}
	if _, err := manager.Delete(ctx, "project", repository.DeleteFiles); err != nil {
		t.Fatalf("deleting a repository already bundled: %v", err)
	}
	close(runner.release)
	result := <-done
	noErr(t, result.err)
	if result.report.Attempts != 1 || result.report.Repositories != 2 || result.report.LongestHold <= 0 || result.report.LongestHoldRepository == "" {
		t.Fatalf("report=%+v", result.report)
	}

	temporary := filepath.Join(root, "temporary")
	noErr(t, os.Mkdir(temporary, 0o700))
	verification, err := Verify(ctx, backup, temporary, "")
	noErr(t, err)
	if !verification.Verified {
		t.Fatalf("verification=%+v", verification)
	}
	restored := filepath.Join(root, "restored-repositories")
	noErr(t, Restore(ctx, backup, filepath.Join(root, "restored-state"), restored, ""))
	for ref, want := range map[string]string{"second.git refs/heads/main": secondMain, "second.git refs/heads/topic": secondMain, "project.git refs/heads/main": projectMain} {
		name := strings.Fields(ref)
		if got := gitOutput(t, filepath.Join(restored, name[0]), "--git-dir", ".", "rev-parse", name[1]); got != want {
			t.Fatalf("restored %s=%s, want %s", ref, got, want)
		}
	}
	if _, err := os.Stat(filepath.Join(restored, "later.git")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a repository created after the instant was restored: %v", err)
	}
	if _, err := manager.HoldForBackup(); err != nil {
		t.Fatalf("the finished backup kept its hold: %v", err)
	}
}

// A Git write in progress when a backup starts delays the backup, not the
// other way round, and the report says how long the backup held writes.
func TestBackupWaitsForAWriteInProgress(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	store, manager := newBackupStore(t, root)
	lock := manager.Locks.For("project")
	lock.Lock()
	done := make(chan error, 1)
	var report CaptureReport
	go func() {
		var err error
		report, err = CreateWhileServing(ctx, store, manager, filepath.Join(root, "backup"))
		done <- err
	}()
	select {
	case err := <-done:
		lock.Unlock()
		t.Fatalf("backup did not wait for the write: %v", err)
	case <-time.After(300 * time.Millisecond):
	}
	lock.Unlock()
	noErr(t, <-done)
	if report.Attempts != 1 || report.LongestHoldRepository != "project" {
		t.Fatalf("report=%+v", report)
	}
}

// A repository that stays busy for the whole lock window is given up for
// this attempt, and nothing stays locked.
func TestCaptureGivesUpABusyRepository(t *testing.T) {
	root := t.TempDir()
	_, manager := newBackupStore(t, root)
	if _, err := manager.Create(context.Background(), "zeta", ""); err != nil {
		t.Fatal(err)
	}
	busy := manager.Locks.For("zeta")
	busy.Lock()
	locks := &captureLocks{manager: manager, taken: map[string]time.Time{}}
	reason, err := locks.take(context.Background(), []string{"project", "zeta"})
	busy.Unlock()
	if err != nil || !strings.Contains(reason, `"zeta"`) {
		t.Fatalf("reason=%q err=%v", reason, err)
	}
	// The given-up wait takes the lock once it is free and releases it at
	// once, so it may still hold it for a moment.
	for _, id := range []string{"project", "zeta"} {
		deadline := time.Now().Add(2 * time.Second)
		for !manager.Locks.For(id).TryLock() {
			if time.Now().After(deadline) {
				t.Fatalf("%s stayed locked", id)
			}
			time.Sleep(time.Millisecond)
		}
		manager.Locks.For(id).Unlock()
	}
}

// A repository that the backup cannot describe fails the backup, named,
// and nothing is published: an unreadable HEAD, a ref writer that could
// not be stopped, and objects kept in another repository or on a promisor
// remote.
func TestBackupRefusesRepositoriesItCannotDescribe(t *testing.T) {
	for name, damage := range map[string]func(t *testing.T, manager *repository.Manager, path string){
		"unreadable HEAD": func(t *testing.T, manager *repository.Manager, path string) {
			noErr(t, os.WriteFile(filepath.Join(path, "HEAD"), []byte(strings.Repeat("1", 40)+"\n"), 0o600))
		},
		"unsettled ref writer": func(t *testing.T, manager *repository.Manager, path string) {
			manager.NoteUnsettledRefWriter("damaged")
		},
		"alternates": func(t *testing.T, manager *repository.Manager, path string) {
			other, err := manager.Path("project")
			noErr(t, err)
			noErr(t, os.WriteFile(filepath.Join(path, "objects", "info", "alternates"), []byte(filepath.Join(other, "objects")+"\n"), 0o600))
		},
		"promisor remote": func(t *testing.T, manager *repository.Manager, path string) {
			runGit(t, path, "--git-dir", ".", "config", "remote.origin.promisor", "true")
		},
	} {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			root := t.TempDir()
			store, manager := newBackupStore(t, root)
			if _, err := manager.Create(ctx, "damaged", ""); err != nil {
				t.Fatal(err)
			}
			path, err := manager.Path("damaged")
			noErr(t, err)
			damage(t, manager, path)
			backup := filepath.Join(root, "backup")
			_, err = CreateWhileServing(ctx, store, manager, backup)
			if err == nil || !strings.Contains(err.Error(), `"damaged"`) {
				t.Fatalf("err=%v", err)
			}
			assertNoRecoveryOutputOrStages(t, backup, ".owngit-backup-")
			if _, err := manager.HoldForBackup(); err != nil {
				t.Fatalf("the failed backup kept its hold: %v", err)
			}
		})
	}
}

// A destination that fills up fails the backup as a space error naming the
// folder, and leaves nothing there.
func TestBackupReportsAFullDestination(t *testing.T) {
	root := t.TempDir()
	store, manager := newBackupStore(t, root)
	backup := filepath.Join(root, "backup")
	_, err := create(context.Background(), store, manager, fullDiskRunner{delegate: manager.Git}, backup, manifestLimit)
	var space *SpaceError
	if !errors.As(err, &space) || space.Dir != root {
		t.Fatalf("err=%v", err)
	}
	assertNoRecoveryOutputOrStages(t, backup, ".owngit-backup-")
}

type fullDiskRunner struct{ delegate commandRunner }

func (runner fullDiskRunner) Run(ctx context.Context, directory string, stdin io.Reader, arguments ...string) (gitexec.Result, error) {
	if strings.Contains(strings.Join(arguments, " "), "bundle create") {
		return gitexec.Result{}, errors.New("git bundle: exit status 128: fatal: sha1 file 'x' write error: No space left on device")
	}
	return runner.delegate.Run(ctx, directory, stdin, arguments...)
}

// With OWNGIT_TEST_FULL_DISK_DIR naming an empty folder on a small file
// system that the backup cannot fit on, a real full disk fails the backup
// with a space error and leaves nothing there.
func TestBackupOntoARealFullDisk(t *testing.T) {
	destination := os.Getenv("OWNGIT_TEST_FULL_DISK_DIR")
	if destination == "" {
		t.Skip("set OWNGIT_TEST_FULL_DISK_DIR to an empty folder on a file system of a few MiB")
	}
	ctx := context.Background()
	root := t.TempDir()
	store, manager := newBackupStore(t, root)
	work := filepath.Join(root, "backup-work")
	large := make([]byte, 64<<20)
	for index := range large {
		large[index] = byte(index*7919 + index>>13)
	}
	noErr(t, os.WriteFile(filepath.Join(work, "large"), large, 0o600))
	runGit(t, work, "add", ".")
	runGit(t, work, "commit", "-m", "large")
	project, err := manager.Path("project")
	noErr(t, err)
	runGit(t, work, "push", project, "HEAD:refs/heads/main")
	backup := filepath.Join(destination, "backup")
	_, err = CreateWhileServing(ctx, store, manager, backup)
	var space *SpaceError
	if !errors.As(err, &space) {
		t.Fatalf("err=%v", err)
	}
	entries, readErr := os.ReadDir(destination)
	noErr(t, readErr)
	if len(entries) != 0 {
		t.Fatalf("the full destination kept %d entries", len(entries))
	}
}

// A backup process that is killed while it writes bundles publishes
// nothing, changes neither the state nor the repositories, and leaves no
// hold behind: a new backup succeeds.
func TestKilledBackupPublishesNothing(t *testing.T) {
	if root := os.Getenv("OWNGIT_TEST_KILLED_BACKUP_ROOT"); root != "" {
		runKilledBackupChild(t, root)
		return
	}
	ctx := context.Background()
	root := t.TempDir()
	store, manager := newBackupStore(t, root)
	before, err := store.RecoverySnapshot(ctx)
	noErr(t, err)
	project, err := manager.Path("project")
	noErr(t, err)
	refsBefore := gitOutput(t, project, "--git-dir", ".", "for-each-ref")
	ready := filepath.Join(root, "child-ready")
	child := exec.Command(os.Args[0], "-test.run=^TestKilledBackupPublishesNothing$", "-test.count=1")
	child.Env = append(os.Environ(), "OWNGIT_TEST_KILLED_BACKUP_ROOT="+root)
	noErr(t, child.Start())
	defer child.Process.Kill()
	deadline := time.Now().Add(60 * time.Second)
	for {
		if _, err := os.Stat(ready); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the child backup never reached its bundle")
		}
		time.Sleep(20 * time.Millisecond)
	}
	noErr(t, child.Process.Kill())
	_ = child.Wait()

	backup := filepath.Join(root, "backup")
	if _, err := os.Lstat(backup); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a killed backup was published: %v", err)
	}
	after, err := store.RecoverySnapshot(ctx)
	noErr(t, err)
	if len(after.Repositories) != len(before.Repositories) || len(after.ImportRuns) != len(before.ImportRuns) {
		t.Fatalf("the killed backup changed the state: before=%+v after=%+v", before.Repositories, after.Repositories)
	}
	if refs := gitOutput(t, project, "--git-dir", ".", "for-each-ref"); refs != refsBefore {
		t.Fatalf("the killed backup changed refs:\n%s\nwant:\n%s", refs, refsBefore)
	}
	_, err = CreateWhileServing(ctx, store, manager, filepath.Join(root, "second-backup"))
	noErr(t, err)
}

// runKilledBackupChild backs up the state under root and stops at the
// first bundle, after telling the parent it got there.
func runKilledBackupChild(t *testing.T, root string) {
	ctx := context.Background()
	store, err := state.Open(ctx, filepath.Join(root, "source-state"))
	noErr(t, err)
	settings, err := store.Settings(ctx)
	noErr(t, err)
	runner, err := gitexec.New("", filepath.Join(root, "source-state", "runtime"))
	noErr(t, err)
	manager := &repository.Manager{Store: store, Git: runner, Locks: gitexec.NewLocks(), Root: settings.RepositoryRoot}
	stopping := stoppingRunner{delegate: runner, ready: filepath.Join(root, "child-ready")}
	_, err = create(ctx, store, manager, stopping, filepath.Join(root, "backup"), manifestLimit)
	t.Fatalf("the child backup was not killed: %v", err)
}

type stoppingRunner struct {
	delegate commandRunner
	ready    string
}

func (runner stoppingRunner) Run(ctx context.Context, directory string, stdin io.Reader, arguments ...string) (gitexec.Result, error) {
	if strings.Contains(strings.Join(arguments, " "), "bundle create") {
		if err := os.WriteFile(runner.ready, nil, 0o600); err != nil {
			return gitexec.Result{}, err
		}
		select {}
	}
	return runner.delegate.Run(ctx, directory, stdin, arguments...)
}
