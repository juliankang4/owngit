package recovery

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"owngit/internal/auth"
	"owngit/internal/gitexec"
	"owngit/internal/pullrequest"
	"owngit/internal/repository"
	"owngit/internal/state"
)

// The direct review service was removed, so nothing in the package can
// register, start, complete, or cancel that work any more. These tests write
// the stored rows themselves and check that backup and restore still carry the
// history, drop the machine-local parts, and leave a restored store usable.

const (
	recoverySecret      = "synthetic-provider-secret"
	recoveryReplacement = "replacement-synthetic-secret"
	recoveryReviewEvent = "77777777777777777777777777777777"
)

func TestBackupV6RoundTripPreservesTerminalDirectReviewDisconnectedAndSecretFree(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	store, manager := newBackupStore(t, root)
	work := filepath.Join(root, "backup-work")
	remote, err := manager.Path("project")
	if err != nil {
		store.Close()
		t.Fatal(err)
	}
	baseOID := gitOutput(t, work, "rev-parse", "HEAD")
	runGit(t, work, "remote", "add", "origin", remote)
	runGit(t, work, "checkout", "-b", "direct-review")
	if err := os.WriteFile(filepath.Join(work, "review.txt"), []byte("review me\n"), 0o600); err != nil {
		store.Close()
		t.Fatal(err)
	}
	runGit(t, work, "add", ".")
	runGit(t, work, "commit", "-m", "direct review change")
	headOID := gitOutput(t, work, "rev-parse", "HEAD")
	runGit(t, work, "push", "origin", "HEAD:refs/heads/direct-review")
	pullRequests := &pullrequest.Service{Store: store, Repositories: manager}
	view, err := pullRequests.Create(ctx, pullrequest.CreateInput{
		Repository: "project", Title: "Portable direct review", SourceBranch: "direct-review", TargetBranch: "main",
		ReviewChoice: "request", SourceOID: headOID, TargetOID: baseOID,
	})
	if err != nil {
		store.Close()
		t.Fatal(err)
	}

	// The pending review a stored automatic request was bound to carries an
	// identity the old service keyed it by.
	now := time.Unix(1_800_100_000, 0).UTC()
	if _, err := store.AppendPullRequestReview(ctx, state.PullRequestReview{
		RepositoryID: "project", PullRequestNumber: view.Number, SourceOID: headOID, TargetOID: baseOID,
		Status: state.ReviewPending, Provenance: state.ReviewProvenanceRequest, ReviewEventID: recoveryReviewEvent,
		CreatedAt: now,
	}); err != nil {
		store.Close()
		t.Fatal(err)
	}

	db := openStateSQL(t, filepath.Join(root, "source-state"))
	settings := recoveryDirectReviewSettings(now)
	insertRecoverySettings(t, db, settings)
	insertRecoveryProbe(t, db, recoveryDirectReviewProbe(settings, now.Add(time.Second), state.DirectReviewPhaseTerminal), now.Add(3*time.Second))
	manual := recoveryDirectReviewRequest(settings, view.Number, headOID, baseOID, now.Add(3*time.Second))
	manual.Phase, manual.TerminalAt = state.DirectReviewPhaseTerminal, timePointer(now.Add(5*time.Second))
	manual.RunningAt = timePointer(now.Add(4 * time.Second))
	manual.InitialContextBytes = 64
	manual.Result = &state.DirectReviewResult{Outcome: "completed", RequestedModel: settings.Model, Requests: 1, InitialContextBytes: manual.InitialContextBytes}
	manual.RegistrationDigest, _ = state.DirectReviewRequestDigest(manual)
	insertRecoveryRequest(t, db, manual)
	automatic := recoveryDirectReviewRequest(settings, view.Number, headOID, baseOID, now.Add(5*time.Second))
	automatic.RequestID = strings.Repeat("8", 32)
	automatic.TriggerKind, automatic.SourceEventKey = state.DirectReviewTriggerAutomaticPR, recoveryReviewEvent
	automatic.ConsentVersion = "automatic-v1"
	automatic.Phase, automatic.TerminalAt = state.DirectReviewPhaseTerminal, timePointer(now.Add(6*time.Second))
	automatic.Result = &state.DirectReviewResult{Outcome: state.DirectReviewOutcomeCapacityUnavailable}
	automatic.RegistrationDigest, _ = state.DirectReviewRequestDigest(automatic)
	insertRecoveryRequest(t, db, automatic)

	// A task context binds one stored request to a check attempt.
	task, err := store.CreateTask(ctx, "project", "Portable task context", now)
	if err != nil {
		store.Close()
		t.Fatal(err)
	}
	attempt := state.CheckAttempt{
		ID: strings.Repeat("4", 32), TaskID: task.ID, RepositoryID: "project", RevisionOID: headOID,
		WorktreeState: state.WorktreeClean, StartedAt: now, CreatedAt: now,
		Protection: state.ProtectionUnknown, ExecutionScope: state.ExecutionScopeInherited,
		CredentialID: strings.Repeat("5", 32), Checks: []state.CheckDefinition{{Name: "test", Command: "go test ./..."}},
	}
	_, attempt, err = store.RegisterCheckAttempt(ctx, attempt)
	if err != nil {
		store.Close()
		t.Fatal(err)
	}
	contextRecord := state.DirectReviewTaskContext{
		ContextID: strings.Repeat("6", 32), RepositoryID: "project", TaskID: task.ID, AttemptID: attempt.ID,
		CredentialID: attempt.CredentialID, BaseOID: baseOID, HeadOID: headOID, PullRequestNumber: view.Number,
		ObservedSourceOID: headOID, ObservedTargetOID: baseOID, CreatedAt: now.Add(7 * time.Second),
	}
	contextRecord.RegistrationDigest, _ = state.DirectReviewTaskContextDigest(contextRecord)
	insertRecoveryContext(t, db, contextRecord)
	scoped := recoveryDirectReviewRequest(settings, 0, headOID, baseOID, now.Add(8*time.Second))
	scoped.RequestID = strings.Repeat("9", 32)
	scoped.TriggerKind, scoped.SourceEventKey = state.DirectReviewTriggerAutomaticTask, attempt.ID
	scoped.TaskID, scoped.AttemptID = task.ID, attempt.ID
	scoped.PullRequestNumber, scoped.ObservedSourceOID, scoped.ObservedTargetOID = 0, "", ""
	scoped.EffectiveBaseOID, scoped.EffectiveHeadOID, scoped.DiffMode = "", "", ""
	scoped.ConsentVersion = "automatic-v1"
	scoped.Phase, scoped.TerminalAt = state.DirectReviewPhaseTerminal, timePointer(now.Add(9*time.Second))
	scoped.Result = &state.DirectReviewResult{Outcome: state.DirectReviewOutcomeCapacityUnavailable}
	scoped.RegistrationDigest, _ = state.DirectReviewRequestDigest(scoped)
	insertRecoveryRequest(t, db, scoped)

	backup := filepath.Join(root, "backup-v6")
	if err := Create(ctx, store, manager, backup); err != nil {
		store.Close()
		t.Fatal(err)
	}
	assertBackupExcludesText(t, backup, recoverySecret)
	manifest, err := readManifest(filepath.Join(backup, manifestName))
	if err != nil {
		store.Close()
		t.Fatal(err)
	}
	manifestJSON, err := json.Marshal(manifest)
	if err != nil {
		store.Close()
		t.Fatal(err)
	}
	// Portable settings keep the connection generation and nothing local, and
	// only finished work travels.
	if manifest.Version != backupVersion || bytesContain(manifestJSON, recoverySecret) || bytesContain(manifestJSON, settings.AuthorityEpoch) {
		store.Close()
		t.Fatalf("format %d manifest leaked local bytes: version=%d secret=%v epoch=%v", backupVersion, manifest.Version,
			bytesContain(manifestJSON, recoverySecret), bytesContain(manifestJSON, settings.AuthorityEpoch))
	}
	if len(manifest.DirectReviewSettings) != 1 || len(manifest.DirectReviewRequests) != 3 || len(manifest.DirectReviewTaskContexts) != 1 {
		store.Close()
		t.Fatalf("format 6 record counts settings=%d requests=%d contexts=%d", len(manifest.DirectReviewSettings),
			len(manifest.DirectReviewRequests), len(manifest.DirectReviewTaskContexts))
	}
	// Only the settings objects are searched for the local columns, because a
	// credential identifier legitimately stays in the check attempts and task
	// contexts that travel beside them. The column count is pinned as well, so a
	// newly added local column cannot pass unnoticed.
	settingsJSON, err := json.Marshal(manifest.DirectReviewSettings)
	if err != nil {
		store.Close()
		t.Fatal(err)
	}
	var settingsObjects []map[string]json.RawMessage
	if err := json.Unmarshal(settingsJSON, &settingsObjects); err != nil {
		store.Close()
		t.Fatal(err)
	}
	if len(settingsObjects) != 1 {
		store.Close()
		t.Fatalf("portable settings objects=%d", len(settingsObjects))
	}
	for _, key := range []string{
		"credential_id", "credential_attached", "authority_epoch", "probe_fingerprint", "probe_request_id",
		"probe_updated_at", "automatic_pr_enabled", "automatic_task_enabled", "automatic_consent_active",
		"automatic_consent_version", "automatic_consent_digest",
	} {
		if _, present := settingsObjects[0][key]; present {
			store.Close()
			t.Fatalf("portable settings carry the local column %q", key)
		}
	}
	if len(settingsObjects[0]) != 12 {
		store.Close()
		t.Fatalf("portable settings have %d keys, want 12", len(settingsObjects[0]))
	}
	if portable := manifest.DirectReviewSettings[0]; portable.ConnectionVersion != settings.ConnectionVersion ||
		portable.Model != settings.Model || portable.InstructionVersion != settings.InstructionVersion {
		store.Close()
		t.Fatalf("portable settings lost their configuration: %+v", portable)
	}
	for _, request := range manifest.DirectReviewRequests {
		if request.Phase != state.DirectReviewPhaseTerminal || state.DirectReviewRequest(request).Result == nil {
			store.Close()
			t.Fatalf("portable request is not terminal: %+v", request)
		}
	}
	if manifest.PullRequestReviews[len(manifest.PullRequestReviews)-1].ReviewEventID != recoveryReviewEvent {
		store.Close()
		t.Fatalf("format 6 omitted review event identity: %+v", manifest.PullRequestReviews)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	restoredState := canonicalTestTarget(t, filepath.Join(root, "restored-state"))
	restoredRepositories := canonicalTestTarget(t, filepath.Join(root, "restored-repositories"))
	if err := Restore(ctx, backup, restoredState, restoredRepositories, ""); err != nil {
		t.Fatal(err)
	}
	restored, err := state.Open(ctx, restoredState)
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	restoredDB := openStateSQL(t, restoredState)
	defer restoredDB.Close()

	// A restored request must keep the digest and the result text it was stored
	// with, so nothing a later reader compares has moved.
	for _, requestID := range []string{manual.RequestID, automatic.RequestID, scoped.RequestID} {
		wantDigest, wantResult := storedRequest(t, db, requestID)
		gotDigest, gotResult := storedRequest(t, restoredDB, requestID)
		if gotDigest != wantDigest || gotResult != wantResult {
			t.Fatalf("restored request %q digest=%q result=%q", requestID, gotDigest, gotResult)
		}
	}
	if section := countRows(t, restoredDB, "direct_review_probes"); section != 0 {
		t.Fatalf("machine-local probes were restored: %d", section)
	}
	if count := countRows(t, restoredDB, "direct_review_credentials"); count != 0 {
		t.Fatalf("credential values were restored: %d", count)
	}
	// Local authority is gone, the connection generation is not, so the rows
	// the backup carried stay readable.
	credentialID, authorityEpoch, connectionVersion, probeFingerprint := storedSettings(t, restoredDB)
	if credentialID != "" || probeFingerprint != "" || connectionVersion != settings.ConnectionVersion ||
		authorityEpoch == settings.AuthorityEpoch || authorityEpoch == "" {
		t.Fatalf("restored settings credential=%q epoch=%q version=%d probe=%q", credentialID, authorityEpoch, connectionVersion, probeFingerprint)
	}
	if digest := storedContextDigest(t, restoredDB, contextRecord.ContextID); digest != contextRecord.RegistrationDigest {
		t.Fatalf("restored task context digest=%q", digest)
	}
	restoredReview, exists, err := restored.PullRequestReviewForRevision(ctx, "project", view.Number, headOID, baseOID)
	if err != nil || !exists || restoredReview.ReviewEventID != recoveryReviewEvent {
		t.Fatalf("restored review event=%+v exists=%v err=%v", restoredReview, exists, err)
	}

	// Reconciliation over restored rows is a second pass, not new work.
	before := directReviewFacts(t, restoredDB)
	if err := restored.ReconcileDirectReviewInterruptions(ctx, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if after := directReviewFacts(t, restoredDB); after != before {
		t.Fatalf("reconciliation changed restored history:\nfirst=%s\nagain=%s", before, after)
	}

	rebackup := filepath.Join(root, "rebackup-v6")
	restoredRunner, err := gitexec.New("", filepath.Join(restoredState, "runtime"))
	if err != nil {
		t.Fatal(err)
	}
	restoredManager := &repository.Manager{Store: restored, Git: restoredRunner, Locks: gitexec.NewLocks(), Root: restoredRepositories}
	if err := Create(ctx, restored, restoredManager, rebackup); err != nil {
		t.Fatal(err)
	}
	rebacked, err := readManifest(filepath.Join(rebackup, manifestName))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(rebacked.DirectReviewSettings, manifest.DirectReviewSettings) ||
		!reflect.DeepEqual(rebacked.DirectReviewTaskContexts, manifest.DirectReviewTaskContexts) ||
		!reflect.DeepEqual(rebacked.DirectReviewRequests, manifest.DirectReviewRequests) {
		t.Fatalf("format 6 re-backup changed direct review state:\nfirst=%+v\nagain=%+v", manifest.DirectReviewRequests, rebacked.DirectReviewRequests)
	}

	// A later attachment is a new generation, so pre-backup rows stay valid.
	attachRecoveryCredential(t, restoredDB, strings.Repeat("f", 32), now.Add(time.Hour))
	credentialID, _, connectionVersion, probeFingerprint = storedSettings(t, restoredDB)
	if credentialID != strings.Repeat("f", 32) || connectionVersion != settings.ConnectionVersion+1 || probeFingerprint != "" {
		t.Fatalf("post-restore attachment credential=%q version=%d probe=%q", credentialID, connectionVersion, probeFingerprint)
	}
}

// TestGenuineLegacyBackupRestoresWithoutTheRemovedService restores a format 6
// backup written before the removal and re-backups it.
//
// The fixture is a retained backup of synthetic data. Its manifest digest is
// pinned so a changed fixture cannot silently weaken this test.
func TestGenuineLegacyBackupRestoresWithoutTheRemovedService(t *testing.T) {
	const manifestDigest = "932667eb4abc6cca01d3ecab50dadd5f9232a7e55e2a264b2d78e49ca1943455"
	input := filepath.Join("testdata", "legacy-backup-v6")
	content, err := os.ReadFile(filepath.Join(input, manifestName))
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(content)
	if hex.EncodeToString(sum[:]) != manifestDigest {
		t.Fatalf("legacy fixture digest=%s", hex.EncodeToString(sum[:]))
	}
	before, err := readManifest(filepath.Join(input, manifestName))
	if err != nil {
		t.Fatal(err)
	}
	if before.Version != 6 || len(before.DirectReviewRequests) != 2 || len(before.DirectReviewTaskContexts) != 1 || len(before.DirectReviewSettings) != 1 {
		t.Fatalf("legacy fixture does not hold the expected history: %+v", before)
	}

	ctx := context.Background()
	root := t.TempDir()
	statePath := canonicalTestTarget(t, filepath.Join(root, "state"))
	repositoryPath := canonicalTestTarget(t, filepath.Join(root, "repositories"))
	if err := Restore(ctx, input, statePath, repositoryPath, ""); err != nil {
		t.Fatal(err)
	}
	store, err := state.Open(ctx, statePath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	db := openStateSQL(t, statePath)

	// Every stored row must survive the trip, and the review identity an
	// automatic request was bound to must still be there.
	if count := countRows(t, db, "direct_review_credentials"); count != 0 {
		t.Fatalf("legacy credential values were restored: %d", count)
	}
	for _, request := range before.DirectReviewRequests {
		digest, resultJSON := storedRequest(t, db, request.RequestID)
		wantResult, err := json.Marshal(request.Result)
		if err != nil {
			t.Fatal(err)
		}
		if digest != request.RegistrationDigest || resultJSON != string(wantResult) {
			t.Fatalf("restored request %q digest=%q result=%q", request.RequestID, digest, resultJSON)
		}
		if request.TriggerKind == state.DirectReviewTriggerAutomaticPR {
			review, exists, err := store.PullRequestReviewForRevision(ctx, request.RepositoryID, request.PullRequestNumber, request.ObservedSourceOID, request.ObservedTargetOID)
			if err != nil || !exists || review.ReviewEventID != request.SourceEventKey {
				t.Fatalf("restored review event=%+v exists=%v err=%v", review, exists, err)
			}
		}
	}
	if err := store.ReconcileDirectReviewInterruptions(ctx, time.Unix(1_900_000_000, 0).UTC()); err != nil {
		t.Fatal(err)
	}
	for _, request := range before.DirectReviewRequests {
		if phase := storedRequestPhase(t, db, request.RequestID); phase != state.DirectReviewPhaseTerminal {
			t.Fatalf("legacy request %q is not terminal after reconciliation: %s", request.RequestID, phase)
		}
	}

	runner, err := gitexec.New("", filepath.Join(statePath, "runtime"))
	if err != nil {
		t.Fatal(err)
	}
	manager := &repository.Manager{Store: store, Git: runner, Locks: gitexec.NewLocks(), Root: repositoryPath}
	output := filepath.Join(root, "rebackup")
	if err := Create(ctx, store, manager, output); err != nil {
		t.Fatal(err)
	}
	after, err := readManifest(filepath.Join(output, manifestName))
	if err != nil {
		t.Fatal(err)
	}
	// The fixture records its times in one zone and a new backup writes
	// them in the process zone. The instants must survive; the zone is
	// presentation.
	beforeState, afterState := recoveryState(before), recoveryState(after)
	utcTimes(reflect.ValueOf(&beforeState))
	utcTimes(reflect.ValueOf(&afterState))
	if !reflect.DeepEqual(beforeState, afterState) {
		t.Fatal("legacy portable state changed after restore and backup")
	}
	for index := range before.Repositories {
		if !reflect.DeepEqual(before.Repositories[index].Refs, after.Repositories[index].Refs) ||
			!reflect.DeepEqual(before.Repositories[index].Head, after.Repositories[index].Head) {
			t.Fatal("legacy repository refs or HEAD changed")
		}
	}
	assertBackupExcludesText(t, output, "portable-backup-must-not-contain-this-secret")
}

func TestBackupV6RejectsUnknownDirectReviewFieldsDuringStrictDecode(t *testing.T) {
	for _, test := range []struct {
		name    string
		content string
		field   string
	}{
		{name: "settings", content: `{"format":"owngit-offline-backup","version":6,"direct_review_settings":[{"unknown_setting":true}]}`, field: "unknown_setting"},
		{name: "request", content: `{"format":"owngit-offline-backup","version":6,"direct_review_requests":[{"unknown_request":true}]}`, field: "unknown_request"},
		{name: "nested result", content: `{"format":"owngit-offline-backup","version":6,"direct_review_requests":[{"result":{"unknown_result":true}}]}`, field: "unknown_result"},
	} {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "manifest.json")
			if err := os.WriteFile(path, []byte(test.content), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := readManifest(path); err == nil || !strings.Contains(err.Error(), test.field) {
				t.Fatalf("format 6 accepted unknown field %q: %v", test.field, err)
			}
		})
	}
}

func TestBackupVersionFiveRemainsReadableAndCannotCarryDirectReviewFields(t *testing.T) {
	hash, _ := auth.HashPassword("admin-password")
	manifest := Manifest{Format: backupFormat, Version: checkBackupVersion, CreatedAt: time.Now().UTC(), AccessMode: "open", AdminHash: hash}
	path := filepath.Join(t.TempDir(), manifestName)
	writeManifestFile(t, path, manifest)
	read, err := readManifest(path)
	if err != nil || validateManifest(read) != nil {
		t.Fatalf("format 5 was not readable: read=%v validate=%v", err, validateManifest(read))
	}
	manifest.DirectReviewSettings = []DirectReviewSettingsManifest{{RepositoryID: "project"}}
	writeManifestFile(t, path, manifest)
	read, err = readManifest(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := validateManifest(read); err == nil || !strings.Contains(err.Error(), "unsupported direct review metadata") {
		t.Fatalf("format 5 accepted format 6 fields: %v", err)
	}
	manifest.DirectReviewSettings = nil
	manifest.PullRequestReviews = []PullRequestReviewManifest{{ReviewEventID: strings.Repeat("1", 32)}}
	if err := validateManifest(manifest); err == nil || !strings.Contains(err.Error(), "unsupported review event identity") {
		t.Fatalf("format 5 accepted a format 6 review event identity: %v", err)
	}
}

// openStateSQL opens the state database on a second connection, so a stored row
// can be written without any store operation.
func openStateSQL(t *testing.T, stateDir string) *sql.DB {
	t.Helper()
	path, err := filepath.Abs(filepath.Join(stateDir, "owngit.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path)+"?_txlock=immediate")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.ExecContext(context.Background(), `PRAGMA foreign_keys=ON`); err != nil {
		t.Fatal(err)
	}
	return db
}

func recoveryDirectReviewLimits() (state.DirectReviewProviderLimits, state.DirectReviewRepositoryLimits) {
	return state.DirectReviewProviderLimits{
		MaxRequestBytes: 1 << 20, MaxContextBytes: 128 << 10, MaxResponseBytes: 128 << 10,
		MaxToolOutputBytes: 64 << 10, MaxToolRounds: 4, MaxToolCalls: 8, MaxOutputTokens: 2048, MaxDurationMS: 5_000,
	}, state.DirectReviewRepositoryLimits{
		MaxInitialContextBytes: 64 << 10, MaxDirectoryBytes: 64 << 10, MaxFileChunkBytes: 8 << 10,
		MaxFilePrefixBytes: 64 << 10, MaxToolOutputBytes: 32 << 10, MaxOperationDurationMS: 5_000,
	}
}

func recoveryDirectReviewSettings(now time.Time) state.DirectReviewSettings {
	provider, repository := recoveryDirectReviewLimits()
	return state.DirectReviewSettings{
		RepositoryID: "project", ConfigurationVersion: 1, Protocol: state.DirectReviewProtocolOpenAIResponses,
		Endpoint: "https://provider.example.invalid/v1/responses", Model: "synthetic-model",
		AuthenticationMode: state.DirectReviewAuthenticationStored, ProviderLimits: provider, RepositoryLimits: repository,
		InstructionVersion: "direct-review-v1", CredentialID: strings.Repeat("3", 32), CredentialAttached: true,
		ConnectionVersion: 1, AuthorityEpoch: strings.Repeat("a", 32),
		ProbeFingerprint: strings.Repeat("b", 64), ProbeRequestID: strings.Repeat("c", 32), ProbeUpdatedAt: &now,
		CreatedAt: now, UpdatedAt: now,
	}
}

func recoveryDirectReviewProbe(settings state.DirectReviewSettings, created time.Time, phase string) state.DirectReviewProbe {
	probe := state.DirectReviewProbe{
		RequestID: strings.Repeat("1", 32), RepositoryID: settings.RepositoryID, CapabilityFingerprint: strings.Repeat("2", 64),
		ConfigurationVersion: settings.ConfigurationVersion, ConnectionVersion: settings.ConnectionVersion,
		ConnectionFingerprint: state.DirectReviewConnectionFingerprint(settings), Protocol: settings.Protocol, Endpoint: settings.Endpoint,
		Model: settings.Model, AuthenticationMode: settings.AuthenticationMode, ProviderLimits: settings.ProviderLimits,
		DisclosureVersion: "probe-v1", DisclosureDigest: strings.Repeat("4", 64), Phase: phase, CreatedAt: created,
	}
	probe.RegistrationDigest, _ = state.DirectReviewProbeDigest(probe)
	return probe
}

func recoveryDirectReviewRequest(settings state.DirectReviewSettings, number int64, sourceOID, targetOID string, created time.Time) state.DirectReviewRequest {
	request := state.DirectReviewRequest{
		RequestID: strings.Repeat("2", 32), RepositoryID: "project", TriggerKind: state.DirectReviewTriggerManual,
		PullRequestNumber: number, ObservedSourceOID: sourceOID, ObservedTargetOID: targetOID,
		EffectiveBaseOID: targetOID, EffectiveHeadOID: sourceOID, DiffMode: state.DirectReviewDiffTwoCommit,
		ConfigurationVersion: settings.ConfigurationVersion, ConnectionVersion: settings.ConnectionVersion,
		ConnectionFingerprint: state.DirectReviewConnectionFingerprint(settings), Protocol: settings.Protocol, Endpoint: settings.Endpoint, Model: settings.Model,
		AuthenticationMode: settings.AuthenticationMode, ProviderLimits: settings.ProviderLimits, RepositoryLimits: settings.RepositoryLimits,
		InstructionVersion: settings.InstructionVersion, ConsentVersion: "manual-v1", ConsentDigest: strings.Repeat("5", 64),
		DisclosureVersion: "disclosure-v1", DisclosureDigest: strings.Repeat("6", 64), Phase: state.DirectReviewPhasePreparing,
		CreatedAt: created, PreparingAt: &created,
	}
	request.RegistrationDigest, _ = state.DirectReviewRequestDigest(request)
	return request
}

func insertRecoverySettings(t *testing.T, db *sql.DB, settings state.DirectReviewSettings) {
	t.Helper()
	providerJSON, repositoryJSON := recoveryLimitsJSON(t, settings.ProviderLimits, settings.RepositoryLimits)
	execState(t, db, `INSERT INTO direct_review_repository_settings(
		repository_id,configuration_version,protocol,endpoint,model,authentication_mode,provider_limits_json,repository_limits_json,
		instruction_version,credential_id,connection_version,authority_epoch,probe_fingerprint,probe_request_id,probe_updated_at,
		created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		settings.RepositoryID, settings.ConfigurationVersion, settings.Protocol, settings.Endpoint, settings.Model,
		settings.AuthenticationMode, providerJSON, repositoryJSON, settings.InstructionVersion, settings.CredentialID,
		settings.ConnectionVersion, settings.AuthorityEpoch, settings.ProbeFingerprint, settings.ProbeRequestID,
		settings.ProbeUpdatedAt.Unix(), settings.CreatedAt.Unix(), settings.UpdatedAt.Unix())
	execState(t, db, `INSERT INTO direct_review_credentials(id,repository_id,label,value,created_at,updated_at)
		VALUES(?,?,?,?,?,?)`, settings.CredentialID, settings.RepositoryID, "Synthetic provider", recoverySecret,
		settings.CreatedAt.Unix(), settings.CreatedAt.Unix())
}

// attachRecoveryCredential inserts a credential and advances the connection
// generation, which is what a later attachment must do.
func attachRecoveryCredential(t *testing.T, db *sql.DB, id string, now time.Time) {
	t.Helper()
	execState(t, db, `INSERT INTO direct_review_credentials(id,repository_id,label,value,created_at,updated_at)
		VALUES(?,?,?,?,?,?)`, id, "project", "Replacement token", recoveryReplacement, now.Unix(), now.Unix())
	execState(t, db, `UPDATE direct_review_repository_settings SET credential_id=?,connection_version=connection_version+1 WHERE repository_id='project'`, id)
}

// insertRecoveryProbe writes a finished probe row. Probe rows are machine
// local, so nothing in the recovery package reads them back.
func insertRecoveryProbe(t *testing.T, db *sql.DB, probe state.DirectReviewProbe, terminalAt time.Time) {
	t.Helper()
	providerJSON, _ := recoveryLimitsJSON(t, probe.ProviderLimits, state.DirectReviewRepositoryLimits{})
	started := probe.CreatedAt.Add(time.Second)
	probe.RunningAt = &started
	probe.TerminalAt = &terminalAt
	probe.Result = &state.DirectReviewResult{Outcome: "supported", Requests: 1}
	resultJSON, err := json.Marshal(probe.Result)
	if err != nil {
		t.Fatal(err)
	}
	execState(t, db, `INSERT INTO direct_review_probes(request_id,repository_id,registration_digest,capability_fingerprint,
		configuration_version,connection_version,connection_fingerprint,protocol,endpoint,model,authentication_mode,
		provider_limits_json,disclosure_version,disclosure_digest,phase,created_at,running_at,terminal_at,result_json)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, probe.RequestID, probe.RepositoryID, probe.RegistrationDigest,
		probe.CapabilityFingerprint, probe.ConfigurationVersion, probe.ConnectionVersion, probe.ConnectionFingerprint,
		probe.Protocol, probe.Endpoint, probe.Model, probe.AuthenticationMode, providerJSON, probe.DisclosureVersion,
		probe.DisclosureDigest, probe.Phase, probe.CreatedAt.Unix(), started.Unix(), terminalAt.Unix(), string(resultJSON))
}

func insertRecoveryRequest(t *testing.T, db *sql.DB, request state.DirectReviewRequest) {
	t.Helper()
	providerJSON, repositoryJSON := recoveryLimitsJSON(t, request.ProviderLimits, request.RepositoryLimits)
	resultJSON, err := json.Marshal(request.Result)
	if err != nil {
		t.Fatal(err)
	}
	truncated := 0
	if request.InitialContextTruncated {
		truncated = 1
	}
	execState(t, db, `INSERT INTO direct_review_requests(request_id,repository_id,trigger_kind,source_event_key,
		pull_request_number,task_id,attempt_id,observed_source_oid,observed_target_oid,effective_base_oid,effective_head_oid,diff_mode,
		configuration_version,connection_version,connection_fingerprint,protocol,endpoint,model,authentication_mode,
		provider_limits_json,repository_limits_json,instruction_version,consent_version,consent_digest,disclosure_version,
		disclosure_digest,registration_digest,phase,cancel_requested_at,created_at,preparing_at,running_at,terminal_at,
		initial_context_bytes,initial_context_truncated,result_json)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, request.RequestID, request.RepositoryID,
		request.TriggerKind, request.SourceEventKey, request.PullRequestNumber, request.TaskID, request.AttemptID,
		request.ObservedSourceOID, request.ObservedTargetOID, request.EffectiveBaseOID, request.EffectiveHeadOID,
		request.DiffMode, request.ConfigurationVersion, request.ConnectionVersion, request.ConnectionFingerprint,
		request.Protocol, request.Endpoint, request.Model, request.AuthenticationMode, providerJSON, repositoryJSON,
		request.InstructionVersion, request.ConsentVersion, request.ConsentDigest, request.DisclosureVersion,
		request.DisclosureDigest, request.RegistrationDigest, request.Phase, nullTime(request.CancelRequestedAt),
		request.CreatedAt.Unix(), nullTime(request.PreparingAt), nullTime(request.RunningAt), nullTime(request.TerminalAt),
		request.InitialContextBytes, truncated, string(resultJSON))
}

func insertRecoveryContext(t *testing.T, db *sql.DB, contextRecord state.DirectReviewTaskContext) {
	t.Helper()
	execState(t, db, `INSERT INTO direct_review_task_contexts(context_id,repository_id,task_id,attempt_id,credential_id,
		base_oid,head_oid,pull_request_number,observed_source_oid,observed_target_oid,registration_digest,created_at)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`, contextRecord.ContextID, contextRecord.RepositoryID, contextRecord.TaskID,
		contextRecord.AttemptID, contextRecord.CredentialID, contextRecord.BaseOID, contextRecord.HeadOID,
		contextRecord.PullRequestNumber, contextRecord.ObservedSourceOID, contextRecord.ObservedTargetOID,
		contextRecord.RegistrationDigest, contextRecord.CreatedAt.Unix())
}

func recoveryLimitsJSON(t *testing.T, provider state.DirectReviewProviderLimits, repository state.DirectReviewRepositoryLimits) (string, string) {
	t.Helper()
	providerJSON, err := json.Marshal(provider)
	if err != nil {
		t.Fatal(err)
	}
	repositoryJSON, err := json.Marshal(repository)
	if err != nil {
		t.Fatal(err)
	}
	return string(providerJSON), string(repositoryJSON)
}

func storedRequest(t *testing.T, db *sql.DB, requestID string) (string, string) {
	t.Helper()
	var digest, result string
	if err := db.QueryRowContext(context.Background(),
		`SELECT registration_digest,result_json FROM direct_review_requests WHERE request_id=?`, requestID).Scan(&digest, &result); err != nil {
		t.Fatal(err)
	}
	return digest, result
}

func storedRequestPhase(t *testing.T, db *sql.DB, requestID string) string {
	t.Helper()
	var phase string
	if err := db.QueryRowContext(context.Background(),
		`SELECT phase FROM direct_review_requests WHERE request_id=?`, requestID).Scan(&phase); err != nil {
		t.Fatal(err)
	}
	return phase
}

func storedContextDigest(t *testing.T, db *sql.DB, contextID string) string {
	t.Helper()
	var digest string
	if err := db.QueryRowContext(context.Background(),
		`SELECT registration_digest FROM direct_review_task_contexts WHERE context_id=?`, contextID).Scan(&digest); err != nil {
		t.Fatal(err)
	}
	return digest
}

// storedSettings reads the local authority columns and the connection
// generation a settings row carries.
func storedSettings(t *testing.T, db *sql.DB) (credentialID, authorityEpoch string, connectionVersion int64, probeFingerprint string) {
	t.Helper()
	if err := db.QueryRowContext(context.Background(), `SELECT credential_id,authority_epoch,connection_version,probe_fingerprint
		FROM direct_review_repository_settings WHERE repository_id='project'`).
		Scan(&credentialID, &authorityEpoch, &connectionVersion, &probeFingerprint); err != nil {
		t.Fatal(err)
	}
	return credentialID, authorityEpoch, connectionVersion, probeFingerprint
}

func directReviewFacts(t *testing.T, db *sql.DB) string {
	t.Helper()
	var facts string
	if err := db.QueryRowContext(context.Background(), `SELECT group_concat(fact,'|') FROM (
		SELECT request_id||'/'||phase||'/'||COALESCE(terminal_at,0)||'/'||result_json AS fact FROM direct_review_requests
		ORDER BY fact)`).Scan(&facts); err != nil {
		t.Fatal(err)
	}
	return facts
}

func countRows(t *testing.T, db *sql.DB, table string) int {
	t.Helper()
	var count int
	if err := db.QueryRowContext(context.Background(), `SELECT count(*) FROM `+table).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func execState(t *testing.T, db *sql.DB, query string, arguments ...any) {
	t.Helper()
	if _, err := db.ExecContext(context.Background(), query, arguments...); err != nil {
		t.Fatal(err)
	}
}

func timePointer(value time.Time) *time.Time { return &value }

func nullTime(value *time.Time) any {
	if value == nil {
		return nil
	}
	return value.Unix()
}

func bytesContain(content []byte, value string) bool { return strings.Contains(string(content), value) }

func assertBackupExcludesText(t *testing.T, directory, value string) {
	t.Helper()
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		path := filepath.Join(directory, entry.Name())
		if entry.IsDir() {
			assertBackupExcludesText(t, path, value)
			continue
		}
		content, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if bytesContain(content, value) {
			t.Fatalf("backup file %q contains protected text", path)
		}
	}
}

var timeType = reflect.TypeOf(time.Time{})

// utcTimes rewrites every reachable exported time.Time to UTC so equality
// compares instants rather than the zone a value was written in.
func utcTimes(value reflect.Value) {
	switch value.Kind() {
	case reflect.Pointer, reflect.Interface:
		if !value.IsNil() {
			utcTimes(value.Elem())
		}
	case reflect.Struct:
		if value.Type() == timeType {
			if value.CanSet() {
				value.Set(reflect.ValueOf(value.Interface().(time.Time).UTC()))
			}
			return
		}
		for index := 0; index < value.NumField(); index++ {
			if value.Type().Field(index).IsExported() {
				utcTimes(value.Field(index))
			}
		}
	case reflect.Slice, reflect.Array:
		for index := 0; index < value.Len(); index++ {
			utcTimes(value.Index(index))
		}
	case reflect.Map:
		for _, key := range value.MapKeys() {
			entry := reflect.New(value.Type().Elem()).Elem()
			entry.Set(value.MapIndex(key))
			utcTimes(entry)
			value.SetMapIndex(key, entry)
		}
	}
}
