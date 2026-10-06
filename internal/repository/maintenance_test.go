package repository

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"owngit/internal/state"
)

// maintenanceFixture is a repository whose history exercises everything that
// maintenance must keep: loose objects from small pushes, retained history
// from a force-push and a branch deletion, lightweight and annotated tags, an
// unreachable loose object, an unreachable packed object and a kept pack.
type maintenanceFixture struct {
	manager                *Manager
	remote, work           string
	forced, deleted        string
	unreachableLoose       string
	unreachablePacked      string
	keptObject, keptPackID string
}

func newMaintenanceFixture(t *testing.T) *maintenanceFixture {
	t.Helper()
	manager, remote, work := newTestRepository(t)
	fixture := &maintenanceFixture{manager: manager, remote: remote, work: work}
	for index := range 6 {
		commitFile(t, work, fmt.Sprintf("content %d\n", index), fmt.Sprintf("commit %d", index), "2024-01-01T00:00:00Z")
		runGit(t, work, "push", "origin", "HEAD:refs/heads/main")
	}
	runGit(t, work, "tag", "light")
	runGit(t, work, "-c", "user.name=Tagger", "-c", "user.email=tagger@example.invalid", "tag", "-a", "-m", "annotated", "annotated")
	runGit(t, work, "push", "origin", "refs/tags/light", "refs/tags/annotated")

	// A force-push and a branch deletion leave history only in retention refs.
	fixture.forced = gitOutput(t, work, "rev-parse", "HEAD")
	runGit(t, work, "push", "--force", "origin", "HEAD~2:refs/heads/main")
	runGit(t, work, "checkout", "-b", "topic")
	commitFile(t, work, "topic\n", "topic", "2024-01-02T00:00:00Z")
	fixture.deleted = gitOutput(t, work, "rev-parse", "HEAD")
	runGit(t, work, "push", "origin", "HEAD:refs/heads/topic")
	runGit(t, work, "push", "origin", ":refs/heads/topic")
	assertRef(t, remote, "refs/owngit/retained/heads/"+fixture.forced, fixture.forced)
	assertRef(t, remote, "refs/owngit/retained/heads/"+fixture.deleted, fixture.deleted)

	// Unreachable objects, loose and in a pack, and a pack with a .keep file,
	// like one an import is still publishing.
	fixture.unreachablePacked = gitInputOutput(t, remote, []byte("unreachable packed blob\n"), "hash-object", "-w", "--stdin")
	gitInputOutput(t, remote, []byte(fixture.unreachablePacked+"\n"), "pack-objects", "-q", filepath.Join(remote, "objects", "pack", "pack"))
	fixture.unreachableLoose = gitInputOutput(t, remote, []byte("unreachable loose blob\n"), "hash-object", "-w", "--stdin")
	fixture.keptObject = gitInputOutput(t, remote, []byte("kept pack blob\n"), "hash-object", "-w", "--stdin")
	fixture.keptPackID = gitInputOutput(t, remote, []byte(fixture.keptObject+"\n"), "pack-objects", "-q", filepath.Join(remote, "objects", "pack", "pack"))
	noErr(t, os.WriteFile(fixture.keepFile(), []byte("synthetic keep\n"), 0o600))
	return fixture
}

func (fixture *maintenanceFixture) keepFile() string {
	return filepath.Join(fixture.remote, "objects", "pack", "pack-"+fixture.keptPackID+".keep")
}

// repositoryInventory lists every ref with its OID, HEAD, and every object
// in the object store, reachable or not.
type repositoryInventory struct {
	refs, head string
	objects    []string
}

func inventory(t *testing.T, remote string) repositoryInventory {
	t.Helper()
	objects := strings.Fields(gitOutput(t, "", "--git-dir", remote, "cat-file", "--batch-all-objects", "--batch-check=%(objectname)"))
	slices.Sort(objects)
	objects = slices.Compact(objects)
	return repositoryInventory{
		refs:    gitOutput(t, "", "--git-dir", remote, "for-each-ref", "--format=%(refname) %(objectname)"),
		head:    gitOutput(t, "", "--git-dir", remote, "symbolic-ref", "HEAD"),
		objects: objects,
	}
}

func assertSameInventory(t *testing.T, before, after repositoryInventory) {
	t.Helper()
	if before.refs != after.refs || before.head != after.head {
		t.Fatalf("refs changed:\nbefore %s\n%s\nafter %s\n%s", before.head, before.refs, after.head, after.refs)
	}
	if !slices.Equal(before.objects, after.objects) {
		t.Fatalf("object set changed: before %d objects, after %d", len(before.objects), len(after.objects))
	}
}

// objectCounts returns the loose object and pack counts from count-objects.
func objectCounts(t *testing.T, remote string) (loose, packs int) {
	t.Helper()
	for _, line := range strings.Split(gitOutput(t, "", "--git-dir", remote, "count-objects", "-v"), "\n") {
		name, value, _ := strings.Cut(line, ": ")
		number, _ := strconv.Atoi(value)
		switch name {
		case "count":
			loose = number
		case "packs":
			packs = number
		}
	}
	return loose, packs
}

func looseRefFiles(t *testing.T, remote string) []string {
	t.Helper()
	var files []string
	noErr(t, filepath.WalkDir(filepath.Join(remote, "refs"), func(path string, entry os.DirEntry, err error) error {
		if err == nil && !entry.IsDir() {
			files = append(files, path)
		}
		return err
	}))
	return files
}

func TestMaintenanceKeepsEveryRefAndObject(t *testing.T) {
	fixture := newMaintenanceFixture(t)
	manager, remote := fixture.manager, fixture.remote
	ctx := context.Background()
	schedule := MaintenanceSchedule{}.withDefaults()
	before := inventory(t, remote)
	loose, packs := objectCounts(t, remote)
	if loose < 10 || len(looseRefFiles(t, remote)) == 0 {
		t.Fatalf("fixture has %d loose objects and loose refs %v", loose, looseRefFiles(t, remote))
	}
	generation := manager.Locks.For("sample").Generation()

	steps, err := manager.maintain(ctx, "sample", MaintenanceSmall, schedule, false)
	noErr(t, err, "small maintenance")
	if steps != 3 {
		t.Fatalf("small maintenance ran %d steps", steps)
	}
	assertSameInventory(t, before, inventory(t, remote))
	afterLoose, afterPacks := objectCounts(t, remote)
	// repack -d packs only reachable loose objects and removes loose copies
	// of packed objects, so only the unreachable loose blob stays loose.
	if afterLoose != 1 || afterPacks != packs+1 {
		t.Fatalf("after small: loose %d packs %d (before %d, %d)", afterLoose, afterPacks, loose, packs)
	}
	if files := looseRefFiles(t, remote); len(files) != 0 {
		t.Fatalf("loose refs remain: %v", files)
	}
	if _, err := os.Stat(filepath.Join(remote, "objects", "info", "commit-graphs", "commit-graph-chain")); err != nil {
		t.Fatalf("commit-graph chain: %v", err)
	}
	// Maintenance changed no ref, so cached ref snapshots stay current.
	if got := manager.Locks.For("sample").Generation(); got != generation {
		t.Fatalf("lock generation %d, want %d", got, generation)
	}

	steps, err = manager.maintain(ctx, "sample", MaintenanceFull, schedule, false)
	noErr(t, err, "full maintenance")
	if steps != 3 {
		t.Fatalf("full maintenance ran %d steps", steps)
	}
	assertSameInventory(t, before, inventory(t, remote))
	afterLoose, afterPacks = objectCounts(t, remote)
	// Everything, unreachable objects included, is now in one pack beside
	// the kept pack.
	if afterLoose != 0 || afterPacks != 2 {
		t.Fatalf("after full: loose %d packs %d", afterLoose, afterPacks)
	}
	if _, err := os.Stat(fixture.keepFile()); err != nil {
		t.Fatalf("keep file: %v", err)
	}
	runGit(t, "", "--git-dir", remote, "fsck", "--no-dangling")

	// Retained history is still recoverable, and retention still works on
	// packed refs.
	for _, oid := range []string{fixture.forced, fixture.deleted} {
		request := RestoreRequest{Source: oid, Target: "recovered-" + oid[:8], Mode: RestoreAll}
		preview, err := manager.PreviewRestore(ctx, "sample", request)
		noErr(t, err)
		if !preview.CreatesBranch || !preview.CanApply {
			t.Fatalf("restore preview of %s: %+v", oid, preview)
		}
		request.ExpectedHead = preview.ExpectedHead
		_, err = manager.ApplyRestore(ctx, "sample", request)
		noErr(t, err)
	}
	replaced := gitOutput(t, "", "--git-dir", remote, "rev-parse", "refs/heads/main")
	parent := gitOutput(t, "", "--git-dir", remote, "rev-parse", "refs/heads/main~1")
	runGit(t, fixture.work, "push", "--force", "origin", parent+":refs/heads/main")
	assertRef(t, remote, "refs/owngit/retained/heads/"+replaced, replaced)
}

func TestPreparationSucceedsAfterMaintenance(t *testing.T) {
	fixture := newMaintenanceFixture(t)
	schedule := MaintenanceSchedule{}.withDefaults()
	_, err := fixture.manager.maintain(context.Background(), "sample", MaintenanceFull, schedule, false)
	noErr(t, err)
	probe := newPreparationProbe()
	startPreparationForTest(t, fixture.manager, probe, 5*time.Second)
	waitFor(t, "preparation", func() bool { return !fixture.manager.Preparing("sample") })
	if probe.count("sample") != 1 {
		t.Fatalf("preparation step ran %d times", probe.count("sample"))
	}
	summary, err := fixture.manager.Summary(context.Background(), "sample")
	noErr(t, err)
	if len(summary.Branches) == 0 || len(summary.Tags) != 2 {
		t.Fatalf("summary after maintenance: %+v", summary)
	}
}

// maintenanceLog collects scheduler log lines.
type maintenanceLog struct {
	mu    sync.Mutex
	lines []string
}

func (log *maintenanceLog) logf(format string, arguments ...any) {
	log.mu.Lock()
	defer log.mu.Unlock()
	log.lines = append(log.lines, fmt.Sprintf(format, arguments...))
}

func (log *maintenanceLog) matching(substring string) []string {
	log.mu.Lock()
	defer log.mu.Unlock()
	var matched []string
	for _, line := range log.lines {
		if strings.Contains(line, substring) {
			matched = append(matched, line)
		}
	}
	return matched
}

func startMaintenanceForTest(t *testing.T, manager *Manager, schedule MaintenanceSchedule) *maintenanceLog {
	t.Helper()
	log := &maintenanceLog{}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() {
		cancel()
		stop, done := context.WithTimeout(context.Background(), 10*time.Second)
		defer done()
		noErr(t, manager.StopMaintenance(stop))
	})
	noErr(t, manager.StartMaintenance(ctx, schedule, log.logf))
	return log
}

// shiftedClock returns a clock that runs with real time but starts at the
// given local hour today.
func shiftedClock(hour int) func() time.Time {
	now := time.Now()
	target := time.Date(now.Year(), now.Month(), now.Day(), hour, 0, 0, 0, time.Local)
	offset := target.Sub(now)
	return func() time.Time { return time.Now().Add(offset) }
}

func TestMaintenanceWaitsUntilTheRepositoryIsIdle(t *testing.T) {
	if testing.Short() {
		t.Skip("waits out three idle windows of 200 ms")
	}
	fixture := newMaintenanceFixture(t)
	manager := fixture.manager
	idle := 200 * time.Millisecond
	log := startMaintenanceForTest(t, manager, MaintenanceSchedule{Idle: idle, Retry: 50 * time.Millisecond, Now: shiftedClock(12)})
	time.Sleep(3 * idle)
	if lines := log.matching("maintenance"); len(lines) != 0 {
		t.Fatalf("maintenance ran without a write: %v", lines)
	}

	// Uses arrive every idle/4, so maintenance must wait until they stop. A
	// late wake-up of this goroutine really leaves the repository idle, and
	// maintenance may then start in the gap, so the test checks every start
	// it sees against the use before it instead of assuming when uses end.
	// A run clears the pending flag when it starts, and nothing else does
	// here. A start between two readings of the flag saw a last use no
	// earlier than the use noted before the first reading, and happened no
	// later than the second reading.
	startedAfter := func(use time.Time, where string) {
		t.Helper()
		if gap := time.Since(use); gap < idle {
			t.Fatalf("maintenance started %s: at most %s after a use, want at least %s", where, gap, idle)
		}
	}
	use := time.Now()
	manager.NoteRepositoryWrite("sample")
	pending := true
	for range 10 {
		time.Sleep(idle / 4)
		next := time.Now()
		manager.NoteRepositoryUse("sample")
		still := manager.pendingMaintenance("sample")
		if pending && !still {
			startedAfter(use, "while uses arrived")
		}
		pending, use = still, next
	}
	waitFor(t, "small maintenance", func() bool { return len(log.matching(`"sample" maintenance (small) completed`)) == 1 })
	if pending {
		// The run that completed started after the last reading.
		startedAfter(use, "after the last use")
	}
	if files := looseRefFiles(t, fixture.remote); len(files) != 0 {
		t.Fatalf("loose refs remain: %v", files)
	}
	if manager.pendingMaintenance("sample") {
		t.Fatal("maintenance still pending")
	}

	// A repository in use defers maintenance without a log line. A run the
	// uses above paused has already logged, so count only new lines. A new
	// loose branch is work the last run has not seen, so a run starts and
	// finds the reader.
	head := gitOutput(t, "", "--git-dir", fixture.remote, "rev-parse", "refs/heads/main")
	runGit(t, "", "--git-dir", fixture.remote, "update-ref", "refs/heads/deferred", head)
	logged := len(log.matching("(small)"))
	lock := manager.Locks.For("sample")
	lock.RLock()
	manager.NoteRepositoryWrite("sample")
	time.Sleep(3 * idle)
	if len(log.matching("(small)")) != logged || !manager.pendingMaintenance("sample") {
		lock.RUnlock()
		t.Fatalf("maintenance did not wait for the reader: %v", log.matching("maintenance"))
	}
	lock.RUnlock()
	waitFor(t, "deferred maintenance", func() bool { return len(log.matching("(small) completed")) == 2 })

	// Requests for unknown names are not recorded.
	manager.NoteRepositoryUse("no-such-repository")
	if manager.maintenanceKnown("no-such-repository") {
		t.Fatal("use of an unknown repository created scheduler state")
	}
}

func TestMaintenanceConsolidatesPacksOnlyAtNight(t *testing.T) {
	manager, remote, _ := newTestRepository(t)
	for index := range 22 {
		object := gitInputOutput(t, remote, []byte(fmt.Sprintf("pack %d\n", index)), "hash-object", "-w", "--stdin")
		gitInputOutput(t, remote, []byte(object+"\n"), "pack-objects", "-q", filepath.Join(remote, "objects", "pack", "pack"))
	}
	before := inventory(t, remote)

	day := &atomic.Pointer[func() time.Time]{}
	noonClock, nightClock := shiftedClock(12), shiftedClock(3)
	day.Store(&noonClock)
	clock := func() time.Time { return (*day.Load())() }
	log := startMaintenanceForTest(t, manager, MaintenanceSchedule{Idle: 10 * time.Millisecond, Retry: 10 * time.Millisecond, Now: clock})
	time.Sleep(300 * time.Millisecond)
	if _, packs := objectCounts(t, remote); packs != 22 || len(log.matching("maintenance")) != 0 {
		t.Fatalf("daytime maintenance: packs %d log %v", packs, log.matching("maintenance"))
	}

	day.Store(&nightClock)
	manager.NoteRepositoryWrite("sample") // wakes the scheduler for the new clock
	waitFor(t, "night consolidation", func() bool { return len(log.matching(`"sample" maintenance (full) completed`)) == 1 })
	if loose, packs := objectCounts(t, remote); packs != 1 || loose != 0 {
		t.Fatalf("after night: loose %d packs %d", loose, packs)
	}
	assertSameInventory(t, before, inventory(t, remote))

	// Once per night: a later write gets small maintenance only.
	gitInputOutput(t, remote, []byte("a later write\n"), "hash-object", "-w", "--stdin")
	manager.NoteRepositoryWrite("sample")
	waitFor(t, "small maintenance", func() bool { return len(log.matching("(small) completed")) == 1 })
	time.Sleep(200 * time.Millisecond)
	if lines := log.matching("(full)"); len(lines) != 1 {
		t.Fatalf("full consolidation ran again: %v", lines)
	}
}

func TestMaintenanceRunsOneRepositoryAtATime(t *testing.T) {
	manager, _, _ := newTestRepository(t)
	for _, name := range []string{"second", "third"} {
		_, err := manager.Create(context.Background(), name, "")
		noErr(t, err)
	}
	// Every repository gets a loose object, which is what a push leaves
	// behind and what maintenance packs.
	for _, name := range []string{"sample", "second", "third"} {
		path, err := manager.Path(name)
		noErr(t, err)
		gitInputOutput(t, path, []byte(name+"\n"), "hash-object", "-w", "--stdin")
	}
	var active, peak atomic.Int32
	perRepository := sync.Map{}
	manager.maintenanceHook = func(ctx context.Context, id string, args []string) error {
		counter, _ := perRepository.LoadOrStore(id, &atomic.Int32{})
		if counter.(*atomic.Int32).Add(1) > 1 {
			return errors.New("two maintenance commands on one repository")
		}
		defer counter.(*atomic.Int32).Add(-1)
		now := active.Add(1)
		defer active.Add(-1)
		for {
			old := peak.Load()
			if now <= old || peak.CompareAndSwap(old, now) {
				break
			}
		}
		time.Sleep(5 * time.Millisecond)
		return nil
	}
	log := startMaintenanceForTest(t, manager, MaintenanceSchedule{Idle: time.Millisecond, Retry: time.Millisecond, Now: shiftedClock(12)})
	for range 20 {
		for _, id := range []string{"sample", "second", "third"} {
			manager.NoteRepositoryWrite(id)
		}
		time.Sleep(3 * time.Millisecond)
	}
	waitFor(t, "every repository maintained", func() bool {
		return len(log.matching(`"sample" maintenance (small) completed`)) > 0 &&
			len(log.matching(`"second" maintenance (small) completed`)) > 0 &&
			len(log.matching(`"third" maintenance (small) completed`)) > 0 &&
			!manager.pendingMaintenance("sample") && !manager.pendingMaintenance("second") && !manager.pendingMaintenance("third")
	})
	if failed := log.matching("failed"); len(failed) != 0 || peak.Load() != 1 {
		t.Fatalf("peak concurrency %d, failures %v", peak.Load(), failed)
	}
}

// A push that waits for the repository during one maintenance command runs
// right after that command, before the next one, and the run stops so the
// rest waits until the repository is idle again. Without the check for
// waiters, the maintenance takes the lock again before the blocked push.
func TestWaitingPushRunsBeforeTheNextMaintenanceCommand(t *testing.T) {
	for _, blockAt := range []string{"pack-refs", "repack"} {
		t.Run(blockAt, func(t *testing.T) {
			fixture := newMaintenanceFixture(t)
			manager := fixture.manager
			lock := manager.Locks.For("sample")
			var mu sync.Mutex
			var order []string
			record := func(event string) {
				mu.Lock()
				defer mu.Unlock()
				order = append(order, event)
			}
			started := make(chan struct{})
			release := make(chan struct{})
			var once sync.Once
			manager.maintenanceHook = func(ctx context.Context, id string, args []string) error {
				record(args[0])
				if args[0] == blockAt {
					once.Do(func() { close(started) })
					<-release
				}
				return nil
			}
			type outcome struct {
				steps int
				err   error
			}
			done := make(chan outcome, 1)
			go func() {
				steps, err := manager.maintain(context.Background(), "sample", MaintenanceFull, MaintenanceSchedule{}.withDefaults(), false)
				done <- outcome{steps, err}
			}()
			<-started
			// A push, like the Git HTTP handler, takes the write lock and
			// waits for the running command only. It reports its result
			// after releasing the lock, as the handler releases it before
			// the client sees the end of the response.
			commitFile(t, fixture.work, "during maintenance\n", "during", "2024-02-01T00:00:00Z")
			oid := gitOutput(t, fixture.work, "rev-parse", "HEAD")
			pushed := make(chan error, 1)
			go func() {
				pushed <- func() error {
					lock.Lock()
					defer lock.Unlock()
					record("push")
					output, err := gitCombined(fixture.work, "push", "origin", "HEAD:refs/heads/during")
					if err != nil {
						err = fmt.Errorf("%w: %s", err, output)
					}
					return err
				}()
			}()
			waitFor(t, "the push to wait for the lock", lock.Waiting)
			select {
			case <-pushed:
				t.Fatal("push ran while a maintenance command held the repository")
			default:
			}
			close(release)
			noErr(t, <-pushed, "push")
			result := <-done
			mu.Lock()
			got := slices.Clone(order)
			mu.Unlock()
			commands := []string{"pack-refs", "repack", "commit-graph"}
			ran := slices.Index(commands, blockAt) + 1
			want := append(slices.Clone(commands[:ran]), "push")
			if !slices.Equal(got, want) || result.steps != ran || !errors.Is(result.err, errMaintenanceBusy) {
				t.Fatalf("order %v, steps %d, err %v; want order %v, %d steps and a pause", got, result.steps, result.err, want, ran)
			}
			assertRef(t, fixture.remote, "refs/heads/during", oid)
			steps, err := manager.maintain(context.Background(), "sample", MaintenanceFull, MaintenanceSchedule{}.withDefaults(), false)
			if err != nil || steps != 3 {
				t.Fatalf("maintenance after the push: %d steps, %v", steps, err)
			}
			runGit(t, "", "--git-dir", fixture.remote, "fsck", "--no-dangling")
		})
	}
}

// A request for the repository during a command also stops the run, even
// when it did not need the lock.
func TestRequestDuringMaintenanceStopsTheRun(t *testing.T) {
	manager, _, work := newTestRepository(t)
	commitFile(t, work, "one\n", "one", "2024-01-01T00:00:00Z")
	runGit(t, work, "push", "origin", "HEAD:refs/heads/main")
	manager.NoteRepositoryWrite("sample")
	var ran []string
	manager.maintenanceHook = func(ctx context.Context, id string, args []string) error {
		ran = append(ran, args[0])
		if args[0] == "pack-refs" {
			manager.NoteRepositoryUse("sample")
		}
		return nil
	}
	steps, err := manager.maintain(context.Background(), "sample", MaintenanceSmall, MaintenanceSchedule{}.withDefaults(), false)
	if steps != 1 || !errors.Is(err, errMaintenanceBusy) || !slices.Equal(ran, []string{"pack-refs"}) {
		t.Fatalf("steps %d, err %v, ran %v", steps, err, ran)
	}
}

// A check job reads its source through pinned reads, which refuse while the
// repository is locked and are retried instead of waiting. A refused read
// stops the maintenance before its next step, so the job gets in.
func TestRefusedCheckSourceReadStopsTheRun(t *testing.T) {
	manager, _, work := newTestRepository(t)
	commitFile(t, work, "one\n", "one", "2024-01-01T00:00:00Z")
	runGit(t, work, "push", "origin", "HEAD:refs/heads/main")
	oid := gitOutput(t, work, "rev-parse", "HEAD")
	manager.NoteRepositoryWrite("sample")
	var ran []string
	manager.maintenanceHook = func(ctx context.Context, id string, args []string) error {
		ran = append(ran, args[0])
		if args[0] == "repack" {
			if _, err := manager.PinRepository(ctx, "sample", oid, oid); !errors.Is(err, ErrPinnedRepositoryBusy) {
				return fmt.Errorf("pinned read during repack: %v", err)
			}
		}
		return nil
	}
	steps, err := manager.maintain(context.Background(), "sample", MaintenanceSmall, MaintenanceSchedule{}.withDefaults(), false)
	if steps != 2 || !errors.Is(err, errMaintenanceBusy) || !slices.Equal(ran, []string{"pack-refs", "repack"}) {
		t.Fatalf("steps %d, err %v, ran %v", steps, err, ran)
	}
	if _, err := manager.PinRepository(context.Background(), "sample", oid, oid); err != nil {
		t.Fatalf("pinned read after the pause: %v", err)
	}
}

// Maintenance changes no ref, so a cached ref snapshot stays current: the
// dashboard shows the repository without waiting while a command runs. A
// ref that changed during pack-refs invalidates the snapshot.
func TestMaintenanceKeepsCachedRefSnapshots(t *testing.T) {
	fixture := newMaintenanceFixture(t)
	manager := fixture.manager
	lock := manager.Locks.For("sample")
	cached, err := manager.RefSnapshot(context.Background(), "sample")
	noErr(t, err)
	generation := lock.Generation()
	started := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	manager.maintenanceHook = func(ctx context.Context, id string, args []string) error {
		if args[0] == "repack" {
			once.Do(func() { close(started) })
			<-release
		}
		return nil
	}
	done := make(chan error, 1)
	go func() {
		_, err := manager.maintain(context.Background(), "sample", MaintenanceSmall, MaintenanceSchedule{}.withDefaults(), false)
		done <- err
	}()
	<-started
	// The snapshot must come from the cache: a read that waits for the lock
	// shows up as a waiter while repack holds it. The bound is a hang guard.
	type snapshotResult struct {
		snapshot RefSnapshot
		err      error
	}
	read := make(chan snapshotResult, 1)
	go func() {
		snapshot, err := manager.RefSnapshot(context.Background(), "sample")
		read <- snapshotResult{snapshot, err}
	}()
	var during snapshotResult
	for bound := time.Now().Add(30 * time.Second); ; time.Sleep(time.Millisecond) {
		select {
		case during = <-read:
		default:
			if waiting := lock.Waiting(); waiting || time.Now().After(bound) {
				close(release)
				<-done
				t.Fatalf("snapshot during repack did not return from the cache (waiting for the lock: %v)", waiting)
			}
			continue
		}
		break
	}
	noErr(t, during.err, "ref snapshot during repack")
	if during.snapshot.Summary.DefaultOID != cached.Summary.DefaultOID || lock.Waiting() {
		t.Fatalf("snapshot during repack %s, want %s without waiting", during.snapshot.Summary.DefaultOID, cached.Summary.DefaultOID)
	}
	close(release)
	noErr(t, <-done, "maintenance")
	if lock.Generation() != generation {
		t.Fatalf("maintenance advanced the generation from %d to %d", generation, lock.Generation())
	}
	if len(looseRefFiles(t, fixture.remote)) != 0 {
		t.Fatal("pack-refs did not run")
	}

	// A ref written while pack-refs holds the lock is not hidden.
	manager.maintenanceHook = func(ctx context.Context, id string, args []string) error {
		if args[0] == "pack-refs" {
			runGit(t, "", "--git-dir", fixture.remote, "update-ref", "refs/heads/moved", cached.Summary.DefaultOID)
		}
		return nil
	}
	_, err = manager.maintain(context.Background(), "sample", MaintenanceSmall, MaintenanceSchedule{}.withDefaults(), false)
	noErr(t, err)
	if lock.Generation() == generation {
		t.Fatal("a ref change during pack-refs kept the cached snapshot")
	}
	after, err := manager.RefSnapshot(context.Background(), "sample")
	noErr(t, err)
	if !slices.ContainsFunc(after.Summary.Branches, func(branch Ref) bool { return branch.Name == "moved" }) {
		t.Fatalf("snapshot after the change misses the new branch: %+v", after.Summary.Branches)
	}
}

func TestDeletionStopsMaintenanceAndForgetsIt(t *testing.T) {
	fixture := newMaintenanceFixture(t)
	manager := fixture.manager
	started := make(chan struct{})
	var once sync.Once
	manager.maintenanceHook = func(ctx context.Context, id string, args []string) error {
		if args[0] == "repack" {
			once.Do(func() { close(started) })
			<-ctx.Done()
			return ctx.Err()
		}
		return nil
	}
	log := startMaintenanceForTest(t, manager, MaintenanceSchedule{Idle: time.Millisecond, Now: shiftedClock(12)})
	manager.NoteRepositoryWrite("sample")
	<-started
	begun := time.Now()
	_, err := manager.Delete(context.Background(), "sample", DeleteFiles)
	noErr(t, err, "delete during maintenance")
	if elapsed := time.Since(begun); elapsed >= deleteLockWait {
		t.Fatalf("deletion waited %s for maintenance", elapsed)
	}
	waitFor(t, "stopped log", func() bool { return len(log.matching(`"sample" maintenance (small) stopped`)) == 1 })
	if manager.maintenanceKnown("sample") {
		t.Fatal("deleted repository keeps scheduled maintenance")
	}

	// A new repository with the same name starts with no maintenance state
	// and is maintained after its first write.
	manager.maintenanceHook = nil
	_, err = manager.Create(context.Background(), "sample", "")
	noErr(t, err)
	if manager.maintenanceKnown("sample") {
		t.Fatal("new repository inherited maintenance state")
	}
	path, err := manager.Path("sample")
	noErr(t, err)
	gitInputOutput(t, path, []byte("first write\n"), "hash-object", "-w", "--stdin")
	manager.NoteRepositoryWrite("sample")
	waitFor(t, "maintenance of the new repository", func() bool { return len(log.matching(`"sample" maintenance (small) completed`)) == 1 })
}

func TestMaintenanceSkipsRepositoriesBeingPrepared(t *testing.T) {
	fixture := newMaintenanceFixture(t)
	manager := fixture.manager
	probe := newPreparationProbe()
	hold := make(chan struct{})
	probe.hold["sample"] = hold
	startPreparationForTest(t, manager, probe, 10*time.Millisecond)
	log := startMaintenanceForTest(t, manager, MaintenanceSchedule{Idle: time.Millisecond, Retry: time.Millisecond, Now: shiftedClock(12)})
	manager.NoteRepositoryWrite("sample")
	time.Sleep(200 * time.Millisecond)
	if lines := log.matching("maintenance"); len(lines) != 0 || len(looseRefFiles(t, fixture.remote)) == 0 {
		t.Fatalf("maintenance ran during preparation: %v", lines)
	}
	close(hold)
	waitFor(t, "maintenance after preparation", func() bool { return len(log.matching(`"sample" maintenance (small) completed`)) == 1 })
}

// An unchanged preparation reports readiness, not a write, so a restart of a
// maintained repository schedules no maintenance; a branch an offline Git
// command left behind is work, and that preparation reports it.
func TestPreparationSchedulesMaintenanceOnlyForWork(t *testing.T) {
	fixture := newMaintenanceFixture(t)
	manager := fixture.manager
	// A maintained repository has nothing to pack.
	_, err := manager.maintain(context.Background(), "sample", MaintenanceFull, MaintenanceSchedule{}.withDefaults(), false)
	noErr(t, err)
	if loose, _ := objectCounts(t, fixture.remote); loose != 0 || len(looseRefFiles(t, fixture.remote)) != 0 {
		t.Fatalf("the maintained repository still has loose objects %d or refs %v", loose, looseRefFiles(t, fixture.remote))
	}
	var ready, changes, commands atomic.Int32
	manager.OnReady = func(string) { ready.Add(1) }
	manager.OnChange = func(id string) {
		changes.Add(1)
		manager.NoteRepositoryWrite(id)
	}
	manager.maintenanceHook = func(context.Context, string, []string) error { commands.Add(1); return nil }
	log := startMaintenanceForTest(t, manager, MaintenanceSchedule{Idle: 10 * time.Millisecond, Retry: 10 * time.Millisecond, Now: shiftedClock(12)})

	probe := newPreparationProbe()
	startPreparationForTest(t, manager, probe, 5*time.Second)
	waitFor(t, "the first preparation", func() bool { return ready.Load() == 1 })
	if becameTrueWithin(time.Second, func() bool { return commands.Load() > 0 }) {
		t.Fatalf("an unchanged restart scheduled maintenance: %v", log.matching("maintenance"))
	}
	if manager.pendingMaintenance("sample") {
		t.Fatal("an unchanged restart left the repository pending")
	}

	// A Git command the owner ran while OwnGit was stopped leaves a loose
	// branch behind; preparation finds it, reports the write once (not the
	// readiness as well) and the repository is maintained.
	head := gitOutput(t, "", "--git-dir", fixture.remote, "rev-parse", "refs/heads/main")
	runGit(t, "", "--git-dir", fixture.remote, "update-ref", "refs/heads/offline", head)
	manager.PrepareRegistered("sample")
	waitFor(t, "the write after the offline change", func() bool { return changes.Load() == 1 })
	if ready.Load() != 1 {
		t.Fatalf("preparation reported readiness %d times, want only the one without work", ready.Load())
	}
	waitFor(t, "maintenance of the offline write", func() bool { return len(log.matching(`"sample" maintenance (small) completed`)) == 1 })
}

// A write notification with nothing the last completed run has not considered
// behind it, as an unchanged import or a push that sent nothing reports, runs
// no maintenance. The loose objects a completed run keeps because no ref
// reaches them, such as the ones a conflicting merge calculation or an
// unapplied restore preview writes, are not that work again.
func TestWriteNotificationWithoutWorkRunsNoMaintenance(t *testing.T) {
	fixture := newMaintenanceFixture(t)
	manager := fixture.manager
	// A loose object nothing reaches, written before the run that considers it.
	// Its time is a minute old, as a real write that waited for the idle time
	// before the run has.
	gitInputOutput(t, fixture.remote, []byte("unreachable\n"), "hash-object", "-w", "--stdin")
	ageLooseFiles(t, fixture.remote)
	var commands atomic.Int32
	manager.maintenanceHook = func(context.Context, string, []string) error { commands.Add(1); return nil }
	log := startMaintenanceForTest(t, manager, MaintenanceSchedule{Idle: 10 * time.Millisecond, Retry: 10 * time.Millisecond, Now: shiftedClock(12)})
	// The fixture's loose refs are work, so the first notification maintains
	// the repository and records what it considered. Its repack packs them
	// and keeps the unreachable object loose.
	manager.NoteRepositoryWrite("sample")
	waitFor(t, "the run of the loose refs", func() bool { return len(log.matching(`"sample" maintenance (small) completed`)) == 1 })
	if loose, _ := objectCounts(t, fixture.remote); loose == 0 {
		t.Fatal("the run packed the unreachable object, so this test would prove nothing")
	}
	// Three notifications that changed nothing find nothing to pack.
	commands.Store(0)
	for round := range 3 {
		manager.NoteRepositoryWrite("sample")
		if becameTrueWithin(200*time.Millisecond, func() bool { return commands.Load() > 0 }) {
			t.Fatalf("notification %d without work ran maintenance: %v", round+1, log.matching("maintenance"))
		}
	}
	if manager.pendingMaintenance("sample") {
		t.Fatal("a write without work stays pending")
	}
	if lines := log.matching("maintenance"); len(lines) != 1 {
		t.Fatalf("a notification without work was logged: %v", lines[1:])
	}
}

// A run that stopped before its last step retries its own sequence even when
// the packing steps left no loose ref or object behind: a failure after them
// does not leave the sequence half done.
func TestMaintenanceRetriesTheRestOfASequenceWithoutLooseWork(t *testing.T) {
	manager, remote, work := newTestRepository(t)
	commitFile(t, work, "one\n", "one", "2024-01-01T00:00:00Z")
	runGit(t, work, "push", "origin", "HEAD:refs/heads/main")
	var attempts atomic.Int32
	manager.maintenanceHook = func(ctx context.Context, id string, args []string) error {
		if args[0] == "commit-graph" && attempts.Add(1) == 1 {
			return errors.New("synthetic commit-graph failure")
		}
		return nil
	}
	log := startMaintenanceForTest(t, manager, MaintenanceSchedule{
		Idle: time.Millisecond, Retry: time.Millisecond, FailureRetry: 500 * time.Millisecond, Now: shiftedClock(12),
	})
	manager.NoteRepositoryWrite("sample")
	waitFor(t, "the failure", func() bool { return len(log.matching("synthetic commit-graph failure")) > 0 })
	if loose, packs := objectCounts(t, remote); loose != 0 || len(looseRefFiles(t, remote)) != 0 {
		t.Fatalf("the packing steps left work behind: %d loose objects, %d packs, refs %v", loose, packs, looseRefFiles(t, remote))
	}
	waitFor(t, "the retry", func() bool { return len(log.matching(`"sample" maintenance (small) completed`)) == 1 })
}

// ageLooseFiles gives every loose ref and object of a repository a time a
// minute old, as a write that waited for the idle time before a run has.
func ageLooseFiles(t *testing.T, remote string) {
	t.Helper()
	old := time.Now().Add(-time.Minute)
	aged := 0
	noErr(t, filepath.WalkDir(remote, func(path string, entry os.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		relative, err := filepath.Rel(remote, path)
		if err != nil {
			return err
		}
		// Rel separates with a backslash on Windows, so compare the forward
		// slashes Git uses.
		relative = filepath.ToSlash(relative)
		if !strings.HasPrefix(relative, "refs/") && !strings.HasPrefix(relative, "objects/") {
			return nil
		}
		if strings.HasPrefix(relative, "objects/pack/") || strings.HasPrefix(relative, "objects/info/") {
			return nil
		}
		aged++
		return os.Chtimes(path, old, old)
	}))
	if aged == 0 {
		t.Fatal("no loose ref or object was aged, so this test would prove nothing")
	}
}

// A record from a clock that ran ahead, or from a repository copied from a
// computer whose clock was ahead, would hide every write until that time, so
// it is no record: the repository reports work.
func TestMaintenanceWorkWithAFutureRecordReportsWork(t *testing.T) {
	fixture := newMaintenanceFixture(t)
	manager := fixture.manager
	if _, err := manager.maintain(context.Background(), "sample", MaintenanceFull, MaintenanceSchedule{}.withDefaults(), false); err != nil {
		t.Fatalf("maintenance: %v", err)
	}
	ahead := time.Now().Add(24*time.Hour).UTC().Format(time.RFC3339Nano) + "\n"
	noErr(t, os.WriteFile(maintenanceRecordPath(fixture.remote), []byte(ahead), 0o644))
	head := gitOutput(t, "", "--git-dir", fixture.remote, "rev-parse", "refs/heads/main")
	runGit(t, "", "--git-dir", fixture.remote, "update-ref", "refs/heads/after", head)
	work, err := manager.maintenanceWork(context.Background(), "sample")
	noErr(t, err)
	if !work {
		t.Fatal("a record a day ahead hid a new loose branch")
	}
}

// The record is stored a little before the run it records, so a file system
// with whole-second times cannot report a write made just after that run as
// older than the record and so hide it.
func TestMaintenanceRecordCoversCoarseFileTimes(t *testing.T) {
	fixture := newMaintenanceFixture(t)
	manager := fixture.manager
	if _, err := manager.maintain(context.Background(), "sample", MaintenanceFull, MaintenanceSchedule{}.withDefaults(), false); err != nil {
		t.Fatalf("maintenance: %v", err)
	}
	recorded := lastMaintenance(fixture.remote)
	if recorded.IsZero() {
		t.Fatal("the completed run wrote no record")
	}
	// A file whose time is the run's start floored to a whole second, which is
	// what a coarse file system reports for a write made just after that start.
	head := gitOutput(t, "", "--git-dir", fixture.remote, "rev-parse", "refs/heads/main")
	runGit(t, "", "--git-dir", fixture.remote, "update-ref", "refs/heads/coarse", head)
	coarse := recorded.Add(maintenanceRecordMargin)
	noErr(t, os.Chtimes(filepath.Join(fixture.remote, "refs", "heads", "coarse"), coarse, coarse))
	work, err := manager.maintenanceWork(context.Background(), "sample")
	noErr(t, err)
	if !work {
		t.Fatal("a write at the run's start, floored to a whole second, is not work")
	}
}

func (m *Manager) pendingMaintenance(id string) bool {
	s := &m.maintenance
	s.mu.Lock()
	defer s.mu.Unlock()
	entry := s.entries[id]
	return entry != nil && entry.pending
}

func (m *Manager) maintenanceRunning(id string) bool {
	s := &m.maintenance
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.running != nil && s.running.id == id
}

func (m *Manager) maintenanceKnown(id string) bool {
	s := &m.maintenance
	s.mu.Lock()
	defer s.mu.Unlock()
	_, known := s.entries[id]
	return known
}

// A window follows the local wall clock of the computer that runs OwnGit.
// Every answer is positive and lands inside a window, so the scheduler never
// spins or waits past a night. A start hour that does not exist on a
// spring-forward night begins at the first instant after it, so a 2-5 window
// runs from 03:00 in New York and a 0-3 window from 01:00 when Santiago or
// Havana change the clock at midnight; a fall-back night starts once, at the
// single 02:00; and a window that passes midnight belongs to the date it
// started.
func TestMaintenanceNightWindow(t *testing.T) {
	newYork, err := time.LoadLocation("America/New_York")
	noErr(t, err)
	berlin, err := time.LoadLocation("Europe/Berlin")
	noErr(t, err)
	santiago, err := time.LoadLocation("America/Santiago")
	noErr(t, err)
	havana, err := time.LoadLocation("America/Havana")
	noErr(t, err)
	nuuk, err := time.LoadLocation("America/Nuuk")
	noErr(t, err)
	window := func(start, end int) MaintenanceSchedule {
		return MaintenanceSchedule{}.following(state.Maintenance{WindowStart: start, WindowEnd: end})
	}
	at := func(location *time.Location, year int, month time.Month, day, hour, minute int) time.Time {
		return time.Date(year, month, day, hour, minute, 0, 0, location)
	}
	// The repeated hour of a fall-back night is written in UTC, because the
	// local time does not name one instant.
	utcIn := func(location *time.Location, year int, month time.Month, day, hour, minute int) time.Time {
		return time.Date(year, month, day, hour, minute, 0, 0, time.UTC).In(location)
	}
	for _, test := range []struct {
		what     string
		schedule MaintenanceSchedule
		now      time.Time
		night    string
		until    time.Duration
	}{
		// The defaults from Settings, 03:00 until 05:00.
		{"before the default window", MaintenanceSchedule{}.withDefaults(), at(time.UTC, 2026, 9, 24, 2, 0), "", time.Hour},
		{"at the default start", MaintenanceSchedule{}.withDefaults(), at(time.UTC, 2026, 9, 24, 3, 0), "2026-09-24", 24 * time.Hour},
		{"near the default end", MaintenanceSchedule{}.withDefaults(), at(time.UTC, 2026, 9, 24, 4, 59), "2026-09-24", 22*time.Hour + time.Minute},
		{"at the default end", MaintenanceSchedule{}.withDefaults(), at(time.UTC, 2026, 9, 24, 5, 0), "", 22 * time.Hour},
		{"after the default window", MaintenanceSchedule{}.withDefaults(), at(time.UTC, 2026, 9, 24, 23, 30), "", 3*time.Hour + 30*time.Minute},
		// A window that passes midnight belongs to the date it started.
		{"before a window past midnight", window(22, 6), at(time.UTC, 2026, 9, 24, 21, 0), "", time.Hour},
		{"at a window past midnight", window(22, 6), at(time.UTC, 2026, 9, 24, 22, 0), "2026-09-24", 24 * time.Hour},
		{"late in a window past midnight", window(22, 6), at(time.UTC, 2026, 9, 24, 23, 0), "2026-09-24", 23 * time.Hour},
		{"after midnight in the window", window(22, 6), at(time.UTC, 2026, 9, 25, 0, 0), "2026-09-24", 22 * time.Hour},
		{"near the end of a window past midnight", window(22, 6), at(time.UTC, 2026, 9, 25, 5, 0), "2026-09-24", 17 * time.Hour},
		{"at the end of a window past midnight", window(22, 6), at(time.UTC, 2026, 9, 25, 6, 0), "", 16 * time.Hour},
		// A window that starts at midnight, in a zone without a clock change.
		{"before a midnight window", window(0, 3), at(time.UTC, 2026, 9, 24, 23, 30), "", 30 * time.Minute},
		{"inside a midnight window", window(0, 3), at(time.UTC, 2026, 9, 25, 0, 30), "2026-09-25", 23*time.Hour + 30*time.Minute},
		// New York changes the clock at 02:00, in March and in November.
		{"before the New York change", window(2, 5), at(newYork, 2026, 3, 8, 0, 30), "", 90 * time.Minute},
		{"during the missing New York hour", window(2, 5), at(newYork, 2026, 3, 8, 1, 30), "", 30 * time.Minute},
		{"at the real New York start", window(2, 5), at(newYork, 2026, 3, 8, 3, 0), "2026-03-08", 23 * time.Hour},
		{"inside the New York window", window(2, 5), at(newYork, 2026, 3, 8, 4, 59), "2026-03-08", 21*time.Hour + time.Minute},
		{"after the New York window", window(2, 5), at(newYork, 2026, 3, 8, 5, 0), "", 21 * time.Hour},
		{"before the New York fall change", window(2, 5), at(newYork, 2026, 11, 1, 0, 30), "", 2*time.Hour + 30*time.Minute},
		{"first pass of the repeated hour", window(2, 5), utcIn(newYork, 2026, 11, 1, 5, 30), "", 90 * time.Minute},
		{"second pass of the repeated hour", window(2, 5), utcIn(newYork, 2026, 11, 1, 6, 30), "", 30 * time.Minute},
		{"at the single fall start", window(2, 5), at(newYork, 2026, 11, 1, 2, 0), "2026-11-01", 24 * time.Hour},
		// Berlin, and the default window and a window past midnight on a
		// spring-forward night.
		{"before the Berlin change", window(2, 5), at(berlin, 2026, 3, 29, 1, 30), "", 30 * time.Minute},
		{"at the real Berlin start", window(2, 5), at(berlin, 2026, 3, 29, 3, 0), "2026-03-29", 23 * time.Hour},
		{"the default window before its change", window(3, 5), at(newYork, 2026, 3, 8, 1, 30), "", 30 * time.Minute},
		{"a window past midnight before its change", window(22, 6), at(newYork, 2026, 3, 8, 1, 30), "2026-03-07", 19*time.Hour + 30*time.Minute},
		// Santiago and Havana change the clock at midnight, so the start hour
		// 0 does not exist on those dates and the window starts at 01:00.
		{"before the Santiago midnight change", window(0, 3), at(santiago, 2026, 9, 5, 23, 30), "", 30 * time.Minute},
		{"inside the Santiago window", window(0, 3), at(santiago, 2026, 9, 5, 0, 30), "2026-09-05", 23*time.Hour + 30*time.Minute},
		{"after the Santiago midnight change", window(0, 3), at(santiago, 2026, 9, 6, 1, 30), "2026-09-06", 22*time.Hour + 30*time.Minute},
		{"before the Havana midnight change", window(0, 3), at(havana, 2026, 3, 7, 23, 30), "", 30 * time.Minute},
		{"after the Havana midnight change", window(0, 3), at(havana, 2026, 3, 8, 1, 30), "2026-03-08", 22*time.Hour + 30*time.Minute},
		// Nuuk changes the clock at 23:00 on the last Saturday of March, so
		// that date has no start hour 23 and the window begins at the first
		// instant after the gap, on Sunday.
		{"before the missing Nuuk late hour", window(23, 2), at(nuuk, 2026, 3, 28, 12, 0), "", 11 * time.Hour},
		{"just before the missing Nuuk late hour", window(23, 2), at(nuuk, 2026, 3, 28, 22, 30), "", 30 * time.Minute},
		{"inside the Nuuk window", window(23, 2), at(nuuk, 2026, 3, 29, 0, 30), "2026-03-28", 22*time.Hour + 30*time.Minute},
	} {
		night, until := test.schedule.nightOf(test.now), test.schedule.untilNight(test.now)
		if night != test.night || until != test.until {
			t.Errorf("%s: night %q until %s, want %q and %s", test.what, night, until, test.night, test.until)
		}
		if until <= 0 || test.schedule.nightOf(test.now.Add(until)) == "" {
			t.Errorf("%s: until %s lands outside every window", test.what, until)
		}
	}

	// The repeated hour belongs to one night: both passes name the same date,
	// so a repository is maintained once.
	wide := window(1, 4)
	for _, instant := range []time.Time{utcIn(newYork, 2026, 11, 1, 5, 30), utcIn(newYork, 2026, 11, 1, 6, 30)} {
		if night := wide.nightOf(instant); night != "2026-11-01" {
			t.Errorf("%s: night %q, want 2026-11-01", instant, night)
		}
	}
}

// maintenanceSweepZones are the zones whose clock changes are unusual, and so
// the ones a wait computation must survive: a spring-forward gap at midnight
// (Santiago, Havana, Cairo, Beirut; Asuncion had one in earlier years), a change of half an hour (Lord
// Howe), and a start hour that a change removes from the last day it can
// start on (Nuuk, Scoresbysund, 23:00 on the last Saturday of March). Go
// loads them on every platform, from the zone database of the host or from
// its own copy, and the test fails rather than skips when one does not load.
var maintenanceSweepZones = []string{
	"America/New_York",
	"Europe/Berlin",
	"Australia/Sydney",
	"Australia/Lord_Howe",
	"America/Santiago",
	"America/Havana",
	"America/Asuncion",
	"Africa/Cairo",
	"Asia/Beirut",
	"America/Nuuk",
	"America/Scoresbysund",
}

// windowSweep is what the sweep found: how many zones and clock-changing
// dates it walked, and every wait that did not behave.
type windowSweep struct {
	zones    int
	changes  int
	problems []string
}

// TestMaintenanceNightWindowEndsInEveryZone walks the clock-changing dates of
// 2026 in the zones whose changes are unusual, for every start hour: the walk
// of windowStart stays within its bound, every wait is positive, and every
// wait lands inside a window. The scheduler computes that wait under its own
// mutex, so an answer that never comes blocks every request.
func TestMaintenanceNightWindowEndsInEveryZone(t *testing.T) {
	locations := make([]*time.Location, 0, len(maintenanceSweepZones))
	for _, name := range maintenanceSweepZones {
		location, err := time.LoadLocation(name)
		if err != nil {
			t.Fatalf("the time zone %s does not load: %v", name, err)
		}
		locations = append(locations, location)
	}
	// The sweep runs in its own goroutine, because a walk that does not end
	// cannot be stopped, and it reports through its result rather than through
	// the test, so a call after the deadline cannot panic.
	done := make(chan windowSweep, 1)
	go func() { done <- sweepClockChangeWindows(locations) }()
	select {
	case sweep := <-done:
		t.Logf("%d clock-changing dates in %d zones", sweep.changes, sweep.zones)
		for _, problem := range sweep.problems {
			t.Error(problem)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the wait computation does not end: the sweep over the clock-changing dates did not finish")
	}
}

// sweepClockChangeWindows checks every start hour on every clock-changing date
// of 2026 in the given zones and returns what it found.
func sweepClockChangeWindows(locations []*time.Location) windowSweep {
	sweep := windowSweep{zones: len(locations)}
	for _, location := range locations {
		for day := time.Date(2026, 1, 1, 0, 0, 0, 0, location); day.Year() == 2026; day = day.AddDate(0, 0, 1) {
			_, offset := day.Zone()
			_, next := day.AddDate(0, 0, 1).Zone()
			if offset == next {
				continue
			}
			sweep.changes++
			for hour := range 24 {
				schedule := MaintenanceSchedule{NightStartHour: hour, NightEndHour: (hour + 3) % 24}
				naive := time.Date(day.Year(), day.Month(), day.Day(), hour, 0, 0, 0, location)
				start := schedule.windowStart(day)
				if start.Before(naive) || start.Sub(naive) > maintenanceWindowSteps*time.Hour {
					sweep.problems = append(sweep.problems, fmt.Sprintf("%s window %d-%d on %s: start %s is not the first instant of the hour", location, schedule.NightStartHour, schedule.NightEndHour, day.Format(time.DateOnly), start.Format(time.RFC3339)))
				}
				for _, now := range []time.Time{day.AddDate(0, 0, -1), day, day.Add(12 * time.Hour), day.Add(22 * time.Hour)} {
					until := schedule.untilNight(now)
					if until <= 0 || schedule.nightOf(now.Add(until)) == "" {
						sweep.problems = append(sweep.problems, fmt.Sprintf("%s window %d-%d at %s: wait %s lands at %s", location, schedule.NightStartHour, schedule.NightEndHour, now.Format(time.RFC3339), until, now.Add(until).Format(time.RFC3339)))
					}
				}
			}
		}
	}
	return sweep
}

// Maintenance follows the owner's saved choices before each job: off, a
// repository written to is not maintained; turned on, it is, and the
// nightly consolidation check runs only inside the chosen window. Choices
// that cannot be read pause maintenance and are logged once.
func TestMaintenanceFollowsTheSavedChoices(t *testing.T) {
	fixture := newMaintenanceFixture(t)
	manager := fixture.manager
	ctx := context.Background()
	off := state.DefaultMaintenance
	off.Enabled = false
	noErr(t, manager.Store.SavePolicies(ctx, state.PolicyChange{Maintenance: &off}))
	log := startMaintenanceForTest(t, manager, MaintenanceSchedule{Idle: time.Millisecond, Retry: time.Millisecond, Now: shiftedClock(12)})
	manager.NoteRepositoryWrite("sample")
	waitFor(t, "the off log", func() bool { return len(log.matching("turned off in Settings")) == 1 })
	time.Sleep(200 * time.Millisecond)
	if lines := log.matching(`"sample" maintenance`); len(lines) != 0 {
		t.Fatalf("maintenance ran while off: %v", lines)
	}

	noErr(t, manager.Store.Exec(ctx, `UPDATE metadata SET value='{"window_start_hour":25}' WHERE key='maintenance'`))
	manager.WakeMaintenance()
	waitFor(t, "the unreadable log", func() bool { return len(log.matching("maintenance paused: the saved maintenance")) == 1 })

	// Noon is outside a window of 13 to 15: only the small maintenance of
	// the write runs.
	later := state.DefaultMaintenance
	later.WindowStart, later.WindowEnd = 13, 15
	noErr(t, manager.Store.SavePolicies(ctx, state.PolicyChange{Maintenance: &later}))
	manager.WakeMaintenance()
	waitFor(t, "maintenance once on", func() bool { return len(log.matching(`"sample" maintenance (small) completed`)) == 1 })
	if manager.maintenanceNight("sample") != "" {
		t.Fatal("the nightly check ran outside the chosen window")
	}
	// A window of 11 to 13 holds noon, so the nightly check runs.
	around := state.DefaultMaintenance
	around.WindowStart, around.WindowEnd = 11, 13
	noErr(t, manager.Store.SavePolicies(ctx, state.PolicyChange{Maintenance: &around}))
	manager.WakeMaintenance()
	waitFor(t, "the nightly check inside the chosen window", func() bool { return manager.maintenanceNight("sample") != "" })
}

func (m *Manager) maintenanceNight(id string) string {
	s := &m.maintenance
	s.mu.Lock()
	defer s.mu.Unlock()
	if entry := s.entries[id]; entry != nil {
		return entry.night
	}
	return ""
}
