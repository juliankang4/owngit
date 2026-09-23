package state

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

// These tests own the shape of stored direct review rows.
//
// The service that registered, started, completed, and cancelled that work was
// removed, so the lifecycle rules went with it. What remains is the stored
// shape: the columns an older database has, the digests that describe them,
// and the reconciliation that gives work owned by a dead process an honest
// terminal outcome. Rows are therefore written with SQL here, not with the
// removed operations.

var (
	legacyReviewSourceOID = strings.Repeat("b", 40)
	legacyReviewTargetOID = strings.Repeat("a", 40)
)

func TestStoredLegacyDirectReviewRowsStillRead(t *testing.T) {
	store, now := newDirectReviewStateStore(t)
	settings := insertLegacySettings(t, store, now, func(settings *DirectReviewSettings) {
		settings.CredentialID = strings.Repeat("c", 32)
		settings.ProbeFingerprint = strings.Repeat("d", 64)
		settings.ProbeRequestID = strings.Repeat("e", 32)
		settings.ProbeUpdatedAt = timePointer(now)
	})
	probe := directReviewTestProbe(settings, strings.Repeat("e", 32), now.Add(time.Second))
	insertLegacyProbe(t, store, probe)
	request := directReviewTestRequest(settings, strings.Repeat("2", 32), now.Add(2*time.Second))
	insertLegacyRequest(t, store, request)

	stored, exists, err := readLegacySettings(store, "project")
	if err != nil || !exists {
		t.Fatalf("settings read=%+v exists=%v err=%v", stored, exists, err)
	}
	// The secret stays in its own table, the row keeps only the reference.
	if stored.CredentialID != settings.CredentialID || !stored.CredentialAttached || stored.AuthorityEpoch != settings.AuthorityEpoch {
		t.Fatalf("stored settings lost their attachment or local authority: %+v", stored)
	}
	value, exists, err := readLegacyCredential(store, settings.CredentialID)
	if err != nil || !exists || value != legacySecret {
		t.Fatalf("stored credential value=%q exists=%v err=%v", value, exists, err)
	}
	storedProbe, exists, err := readLegacyProbe(store, probe.RequestID)
	if err != nil || !exists {
		t.Fatalf("probe read=%+v exists=%v err=%v", storedProbe, exists, err)
	}
	if storedProbe.RegistrationDigest != probe.RegistrationDigest || storedProbe.CapabilityFingerprint != probe.CapabilityFingerprint {
		t.Fatalf("stored probe lost its identity: %+v", storedProbe)
	}
	storedRequest, exists, err := readLegacyRequest(store, request.RequestID)
	if err != nil || !exists {
		t.Fatalf("request read=%+v exists=%v err=%v", storedRequest, exists, err)
	}
	if storedRequest.RegistrationDigest != request.RegistrationDigest || storedRequest.Sequence <= 0 ||
		storedRequest.ObservedSourceOID != legacyReviewSourceOID || storedRequest.Phase != DirectReviewPhasePreparing {
		t.Fatalf("stored request lost its sealed registration: %+v", storedRequest)
	}
}

func TestReconcileDirectReviewInterruptionsIsHonestAndIdempotent(t *testing.T) {
	store, now := newDirectReviewStateStore(t)
	settings := insertLegacySettings(t, store, now, nil)
	finished := now.Add(time.Second)
	started := now.Add(2 * time.Second)
	cancelledAt := now.Add(3 * time.Second)
	terminalAt := now.Add(4 * time.Second)

	// An automatic request that was registered and never started, which is what
	// every stored request becomes once nothing runs the work.
	queued := directReviewTestRequest(settings, strings.Repeat("1", 32), now)
	queued.TriggerKind = DirectReviewTriggerAutomaticPR
	queued.SourceEventKey = legacyReviewEventID
	queued.PreparingAt = nil
	queued.Phase = DirectReviewPhaseObserved
	insertLegacyRequest(t, store, resealRequest(t, queued))

	// Preparing, cancelled before start, and running, in that order.
	insertLegacyRequest(t, store, directReviewTestRequest(settings, strings.Repeat("2", 32), now))
	cancelled := directReviewTestRequest(settings, strings.Repeat("3", 32), finished)
	cancelled.CancelRequestedAt = &cancelledAt
	insertLegacyRequest(t, store, cancelled)
	running := directReviewTestRequest(settings, strings.Repeat("4", 32), now)
	running.RunningAt = &started
	running.Phase = DirectReviewPhaseRunning
	running.InitialContextBytes = 512
	running.InitialContextTruncated = true
	insertLegacyRequest(t, store, running)

	// A terminal row is already finished and must stay as it was.
	done := directReviewTestRequest(settings, strings.Repeat("5", 32), now)
	done.Phase = DirectReviewPhaseTerminal
	done.TerminalAt = &terminalAt
	done.Result = &DirectReviewResult{Outcome: DirectReviewOutcomeContextUnavailable}
	insertLegacyRequest(t, store, done)

	insertLegacyProbe(t, store, directReviewTestProbe(settings, strings.Repeat("6", 32), now))
	probeRunning := directReviewTestProbe(settings, strings.Repeat("7", 32), now)
	probeRunning.RunningAt = &started
	probeRunning.Phase = DirectReviewPhaseRunning
	insertLegacyProbe(t, store, probeRunning)
	probeCancelled := directReviewTestProbe(settings, strings.Repeat("8", 32), now)
	probeCancelled.CancelRequestedAt = &cancelledAt
	insertLegacyProbe(t, store, probeCancelled)
	probeDone := directReviewTestProbe(settings, strings.Repeat("9", 32), now)
	// A terminal probe carries a probe outcome: the retained validator refuses
	// the request-only ones such as context_unavailable.
	probeDone.RunningAt = &started
	probeDone.Phase = DirectReviewPhaseTerminal
	probeDone.TerminalAt = &terminalAt
	probeDone.Result = &DirectReviewResult{Outcome: "supported", Requests: 1}
	insertLegacyProbe(t, store, probeDone)

	reconciledAt := now.Add(time.Hour)
	if err := store.ReconcileDirectReviewInterruptions(context.Background(), reconciledAt); err != nil {
		t.Fatal(err)
	}

	for _, item := range []struct {
		requestID string
		outcome   string
		requests  int
	}{
		{strings.Repeat("1", 32), DirectReviewOutcomeInterruptedBeforeSubmission, 0},
		{strings.Repeat("2", 32), DirectReviewOutcomeInterruptedBeforeSubmission, 0},
		{strings.Repeat("3", 32), DirectReviewOutcomeCancelledBeforeSubmission, 0},
		{strings.Repeat("4", 32), DirectReviewOutcomeInterruptedMayHaveSubmitted, 0},
		{strings.Repeat("5", 32), DirectReviewOutcomeContextUnavailable, 0},
	} {
		request, exists, err := readLegacyRequest(store, item.requestID)
		if err != nil || !exists {
			t.Fatalf("request %q read exists=%v err=%v", item.requestID, exists, err)
		}
		// The scanner validates the stored row, so an honest outcome that the
		// old validators would refuse fails here.
		want := reconciledAt
		if item.requestID == strings.Repeat("5", 32) {
			want = terminalAt
		}
		if request.Phase != DirectReviewPhaseTerminal || request.Result == nil ||
			request.Result.Outcome != item.outcome || request.Result.Requests != item.requests || !request.TerminalAt.Equal(want) {
			t.Fatalf("request %q reconciled to %+v", item.requestID, request)
		}
	}
	// A measurement taken before the interruption survives in the outcome.
	interrupted, _, _ := readLegacyRequest(store, strings.Repeat("4", 32))
	if interrupted.Result.InitialContextBytes != 512 || !interrupted.Result.InitialContextTruncated {
		t.Fatalf("the interruption dropped the recorded context measurement: %+v", interrupted.Result)
	}
	for _, item := range []struct {
		probeID string
		outcome string
	}{
		{strings.Repeat("6", 32), DirectReviewOutcomeInterruptedBeforeSubmission},
		{strings.Repeat("7", 32), DirectReviewOutcomeInterruptedMayHaveSubmitted},
		{strings.Repeat("8", 32), DirectReviewOutcomeCancelledBeforeSubmission},
		{strings.Repeat("9", 32), "supported"},
	} {
		probe, exists, err := readLegacyProbe(store, item.probeID)
		if err != nil || !exists || probe.Result == nil || probe.Result.Outcome != item.outcome {
			t.Fatalf("probe %q reconciled to %+v exists=%v err=%v", item.probeID, probe, exists, err)
		}
	}

	before := reconcileFacts(t, store)
	if err := store.ReconcileDirectReviewInterruptions(context.Background(), reconciledAt.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	// Repeating the pass may not move a terminal row or touch its settings.
	if after := reconcileFacts(t, store); after != before {
		t.Fatalf("second reconciliation changed stored facts:\nfirst=%s\nagain=%s", before, after)
	}
	storedSettings, exists, err := readLegacySettings(store, "project")
	if err != nil || !exists || !storedSettings.UpdatedAt.Equal(settings.UpdatedAt) {
		t.Fatalf("reconciliation touched settings: %+v exists=%v err=%v", storedSettings, exists, err)
	}
}

func TestRecoverySnapshotCarriesLegacyHistoryWithoutLocalAuthority(t *testing.T) {
	store, now := newDirectReviewStateStore(t)
	settings := insertLegacySettings(t, store, now, func(settings *DirectReviewSettings) {
		settings.CredentialID = strings.Repeat("c", 32)
		settings.ProbeFingerprint = strings.Repeat("d", 64)
		settings.ProbeRequestID = strings.Repeat("e", 32)
		settings.ProbeUpdatedAt = timePointer(now)
	})
	insertLegacyProbe(t, store, directReviewTestProbe(settings, strings.Repeat("e", 32), now))

	// The review event a stored automatic request was bound to.
	insertLegacyReviewEvent(t, store, now)

	terminalAt := now.Add(time.Second)
	manual := directReviewTestRequest(settings, strings.Repeat("2", 32), now)
	manual.Phase = DirectReviewPhaseTerminal
	manual.TerminalAt = &terminalAt
	manual.Result = &DirectReviewResult{Outcome: DirectReviewOutcomeContextUnavailable}
	insertLegacyRequest(t, store, manual)
	queued := directReviewTestRequest(settings, strings.Repeat("3", 32), now)
	queued.TriggerKind = DirectReviewTriggerAutomaticPR
	queued.SourceEventKey = legacyReviewEventID
	queued.PreparingAt = nil
	queued.Phase = DirectReviewPhaseObserved
	insertLegacyRequest(t, store, resealRequest(t, queued))

	// A task-scoped context binds a stored request to a check attempt.
	ctx := context.Background()
	task, err := store.CreateTask(ctx, "project", "Legacy task", now)
	if err != nil {
		t.Fatal(err)
	}
	attempt := attemptFor(task, legacyReviewSourceOID, now, AttemptPassed)
	attempt.CredentialID = strings.Repeat("5", 32)
	_, attempt, err = store.RegisterCheckAttempt(ctx, attempt)
	if err != nil {
		t.Fatal(err)
	}
	contextRecord := DirectReviewTaskContext{
		ContextID: strings.Repeat("6", 32), RepositoryID: "project", TaskID: task.ID, AttemptID: attempt.ID,
		CredentialID: attempt.CredentialID, BaseOID: legacyReviewTargetOID, HeadOID: legacyReviewSourceOID,
		PullRequestNumber: 1, ObservedSourceOID: legacyReviewSourceOID, ObservedTargetOID: legacyReviewTargetOID,
		CreatedAt: now.Add(2 * time.Second),
	}
	contextRecord.RegistrationDigest, _ = DirectReviewTaskContextDigest(contextRecord)
	insertLegacyTaskContext(t, store, contextRecord)
	scoped := directReviewTestRequest(settings, strings.Repeat("7", 32), now)
	scoped.TriggerKind = DirectReviewTriggerAutomaticTask
	scoped.SourceEventKey = attempt.ID
	scoped.PullRequestNumber = 0
	scoped.TaskID = task.ID
	scoped.AttemptID = attempt.ID
	scoped.ObservedSourceOID = ""
	scoped.ObservedTargetOID = ""
	scoped.EffectiveBaseOID = ""
	scoped.EffectiveHeadOID = ""
	scoped.DiffMode = ""
	scoped.ConsentVersion = "automatic-v1"
	scoped.Phase = DirectReviewPhaseTerminal
	scoped.TerminalAt = &terminalAt
	scoped.Result = &DirectReviewResult{Outcome: DirectReviewOutcomeCapacityUnavailable}
	insertLegacyRequest(t, store, resealRequest(t, scoped))

	// RecoverySnapshot reconciles and validates everything it read, so a stored
	// row the portable rules refuse fails here.
	snapshot, err := store.RecoverySnapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}

	if len(snapshot.DirectReviewSettings) != 1 {
		t.Fatalf("portable settings=%+v", snapshot.DirectReviewSettings)
	}
	portable := snapshot.DirectReviewSettings[0]
	// Attachment, probe authority, and local authority do not travel. The
	// nonsecret connection generation does, so a later attachment cannot revive
	// pre-backup authority.
	if portable.CredentialID != "" || portable.CredentialAttached || portable.AuthorityEpoch != "" ||
		portable.ProbeFingerprint != "" || portable.ProbeRequestID != "" || portable.ProbeUpdatedAt != nil ||
		portable.ConnectionVersion != settings.ConnectionVersion {
		t.Fatalf("portable settings kept local authority: %+v", portable)
	}
	if len(snapshot.DirectReviewRequests) != 3 {
		t.Fatalf("portable requests=%+v", snapshot.DirectReviewRequests)
	}
	for _, request := range snapshot.DirectReviewRequests {
		if request.Phase != DirectReviewPhaseTerminal || request.Result == nil || request.Sequence <= 0 {
			t.Fatalf("portable request=%+v", request)
		}
	}
	if len(snapshot.DirectReviewTaskContexts) != 1 ||
		snapshot.DirectReviewTaskContexts[0].RegistrationDigest != contextRecord.RegistrationDigest {
		t.Fatalf("portable task contexts=%+v", snapshot.DirectReviewTaskContexts)
	}
	events := make([]string, 0, len(snapshot.PullRequestReviews))
	for _, review := range snapshot.PullRequestReviews {
		events = append(events, review.ReviewEventID)
	}
	if !strings.Contains(strings.Join(events, ","), legacyReviewEventID) {
		t.Fatalf("portable reviews lost the review event identity: %+v", events)
	}
	encoded, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(encoded, []byte(legacySecret)) || bytes.Contains(encoded, []byte(settings.AuthorityEpoch)) {
		t.Fatalf("portable snapshot contains local state: %s", encoded)
	}
}

func TestBrokenStoredDirectReviewRowsAreRefused(t *testing.T) {
	// Each case stores one row the old service would not have written and asks
	// the retained scanner to refuse it.
	cases := []struct {
		name  string
		store func(t *testing.T, store *Store, now time.Time) func() error
		want  string
	}{
		{
			name: "attachment without an authentication mode",
			store: func(t *testing.T, store *Store, now time.Time) func() error {
				insertLegacySettings(t, store, now, func(settings *DirectReviewSettings) {
					settings.AuthenticationMode = DirectReviewAuthenticationNone
					settings.CredentialID = strings.Repeat("c", 32)
				})
				return func() error {
					_, _, err := readLegacySettings(store, "project")
					return err
				}
			},
			want: "attachment",
		},
		{
			name: "request whose registration digest was rewritten",
			store: func(t *testing.T, store *Store, now time.Time) func() error {
				settings := insertLegacySettings(t, store, now, nil)
				request := directReviewTestRequest(settings, strings.Repeat("2", 32), now)
				request.RegistrationDigest = strings.Repeat("0", 64)
				insertLegacyRequest(t, store, request)
				return func() error {
					_, _, err := readLegacyRequest(store, strings.Repeat("2", 32))
					return err
				}
			},
			want: "registration digest",
		},
		{
			name: "unfinished request that already has a result",
			store: func(t *testing.T, store *Store, now time.Time) func() error {
				settings := insertLegacySettings(t, store, now, nil)
				request := directReviewTestRequest(settings, strings.Repeat("2", 32), now)
				request.Result = &DirectReviewResult{Outcome: DirectReviewOutcomeContextUnavailable}
				insertLegacyRequest(t, store, request)
				return func() error {
					_, _, err := readLegacyRequest(store, strings.Repeat("2", 32))
					return err
				}
			},
			want: "preparing direct review request",
		},
		{
			name: "terminal probe without a result",
			store: func(t *testing.T, store *Store, now time.Time) func() error {
				settings := insertLegacySettings(t, store, now, nil)
				probe := directReviewTestProbe(settings, strings.Repeat("e", 32), now)
				probe.Phase = DirectReviewPhaseTerminal
				probe.TerminalAt = timePointer(now)
				insertLegacyProbe(t, store, probe)
				return func() error {
					_, _, err := readLegacyProbe(store, strings.Repeat("e", 32))
					return err
				}
			},
			want: "terminal direct review probe",
		},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			store, now := newDirectReviewStateStore(t)
			read := test.store(t, store, now)
			if err := read(); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("read err=%v, want %q", err, test.want)
			}
		})
	}
}

const (
	legacySecret        = "synthetic-provider-secret"
	legacyReviewEventID = "77777777777777777777777777777777"
)

func newDirectReviewStateStore(t *testing.T) (*Store, time.Time) {
	t.Helper()
	ctx := context.Background()
	store := openTestStore(t)
	completeTestSetup(t, store)
	now := time.Unix(1_800_000_000, 0).UTC()
	if err := store.AddRepository(ctx, Repository{ID: "project", Name: "Project", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	execLegacy(t, store, `INSERT INTO pull_requests(repository_id,number,title,source_branch,target_branch,status,created_at,updated_at)
		VALUES('project',1,'Legacy review fixture','feature','main','open',?,?)`, now.Unix(), now.Unix())
	execLegacy(t, store, `INSERT INTO pull_request_revisions(repository_id,pull_request_number,source_oid,target_oid,recorded_at)
		VALUES('project',1,?,?,?)`, legacyReviewSourceOID, legacyReviewTargetOID, now.Unix())
	// A repository with a review choice needs no review service to exist.
	execLegacy(t, store, `INSERT INTO pull_request_reviews(repository_id,pull_request_number,sequence,source_oid,target_oid,status,reviewer_label,provenance,created_at)
		VALUES('project',1,1,?,?,?,'',?,?)`, legacyReviewSourceOID, legacyReviewTargetOID, ReviewNotRequested, ReviewProvenanceDefault, now.Unix())
	return store, now
}

// insertLegacySettings writes a settings row the way the removed service did:
// the attachment and probe authority columns are references, and the secret
// lives in the credential table.
func insertLegacySettings(t *testing.T, store *Store, now time.Time, mutate func(*DirectReviewSettings)) DirectReviewSettings {
	t.Helper()
	settings := legacyDirectReviewSettings(now, mutate)
	providerJSON, repositoryJSON, err := marshalDirectReviewLimits(settings.ProviderLimits, settings.RepositoryLimits)
	if err != nil {
		t.Fatal(err)
	}
	probeUpdated := time.Unix(0, 0)
	if settings.ProbeUpdatedAt != nil {
		probeUpdated = *settings.ProbeUpdatedAt
	}
	execLegacy(t, store, `INSERT INTO direct_review_repository_settings(
		repository_id,configuration_version,protocol,endpoint,model,authentication_mode,provider_limits_json,repository_limits_json,
		instruction_version,credential_id,connection_version,authority_epoch,probe_fingerprint,probe_request_id,probe_updated_at,
		created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		settings.RepositoryID, settings.ConfigurationVersion, settings.Protocol, settings.Endpoint, settings.Model,
		settings.AuthenticationMode, providerJSON, repositoryJSON, settings.InstructionVersion, settings.CredentialID,
		settings.ConnectionVersion, settings.AuthorityEpoch, settings.ProbeFingerprint, settings.ProbeRequestID,
		probeUpdated.Unix(), settings.CreatedAt.Unix(), settings.UpdatedAt.Unix())
	if settings.CredentialID != "" {
		execLegacy(t, store, `INSERT INTO direct_review_credentials(id,repository_id,label,value,created_at,updated_at)
			VALUES(?,?,?,?,?,?)`, settings.CredentialID, settings.RepositoryID, "Synthetic provider", legacySecret, now.Unix(), now.Unix())
	}
	return settings
}

func legacyDirectReviewSettings(now time.Time, mutate func(*DirectReviewSettings)) DirectReviewSettings {
	provider, repository := directReviewTestLimits()
	settings := DirectReviewSettings{
		RepositoryID: "project", ConfigurationVersion: 1, Protocol: DirectReviewProtocolOpenAIResponses,
		Endpoint: "https://provider.example.invalid/v1/responses", Model: "synthetic-model",
		AuthenticationMode: DirectReviewAuthenticationStored, ProviderLimits: provider, RepositoryLimits: repository,
		InstructionVersion: "direct-review-v1", ConnectionVersion: 1, AuthorityEpoch: strings.Repeat("f", 32),
		CreatedAt: now, UpdatedAt: now,
	}
	if mutate != nil {
		mutate(&settings)
	}
	return settings
}

func insertLegacyProbe(t *testing.T, store *Store, probe DirectReviewProbe) {
	t.Helper()
	providerJSON, _, err := marshalDirectReviewLimits(probe.ProviderLimits, minimumDirectReviewRepositoryLimits(probe.ProviderLimits))
	if err != nil {
		t.Fatal(err)
	}
	execLegacy(t, store, `INSERT INTO direct_review_probes(request_id,repository_id,registration_digest,capability_fingerprint,
		configuration_version,connection_version,connection_fingerprint,protocol,endpoint,model,authentication_mode,
		provider_limits_json,disclosure_version,disclosure_digest,phase,cancel_requested_at,created_at,running_at,terminal_at,result_json)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		probe.RequestID, probe.RepositoryID, probe.RegistrationDigest, probe.CapabilityFingerprint, probe.ConfigurationVersion,
		probe.ConnectionVersion, probe.ConnectionFingerprint, probe.Protocol, probe.Endpoint, probe.Model, probe.AuthenticationMode,
		providerJSON, probe.DisclosureVersion, probe.DisclosureDigest, probe.Phase, nullableUnix(probe.CancelRequestedAt),
		probe.CreatedAt.Unix(), nullableUnix(probe.RunningAt), nullableUnix(probe.TerminalAt), legacyResultJSON(t, probe.Result))
}

func insertLegacyRequest(t *testing.T, store *Store, request DirectReviewRequest) {
	t.Helper()
	providerJSON, repositoryJSON, err := marshalDirectReviewLimits(request.ProviderLimits, request.RepositoryLimits)
	if err != nil {
		t.Fatal(err)
	}
	truncated := 0
	if request.InitialContextTruncated {
		truncated = 1
	}
	execLegacy(t, store, `INSERT INTO direct_review_requests(request_id,repository_id,trigger_kind,source_event_key,
		pull_request_number,task_id,attempt_id,observed_source_oid,observed_target_oid,effective_base_oid,effective_head_oid,diff_mode,
		configuration_version,connection_version,connection_fingerprint,protocol,endpoint,model,authentication_mode,
		provider_limits_json,repository_limits_json,instruction_version,consent_version,consent_digest,disclosure_version,
		disclosure_digest,registration_digest,phase,cancel_requested_at,created_at,preparing_at,running_at,terminal_at,
		initial_context_bytes,initial_context_truncated,result_json)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		request.RequestID, request.RepositoryID, request.TriggerKind, request.SourceEventKey, request.PullRequestNumber,
		request.TaskID, request.AttemptID, request.ObservedSourceOID, request.ObservedTargetOID, request.EffectiveBaseOID,
		request.EffectiveHeadOID, request.DiffMode, request.ConfigurationVersion, request.ConnectionVersion,
		request.ConnectionFingerprint, request.Protocol, request.Endpoint, request.Model, request.AuthenticationMode,
		providerJSON, repositoryJSON, request.InstructionVersion, request.ConsentVersion, request.ConsentDigest,
		request.DisclosureVersion, request.DisclosureDigest, request.RegistrationDigest, request.Phase,
		nullableUnix(request.CancelRequestedAt), request.CreatedAt.Unix(), nullableUnix(request.PreparingAt),
		nullableUnix(request.RunningAt), nullableUnix(request.TerminalAt), request.InitialContextBytes, truncated,
		legacyResultJSON(t, request.Result))
}

func insertLegacyTaskContext(t *testing.T, store *Store, contextRecord DirectReviewTaskContext) {
	t.Helper()
	execLegacy(t, store, `INSERT INTO direct_review_task_contexts(context_id,repository_id,task_id,attempt_id,credential_id,
		base_oid,head_oid,pull_request_number,observed_source_oid,observed_target_oid,registration_digest,created_at)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`, contextRecord.ContextID, contextRecord.RepositoryID, contextRecord.TaskID,
		contextRecord.AttemptID, contextRecord.CredentialID, contextRecord.BaseOID, contextRecord.HeadOID,
		contextRecord.PullRequestNumber, contextRecord.ObservedSourceOID, contextRecord.ObservedTargetOID,
		contextRecord.RegistrationDigest, contextRecord.CreatedAt.Unix())
}

// insertLegacyReviewEvent records the pending review choice an automatic
// request was bound to. The identity outlives the provider that keyed it.
func insertLegacyReviewEvent(t *testing.T, store *Store, now time.Time) {
	t.Helper()
	execLegacy(t, store, `INSERT INTO pull_request_reviews(repository_id,pull_request_number,sequence,source_oid,target_oid,
		status,reviewer_label,provenance,review_event_id,created_at) VALUES('project',1,2,?,?,?,'',?,?,?)`,
		legacyReviewSourceOID, legacyReviewTargetOID, ReviewPending, ReviewProvenanceRequest, legacyReviewEventID, now.Unix())
}

func readLegacySettings(store *Store, repositoryID string) (DirectReviewSettings, bool, error) {
	row := store.db.QueryRowContext(context.Background(), directReviewSettingsSelect+` WHERE repository_id=?`, repositoryID)
	settings, err := scanDirectReviewSettings(row)
	return settings, err == nil, err
}

func readLegacyCredential(store *Store, id string) (string, bool, error) {
	var value string
	err := store.db.QueryRowContext(context.Background(), `SELECT value FROM direct_review_credentials WHERE id=?`, id).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	return value, err == nil, err
}

func readLegacyProbe(store *Store, requestID string) (DirectReviewProbe, bool, error) {
	row := store.db.QueryRowContext(context.Background(), directReviewProbeSelect+` WHERE request_id=?`, requestID)
	probe, err := scanDirectReviewProbe(row)
	return probe, err == nil, err
}

func readLegacyRequest(store *Store, requestID string) (DirectReviewRequest, bool, error) {
	row := store.db.QueryRowContext(context.Background(), directReviewRequestSelect+` WHERE request_id=?`, requestID)
	request, err := scanDirectReviewRequest(row)
	return request, err == nil, err
}

// reconcileFacts is the exact stored text the second pass must reproduce.
func reconcileFacts(t *testing.T, store *Store) string {
	t.Helper()
	var facts string
	row := store.db.QueryRowContext(context.Background(), `SELECT group_concat(fact,'|') FROM (
		SELECT request_id||'/'||phase||'/'||COALESCE(terminal_at,0)||'/'||result_json AS fact FROM direct_review_requests
		UNION ALL SELECT request_id||'/'||phase||'/'||COALESCE(terminal_at,0)||'/'||result_json FROM direct_review_probes
		ORDER BY fact)`)
	if err := row.Scan(&facts); err != nil {
		t.Fatal(err)
	}
	return facts
}

func execLegacy(t *testing.T, store *Store, query string, arguments ...any) {
	t.Helper()
	if _, err := store.db.ExecContext(context.Background(), query, arguments...); err != nil {
		t.Fatal(err)
	}
}

func legacyResultJSON(t *testing.T, result *DirectReviewResult) string {
	t.Helper()
	if result == nil {
		return ""
	}
	content, err := marshalDirectReviewResult(*result)
	if err != nil {
		t.Fatal(err)
	}
	return content
}

// resealRequest recomputes the digest a stored row carries. The removed
// service sealed rows on the way in; a test that changes registration facts
// must settle them the same way.
func resealRequest(t *testing.T, request DirectReviewRequest) DirectReviewRequest {
	t.Helper()
	digest, err := DirectReviewRequestDigest(request)
	if err != nil {
		t.Fatal(err)
	}
	request.RegistrationDigest = digest
	return request
}

func timePointer(value time.Time) *time.Time { return &value }

func directReviewTestLimits() (DirectReviewProviderLimits, DirectReviewRepositoryLimits) {
	return DirectReviewProviderLimits{
		MaxRequestBytes: 1 << 20, MaxContextBytes: 128 << 10, MaxResponseBytes: 128 << 10,
		MaxToolOutputBytes: 64 << 10, MaxToolRounds: 4, MaxToolCalls: 8, MaxOutputTokens: 2048, MaxDurationMS: 5_000,
	}, DirectReviewRepositoryLimits{
		MaxInitialContextBytes: 64 << 10, MaxDirectoryBytes: 64 << 10, MaxFileChunkBytes: 8 << 10,
		MaxFilePrefixBytes: 64 << 10, MaxToolOutputBytes: 32 << 10, MaxOperationDurationMS: 5_000,
	}
}

func directReviewTestProbe(settings DirectReviewSettings, requestID string, created time.Time) DirectReviewProbe {
	probe := DirectReviewProbe{
		RequestID: requestID, RepositoryID: settings.RepositoryID, CapabilityFingerprint: strings.Repeat("8", 64),
		ConfigurationVersion: settings.ConfigurationVersion, ConnectionVersion: settings.ConnectionVersion,
		ConnectionFingerprint: DirectReviewConnectionFingerprint(settings), Protocol: settings.Protocol, Endpoint: settings.Endpoint,
		Model: settings.Model, AuthenticationMode: settings.AuthenticationMode, ProviderLimits: settings.ProviderLimits,
		DisclosureVersion: "probe-v1", DisclosureDigest: strings.Repeat("a", 64), Phase: DirectReviewPhasePreparing, CreatedAt: created,
	}
	probe.RegistrationDigest, _ = DirectReviewProbeDigest(probe)
	return probe
}

func directReviewTestRequest(settings DirectReviewSettings, requestID string, created time.Time) DirectReviewRequest {
	request := DirectReviewRequest{
		RequestID: requestID, RepositoryID: settings.RepositoryID, TriggerKind: DirectReviewTriggerManual, PullRequestNumber: 1,
		ObservedSourceOID: legacyReviewSourceOID, ObservedTargetOID: legacyReviewTargetOID,
		EffectiveBaseOID: legacyReviewTargetOID, EffectiveHeadOID: legacyReviewSourceOID, DiffMode: DirectReviewDiffTwoCommit,
		ConfigurationVersion: settings.ConfigurationVersion, ConnectionVersion: settings.ConnectionVersion,
		ConnectionFingerprint: DirectReviewConnectionFingerprint(settings), Protocol: settings.Protocol, Endpoint: settings.Endpoint, Model: settings.Model,
		AuthenticationMode: settings.AuthenticationMode, ProviderLimits: settings.ProviderLimits, RepositoryLimits: settings.RepositoryLimits,
		InstructionVersion: settings.InstructionVersion, ConsentVersion: "manual-v1", ConsentDigest: strings.Repeat("c", 64),
		DisclosureVersion: "disclosure-v1", DisclosureDigest: strings.Repeat("d", 64), Phase: DirectReviewPhasePreparing,
		CreatedAt: created, PreparingAt: &created,
	}
	request.RegistrationDigest, _ = DirectReviewRequestDigest(request)
	return request
}
