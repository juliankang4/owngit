package repository

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// preparationProbe is a PreparationStep whose result per repository the test
// controls, counting attempts and recording the log.
type preparationProbe struct {
	mu       sync.Mutex
	fail     map[string]bool
	hold     map[string]chan struct{}
	attempts map[string]*atomic.Int32
	logs     []string
}

func newPreparationProbe() *preparationProbe {
	return &preparationProbe{fail: map[string]bool{}, hold: map[string]chan struct{}{}, attempts: map[string]*atomic.Int32{}}
}

func (probe *preparationProbe) step(ctx context.Context, id, _ string) error {
	probe.mu.Lock()
	counter := probe.attempts[id]
	if counter == nil {
		counter = &atomic.Int32{}
		probe.attempts[id] = counter
	}
	fail, hold := probe.fail[id], probe.hold[id]
	probe.mu.Unlock()
	counter.Add(1)
	if hold != nil {
		select {
		case <-hold:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if fail {
		return fmt.Errorf("synthetic preparation failure for %s", id)
	}
	return nil
}

func (probe *preparationProbe) setFail(id string, fail bool) {
	probe.mu.Lock()
	defer probe.mu.Unlock()
	probe.fail[id] = fail
}

func (probe *preparationProbe) count(id string) int32 {
	probe.mu.Lock()
	defer probe.mu.Unlock()
	if counter := probe.attempts[id]; counter != nil {
		return counter.Load()
	}
	return 0
}

func (probe *preparationProbe) logf(format string, arguments ...any) {
	probe.mu.Lock()
	defer probe.mu.Unlock()
	probe.logs = append(probe.logs, fmt.Sprintf(format, arguments...))
}

func (probe *preparationProbe) logged() []string {
	probe.mu.Lock()
	defer probe.mu.Unlock()
	return append([]string(nil), probe.logs...)
}

func startPreparationForTest(t *testing.T, manager *Manager, probe *preparationProbe, grace time.Duration) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() {
		cancel()
		stop, done := context.WithTimeout(context.Background(), 10*time.Second)
		defer done()
		noErr(t, manager.StopPreparation(stop))
	})
	noErr(t, manager.StartPreparation(ctx, probe.step, grace, probe.logf))
}

func waitFor(t *testing.T, what string, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !condition() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting until %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestFailedPreparationLocksOnlyThatRepositoryUntilARetrySucceeds(t *testing.T) {
	manager, _, _ := newTestRepository(t)
	_, err := manager.Create(context.Background(), "other", "")
	noErr(t, err)
	manager.PreparationRetry = 10 * time.Millisecond
	probe := newPreparationProbe()
	probe.setFail("sample", true)
	startPreparationForTest(t, manager, probe, 5*time.Second)

	if _, _, _, err := manager.ExistingPath(context.Background(), "sample"); !errors.Is(err, ErrRepositoryPreparing) {
		t.Fatalf("failed repository lookup error=%v, want ErrRepositoryPreparing", err)
	}
	if _, err := manager.RefSnapshot(context.Background(), "sample"); !errors.Is(err, ErrRepositoryPreparing) {
		t.Fatalf("failed repository snapshot error=%v", err)
	}
	if _, _, exists, err := manager.ExistingPath(context.Background(), "other"); err != nil || !exists || manager.Preparing("other") {
		t.Fatalf("the other repository is not served: exists=%v err=%v", exists, err)
	}
	waitFor(t, "a few retries ran", func() bool { return probe.count("sample") >= 3 })
	if !manager.Preparing("sample") {
		t.Fatal("a failing repository became ready")
	}
	var delays []string
	for _, line := range probe.logged() {
		if strings.Contains(line, `"sample" could not be prepared`) {
			delays = append(delays, line[strings.Index(line, "retrying in ")+len("retrying in "):strings.Index(line, ": synthetic")])
		}
	}
	if len(delays) < 2 || delays[0] != "10ms" || delays[1] != "20ms" {
		t.Fatalf("retry waits %q, want 10ms then doubling", delays)
	}

	probe.setFail("sample", false)
	waitFor(t, "the repository became ready", func() bool { return !manager.Preparing("sample") })
	if _, _, exists, err := manager.ExistingPath(context.Background(), "sample"); err != nil || !exists {
		t.Fatalf("prepared repository lookup exists=%v err=%v", exists, err)
	}
	attempts := probe.count("sample")
	time.Sleep(50 * time.Millisecond)
	if probe.count("sample") != attempts {
		t.Fatal("preparation continued after it succeeded")
	}
}

func TestHungPreparationDoesNotHoldStartupOrOverlap(t *testing.T) {
	manager, _, _ := newTestRepository(t)
	manager.PreparationRetry = time.Millisecond
	probe := newPreparationProbe()
	release := make(chan struct{})
	probe.hold["sample"] = release
	started := time.Now()
	startPreparationForTest(t, manager, probe, 100*time.Millisecond)
	if elapsed := time.Since(started); elapsed > 5*time.Second {
		t.Fatalf("startup waited %s for a hung repository", elapsed)
	}
	if !manager.Preparing("sample") {
		t.Fatal("a repository with a hung attempt is served")
	}
	time.Sleep(50 * time.Millisecond)
	if attempts := probe.count("sample"); attempts != 1 {
		t.Fatalf("%d attempts ran while the first had not returned, want 1", attempts)
	}
	close(release)
	waitFor(t, "the released repository became ready", func() bool { return !manager.Preparing("sample") })
}

func TestPreparationRefusesAfterSafetyConfigurationFails(t *testing.T) {
	manager, remote, _ := newTestRepository(t)
	// Git refuses to overwrite a key with several values, so the safety
	// configuration of this repository fails.
	runGit(t, "", "--git-dir", remote, "config", "--local", "--add", "transfer.hideRefs", "refs/other/")
	manager.PreparationRetry = time.Hour
	probe := newPreparationProbe()
	startPreparationForTest(t, manager, probe, 5*time.Second)
	if !manager.Preparing("sample") {
		t.Fatal("a repository whose configuration failed is served")
	}
	if probe.count("sample") != 0 {
		t.Fatal("the recovery step ran after the configuration failed")
	}
}

func TestDeletingAPreparingRepositoryStopsItsPreparation(t *testing.T) {
	manager, _, _ := newTestRepository(t)
	manager.PreparationRetry = 20 * time.Millisecond
	probe := newPreparationProbe()
	probe.setFail("sample", true)
	startPreparationForTest(t, manager, probe, 5*time.Second)
	if !manager.Preparing("sample") {
		t.Fatal("fixture repository is not preparing")
	}
	if _, err := manager.Delete(context.Background(), "sample", DeleteKeepFiles); err != nil {
		t.Fatalf("delete a preparing repository: %v", err)
	}
	if manager.Preparing("sample") {
		t.Fatal("a deleted repository is still preparing")
	}
	attempts := probe.count("sample")

	// A new repository with the same ID is configured at creation and is
	// served at once; the stopped job never prepares it.
	_, err := manager.Create(context.Background(), "sample", "")
	noErr(t, err)
	time.Sleep(100 * time.Millisecond)
	if manager.Preparing("sample") {
		t.Fatal("the new repository with a reused ID is locked")
	}
	if probe.count("sample") != attempts {
		t.Fatal("a stale preparation attempt ran for the new repository")
	}
}

func TestDeletionWaitsForARunningPreparationAttempt(t *testing.T) {
	manager, _, _ := newTestRepository(t)
	probe := newPreparationProbe()
	release := make(chan struct{})
	probe.hold["sample"] = release
	defer close(release)
	startPreparationForTest(t, manager, probe, 10*time.Millisecond)
	waitFor(t, "the attempt started", func() bool { return probe.count("sample") == 1 })
	if _, err := manager.Delete(context.Background(), "sample", DeleteKeepFiles); !errors.Is(err, ErrRepositoryInUse) {
		t.Fatalf("delete during a running attempt error=%v, want ErrRepositoryInUse", err)
	}
	if !manager.Preparing("sample") {
		t.Fatal("a refused deletion changed the preparation state")
	}
}

func TestPreparationFailsStartupOnlyForGlobalFailures(t *testing.T) {
	manager, _, _ := newTestRepository(t)
	noErr(t, manager.Store.Close())
	err := manager.StartPreparation(context.Background(), nil, time.Second, nil)
	if err == nil || !strings.Contains(err.Error(), "read repositories for preparation") {
		t.Fatalf("closed state error=%v", err)
	}
}

// Recovery that registers a repository after startup marks it first, so the
// repository is served only after preparation for this process succeeds.
func TestRegisteredRepositoryIsServedOnlyAfterPreparation(t *testing.T) {
	manager, _, _ := newTestRepository(t)
	manager.PreparationRetry = 10 * time.Millisecond
	probe := newPreparationProbe()
	probe.setFail("late", true)
	startPreparationForTest(t, manager, probe, 5*time.Second)
	manager.PrepareRegistered("late")
	_, err := manager.Create(context.Background(), "late", "")
	noErr(t, err)
	waitFor(t, "the registered repository was attempted", func() bool { return probe.count("late") >= 1 })
	if _, _, _, err := manager.ExistingPath(context.Background(), "late"); !errors.Is(err, ErrRepositoryPreparing) {
		t.Fatalf("registered repository lookup error=%v before preparation succeeded", err)
	}
	probe.setFail("late", false)
	waitFor(t, "the registered repository became ready", func() bool { return !manager.Preparing("late") })
}

// PrepareUnavailable locks a served repository only when its storage cannot
// be read, never starts preparation for a repository that no longer exists,
// and leaves a repository with readable storage served (QA-009).
func TestPrepareUnavailableLocksOnlyUnavailableStorage(t *testing.T) {
	manager, _, _ := newTestRepository(t)
	ctx := context.Background()
	for _, id := range []string{"gone", "deleted"} {
		_, err := manager.Create(ctx, id, "")
		noErr(t, err)
	}
	gonePath, err := manager.Path("gone")
	noErr(t, err)
	noErr(t, os.Rename(gonePath, gonePath+".away"))
	if err := manager.PrepareUnavailable(ctx, "gone"); !errors.Is(err, ErrStorageUnavailable) || manager.Preparing("gone") {
		t.Fatalf("before preparation started: err=%v preparing=%v, want the storage error and no lock", err, manager.Preparing("gone"))
	}
	manager.PreparationRetry = time.Hour
	probe := newPreparationProbe()
	noErr(t, os.Rename(gonePath+".away", gonePath))
	startPreparationForTest(t, manager, probe, 5*time.Second)
	noErr(t, os.Rename(gonePath, gonePath+".away"))

	if err := manager.PrepareUnavailable(ctx, "sample"); err != nil || manager.Preparing("sample") {
		t.Fatalf("readable repository: err=%v preparing=%v", err, manager.Preparing("sample"))
	}
	if err := manager.PrepareUnavailable(ctx, "gone"); !errors.Is(err, ErrRepositoryPreparing) || !manager.Preparing("gone") {
		t.Fatalf("missing folder: err=%v preparing=%v, want a lock", err, manager.Preparing("gone"))
	}
	_, err = manager.Delete(ctx, "deleted", DeleteKeepFiles)
	noErr(t, err)
	if err := manager.PrepareUnavailable(ctx, "deleted"); !errors.Is(err, ErrRepositoryNotFound) || manager.Preparing("deleted") {
		t.Fatalf("deleted repository: err=%v preparing=%v", err, manager.Preparing("deleted"))
	}
}

// A repository locked because its storage was unavailable is prepared again
// soon after the storage returns, not after the whole retry wait (review of
// QA-009).
func TestUnavailableStorageIsRetriedOnceReadable(t *testing.T) {
	manager, _, _ := newTestRepository(t)
	previous := storageProbeInterval
	storageProbeInterval = 20 * time.Millisecond
	t.Cleanup(func() { storageProbeInterval = previous })
	manager.PreparationRetry = time.Hour
	path, err := manager.Path("sample")
	noErr(t, err)
	noErr(t, os.Rename(path, path+".away"))
	probe := newPreparationProbe()
	startPreparationForTest(t, manager, probe, 5*time.Second)
	if !manager.Preparing("sample") {
		t.Fatal("a repository with a missing folder was served")
	}
	time.Sleep(100 * time.Millisecond)
	if !manager.Preparing("sample") {
		t.Fatal("the repository was served while its folder was missing")
	}
	noErr(t, os.Rename(path+".away", path))
	waitFor(t, "the repository was served again", func() bool { return !manager.Preparing("sample") })

	// A failure that is not about storage still waits for its retry.
	probe.setFail("sample", true)
	_, err = manager.Create(context.Background(), "failing", "")
	noErr(t, err)
	probe.setFail("failing", true)
	manager.PrepareRegistered("failing")
	waitFor(t, "the first attempt", func() bool { return probe.count("failing") == 1 })
	time.Sleep(100 * time.Millisecond)
	if probe.count("failing") != 1 {
		t.Fatal("a failure unrelated to storage was retried before its wait")
	}
}
