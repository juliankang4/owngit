package importsync

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"

	"owngit/internal/repository"
	"owngit/internal/state"
)

func TestInitialDirectoryCreationProblemOnlyTreatsCreationCollisionAsTaken(t *testing.T) {
	path := filepath.Join(t.TempDir(), "existing")
	noErr(t, os.Mkdir(path, 0o700))
	collisionErr := state.MkdirPrivate(path)
	collisionIssue, collision := initialDirectoryCreationProblem(collisionErr)
	require(t, collision.Code == CodeRepositoryTaken && collisionIssue == "directory name was already present",
		"collision issue=%q problem=%+v", collisionIssue, collision)

	cleanupErr := errors.Join(errors.New("new folder inherited a permit entry"), fmt.Errorf("remove synthetic staging: %w", syscall.ENOTEMPTY))
	failureIssue, failure := initialDirectoryCreationProblem(cleanupErr)
	require(t, failure.Code == CodeRepositoryCreateFailed && failureIssue == initialDestinationCreateIssue &&
		strings.Contains(failure.Error(), "inherited a permit") &&
		strings.Contains(failure.Error(), "directory not empty"),
		"private creation issue=%q problem=%+v", failureIssue, failure)
}

func TestInitialImportRefusesExistingDestination(t *testing.T) {
	f := newFixture(t)
	first := f.commit("first snapshot", "first bytes\n")
	f.mustImport(ImportInput{})
	beforeRefs := f.destinationRefs()
	beforeSource, exists, err := f.service.Store.ImportSource(context.Background(), "project")
	require(t, err == nil && exists, "initial source exists=%v err=%v", exists, err)
	f.commit("later source snapshot", "later bytes\n")
	beforeUpstream := f.sourceRefs()
	result, err := f.importProject(ImportInput{})
	require(t, err != nil,
		"initial import reused an existing destination: status=%s old_main=%s actual_main=%s", result.Run.Status, first, f.destinationRefs()["refs/heads/main"])
	require(t, problemCode(err) == CodeRepositoryTaken, "collision code=%s err=%v", problemCode(err), err)
	after := f.destinationRefs()
	require(t, reflect.DeepEqual(beforeRefs, after),
		"initial collision changed destination refs: before=%v after=%v", beforeRefs, after)
	after = f.sourceRefs()
	require(t, reflect.DeepEqual(beforeUpstream, after),
		"initial collision changed upstream refs: before=%v after=%v", beforeUpstream, after)
	afterSource, exists, err := f.service.Store.ImportSource(context.Background(), "project")
	require(t, err == nil && exists && reflect.DeepEqual(beforeSource, afterSource),
		"initial collision changed existing source authority: before=%+v after=%+v exists=%v err=%v", beforeSource, afterSource, exists, err)
}

func TestInitialRepositoryIsNotDiscoverableBeforeRefsReady(t *testing.T) {
	f := newFixture(t)
	wanted := f.commit("initial snapshot", "initial bytes\n")
	originalClock := f.service.Clock
	exposed := make(chan string, 1)
	f.service.Clock = func() time.Time {
		path, _, exists, err := f.manager.ExistingPath(context.Background(), "project")
		if err == nil && exists {
			actual := f.gitMaybe(path, "--git-dir", ".", "rev-parse", "--verify", "refs/heads/main")
			if actual != wanted {
				select {
				case exposed <- fmt.Sprintf("visible main=%q wanted=%s", actual, wanted):
				default:
				}
			}
		}
		repos, listErr := f.store.Repositories(context.Background())
		if listErr == nil {
			for _, repo := range repos {
				if repo.ID != "project" {
					continue
				}
				actual := ""
				if err == nil && exists {
					actual = f.gitMaybe(path, "--git-dir", ".", "rev-parse", "--verify", "refs/heads/main")
				}
				if actual != wanted {
					select {
					case exposed <- fmt.Sprintf("repository list visible main=%q wanted=%s", actual, wanted):
					default:
					}
				}
			}
		}
		return originalClock()
	}
	result := f.mustImport(ImportInput{})
	select {
	case detail := <-exposed:
		t.Fatalf("initial repository was discoverable before ready refs: %s final_status=%s", detail, result.Run.Status)
	default:
	}
	got := f.destinationRefs()["refs/heads/main"]
	require(t, got == wanted, "completed initial refs=%s want %s", got, wanted)
	repos, err := f.store.Repositories(context.Background())
	noErr(t, err)
	listed := false
	for _, repo := range repos {
		if repo.ID == "project" {
			listed = true
		}
	}
	require(t, listed, "completed initial repository was not listed")
}

func TestInitialImportRefusesDestinationBeforeConfiguration(t *testing.T) {
	f := newFixture(t)
	f.commit("one", "one\n")
	final := repositoryFinalPath(t, f, "project")
	noErr(t, os.Mkdir(final, 0o700))
	noErr(t, os.WriteFile(filepath.Join(final, "keep.txt"), []byte("foreign"), 0o600))
	_, err := f.importProject(ImportInput{})
	require(t, problemCode(err) == CodeRepositoryTaken, "directory collision err=%v", err)
	_, exists, err := f.store.ImportSource(context.Background(), "project")
	require(t, err == nil && !exists, "configuration changed before refusal exists=%v err=%v", exists, err)
	content, err := os.ReadFile(filepath.Join(final, "keep.txt"))
	require(t, err == nil && string(content) == "foreign", "foreign directory changed content=%q err=%v", content, err)

	f = newFixture(t)
	f.commit("one", "one\n")
	noErr(t, f.store.AddRepository(context.Background(),
		state.Repository{ID: "project", Name: "project", CreatedAt: f.now}))
	_, err = f.importProject(ImportInput{})
	require(t, problemCode(err) == CodeRepositoryTaken, "row collision err=%v", err)
	_, exists, err = f.store.ImportSource(context.Background(), "project")
	require(t, err == nil && !exists, "row collision configured a source exists=%v err=%v", exists, err)
	_, err = os.Lstat(repositoryFinalPath(t, f, "project"))
	require(t, os.IsNotExist(err), "row collision created a directory err=%v", err)
}

func TestInitialRenameRefusesDestinationThatAppears(t *testing.T) {
	f := newFixture(t)
	wanted := f.commit("one", "one\n")
	final := repositoryFinalPath(t, f, "project")
	f.service.beforeInitialRename = func() error {
		f.service.beforeInitialRename = nil
		if err := os.Mkdir(final, 0o700); err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(final, "keep.txt"), []byte("foreign"), 0o600)
	}
	_, err := f.importProject(ImportInput{})
	require(t, problemCode(err) == CodeRepositoryTaken, "rename collision err=%v", err)
	content, err := os.ReadFile(filepath.Join(final, "keep.txt"))
	require(t, err == nil && string(content) == "foreign", "foreign directory changed content=%q err=%v", content, err)
	_, exists, err := f.store.Repository(context.Background(), "project")
	require(t, err == nil && !exists, "rename collision recorded a repository exists=%v err=%v", exists, err)
	dirs := unpublishedInitialDirectories(t, f)
	require(t, len(dirs) == 1, "unpublished directories=%v", dirs)
	got := f.gitMaybe(dirs[0], "--git-dir", ".", "rev-parse", "--verify", "refs/heads/main")
	require(t, got == wanted, "unpublished main=%s want %s", got, wanted)
}

func TestInitialRepositoryStaysHiddenUntilRename(t *testing.T) {
	f := newFixture(t)
	wanted := f.commit("one", "one\n")
	saw := false
	f.service.beforeInitialRename = func() error {
		saw = true
		if _, _, exists, err := f.manager.ExistingPath(context.Background(), "project"); err != nil || exists {
			t.Errorf("destination visible before rename exists=%v err=%v", exists, err)
		}
		repos, err := f.store.Repositories(context.Background())
		if err != nil {
			t.Error(err)
		}
		for _, repo := range repos {
			if repo.ID == "project" {
				t.Error("repository listed before rename")
			}
		}
		dirs := unpublishedInitialDirectories(t, f)
		if len(dirs) != 1 {
			t.Errorf("unpublished directories=%v", dirs)
			return nil
		}
		if got := f.gitMaybe(dirs[0], "--git-dir", ".", "rev-parse", "--verify", "refs/heads/main"); got != wanted {
			t.Errorf("unpublished main=%s want %s", got, wanted)
		}
		return nil
	}
	result := f.mustImport(ImportInput{})
	require(t, saw, "rename boundary was not observed")
	got := f.destinationRefs()["refs/heads/main"]
	require(t, got == wanted && result.Run.Status == state.ImportRunComplete,
		"completed main=%s status=%s", got, result.Run.Status)
}

func TestInitialCrashBeforeRenamePreservesDirectoryUntilReconcile(t *testing.T) {
	f := newFixture(t)
	wanted := f.commit("one", "one\n")
	f.service.beforeInitialRename = func() error {
		return errors.New("synthetic crash before rename")
	}
	_, err := f.importProject(ImportInput{})
	require(t, err != nil && problemCode(err) == CodeUnresolved, "crash before rename err=%v", err)
	_, _, exists, err := f.manager.ExistingPath(context.Background(), "project")
	require(t, err == nil && !exists, "crashed import was discoverable exists=%v err=%v", exists, err)
	dirs := unpublishedInitialDirectories(t, f)
	require(t, len(dirs) == 1, "unpublished directories=%v", dirs)
	got := f.gitMaybe(dirs[0], "--git-dir", ".", "rev-parse", "--verify", "refs/heads/main")
	require(t, got == wanted, "preserved main=%s want %s", got, wanted)
	noErr(t, f.service.Reconcile(context.Background()))
	got = f.destinationRefs()["refs/heads/main"]
	require(t, got == wanted, "reconciled main=%s want %s", got, wanted)
	after := unpublishedInitialDirectories(t, f)
	require(t, len(after) == 0, "published directory remained unpublished: %v", after)
}

func TestInitialCrashAfterRenameBeforeRowIsUnresolved(t *testing.T) {
	f := newFixture(t)
	wanted := f.commit("one", "one\n")
	f.service.beforeInitialRepositoryRecord = func() error {
		return errors.New("synthetic row write crash")
	}
	_, err := f.importProject(ImportInput{})
	require(t, err != nil && problemCode(err) == CodeUnresolved && strings.Contains(err.Error(), "remains at") &&
		strings.Contains(err.Error(), "for owner recovery"), "row crash err=%v", err)
	_, _, exists, err := f.manager.ExistingPath(context.Background(), "project")
	require(t, err == nil && !exists, "row crash was discoverable exists=%v err=%v", exists, err)
	final := repositoryFinalPath(t, f, "project")
	got := f.gitMaybe(final, "--git-dir", ".", "rev-parse", "--verify", "refs/heads/main")
	require(t, got == wanted, "landed main=%s want %s", got, wanted)
	noErr(t, f.service.Reconcile(context.Background()))
	got = f.destinationRefs()["refs/heads/main"]
	require(t, got == wanted, "recovered main=%s want %s", got, wanted)
}

func TestIncompleteInitialDirectoryIsRemovedOnlyWithProof(t *testing.T) {
	f := newFixture(t)
	f.commit("one", "one\n")
	f.service.beforeInitialPublication = func() error {
		return errors.New("synthetic crash before publication")
	}
	_, err := f.importProject(ImportInput{})
	require(t, err != nil && problemCode(err) == CodeUnresolved, "incomplete crash err=%v", err)
	owned := unpublishedInitialDirectories(t, f)
	require(t, len(owned) == 1, "owned directories=%v", owned)
	root := f.manager.RepositoryRoot()
	lookAlike := filepath.Join(root, ".owngit-create-"+strings.Repeat("ab", 16))
	noErr(t, os.Mkdir(lookAlike, 0o700))
	noErr(t, os.WriteFile(filepath.Join(lookAlike, "keep.txt"), []byte("foreign"), 0o600))
	noErr(t, f.service.Reconcile(context.Background()))
	_, err = os.Lstat(owned[0])
	require(t, os.IsNotExist(err), "proven incomplete directory remained err=%v", err)
	content, err := os.ReadFile(filepath.Join(lookAlike, "keep.txt"))
	require(t, err == nil && string(content) == "foreign", "unowned look-alike changed content=%q err=%v", content, err)
	_, _, exists, err := f.manager.ExistingPath(context.Background(), "project")
	require(t, err == nil && !exists, "incomplete import became discoverable exists=%v err=%v", exists, err)
}

func TestUnownedInitialLookAlikeIsPreserved(t *testing.T) {
	f := newFixture(t)
	root := f.manager.RepositoryRoot()
	lookAlike := filepath.Join(root, ".owngit-create-"+strings.Repeat("cd", 16))
	noErr(t, os.Mkdir(lookAlike, 0o700))
	noErr(t, os.WriteFile(filepath.Join(lookAlike, "keep.txt"), []byte("foreign"), 0o600))
	noErr(t, f.service.Reconcile(context.Background()))
	content, err := os.ReadFile(filepath.Join(lookAlike, "keep.txt"))
	require(t, err == nil && string(content) == "foreign", "look-alike changed content=%q err=%v", content, err)
	row, exists, err := f.store.ImportInitialDestination(context.Background(), filepath.Base(lookAlike))
	require(t, err == nil && exists && row.State == state.ImportInitialUnknown,
		"unknown row=%+v exists=%v err=%v", row, exists, err)
	noErr(t, f.service.Reconcile(context.Background()))
	_, err = os.Lstat(filepath.Join(lookAlike, "keep.txt"))
	noErr(t, err, "second reconcile removed look-alike")
}

func TestInitialImportProvesHEADOwnershipForFirstRefresh(t *testing.T) {
	f := newFixture(t)
	main := f.commit("main", "main\n")
	f.git(f.source, "branch", "dev")
	result := f.mustImport(ImportInput{})
	intent, exists, err := f.store.CompletedImportIntentForRun(context.Background(), result.Run.ID)
	require(t, err == nil && exists && intent.HeadOwned,
		"initial ownership intent=%+v exists=%v err=%v", intent, exists, err)
	symref, oid, err := f.manager.ReadHead(context.Background(), f.destinationPath())
	require(t, err == nil && symref == "refs/heads/main" && oid == main,
		"initial HEAD symref=%q oid=%q err=%v", symref, oid, err)
	require(t, intentProvesHEADOwnership(intent, headIdentity{kind: headSymbolic, target: symref, oid: oid}),
		"initial receipt did not prove HEAD ownership: %+v", intent)
	f.git(f.source, "checkout", "--quiet", "dev")
	_, err = f.refresh()
	noErr(t, err)
	eq(t, "first refresh did not follow owned HEAD",
		f.git(f.destinationPath(), "symbolic-ref", "--no-recurse", "HEAD"), "refs/heads/dev")
}

func TestInitialImportWritesDetachedHEAD(t *testing.T) {
	f := newFixture(t)
	oid := f.commit("detached", "detached bytes\n")
	f.git(f.source, "checkout", "--quiet", "--detach", oid)
	result := f.mustImport(ImportInput{})
	symref, resolved, err := f.manager.ReadHead(context.Background(), f.destinationPath())
	require(t, err == nil && symref == "" && resolved == oid,
		"detached HEAD symref=%q oid=%q err=%v", symref, resolved, err)
	intent, exists, err := f.store.CompletedImportIntentForRun(context.Background(), result.Run.ID)
	require(t, err == nil && exists && intent.HeadOwned,
		"detached ownership intent=%+v exists=%v err=%v", intent, exists, err)
	require(t, intentProvesHEADOwnership(intent, headIdentity{kind: headDetached, oid: oid}),
		"detached receipt did not prove HEAD ownership: %+v", intent)
}

func TestInitialIntentIsNotJudgedAgainstAnotherDirectory(t *testing.T) {
	f := newFixture(t)
	wanted := f.commit("owned snapshot", "owned bytes\n")
	var intentID string
	f.service.beforeInitialRename = func() error {
		intent, err := f.store.PendingImportIntents(context.Background(), "project")
		if err != nil || len(intent) != 1 || intent[0].Status != state.ImportIntentApplied {
			t.Errorf("applied intent before rename: %+v err=%v", intent, err)
		} else {
			intentID = intent[0].ID
		}
		return errors.New("synthetic crash before rename")
	}
	_, err := f.importProject(ImportInput{})
	require(t, err != nil && problemCode(err) == CodeUnresolved, "crash before rename err=%v", err)
	require(t, intentID != "", "applied intent was not captured")
	_, err = f.manager.CreateWithOptions(context.Background(), "project", "foreign empty", repository.CreateOptions{ObjectFormat: "sha1"})
	noErr(t, err)
	foreign := repositoryFinalPath(t, f, "project")
	noErr(t, f.service.Reconcile(context.Background()))
	stored, exists, err := f.store.ImportIntent(context.Background(), intentID)
	require(t, err == nil && exists && stored.ReceiptJSON != "" && stored.Status != state.ImportIntentNotApplied &&
		stored.Status != state.ImportIntentComplete,
		"initial intent was judged against another directory: status=%s exists=%v receipt=%q err=%v", stored.Status, exists, stored.ReceiptJSON, err)
	dirs := unpublishedInitialDirectories(t, f)
	require(t, len(dirs) == 1, "unpublished directories=%v", dirs)
	got := f.gitMaybe(dirs[0], "--git-dir", ".", "rev-parse", "--verify", "refs/heads/main")
	require(t, got == wanted, "owned directory main=%s want %s", got, wanted)
	got = f.gitMaybe(foreign, "--git-dir", ".", "rev-parse", "--verify", "refs/heads/main")
	require(t, got == "", "foreign directory gained main=%s", got)
	noErr(t, f.store.Exec(context.Background(), `DELETE FROM repositories WHERE id=?`, "project"))
	noErr(t, os.RemoveAll(foreign))
	// This fixture removed a registered lifetime without Manager.Delete.
	lock := f.manager.Locks.For("project")
	lock.Lock()
	lock.AdvanceIncarnation()
	lock.UnlockWithoutRefChanges()
	noErr(t, f.service.Reconcile(context.Background()))
	got = f.destinationRefs()["refs/heads/main"]
	require(t, got == wanted, "reconciled owned directory main=%s want %s", got, wanted)
}

func TestSecondImportDoesNotJudgeFirstInitialIntent(t *testing.T) {
	f := newFixture(t)
	wanted := f.commit("owned snapshot", "owned bytes\n")
	f.service.beforeInitialRename = func() error {
		return errors.New("synthetic crash before rename")
	}
	_, err := f.importProject(ImportInput{})
	require(t, err != nil && problemCode(err) == CodeUnresolved, "crash before rename err=%v", err)
	first, err := f.store.PendingImportIntents(context.Background(), "project")
	require(t, err == nil && len(first) == 1 && first[0].Status == state.ImportIntentApplied,
		"first intent=%+v err=%v", first, err)
	receipt := first[0].ReceiptJSON
	f.service.beforeInitialRename = nil
	f.commit("second snapshot", "second bytes\n")
	_, err = f.importProject(ImportInput{})
	require(t, err == nil, "second import err=%v", err)
	stored, exists, err := f.store.ImportIntent(context.Background(), first[0].ID)
	require(t, err == nil && exists && stored.Status == state.ImportIntentApplied && stored.ReceiptJSON == receipt,
		"second import judged the first intent: status=%s exists=%v err=%v", stored.Status, exists, err)
	owned := false
	for _, dir := range unpublishedInitialDirectories(t, f) {
		if f.gitMaybe(dir, "--git-dir", ".", "rev-parse", "--verify", "refs/heads/main") == wanted {
			owned = true
		}
	}
	require(t, owned, "first unpublished directory was not preserved")
}

func TestImportLockedCheckRefusesRepositoryCreatedBeforeConfiguration(t *testing.T) {
	f := newFixture(t)
	f.commit("incoming", "incoming bytes\n")
	var before state.ImportSource
	f.service.beforeImportConfiguration = func() error {
		f.service.beforeImportConfiguration = nil
		if _, err := f.manager.CreateWithOptions(context.Background(), "project", "existing", repository.CreateOptions{ObjectFormat: "sha1"}); err != nil {
			return err
		}
		if _, err := f.service.ConfigureSource(context.Background(), ConfigureInput{
			RepositoryID: "project", URL: "https://example.invalid/existing.git",
		}); err != nil {
			return err
		}
		if err := f.service.SetCredentials(context.Background(), "project", &Credentials{Username: "owner", Password: "secret"}); err != nil {
			return err
		}
		source, exists, err := f.store.ImportSource(context.Background(), "project")
		if err != nil || !exists || source.CredentialGeneration == "" {
			return fmt.Errorf("existing binding exists=%v err=%v source=%+v", exists, err, source)
		}
		before = source
		return nil
	}
	_, err := f.importProject(ImportInput{Credentials: &Credentials{Username: "intruder", Password: "other"}})
	require(t, problemCode(err) == CodeRepositoryTaken, "late configuration collision err=%v", err)
	after, exists, err := f.store.ImportSource(context.Background(), "project")
	require(t, err == nil && exists && after.URL == before.URL && after.SourceGeneration == before.SourceGeneration &&
		after.AuthorityRevision == before.AuthorityRevision && after.CredentialGeneration == before.CredentialGeneration,
		"locked refusal changed source binding: before=%+v after=%+v exists=%v err=%v", before, after, exists, err)
	got := f.gitMaybe(repositoryFinalPath(t, f, "project"), "--git-dir", ".", "rev-parse", "--verify", "refs/heads/main")
	require(t, got == "", "existing destination refs changed: %s", got)
	runs, _, err := f.store.ImportRuns(context.Background(), "project", 10)
	require(t, err == nil && len(runs) == 0, "locked refusal still started an import run: %+v err=%v", runs, err)
}

func TestImportAdmissionDoesNotRestoreBindingChangedBeforeExecute(t *testing.T) {
	f := newFixture(t)
	f.commit("incoming", "incoming bytes\n")
	var later state.ImportSource
	f.service.afterImportBinding = func() error {
		f.service.afterImportBinding = nil
		if _, err := f.manager.CreateWithOptions(context.Background(), "project", "later creator", repository.CreateOptions{ObjectFormat: "sha1"}); err != nil {
			return err
		}
		source, err := f.service.ConfigureSource(context.Background(), ConfigureInput{
			RepositoryID: "project", URL: "https://example.invalid/later.git",
		})
		later = source
		return err
	}
	_, err := f.importProject(ImportInput{})
	// Admission sees the row differ from the binding bindNewImport wrote, before
	// fetch or collision restore. CodeSuperseded is that stop. CodeRepositoryTaken
	// would mean the run continued and treated the later row as its own binding.
	require(t, problemCode(err) == CodeSuperseded, "admission after a later configuration err=%v", err)
	after, exists, readErr := f.store.ImportSource(context.Background(), "project")
	require(t, readErr == nil && exists && after.URL == later.URL && after.AuthorityRevision == later.AuthorityRevision,
		"later configuration did not survive admission: url=%q authority=%d exists=%v want url=%q authority=%d err=%v", after.URL, after.AuthorityRevision, exists, later.URL, later.AuthorityRevision, readErr)
}

func TestInitialCollisionPreservesLaterSourceConfiguration(t *testing.T) {
	f := newFixture(t)
	f.commit("incoming", "incoming bytes\n")
	var later state.ImportSource
	f.service.beforeInitialDestinationCheck = func() error {
		f.service.beforeInitialDestinationCheck = nil
		if _, err := f.manager.CreateWithOptions(context.Background(), "project", "later creator", repository.CreateOptions{ObjectFormat: "sha1"}); err != nil {
			return err
		}
		source, err := f.service.ConfigureSource(context.Background(), ConfigureInput{
			RepositoryID: "project", URL: "https://example.invalid/later.git",
		})
		later = source
		return err
	}
	_, err := f.importProject(ImportInput{})
	require(t, problemCode(err) == CodeRepositoryTaken, "collision after later configuration err=%v", err)
	after, exists, err := f.store.ImportSource(context.Background(), "project")
	require(t, err == nil && exists && after.URL == later.URL && after.AuthorityRevision == later.AuthorityRevision,
		"later configuration did not survive collision: url=%q authority=%d exists=%v want url=%q authority=%d err=%v", after.URL, after.AuthorityRevision, exists, later.URL, later.AuthorityRevision, err)
}

func TestInitialCollisionAfterFetchRestoresSourceBinding(t *testing.T) {
	f := newFixture(t)
	f.commit("incoming", "incoming bytes\n")
	final := repositoryFinalPath(t, f, "project")
	f.service.beforeInitialRename = func() error {
		f.service.beforeInitialRename = nil
		if err := os.Mkdir(final, 0o700); err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(final, "keep.txt"), []byte("foreign"), 0o600)
	}
	_, err := f.importProject(ImportInput{Credentials: &Credentials{Username: "intruder", Password: "other"}})
	require(t, problemCode(err) == CodeRepositoryTaken, "collision after fetch err=%v", err)
	_, exists, err := f.store.ImportSource(context.Background(), "project")
	require(t, err == nil && !exists, "source binding was not restored exists=%v err=%v", exists, err)
	_, exists, err = f.store.LoadImportCredentials(context.Background(), "project")
	require(t, err == nil && !exists, "credential binding was not restored exists=%v err=%v", exists, err)
	content, err := os.ReadFile(filepath.Join(final, "keep.txt"))
	require(t, err == nil && string(content) == "foreign", "foreign directory changed content=%q err=%v", content, err)
}

func repositoryFinalPath(t *testing.T, f *fixture, id string) string {
	t.Helper()
	path, err := f.manager.Path(id)
	noErr(t, err)
	return path
}

func unpublishedInitialDirectories(t *testing.T, f *fixture) []string {
	t.Helper()
	root := f.manager.RepositoryRoot()
	entries, err := os.ReadDir(root)
	noErr(t, err)
	var paths []string
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), unpublishedDirectoryPrefix) {
			paths = append(paths, filepath.Join(root, entry.Name()))
		}
	}
	return paths
}
