package backups

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"owngit/internal/auth"
	"owngit/internal/gitexec"
	"owngit/internal/recovery"
	"owngit/internal/repository"
	"owngit/internal/state"
	"owngit/internal/testfixture"
)

type clock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *clock) advance(by time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(by)
}

type fixture struct {
	store       *state.Store
	manager     *repository.Manager
	service     *Service
	clock       *clock
	destination string
}

// newFixture is a configured OwnGit with a repository that has a commit and
// an empty one, and a backup service that no scheduler drives: the tests
// call tick themselves.
func newFixture(t *testing.T) *fixture {
	t.Helper()
	ctx := context.Background()
	root := t.TempDir()
	repositoriesRoot := filepath.Join(root, "repositories")
	noErr(t, os.Mkdir(repositoriesRoot, 0o700))
	store, err := state.Open(ctx, filepath.Join(root, "state"))
	noErr(t, err)
	t.Cleanup(func() { _ = store.Close() })
	adminHash, err := auth.HashPassword("admin-password")
	noErr(t, err)
	noErr(t, store.CompleteSetup(ctx, repositoriesRoot, "open", "", adminHash, false))
	runner, err := gitexec.New("", filepath.Join(root, "state", "runtime"))
	noErr(t, err)
	manager := &repository.Manager{Store: store, Git: runner, Locks: gitexec.NewLocks(), Root: repositoriesRoot}
	for _, id := range []string{"project", "empty"} {
		if _, err := manager.Create(ctx, id, ""); err != nil {
			t.Fatal(err)
		}
	}
	remote, err := manager.Path("project")
	noErr(t, err)
	work := filepath.Join(root, "work")
	git(t, "", "init", "--initial-branch=main", work)
	git(t, work, "config", "user.name", "Backup Test")
	git(t, work, "config", "user.email", "backup@example.invalid")
	noErr(t, os.WriteFile(filepath.Join(work, "file"), []byte("data"), 0o600))
	git(t, work, "add", ".")
	git(t, work, "commit", "-m", "data")
	git(t, work, "push", remote, "HEAD:refs/heads/main")

	fake := &clock{now: time.Now().Truncate(time.Second)}
	service := &Service{Store: store, Repositories: manager, Now: fake.Now, Logf: t.Logf}
	service.open(ctx)
	t.Cleanup(func() { noErr(t, service.Stop(context.Background())) })
	return &fixture{store: store, manager: manager, service: service, clock: fake, destination: filepath.Join(root, "backups")}
}

func (f *fixture) configure(t *testing.T, change ScheduleChange) state.BackupSchedule {
	t.Helper()
	if change.Destination == nil {
		if _, configured, err := f.store.BackupSchedule(context.Background()); err != nil || !configured {
			change.Destination = &f.destination
		}
	}
	_, _, after, err := f.service.ChangeSchedule(context.Background(), change)
	noErr(t, err)
	return after
}

// backUpNow starts a backup, waits for it to end, and returns its record.
// Every backup starts an hour after the one before.
func (f *fixture) backUpNow(t *testing.T) state.BackupRun {
	t.Helper()
	f.clock.advance(time.Hour)
	run, err := f.service.StartNow()
	noErr(t, err)
	f.service.work.Wait()
	return f.run(t, run.ID)
}

func (f *fixture) run(t *testing.T, id string) state.BackupRun {
	t.Helper()
	runs, err := f.store.BackupRuns(context.Background())
	noErr(t, err)
	for _, run := range runs {
		if run.ID == id {
			return run
		}
	}
	t.Fatalf("no run %s", id)
	return state.BackupRun{}
}

// present says whether run's folder holds a backup.
func present(run state.BackupRun) bool {
	folder, err := recovery.OpenBackupFolder(run.Destination)
	if err != nil {
		return false
	}
	defer folder.Close()
	backup, err := folder.Open(run.BackupName)
	if err != nil {
		return false
	}
	backup.Close()
	return true
}

func TestBackUpNowWritesAndVerifiesABackup(t *testing.T) {
	f := newFixture(t)
	if _, err := f.service.StartNow(); !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("backup without a folder: %v", err)
	}
	schedule := f.configure(t, ScheduleChange{})
	if !schedule.Enabled || schedule.Interval != 24*time.Hour || schedule.Keep != 7 || !schedule.Verify {
		t.Fatalf("defaults: %+v", schedule)
	}
	run := f.backUpNow(t)
	if run.Kind != state.BackupRunManual || run.Status != state.BackupSucceeded || run.Verification != state.BackupVerifyPassed || run.Message != "" {
		t.Fatalf("run: %+v", run)
	}
	if !run.HoldKnown || run.LongestHoldRepository == "" || run.LongestHold < 0 {
		t.Fatalf("the run did not record how long Git writes waited: %+v", run)
	}
	if !present(run) || !strings.HasPrefix(run.BackupName, namePrefix) {
		t.Fatalf("backup %s is not there", run.BackupName)
	}
	status, err := f.service.Status(context.Background())
	noErr(t, err)
	if status.Schedule.State != ScheduleOn || status.Running != nil || status.LastRun == nil || status.LastRun.ID != run.ID ||
		status.LastVerified == nil || status.LastVerified.ID != run.ID || status.LastRun.Path == "" || status.NextRun == nil {
		t.Fatalf("status: %+v", status)
	}
}

func TestScheduledBackupsRunOncePerInterval(t *testing.T) {
	f := newFixture(t)
	if wait := f.service.tick(); wait != recheck {
		t.Fatalf("not configured: waits %s", wait)
	}
	f.configure(t, ScheduleChange{})
	// The first scheduled backup is due at once.
	f.service.tick()
	f.service.work.Wait()
	runs, err := f.store.BackupRuns(context.Background())
	noErr(t, err)
	if len(runs) != 1 || runs[0].Kind != state.BackupRunScheduled || runs[0].Status != state.BackupSucceeded {
		t.Fatalf("runs: %+v", runs)
	}
	if !runs[0].StartedAt.Equal(f.clock.Now()) {
		t.Fatalf("the run started at %s, not at the clock's %s", runs[0].StartedAt, f.clock.Now())
	}
	f.clock.advance(23 * time.Hour)
	if wait := f.service.tick(); wait != time.Hour {
		t.Fatalf("an hour before the next backup: waits %s", wait)
	}
	f.clock.advance(time.Hour)
	f.service.tick()
	f.service.work.Wait()
	// Another look at the same instant finds the slot used.
	f.service.tick()
	f.service.work.Wait()
	runs, err = f.store.BackupRuns(context.Background())
	noErr(t, err)
	if len(runs) != 2 || runs[0].Kind != state.BackupRunScheduled || !runs[0].StartedAt.Equal(f.clock.Now()) {
		t.Fatalf("runs: %+v", runs)
	}

	off := false
	f.configure(t, ScheduleChange{Enabled: &off})
	f.clock.advance(48 * time.Hour)
	f.service.tick()
	f.service.work.Wait()
	if runs, _ := f.store.BackupRuns(context.Background()); len(runs) != 2 {
		t.Fatalf("scheduled backups ran while off: %d runs", len(runs))
	}
	status, err := f.service.Status(context.Background())
	noErr(t, err)
	if status.Schedule.State != ScheduleOff || status.NextRun != nil {
		t.Fatalf("status while off: %+v", status)
	}
	// Back up now still works while scheduled backups are off.
	if run := f.backUpNow(t); run.Status != state.BackupSucceeded {
		t.Fatalf("run: %+v", run)
	}
}

func TestSecondBackupIsRefusedWhileOneRuns(t *testing.T) {
	f := newFixture(t)
	f.configure(t, ScheduleChange{})
	other := state.BackupRun{ID: strings.Repeat("a", 32), Kind: state.BackupRunManual, Destination: f.destination, BackupName: namePrefix + "other", StartedAt: f.clock.Now()}
	noErr(t, f.store.StartBackupRun(context.Background(), other))
	if _, err := f.service.StartNow(); !errors.Is(err, state.ErrBackupRunning) {
		t.Fatalf("second backup: %v", err)
	}
	f.service.tick()
	f.service.work.Wait()
	if runs, _ := f.store.BackupRuns(context.Background()); len(runs) != 1 {
		t.Fatalf("a scheduled backup started beside a running one: %+v", runs)
	}
}

// A run that a stopped process left running becomes interrupted, never a
// success, and its slot is used, so the schedule does not repeat it.
func TestRestartRecordsARunningBackupAsInterrupted(t *testing.T) {
	f := newFixture(t)
	f.configure(t, ScheduleChange{})
	left := state.BackupRun{ID: strings.Repeat("b", 32), Kind: state.BackupRunScheduled, Destination: f.destination, BackupName: namePrefix + "left", StartedAt: f.clock.Now()}
	noErr(t, f.store.StartBackupRun(context.Background(), left))

	restarted := &Service{Store: f.store, Repositories: f.manager, Now: f.clock.Now, Logf: t.Logf}
	noErr(t, restarted.Start(context.Background()))
	noErr(t, restarted.Stop(context.Background()))
	run := f.run(t, left.ID)
	if run.Status != state.BackupInterrupted || run.Verification != state.BackupVerifyNotRun || run.Message != interruptedMessage || run.FinishedAt.IsZero() {
		t.Fatalf("left run: %+v", run)
	}
	next, err := f.service.nextRun(context.Background())
	noErr(t, err)
	if !next.Equal(left.StartedAt.Add(24 * time.Hour)) {
		t.Fatalf("next scheduled backup %s, want one interval after the interrupted one", next)
	}
}

// A backup that OwnGit stops while it runs ends interrupted, and nothing
// is published.
func TestStoppedBackupIsInterrupted(t *testing.T) {
	f := newFixture(t)
	schedule := f.configure(t, ScheduleChange{})
	run := state.BackupRun{ID: strings.Repeat("c", 32), Kind: state.BackupRunManual, Status: state.BackupRunning, Destination: f.destination,
		BackupName: namePrefix + "stopped", Verification: state.BackupVerifyNotRun, StartedAt: f.clock.Now()}
	noErr(t, f.store.StartBackupRun(context.Background(), run))
	stopped, cancel := context.WithCancel(context.Background())
	cancel()
	f.service.execute(stopped, run, schedule)
	run = f.run(t, run.ID)
	if run.Status != state.BackupInterrupted || present(run) {
		t.Fatalf("stopped run: %+v, published %v", run, present(run))
	}
}

func TestVerificationPastItsLimitFails(t *testing.T) {
	f := newFixture(t)
	f.configure(t, ScheduleChange{})
	f.service.VerifyLimit = time.Nanosecond
	run := f.backUpNow(t)
	if run.Status != state.BackupFailed || run.Verification != state.BackupVerifyFailed || !strings.Contains(run.Message, "did not finish within 1ns") {
		t.Fatalf("run: %+v", run)
	}
	status, err := f.service.Status(context.Background())
	noErr(t, err)
	if status.LastVerified != nil || status.LastRun.Status != state.BackupFailed {
		t.Fatalf("status: %+v", status)
	}
}

func TestRetentionRemovesOnlyOlderBackupsOwnGitWrote(t *testing.T) {
	f := newFixture(t)
	keep := 2
	f.configure(t, ScheduleChange{Keep: &keep})
	first := f.backUpNow(t)
	// A folder named like a backup and a file that OwnGit did not write.
	foreign := filepath.Join(f.destination, namePrefix+"foreign")
	noErr(t, os.CopyFS(foreign, os.DirFS(filepath.Join(f.destination, first.BackupName))))
	noErr(t, os.WriteFile(filepath.Join(f.destination, "notes.txt"), []byte("mine"), 0o600))
	second := f.backUpNow(t)
	// The owner put a file into the second backup, so OwnGit will not
	// remove it and says so.
	added := filepath.Join(f.destination, second.BackupName, "readme.txt")
	noErr(t, os.WriteFile(added, []byte("mine"), 0o600))
	third := f.backUpNow(t)
	if !present(third) || !present(second) || present(first) || third.Message != "" {
		t.Fatalf("after the third backup: first %v second %v third %v, %q", present(first), present(second), present(third), third.Message)
	}
	fourth := f.backUpNow(t)
	if fourth.Status != state.BackupSucceeded || !strings.Contains(fourth.Message, "could not remove the older backup") ||
		!strings.Contains(fourth.Message, "readme.txt") || !present(second) {
		t.Fatalf("fourth: %+v, second present %v", fourth, present(second))
	}
	if _, err := os.Stat(added); err != nil {
		t.Fatalf("the owner's file is gone: %v", err)
	}
	for _, path := range []string{foreign, filepath.Join(f.destination, "notes.txt")} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("%s is gone: %v", path, err)
		}
	}
}

func TestRetentionKeepsTheLastVerifiedBackup(t *testing.T) {
	f := newFixture(t)
	keep := 1
	f.configure(t, ScheduleChange{Keep: &keep})
	verified := f.backUpNow(t)
	off := false
	f.configure(t, ScheduleChange{Verify: &off})
	second := f.backUpNow(t)
	third := f.backUpNow(t)
	if second.Verification != state.BackupVerifyNotRun || third.Status != state.BackupSucceeded {
		t.Fatalf("unverified runs: %+v %+v", second, third)
	}
	if !present(verified) || present(second) || !present(third) {
		t.Fatalf("verified %v second %v third %v", present(verified), present(second), present(third))
	}
	// A backup that fails verification removes nothing.
	on := true
	f.configure(t, ScheduleChange{Verify: &on})
	f.service.VerifyLimit = time.Nanosecond
	failed := f.backUpNow(t)
	if failed.Status != state.BackupFailed || !present(verified) || !present(third) || !present(failed) {
		t.Fatalf("after a failed verification: %+v, verified %v third %v", failed, present(verified), present(third))
	}
	status, err := f.service.Status(context.Background())
	noErr(t, err)
	if status.LastVerified == nil || status.LastVerified.ID != verified.ID {
		t.Fatalf("last verified: %+v", status.LastVerified)
	}
}

func TestScheduleRefusesAFolderInsideOwnGitStorage(t *testing.T) {
	f := newFixture(t)
	inside := filepath.Join(f.manager.RepositoryRoot(), "backups")
	for _, destination := range []string{"relative/backups", inside, filepath.Join(f.store.Dir(), "backups")} {
		_, _, _, err := f.service.ChangeSchedule(context.Background(), ScheduleChange{Destination: &destination})
		var refused *ChangeError
		if !errors.As(err, &refused) {
			t.Errorf("%s: %v", destination, err)
		}
	}
	if _, configured, _ := f.store.BackupSchedule(context.Background()); configured {
		t.Fatal("a refused folder was saved")
	}
	keep := 3
	if _, _, _, err := f.service.ChangeSchedule(context.Background(), ScheduleChange{Keep: &keep}); err == nil {
		t.Fatal("a schedule without a folder was saved")
	}
}

func git(t *testing.T, directory string, arguments ...string) {
	t.Helper()
	command := exec.Command("git", arguments...)
	command.Dir = directory
	command.Env = testfixture.GitEnvironment(append(os.Environ(), "GIT_TERMINAL_PROMPT=0"))
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(arguments, " "), err, output)
	}
}

func noErr(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

// The scheduler of a started service makes the first backup as soon as a
// folder is set, and a stop ends it.
func TestSchedulerStartsTheFirstBackupWhenAFolderIsSet(t *testing.T) {
	f := newFixture(t)
	ended := make(chan string, 4)
	running := &Service{Store: f.store, Repositories: f.manager, Now: f.clock.Now, Logf: func(format string, arguments ...any) {
		if line := fmt.Sprintf(format, arguments...); strings.HasPrefix(line, "backup ") {
			ended <- line
		}
	}}
	noErr(t, running.Start(context.Background()))
	defer func() { noErr(t, running.Stop(context.Background())) }()
	_, _, _, err := running.ChangeSchedule(context.Background(), ScheduleChange{Destination: &f.destination})
	noErr(t, err)
	select {
	case line := <-ended:
		if !strings.Contains(line, " succeeded") {
			t.Fatalf("scheduled backup: %s", line)
		}
	case <-time.After(2 * time.Minute):
		t.Fatal("no scheduled backup ended")
	}
	runs, err := f.store.BackupRuns(context.Background())
	noErr(t, err)
	if len(runs) != 1 || runs[0].Kind != state.BackupRunScheduled || runs[0].Verification != state.BackupVerifyPassed {
		t.Fatalf("runs: %+v", runs)
	}
}

// runAt starts and executes a manual backup with the given ID, as start
// does, and returns its record.
func (f *fixture) runAt(t *testing.T, id string, started time.Time) state.BackupRun {
	t.Helper()
	schedule, _, err := f.store.BackupSchedule(context.Background())
	noErr(t, err)
	run := state.BackupRun{ID: id, Kind: state.BackupRunManual, Status: state.BackupRunning, Destination: schedule.Destination,
		BackupName: namePrefix + started.UTC().Format("20060102-150405") + "-" + id[:8], Verification: state.BackupVerifyNotRun, StartedAt: started}
	noErr(t, f.store.StartBackupRun(context.Background(), run))
	f.service.execute(context.Background(), run, schedule)
	return f.run(t, id)
}

// Two backups started in the same second keep the newer one: the order is
// the order the runs started, not their random IDs.
func TestRetentionKeepsTheNewerOfTwoBackupsInTheSameSecond(t *testing.T) {
	f := newFixture(t)
	keep, off := 1, false
	f.configure(t, ScheduleChange{Keep: &keep, Verify: &off})
	started := time.Now().Truncate(time.Second)
	older := f.runAt(t, strings.Repeat("f", 32), started)
	newer := f.runAt(t, strings.Repeat("0", 32), started)
	if newer.Status != state.BackupSucceeded || !present(newer) || present(state.BackupRun{Destination: f.destination, BackupName: namePrefix + started.UTC().Format("20060102-150405") + "-ffffffff"}) {
		t.Fatalf("newer %+v present %v; older %+v", newer, present(newer), older)
	}
	status, err := f.service.Status(context.Background())
	noErr(t, err)
	if status.LastRun == nil || status.LastRun.ID != newer.ID {
		t.Fatalf("last run: %+v", status.LastRun)
	}
}

// A backup folder replaced by a link is not followed: the backup the link
// leads to stays, and the run says what it left.
func TestRetentionDoesNotFollowALink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("links need a privilege on Windows")
	}
	f := newFixture(t)
	keep, off := 1, false
	f.configure(t, ScheduleChange{Keep: &keep, Verify: &off})
	first := f.backUpNow(t)
	elsewhere := filepath.Join(t.TempDir(), "elsewhere")
	noErr(t, os.Rename(filepath.Join(f.destination, first.BackupName), elsewhere))
	noErr(t, os.Symlink(elsewhere, filepath.Join(f.destination, first.BackupName)))
	second := f.backUpNow(t)
	if second.Status != state.BackupSucceeded || !strings.Contains(second.Message, first.BackupName) || !strings.Contains(second.Message, "left in place") {
		t.Fatalf("second: %+v", second)
	}
	for _, name := range []string{"manifest.json", filepath.Join("repositories", "project.bundle")} {
		if _, err := os.Stat(filepath.Join(elsewhere, name)); err != nil {
			t.Fatalf("the backup behind the link lost %s: %v", name, err)
		}
	}
	if run := f.run(t, first.ID); run.BackupName != "" {
		t.Fatalf("the run still claims the linked folder: %+v", run)
	}
}

// A file added to a backup is never removed, even one named like a bundle.
func TestRetentionLeavesAnAddedBundleFile(t *testing.T) {
	f := newFixture(t)
	keep, off := 1, false
	f.configure(t, ScheduleChange{Keep: &keep, Verify: &off})
	first := f.backUpNow(t)
	added := filepath.Join(f.destination, first.BackupName, "repositories", "foreign.bundle")
	noErr(t, os.WriteFile(added, []byte("mine"), 0o600))
	second := f.backUpNow(t)
	if !strings.Contains(second.Message, "foreign.bundle") || !present(first) {
		t.Fatalf("second: %+v, first present %v", second, present(first))
	}
	if content, err := os.ReadFile(added); err != nil || string(content) != "mine" {
		t.Fatalf("the added file: %q %v", content, err)
	}
}

// A backup whose manifest cannot be read is reported and left, never taken
// for absent, and the free space goes unchecked with that reason.
func TestRetentionReportsAnUnreadableBackup(t *testing.T) {
	f := newFixture(t)
	keep := 1
	f.configure(t, ScheduleChange{Keep: &keep})
	first := f.backUpNow(t)
	noErr(t, os.WriteFile(filepath.Join(f.destination, first.BackupName, "manifest.json"), []byte("{\"format\":"), 0o600))
	off := false
	f.configure(t, ScheduleChange{Verify: &off})
	second := f.backUpNow(t)
	if second.Status != state.BackupSucceeded || !strings.Contains(second.Message, "could not read the older backup "+first.BackupName) ||
		!strings.Contains(second.Message, "free space was not checked") {
		t.Fatalf("second: %+v", second)
	}
	if _, err := os.Stat(filepath.Join(f.destination, first.BackupName, "repositories", "project.bundle")); err != nil {
		t.Fatalf("the unreadable backup lost its bundle: %v", err)
	}
	if run := f.run(t, first.ID); run.BackupName == "" {
		t.Fatal("the unreadable backup was forgotten")
	}
}

// Forgetting old run records keeps the newest scheduled run, so its slot
// is not run again.
func TestForgettingRunsKeepsTheScheduledSlot(t *testing.T) {
	f := newFixture(t)
	f.configure(t, ScheduleChange{})
	ctx := context.Background()
	finish := func(run state.BackupRun) state.BackupRun {
		noErr(t, f.store.StartBackupRun(ctx, run))
		run.Status, run.Verification, run.BackupName, run.FinishedAt = state.BackupFailed, state.BackupVerifyNotRun, "", run.StartedAt
		noErr(t, f.store.FinishBackupRun(ctx, run))
		return run
	}
	scheduled := finish(state.BackupRun{ID: fmt.Sprintf("%032x", 1), Kind: state.BackupRunScheduled, Destination: f.destination, StartedAt: f.clock.Now()})
	var last state.BackupRun
	for index := 2; index <= runsKept+2; index++ {
		last = finish(state.BackupRun{ID: fmt.Sprintf("%032x", index), Kind: state.BackupRunManual, Destination: f.destination, StartedAt: f.clock.Now()})
	}
	noErr(t, f.service.forgetOldRuns(ctx, last))
	runs, err := f.store.BackupRuns(ctx)
	noErr(t, err)
	if len(runs) != runsKept+1 {
		t.Fatalf("%d runs kept", len(runs))
	}
	next, err := f.service.nextRun(ctx)
	noErr(t, err)
	if !next.Equal(scheduled.StartedAt.Add(24 * time.Hour)) {
		t.Fatalf("next scheduled backup %s, want one interval after %s", next, scheduled.StartedAt)
	}
}

// Status and its summary come from OwnGit's records alone, so a backup
// folder that is gone, or would hang, does not hold them up.
func TestStatusReadsNoBackupFolder(t *testing.T) {
	f := newFixture(t)
	f.configure(t, ScheduleChange{})
	verified := f.backUpNow(t)
	noErr(t, os.Rename(f.destination, f.destination+"-away"))
	noErr(t, os.WriteFile(f.destination, []byte("not a folder"), 0o600))
	status, err := f.service.Status(context.Background())
	noErr(t, err)
	summary := Summarize(status)
	if status.LastVerified == nil || status.LastVerified.ID != verified.ID || summary.LastVerifiedAt == nil || summary.LastRun == nil {
		t.Fatalf("status %+v summary %+v", status, summary)
	}
}

// A run owns its folder only while it holds the very manifest the run
// wrote.
func TestARunOwnsOnlyTheManifestItWrote(t *testing.T) {
	f := newFixture(t)
	f.configure(t, ScheduleChange{})
	run := f.backUpNow(t)
	folder, err := recovery.OpenBackupFolder(f.destination)
	noErr(t, err)
	defer folder.Close()
	backup, err := openOwned(folder, run)
	noErr(t, err)
	backup.Close()
	for _, digest := range []string{"", strings.Repeat("0", 64)} {
		other := run
		other.ManifestSHA256 = digest
		if _, err := openOwned(folder, other); !errors.Is(err, errNotOwned) {
			t.Errorf("a run with manifest digest %q owns the backup: %v", digest, err)
		}
	}
	missing := run
	missing.BackupName = namePrefix + "missing"
	if _, err := openOwned(folder, missing); !errors.Is(err, errMissing) {
		t.Errorf("missing: %v", err)
	}
}

// Two verified backups made in the same second whose folders were
// exchanged are both left: each folder holds the other run's manifest.
func TestRetentionLeavesExchangedBackupsOfTheSameSecond(t *testing.T) {
	f := newFixture(t)
	keep := 2
	f.configure(t, ScheduleChange{Keep: &keep})
	started := f.clock.Now()
	older := f.runAt(t, strings.Repeat("f", 32), started)
	newer := f.runAt(t, strings.Repeat("e", 32), started)
	if older.Verification != state.BackupVerifyPassed || newer.Verification != state.BackupVerifyPassed {
		t.Fatalf("runs: %+v %+v", older, newer)
	}
	olderPath, newerPath := filepath.Join(f.destination, older.BackupName), filepath.Join(f.destination, newer.BackupName)
	noErr(t, os.Rename(olderPath, olderPath+"-swap"))
	noErr(t, os.Rename(newerPath, olderPath))
	noErr(t, os.Rename(olderPath+"-swap", newerPath))
	third := f.runAt(t, strings.Repeat("d", 32), started)
	if third.Status != state.BackupSucceeded || !strings.Contains(third.Message, "holds another backup") {
		t.Fatalf("third: %+v", third)
	}
	for _, path := range []string{olderPath, newerPath} {
		if _, err := os.Stat(filepath.Join(path, "manifest.json")); err != nil {
			t.Fatalf("%s was removed: %v", path, err)
		}
	}
}
