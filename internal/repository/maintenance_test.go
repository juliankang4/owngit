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

	steps, err := manager.maintain(ctx, "sample", MaintenanceSmall, schedule)
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

	steps, err = manager.maintain(ctx, "sample", MaintenanceFull, schedule)
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
	_, err := fixture.manager.maintain(context.Background(), "sample", MaintenanceFull, schedule)
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
	// uses above paused has already logged, so count only new lines.
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
				steps, err := manager.maintain(context.Background(), "sample", MaintenanceFull, MaintenanceSchedule{}.withDefaults())
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
			steps, err := manager.maintain(context.Background(), "sample", MaintenanceFull, MaintenanceSchedule{}.withDefaults())
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
	steps, err := manager.maintain(context.Background(), "sample", MaintenanceSmall, MaintenanceSchedule{}.withDefaults())
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
	steps, err := manager.maintain(context.Background(), "sample", MaintenanceSmall, MaintenanceSchedule{}.withDefaults())
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
		_, err := manager.maintain(context.Background(), "sample", MaintenanceSmall, MaintenanceSchedule{}.withDefaults())
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
	_, err = manager.maintain(context.Background(), "sample", MaintenanceSmall, MaintenanceSchedule{}.withDefaults())
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

func (m *Manager) pendingMaintenance(id string) bool {
	s := &m.maintenance
	s.mu.Lock()
	defer s.mu.Unlock()
	entry := s.entries[id]
	return entry != nil && entry.pending
}

func (m *Manager) maintenanceKnown(id string) bool {
	s := &m.maintenance
	s.mu.Lock()
	defer s.mu.Unlock()
	_, known := s.entries[id]
	return known
}

func TestMaintenanceNightWindow(t *testing.T) {
	schedule := MaintenanceSchedule{}.withDefaults()
	at := func(hour, minute int) time.Time { return time.Date(2026, 9, 24, hour, minute, 0, 0, time.UTC) }
	for _, test := range []struct {
		now   time.Time
		night string
		until time.Duration
	}{
		{at(2, 0), "", time.Hour},
		{at(3, 0), "2026-09-24", 24 * time.Hour},
		{at(4, 59), "2026-09-24", 22*time.Hour + time.Minute},
		{at(5, 0), "", 22 * time.Hour},
		{at(23, 30), "", 3*time.Hour + 30*time.Minute},
	} {
		if night, until := schedule.nightOf(test.now), schedule.untilNight(test.now); night != test.night || until != test.until {
			t.Errorf("%s: night %q until %s, want %q and %s", test.now.Format(time.TimeOnly), night, until, test.night, test.until)
		}
	}
}
