package importsync

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"owngit/internal/repository"
	"owngit/internal/state"
)

func TestInitialImportRefusesExistingDestination(t *testing.T) {
	f := newFixture(t)
	first := f.commit("first snapshot", "first bytes\n")
	f.mustImport(ImportInput{})
	beforeRefs := f.destinationRefs()
	beforeSource, exists, err := f.service.Store.ImportSource(context.Background(), "project")
	if err != nil || !exists {
		t.Fatalf("initial source exists=%v err=%v", exists, err)
	}
	f.commit("later source snapshot", "later bytes\n")
	beforeUpstream := f.sourceRefs()
	result, err := f.importProject(ImportInput{})
	if err == nil {
		t.Fatalf("initial import reused an existing destination: status=%s old_main=%s actual_main=%s", result.Run.Status, first, f.destinationRefs()["refs/heads/main"])
	}
	if problemCode(err) != CodeRepositoryTaken {
		t.Fatalf("collision code=%s err=%v", problemCode(err), err)
	}
	if after := f.destinationRefs(); !reflect.DeepEqual(beforeRefs, after) {
		t.Fatalf("initial collision changed destination refs: before=%v after=%v", beforeRefs, after)
	}
	if after := f.sourceRefs(); !reflect.DeepEqual(beforeUpstream, after) {
		t.Fatalf("initial collision changed upstream refs: before=%v after=%v", beforeUpstream, after)
	}
	afterSource, exists, err := f.service.Store.ImportSource(context.Background(), "project")
	if err != nil || !exists || !reflect.DeepEqual(beforeSource, afterSource) {
		t.Fatalf("initial collision changed existing source authority: before=%+v after=%+v exists=%v err=%v", beforeSource, afterSource, exists, err)
	}
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
	if got := f.destinationRefs()["refs/heads/main"]; got != wanted {
		t.Fatalf("completed initial refs=%s want %s", got, wanted)
	}
	repos, err := f.store.Repositories(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	listed := false
	for _, repo := range repos {
		if repo.ID == "project" {
			listed = true
		}
	}
	if !listed {
		t.Fatal("completed initial repository was not listed")
	}
}

func TestInitialImportRefusesDestinationBeforeConfiguration(t *testing.T) {
	f := newFixture(t)
	f.commit("one", "one\n")
	final := repositoryFinalPath(t, f, "project")
	if err := os.Mkdir(final, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(final, "keep.txt"), []byte("foreign"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := f.importProject(ImportInput{}); problemCode(err) != CodeRepositoryTaken {
		t.Fatalf("directory collision err=%v", err)
	}
	if _, exists, err := f.store.ImportSource(context.Background(), "project"); err != nil || exists {
		t.Fatalf("configuration changed before refusal exists=%v err=%v", exists, err)
	}
	content, err := os.ReadFile(filepath.Join(final, "keep.txt"))
	if err != nil || string(content) != "foreign" {
		t.Fatalf("foreign directory changed content=%q err=%v", content, err)
	}

	f = newFixture(t)
	f.commit("one", "one\n")
	if err := f.store.AddRepository(context.Background(), state.Repository{ID: "project", Name: "project", CreatedAt: f.now}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.importProject(ImportInput{}); problemCode(err) != CodeRepositoryTaken {
		t.Fatalf("row collision err=%v", err)
	}
	if _, exists, err := f.store.ImportSource(context.Background(), "project"); err != nil || exists {
		t.Fatalf("row collision configured a source exists=%v err=%v", exists, err)
	}
	if _, err := os.Lstat(repositoryFinalPath(t, f, "project")); !os.IsNotExist(err) {
		t.Fatalf("row collision created a directory err=%v", err)
	}
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
	if _, err := f.importProject(ImportInput{}); problemCode(err) != CodeRepositoryTaken {
		t.Fatalf("rename collision err=%v", err)
	}
	content, err := os.ReadFile(filepath.Join(final, "keep.txt"))
	if err != nil || string(content) != "foreign" {
		t.Fatalf("foreign directory changed content=%q err=%v", content, err)
	}
	if _, exists, err := f.store.Repository(context.Background(), "project"); err != nil || exists {
		t.Fatalf("rename collision recorded a repository exists=%v err=%v", exists, err)
	}
	dirs := unpublishedInitialDirectories(t, f)
	if len(dirs) != 1 {
		t.Fatalf("unpublished directories=%v", dirs)
	}
	if got := f.gitMaybe(dirs[0], "--git-dir", ".", "rev-parse", "--verify", "refs/heads/main"); got != wanted {
		t.Fatalf("unpublished main=%s want %s", got, wanted)
	}
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
	if !saw {
		t.Fatal("rename boundary was not observed")
	}
	if got := f.destinationRefs()["refs/heads/main"]; got != wanted || result.Run.Status != state.ImportRunComplete {
		t.Fatalf("completed main=%s status=%s", got, result.Run.Status)
	}
}

func TestInitialCrashBeforeRenamePreservesDirectoryUntilReconcile(t *testing.T) {
	f := newFixture(t)
	wanted := f.commit("one", "one\n")
	f.service.beforeInitialRename = func() error {
		return errors.New("synthetic crash before rename")
	}
	if _, err := f.importProject(ImportInput{}); err == nil || problemCode(err) != CodeUnresolved {
		t.Fatalf("crash before rename err=%v", err)
	}
	if _, _, exists, err := f.manager.ExistingPath(context.Background(), "project"); err != nil || exists {
		t.Fatalf("crashed import was discoverable exists=%v err=%v", exists, err)
	}
	dirs := unpublishedInitialDirectories(t, f)
	if len(dirs) != 1 {
		t.Fatalf("unpublished directories=%v", dirs)
	}
	if got := f.gitMaybe(dirs[0], "--git-dir", ".", "rev-parse", "--verify", "refs/heads/main"); got != wanted {
		t.Fatalf("preserved main=%s want %s", got, wanted)
	}
	if err := f.service.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := f.destinationRefs()["refs/heads/main"]; got != wanted {
		t.Fatalf("reconciled main=%s want %s", got, wanted)
	}
	if after := unpublishedInitialDirectories(t, f); len(after) != 0 {
		t.Fatalf("published directory remained unpublished: %v", after)
	}
}

func TestInitialCrashAfterRenameBeforeRowIsUnresolved(t *testing.T) {
	f := newFixture(t)
	wanted := f.commit("one", "one\n")
	f.service.beforeInitialRepositoryRecord = func() error {
		return errors.New("synthetic row write crash")
	}
	_, err := f.importProject(ImportInput{})
	if err == nil || problemCode(err) != CodeUnresolved || !strings.Contains(err.Error(), "remains at") || !strings.Contains(err.Error(), "for owner recovery") {
		t.Fatalf("row crash err=%v", err)
	}
	if _, _, exists, err := f.manager.ExistingPath(context.Background(), "project"); err != nil || exists {
		t.Fatalf("row crash was discoverable exists=%v err=%v", exists, err)
	}
	final := repositoryFinalPath(t, f, "project")
	if got := f.gitMaybe(final, "--git-dir", ".", "rev-parse", "--verify", "refs/heads/main"); got != wanted {
		t.Fatalf("landed main=%s want %s", got, wanted)
	}
	if err := f.service.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := f.destinationRefs()["refs/heads/main"]; got != wanted {
		t.Fatalf("recovered main=%s want %s", got, wanted)
	}
}

func TestIncompleteInitialDirectoryIsRemovedOnlyWithProof(t *testing.T) {
	f := newFixture(t)
	f.commit("one", "one\n")
	f.service.beforeInitialPublication = func() error {
		return errors.New("synthetic crash before publication")
	}
	if _, err := f.importProject(ImportInput{}); err == nil || problemCode(err) != CodeUnresolved {
		t.Fatalf("incomplete crash err=%v", err)
	}
	owned := unpublishedInitialDirectories(t, f)
	if len(owned) != 1 {
		t.Fatalf("owned directories=%v", owned)
	}
	root := f.manager.RepositoryRoot()
	lookAlike := filepath.Join(root, ".owngit-create-"+strings.Repeat("ab", 16))
	if err := os.Mkdir(lookAlike, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(lookAlike, "keep.txt"), []byte("foreign"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := f.service.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(owned[0]); !os.IsNotExist(err) {
		t.Fatalf("proven incomplete directory remained err=%v", err)
	}
	content, err := os.ReadFile(filepath.Join(lookAlike, "keep.txt"))
	if err != nil || string(content) != "foreign" {
		t.Fatalf("unowned look-alike changed content=%q err=%v", content, err)
	}
	if _, _, exists, err := f.manager.ExistingPath(context.Background(), "project"); err != nil || exists {
		t.Fatalf("incomplete import became discoverable exists=%v err=%v", exists, err)
	}
}

func TestUnownedInitialLookAlikeIsPreserved(t *testing.T) {
	f := newFixture(t)
	root := f.manager.RepositoryRoot()
	lookAlike := filepath.Join(root, ".owngit-create-"+strings.Repeat("cd", 16))
	if err := os.Mkdir(lookAlike, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(lookAlike, "keep.txt"), []byte("foreign"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := f.service.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(filepath.Join(lookAlike, "keep.txt"))
	if err != nil || string(content) != "foreign" {
		t.Fatalf("look-alike changed content=%q err=%v", content, err)
	}
	row, exists, err := f.store.ImportInitialDestination(context.Background(), filepath.Base(lookAlike))
	if err != nil || !exists || row.State != state.ImportInitialUnknown {
		t.Fatalf("unknown row=%+v exists=%v err=%v", row, exists, err)
	}
	if err := f.service.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(lookAlike, "keep.txt")); err != nil {
		t.Fatalf("second reconcile removed look-alike: %v", err)
	}
}

func TestInitialImportProvesHEADOwnershipForFirstRefresh(t *testing.T) {
	f := newFixture(t)
	main := f.commit("main", "main\n")
	f.git(f.source, "branch", "dev")
	result := f.mustImport(ImportInput{})
	intent, exists, err := f.store.CompletedImportIntentForRun(context.Background(), result.Run.ID)
	if err != nil || !exists || !intent.HeadOwned {
		t.Fatalf("initial ownership intent=%+v exists=%v err=%v", intent, exists, err)
	}
	symref, oid, err := f.manager.ReadHead(context.Background(), f.destinationPath())
	if err != nil || symref != "refs/heads/main" || oid != main {
		t.Fatalf("initial HEAD symref=%q oid=%q err=%v", symref, oid, err)
	}
	if !intentProvesHEADOwnership(intent, headIdentity{kind: headSymbolic, target: symref, oid: oid}) {
		t.Fatalf("initial receipt did not prove HEAD ownership: %+v", intent)
	}
	f.git(f.source, "checkout", "--quiet", "dev")
	if _, err := f.refresh(); err != nil {
		t.Fatal(err)
	}
	if got := f.git(f.destinationPath(), "symbolic-ref", "--no-recurse", "HEAD"); got != "refs/heads/dev" {
		t.Fatalf("first refresh did not follow owned HEAD: %s", got)
	}
}

func TestInitialImportWritesDetachedHEAD(t *testing.T) {
	f := newFixture(t)
	oid := f.commit("detached", "detached bytes\n")
	f.git(f.source, "checkout", "--quiet", "--detach", oid)
	result := f.mustImport(ImportInput{})
	symref, resolved, err := f.manager.ReadHead(context.Background(), f.destinationPath())
	if err != nil || symref != "" || resolved != oid {
		t.Fatalf("detached HEAD symref=%q oid=%q err=%v", symref, resolved, err)
	}
	intent, exists, err := f.store.CompletedImportIntentForRun(context.Background(), result.Run.ID)
	if err != nil || !exists || !intent.HeadOwned {
		t.Fatalf("detached ownership intent=%+v exists=%v err=%v", intent, exists, err)
	}
	if !intentProvesHEADOwnership(intent, headIdentity{kind: headDetached, oid: oid}) {
		t.Fatalf("detached receipt did not prove HEAD ownership: %+v", intent)
	}
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
	if _, err := f.importProject(ImportInput{}); err == nil || problemCode(err) != CodeUnresolved {
		t.Fatalf("crash before rename err=%v", err)
	}
	if intentID == "" {
		t.Fatal("applied intent was not captured")
	}
	if _, err := f.manager.CreateWithOptions(context.Background(), "project", "foreign empty", repository.CreateOptions{ObjectFormat: "sha1"}); err != nil {
		t.Fatal(err)
	}
	foreign := repositoryFinalPath(t, f, "project")
	if err := f.service.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	stored, exists, err := f.store.ImportIntent(context.Background(), intentID)
	if err != nil || !exists || stored.ReceiptJSON == "" || stored.Status == state.ImportIntentNotApplied || stored.Status == state.ImportIntentComplete {
		t.Fatalf("initial intent was judged against another directory: status=%s exists=%v receipt=%q err=%v", stored.Status, exists, stored.ReceiptJSON, err)
	}
	dirs := unpublishedInitialDirectories(t, f)
	if len(dirs) != 1 {
		t.Fatalf("unpublished directories=%v", dirs)
	}
	if got := f.gitMaybe(dirs[0], "--git-dir", ".", "rev-parse", "--verify", "refs/heads/main"); got != wanted {
		t.Fatalf("owned directory main=%s want %s", got, wanted)
	}
	if got := f.gitMaybe(foreign, "--git-dir", ".", "rev-parse", "--verify", "refs/heads/main"); got != "" {
		t.Fatalf("foreign directory gained main=%s", got)
	}
	if err := f.store.Exec(context.Background(), `DELETE FROM repositories WHERE id=?`, "project"); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(foreign); err != nil {
		t.Fatal(err)
	}
	if err := f.service.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := f.destinationRefs()["refs/heads/main"]; got != wanted {
		t.Fatalf("reconciled owned directory main=%s want %s", got, wanted)
	}
}

func TestSecondImportDoesNotJudgeFirstInitialIntent(t *testing.T) {
	f := newFixture(t)
	wanted := f.commit("owned snapshot", "owned bytes\n")
	f.service.beforeInitialRename = func() error {
		return errors.New("synthetic crash before rename")
	}
	if _, err := f.importProject(ImportInput{}); err == nil || problemCode(err) != CodeUnresolved {
		t.Fatalf("crash before rename err=%v", err)
	}
	first, err := f.store.PendingImportIntents(context.Background(), "project")
	if err != nil || len(first) != 1 || first[0].Status != state.ImportIntentApplied {
		t.Fatalf("first intent=%+v err=%v", first, err)
	}
	receipt := first[0].ReceiptJSON
	f.service.beforeInitialRename = nil
	f.commit("second snapshot", "second bytes\n")
	if _, err := f.importProject(ImportInput{}); err != nil {
		t.Fatalf("second import err=%v", err)
	}
	stored, exists, err := f.store.ImportIntent(context.Background(), first[0].ID)
	if err != nil || !exists || stored.Status != state.ImportIntentApplied || stored.ReceiptJSON != receipt {
		t.Fatalf("second import judged the first intent: status=%s exists=%v err=%v", stored.Status, exists, err)
	}
	owned := false
	for _, dir := range unpublishedInitialDirectories(t, f) {
		if f.gitMaybe(dir, "--git-dir", ".", "rev-parse", "--verify", "refs/heads/main") == wanted {
			owned = true
		}
	}
	if !owned {
		t.Fatal("first unpublished directory was not preserved")
	}
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
	if problemCode(err) != CodeRepositoryTaken {
		t.Fatalf("late configuration collision err=%v", err)
	}
	after, exists, err := f.store.ImportSource(context.Background(), "project")
	if err != nil || !exists || after.URL != before.URL || after.SourceGeneration != before.SourceGeneration ||
		after.AuthorityRevision != before.AuthorityRevision || after.CredentialGeneration != before.CredentialGeneration {
		t.Fatalf("locked refusal changed source binding: before=%+v after=%+v exists=%v err=%v", before, after, exists, err)
	}
	if got := f.gitMaybe(repositoryFinalPath(t, f, "project"), "--git-dir", ".", "rev-parse", "--verify", "refs/heads/main"); got != "" {
		t.Fatalf("existing destination refs changed: %s", got)
	}
	runs, _, err := f.store.ImportRuns(context.Background(), "project", 10)
	if err != nil || len(runs) != 0 {
		t.Fatalf("locked refusal still started an import run: %+v err=%v", runs, err)
	}
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
	if problemCode(err) != CodeSuperseded {
		t.Fatalf("admission after a later configuration err=%v", err)
	}
	after, exists, readErr := f.store.ImportSource(context.Background(), "project")
	if readErr != nil || !exists || after.URL != later.URL || after.AuthorityRevision != later.AuthorityRevision {
		t.Fatalf("later configuration did not survive admission: url=%q authority=%d exists=%v want url=%q authority=%d err=%v", after.URL, after.AuthorityRevision, exists, later.URL, later.AuthorityRevision, readErr)
	}
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
	if problemCode(err) != CodeRepositoryTaken {
		t.Fatalf("collision after later configuration err=%v", err)
	}
	after, exists, err := f.store.ImportSource(context.Background(), "project")
	if err != nil || !exists || after.URL != later.URL || after.AuthorityRevision != later.AuthorityRevision {
		t.Fatalf("later configuration did not survive collision: url=%q authority=%d exists=%v want url=%q authority=%d err=%v", after.URL, after.AuthorityRevision, exists, later.URL, later.AuthorityRevision, err)
	}
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
	if problemCode(err) != CodeRepositoryTaken {
		t.Fatalf("collision after fetch err=%v", err)
	}
	if _, exists, err := f.store.ImportSource(context.Background(), "project"); err != nil || exists {
		t.Fatalf("source binding was not restored exists=%v err=%v", exists, err)
	}
	if _, exists, err := f.store.LoadImportCredentials(context.Background(), "project"); err != nil || exists {
		t.Fatalf("credential binding was not restored exists=%v err=%v", exists, err)
	}
	content, err := os.ReadFile(filepath.Join(final, "keep.txt"))
	if err != nil || string(content) != "foreign" {
		t.Fatalf("foreign directory changed content=%q err=%v", content, err)
	}
}

func repositoryFinalPath(t *testing.T, f *fixture, id string) string {
	t.Helper()
	path, err := f.manager.Path(id)
	if err != nil {
		t.Fatal(err)
	}
	return path
}

func unpublishedInitialDirectories(t *testing.T, f *fixture) []string {
	t.Helper()
	root := f.manager.RepositoryRoot()
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	var paths []string
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), unpublishedDirectoryPrefix) {
			paths = append(paths, filepath.Join(root, entry.Name()))
		}
	}
	return paths
}
