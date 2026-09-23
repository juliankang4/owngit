package state

import (
	"context"
	"crypto/sha256"
	"errors"
	"os"
	"strings"
	"testing"
	"time"
)

func testDeletion(repositoryID, mode string) RepositoryDeletion {
	moved := ".owngit-delete-" + strings.Repeat("a", 32)
	if mode == RepositoryDeletionKeepFiles {
		moved = ".owngit-removed/" + repositoryID + "-20270115T080000Z.git"
	}
	return RepositoryDeletion{
		RepositoryID: repositoryID, Mode: mode, Phase: RepositoryDeletionPending,
		Root: string(os.PathSeparator) + "owngit-test-root", Moved: moved, Marker: strings.Repeat("b", 32), CreatedAt: time.Unix(1_800_000_000, 0),
	}
}

// repositoryKeyedCounts counts rows for one repository in every table that has
// a repository_id column, plus the attempt-keyed result and log tables. It
// reads the live catalog, so a future repository table is covered as well.
func repositoryKeyedCounts(t *testing.T, store *Store, repositoryID string) map[string]int {
	t.Helper()
	ctx := context.Background()
	rows, err := store.db.QueryContext(ctx, `SELECT m.name FROM sqlite_master m WHERE m.type='table'
		AND EXISTS(SELECT 1 FROM pragma_table_info(m.name) p WHERE p.name='repository_id') ORDER BY m.name`)
	noErr(t, err)
	var tables []string
	for rows.Next() {
		var name string
		noErr(t, rows.Scan(&name))
		tables = append(tables, name)
	}
	noErr(t, closeRows(rows))
	if len(tables) < 20 {
		t.Fatalf("catalog scan found only %d repository tables: %v", len(tables), tables)
	}
	counts := map[string]int{}
	for _, table := range tables {
		var count int
		noErr(t, store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM "`+table+`" WHERE repository_id=?`, repositoryID).Scan(&count))
		counts[table] = count
	}
	for _, table := range []string{"check_results", "check_raw_logs"} {
		var count int
		noErr(t, store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM "`+table+`" WHERE attempt_id IN (SELECT id FROM check_attempts WHERE repository_id=?)
			OR attempt_id NOT IN (SELECT id FROM check_attempts)`, repositoryID).Scan(&count))
		counts[table] = count
	}
	var repositories int
	noErr(t, store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM repositories WHERE id=?`, repositoryID).Scan(&repositories))
	counts["repositories"] = repositories
	return counts
}

// seedRepositoryRecords writes records of every kind that a repository can
// own, including a finished job with results and a raw log and a queued job.
func seedRepositoryRecords(t *testing.T, fixture *checkJobFixture) {
	t.Helper()
	ctx := context.Background()
	store := fixture.store
	fixture.setPolicy(t, nil)
	fixture.grantConsent(t)
	runner, _ := fixture.issueRunner(t)
	finished := fixture.admit(t, pushJobRequest())
	fixture.claimAndStart(t, finished, runner, "")
	claimed, _, err := store.CheckJob(ctx, "project", finished.ID)
	noErr(t, err)
	completeJobAttempt(t, store, fixture.registerJobAttempt(t, claimed, runner), AttemptPassed, fixture.now.Add(time.Second))
	queued := fixture.admit(t, pullRequestJobRequest())
	if queued.Status != CheckJobPending {
		t.Fatalf("second job is not queued: %+v", queued)
	}
	noErr(t, store.RecordCheckObservation(ctx, "project", "refs/heads/main", strings.Repeat("a", 40), fixture.now))
	hash := sha256.Sum256([]byte("helper-token"))
	if _, _, err := store.CreateHelperCredential(ctx, "project", "laptop", "", hash[:], fixture.now); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreatePullRequest(ctx, "project", "Change", "feature", "main", strings.Repeat("a", 40), strings.Repeat("b", 40), ReviewPending, fixture.now); err != nil {
		t.Fatal(err)
	}
	noErr(t, store.Exec(ctx, `INSERT INTO direct_review_credentials(id,repository_id,label,value,created_at,updated_at) VALUES('dr','project','label','value',1,1)`))

	source, err := store.ConfigureImportSource(ctx, ImportSourceInput{
		RepositoryID: "project", URL: "https://example.invalid/team/project.git", Mode: ImportModeStandalone, Now: testImportNow(),
	})
	noErr(t, err)
	if _, err := store.SaveImportCredentials(ctx, ImportCredentials{
		RepositoryID: "project", URL: source.URL, SourceGeneration: source.SourceGeneration, ExpectedAuthorityRevision: source.AuthorityRevision,
		BearerToken: "synthetic-token",
	}, testImportNow()); err != nil {
		t.Fatal(err)
	}
	run := testImportRun(t, strings.Repeat("c", 32), "project", 1, ImportKindRefresh, ImportRunPreparing)
	noErr(t, store.BeginImportRun(ctx, run))
	run.Status, run.FinishedAt = ImportRunComplete, testImportNow().Add(time.Minute)
	noErr(t, store.FinishImportRun(ctx, run))
	noErr(t, store.RecordImportObservations(ctx, []ImportObservation{{
		RepositoryID: "project", SourceGeneration: 1, RefName: "refs/heads/main", OID: strings.Repeat("a", 40), ObservedAt: testImportNow(), RunID: run.ID,
	}}))
	noErr(t, store.CreateImportIntent(ctx, ImportIntent{
		ID: strings.Repeat("d", 32), RepositoryID: "project", RunID: run.ID, SourceGeneration: 1, AuthorityRevision: 1, Status: ImportIntentPlanning,
		Expected: map[string]string{"refs/heads/main": ""}, Desired: map[string]string{"refs/heads/main": strings.Repeat("a", 40)},
		Observed: map[string]string{"refs/heads/main": strings.Repeat("a", 40)}, Retained: map[string]string{}, CreatedAt: testImportNow(),
	}))
	if _, err := store.SetImportSchedule(ctx, "project", true, time.Hour, testImportNow()); err != nil {
		t.Fatal(err)
	}
	noErr(t, store.RegisterImportStaging(ctx, ImportStaging{
		Name: "run-" + strings.Repeat("e", 32), RepositoryID: "project", RunID: run.ID, Token: strings.Repeat("f", 32),
		State: ImportStagingActive, CreatedAt: testImportNow(),
	}))
	noErr(t, store.RegisterImportInitialDestination(ctx, ImportInitialDestination{
		Name: ".owngit-create-" + strings.Repeat("1", 32), RepositoryID: "project", RunID: run.ID, RootID: strings.Repeat("2", 32),
		Token: strings.Repeat("3", 32), DisplayName: "project", State: ImportInitialPublished, CreatedAt: testImportNow(),
	}))
}

func TestRepositoryDeletionRemovesEveryRecordKeyedByRepository(t *testing.T) {
	ctx := context.Background()
	fixture := newCheckJobFixture(t)
	store := fixture.store
	seedRepositoryRecords(t, fixture)
	otherPolicy := defaultPolicyInput()
	otherPolicy.RepositoryID = "other"
	if _, err := store.SetCheckPolicy(ctx, otherPolicy, fixture.now); err != nil {
		t.Fatal(err)
	}
	before := repositoryKeyedCounts(t, store, "project")
	for _, table := range []string{"check_jobs", "check_attempts", "check_results", "check_raw_logs", "tasks", "check_policies", "check_runner_credentials",
		"helper_credentials", "pull_requests", "import_sources", "import_runs", "import_publication_intents", "import_schedules", "import_stagings",
		"import_initial_destinations", "direct_review_credentials", "repositories"} {
		if before[table] == 0 {
			t.Fatalf("fixture wrote no %s rows: %v", table, before)
		}
	}
	otherBefore := repositoryKeyedCounts(t, store, "other")

	noErr(t, store.BeginRepositoryDeletion(ctx, testDeletion("project", RepositoryDeletionDeleteFiles)))

	for table, count := range repositoryKeyedCounts(t, store, "project") {
		if count != 0 {
			t.Errorf("%s keeps %d rows of the deleted repository", table, count)
		}
	}
	for table, count := range repositoryKeyedCounts(t, store, "other") {
		if count != otherBefore[table] {
			t.Errorf("%s rows of another repository changed from %d to %d", table, otherBefore[table], count)
		}
	}
	if _, exists, err := store.LoadImportCredentials(ctx, "project"); err != nil || exists {
		t.Fatalf("stored import credential remains exists=%v err=%v", exists, err)
	}
	if _, claimed, err := store.ClaimLocalCheckJob(ctx, "project", fixture.now); err != nil || claimed {
		t.Fatalf("a queued job of the deleted repository was claimable claimed=%v err=%v", claimed, err)
	}
	deletion, exists, err := store.RepositoryDeletion(ctx, "project")
	if err != nil || !exists || deletion.Phase != RepositoryDeletionPending || deletion.Moved != testDeletion("project", RepositoryDeletionDeleteFiles).Moved {
		t.Fatalf("intent=%+v exists=%v err=%v", deletion, exists, err)
	}
	// No path can claim the name while the deletion is unfinished, and it is
	// free for a new repository as soon as the deletion finishes.
	if err := store.AddRepository(ctx, Repository{ID: "project", Name: "project", CreatedAt: fixture.now}); !errors.Is(err, ErrRepositoryDeletionPending) {
		t.Fatalf("name claimed during an unfinished deletion: %v", err)
	}
	noErr(t, store.FinishRepositoryDeletion(ctx, "project"))
	noErr(t, store.AddRepository(ctx, Repository{ID: "project", Name: "project", CreatedAt: fixture.now}))
	if runs, _, err := store.ImportRuns(ctx, "project", 10); err != nil || len(runs) != 0 {
		t.Fatalf("new repository inherited import history: %d err=%v", len(runs), err)
	}
}

func TestRepositoryDeletionRefusesActiveImportAndCheck(t *testing.T) {
	ctx := context.Background()
	fixture := newCheckJobFixture(t)
	store := fixture.store
	if _, err := store.ConfigureImportSource(ctx, ImportSourceInput{
		RepositoryID: "project", URL: "https://example.invalid/team/project.git", Mode: ImportModeStandalone, Now: testImportNow(),
	}); err != nil {
		t.Fatal(err)
	}
	run := testImportRun(t, strings.Repeat("c", 32), "project", 1, ImportKindRefresh, ImportRunFetching)
	noErr(t, store.BeginImportRun(ctx, run))
	if err := store.BeginRepositoryDeletion(ctx, testDeletion("project", RepositoryDeletionKeepFiles)); !errors.Is(err, ErrRepositoryDeletionImportActive) {
		t.Fatalf("active import error=%v", err)
	}
	run.Status, run.FinishedAt = ImportRunFailed, testImportNow()
	noErr(t, store.FinishImportRun(ctx, run))

	fixture.setPolicy(t, nil)
	fixture.grantConsent(t)
	runner, _ := fixture.issueRunner(t)
	job := fixture.admit(t, pushJobRequest())
	if _, claimed, err := store.ClaimCheckJob(ctx, "project", runner.ID, fixture.now); err != nil || !claimed {
		t.Fatalf("claim claimed=%v err=%v", claimed, err)
	}
	if err := store.RepositoryDeletionBusy(ctx, "project"); !errors.Is(err, ErrRepositoryDeletionCheckActive) {
		t.Fatalf("claimed job busy=%v", err)
	}
	if err := store.BeginRepositoryDeletion(ctx, testDeletion("project", RepositoryDeletionKeepFiles)); !errors.Is(err, ErrRepositoryDeletionCheckActive) {
		t.Fatalf("claimed job error=%v", err)
	}
	if _, exists, err := store.Repository(ctx, "project"); err != nil || !exists {
		t.Fatalf("refused deletion removed the repository exists=%v err=%v", exists, err)
	}
	if stored, _, err := store.CheckJob(ctx, "project", job.ID); err != nil || stored.Status != CheckJobClaimed {
		t.Fatalf("refused deletion changed the job %+v err=%v", stored, err)
	}
	if deletions, err := store.RepositoryDeletions(ctx); err != nil || len(deletions) != 0 {
		t.Fatalf("refused deletion left an intent %+v err=%v", deletions, err)
	}
	if err := store.BeginRepositoryDeletion(ctx, testDeletion("missing", RepositoryDeletionKeepFiles)); !errors.Is(err, ErrRepositoryNotFound) {
		t.Fatalf("missing repository error=%v", err)
	}
}

func TestRepositoryDeletionIntentLifecycleAndValidation(t *testing.T) {
	ctx := context.Background()
	fixture := newCheckJobFixture(t)
	store := fixture.store
	for _, mutate := range []func(*RepositoryDeletion){
		func(d *RepositoryDeletion) { d.Moved = "../outside" },
		func(d *RepositoryDeletion) { d.Moved = ".owngit-removed/../../outside" },
		func(d *RepositoryDeletion) { d.Root = "relative" },
		func(d *RepositoryDeletion) { d.Mode = "erase" },
		func(d *RepositoryDeletion) { d.Phase = RepositoryDeletionMoved },
		func(d *RepositoryDeletion) { d.Marker = "" },
		func(d *RepositoryDeletion) { d.Marker = strings.Repeat("B", 32) },
	} {
		invalid := testDeletion("project", RepositoryDeletionKeepFiles)
		mutate(&invalid)
		if err := store.BeginRepositoryDeletion(ctx, invalid); err == nil {
			t.Fatalf("invalid intent accepted: %+v", invalid)
		}
	}
	if _, exists, _ := store.Repository(ctx, "project"); !exists {
		t.Fatal("an invalid intent removed the repository")
	}

	noErr(t, store.BeginRepositoryDeletion(ctx, testDeletion("project", RepositoryDeletionDeleteFiles)))
	// An older build ignores the intent and may record the name again.
	noErr(t, store.Exec(ctx, `INSERT INTO repositories(id,name,description,created_at) VALUES('project','project','',1)`))
	if err := store.BeginRepositoryDeletion(ctx, testDeletion("project", RepositoryDeletionDeleteFiles)); !errors.Is(err, ErrRepositoryDeletionPending) {
		t.Fatalf("second deletion while one is unfinished error=%v", err)
	}
	noErr(t, store.MarkRepositoryDeletionMoved(ctx, "project"))
	noErr(t, store.MarkRepositoryDeletionMoved(ctx, "project"))
	deletions, err := store.RepositoryDeletions(ctx)
	if err != nil || len(deletions) != 1 || deletions[0].RepositoryID != "project" || deletions[0].Phase != RepositoryDeletionMoved {
		t.Fatalf("deletions=%+v err=%v", deletions, err)
	}
	noErr(t, store.FinishRepositoryDeletion(ctx, "project"))
	if deletions, err := store.RepositoryDeletions(ctx); err != nil || len(deletions) != 0 {
		t.Fatalf("finished intent remains %+v err=%v", deletions, err)
	}
	// Unrelated metadata such as the schema version is not read as an intent.
	if settings, err := store.Settings(ctx); err != nil || !settings.Initialized {
		t.Fatalf("settings=%+v err=%v", settings, err)
	}
}

func TestRecoverySnapshotOmitsRepositoryInDeletion(t *testing.T) {
	ctx := context.Background()
	fixture := newCheckJobFixture(t)
	store := fixture.store
	seedRepositoryRecords(t, fixture)
	noErr(t, store.BeginRepositoryDeletion(ctx, testDeletion("project", RepositoryDeletionKeepFiles)))
	snapshot, err := store.RecoverySnapshot(ctx)
	noErr(t, err)
	for _, repository := range snapshot.Repositories {
		if repository.ID == "project" {
			t.Fatal("snapshot includes the repository being deleted")
		}
	}
	for _, job := range snapshot.CheckJobs {
		if job.RepositoryID == "project" {
			t.Fatal("snapshot includes a check job of the deleted repository")
		}
	}
	if len(snapshot.ImportSources) != 0 || len(snapshot.ImportRuns) != 0 || len(snapshot.PullRequests) != 0 || len(snapshot.Tasks) != 0 {
		t.Fatalf("snapshot includes records of the deleted repository: %+v", snapshot)
	}
}

// A container whose removal is unconfirmed keeps its ownership row so a later
// start can remove it. Deletion waits for that cleanup instead of dropping it.
func TestRepositoryDeletionWaitsForCheckContainerCleanup(t *testing.T) {
	ctx := context.Background()
	fixture := newCheckJobFixture(t)
	store := fixture.store
	fixture.setPolicy(t, func(input *CheckPolicyInput) {
		input.Executor = CheckExecutorContainer
		input.Execution.ContainerImage = "example.invalid/checks@sha256:" + strings.Repeat("a", 64)
	})
	fixture.grantConsent(t)
	job := fixture.admit(t, pushJobRequest())
	claimed, found, err := store.ClaimLocalCheckJob(ctx, "project", fixture.now)
	if err != nil || !found {
		t.Fatalf("local claim found=%v err=%v", found, err)
	}
	authority := CheckJobCompletionAuthority{JobID: job.ID, LeaseID: claimed.LeaseID, CredentialID: claimed.CredentialID, CredentialGeneration: claimed.CredentialGeneration}
	containerID := strings.Repeat("b", 64)
	noErr(t, store.PlanCheckContainer(ctx, authority, "owngit-check-test", "daemon-one", fixture.now))
	noErr(t, store.ConfirmCheckContainer(ctx, job.ID, "owngit-check-test", containerID, "daemon-one"))
	// A restart ends the job while the container cleanup stays uncertain.
	if _, err := store.ReconcileCheckJobRestart(ctx, fixture.now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if stored, _, err := store.CheckJob(ctx, "project", job.ID); err != nil || stored.Status != CheckJobInterrupted {
		t.Fatalf("job after restart=%+v err=%v", stored, err)
	}

	if err := store.RepositoryDeletionBusy(ctx, "project"); !errors.Is(err, ErrRepositoryDeletionCheckCleanup) {
		t.Fatalf("busy=%v", err)
	}
	if err := store.BeginRepositoryDeletion(ctx, testDeletion("project", RepositoryDeletionKeepFiles)); !errors.Is(err, ErrRepositoryDeletionCheckCleanup) {
		t.Fatalf("deletion with pending container cleanup error=%v", err)
	}
	if owned, err := store.ActiveCheckContainers(ctx, 10); err != nil || len(owned) != 1 {
		t.Fatalf("cleanup record was dropped: %+v err=%v", owned, err)
	}
	noErr(t, store.ClearCheckContainer(ctx, job.ID, containerID, "daemon-one"))
	noErr(t, store.BeginRepositoryDeletion(ctx, testDeletion("project", RepositoryDeletionKeepFiles)))
}

// A run whose source was read before the deletion committed is refused, so
// no run row outlives the repository.
func TestImportRunAdmittedAfterDeletionIsRefused(t *testing.T) {
	ctx := context.Background()
	fixture := newCheckJobFixture(t)
	store := fixture.store
	source, err := store.ConfigureImportSource(ctx, ImportSourceInput{
		RepositoryID: "project", URL: "https://example.invalid/team/project.git", Mode: ImportModeStandalone, Now: testImportNow(),
	})
	noErr(t, err)
	noErr(t, store.BeginRepositoryDeletion(ctx, testDeletion("project", RepositoryDeletionDeleteFiles)))
	late := testImportRun(t, strings.Repeat("c", 32), "project", source.SourceGeneration, ImportKindRefresh, ImportRunPreparing)
	if err := store.BeginImportRun(ctx, late); !errors.Is(err, ErrImportSourceChanged) {
		t.Fatalf("late run error=%v", err)
	}
	if count, err := store.TableRowCount(ctx, "import_runs"); err != nil || count != 0 {
		t.Fatalf("late run was recorded: %d err=%v", count, err)
	}
	// A configured source with another generation is refused the same way.
	noErr(t, store.FinishRepositoryDeletion(ctx, "project"))
	current, err := store.ConfigureImportSource(ctx, ImportSourceInput{
		RepositoryID: "project", URL: "https://example.invalid/team/other.git", Mode: ImportModeStandalone, Now: testImportNow(),
	})
	noErr(t, err)
	stale := testImportRun(t, strings.Repeat("d", 32), "project", current.SourceGeneration+1, ImportKindRefresh, ImportRunPreparing)
	if err := store.BeginImportRun(ctx, stale); !errors.Is(err, ErrImportSourceChanged) {
		t.Fatalf("stale generation error=%v", err)
	}
}

// A source configured after the deletion began (by an older build that ignores
// the intent) keeps its credential when the deletion finishes.
func TestFinishingDeletionKeepsCredentialOfNewerSource(t *testing.T) {
	ctx := context.Background()
	fixture := newCheckJobFixture(t)
	store := fixture.store
	noErr(t, store.BeginRepositoryDeletion(ctx, testDeletion("project", RepositoryDeletionDeleteFiles)))
	source, err := store.ConfigureImportSource(ctx, ImportSourceInput{
		RepositoryID: "project", URL: "https://example.invalid/team/project.git", Mode: ImportModeStandalone, Now: testImportNow(),
	})
	noErr(t, err)
	if _, err := store.SaveImportCredentials(ctx, ImportCredentials{
		RepositoryID: "project", URL: source.URL, SourceGeneration: source.SourceGeneration, ExpectedAuthorityRevision: source.AuthorityRevision,
		BearerToken: "synthetic-token",
	}, testImportNow()); err != nil {
		t.Fatal(err)
	}
	noErr(t, store.FinishRepositoryDeletion(ctx, "project"))
	if _, exists, err := store.LoadImportCredentials(ctx, "project"); err != nil || !exists {
		t.Fatalf("newer credential was removed exists=%v err=%v", exists, err)
	}
}
