package server

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"owngit/internal/checkapi"
	"owngit/internal/pullrequest"
	"owngit/internal/state"
)

func TestHelperCredentialAuthAndAttemptUpload(t *testing.T) {
	fixture := newAPIFixture(t, false)
	ctx := context.Background()
	base, token := helperAPI(t, fixture, "laptop", time.Now())

	unauthenticated := checkRequest(t, http.MethodPost, base+"/tasks", map[string]any{"title": "Build"}, "")
	if unauthenticated.StatusCode != http.StatusUnauthorized || apiErrorCode(t, unauthenticated) != "helper_authentication_required" {
		t.Fatalf("unauthenticated task status=%d", unauthenticated.StatusCode)
	}
	wrong := checkRequest(t, http.MethodPost, base+"/tasks", map[string]any{"title": "Build"}, "wrong-token")
	if wrong.StatusCode != http.StatusUnauthorized || apiErrorCode(t, wrong) != "invalid_helper_credential" {
		t.Fatalf("wrong token status=%d", wrong.StatusCode)
	}

	created := checkRequest(t, http.MethodPost, base+"/tasks", map[string]any{"title": "Build"}, token)
	if created.StatusCode != http.StatusOK {
		t.Fatalf("create task status=%d error=%q", created.StatusCode, apiErrorCode(t, created))
	}
	var taskResponse checkapi.TaskResponse
	decodeCheckJSON(t, created, &taskResponse)
	if taskResponse.Task == nil || taskResponse.Task.Status != state.TaskActive || taskResponse.Task.CorrectionCyclesRemaining != state.CorrectionCycleLimit {
		t.Fatalf("created task=%+v", taskResponse.Task)
	}
	taskID := taskResponse.Task.ID

	upload := attemptUploadBody(fixture.sourceOID, "clean", "failed")
	recorded := recordAttempt(t, base, taskID, token, upload)
	if recorded.StatusCode != http.StatusOK {
		t.Fatalf("record attempt status=%d error=%q", recorded.StatusCode, apiErrorCode(t, recorded))
	}
	var attemptResponse checkapi.TaskResponse
	decodeCheckJSON(t, recorded, &attemptResponse)
	if attemptResponse.Attempt == nil || attemptResponse.Attempt.Status != state.AttemptFailed || attemptResponse.Attempt.ConfigurationVersion != 1 {
		t.Fatalf("recorded attempt=%+v", attemptResponse.Attempt)
	}
	if attemptResponse.Attempt.Sequence != 1 {
		t.Fatalf("attempt sequence=%d want 1", attemptResponse.Attempt.Sequence)
	}
	if attemptResponse.Task == nil || attemptResponse.Task.CorrectionCyclesUsed != 0 || attemptResponse.Task.CorrectionCyclesRemaining != state.CorrectionCycleLimit || !attemptResponse.Task.InitialCheckDone {
		t.Fatalf("task after the initial failure=%+v", attemptResponse.Task)
	}
	if attemptResponse.Attempt.Protection != state.ProtectionUnknown || attemptResponse.Attempt.ExecutionScope != state.ExecutionScopeInherited {
		t.Fatalf("attempt execution context=%+v", attemptResponse.Attempt)
	}
	if attemptResponse.Attempt.CredentialID == "" {
		t.Fatal("attempt lacks the authenticated credential provenance")
	}
	if attemptResponse.Attempt.LogID == "" {
		t.Fatal("raw log was not stored")
	}

	log := checkRequest(t, http.MethodGet, base+"/check-attempts/"+attemptResponse.Attempt.ID+"/log", nil, token)
	if log.StatusCode != http.StatusOK {
		t.Fatalf("log status=%d error=%q", log.StatusCode, apiErrorCode(t, log))
	}
	var logResponse checkapi.LogResponse
	decodeCheckJSON(t, log, &logResponse)
	if !logResponse.OK || logResponse.Content == "" || logResponse.ExpiresAt == nil {
		t.Fatalf("log response=%+v", logResponse)
	}
	// Delete the disposable row while keeping the request clock before its
	// recorded expiry. Missing and expired remain distinct API states.
	if attemptResponse.Attempt.LogExpiresAt == nil {
		t.Fatal("raw log has no expiry")
	}
	removed, err := fixture.store.PruneCheckLogs(ctx, attemptResponse.Attempt.LogExpiresAt.Add(time.Second))
	if err != nil || removed != 1 {
		t.Fatalf("prune raw log removed=%d err=%v", removed, err)
	}
	requestNow := attemptResponse.Attempt.LogExpiresAt.Add(-time.Second)
	content, logState, err := fixture.store.ReadCheckLog(attemptResponse.Attempt.LogID, attemptResponse.Attempt.LogExpiresAt, requestNow)
	if err != nil || logState != state.CheckLogMissing || content != nil {
		t.Fatalf("pruned raw log content=%q state=%q err=%v", content, logState, err)
	}
	previousNow := fixture.app.Now
	fixture.app.Now = func() time.Time { return requestNow }
	missing := checkRequest(t, http.MethodGet, base+"/check-attempts/"+attemptResponse.Attempt.ID+"/log", nil, token)
	fixture.app.Now = previousNow
	if missing.StatusCode != http.StatusNotFound || apiErrorCode(t, missing) != "log_missing" {
		t.Fatalf("missing log status=%d error=%q", missing.StatusCode, apiErrorCode(t, missing))
	}
	stillShown := checkRequest(t, http.MethodGet, base+"/tasks/"+taskID, nil, token)
	if stillShown.StatusCode != http.StatusOK {
		t.Fatalf("attempt after raw-log deletion status=%d", stillShown.StatusCode)
	}

	shown := checkRequest(t, http.MethodGet, base+"/tasks/"+taskID, nil, token)
	if shown.StatusCode != http.StatusOK {
		t.Fatalf("show task status=%d", shown.StatusCode)
	}
	var shownResponse checkapi.TaskResponse
	decodeCheckJSON(t, shown, &shownResponse)
	if shownResponse.Attempt == nil || shownResponse.Attempt.ID != attemptResponse.Attempt.ID {
		t.Fatalf("shown task attempt=%+v", shownResponse.Attempt)
	}

	configuration := checkRequest(t, http.MethodGet, base+"/check-configurations/latest", nil, token)
	if configuration.StatusCode != http.StatusOK {
		t.Fatalf("latest configuration status=%d", configuration.StatusCode)
	}
	var configurationResponse checkapi.ConfigurationResponse
	decodeCheckJSON(t, configuration, &configurationResponse)
	if configurationResponse.Configuration == nil || configurationResponse.Configuration.Version != 1 || len(configurationResponse.Configuration.Checks) != 1 {
		t.Fatalf("configuration=%+v", configurationResponse.Configuration)
	}

	// A helper credential cannot manage credentials: that needs the
	// administrator password.
	helperOnAdmin := checkRequest(t, http.MethodGet, base+"/helper-credentials", nil, token)
	if helperOnAdmin.StatusCode != http.StatusUnauthorized || apiErrorCode(t, helperOnAdmin) != "admin_authentication_required" {
		t.Fatalf("helper credential managed credentials: status=%d", helperOnAdmin.StatusCode)
	}
	generalOnAdmin := apiRequest(t, http.MethodGet, base+"/helper-credentials", nil, "shared-password", "")
	if generalOnAdmin.StatusCode != http.StatusUnauthorized || apiErrorCode(t, generalOnAdmin) != "admin_authentication_required" {
		t.Fatalf("general password managed credentials: status=%d", generalOnAdmin.StatusCode)
	}
	adminCreated := adminAPIRequest(t, http.MethodPost, base+"/helper-credentials", map[string]any{"label": "second"}, "admin-password")
	if adminCreated.StatusCode != http.StatusOK {
		t.Fatalf("admin create credential status=%d error=%q", adminCreated.StatusCode, apiErrorCode(t, adminCreated))
	}

	// A browser admin session can read with its CSRF header, but issuing
	// authority still needs the current administrator password.
	settings, err := fixture.store.Settings(ctx)
	noErr(t, err)
	noErr(t, fixture.store.CreateSession(ctx, "admin-session", "admin", "admin-csrf", settings.AdminSessionVersion, time.Now().Add(time.Hour)))
	sessionRead := adminSessionRequest(t, http.MethodGet, base+"/helper-credentials", nil, "admin-csrf", "")
	if sessionRead.StatusCode != http.StatusOK {
		t.Fatalf("admin session read status=%d error=%q", sessionRead.StatusCode, apiErrorCode(t, sessionRead))
	}
	sessionRead.Body.Close()
	wrongCSRF := adminSessionRequest(t, http.MethodGet, base+"/helper-credentials", nil, "wrong", "")
	if wrongCSRF.StatusCode != http.StatusForbidden || apiErrorCode(t, wrongCSRF) != "csrf_required" {
		t.Fatalf("wrong csrf status=%d error=%q", wrongCSRF.StatusCode, apiErrorCode(t, wrongCSRF))
	}
	wrongCSRF.Body.Close()
	var credentialResponse checkapi.CredentialResponse
	decodeCheckJSON(t, adminCreated, &credentialResponse)
	if credentialResponse.Credential == nil || credentialResponse.Token == "" {
		t.Fatalf("created credential=%+v", credentialResponse)
	}
	revoked := adminAPIRequest(t, http.MethodDelete, base+"/helper-credentials/"+credentialResponse.Credential.ID, nil, "admin-password")
	if revoked.StatusCode != http.StatusOK {
		t.Fatalf("revoke status=%d", revoked.StatusCode)
	}
	afterRevoke := checkRequest(t, http.MethodGet, base+"/tasks", nil, credentialResponse.Token)
	if afterRevoke.StatusCode != http.StatusUnauthorized || apiErrorCode(t, afterRevoke) != "invalid_helper_credential" {
		t.Fatalf("revoked credential status=%d", afterRevoke.StatusCode)
	}

	// A credential is scoped to its repository.
	if _, err := fixture.app.Repositories.Create(ctx, "other", "Other"); err != nil {
		t.Fatal(err)
	}
	scoped := checkRequest(t, http.MethodGet, strings.TrimSuffix(base, "/project")+"/other/tasks", nil, token)
	if scoped.StatusCode != http.StatusForbidden || apiErrorCode(t, scoped) != "helper_credential_scope" {
		t.Fatalf("cross-repository credential status=%d", scoped.StatusCode)
	}
}

func TestHelperCredentialCreationIdentityIsRecoverable(t *testing.T) {
	fixture := newAPIFixture(t, false)
	server := serve(t, fixture.app.Handler())
	base := server.URL + "/api/v1/repositories/project"
	creationID := "0123456789abcdef0123456789abcdef"

	first := adminAPIRequest(t, http.MethodPost, base+"/helper-credentials", map[string]any{"label": "laptop", "creation_id": creationID}, "admin-password")
	if first.StatusCode != http.StatusOK {
		t.Fatalf("create status=%d error=%q", first.StatusCode, apiErrorCode(t, first))
	}
	var created checkapi.CredentialResponse
	decodeCheckJSON(t, first, &created)
	if created.Credential == nil || created.Token == "" || created.Credential.CreationID != creationID {
		t.Fatalf("created credential=%+v", created)
	}
	// A retransmit with the same creation identity returns the same credential
	// without a token, so no duplicate authority is created.
	again := adminAPIRequest(t, http.MethodPost, base+"/helper-credentials", map[string]any{"label": "laptop", "creation_id": creationID}, "admin-password")
	if again.StatusCode != http.StatusOK {
		t.Fatalf("retransmit status=%d error=%q", again.StatusCode, apiErrorCode(t, again))
	}
	var repeated checkapi.CredentialResponse
	decodeCheckJSON(t, again, &repeated)
	if repeated.Credential == nil || repeated.Credential.ID != created.Credential.ID || repeated.Token != "" {
		t.Fatalf("retransmitted credential=%+v", repeated)
	}
	// A scoped compensating revoke is idempotent and cannot touch another
	// credential.
	revoke := adminAPIRequest(t, http.MethodDelete, base+"/helper-credentials/by-creation/"+creationID, nil, "admin-password")
	if revoke.StatusCode != http.StatusOK {
		t.Fatalf("scoped revoke status=%d error=%q", revoke.StatusCode, apiErrorCode(t, revoke))
	}
	revokeAgain := adminAPIRequest(t, http.MethodDelete, base+"/helper-credentials/by-creation/"+creationID, nil, "admin-password")
	if revokeAgain.StatusCode != http.StatusOK {
		t.Fatalf("repeated scoped revoke status=%d error=%q", revokeAgain.StatusCode, apiErrorCode(t, revokeAgain))
	}
	afterRevoke := checkRequest(t, http.MethodGet, base+"/tasks", nil, created.Token)
	if afterRevoke.StatusCode != http.StatusUnauthorized {
		t.Fatalf("revoked credential status=%d", afterRevoke.StatusCode)
	}
}

func TestHelperCredentialChangesRequireTheCurrentAdminPassword(t *testing.T) {
	fixture := newAPIFixture(t, false)
	server := serve(t, fixture.app.Handler())
	ctx := context.Background()
	base := server.URL + "/api/v1/repositories/project"

	settings, err := fixture.store.Settings(ctx)
	noErr(t, err)
	noErr(t, fixture.store.CreateSession(ctx, "admin-session", "admin", "admin-csrf", settings.AdminSessionVersion, time.Now().Add(time.Hour)))

	// A remembered browser session can read.
	if status, code := checkStatus(t, adminSessionRequest(t, http.MethodGet, base+"/helper-credentials", nil, "admin-csrf", "")); status != http.StatusOK {
		t.Fatalf("session read status=%d error=%q", status, code)
	}

	// It cannot issue authority without the current password.
	if status, code := checkStatus(t, adminSessionRequest(t, http.MethodPost, base+"/helper-credentials", map[string]any{"label": "browser"}, "admin-csrf", "")); status != http.StatusUnauthorized || code != "admin_password_required" {
		t.Fatalf("session create status=%d error=%q", status, code)
	}

	// A wrong password is rejected even with a valid session and CSRF token.
	if status, code := checkStatus(t, adminSessionRequest(t, http.MethodPost, base+"/helper-credentials", map[string]any{"label": "browser"}, "admin-csrf", "wrong-password")); status != http.StatusUnauthorized || code != "invalid_admin_credentials" {
		t.Fatalf("wrong password status=%d error=%q", status, code)
	}

	// The current password with the session and CSRF token succeeds.
	created := adminSessionRequest(t, http.MethodPost, base+"/helper-credentials", map[string]any{"label": "browser"}, "admin-csrf", "admin-password")
	if created.StatusCode != http.StatusOK {
		status, code := checkStatus(t, created)
		t.Fatalf("session create with password status=%d error=%q", status, code)
	}
	var credentialResponse checkapi.CredentialResponse
	decodeCheckJSON(t, created, &credentialResponse)
	if credentialResponse.Credential == nil || credentialResponse.Token == "" {
		t.Fatalf("created credential=%+v", credentialResponse)
	}

	// Revoking is a security change too.
	if status, code := checkStatus(t, adminSessionRequest(t, http.MethodDelete, base+"/helper-credentials/"+credentialResponse.Credential.ID, nil, "admin-csrf", "")); status != http.StatusUnauthorized || code != "admin_password_required" {
		t.Fatalf("session revoke status=%d error=%q", status, code)
	}
	if status, code := checkStatus(t, adminSessionRequest(t, http.MethodDelete, base+"/helper-credentials/"+credentialResponse.Credential.ID, nil, "admin-csrf", "admin-password")); status != http.StatusOK {
		t.Fatalf("session revoke with password status=%d error=%q", status, code)
	}

	// The Basic CLI flow still works, and a wrong password fails.
	if status, code := checkStatus(t, adminAPIRequest(t, http.MethodPost, base+"/helper-credentials", map[string]any{"label": "cli"}, "admin-password")); status != http.StatusOK {
		t.Fatalf("basic create status=%d error=%q", status, code)
	}
	if status, code := checkStatus(t, adminAPIRequest(t, http.MethodPost, base+"/helper-credentials", map[string]any{"label": "cli"}, "wrong-password")); status != http.StatusUnauthorized || code != "invalid_admin_credentials" {
		t.Fatalf("wrong basic status=%d error=%q", status, code)
	}

	// A helper token and the general password are not administrator authority.
	if status, code := checkStatus(t, checkRequest(t, http.MethodPost, base+"/helper-credentials", map[string]any{"label": "helper"}, "synthetic-helper-token")); status != http.StatusUnauthorized || code != "admin_authentication_required" {
		t.Fatalf("helper token write status=%d error=%q", status, code)
	}
	if status, code := checkStatus(t, apiRequest(t, http.MethodPost, base+"/helper-credentials", map[string]any{"label": "general"}, "shared-password", "")); status != http.StatusUnauthorized || code != "admin_authentication_required" {
		t.Fatalf("general password write status=%d error=%q", status, code)
	}

	// A session without its CSRF token is still rejected on a read.
	if status, code := checkStatus(t, adminSessionRequest(t, http.MethodGet, base+"/helper-credentials", nil, "", "")); status != http.StatusForbidden || code != "csrf_required" {
		t.Fatalf("session without csrf status=%d error=%q", status, code)
	}
}

func TestAttemptRegistrationReplayIgnoresLaterServerClock(t *testing.T) {
	fixture := newAPIFixture(t, false)
	serverNow := time.Unix(1_800_000_000, 0).UTC()
	fixture.app.Now = func() time.Time { return serverNow }
	ctx := context.Background()
	base, token := helperAPI(t, fixture, "clock fixture", serverNow)
	taskID := createCheckTask(t, base, token, "Clock replay")
	payload := attemptUploadBodyWithID(fixture.sourceOID, "clean", "passed", "ffffffffffffffffffffffffffffffff")
	first := registerAttempt(t, base, taskID, token, payload)
	if first.StatusCode != http.StatusOK {
		t.Fatalf("first registration status=%d error=%q", first.StatusCode, apiErrorCode(t, first))
	}
	attempts, err := fixture.store.CheckAttempts(ctx, "project")
	if err != nil || len(attempts) != 1 {
		t.Fatalf("first attempts=%+v err=%v", attempts, err)
	}
	original := attempts[0]

	serverNow = serverNow.Add(2 * time.Second)
	replay := registerAttempt(t, base, taskID, token, payload)
	if replay.StatusCode != http.StatusOK {
		t.Fatalf("identical registration after server clock advance: status=%d error=%q", replay.StatusCode, apiErrorCode(t, replay))
	}
	attempts, err = fixture.store.CheckAttempts(ctx, "project")
	if err != nil || len(attempts) != 1 || !attempts[0].CreatedAt.Equal(original.CreatedAt) || attempts[0].Sequence != original.Sequence || attempts[0].RegistrationDigest != original.RegistrationDigest {
		t.Fatalf("replay changed stored registration: attempts=%+v original=%+v err=%v", attempts, original, err)
	}
}

func TestAttemptRegistrationIsIdempotentAndOrdered(t *testing.T) {
	fixture := newAPIFixture(t, false)
	ctx := context.Background()
	base, token := helperAPI(t, fixture, "laptop", time.Now())
	taskID := createCheckTask(t, base, token, "Idempotent")

	// The older attempt is registered first, so it holds the lower sequence.
	older := attemptUploadBodyWithID(fixture.sourceOID, "clean", "failed", "ffffffffffffffffffffffffffffffff")
	older["started_at"] = time.Now().UTC().Add(-time.Hour).Format(time.RFC3339Nano)
	older["finished_at"] = time.Now().UTC().Add(-time.Hour).Format(time.RFC3339Nano)
	olderRegistered := registerAttempt(t, base, taskID, token, older)
	if olderRegistered.StatusCode != http.StatusOK {
		t.Fatalf("older registration status=%d error=%q", olderRegistered.StatusCode, apiErrorCode(t, olderRegistered))
	}
	var olderResponse checkapi.TaskResponse
	decodeCheckJSON(t, olderRegistered, &olderResponse)
	if olderResponse.Attempt.Sequence != 1 || olderResponse.Attempt.Status != state.AttemptPending {
		t.Fatalf("older registration=%+v", olderResponse.Attempt)
	}

	// A retransmit of the same registration must not store a second attempt or
	// issue a second sequence.
	retransmit := registerAttempt(t, base, taskID, token, older)
	if retransmit.StatusCode != http.StatusOK {
		t.Fatalf("retransmit status=%d error=%q", retransmit.StatusCode, apiErrorCode(t, retransmit))
	}
	var retransmitResponse checkapi.TaskResponse
	decodeCheckJSON(t, retransmit, &retransmitResponse)
	if retransmitResponse.Attempt.ID != olderResponse.Attempt.ID || retransmitResponse.Attempt.Sequence != olderResponse.Attempt.Sequence {
		t.Fatalf("retransmit stored a different attempt: %+v", retransmitResponse.Attempt)
	}
	attempts, err := fixture.store.CheckAttempts(ctx, "project")
	if err != nil || len(attempts) != 1 {
		t.Fatalf("stored attempts=%d err=%v", len(attempts), err)
	}

	// The same identity with different content is a conflict.
	conflict := attemptUploadBodyWithID(fixture.sourceOID, "dirty", "failed", olderResponse.Attempt.ID)
	conflictResponse := registerAttempt(t, base, taskID, token, conflict)
	if conflictResponse.StatusCode != http.StatusConflict || apiErrorCode(t, conflictResponse) != "attempt_conflict" {
		t.Fatalf("conflict status=%d error=%q", conflictResponse.StatusCode, apiErrorCode(t, conflictResponse))
	}

	// A newer passing attempt is registered and completed first.
	newer := attemptUploadBodyWithID(fixture.sourceOID, "clean", "passed", "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee")
	newerRegistered := registerAttempt(t, base, taskID, token, newer)
	if newerRegistered.StatusCode != http.StatusOK {
		t.Fatalf("newer registration status=%d error=%q", newerRegistered.StatusCode, apiErrorCode(t, newerRegistered))
	}
	var newerResponse checkapi.TaskResponse
	decodeCheckJSON(t, newerRegistered, &newerResponse)
	if newerResponse.Attempt.Sequence != 2 {
		t.Fatalf("newer sequence=%d want 2", newerResponse.Attempt.Sequence)
	}
	newerCompleted := completeAttempt(t, base, taskID, newerResponse.Attempt.ID, token, newer)
	if newerCompleted.StatusCode != http.StatusOK {
		t.Fatalf("newer completion status=%d error=%q", newerCompleted.StatusCode, apiErrorCode(t, newerCompleted))
	}
	var newerTask checkapi.TaskResponse
	decodeCheckJSON(t, newerCompleted, &newerTask)
	if newerTask.Task.Status != state.TaskResolved {
		t.Fatalf("task after the newer pass=%+v", newerTask.Task)
	}

	// The older attempt finishes much later with a client clock far ahead. The
	// server-issued sequence, not the finish time, decides the order.
	older["finished_at"] = time.Now().UTC().Add(10 * time.Hour).Format(time.RFC3339Nano)
	olderCompleted := completeAttempt(t, base, taskID, olderResponse.Attempt.ID, token, older)
	if olderCompleted.StatusCode != http.StatusOK {
		t.Fatalf("older completion status=%d error=%q", olderCompleted.StatusCode, apiErrorCode(t, olderCompleted))
	}
	var olderTask checkapi.TaskResponse
	decodeCheckJSON(t, olderCompleted, &olderTask)
	if olderTask.Task.Status != state.TaskResolved || olderTask.Task.LastAppliedAttemptID != newerResponse.Attempt.ID {
		t.Fatalf("late older failure overrode the task: %+v", olderTask.Task)
	}
}

func TestCorrectionCycleReservationIsExplicitAndCountedOnce(t *testing.T) {
	fixture := newAPIFixture(t, false)
	base, token := helperAPI(t, fixture, "laptop", time.Now())
	taskID := createCheckTask(t, base, token, "Correction")

	// The initial check consumes no round.
	initial := recordAttempt(t, base, taskID, token, attemptUploadBody(fixture.sourceOID, "clean", "failed"))
	if initial.StatusCode != http.StatusOK {
		t.Fatalf("initial check status=%d error=%q", initial.StatusCode, apiErrorCode(t, initial))
	}
	var initialTask checkapi.TaskResponse
	decodeCheckJSON(t, initial, &initialTask)
	if initialTask.Task.CorrectionCyclesUsed != 0 {
		t.Fatalf("initial check consumed a round: %+v", initialTask.Task)
	}

	// A round is reserved before the correction and counted once.
	cycleID := "0123456789abcdef0123456789abcdef"
	reserved := checkRequest(t, http.MethodPost, base+"/tasks/"+taskID+"/cycles", map[string]any{"cycle_id": cycleID}, token)
	if reserved.StatusCode != http.StatusOK {
		t.Fatalf("reserve status=%d error=%q", reserved.StatusCode, apiErrorCode(t, reserved))
	}
	var reservedResponse checkapi.CycleResponse
	decodeCheckJSON(t, reserved, &reservedResponse)
	if reservedResponse.Cycle == nil || reservedResponse.Cycle.Sequence != 1 || reservedResponse.Task.CorrectionCyclesUsed != 1 {
		t.Fatalf("reserved cycle=%+v task=%+v", reservedResponse.Cycle, reservedResponse.Task)
	}
	// A retransmitted reservation returns the same round without counting twice.
	again := checkRequest(t, http.MethodPost, base+"/tasks/"+taskID+"/cycles", map[string]any{"cycle_id": cycleID}, token)
	if again.StatusCode != http.StatusOK {
		t.Fatalf("retransmitted reservation status=%d error=%q", again.StatusCode, apiErrorCode(t, again))
	}
	var againResponse checkapi.CycleResponse
	decodeCheckJSON(t, again, &againResponse)
	if againResponse.Cycle.ID != cycleID || againResponse.Task.CorrectionCyclesUsed != 1 {
		t.Fatalf("retransmitted reservation=%+v", againResponse)
	}

	// The correction succeeds, and the round still counts.
	correction := attemptUploadBodyWithID(fixture.sourceOID, "clean", "passed", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	correction["cycle_id"] = cycleID
	completed := recordAttempt(t, base, taskID, token, correction)
	if completed.StatusCode != http.StatusOK {
		t.Fatalf("correction status=%d error=%q", completed.StatusCode, apiErrorCode(t, completed))
	}
	var completedResponse checkapi.TaskResponse
	decodeCheckJSON(t, completed, &completedResponse)
	if completedResponse.Task.Status != state.TaskResolved || completedResponse.Task.CorrectionCyclesUsed != 1 {
		t.Fatalf("successful correction task=%+v", completedResponse.Task)
	}
	if completedResponse.Attempt.CycleID != cycleID {
		t.Fatalf("attempt lost its round: %+v", completedResponse.Attempt)
	}

	// A manual rerun consumes nothing.
	manual := recordAttempt(t, base, taskID, token, attemptUploadBodyWithID(fixture.sourceOID, "clean", "failed", "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"))
	var manualResponse checkapi.TaskResponse
	decodeCheckJSON(t, manual, &manualResponse)
	if manualResponse.Task.CorrectionCyclesUsed != 1 {
		t.Fatalf("manual rerun consumed a round: %+v", manualResponse.Task)
	}

	// An attempt bound to an unreserved round is reported instead of silently
	// consuming the budget.
	unreserved := attemptUploadBodyWithID(fixture.sourceOID, "clean", "passed", "dddddddddddddddddddddddddddddddd")
	unreserved["cycle_id"] = "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"
	unreservedResponse := registerAttempt(t, base, taskID, token, unreserved)
	if unreservedResponse.StatusCode != http.StatusNotFound || apiErrorCode(t, unreservedResponse) != "cycle_not_found" {
		t.Fatalf("unreserved cycle status=%d error=%q", unreservedResponse.StatusCode, apiErrorCode(t, unreservedResponse))
	}

	// The reserved rounds are listed for the task.
	listed := checkRequest(t, http.MethodGet, base+"/tasks/"+taskID+"/cycles", nil, token)
	if listed.StatusCode != http.StatusOK {
		t.Fatalf("list cycles status=%d", listed.StatusCode)
	}
	var listResponse checkapi.CycleListResponse
	decodeCheckJSON(t, listed, &listResponse)
	if len(listResponse.Cycles) != 1 || listResponse.Cycles[0].ID != cycleID {
		t.Fatalf("cycles=%+v", listResponse.Cycles)
	}
}

func TestAttemptCompletionValidatesTheDeclaredChecks(t *testing.T) {
	fixture := newAPIFixture(t, false)
	base, token := helperAPI(t, fixture, "laptop", time.Now())
	taskID := createCheckTask(t, base, token, "Validation")

	mismatch := attemptUploadBody(fixture.sourceOID, "clean", "passed")
	mismatch["results"] = []map[string]any{{
		"name": "other", "command": "go test ./...", "status": "passed", "exit_code": 0,
		"duration_ms": 1000, "output_excerpt": "output",
	}}
	response := recordAttempt(t, base, taskID, token, mismatch)
	if response.StatusCode != http.StatusUnprocessableEntity || apiErrorCode(t, response) != "invalid_attempt" {
		t.Fatalf("mismatch status=%d error=%q", response.StatusCode, apiErrorCode(t, response))
	}

	missingID := attemptUploadBody(fixture.sourceOID, "clean", "passed")
	delete(missingID, "attempt_id")
	response = registerAttempt(t, base, taskID, token, missingID)
	if response.StatusCode != http.StatusUnprocessableEntity || apiErrorCode(t, response) != "invalid_attempt_id" {
		t.Fatalf("missing identity status=%d error=%q", response.StatusCode, apiErrorCode(t, response))
	}

	// A completion for an unregistered attempt is reported as such.
	unregistered := attemptUploadBody(fixture.sourceOID, "clean", "passed")
	response = completeAttempt(t, base, taskID, "cccccccccccccccccccccccccccccccc", token, unregistered)
	if response.StatusCode != http.StatusNotFound || apiErrorCode(t, response) != "attempt_not_found" {
		t.Fatalf("unregistered completion status=%d error=%q", response.StatusCode, apiErrorCode(t, response))
	}
}

func TestExpiredLogIsReportedWhileTheAttemptRemains(t *testing.T) {
	fixture := newAPIFixture(t, false)
	base, token := helperAPI(t, fixture, "laptop", time.Now())
	taskID := createCheckTask(t, base, token, "Expiry")
	recorded := recordAttempt(t, base, taskID, token, attemptUploadBody(fixture.sourceOID, "clean", "passed"))
	var attemptResponse checkapi.TaskResponse
	decodeCheckJSON(t, recorded, &attemptResponse)

	// Move the server clock past the retention window.
	fixture.app.Now = func() time.Time { return time.Now().Add(31 * 24 * time.Hour) }
	expired := checkRequest(t, http.MethodGet, base+"/check-attempts/"+attemptResponse.Attempt.ID+"/log", nil, token)
	if expired.StatusCode != http.StatusGone || apiErrorCode(t, expired) != "log_expired" {
		t.Fatalf("expired log status=%d error=%q", expired.StatusCode, apiErrorCode(t, expired))
	}
	stillShown := checkRequest(t, http.MethodGet, base+"/tasks/"+taskID, nil, token)
	if stillShown.StatusCode != http.StatusOK {
		t.Fatalf("attempt after log expiry status=%d", stillShown.StatusCode)
	}
}

// The configuration that applies to a pull request is the one committed in its
// source revision. A configuration recorded for another branch never makes the
// head's evidence stale; evidence that ran other checks than the committed
// ones is stale.
func TestCheckEvidenceIsStaleOnlyAgainstTheSourceRevisionConfiguration(t *testing.T) {
	fixture := newAPIFixture(t, false)
	base, token := helperAPI(t, fixture, "laptop", time.Now())
	prEndpoint := base + "/pull-requests"
	created := apiRequest(t, http.MethodPost, prEndpoint, map[string]any{
		"title": "Configuration change", "source_branch": "feature", "target_branch": "main",
	}, "", "")
	view := decodeAPISuccess(t, created)
	number := itoa(view.PullRequest.Number)
	showChecks := func() pullrequest.Checks {
		t.Helper()
		shown := apiRequest(t, http.MethodGet, prEndpoint+"/"+number, nil, "", "")
		return decodeAPISuccess(t, shown).PullRequest.Checks
	}

	task := checkRequest(t, http.MethodPost, base+"/tasks", map[string]any{"title": "Configuration change"}, token)
	var taskResponse checkapi.TaskResponse
	decodeCheckJSON(t, task, &taskResponse)
	record := func(revision, attemptID, name, command string) {
		t.Helper()
		body := attemptUploadBodyWithID(revision, "clean", "passed", attemptID)
		body["checks"] = []map[string]string{{"name": name, "command": command}}
		body["results"] = []map[string]any{{
			"name": name, "command": command, "status": "passed", "exit_code": 0,
			"duration_ms": 1000, "output_excerpt": "ok",
		}}
		if response := recordAttempt(t, base, taskResponse.Task.ID, token, body); response.StatusCode != http.StatusOK {
			t.Fatalf("record attempt status=%d error=%q", response.StatusCode, apiErrorCode(t, response))
		}
	}

	// The head has no committed configuration, so the explicitly chosen checks
	// stand, even after a newer configuration is recorded for another branch.
	record(fixture.sourceOID, strings.Repeat("1", 32), "unit", "go test ./...")
	record(strings.Repeat("c", 40), strings.Repeat("2", 32), "lint", "go vet ./...")
	if checks := showChecks(); checks.Status != "passed" || checks.Stale || !checks.Passed {
		t.Fatalf("another branch's configuration made the evidence stale: %+v", checks)
	}

	// Commit a configuration on the pull request head.
	noErr(t, os.MkdirAll(filepath.Join(fixture.work, ".owngit"), 0o700))
	noErr(t, os.WriteFile(filepath.Join(fixture.work, ".owngit", "checks.json"),
		[]byte(`{"version":1,"events":{"push":{}},"checks":[{"name":"lint","command":"go vet ./..."}]}`), 0o600))
	apiRunGit(t, fixture.work, "add", ".")
	apiRunGit(t, fixture.work, "commit", "-m", "checks")
	apiRunGit(t, fixture.work, "push", "origin", "HEAD:refs/heads/feature")
	head := apiGitOutput(t, fixture.work, "rev-parse", "HEAD")

	// Evidence that ran other checks than the committed ones is stale.
	record(head, strings.Repeat("3", 32), "unit", "go test ./...")
	if checks := showChecks(); checks.Status != "stale" || !checks.Stale || checks.RevisionOID != head || !checks.Passed {
		t.Fatalf("evidence for other checks than the committed ones was not stale: %+v", checks)
	}

	// Evidence that ran the committed checks is current, and a configuration
	// recorded later for another branch does not change that.
	record(head, strings.Repeat("4", 32), "lint", "go vet ./...")
	record(strings.Repeat("d", 40), strings.Repeat("5", 32), "other", "true")
	if checks := showChecks(); checks.Status != "passed" || checks.Stale || checks.RevisionOID != head {
		t.Fatalf("evidence for the committed checks was not current: %+v", checks)
	}
}

func TestAdvisoryReadFailureDoesNotBlockAMerge(t *testing.T) {
	fixture := newAPIFixture(t, false)
	server := serve(t, fixture.app.Handler())
	ctx := context.Background()
	base := server.URL + "/api/v1/repositories/project"
	prEndpoint := base + "/pull-requests"
	created := apiRequest(t, http.MethodPost, prEndpoint, map[string]any{
		"title": "Advisory failure", "source_branch": "feature", "target_branch": "main",
	}, "", "")
	view := decodeAPISuccess(t, created)
	number := itoa(view.PullRequest.Number)

	// Break only the advisory check read. The durable pull request state and
	// the Git safety checks still work.
	noErr(t, fixture.store.Exec(ctx, "DROP TABLE check_configurations"))
	shown := apiRequest(t, http.MethodGet, prEndpoint+"/"+number, nil, "", "")
	if shown.StatusCode != http.StatusOK {
		t.Fatalf("show with a broken advisory read status=%d", shown.StatusCode)
	}
	checks := decodeAPISuccess(t, shown).PullRequest.Checks
	if checks.Status != "" || checks.ReadFailure == nil || checks.ReadFailure.Code != pullrequest.ReadFailureCheckConfiguration || checks.Configured || !checks.Advisory {
		t.Fatalf("advisory read failure=%+v", checks)
	}
	merged := apiRequest(t, http.MethodPost, prEndpoint+"/"+number+"/merge", pullrequest.RevisionInput{
		SourceOID: fixture.sourceOID, TargetOID: fixture.targetOID,
	}, "", "")
	if merged.StatusCode != http.StatusOK {
		t.Fatalf("merge with a broken advisory read status=%d error=%q", merged.StatusCode, apiErrorCode(t, merged))
	}
}

func TestCreatePullRequestValidatesExpectedHeadsInsideTheLock(t *testing.T) {
	fixture := newAPIFixture(t, false)
	server := serve(t, fixture.app.Handler())
	base := server.URL + "/api/v1/repositories/project/pull-requests"

	// A matching expected head creates the pull request.
	ok := apiRequest(t, http.MethodPost, base, map[string]any{
		"title": "Expected heads", "source_branch": "feature", "target_branch": "main",
		"source_oid": fixture.sourceOID, "target_oid": fixture.targetOID,
	}, "", "")
	if ok.StatusCode != http.StatusOK {
		t.Fatalf("matching expected heads status=%d error=%q", ok.StatusCode, apiErrorCode(t, ok))
	}
	// A stale expected head is reported with the current object IDs instead of
	// silently creating a pull request for a different revision.
	stale := apiRequest(t, http.MethodPost, base, map[string]any{
		"title": "Stale heads", "source_branch": "feature", "target_branch": "main",
		"source_oid": strings.Repeat("a", 40), "target_oid": fixture.targetOID,
	}, "", "")
	if stale.StatusCode != http.StatusConflict || apiErrorCode(t, stale) != "stale_revision" {
		t.Fatalf("stale expected head status=%d error=%q", stale.StatusCode, apiErrorCode(t, stale))
	}
}

func TestPullRequestViewShowsRevisionBoundChecksAndAdvisoryMerge(t *testing.T) {
	fixture := newAPIFixture(t, false)
	base, token := helperAPI(t, fixture, "laptop", time.Now())
	prEndpoint := base + "/pull-requests"

	created := apiRequest(t, http.MethodPost, prEndpoint, map[string]any{
		"title": "Checked feature", "source_branch": "feature", "target_branch": "main", "review": "request",
	}, "", "")
	if created.StatusCode != http.StatusOK {
		t.Fatalf("create status=%d", created.StatusCode)
	}
	view := decodeAPISuccess(t, created)
	number := itoa(view.PullRequest.Number)

	task := checkRequest(t, http.MethodPost, base+"/tasks", map[string]any{"title": "Checked feature"}, token)
	var taskResponse checkapi.TaskResponse
	decodeCheckJSON(t, task, &taskResponse)
	recorded := recordAttempt(t, base, taskResponse.Task.ID, token, attemptUploadBody(fixture.sourceOID, "clean", "failed"))
	if recorded.StatusCode != http.StatusOK {
		t.Fatalf("record attempt status=%d", recorded.StatusCode)
	}

	shown := apiRequest(t, http.MethodGet, prEndpoint+"/"+number, nil, "", "")
	if shown.StatusCode != http.StatusOK {
		t.Fatalf("show status=%d", shown.StatusCode)
	}
	checked := decodeAPISuccess(t, shown)
	checks := checked.PullRequest.Checks
	if checks.Status != state.AttemptFailed || checks.RevisionOID != fixture.sourceOID || !checks.TestedCommit || checks.Stale || checks.Blocking || !checks.Advisory {
		t.Fatalf("revision-bound checks=%+v", checks)
	}
	if !checked.PullRequest.MergeEligibility.Eligible || len(checked.PullRequest.MergeEligibility.Blockers) != 0 {
		t.Fatalf("failed check or pending review blocked merge: %+v", checked.PullRequest.MergeEligibility)
	}

	merged := apiRequest(t, http.MethodPost, prEndpoint+"/"+number+"/merge", pullrequest.RevisionInput{
		SourceOID: fixture.sourceOID, TargetOID: fixture.targetOID,
	}, "", "")
	if merged.StatusCode != http.StatusOK {
		t.Fatalf("advisory merge status=%d error=%q", merged.StatusCode, apiErrorCode(t, merged))
	}

	// A moved source head must not inherit the old success or failure. The new
	// pull request never had the old revision, so it has no earlier result.
	noErr(t, os.WriteFile(filepath.Join(fixture.work, "second.txt"), []byte("second\n"), 0o600))
	apiRunGit(t, fixture.work, "add", ".")
	apiRunGit(t, fixture.work, "commit", "-m", "second")
	apiRunGit(t, fixture.work, "push", "origin", "HEAD:refs/heads/feature")
	moved := apiRequest(t, http.MethodPost, prEndpoint, map[string]any{
		"title": "Moved feature", "source_branch": "feature", "target_branch": "main",
	}, "", "")
	if moved.StatusCode != http.StatusOK {
		t.Fatalf("second create status=%d error=%q", moved.StatusCode, apiErrorCode(t, moved))
	}
	movedView := decodeAPISuccess(t, moved)
	movedNumber := itoa(movedView.PullRequest.Number)
	movedShown := apiRequest(t, http.MethodGet, prEndpoint+"/"+movedNumber, nil, "", "")
	movedChecks := decodeAPISuccess(t, movedShown).PullRequest.Checks
	if movedChecks.Status != "absent" || movedChecks.Stale || movedChecks.RevisionOID != "" || movedChecks.TestedCommit {
		t.Fatalf("moved head checks=%+v", movedChecks)
	}
}

func attemptUploadBody(revision, worktree, status string) map[string]any {
	return attemptUploadBodyWithID(revision, worktree, status, "0123456789abcdef0123456789abcdef")
}

func attemptUploadBodyWithID(revision, worktree, status, attemptID string) map[string]any {
	start := time.Now().UTC().Add(-time.Second)
	finish := time.Now().UTC()
	exit := 1
	if status == state.AttemptPassed {
		exit = 0
	}
	return map[string]any{
		"attempt_id": attemptID, "revision_oid": revision, "worktree_state": worktree,
		"checks":      []map[string]string{{"name": "unit", "command": "go test ./..."}},
		"started_at":  start.Format(time.RFC3339Nano),
		"finished_at": finish.Format(time.RFC3339Nano),
		"timeout_ms":  600000, "output_limit_bytes": 65536,
		"results": []map[string]any{{
			"name": "unit", "command": "go test ./...", "status": status, "exit_code": exit,
			"duration_ms": 1000, "output_excerpt": "output",
		}},
		"log": "raw log output",
	}
}

// registrationBody keeps only the fields the first phase carries.
func registrationBody(body map[string]any) map[string]any {
	registration := map[string]any{
		"attempt_id": body["attempt_id"], "revision_oid": body["revision_oid"],
		"worktree_state": body["worktree_state"], "checks": body["checks"],
		"started_at": body["started_at"], "timeout_ms": body["timeout_ms"],
		"output_limit_bytes": body["output_limit_bytes"],
	}
	if cycle, ok := body["cycle_id"]; ok {
		registration["cycle_id"] = cycle
	}
	return registration
}

// completionBody keeps only the fields the second phase carries.
func completionBody(body map[string]any) map[string]any {
	completion := map[string]any{
		"results": body["results"], "finished_at": body["finished_at"],
		"worktree_state": body["worktree_state"], "log": body["log"],
	}
	if cancelled, ok := body["cancelled"]; ok {
		completion["cancelled"] = cancelled
	}
	return completion
}

func registerAttempt(t *testing.T, base, taskID, token string, body map[string]any) *http.Response {
	t.Helper()
	return checkRequest(t, http.MethodPost, base+"/tasks/"+taskID+"/attempts", registrationBody(body), token)
}

func completeAttempt(t *testing.T, base, taskID, attemptID, token string, body map[string]any) *http.Response {
	t.Helper()
	return checkRequest(t, http.MethodPost, base+"/tasks/"+taskID+"/attempts/"+attemptID+"/complete", completionBody(body), token)
}

// recordAttempt registers and completes one attempt through the API, which is
// what the helper does in two phases.
func recordAttempt(t *testing.T, base, taskID, token string, body map[string]any) *http.Response {
	t.Helper()
	registered := registerAttempt(t, base, taskID, token, body)
	content, err := io.ReadAll(registered.Body)
	registered.Body.Close()
	noErr(t, err)
	if registered.StatusCode != http.StatusOK {
		return &http.Response{StatusCode: registered.StatusCode, Header: registered.Header, Body: io.NopCloser(bytes.NewReader(content))}
	}
	var response checkapi.TaskResponse
	if err := json.Unmarshal(content, &response); err != nil || response.Attempt == nil {
		t.Fatalf("decode registration: %v", err)
	}
	return completeAttempt(t, base, taskID, response.Attempt.ID, token, body)
}

func checkRequest(t *testing.T, method, target string, value any, token string) *http.Response {
	t.Helper()
	bearer := ""
	if token != "" {
		bearer = "Bearer " + token
	}
	return sendJSON(t, method, target, value, header("Authorization", bearer))
}
func adminAPIRequest(t *testing.T, method, target string, value any, password string) *http.Response {
	t.Helper()
	return sendJSON(t, method, target, value, func(request *http.Request) { request.SetBasicAuth("admin", password) })
}
func adminSessionRequest(t *testing.T, method, target string, value any, csrf, password string) *http.Response {
	t.Helper()
	return sendJSON(t, method, target, value, adminCookieValue("admin-session"), header(csrfHeader, csrf), header(adminPasswordHeader, password))
}

// checkStatus reads and closes a response, returning its status and error
// code. A success response has an empty code.
func checkStatus(t *testing.T, response *http.Response) (int, string) {
	t.Helper()
	defer response.Body.Close()
	var envelope pullrequest.ErrorEnvelope
	noErrf(t, json.NewDecoder(response.Body).Decode(&envelope), "decode API response")
	return response.StatusCode, envelope.Error.Code
}

func decodeCheckJSON(t *testing.T, response *http.Response, destination any) {
	t.Helper()
	defer response.Body.Close()
	noErr(t, json.NewDecoder(response.Body).Decode(destination))
}

// TestCompletionWithTheWrongTaskURLDoesNotMutate covers a completion posted to
// the wrong task path. The attempt belongs to another task, so the request must
// fail before any durable effect.
func TestCompletionWithTheWrongTaskURLDoesNotMutate(t *testing.T) {
	fixture := newAPIFixture(t, false)
	ctx := context.Background()
	base, token := helperAPI(t, fixture, "laptop", time.Now())
	first := checkRequest(t, http.MethodPost, base+"/tasks", map[string]any{"title": "First"}, token)
	var firstResponse checkapi.TaskResponse
	decodeCheckJSON(t, first, &firstResponse)
	second := checkRequest(t, http.MethodPost, base+"/tasks", map[string]any{"title": "Second"}, token)
	var secondResponse checkapi.TaskResponse
	decodeCheckJSON(t, second, &secondResponse)

	body := attemptUploadBody(fixture.sourceOID, "clean", "passed")
	registered := registerAttempt(t, base, firstResponse.Task.ID, token, body)
	var registeredResponse checkapi.TaskResponse
	decodeCheckJSON(t, registered, &registeredResponse)

	wrong := completeAttempt(t, base, secondResponse.Task.ID, registeredResponse.Attempt.ID, token, body)
	if wrong.StatusCode != http.StatusNotFound || apiErrorCode(t, wrong) != "attempt_not_found" {
		t.Fatalf("wrong task URL status=%d error=%q", wrong.StatusCode, apiErrorCode(t, wrong))
	}
	stored, found, err := fixture.store.CheckAttemptByID(ctx, "project", registeredResponse.Attempt.ID)
	if err != nil || !found {
		t.Fatalf("stored attempt found=%v err=%v", found, err)
	}
	if stored.Status != state.AttemptPending {
		t.Fatalf("attempt status=%q after the rejected completion", stored.Status)
	}
	task, _, err := fixture.store.Task(ctx, "project", firstResponse.Task.ID)
	noErr(t, err)
	if task.LastAppliedAttemptID != "" || task.Status != state.TaskActive {
		t.Fatalf("task mutated by the rejected completion: %+v", task)
	}
}

// helperAPI serves fixture with one helper credential for "project" and
// returns the repository API base URL and that credential's token.
func helperAPI(t *testing.T, fixture apiFixture, label string, created time.Time) (base, token string) {
	t.Helper()
	server := serve(t, fixture.app.Handler())
	token = "synthetic-helper-token"
	hash := sha256.Sum256([]byte(token))
	_, _, err := fixture.store.CreateHelperCredential(context.Background(), "project", label, "", hash[:], created)
	noErr(t, err)
	return server.URL + "/api/v1/repositories/project", token
}

// createCheckTask creates a task through the helper API and returns its ID.
func createCheckTask(t *testing.T, base, token, title string) string {
	t.Helper()
	var response checkapi.TaskResponse
	decodeCheckJSON(t, checkRequest(t, http.MethodPost, base+"/tasks", map[string]any{"title": title}, token), &response)
	if response.Task == nil {
		t.Fatalf("task %q was not created", title)
	}
	return response.Task.ID
}
