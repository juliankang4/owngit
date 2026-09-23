package state

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func testImportNow() time.Time { return time.Unix(1_800_000_000, 0).UTC() }

func testImportRun(t *testing.T, id, repositoryID string, generation int64, kind, status string) ImportRun {
	t.Helper()
	return ImportRun{
		ID: id, RepositoryID: repositoryID, SourceGeneration: generation, AuthorityRevision: 1, Kind: kind, Status: status,
		StartedAt: testImportNow(), CreatedAt: testImportNow(),
	}
}

func TestImportSourceSeparatesIdentityAndExecutionAuthority(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	first, err := store.ConfigureImportSource(ctx, ImportSourceInput{
		RepositoryID: "project", URL: "https://example.invalid/team/project.git",
		Mode: ImportModeStandalone, GitOnlyConsent: true, AllowPrivateNetwork: true, Now: testImportNow(),
	})
	if err != nil || first.SourceGeneration != 1 || first.AuthorityRevision != 1 || !first.GitOnlyConsent || !first.AllowPrivateNetwork {
		t.Fatalf("configure first source=%+v err=%v", first, err)
	}
	same, err := store.ConfigureImportSource(ctx, ImportSourceInput{
		RepositoryID: "project", URL: "https://example.invalid/team/project.git",
		Mode: ImportModeCoexistence, GitOnlyConsent: true, AllowPrivateNetwork: false, Now: testImportNow().Add(time.Minute),
	})
	if err != nil || same.SourceGeneration != 1 || same.AuthorityRevision != 2 || !same.GitOnlyConsent || same.AllowPrivateNetwork || same.Mode != ImportModeCoexistence {
		t.Fatalf("same URL authority change=%+v err=%v", same, err)
	}
	unchanged, err := store.ConfigureImportSource(ctx, ImportSourceInput{
		RepositoryID: "project", URL: same.URL, Mode: ImportModeCoexistence,
		GitOnlyConsent: true, AllowPrivateNetwork: false, Now: testImportNow().Add(90 * time.Second),
	})
	if err != nil || unchanged.AuthorityRevision != same.AuthorityRevision {
		t.Fatalf("unchanged configuration revoked authority: %+v err=%v", unchanged, err)
	}
	if switched, err := store.SetImportTransportConsent(ctx, "project", true, testImportNow()); err != nil || !switched.AllowPrivateNetwork || switched.AuthorityRevision != 3 {
		t.Fatalf("transport consent update=%+v err=%v", switched, err)
	}
	changed, err := store.ConfigureImportSource(ctx, ImportSourceInput{
		RepositoryID: "project", URL: "https://example.invalid/other/project.git",
		Mode: ImportModeCoexistence, Now: testImportNow().Add(2 * time.Minute),
	})
	if err != nil || changed.SourceGeneration != 2 || changed.AuthorityRevision != 4 {
		t.Fatalf("URL change source=%+v err=%v", changed, err)
	}
	noErr(t, store.DeleteImportSource(ctx, "project"))
	if _, exists, err := store.ImportSource(ctx, "project"); err != nil || exists {
		t.Fatalf("deleted source exists=%v err=%v", exists, err)
	}
}

func TestDeletedSourceCannotReuseRetainedRunAuthority(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	if _, err := store.ConfigureImportSource(ctx, ImportSourceInput{
		RepositoryID: "project", URL: "https://example.invalid/team/project.git", Mode: ImportModeStandalone, Now: testImportNow(),
	}); err != nil {
		t.Fatal(err)
	}
	run := testImportRun(t, strings.Repeat("9", 32), "project", 1, ImportKindInitial, ImportRunPreparing)
	noErr(t, store.BeginImportRun(ctx, run))
	noErr(t, store.DeleteImportSource(ctx, "project"))
	recreated, err := store.ConfigureImportSource(ctx, ImportSourceInput{
		RepositoryID: "project", URL: "https://example.invalid/team/project.git", Mode: ImportModeStandalone, Now: testImportNow().Add(time.Minute),
	})
	noErr(t, err)
	if recreated.SourceGeneration != 2 || recreated.AuthorityRevision != 2 {
		t.Fatalf("recreated source reused stale authority: %+v", recreated)
	}
}

func TestImportRunActiveRefusalAndInterruption(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	if _, err := store.ConfigureImportSource(ctx, ImportSourceInput{
		RepositoryID: "project", URL: "https://example.invalid/team/project.git", Mode: ImportModeStandalone, Now: testImportNow(),
	}); err != nil {
		t.Fatal(err)
	}
	run := testImportRun(t, strings.Repeat("a", 32), "project", 1, ImportKindInitial, ImportRunPreparing)
	noErr(t, store.BeginImportRun(ctx, run))
	if err := store.BeginImportRun(ctx, testImportRun(t, strings.Repeat("b", 32), "project", 1, ImportKindRefresh, ImportRunFetching)); !errors.Is(err, ErrImportActive) {
		t.Fatalf("second active run error=%v", err)
	}
	noErr(t, store.SetImportRunStatus(ctx, run.ID, ImportRunFetching))
	cancelled, exists, err := store.RequestImportCancel(ctx, "project", testImportNow().Add(time.Second))
	if err != nil || !exists || cancelled.CancelRequestedAt == nil {
		t.Fatalf("cancel exists=%v run=%+v err=%v", exists, cancelled, err)
	}
	runs, intents, err := store.InterruptImportAuthority(ctx, testImportNow().Add(2*time.Second))
	if err != nil || runs != 1 || intents != 0 {
		t.Fatalf("interrupt runs=%d intents=%d err=%v", runs, intents, err)
	}
	stored, exists, err := store.ImportRun(ctx, run.ID)
	if err != nil || !exists || stored.Status != ImportRunInterrupted || stored.FinishedAt.IsZero() {
		t.Fatalf("interrupted run=%+v exists=%v err=%v", stored, exists, err)
	}
	stored.Status = ImportRunComplete
	stored.FinishedAt = testImportNow().Add(3 * time.Second)
	noErr(t, store.FinishImportRun(ctx, stored))
	if finished, _, err := store.ImportRun(ctx, run.ID); err != nil || finished.Status != ImportRunComplete {
		t.Fatalf("finished run=%+v err=%v", finished, err)
	}
	history, more, err := store.ImportRuns(ctx, "project", 1)
	if err != nil || more || len(history) != 1 || history[0].ID != run.ID {
		t.Fatalf("history=%d more=%v err=%v", len(history), more, err)
	}
}

func TestImportRunHistoryUsesAdmissionOrderWhenClocksTie(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	if _, err := store.ConfigureImportSource(ctx, ImportSourceInput{
		RepositoryID: "project", URL: "https://example.invalid/team/project.git", Mode: ImportModeStandalone,
		GitOnlyConsent: true, Now: testImportNow(),
	}); err != nil {
		t.Fatal(err)
	}
	finish := func(id string, inspectionComplete bool) ImportRun {
		run := testImportRun(t, id, "project", 1, ImportKindRefresh, ImportRunPreparing)
		noErr(t, store.BeginImportRun(ctx, run))
		run.Status = ImportRunComplete
		run.FinishedAt = testImportNow()
		run.LFSInspectionDone = inspectionComplete
		noErr(t, store.FinishImportRun(ctx, run))
		return run
	}
	first := finish(strings.Repeat("f", 32), true)
	second := finish(strings.Repeat("0", 32), false)

	history, more, err := store.ImportRuns(ctx, "project", 2)
	if err != nil || more || len(history) != 2 || history[0].ID != second.ID || history[1].ID != first.ID {
		t.Fatalf("history=%+v more=%v err=%v", history, more, err)
	}
	accepted, exists, err := store.LatestCompletedImportRun(ctx, "project")
	if err != nil || !exists || accepted.ID != second.ID || accepted.LFSInspectionDone {
		t.Fatalf("accepted=%+v exists=%v err=%v", accepted, exists, err)
	}

	later := testImportRun(t, strings.Repeat("a", 32), "project", 1, ImportKindRefresh, ImportRunPreparing)
	noErr(t, store.BeginImportRun(ctx, later))
	active, activeExists, err := store.ActiveImportRun(ctx, "project")
	if err != nil || !activeExists || active.ID != later.ID {
		t.Fatalf("active=%+v exists=%v err=%v", active, activeExists, err)
	}
	accepted, exists, err = store.LatestCompletedImportRun(ctx, "project")
	if err != nil || !exists || accepted.ID != second.ID {
		t.Fatalf("active run hid accepted=%+v exists=%v err=%v", accepted, exists, err)
	}
	later.Status = ImportRunFailed
	later.FinishedAt = testImportNow()
	later.ErrorClass = "synthetic_failure"
	noErr(t, store.FinishImportRun(ctx, later))
	history, _, err = store.ImportRuns(ctx, "project", 1)
	if err != nil || len(history) != 1 || history[0].ID != later.ID {
		t.Fatalf("failed latest history=%+v err=%v", history, err)
	}
	accepted, exists, err = store.LatestCompletedImportRun(ctx, "project")
	if err != nil || !exists || accepted.ID != second.ID || accepted.LFSInspectionDone {
		t.Fatalf("failed run hid accepted=%+v exists=%v err=%v", accepted, exists, err)
	}
}

func TestImportRunAdmissionOrderSurvivesCurrentRecoveryAcrossRepositories(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	noErr(t, store.CompleteSetup(ctx, t.TempDir(), "open", "", "admin-hash", true))
	for _, repositoryID := range []string{"alpha", "beta"} {
		noErr(t, store.AddRepository(ctx, Repository{ID: repositoryID, Name: repositoryID, CreatedAt: testImportNow()}))
		if _, err := store.ConfigureImportSource(ctx, ImportSourceInput{
			RepositoryID: repositoryID, URL: "https://example.invalid/" + repositoryID + ".git",
			Mode: ImportModeStandalone, Now: testImportNow(),
		}); err != nil {
			t.Fatal(err)
		}
	}
	finish := func(repositoryID, id string) {
		run := testImportRun(t, id, repositoryID, 1, ImportKindRefresh, ImportRunPreparing)
		noErr(t, store.BeginImportRun(ctx, run))
		run.Status = ImportRunComplete
		run.FinishedAt = testImportNow()
		run.LFSInspectionDone = true
		noErr(t, store.FinishImportRun(ctx, run))
	}
	admissions := []struct{ repositoryID, id string }{
		{"alpha", strings.Repeat("f", 32)},
		{"beta", strings.Repeat("e", 32)},
		{"alpha", strings.Repeat("0", 32)},
		{"beta", strings.Repeat("1", 32)},
	}
	for _, admission := range admissions {
		finish(admission.repositoryID, admission.id)
	}

	snapshot, err := store.RecoverySnapshot(ctx)
	noErr(t, err)
	if len(snapshot.ImportRuns) != len(admissions) {
		t.Fatalf("snapshot runs=%d", len(snapshot.ImportRuns))
	}
	for index, admission := range admissions {
		if snapshot.ImportRuns[index].ID != admission.id {
			t.Fatalf("snapshot order[%d]=%s want %s", index, snapshot.ImportRuns[index].ID, admission.id)
		}
	}

	restored := openTestStore(t)
	noErr(t, restored.RestoreRecoveryState(ctx, t.TempDir(), snapshot))
	for repositoryID, expected := range map[string][]string{
		"alpha": {admissions[2].id, admissions[0].id},
		"beta":  {admissions[3].id, admissions[1].id},
	} {
		runs, _, err := restored.ImportRuns(ctx, repositoryID, 2)
		if err != nil || len(runs) != 2 || runs[0].ID != expected[0] || runs[1].ID != expected[1] {
			t.Fatalf("restored %s runs=%+v err=%v", repositoryID, runs, err)
		}
	}
}

func TestImportObservationUpsertAndIntentReceipt(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	if _, err := store.ConfigureImportSource(ctx, ImportSourceInput{
		RepositoryID: "project", URL: "https://example.invalid/team/project.git", Mode: ImportModeStandalone, Now: testImportNow(),
	}); err != nil {
		t.Fatal(err)
	}
	observation := ImportObservation{
		RepositoryID: "project", SourceGeneration: 1, RefName: "refs/heads/main",
		OID: strings.Repeat("a", 40), ObservedAt: testImportNow(), RunID: strings.Repeat("c", 32),
	}
	noErr(t, store.RecordImportObservations(ctx, []ImportObservation{observation}))
	observation.OID = strings.Repeat("b", 40)
	observation.ObservedAt = testImportNow().Add(time.Minute)
	noErr(t, store.RecordImportObservations(ctx, []ImportObservation{observation}))
	observations, err := store.ImportObservations(ctx, "project", 1)
	if err != nil || len(observations) != 1 || observations[0].OID != strings.Repeat("b", 40) {
		t.Fatalf("observations=%+v err=%v", observations, err)
	}
	if err := store.RecordImportObservations(ctx, []ImportObservation{{
		RepositoryID: "project", SourceGeneration: 1, RefName: "refs/heads/main",
		OID: "short", ObservedAt: testImportNow(),
	}}); err == nil {
		t.Fatal("short observation object was accepted")
	}

	intent := ImportIntent{
		ID: strings.Repeat("d", 32), RepositoryID: "project", RunID: strings.Repeat("c", 32),
		SourceGeneration: 1, AuthorityRevision: 1, Status: ImportIntentPlanning,
		Expected:   map[string]string{"refs/heads/main": ""},
		Desired:    map[string]string{"refs/heads/main": strings.Repeat("b", 40)},
		Observed:   map[string]string{"refs/heads/main": strings.Repeat("b", 40), ImportHeadRef: strings.Repeat("b", 40)},
		Retained:   map[string]string{},
		HeadSymref: "refs/heads/main",
		CreatedAt:  testImportNow(),
	}
	noErr(t, store.CreateImportIntent(ctx, intent))
	receipt := map[string]string{"refs/heads/main": strings.Repeat("b", 40)}
	receiptJSON := `{"refs/heads/main":"` + strings.Repeat("b", 40) + `"}`
	noErr(t, store.UpdateImportIntent(ctx, intent.ID, ImportIntentComplete, receiptJSON, ImportReceiptDigest(receiptJSON), "", testImportNow()))
	stored, exists, err := store.ImportIntent(ctx, intent.ID)
	if err != nil || !exists || stored.Status != ImportIntentComplete || stored.ReceiptDigest == "" || len(stored.Observed) != 2 {
		t.Fatalf("stored intent=%+v exists=%v err=%v", stored, exists, err)
	}
	byRun, exists, err := store.CompletedImportIntentForRun(ctx, intent.RunID)
	if err != nil || !exists || byRun.ID != intent.ID {
		t.Fatalf("complete intent by run=%+v exists=%v err=%v", byRun, exists, err)
	}
	partial := stored
	partial.ID = strings.Repeat("e", 32)
	partial.ReceiptJSON = `{}`
	partial.ReceiptDigest = ImportReceiptDigest(partial.ReceiptJSON)
	if err := store.CreateImportIntent(ctx, partial); err == nil {
		t.Fatal("complete intent with a partial receipt was accepted")
	}
	duplicate := stored
	duplicate.ID = strings.Repeat("f", 32)
	noErr(t, store.CreateImportIntent(ctx, duplicate))
	if _, _, err := store.CompletedImportIntentForRun(ctx, intent.RunID); err == nil {
		t.Fatal("multiple complete intents supplied ambiguous ownership evidence")
	}
	if len(receipt) != 1 {
		t.Fatalf("receipt shrank: %v", receipt)
	}
	// A receipt that claims an unplanned value must be refused by validation.
	forged := stored
	forged.ReceiptDigest = strings.Repeat("0", 64)
	if err := validateImportIntentRecord(forged); err == nil {
		t.Fatal("forged receipt digest was accepted")
	}
}

func TestImportScheduleFairnessAndDueOrder(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	now := testImportNow()
	for _, id := range []string{"alpha", "beta", "gamma"} {
		if _, err := store.SetImportSchedule(ctx, id, true, time.Minute, now); err != nil {
			t.Fatal(err)
		}
	}
	due, err := store.DueImportSchedules(ctx, now, 8)
	if err != nil || len(due) != 3 || due[0].RepositoryID != "alpha" {
		t.Fatalf("due=%+v err=%v", due, err)
	}
	// A run is recorded only for a configured source generation.
	if _, err := store.ConfigureImportSource(ctx, ImportSourceInput{
		RepositoryID: "alpha", URL: "https://example.invalid/team/alpha.git", Mode: ImportModeStandalone, Now: now,
	}); err != nil {
		t.Fatal(err)
	}
	run := testImportRun(t, strings.Repeat("e", 32), "alpha", 1, ImportKindScheduled, ImportRunPreparing)
	noErr(t, store.BeginImportRun(ctx, run))
	due, err = store.DueImportSchedules(ctx, now, 8)
	if err != nil || len(due) != 2 || due[0].RepositoryID != "beta" {
		t.Fatalf("fair due=%+v err=%v", due, err)
	}
	// The stamped start makes alpha due again only after its interval.
	due, err = store.DueImportSchedules(ctx, now.Add(time.Minute), 8)
	if err != nil || len(due) != 3 || due[2].RepositoryID != "alpha" {
		t.Fatalf("interval due=%+v err=%v", due, err)
	}
}

func TestCredentialAuthorityLocksAreRepositoryScoped(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	releaseProject := store.LockImportCredentialAuthority("project")
	released := false
	defer func() {
		if !released {
			releaseProject()
		}
	}()
	done := make(chan error, 1)
	go func() {
		_, err := store.ConfigureImportSource(ctx, ImportSourceInput{
			RepositoryID: "other", URL: "https://example.invalid/other.git", Mode: ImportModeStandalone, Now: testImportNow(),
		})
		done <- err
	}()
	timer := time.NewTimer(2 * time.Second)
	defer timer.Stop()
	select {
	case err := <-done:
		noErr(t, err)
	case <-timer.C:
		releaseProject()
		released = true
		<-done
		t.Fatal("one repository credential lock stalled another repository")
	}
}

func TestImportCredentialFileIsBoundAndPrivate(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	source, err := store.ConfigureImportSource(ctx, ImportSourceInput{
		RepositoryID: "project", URL: "https://example.invalid/team/project.git", Mode: ImportModeStandalone, Now: testImportNow(),
	})
	noErr(t, err)
	credential := ImportCredentials{
		RepositoryID: "project", URL: source.URL, SourceGeneration: source.SourceGeneration, ExpectedAuthorityRevision: source.AuthorityRevision,
		Basic: &ImportBasicAuth{Username: "user", Password: "secret"},
	}
	source, err = store.SaveImportCredentials(ctx, credential, testImportNow().Add(time.Second))
	if err != nil || source.AuthorityRevision != 2 {
		t.Fatalf("save credentials source=%+v err=%v", source, err)
	}
	loaded, exists, err := store.LoadImportCredentials(ctx, "project")
	if err != nil || !exists || loaded.Basic == nil || loaded.Basic.Password != "secret" || !loaded.Bound(source) {
		t.Fatalf("credential did not load as bound: exists=%v err=%v", exists, err)
	}
	current := credential
	current.ExpectedAuthorityRevision = source.AuthorityRevision
	unchanged, err := store.SaveImportCredentials(ctx, current, testImportNow().Add(2*time.Second))
	if err != nil || unchanged.AuthorityRevision != source.AuthorityRevision {
		t.Fatalf("identical credential changed authority: source=%+v err=%v", unchanged, err)
	}
	if _, err := store.SaveImportCredentials(ctx, credential, testImportNow().Add(3*time.Second)); err == nil {
		t.Fatal("stale credential writer replaced current authority")
	}
	modeChanged, err := store.ConfigureImportSource(ctx, ImportSourceInput{
		RepositoryID: "project", URL: source.URL, Mode: ImportModeCoexistence, Now: testImportNow().Add(4 * time.Second),
	})
	if err != nil || !loaded.Bound(modeChanged) {
		t.Fatalf("same-source configuration lost valid credential binding: bound=%v err=%v", loaded.Bound(modeChanged), err)
	}
	source = modeChanged
	if runtime.GOOS != "windows" {
		path := filepath.Join(store.Dir(), importCredentialDir, "project.json")
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm() != 0o600 {
			t.Fatalf("credential mode=%v err=%v", infoMode(info), err)
		}
	}
	changed, err := store.ConfigureImportSource(ctx, ImportSourceInput{
		RepositoryID: "project", URL: "https://example.invalid/other/project.git", Mode: ImportModeStandalone, Now: testImportNow().Add(time.Minute),
	})
	noErr(t, err)
	if loaded.Bound(changed) {
		t.Fatal("credential stayed bound after the source URL changed")
	}
	if revoked, err := store.DeleteImportCredentials(ctx, "project", testImportNow().Add(2*time.Minute)); err != nil || revoked.AuthorityRevision != changed.AuthorityRevision+1 {
		t.Fatalf("revoke credentials source=%+v err=%v", revoked, err)
	}
	if _, exists, err := store.LoadImportCredentials(ctx, "project"); err != nil || exists {
		t.Fatalf("deleted credential exists=%v err=%v", exists, err)
	}
}

func TestCredentialDatabaseFailureLeavesAuthorityFailClosed(t *testing.T) {
	t.Run("replacement", func(t *testing.T) {
		store := openTestStore(t)
		ctx := context.Background()
		source, err := store.ConfigureImportSource(ctx, ImportSourceInput{
			RepositoryID: "project", URL: "https://example.invalid/team/project.git", Mode: ImportModeStandalone, Now: testImportNow(),
		})
		noErr(t, err)
		credential := ImportCredentials{
			RepositoryID: "project", URL: source.URL, SourceGeneration: source.SourceGeneration, ExpectedAuthorityRevision: source.AuthorityRevision,
			Basic: &ImportBasicAuth{Username: "user", Password: "first-secret"},
		}
		source, err = store.SaveImportCredentials(ctx, credential, testImportNow().Add(time.Second))
		noErr(t, err)
		noErr(t, store.Exec(ctx, `CREATE TRIGGER fail_import_authority BEFORE UPDATE OF authority_revision ON import_sources BEGIN SELECT RAISE(FAIL,'synthetic authority failure'); END`))
		replacement := credential
		replacement.ExpectedAuthorityRevision = source.AuthorityRevision
		replacement.Basic = &ImportBasicAuth{Username: "user", Password: "replacement-secret"}
		if _, err := store.SaveImportCredentials(ctx, replacement, testImportNow().Add(2*time.Second)); err == nil {
			t.Fatal("credential replacement survived database failure")
		}
		current, _, err := store.ImportSource(ctx, "project")
		if err != nil || current.AuthorityRevision != source.AuthorityRevision {
			t.Fatalf("source=%+v err=%v", current, err)
		}
		stored, exists, err := store.LoadImportCredentials(ctx, "project")
		if err != nil || !exists || stored.Bound(current) {
			t.Fatalf("failed credential became usable: exists=%v bound=%v err=%v", exists, stored.Bound(current), err)
		}
		if _, blocked := store.ImportCredentialAuthority("project"); !blocked {
			t.Fatal("failed credential transition did not block new execution")
		}
		noErr(t, store.Exec(ctx, `DROP TRIGGER fail_import_authority`))
		changed, err := store.ConfigureImportSource(ctx, ImportSourceInput{
			RepositoryID: "project", URL: source.URL, Mode: ImportModeCoexistence, Now: testImportNow().Add(3 * time.Second),
		})
		noErr(t, err)
		stored, exists, err = store.LoadImportCredentials(ctx, "project")
		if err != nil || !exists || stored.Bound(changed) {
			t.Fatalf("unrelated configuration authorized failed credentials: exists=%v bound=%v err=%v", exists, stored.Bound(changed), err)
		}
		replacement.ExpectedAuthorityRevision = changed.AuthorityRevision
		retried, err := store.SaveImportCredentials(ctx, replacement, testImportNow().Add(4*time.Second))
		if err != nil {
			t.Fatalf("safe retry: %v", err)
		}
		stored, exists, err = store.LoadImportCredentials(ctx, "project")
		if _, blocked := store.ImportCredentialAuthority("project"); err != nil || !exists || blocked || !stored.Bound(retried) {
			t.Fatalf("safe retry did not restore bound authority: exists=%v blocked=%v bound=%v err=%v", exists, blocked, stored.Bound(retried), err)
		}
	})

	t.Run("revocation", func(t *testing.T) {
		store := openTestStore(t)
		ctx := context.Background()
		source, err := store.ConfigureImportSource(ctx, ImportSourceInput{
			RepositoryID: "project", URL: "https://example.invalid/team/project.git", Mode: ImportModeStandalone, Now: testImportNow(),
		})
		noErr(t, err)
		credential := ImportCredentials{
			RepositoryID: "project", URL: source.URL, SourceGeneration: source.SourceGeneration, ExpectedAuthorityRevision: source.AuthorityRevision,
			BearerToken: "first-secret",
		}
		source, err = store.SaveImportCredentials(ctx, credential, testImportNow().Add(time.Second))
		noErr(t, err)
		noErr(t, store.Exec(ctx, `CREATE TRIGGER fail_import_authority BEFORE UPDATE OF authority_revision ON import_sources BEGIN SELECT RAISE(FAIL,'synthetic authority failure'); END`))
		if _, err := store.DeleteImportCredentials(ctx, "project", testImportNow().Add(2*time.Second)); err == nil {
			t.Fatal("credential revocation survived database failure")
		}
		current, _, err := store.ImportSource(ctx, "project")
		if err != nil || current.AuthorityRevision != source.AuthorityRevision {
			t.Fatalf("source=%+v err=%v", current, err)
		}
		if _, exists, err := store.LoadImportCredentials(ctx, "project"); err != nil || exists {
			t.Fatalf("failed revocation restored old credential exists=%v err=%v", exists, err)
		}
		if _, blocked := store.ImportCredentialAuthority("project"); !blocked {
			t.Fatal("failed revocation did not block new execution")
		}
		noErr(t, store.Exec(ctx, `DROP TRIGGER fail_import_authority`))
		revoked, err := store.DeleteImportCredentials(ctx, "project", testImportNow().Add(3*time.Second))
		if err != nil || revoked.CredentialGeneration != "" {
			t.Fatalf("safe revocation retry: credential_generation=%q err=%v", revoked.CredentialGeneration, err)
		}
		if _, blocked := store.ImportCredentialAuthority("project"); blocked {
			t.Fatal("successful revocation retry left execution blocked")
		}
	})
}

func TestImportRecoveryPreservesHistoryAndInvalidatesMachineState(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	noErr(t, store.CompleteSetup(ctx, t.TempDir(), "open", "", "admin-hash", true))
	repository := Repository{ID: "project", Name: "Project", CreatedAt: testImportNow()}
	noErr(t, store.AddRepository(ctx, repository))
	if _, err := store.ConfigureImportSource(ctx, ImportSourceInput{
		RepositoryID: "project", URL: "https://example.invalid/team/project.git",
		Mode: ImportModeCoexistence, GitOnlyConsent: true, AllowPrivateNetwork: true, Now: testImportNow(),
	}); err != nil {
		t.Fatal(err)
	}
	run := testImportRun(t, strings.Repeat("f", 32), "project", 1, ImportKindInitial, ImportRunPreparing)
	noErr(t, store.BeginImportRun(ctx, run))
	run.Status = ImportRunComplete
	run.FinishedAt = testImportNow().Add(time.Second)
	run.LFSInspectionDone = false
	noErr(t, store.FinishImportRun(ctx, run))
	pending := testImportRun(t, strings.Repeat("1", 32), "project", 1, ImportKindRefresh, ImportRunPublishing)
	noErr(t, store.BeginImportRun(ctx, pending))
	if err := store.RecordImportObservations(ctx, []ImportObservation{{
		RepositoryID: "project", SourceGeneration: 1, RefName: "refs/heads/main",
		OID: strings.Repeat("a", 40), ObservedAt: testImportNow(), RunID: run.ID,
	}}); err != nil {
		t.Fatal(err)
	}
	head := "symbolic refs/heads/main " + strings.Repeat("a", 40)
	intent := ImportIntent{
		ID: strings.Repeat("2", 32), RepositoryID: "project", RunID: pending.ID, SourceGeneration: 1, AuthorityRevision: 1,
		Status: ImportIntentPlanning, Expected: map[string]string{"refs/heads/main": "", ImportHeadRef: head},
		Desired:  map[string]string{"refs/heads/main": strings.Repeat("a", 40), ImportHeadRef: head},
		Observed: map[string]string{"refs/heads/main": strings.Repeat("a", 40), ImportHeadRef: head},
		Retained: map[string]string{}, HeadOwned: true, CreatedAt: testImportNow(),
	}
	noErr(t, store.CreateImportIntent(ctx, intent))
	snapshot, err := store.RecoverySnapshot(ctx)
	noErr(t, err)
	if len(snapshot.ImportSources) != 1 || snapshot.ImportSources[0].AllowPrivateNetwork {
		t.Fatalf("snapshot did not clear transport consent: %+v", snapshot.ImportSources)
	}
	if len(snapshot.ImportRuns) != 2 || len(snapshot.ImportObservations) != 1 || len(snapshot.ImportIntents) != 1 {
		t.Fatalf("snapshot import counts runs=%d observations=%d intents=%d", len(snapshot.ImportRuns), len(snapshot.ImportObservations), len(snapshot.ImportIntents))
	}
	for _, stored := range snapshot.ImportRuns {
		if stored.Status != ImportRunComplete && stored.Status != ImportRunInterrupted {
			t.Fatalf("run %s status %q is not terminal", stored.ID, stored.Status)
		}
	}
	if snapshot.ImportIntents[0].Status != ImportIntentInvalidated || snapshot.ImportIntents[0].HeadOwned {
		t.Fatalf("pending intent retained authority: %+v", snapshot.ImportIntents[0])
	}

	restored := openTestStore(t)
	if err := restored.writeImportCredentials(ctx, ImportCredentials{
		RepositoryID: "project", URL: "https://example.invalid/team/project.git", SourceGeneration: 1,
		CredentialGeneration: strings.Repeat("b", 32), BearerToken: "synthetic-orphan",
	}); err != nil {
		t.Fatal(err)
	}
	noErr(t, restored.RestoreRecoveryState(ctx, t.TempDir(), snapshot))
	source, exists, err := restored.ImportSource(ctx, "project")
	if err != nil || !exists || source.AllowPrivateNetwork || !source.GitOnlyConsent || source.Mode != ImportModeCoexistence || source.AuthorityRevision != 2 || source.CredentialGeneration != "" {
		t.Fatalf("restored source=%+v exists=%v err=%v", source, exists, err)
	}
	if _, err := restored.SetImportSchedule(ctx, "project", true, time.Minute, testImportNow()); err != nil {
		t.Fatal(err)
	}
	runs, _, err := restored.ImportRuns(ctx, "project", 10)
	if err != nil || len(runs) != 2 {
		t.Fatalf("restored runs=%d err=%v", len(runs), err)
	}
	if runs[0].ID != pending.ID || runs[1].ID != run.ID {
		t.Fatalf("restored admission order=%v", []string{runs[0].ID, runs[1].ID})
	}
	accepted, acceptedExists, err := restored.LatestCompletedImportRun(ctx, "project")
	if err != nil || !acceptedExists || accepted.ID != run.ID || accepted.LFSInspectionDone {
		t.Fatalf("restored accepted snapshot=%+v exists=%v err=%v", accepted, acceptedExists, err)
	}
	restoredIntent, exists, err := restored.ImportIntent(ctx, intent.ID)
	if err != nil || !exists || restoredIntent.Status != ImportIntentInvalidated || restoredIntent.HeadOwned {
		t.Fatalf("restored intent=%+v exists=%v err=%v", restoredIntent, exists, err)
	}
	if _, exists, err := restored.ImportStaging(ctx, "run-"+pending.ID); err != nil || exists {
		t.Fatalf("restored staging exists=%v err=%v", exists, err)
	}
	// A restored store has no credential file until one is saved again.
	if _, exists, err := restored.LoadImportCredentials(ctx, "project"); err != nil || exists {
		t.Fatalf("restored credential exists=%v err=%v", exists, err)
	}
}

func TestPendingIntentPageDoesNotRereadResolvedBoundary(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	now := testImportNow()
	ids := []string{strings.Repeat("a", 32), strings.Repeat("b", 32), strings.Repeat("c", 32)}
	for _, id := range ids {
		if err := store.CreateImportIntent(ctx, ImportIntent{
			ID: id, RepositoryID: "project", RunID: strings.Repeat("1", 32),
			SourceGeneration: 1, AuthorityRevision: 1, Status: ImportIntentPlanning,
			Expected: map[string]string{"refs/heads/main": ""},
			Desired:  map[string]string{"refs/heads/main": strings.Repeat("a", 40)},
			Observed: map[string]string{"refs/heads/main": strings.Repeat("a", 40)},
			Retained: map[string]string{}, CreatedAt: now,
		}); err != nil {
			t.Fatal(err)
		}
	}
	first, err := store.PendingImportIntentsPage(ctx, 0, 2)
	if err != nil || len(first) != 2 {
		t.Fatalf("first page=%v err=%v", first, err)
	}
	for _, intent := range first {
		noErr(t, store.UpdateImportIntent(ctx, intent.ID, ImportIntentNotApplied, "", "", "resolved", now))
	}
	second, err := store.PendingImportIntentsPage(ctx, first[len(first)-1].RowID, 2)
	if err != nil || len(second) != 1 || second[0].ID == first[0].ID || second[0].ID == first[1].ID {
		t.Fatalf("resolved page boundary was re-read: first=%v second=%v err=%v", first, second, err)
	}
}

// Each snapshot carries one cross-record fault that restore must refuse
// before it writes anything.
func TestValidateImportRecoveryRefusesInconsistentSnapshots(t *testing.T) {
	now := testImportNow()
	source := ImportSource{
		RepositoryID: "project", URL: "https://example.invalid/team/project.git", SourceGeneration: 1, AuthorityRevision: 1,
		Mode: ImportModeStandalone, CreatedAt: now, UpdatedAt: now,
	}
	credentialSource := source
	credentialSource.CredentialGeneration = strings.Repeat("a", 32)
	otherRun := testImportRun(t, strings.Repeat("a", 32), "other", 1, ImportKindRefresh, ImportRunComplete)
	otherRun.FinishedAt = now
	receipt := `{"refs/heads/main":"` + strings.Repeat("a", 40) + `"}`
	tests := []struct {
		name     string
		snapshot RecoveryState
		want     string
	}{
		{name: "observation without source generation", want: "import observation without a run does not match a source generation", snapshot: RecoveryState{
			ImportSources: []ImportSource{source},
			ImportObservations: []ImportObservation{{
				RepositoryID: "project", SourceGeneration: 4, RefName: "refs/heads/main", OID: strings.Repeat("a", 40), ObservedAt: now,
			}},
		}},
		{name: "schedule without source", want: "import schedule does not reference an import source", snapshot: RecoveryState{
			ImportSchedules: []ImportSchedule{{RepositoryID: "orphan", Enabled: true, IntervalSeconds: 60, CreatedAt: now, UpdatedAt: now}},
		}},
		{name: "machine-local initial destination", want: "portable import snapshot contains machine-local initial destinations", snapshot: RecoveryState{
			ImportInitialDestinations: []ImportInitialDestination{{
				Name: ".owngit-create-" + strings.Repeat("a", 32), RootID: strings.Repeat("b", 32), Token: strings.Repeat("c", 32),
				State: ImportInitialUnknown, CreatedAt: now,
			}},
		}},
		{name: "machine-local credential generation", want: "portable import source contains machine-local credential authority", snapshot: RecoveryState{
			ImportSources: []ImportSource{credentialSource},
		}},
		{name: "intent with missing run", want: "import intent refers to a missing run", snapshot: RecoveryState{
			ImportIntents: []ImportIntent{{
				ID: strings.Repeat("3", 32), RepositoryID: "project", RunID: strings.Repeat("4", 32),
				SourceGeneration: 1, AuthorityRevision: 1, Status: ImportIntentComplete,
				Expected: map[string]string{"refs/heads/main": ""}, Desired: map[string]string{"refs/heads/main": strings.Repeat("a", 40)},
				Observed: map[string]string{}, Retained: map[string]string{},
				ReceiptJSON: receipt, ReceiptDigest: ImportReceiptDigest(receipt), CreatedAt: now,
			}},
		}},
		{name: "observation of another repository's run", want: "import observation identity does not match its run", snapshot: RecoveryState{
			ImportRuns: []ImportRun{otherRun},
			ImportObservations: []ImportObservation{{
				RepositoryID: "project", SourceGeneration: 1, RefName: "refs/heads/main", OID: strings.Repeat("b", 40), ObservedAt: now, RunID: otherRun.ID,
			}},
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := ValidateImportRecovery(test.snapshot); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error=%v, want %q", err, test.want)
			}
		})
	}
}

func TestValidateImportRecoveryRefusesIntentWithoutSnapshotRepository(t *testing.T) {
	now := testImportNow()
	snapshotWith := func(status string) RecoveryState {
		run := testImportRun(t, strings.Repeat("a", 32), "missing", 1, ImportKindInitial, ImportRunInterrupted)
		run.FinishedAt = now
		return RecoveryState{
			Repositories: []Repository{{ID: "project", Name: "Project", CreatedAt: now}},
			ImportRuns:   []ImportRun{run},
			ImportIntents: []ImportIntent{{
				ID: strings.Repeat("b", 32), RepositoryID: "missing", RunID: run.ID,
				SourceGeneration: 1, AuthorityRevision: 1, Status: status,
				Expected: map[string]string{"refs/heads/main": ""},
				Desired:  map[string]string{"refs/heads/main": strings.Repeat("a", 40)},
				Observed: map[string]string{"refs/heads/main": strings.Repeat("a", 40)},
				Retained: map[string]string{}, CreatedAt: now,
			}},
		}
	}
	// An initial import that stopped before its repository existed leaves a
	// settled intent. It is history and must not block a backup.
	for _, status := range []string{ImportIntentNotApplied, ImportIntentInvalidated, ImportIntentAbandoned} {
		if err := ValidateImportRecovery(snapshotWith(status)); err != nil {
			t.Fatalf("settled %s intent without repository error=%v", status, err)
		}
	}
	// Open, unresolved, or complete intents need their repository and stay
	// visible by failing closed.
	for _, status := range []string{ImportIntentPlanning, ImportIntentApplied, ImportIntentUnresolved} {
		if err := ValidateImportRecovery(snapshotWith(status)); err == nil || !strings.Contains(err.Error(), `"missing" is `+status) || !strings.Contains(err.Error(), "not recorded") {
			t.Fatalf("%s intent without snapshot repository error=%v", status, err)
		}
	}
}

// The recorded shape of an initial import stopped after its intent existed:
// source row, interrupted run, invalidated intent, and no repository. A real
// snapshot and restore carry it unchanged.
func TestRecoveryCarriesInvalidatedIntentOfUnpublishedInitialImport(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, t.TempDir())
	noErr(t, err)
	defer store.Close()
	noErr(t, store.CompleteSetup(ctx, t.TempDir(), "open", "", "admin-hash", true))
	now := testImportNow()
	source, err := store.ConfigureImportSource(ctx, ImportSourceInput{RepositoryID: "ghost", URL: "https://example.invalid/team/ghost.git", Mode: ImportModeStandalone, Now: now})
	noErr(t, err)
	run := testImportRun(t, strings.Repeat("c", 32), "ghost", source.SourceGeneration, ImportKindInitial, ImportRunPublishing)
	run.AuthorityRevision = source.AuthorityRevision
	noErr(t, store.BeginImportRun(ctx, run))
	intent := ImportIntent{
		ID: strings.Repeat("d", 32), RepositoryID: "ghost", RunID: run.ID,
		SourceGeneration: source.SourceGeneration, AuthorityRevision: source.AuthorityRevision, Status: ImportIntentPlanning,
		Expected: map[string]string{"refs/heads/main": ""},
		Desired:  map[string]string{"refs/heads/main": strings.Repeat("e", 40)},
		Observed: map[string]string{"refs/heads/main": strings.Repeat("e", 40)},
		Retained: map[string]string{}, CreatedAt: now, UpdatedAt: now,
	}
	noErr(t, store.CreateImportIntent(ctx, intent))
	if _, _, err := store.InterruptImportAuthority(ctx, now); err != nil {
		t.Fatal(err)
	}
	noErr(t, store.UpdateImportIntent(ctx, intent.ID, ImportIntentInvalidated, "", "", "unpublished initial directory removed with proof", now))
	snapshot, err := store.RecoverySnapshot(ctx)
	if err != nil {
		t.Fatalf("snapshot refused a settled intent without a repository: %v", err)
	}
	restored, err := Open(ctx, t.TempDir())
	noErr(t, err)
	defer restored.Close()
	if err := restored.RestoreRecoveryState(ctx, t.TempDir(), snapshot); err != nil {
		t.Fatalf("restore: %v", err)
	}
	stored, exists, err := restored.ImportIntent(ctx, intent.ID)
	if err != nil || !exists || stored.Status != ImportIntentInvalidated {
		t.Fatalf("restored intent=%+v exists=%v err=%v", stored, exists, err)
	}
	if _, exists, err := restored.Repository(ctx, "ghost"); err != nil || exists {
		t.Fatalf("restore invented a repository exists=%v err=%v", exists, err)
	}
}

func TestClaimImportStagingAdoptsInformationalRow(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	// Reconciliation registers a fresh directory as informational while the run
	// that created it is still claiming the name.
	informational := ImportStaging{
		Name: "run-" + strings.Repeat("a", 32), Token: strings.Repeat("b", 32),
		State: ImportStagingUnknown, Issue: "no authorization record", CreatedAt: testImportNow(),
	}
	noErr(t, store.RegisterImportStaging(ctx, informational))
	claimed := ImportStaging{
		Name: informational.Name, RepositoryID: "project", RunID: strings.Repeat("c", 32),
		Token: strings.Repeat("d", 32), State: ImportStagingActive, CreatedAt: testImportNow(),
	}
	if applied, err := store.ClaimImportStaging(ctx, claimed); err != nil || !applied {
		t.Fatalf("claim applied=%v err=%v", applied, err)
	}
	row, exists, err := store.ImportStaging(ctx, claimed.Name)
	if err != nil || !exists {
		t.Fatalf("claimed row exists=%v err=%v", exists, err)
	}
	if row.State != ImportStagingActive || row.RepositoryID != "project" || row.RunID != claimed.RunID || row.Token != claimed.Token || row.Issue != "" {
		t.Fatalf("claim did not take ownership: %+v", row)
	}
	if count, err := store.TableRowCount(ctx, "import_stagings"); err != nil || count != 1 {
		t.Fatalf("claim changed the row count: %d err=%v", count, err)
	}
	if applied, err := store.ClaimImportStaging(ctx, claimed); err != nil || !applied {
		t.Fatalf("repeated claim applied=%v err=%v", applied, err)
	}
	if _, err := store.ClaimImportStaging(ctx, ImportStaging{Name: claimed.Name, Token: strings.Repeat("e", 32)}); err == nil {
		t.Fatal("claim accepted a missing state")
	}
}

func TestClaimImportStagingDoesNotAdoptAnotherRun(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	foreign := ImportStaging{
		Name: "run-" + strings.Repeat("a", 32), RepositoryID: "project",
		RunID: strings.Repeat("b", 32), Token: strings.Repeat("c", 32),
		State: ImportStagingActive, CreatedAt: testImportNow(),
	}
	noErr(t, store.RegisterImportStaging(ctx, foreign))
	claimed := ImportStaging{
		Name: foreign.Name, RepositoryID: "project", RunID: strings.Repeat("d", 32),
		Token: strings.Repeat("e", 32), State: ImportStagingActive, CreatedAt: testImportNow(),
	}
	if applied, err := store.ClaimImportStaging(ctx, claimed); err != nil || applied {
		t.Fatalf("foreign row claim applied=%v err=%v", applied, err)
	}
	row, exists, err := store.ImportStaging(ctx, foreign.Name)
	if err != nil || !exists || row.RunID != foreign.RunID || row.Token != foreign.Token || row.State != ImportStagingActive {
		t.Fatalf("foreign row was changed: %+v exists=%v err=%v", row, exists, err)
	}
}
