package importsync

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"owngit/internal/gitexec"
	"owngit/internal/repository"
	"owngit/internal/state"
)

// A callback result can never commit after the deadline, and the
// runner reports whether the callback was still running when it returned. A
// callback that returns inside the grace interval is joined; one that ignores
// its context is reported as detached so the caller can hold its own guards.
func TestPreparedCallbackLifetimeIsReportedAtTimeout(t *testing.T) {
	for _, cooperative := range []bool{true, false} {
		name := "detached"
		if cooperative {
			name = "joined"
		}
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			commit := f.commit("source", "source bytes\n")
			f.git(f.source, "tag", "-a", "v1", "-m", "annotation")
			old := f.git(f.source, "rev-parse", "refs/tags/v1")
			f.mustImport(ImportInput{})
			path := f.destinationPath()
			f.manager.Git.TerminationGrace = 300 * time.Millisecond
			release := make(chan struct{})
			callbackDone := make(chan struct{})
			_, err := f.manager.Git.RunPreparedUpdateContext(context.Background(), path,
				[]string{"update refs/tags/v1 " + commit + " " + old},
				gitexec.CommandLimits{Timeout: 200 * time.Millisecond}, func(ctx context.Context) error {
					defer close(callbackDone)
					<-ctx.Done()
					if cooperative {
						return nil
					}
					<-release
					return nil
				})
			require(t, errors.Is(err, context.DeadlineExceeded), "expected deadline error: %v", err)
			require(t, errors.Is(err, gitexec.ErrPreparedCallbackDetached) == !cooperative,
				"callback detached report (cooperative=%v): %v", cooperative, err)
			finished := false
			select {
			case <-callbackDone:
				finished = true
			default:
			}
			require(t, finished == cooperative, "callback finished at return=%v, cooperative=%v", finished, cooperative)
			if !cooperative {
				close(release)
				<-callbackDone
			}
			eq(t, "tag after a late callback", f.rev(path, "refs/tags/v1"), old)
			f.git(path, "--git-dir", ".", "update-ref", "--no-deref", "refs/tags/v1", old, old)
		})
	}
}

// The production callback honors its derived context at every blocking step,
// so the callback join stays bounded: a cancellation while prepared returns
// promptly, aborts, releases Git's locks and keeps the transaction error.
func TestProductionPreparedCallbackJoinIsBoundedUnderCancellation(t *testing.T) {
	f := newFixture(t)
	old := f.commit("source", "initial\n")
	f.mustImport(ImportInput{})
	f.commit("source", "next\n")
	ctx, cancel := context.WithCancel(context.Background())
	var repositoryLockedInCallback bool
	f.service.whileRefsPrepared = func() {
		f.service.whileRefsPrepared = nil
		repositoryLockedInCallback = !f.manager.Locks.For("project").TryLock()
		cancel()
	}
	started := time.Now()
	run, err := f.service.Refresh(ctx, "project", Limits{})
	elapsed := time.Since(started)
	require(t, err != nil && (run.Status == state.ImportRunFailed || run.Status == state.ImportRunCancelled),
		"cancelled prepared run=%+v err=%v", run, err)
	require(t, repositoryLockedInCallback, "prepared callback ran outside the repository write lock")
	require(t, elapsed <= 10*time.Second, "cancelled prepared callback join was not bounded: %s", elapsed)
	path := f.destinationPath()
	eq(t, "main after a cancelled prepared transaction", f.rev(path, "refs/heads/main"), old)
	absent(t, filepath.Join(path, "refs", "heads", "main.lock"))
	f.git(path, "--git-dir", ".", "update-ref", "--no-deref", "refs/heads/main", old, old)
}

// Readback must use exact named-ref kinds, and the two false
// classifications are proven independently. A resolved alias whose object
// equals the desired OID is still not a direct publication, and a retention
// name that an independent writer turned into a dangling alias is neither
// proven absence nor created retention.
func TestObserveIntentRequiresExactKindsForCreationAndRetention(t *testing.T) {
	// refs/heads/created: desired creation; installed as a resolved alias to
	// an object already present in the destination, so the resolved OID alone
	// equals the desired one.
	t.Run("resolved alias is not direct creation", func(t *testing.T) {
		f := newFixture(t)
		old := f.commit("source", "initial\n")
		f.mustImport(ImportInput{})
		path := f.destinationPath()
		protected := "refs/owngit/manual/keep"
		f.git(path, "--git-dir", ".", "update-ref", protected, old)
		f.git(path, "--git-dir", ".", "symbolic-ref", "refs/heads/created", protected)
		head := (headIdentity{kind: headSymbolic, target: "refs/heads/main", oid: old}).encode()
		intent := state.ImportIntent{
			Expected: map[string]string{"refs/heads/created": "", state.ImportHeadRef: head},
			Desired:  map[string]string{"refs/heads/created": old, state.ImportHeadRef: head},
			Retained: map[string]string{},
		}
		observation, err := f.service.observeIntent(context.Background(), path, intent)
		noErr(t, err)
		eq(t, "fixture alias referent", f.rev(path, "refs/heads/created"), old)
		require(t, !observation.matchesDesired && !observation.matchesExpected && observation.retentionComplete,
			"resolved alias must be neither exact publication nor proven absence, and empty retention complete: %+v", observation)
		eq(t, "independent alias", f.symref(path, "refs/heads/created"), protected)
	})

	// retention: desired direct old object; installed as a dangling alias, so
	// the ref is neither absent, nor a direct publication, nor complete
	// retention.
	t.Run("dangling alias is not complete retention", func(t *testing.T) {
		f := newFixture(t)
		old := f.commit("source", "initial\n")
		f.mustImport(ImportInput{})
		path := f.destinationPath()
		retention := repository.RetainedRefName("heads", old)
		raw := []byte("ref: refs/heads/missing-target\n")
		noErr(t, os.MkdirAll(filepath.Dir(filepath.Join(path, filepath.FromSlash(retention))), 0o700))
		noErr(t, os.WriteFile(filepath.Join(path, filepath.FromSlash(retention)), raw, 0o600))
		head := (headIdentity{kind: headSymbolic, target: "refs/heads/main", oid: old}).encode()
		intent := state.ImportIntent{
			Expected: map[string]string{retention: "", state.ImportHeadRef: head},
			Desired:  map[string]string{retention: old, state.ImportHeadRef: head},
			Retained: map[string]string{retention: old},
		}
		observation, err := f.service.observeIntent(context.Background(), path, intent)
		noErr(t, err)
		require(t, !observation.retentionComplete && !observation.matchesDesired && !observation.matchesExpected,
			"dangling alias must be neither complete retention, exact publication nor proven absence: %+v", observation)
		fileIs(t, filepath.Join(path, filepath.FromSlash(retention)), string(raw))
	})
}

// A proven untouched expected state also needs the exact kind: a ref that an
// intent expected absent and an independent writer made a dangling alias is
// neither absent nor desired, so reconciliation records unresolved instead of
// not applied.
func TestReconcileAbsentToDanglingAliasIsUnresolved(t *testing.T) {
	f := newFixture(t)
	old := f.commit("one", "one\n")
	f.mustImport(ImportInput{})
	path := f.destinationPath()
	created := filepath.Join(path, "refs", "heads", "created")
	noErr(t, os.WriteFile(created, []byte("ref: refs/heads/dangling\n"), 0o600))
	head := (headIdentity{kind: headSymbolic, target: "refs/heads/main", oid: old}).encode()
	intent := f.seedIntent('e',
		map[string]string{"refs/heads/main": old, "refs/heads/created": "", state.ImportHeadRef: head},
		map[string]string{"refs/heads/main": old, "refs/heads/created": old, state.ImportHeadRef: head},
		map[string]string{"refs/heads/main": old, "refs/heads/created": old, state.ImportHeadRef: head})
	err := f.service.Reconcile(context.Background())
	require(t, err != nil && problemCode(err) == CodeUnresolved, "absent-to-dangling reconciliation err=%v", err)
	stored, _, err := f.store.ImportIntent(context.Background(), intent.ID)
	require(t, err == nil && stored.Status == state.ImportIntentUnresolved,
		"intent status=%q err=%v", stored.Status, err)
	fileIs(t, created, "ref: refs/heads/dangling\n")
}

// Prepared validation and readback use fixed namespace arguments.
// The argv size must not grow with the number or length of ref names, so the
// check counts the bytes the fixed prefixes occupy rather than depending on the
// host ARG_MAX.
func TestPublicationRefQueriesUseFixedNamespaceArguments(t *testing.T) {
	f := newFixture(t)
	oid := f.commit("source", "source bytes\n")
	f.mustImport(ImportInput{})
	path := f.destinationPath()
	var names []string
	var input strings.Builder
	for index := 0; index < 2000; index++ {
		name := fmt.Sprintf("refs/heads/wide-%04d-%s", index, strings.Repeat("n", 200))
		names = append(names, name)
		fmt.Fprintf(&input, "create %s %s\n", name, oid)
	}
	_, err := f.manager.Git.Run(context.Background(), path, strings.NewReader(input.String()), "--git-dir", ".", "update-ref", "--stdin")
	noErr(t, err)
	argumentBytes := 0
	for _, prefix := range publicationRefPrefixes(names) {
		argumentBytes += len(prefix) + 1
	}
	nameBytes := 0
	for _, name := range names {
		nameBytes += len(name) + 1
	}
	require(t, argumentBytes < nameBytes/100,
		"fixed namespace arguments are not independent of ref names: prefixes=%d names=%d", argumentBytes, nameBytes)
	refs, symrefs, err := f.service.readPublicationRefs(context.Background(), path, names)
	noErr(t, err, "bounded read of wide names")
	for _, name := range names {
		require(t, refs[name] == oid && symrefs[name] == "",
			"exact read missed %s: oid=%q symref=%q", name, refs[name], symrefs[name])
	}
	// Enumeration omits dangling aliases; the exact read still finds them.
	dangling := "refs/heads/dangling-alias"
	noErr(t, os.WriteFile(filepath.Join(path, filepath.FromSlash(dangling)),
		[]byte("ref: refs/heads/nowhere\n"), 0o600))
	refs, symrefs, err = f.service.readPublicationRefs(context.Background(), path, []string{dangling})
	noErr(t, err)
	require(t, symrefs[dangling] == "refs/heads/nowhere" && refs[dangling] == "",
		"dangling alias omitted by bounded enumeration: oid=%q symref=%q", refs[dangling], symrefs[dangling])
}

// The platform indirect-path check applies to repository root, raw
// HEAD, HEAD.lock and cleanup identity. On Unix the check is ModeSymlink; the
// Windows reparse-point tests live in head_windows_test.go.
func TestHEADLockRefusesIndirectRootAndLockPaths(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symbolic link fixtures need privileges on Windows; native reparse coverage is separate")
	}
	f := newFixture(t)
	oid := f.commit("source", "source bytes\n")
	f.mustImport(ImportInput{})
	path := f.destinationPath()
	expected := headIdentity{kind: headSymbolic, target: "refs/heads/main", oid: oid}
	run := &runState{limits: DefaultLimits()}

	t.Run("repository root link", func(t *testing.T) {
		link := filepath.Join(f.root, "root-link")
		noErr(t, os.Symlink(path, link))
		_, err := f.service.acquireHEADLock(context.Background(), run, link, expected)
		require(t, err != nil && problemCode(err) == CodeRepositoryMissing, "linked repository root accepted: %v", err)
		absent(t, filepath.Join(path, "HEAD.lock"))
	})
	t.Run("HEAD.lock link is preserved", func(t *testing.T) {
		sentinel := filepath.Join(f.root, "lock-sentinel")
		noErr(t, os.WriteFile(sentinel, []byte("independent\n"), 0o600))
		lockPath := filepath.Join(path, "HEAD.lock")
		noErr(t, os.Symlink(sentinel, lockPath))
		defer os.Remove(lockPath)
		_, err := f.service.acquireHEADLock(context.Background(), run, path, expected)
		require(t, err != nil && problemCode(err) == CodeDestinationChanged, "linked HEAD.lock accepted: %v", err)
		require(t, removeOwnedHEADLock(lockPath, nil) != nil, "cleanup removed a lock it never created")
		isSymlink(t, lockPath)
		fileIs(t, sentinel, "independent\n")
	})
	t.Run("owned lock replaced by link before rollback", func(t *testing.T) {
		lock, err := f.service.acquireHEADLock(context.Background(), run, path, expected)
		noErr(t, err)
		sentinel := filepath.Join(f.root, "rollback-sentinel")
		noErr(t, os.WriteFile(sentinel, []byte("independent\n"), 0o600))
		noErr(t, os.Rename(lock.path, lock.path+".moved"))
		defer os.Remove(lock.path + ".moved")
		noErr(t, os.Symlink(sentinel, lock.path))
		defer os.Remove(lock.path)
		require(t, lock.rollback() != nil, "rollback removed a replacement link")
		isSymlink(t, lock.path)
		fileIs(t, sentinel, "independent\n")
	})
	t.Run("HEAD link refused before lock creation", func(t *testing.T) {
		headPath := filepath.Join(path, "HEAD")
		original, err := os.ReadFile(headPath)
		noErr(t, err)
		// Git recognizes a symlinked HEAD only when the link target begins with
		// "refs/", so the sentinel lives inside the repository refs tree and the
		// link target stays relative.
		sentinel := filepath.Join(path, "refs", "heads", "head-sentinel")
		noErr(t, os.WriteFile(sentinel, original, 0o600))
		noErr(t, os.Remove(headPath))
		linkTarget := "refs/heads/head-sentinel"
		noErr(t, os.Symlink(linkTarget, headPath))
		defer func() {
			_ = os.Remove(headPath)
			_ = os.WriteFile(headPath, original, 0o600)
		}()
		target, err := os.Readlink(headPath)
		require(t, err == nil && target == linkTarget, "HEAD symlink target=%q err=%v", target, err)
		eq(t, "Git recognizes the symlinked HEAD repository (core.bare)",
			f.git(path, "--git-dir", ".", "config", "--local", "--get", "core.bare"), "true")
		_, err = f.service.acquireHEADLock(context.Background(), run, path, expected)
		require(t, err != nil && problemCode(err) == CodePublishFailed, "linked HEAD accepted: %v", err)
		absent(t, filepath.Join(path, "HEAD.lock"))
		fileIs(t, sentinel, string(original))
	})
}

// publicationHoldDeadlines are the publication deadlines tried in turn by
// TestPublicationHoldsGuardsUntilPreparedCallbackFinishes.
var publicationHoldDeadlines = []time.Duration{time.Second, 4 * time.Second, 16 * time.Second}

// Production lifecycle: while the prepared callback is still running, the
// publication guard and the repository write lock stay held, no HEAD write
// happens and the run does not complete. Once the callback finishes the run
// reports the deadline honestly with the transaction aborted.
func TestPublicationHoldsGuardsUntilPreparedCallbackFinishes(t *testing.T) {
	if testing.Short() {
		t.Skip("waits out publication deadlines plus a 5 second grace")
	}
	f := newFixture(t)
	old := f.ownedDevCheckout()
	f.commit("dev next", "next\n")
	// The grace bounds each wait for the stopped transaction: first for the
	// callback to abort it, then for the killed process to be reaped. Each
	// wait returns as soon as its step finishes, so a long grace costs time
	// only on a loaded machine that needs it. A short one let a loaded machine
	// miss the reap, and the run then honestly ended unresolved.
	grace := 5 * time.Second
	f.manager.Git.TerminationGrace = grace
	entered := make(chan struct{})
	release := make(chan struct{})
	f.service.whileRefsPrepared = func() {
		f.service.whileRefsPrepared = nil
		close(entered)
		<-release
	}
	// Under load the publication deadline can expire before publication
	// reaches the prepared callback, and the run then ends without testing
	// anything. Such a round is repeated with a longer deadline; the wait for
	// the callback is bounded, so a missed round never hangs the test.
	var (
		done    chan struct{}
		run     state.ImportRun
		runErr  error
		timeout time.Duration
	)
	for round, candidate := range publicationHoldDeadlines {
		timeout, done = candidate, make(chan struct{})
		go func(done chan struct{}) {
			defer close(done)
			run, runErr = f.service.Refresh(context.Background(), "project", Limits{PublishTimeout: timeout})
		}(done)
		select {
		case <-entered:
		case <-done:
			// Only a cleanly failed round leaves the repository ready for
			// another attempt.
			require(t, round != len(publicationHoldDeadlines)-1 && run.Status == state.ImportRunFailed,
				"publication ended before its prepared callback ran with a %s deadline: run=%+v err=%v", timeout, run, runErr)
			t.Logf("publication ended before its prepared callback ran with a %s deadline (run %s, err %v); retrying with a longer one", timeout, run.Status, runErr)
			continue
		case <-time.After(timeout + time.Minute):
			t.Fatalf("publication neither reached its prepared callback nor returned within %s", timeout+time.Minute)
		}
		break
	}
	// Without the guard, publication would return once the deadline and the
	// callback's abort grace passed and the killed process was reaped, which
	// takes well under 1.5 s more. It must still be waiting here.
	time.Sleep(timeout + grace + 1500*time.Millisecond)
	select {
	case <-done:
		t.Fatalf("publication returned while its prepared callback was still running: run=%+v err=%v", run, runErr)
	default:
	}
	if f.manager.Locks.For("project").TryLock() {
		f.manager.Locks.For("project").Unlock()
		t.Fatal("repository write lock was released while the prepared callback was running")
	}
	active, exists, err := f.store.ActiveImportRun(context.Background(), "project")
	require(t, err == nil && exists && active.Status == state.ImportRunPublishing,
		"run left publishing while the callback was running: active=%+v exists=%v err=%v", active, exists, err)
	close(release)
	select {
	case <-done:
	case <-time.After(time.Minute):
		t.Fatal("publication did not return after the callback finished")
	}
	require(t, runErr != nil && run.Status == state.ImportRunFailed && errors.Is(runErr, context.DeadlineExceeded),
		"late callback run=%+v err=%v", run, runErr)
	path := f.destinationPath()
	eq(t, "dev after an aborted transaction", f.destinationRefs()["refs/heads/dev"], old)
	eq(t, "HEAD after an aborted transaction", f.headRef(path), "refs/heads/main")
	absent(t, filepath.Join(path, "HEAD.lock"))
	f.git(path, "--git-dir", ".", "update-ref", "--no-deref", "refs/heads/dev", old, old)
}
