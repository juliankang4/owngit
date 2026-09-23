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

// Finding 1: a callback result can never commit after the deadline, and the
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
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("expected deadline error: %v", err)
			}
			if cooperative {
				if errors.Is(err, gitexec.ErrPreparedCallbackDetached) {
					t.Fatalf("joined callback reported detached: %v", err)
				}
				select {
				case <-callbackDone:
				default:
					t.Fatal("runner returned before the cooperative callback finished")
				}
			} else {
				if !errors.Is(err, gitexec.ErrPreparedCallbackDetached) {
					t.Fatalf("running callback was not reported: %v", err)
				}
				select {
				case <-callbackDone:
					t.Fatal("detached callback finished before release")
				default:
				}
				close(release)
				<-callbackDone
			}
			if got := f.git(path, "--git-dir", ".", "rev-parse", "refs/tags/v1"); got != old {
				t.Fatalf("late callback committed: before=%s after=%s", old, got)
			}
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
	if err == nil || (run.Status != state.ImportRunFailed && run.Status != state.ImportRunCancelled) {
		t.Fatalf("cancelled prepared run=%+v err=%v", run, err)
	}
	if !repositoryLockedInCallback {
		t.Fatal("prepared callback ran outside the repository write lock")
	}
	if elapsed > 10*time.Second {
		t.Fatalf("cancelled prepared callback join was not bounded: %s", elapsed)
	}
	path := f.destinationPath()
	if got := f.git(path, "--git-dir", ".", "rev-parse", "refs/heads/main"); got != old {
		t.Fatalf("cancelled prepared transaction committed: got=%s want=%s", got, old)
	}
	if _, statErr := os.Lstat(filepath.Join(path, "refs", "heads", "main.lock")); !os.IsNotExist(statErr) {
		t.Fatalf("prepared ref lock leaked after cancellation: %v", statErr)
	}
	f.git(path, "--git-dir", ".", "update-ref", "--no-deref", "refs/heads/main", old, old)
}

// Finding 2: readback must use exact named-ref kinds, and the two false
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
		if got := f.git(path, "--git-dir", ".", "rev-parse", "refs/heads/created"); got != old {
			t.Fatalf("fixture alias does not resolve to the desired object: %s", got)
		}
		if observation.matchesDesired {
			t.Fatalf("resolved alias classified as exact publication: %+v", observation)
		}
		if observation.matchesExpected {
			t.Fatalf("resolved alias classified as proven absence: %+v", observation)
		}
		if !observation.retentionComplete {
			t.Fatalf("empty retention was not complete: %+v", observation)
		}
		if got := f.git(path, "--git-dir", ".", "symbolic-ref", "--no-recurse", "refs/heads/created"); got != protected {
			t.Fatalf("independent alias changed: %s", got)
		}
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
		if observation.retentionComplete {
			t.Fatalf("dangling alias classified as complete retention: %+v", observation)
		}
		if observation.matchesDesired {
			t.Fatalf("dangling alias classified as exact publication: %+v", observation)
		}
		if observation.matchesExpected {
			t.Fatalf("dangling alias classified as proven absence: %+v", observation)
		}
		if content, readErr := os.ReadFile(filepath.Join(path, filepath.FromSlash(retention))); readErr != nil || string(content) != string(raw) {
			t.Fatalf("dangling retention alias changed: %q err=%v", content, readErr)
		}
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
	run := state.ImportRun{
		ID: strings.Repeat("e", 32), RepositoryID: "project", SourceGeneration: 1, AuthorityRevision: 1,
		Kind: state.ImportKindRefresh, Status: state.ImportRunPreparing, StartedAt: f.now, CreatedAt: f.now,
	}
	noErr(t, f.store.BeginImportRun(context.Background(), run))
	run.Status = state.ImportRunInterrupted
	run.FinishedAt = f.now
	noErr(t, f.store.FinishImportRun(context.Background(), run))
	head := (headIdentity{kind: headSymbolic, target: "refs/heads/main", oid: old}).encode()
	intent := state.ImportIntent{
		ID: strings.Repeat("f", 32), RepositoryID: "project", RunID: run.ID, SourceGeneration: 1, AuthorityRevision: 1,
		Status:   state.ImportIntentPlanning,
		Expected: map[string]string{"refs/heads/main": old, "refs/heads/created": "", state.ImportHeadRef: head},
		Desired:  map[string]string{"refs/heads/main": old, "refs/heads/created": old, state.ImportHeadRef: head},
		Observed: map[string]string{"refs/heads/main": old, "refs/heads/created": old, state.ImportHeadRef: head},
		Retained: map[string]string{}, CreatedAt: f.now,
	}
	noErr(t, f.store.CreateImportIntent(context.Background(), intent))
	if err := f.service.Reconcile(context.Background()); err == nil || problemCode(err) != CodeUnresolved {
		t.Fatalf("absent-to-dangling reconciliation err=%v", err)
	}
	stored, _, err := f.store.ImportIntent(context.Background(), intent.ID)
	if err != nil || stored.Status != state.ImportIntentUnresolved {
		t.Fatalf("intent status=%q err=%v", stored.Status, err)
	}
	if content, readErr := os.ReadFile(created); readErr != nil || string(content) != "ref: refs/heads/dangling\n" {
		t.Fatalf("dangling alias changed: %q err=%v", content, readErr)
	}
}

// Finding 3: prepared validation and readback use fixed namespace arguments.
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
	if _, err := f.manager.Git.Run(context.Background(), path, strings.NewReader(input.String()), "--git-dir", ".", "update-ref", "--stdin"); err != nil {
		t.Fatal(err)
	}
	argumentBytes := 0
	for _, prefix := range publicationRefPrefixes {
		argumentBytes += len(prefix) + 1
	}
	nameBytes := 0
	for _, name := range names {
		nameBytes += len(name) + 1
	}
	if argumentBytes >= nameBytes/100 {
		t.Fatalf("fixed namespace arguments are not independent of ref names: prefixes=%d names=%d", argumentBytes, nameBytes)
	}
	refs, symrefs, err := f.service.readPublicationRefs(context.Background(), path, names)
	if err != nil {
		t.Fatalf("bounded read of %d wide names: %v", len(names), err)
	}
	for _, name := range names {
		if refs[name] != oid || symrefs[name] != "" {
			t.Fatalf("exact read missed %s: oid=%q symref=%q", name, refs[name], symrefs[name])
		}
	}
	// Enumeration omits dangling aliases; the exact read still finds them.
	dangling := "refs/heads/dangling-alias"
	noErr(t, os.WriteFile(filepath.Join(path, filepath.FromSlash(dangling)), []byte("ref: refs/heads/nowhere\n"), 0o600))
	refs, symrefs, err = f.service.readPublicationRefs(context.Background(), path, []string{dangling})
	noErr(t, err)
	if symrefs[dangling] != "refs/heads/nowhere" || refs[dangling] != "" {
		t.Fatalf("dangling alias omitted by bounded enumeration: oid=%q symref=%q", refs[dangling], symrefs[dangling])
	}
}

// Finding 4: the platform indirect-path check applies to repository root, raw
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
		if _, err := f.service.acquireHEADLock(context.Background(), run, link, expected); err == nil || problemCode(err) != CodeRepositoryMissing {
			t.Fatalf("linked repository root accepted: %v", err)
		}
		if _, statErr := os.Lstat(filepath.Join(path, "HEAD.lock")); !os.IsNotExist(statErr) {
			t.Fatalf("lock was created through the linked root: %v", statErr)
		}
	})
	t.Run("HEAD.lock link is preserved", func(t *testing.T) {
		sentinel := filepath.Join(f.root, "lock-sentinel")
		noErr(t, os.WriteFile(sentinel, []byte("independent\n"), 0o600))
		lockPath := filepath.Join(path, "HEAD.lock")
		noErr(t, os.Symlink(sentinel, lockPath))
		defer os.Remove(lockPath)
		if _, err := f.service.acquireHEADLock(context.Background(), run, path, expected); err == nil || problemCode(err) != CodeDestinationChanged {
			t.Fatalf("linked HEAD.lock accepted: %v", err)
		}
		if err := removeOwnedHEADLock(lockPath, nil); err == nil {
			t.Fatal("cleanup removed a lock it never created")
		}
		if info, err := os.Lstat(lockPath); err != nil || info.Mode()&os.ModeSymlink == 0 {
			t.Fatalf("linked lock changed: %v %v", info, err)
		}
		if content, err := os.ReadFile(sentinel); err != nil || string(content) != "independent\n" {
			t.Fatalf("lock target changed: %q %v", content, err)
		}
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
		if err := lock.rollback(); err == nil {
			t.Fatal("rollback removed a replacement link")
		}
		if info, err := os.Lstat(lock.path); err != nil || info.Mode()&os.ModeSymlink == 0 {
			t.Fatalf("replacement link changed: %v %v", info, err)
		}
		if content, err := os.ReadFile(sentinel); err != nil || string(content) != "independent\n" {
			t.Fatalf("replacement target changed: %q %v", content, err)
		}
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
		if target, err := os.Readlink(headPath); err != nil || target != linkTarget {
			t.Fatalf("HEAD symlink target=%q err=%v", target, err)
		}
		if bare := f.git(path, "--git-dir", ".", "config", "--local", "--get", "core.bare"); bare != "true" {
			t.Fatalf("Git does not recognize the symlinked HEAD repository: core.bare=%q", bare)
		}
		if _, err := f.service.acquireHEADLock(context.Background(), run, path, expected); err == nil || problemCode(err) != CodePublishFailed {
			t.Fatalf("linked HEAD accepted: %v", err)
		}
		if _, statErr := os.Lstat(filepath.Join(path, "HEAD.lock")); !os.IsNotExist(statErr) {
			t.Fatalf("lock was created beside a linked HEAD: %v", statErr)
		}
		if content, err := os.ReadFile(sentinel); err != nil || string(content) != string(original) {
			t.Fatalf("HEAD link target changed: %q %v", content, err)
		}
	})
}

// Production lifecycle: while the prepared callback is still running, the
// publication guard and the repository write lock stay held, no HEAD write
// happens and the run does not complete. Once the callback finishes the run
// reports the deadline honestly with the transaction aborted.
func TestPublicationHoldsGuardsUntilPreparedCallbackFinishes(t *testing.T) {
	f := newFixture(t)
	old := f.commit("initial", "initial\n")
	f.git(f.source, "branch", "dev")
	initial := f.mustImport(ImportInput{})
	markHEADOwnedForTest(t, f, initial.Run.ID)
	f.git(f.source, "checkout", "--quiet", "dev")
	f.commit("dev next", "next\n")
	f.manager.Git.TerminationGrace = 200 * time.Millisecond
	entered := make(chan struct{})
	release := make(chan struct{})
	f.service.whileRefsPrepared = func() {
		f.service.whileRefsPrepared = nil
		close(entered)
		<-release
	}
	done := make(chan struct{})
	var run state.ImportRun
	var runErr error
	go func() {
		defer close(done)
		run, runErr = f.service.Refresh(context.Background(), "project", Limits{PublishTimeout: time.Second})
	}()
	<-entered
	// Deadline plus termination grace plus join grace all pass here.
	time.Sleep(2500 * time.Millisecond)
	select {
	case <-done:
		t.Fatalf("publication returned while its prepared callback was still running: run=%+v err=%v", run, runErr)
	default:
	}
	if f.manager.Locks.For("project").TryLock() {
		f.manager.Locks.For("project").Unlock()
		t.Fatal("repository write lock was released while the prepared callback was running")
	}
	if active, exists, err := f.store.ActiveImportRun(context.Background(), "project"); err != nil || !exists || active.Status != state.ImportRunPublishing {
		t.Fatalf("run left publishing while the callback was running: active=%+v exists=%v err=%v", active, exists, err)
	}
	close(release)
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("publication did not return after the callback finished")
	}
	if runErr == nil || run.Status != state.ImportRunFailed || !errors.Is(runErr, context.DeadlineExceeded) {
		t.Fatalf("late callback run=%+v err=%v", run, runErr)
	}
	path := f.destinationPath()
	if got := f.destinationRefs()["refs/heads/dev"]; got != old {
		t.Fatalf("aborted transaction committed: got=%s want=%s", got, old)
	}
	if got := f.git(path, "--git-dir", ".", "symbolic-ref", "--no-recurse", "HEAD"); got != "refs/heads/main" {
		t.Fatalf("HEAD written after an aborted transaction: %s", got)
	}
	if _, statErr := os.Lstat(filepath.Join(path, "HEAD.lock")); !os.IsNotExist(statErr) {
		t.Fatalf("HEAD.lock left behind: %v", statErr)
	}
	f.git(path, "--git-dir", ".", "update-ref", "--no-deref", "refs/heads/dev", old, old)
}
