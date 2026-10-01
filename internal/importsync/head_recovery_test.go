package importsync

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"owngit/internal/gitexec"
	"owngit/internal/repository"
	"owngit/internal/state"
	"owngit/internal/testfixture"
)

func TestRecoveredPublicationKeepsProvenHEADOwnership(t *testing.T) {
	f := newFixture(t)
	f.commit("initial", "initial\n")
	f.mustImport(ImportInput{})
	f.git(f.source, "branch", "release")
	f.git(f.source, "symbolic-ref", "HEAD", "refs/heads/release")
	ctx := context.Background()
	noErr(t, f.store.Exec(ctx, `CREATE TRIGGER fail_receipt BEFORE UPDATE ON import_publication_intents
 WHEN NEW.status='complete' BEGIN SELECT RAISE(FAIL,'synthetic final receipt failure'); END`))
	run, err := f.refresh()
	if err == nil {
		t.Fatal("fixture did not interrupt final bookkeeping")
	}
	intents, err := f.store.PendingImportIntents(ctx, "project")
	noErr(t, err)
	if len(intents) != 1 || !intents[0].HeadOwned {
		t.Fatalf("missing pre-restart ownership: %+v", intents)
	}
	noErr(t, f.store.Exec(ctx, `DROP TRIGGER fail_receipt`))
	noErr(t, f.service.Close())
	f.service = &Service{Store: f.store, Repositories: f.manager, Fetch: f.transport.fetch, Clock: func() time.Time { return f.now }}
	noErr(t, f.service.Reconcile(ctx))
	recovered, exists, err := f.store.ImportRun(ctx, run.ID)
	noErr(t, err)
	if !exists || recovered.Status != state.ImportRunComplete {
		t.Fatalf("recovered run: %+v", recovered)
	}
	f.git(f.source, "symbolic-ref", "HEAD", "refs/heads/main")
	next, err := f.refresh()
	noErr(t, err)
	if next.RefsDivergent != 0 || f.git(f.destinationPath(), "--git-dir", ".", "symbolic-ref", "HEAD") != "refs/heads/main" {
		t.Fatalf("recovered source HEAD ownership was lost: %+v", next)
	}
}

// This child uses real Git and pauses without changing any publication result.
// The parent kills the process only after the labelled boundary is reached.
func TestImportPublicationCrashChild(t *testing.T) {
	phase, marker := os.Getenv("OWNGIT_IMPORT_CRASH_PHASE"), os.Getenv("OWNGIT_IMPORT_CRASH_MARKER")
	if phase == "" || marker == "" {
		t.Skip("subprocess fixture")
	}
	f := newFixture(t)
	first := f.commit("initial", "initial\n")
	f.git(f.source, "checkout", "-b", "release")
	f.commit("release", "release\n")
	f.git(f.source, "tag", "v1", first)
	f.git(f.source, "checkout", "main")
	f.mustImport(ImportInput{})
	next := f.commit("next", "next\n")
	f.git(f.source, "branch", "-f", "release", first)
	f.git(f.source, "tag", "-f", "v1", next)
	f.git(f.source, "branch", "topic")
	f.git(f.source, "symbolic-ref", "HEAD", "refs/heads/release")
	pause := func() {
		content, err := json.Marshal(map[string]string{"root": f.root, "phase": phase})
		noErr(t, err)
		noErr(t, os.WriteFile(marker, content, 0600))
		select {}
	}
	switch phase {
	case "before-refs":
		f.service.beforeRefTransaction = pause
	case "after-refs":
		f.service.beforeRecord = func(_ context.Context, record string) {
			if record == "applied publication" {
				pause()
			}
		}
	case "before-head":
		f.service.beforeFinalHEADLock = pause
	case "head-locked":
		f.service.beforeHEADRename = pause
	case "after-head":
		f.service.beforeRecord = func(_ context.Context, record string) {
			if record == "applied HEAD" {
				pause()
			}
		}
	case "before-finalize":
		f.service.beforeRecord = func(_ context.Context, record string) {
			if record == "completed publication" {
				pause()
			}
		}
	case "after-finalize":
		f.service.beforeRecord = func(_ context.Context, record string) {
			if record == "publication response" {
				pause()
			}
		}
	default:
		t.Fatal("unknown publication phase")
	}
	_, err := f.refresh()
	t.Fatalf("pause was not reached: %v", err)
}

func killedPublicationFixture(t *testing.T, phase string) *fixture {
	t.Helper()
	controller := t.TempDir()
	marker := filepath.Join(controller, "reached.json")
	log, err := os.Create(filepath.Join(controller, "child.log"))
	noErr(t, err)
	defer log.Close()
	binary, err := os.Executable()
	noErr(t, err)
	childTest := "TestImportPublicationCrashChild"
	if phase == "legacy-head-locked" {
		binary = os.Getenv("OWNGIT_LEGACY_IMPORT_TEST_BINARY")
		if binary == "" {
			t.Skip("released import fixture binary is not configured")
		}
		childTest = "TestLegacyLockCrashFixture"
	}
	cmd := exec.Command(binary, "-test.run=^"+childTest+"$", "-test.timeout=45s")
	cmd.Env = append(os.Environ(), "OWNGIT_IMPORT_CRASH_PHASE="+phase, "OWNGIT_IMPORT_CRASH_MARKER="+marker, "TMPDIR="+controller)
	cmd.Stdout, cmd.Stderr = log, log
	noErr(t, cmd.Start())
	stopped := false
	defer func() {
		if !stopped {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	}()
	deadline := time.Now().Add(30 * time.Second)
	var gate map[string]string
	for time.Now().Before(deadline) {
		content, readErr := os.ReadFile(marker)
		if readErr == nil && json.Unmarshal(content, &gate) == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if gate["root"] == "" || gate["phase"] != phase {
		content, _ := os.ReadFile(log.Name())
		t.Fatalf("publication pause not reached: %s", content)
	}
	noErr(t, cmd.Process.Kill())
	if err := cmd.Wait(); err == nil {
		t.Fatal("child was not killed")
	}
	stopped = true
	t.Logf("reached %s and killed importer process", phase)
	ctx := context.Background()
	root := gate["root"]
	store, err := state.Open(ctx, filepath.Join(root, "state"))
	noErr(t, err)
	t.Cleanup(func() { noErr(t, store.Close()) })
	runner, err := gitexec.New("", filepath.Join(root, "runtime"))
	noErr(t, err)
	manager := &repository.Manager{Store: store, Git: runner, Locks: gitexec.NewLocks(), Root: filepath.Join(root, "repositories")}
	env := append(os.Environ(), "GIT_CONFIG_GLOBAL="+filepath.Join(root, "gitconfig"), "GIT_CONFIG_NOSYSTEM=1",
		"GIT_AUTHOR_NAME=Import Test", "GIT_AUTHOR_EMAIL=import@example.invalid", "GIT_COMMITTER_NAME=Import Test", "GIT_COMMITTER_EMAIL=import@example.invalid")
	f := &fixture{t: t, root: root, gitPath: runner.GitPath, env: testfixture.GitEnvironment(env), store: store, manager: manager,
		source: filepath.Join(root, "source"), now: time.Unix(1_800_000_000, 0).UTC()}
	f.transport = &fakeTransport{fixture: f}
	f.service = &Service{Store: store, Repositories: manager, Fetch: f.transport.fetch, Clock: func() time.Time { return f.now }}
	t.Cleanup(func() { noErr(t, f.service.Close()) })
	return f
}

func TestImportPublicationCrashRecovery(t *testing.T) {
	for _, phase := range []string{"before-refs", "after-refs", "before-head", "head-locked", "after-head", "before-finalize", "after-finalize"} {
		t.Run(phase, func(t *testing.T) {
			f := killedPublicationFixture(t, phase)
			ctx := context.Background()
			history, _, err := f.store.ImportRuns(ctx, "project", 10)
			noErr(t, err)
			var initial state.ImportIntent
			for _, run := range history {
				if run.Kind == state.ImportKindInitial {
					var exists bool
					initial, exists, err = f.store.CompletedImportIntentForRun(ctx, run.ID)
					noErr(t, err)
					if !exists {
						t.Fatal("initial receipt missing")
					}
				}
			}
			err = f.service.Reconcile(ctx)
			if phase == "after-refs" || phase == "before-head" || phase == "head-locked" {
				if problemCode(err) != CodeUnresolved {
					t.Fatalf("partial publication was not unresolved: %v", err)
				}
				_, err = f.service.ResolveUnresolved(ctx, "project")
				noErr(t, err)
			} else {
				noErr(t, err)
			}
			if _, err := os.Lstat(filepath.Join(f.destinationPath(), "HEAD.lock")); !os.IsNotExist(err) {
				t.Fatalf("abandoned lock remains: %v", err)
			}
			_, err = f.refresh()
			noErr(t, err)
			refs := f.destinationRefs()
			for _, kind := range []string{"heads", "tags"} {
				name := "refs/heads/release"
				if kind == "tags" {
					name = "refs/tags/v1"
				}
				tip := initial.Desired[name]
				if tip == "" || refs[repository.RetainedRefName(kind, tip)] != tip {
					t.Fatalf("replaced %s history was not retained: %s", kind, tip)
				}
			}
			if got := f.git(f.destinationPath(), "symbolic-ref", "HEAD"); got != "refs/heads/release" {
				t.Fatalf("recovery HEAD=%s", got)
			}
			local := f.localWork("release", "owner work after recovery\n")
			if f.destinationRefs()["refs/heads/release"] != local {
				t.Fatal("owner push was not accepted")
			}
			f.git(f.source, "symbolic-ref", "HEAD", "refs/heads/main")
			_, err = f.refresh()
			noErr(t, err)
			if got := f.git(f.destinationPath(), "symbolic-ref", "HEAD"); got != "refs/heads/main" {
				t.Fatalf("next source HEAD was not followed: %s", got)
			}
			noErr(t, f.manager.SetDefaultBranch(ctx, "project", "topic"))
			f.git(f.source, "symbolic-ref", "HEAD", "refs/heads/release")
			_, err = f.refresh()
			noErr(t, err)
			if got := f.git(f.destinationPath(), "symbolic-ref", "HEAD"); got != "refs/heads/topic" {
				t.Fatalf("owner default branch changed: %s", got)
			}
			f.git(f.destinationPath(), "fsck", "--full")
			records, err := f.store.ImportRefLocksPage(ctx, "", 100)
			noErr(t, err)
			if len(records) != 0 {
				t.Fatalf("settled locks retained evidence: %+v", records)
			}
		})
	}
}

func TestLegacyImportLockRequiresOwnerRecovery(t *testing.T) {
	f := killedPublicationFixture(t, "legacy-head-locked")
	ctx := context.Background()
	path := filepath.Join(f.destinationPath(), "HEAD.lock")
	before, err := os.ReadFile(path)
	noErr(t, err)
	if err := f.service.Reconcile(ctx); problemCode(err) != CodeUnresolved {
		t.Fatalf("legacy state was not unresolved: %v", err)
	}
	after, err := os.ReadFile(path)
	noErr(t, err)
	if string(before) != string(after) {
		t.Fatal("legacy lock was changed without proof")
	}
	records, err := f.store.ImportRefLocksPage(ctx, "", 100)
	noErr(t, err)
	if len(records) != 0 {
		t.Fatal("legacy fixture unexpectedly had durable lock evidence")
	}
	intents, err := f.store.UnresolvedImportIntents(ctx, "project")
	noErr(t, err)
	if len(intents) != 1 || !strings.Contains(intents[0].Reason, "Stop OwnGit and all Git writers") {
		t.Fatalf("owner recovery route is missing: %+v", intents)
	}
	noErr(t, f.service.Close())
	noErr(t, os.Rename(path, filepath.Join(f.root, "preserved-legacy-lock")))
	f.service = &Service{Store: f.store, Repositories: f.manager, Fetch: f.transport.fetch, Clock: func() time.Time { return f.now }}
	if err := f.service.Reconcile(ctx); problemCode(err) != CodeUnresolved {
		t.Fatalf("partial refs should still need acceptance: %v", err)
	}
	_, err = f.service.ResolveUnresolved(ctx, "project")
	noErr(t, err)
	f.localWork("main", "owner push after legacy recovery\n")
	noErr(t, f.manager.SetDefaultBranch(ctx, "project", "release"))
	_, err = f.refresh()
	noErr(t, err)
	f.git(f.destinationPath(), "fsck", "--full")
}

func TestReplacedRecordedLockIsPreserved(t *testing.T) {
	f := killedPublicationFixture(t, "head-locked")
	path := filepath.Join(f.destinationPath(), "HEAD.lock")
	original, err := os.ReadFile(path)
	noErr(t, err)
	noErr(t, os.Rename(path, path+".original"))
	noErr(t, os.WriteFile(path, original, 0600))
	if err := f.service.Reconcile(context.Background()); err == nil {
		t.Fatal("replacement was accepted as owned")
	}
	actual, err := os.ReadFile(path)
	noErr(t, err)
	if string(actual) != string(original) {
		t.Fatal("replacement was changed")
	}
}

func TestRecordedLockFingerprintChangesArePreserved(t *testing.T) {
	for _, change := range []string{"content", "size", "mtime", "file-id", "directory-id", "unknown-filesystem", "current-process"} {
		t.Run(change, func(t *testing.T) {
			f := killedPublicationFixture(t, "head-locked")
			path := filepath.Join(f.destinationPath(), "HEAD.lock")
			records, err := f.store.ImportRefLocksPage(context.Background(), "", 100)
			noErr(t, err)
			if len(records) != 1 {
				t.Fatalf("records=%+v", records)
			}
			record := records[0]
			switch change {
			case "content":
				noErr(t, os.WriteFile(path, []byte("ref: refs/heads/foreign\n"), 0600))
				noErr(t, os.Chtimes(path, time.Unix(0, record.ModTime), time.Unix(0, record.ModTime)))
			case "size":
				noErr(t, os.WriteFile(path, []byte(record.Content+"\n"), 0600))
				noErr(t, os.Chtimes(path, time.Unix(0, record.ModTime), time.Unix(0, record.ModTime)))
			case "mtime":
				noErr(t, os.Chtimes(path, time.Now(), time.Now().Add(time.Hour)))
			case "file-id":
				record.FileID += "changed"
			case "directory-id":
				record.DirectoryID += "changed"
			case "unknown-filesystem":
				record.FileID = ""
				record.DirectoryID = ""
			case "current-process":
				record.SessionID, err = f.service.refLockSession()
				noErr(t, err)
			}
			noErr(t, f.store.SaveImportRefLock(context.Background(), record))
			if err := f.service.Reconcile(context.Background()); err == nil {
				t.Fatal("unproven lock was accepted")
			}
			if _, err := os.Stat(path); err != nil {
				t.Fatalf("unproven lock removed: %v", err)
			}
			// Follow the same safe owner route shown in the diagnostic. Preserve the
			// lock outside the repository, rather than deleting an unknown resource.
			noErr(t, os.Rename(path, filepath.Join(f.root, "preserved-lock")))
			_, err = f.service.ResolveUnresolved(context.Background(), "project")
			noErr(t, err)
			f.localWork("main", "owner push after manual recovery\n")
			noErr(t, f.manager.SetDefaultBranch(context.Background(), "project", "release"))
		})
	}
}

func TestUnreadableWrittenHEADPreservesRecoveryEvidence(t *testing.T) {
	f := killedPublicationFixture(t, "after-head")
	ctx := context.Background()
	_, err := f.service.Prepare(ctx)
	noErr(t, err)
	records, err := f.store.ImportRefLocksPage(ctx, "", 100)
	noErr(t, err)
	if len(records) != 1 {
		t.Fatalf("records=%+v", records)
	}
	repositoryPath := f.destinationPath()
	path := filepath.Join(repositoryPath, "HEAD")
	noErr(t, os.Rename(path, path+".held"))
	noErr(t, os.Mkdir(path, 0700))
	err = f.service.reconcileRecordedRefLock(ctx, "", repositoryPath, records[0])
	if err == nil {
		t.Error("unreadable written HEAD was treated as settled evidence")
	}
	retained, queryErr := f.store.ImportRefLocksPage(ctx, "", 100)
	noErr(t, queryErr)
	if len(retained) != 1 {
		t.Error("unreadable HEAD discarded durable write proof")
	}
	noErr(t, os.Rename(path, filepath.Join(f.root, "preserved-unreadable-head")))
	noErr(t, os.Rename(path+".held", path))
	noErr(t, f.service.Reconcile(ctx))
	f.git(f.source, "symbolic-ref", "HEAD", "refs/heads/main")
	_, err = f.refresh()
	noErr(t, err)
	if got := f.git(f.destinationPath(), "symbolic-ref", "HEAD"); got != "refs/heads/main" {
		t.Errorf("recovered HEAD ownership was lost after the read error: %s", got)
	}
}

func TestLiveRecordedLockIsPreserved(t *testing.T) {
	f := newFixture(t)
	f.commit("initial", "initial\n")
	f.mustImport(ImportInput{})
	f.git(f.source, "branch", "release")
	f.git(f.source, "symbolic-ref", "HEAD", "refs/heads/release")
	f.service.beforeHEADRename = func() {
		records, err := f.store.ImportRefLocksPage(context.Background(), "", 100)
		noErr(t, err)
		if len(records) != 1 {
			t.Fatalf("records=%+v", records)
		}
		// Reconcile excludes this live run even with a record from another session.
		record := records[0]
		record.SessionID = strings.Repeat("f", 32)
		noErr(t, f.store.SaveImportRefLock(context.Background(), record))
		noErr(t, f.service.reconcileRecordedRefLocks(context.Background(), ""))
		if _, err := os.Stat(filepath.Join(f.destinationPath(), "HEAD.lock")); err != nil {
			t.Fatalf("live lock removed: %v", err)
		}
	}
	_, err := f.refresh()
	noErr(t, err)
}
